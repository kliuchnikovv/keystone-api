/**
 * The two role wrappers, end to end over a real unix socket.
 */
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { afterEach, describe, expect, it, vi } from "vitest";

import { Mux } from "../src/mux.js";
import { runPlugin, type RunningPlugin } from "../src/plugin.js";
import { connectCore, type CoreClient } from "../src/client.js";
import type { Peer, Push } from "../src/peer.js";

const running: { plugin?: RunningPlugin; client?: CoreClient; dir?: string }[] = [];

afterEach(async () => {
  for (const r of running.splice(0)) {
    await r.client?.close();
    await r.plugin?.stop();
    if (r.dir) await rm(r.dir, { recursive: true, force: true });
  }
});

async function socketPath(): Promise<{ path: string; dir: string }> {
  // Short path: unix socket names are capped around 104 bytes on darwin.
  const dir = await mkdtemp(join(tmpdir(), "ks"));
  return { path: join(dir, "p.sock"), dir };
}

describe("runPlugin and connectCore", () => {
  it("completes the handshake and answers a method", async () => {
    const { path, dir } = await socketPath();

    const plugin = await runPlugin({
      name: "matter",
      version: "1.2.0",
      capabilities: ["realtime.state-stream", "commission.setup-code"],
      socketPath: path,
      handler: new Mux().handle("discover", () => [
        { deviceRef: "matter:3", type: "light" },
      ]),
    });

    const client = await connectCore(path, { version: "0.5.2" });
    running.push({ plugin, client, dir });

    // The plugin greets first, and the core records what it said.
    expect(client.session?.peerName).toBe("matter");
    expect(client.session?.peerVersion).toBe("1.2.0");
    expect(client.session?.capabilities).toContain("realtime.state-stream");

    const devices = await client.call<{ deviceRef: string }[]>("discover");
    expect(devices).toHaveLength(1);
    expect(devices[0]!.deviceRef).toBe("matter:3");
  });

  it("delivers pushes after a subscribe", async () => {
    const { path, dir } = await socketPath();

    let peer: Peer | undefined;
    const plugin = await runPlugin({
      name: "matter",
      socketPath: path,
      onConnect: (p) => {
        peer = p;
      },
    });

    const received: Push[] = [];
    const client = await connectCore(path, {
      version: "0.5.2",
      onPush: (p) => received.push(p),
    });
    running.push({ plugin, client, dir });

    await client.subscribe("state.**", "lifecycle.**");
    await vi.waitFor(() => expect(peer).toBeDefined());

    peer!.publish("state.matter:3.onoff.value", { value: true, origin: "device_report" });

    await vi.waitFor(() => expect(received).toHaveLength(1));
    expect(received[0]!.topic).toBe("state.matter:3.onoff.value");
    expect(received[0]!.seq).toBe(1);
    expect(client.lastSeq).toBe(1);
  });

  it("replays what was missed while disconnected", async () => {
    // The publisher belongs to the plugin, not to any connection, so pushes
    // made with nothing attached land in the ring buffer and are replayed on
    // resume. A per-connection buffer would be empty at exactly this moment.
    const { path, dir } = await socketPath();

    const plugin = await runPlugin({ name: "matter", socketPath: path });

    const first: Push[] = [];
    const client1 = await connectCore(path, { version: "0.5.2", onPush: (p) => first.push(p) });
    await client1.subscribe("state.**");

    plugin.publisher.publish("state.matter:3.onoff.value", { n: 1 });
    await vi.waitFor(() => expect(first).toHaveLength(1));

    await client1.close();
    await vi.waitFor(() => expect(plugin.publisher.filters.length).toBeGreaterThan(0));

    // Publish with nothing attached. These buffer rather than reach anyone.
    for (const n of [2, 3, 4]) {
      try {
        plugin.publisher.publish("state.matter:3.onoff.value", { n });
      } catch {
        // NotConnectedError is expected and is exactly what we are relying on:
        // the frame is buffered before the write is attempted.
      }
    }

    const second: Push[] = [];
    const client2 = await connectCore(path, { version: "0.5.2", onPush: (p) => second.push(p) });
    running.push({ plugin, client: client2, dir });

    await client2.subscribe("state.**");
    const res = await client2.resume();

    expect(res.resumed).toBe(true);
    await vi.waitFor(() => expect(second.map((p) => p.seq)).toEqual([1, 2, 3, 4]));
  });

  it("clamps its sequence when the plugin restarts", async () => {
    // A restarted plugin numbers from 1 again, so its seq values now refer to
    // a different run's events. A core that carries its old high-water mark
    // across the re-dial — which it must, or replayed pushes are delivered
    // twice — would then discard every push from the new run as a duplicate.
    // For a plugin restarted after a week of uptime that is forever: the house
    // stops updating and nothing errors.
    const { path, dir } = await socketPath();

    const first = await runPlugin({ name: "restarter", socketPath: path });
    const pushes: Push[] = [];
    const client = await connectCore(path, { version: "0.5.2", onPush: (p) => pushes.push(p) });
    await client.subscribe("state.**");

    for (let n = 1; n <= 5; n++) {
      first.publisher.publish("state.matter:3.onoff.value", { n });
    }
    await vi.waitFor(() => expect(pushes).toHaveLength(5));
    expect(client.lastSeq).toBe(5);

    const watermark = client.lastSeq;
    await client.close();
    await first.stop();

    // A fresh plugin on the same socket — its counter is back to zero.
    const second = await runPlugin({ name: "restarter", socketPath: path });
    const afterRestart: Push[] = [];
    const client2 = await connectCore(path, {
      version: "0.5.2",
      initialSeq: watermark,
      onPush: (p) => afterRestart.push(p),
    });
    running.push({ plugin: second, client: client2, dir });

    expect(client2.lastSeq).toBe(5);

    await client2.subscribe("state.**");
    const res = await client2.resume();

    // The plugin reports a position behind ours, so we clamp to it.
    expect(res.seq).toBe(0);
    expect(client2.lastSeq).toBe(0);

    second.publisher.publish("state.matter:3.onoff.value", { n: 1 });
    await vi.waitFor(() => expect(afterRestart).toHaveLength(1));
    expect(afterRestart[0]!.seq).toBe(1);
  });

  it("would discard everything after a restart without the clamp", async () => {
    // The negative control for the test above: skip resume, and the stale
    // watermark silently swallows every push from the new run.
    const { path, dir } = await socketPath();

    const plugin = await runPlugin({ name: "restarter", socketPath: path });
    const received: Push[] = [];
    const client = await connectCore(path, {
      version: "0.5.2",
      initialSeq: 5,
      onPush: (p) => received.push(p),
    });
    running.push({ plugin, client, dir });

    await client.subscribe("state.**");
    plugin.publisher.publish("state.matter:3.onoff.value", { n: 1 });
    await new Promise((r) => setTimeout(r, 100));

    expect(received).toHaveLength(0);
  });

  it("drops replayed duplicates", async () => {
    // resume deliberately over-sends rather than risk under-sending, so this
    // is the normal path after a reconnect. Forwarding a duplicate would
    // re-fire automations for events from before the drop.
    const { path, dir } = await socketPath();

    const plugin = await runPlugin({ name: "matter", socketPath: path });
    const received: Push[] = [];
    const client = await connectCore(path, { version: "0.5.2", onPush: (p) => received.push(p) });
    running.push({ plugin, client, dir });

    await client.subscribe("state.**");
    for (let n = 1; n <= 3; n++) {
      plugin.publisher.publish("state.matter:3.onoff.value", { n });
    }
    await vi.waitFor(() => expect(received).toHaveLength(3));

    // Ask for a replay of ground already covered.
    await client.resume();
    await new Promise((r) => setTimeout(r, 50));

    expect(received.map((p) => p.seq)).toEqual([1, 2, 3]);
  });

  it("refuses to start without a name", async () => {
    await expect(runPlugin({ name: "" })).rejects.toThrow(/name is required/);
  });
});
