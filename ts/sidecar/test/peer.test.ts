import { afterEach, describe, expect, it, vi } from "vitest";

import { Peer, Mux, type Push } from "../src/peer.js";
import { ProtocolError, Code, NotConnectedError, isRetryable, errors } from "../src/errors.js";
import { socketPair, type SocketPair } from "../src/testing.js";

// Real connected unix sockets rather than in-memory streams. The peer runtime
// cannot tell the difference, but the tests exercise the same code path
// production does — including how close and destroy actually behave.
const open: SocketPair[] = [];

async function pipePair(): Promise<[SocketPair["a"], SocketPair["b"]]> {
  const pair = await socketPair();
  open.push(pair);
  return [pair.a, pair.b];
}

async function pipePeers(
  aOpts: ConstructorParameters<typeof Peer>[1] = {},
  bOpts: ConstructorParameters<typeof Peer>[1] = {},
): Promise<[Peer, Peer, () => Promise<void>]> {
  const [ca, cb] = await pipePair();
  const a = new Peer(ca, aOpts);
  const b = new Peer(cb, bOpts);
  void a.serve();
  void b.serve();
  return [a, b, async () => {
    await a.close();
    await b.close();
  }];
}

afterEach(async () => {
  for (const pair of open.splice(0)) await pair.close();
});

describe("Peer.call", () => {
  it("round-trips a method call", async () => {
    const server = new Mux().handle("readState", (req) => {
      const { deviceRef } = req.bind<{ deviceRef: string }>();
      if (deviceRef !== "matter:3") throw errors.deviceNotFound();
      return { value: true };
    });

    const [client, , stop] = await pipePeers({}, { handler: server });
    try {
      const res = await client.call<{ value: boolean }>("readState", { deviceRef: "matter:3" });
      expect(res.value).toBe(true);
    } finally {
      await stop();
    }
  });

  it("multiplexes concurrent calls", async () => {
    // Many calls in flight on one connection, each matched to its own reply by
    // id. Without it every device operation would serialise behind the
    // slowest one.
    const server = new Mux().handle("echo", async (req) => {
      const { n } = req.bind<{ n: number }>();
      // Stagger the replies so they cannot come back in request order.
      await new Promise((r) => setTimeout(r, 20 - n));
      return { n };
    });

    const [client, , stop] = await pipePeers({}, { handler: server });
    try {
      const results = await Promise.all(
        Array.from({ length: 20 }, (_, n) => client.call<{ n: number }>("echo", { n })),
      );
      expect(results.map((r) => r.n)).toEqual(Array.from({ length: 20 }, (_, n) => n));
    } finally {
      await stop();
    }
  });

  it("surfaces a typed error with its code and details", async () => {
    const server = new Mux().handle("readState", () => {
      throw new ProtocolError(Code.DeviceOffline, "asleep", {
        details: { deviceRef: "matter:3" },
      });
    });

    const [client, , stop] = await pipePeers({}, { handler: server });
    try {
      await expect(client.call("readState")).rejects.toMatchObject({
        code: Code.DeviceOffline,
        details: { deviceRef: "matter:3" },
      });

      // The code survives the round trip, which is what lets the core decide
      // to back off rather than surface a failure to the user.
      const err = await client.call("readState").catch((e) => e);
      expect(isRetryable(err)).toBe(true);
    } finally {
      await stop();
    }
  });

  it("answers an unknown method rather than ignoring it", async () => {
    // The caller is blocked on a reply for this id. Silence would turn "I do
    // not implement that" into a timeout that reads like a dead plugin.
    const [client, , stop] = await pipePeers({}, { handler: new Mux() });
    try {
      await expect(client.call("noSuchMethod", {}, { timeoutMs: 2000 })).rejects.toMatchObject({
        code: Code.ProtocolUnsupported,
      });
    } finally {
      await stop();
    }
  });

  it("fails a call that throws inside a handler without killing the connection", async () => {
    const server = new Mux()
      .handle("boom", () => {
        const bad = undefined as unknown as { x: number };
        return bad.x; // throws
      })
      .handle("ping", () => ({ pong: true }));

    const [client, , stop] = await pipePeers({}, { handler: server });
    try {
      await expect(client.call("boom")).rejects.toMatchObject({ code: Code.PluginInternal });
      // And the connection still works.
      await expect(client.call("ping")).resolves.toEqual({ pong: true });
    } finally {
      await stop();
    }
  });

  it("fails in-flight calls immediately when the peer closes", async () => {
    // Without this every in-flight call would block for its full timeout after
    // the connection was already known to be gone.
    const release = new Promise(() => {});
    const server = new Mux().handle("slow", () => release);

    const [client, , stop] = await pipePeers({ callTimeoutMs: 30_000 }, { handler: server });
    try {
      const calls = Array.from({ length: 10 }, () => client.call("slow"));
      await new Promise((r) => setTimeout(r, 50));
      await client.close();

      const results = await Promise.allSettled(calls);
      for (const r of results) {
        expect(r.status).toBe("rejected");
        expect((r as PromiseRejectedResult).reason).toBeInstanceOf(NotConnectedError);
      }
    } finally {
      await stop();
    }
  });

  it("rejects a call made after close", async () => {
    const [client, , stop] = await pipePeers({}, { handler: new Mux() });
    await client.close();
    await expect(client.call("ping")).rejects.toBeInstanceOf(NotConnectedError);
    await stop();
  });
});

describe("Peer pushes", () => {
  it("delivers a push that matches the subscription", async () => {
    const received: Push[] = [];
    const [, server, stop] = await pipePeers(
      { onPush: (p) => received.push(p) },
      { handler: new Mux() },
    );

    try {
      // Deny-by-default: nothing arrives until the receiver subscribes.
      server.publisher.setFilters(["state.**"]);
      server.publish("state.matter:3.onoff.value", { value: true, origin: "device_report" });

      await vi.waitFor(() => expect(received).toHaveLength(1));
      expect(received[0]!.topic).toBe("state.matter:3.onoff.value");
      expect(received[0]!.seq).toBe(1);
      expect(received[0]!.ts).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/);
    } finally {
      await stop();
    }
  });

  it("does not number a push the filter rejected", async () => {
    // The decision that makes gap detection trustworthy. If filtered-out
    // pushes consumed seq numbers, the receiver would see permanent gaps for
    // topics it deliberately never subscribed to, and could no longer tell
    // "you missed something" from "you did not want that".
    const received: Push[] = [];
    const [, server, stop] = await pipePeers(
      { onPush: (p) => received.push(p) },
      { handler: new Mux() },
    );

    try {
      server.publisher.setFilters(["state.**"]);
      server.publish("state.matter:3.onoff.value", { value: true });
      server.publish("plugin.log.debug", { message: "noise" });
      server.publish("state.matter:3.onoff.value", { value: false });

      await vi.waitFor(() => expect(received).toHaveLength(2));
      expect(received.map((p) => p.seq)).toEqual([1, 2]);
      expect(server.publisher.dropped).toBe(1);
    } finally {
      await stop();
    }
  });

  it("lets a handler push on the same connection while answering", async () => {
    // Symmetry made usable: this is how commissioning reports progress rather
    // than leaving the user watching a spinner.
    const progress: Push[] = [];
    const [client, , stop] = await pipePeers(
      { onPush: (p) => progress.push(p) },
      {
        handler: new Mux().handle("commission", (req) => {
          req.peer.publisher.setFilters(["commissioning.**"]);
          for (const stage of ["discovered", "pase_established", "operational"]) {
            req.peer.publish("commissioning.sess_1.progress", { stage });
          }
          return { deviceRef: "matter:9" };
        }),
      },
    );

    try {
      const res = await client.call<{ deviceRef: string }>("commission");
      expect(res.deviceRef).toBe("matter:9");

      await vi.waitFor(() => expect(progress).toHaveLength(3));
      expect(progress.map((p) => (p.payload as { stage: string }).stage)).toEqual([
        "discovered",
        "pase_established",
        "operational",
      ]);
    } finally {
      await stop();
    }
  });
});

describe("Peer robustness", () => {
  it("survives a malformed frame", async () => {
    // One bad line from a buggy peer must not take out a connection carrying
    // every device's state.
    const [ca, cb] = await pipePair();
    const server = new Peer(cb, {
      handler: new Mux().handle("ping", () => ({ pong: true })),
    });
    void server.serve();

    try {
      ca.write("this is not json\n");
      ca.write('{"type":"response","id":"nobody-waiting"}\n');
      ca.write('{"type":"request","id":"1","method":"ping"}\n');

      const line = await new Promise<string>((resolve) => {
        let buf = "";
        ca.on("data", (chunk: Buffer) => {
          buf += chunk.toString();
          const nl = buf.indexOf("\n");
          if (nl >= 0) resolve(buf.slice(0, nl));
        });
      });

      expect(JSON.parse(line)).toMatchObject({ type: "response", id: "1", result: { pong: true } });
    } finally {
      await server.close();
    }
  });

  it("encodes an empty handler result as an object, not null", async () => {
    // "result":{} reads better than "result":null in a transcript, and matches
    // what the schemas describe for the methods that return nothing.
    const [client, , stop] = await pipePeers(
      {},
      { handler: new Mux().handle("decommission", () => undefined) },
    );
    try {
      await expect(client.call("decommission")).resolves.toEqual({});
    } finally {
      await stop();
    }
  });
});
