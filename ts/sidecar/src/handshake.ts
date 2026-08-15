/**
 * Handshake, heartbeat and the protocol-level method handlers.
 */
import type { HelloParams, HelloResult } from "./types.gen.js";
import type { Peer } from "./peer.js";
import type { Publisher } from "./publisher.js";
import { Mux, type Request } from "./mux.js";
import { ProtocolError, Code } from "./errors.js";
import { PROTOCOL_V1 } from "./wire.js";

export type { HelloParams, HelloResult };

/** Method names of the v1 inventory (spec §7). */
export const Method = {
  Hello: "hello",
  Ping: "ping",
  Shutdown: "shutdown",
  GetHealth: "getHealth",
  Discover: "discover",
  Commission: "commission",
  Decommission: "decommission",
  ReadState: "readState",
  WriteState: "writeState",
  InvokeAction: "invokeAction",
  Subscribe: "subscribe",
  Unsubscribe: "unsubscribe",
  Resume: "resume",
  ConfigFlow: "configFlow",
  GetConfig: "getConfig",
  SetConfig: "setConfig",
} as const;

/** Core-side features advertised in the hello response. */
export const CoreFeature = {
  /** The core tracks seq and will call resume after a reconnect, so the
   * plugin must keep its ring buffer. */
  SeqReplay: "seq.replay",
  /** The core sends subscribe filters, so unmatched pushes may be dropped
   * before they are numbered. */
  TopicFiltering: "topic.filtering",
} as const;

/** Heartbeat intervals from spec §6.1. */
export const DEFAULT_PING_INTERVAL_MS = 10_000;
export const DEFAULT_PING_TIMEOUT_MS = 30_000;

/**
 * Checks a peer's declared protocol version.
 *
 * Only the major matters. Within one major the protocol changes additively, so
 * a newer peer sends fields an older one ignores and everything keeps working
 * (§5.2). Across majors nothing is assumed, which is why a mismatch is fatal
 * rather than a warning.
 */
export function negotiateProtocol(theirs: string, supported: readonly string[] = [PROTOCOL_V1]): void {
  if (!theirs) {
    throw new ProtocolError(Code.ProtocolUnsupported, "peer did not declare a protocol version");
  }
  if (!supported.includes(theirs)) {
    throw new ProtocolError(
      Code.ProtocolUnsupported,
      `peer speaks protocol ${theirs}; this build speaks ${supported.join(", ")}`,
    );
  }
}

/** Performs the handshake as the initiating side. */
export async function sendHello(peer: Peer, hello: HelloParams): Promise<HelloResult> {
  const params: HelloParams = { ...hello, protocol: hello.protocol || PROTOCOL_V1 };
  const result = await peer.call<HelloResult>(Method.Hello, params);
  negotiateProtocol(result.protocol);

  peer.setSession({
    session: result.session,
    peerName: result.core,
    peerVersion: result.core,
    capabilities: [...(params.capabilities ?? [])],
    features: [...(result.features ?? [])],
  });
  return result;
}

/**
 * Answers a hello on the accepting side.
 *
 * The protocol check happens before fn is called, so a handler never has to
 * think about version negotiation.
 */
export function helloHandler(
  fn: (hello: HelloParams) => HelloResult | Promise<HelloResult>,
): (req: Request) => Promise<HelloResult> {
  return async (req: Request) => {
    const hello = req.bind<HelloParams>();
    negotiateProtocol(hello.protocol);

    const result = await fn(hello);
    const filled: HelloResult = {
      ...result,
      protocol: result.protocol || PROTOCOL_V1,
      session: result.session || `sess_${Math.random().toString(36).slice(2, 12)}`,
    };

    req.peer.setSession({
      session: filled.session,
      peerName: hello.plugin,
      peerVersion: hello.version,
      capabilities: [...(hello.capabilities ?? [])],
      features: [...(filled.features ?? [])],
    });
    return filled;
  };
}

/** Answers heartbeats. */
export const pingHandler = () => ({ pong: true as const });

/**
 * The subscribe / unsubscribe / resume handlers, backed by a publisher.
 *
 * Pure protocol plumbing with no plugin-specific content, so every plugin
 * would otherwise write the same three — and would have to get the resume
 * ordering right, which is the fiddly part.
 */
export function subscriptionHandlers(pub: Publisher): Mux {
  return new Mux()
    .handle(Method.Subscribe, (req: Request) => {
      const { topics } = req.bind<{ topics?: string[] }>();
      pub.setFilters(topics ?? []);
      return { subscribed: true };
    })
    .handle(Method.Unsubscribe, () => {
      pub.setFilters([]);
      // The counter is deliberately not reset: a later subscribe continues
      // where this left off, so a resume across the quiet period lines up.
      return { unsubscribed: true };
    })
    .handle(Method.Resume, (req: Request) => {
      const { since_seq: since } = req.bind<{ since_seq?: number }>();
      return pub.resume(since ?? 0);
    });
}

export interface HeartbeatOptions {
  intervalMs?: number;
  timeoutMs?: number;
}

/**
 * Pings the far side on an interval and closes the peer if a pong does not
 * come back in time. Returns a function that stops the loop.
 *
 * Over a unix socket a peer whose process dies usually EOFs on its own, so
 * this matters less than it did over TCP. What it still catches is the harder
 * case: a process that is alive, holds the socket open, and has stopped making
 * progress. To a blocked read that is indistinguishable from a quiet house.
 */
export function startHeartbeat(peer: Peer, opts: HeartbeatOptions = {}): () => void {
  const intervalMs = opts.intervalMs ?? DEFAULT_PING_INTERVAL_MS;
  const timeoutMs = opts.timeoutMs ?? DEFAULT_PING_TIMEOUT_MS;

  const timer = setInterval(() => {
    void peer.call(Method.Ping, {}, { timeoutMs }).catch(() => {
      if (peer.isClosed) return;
      // Close rather than shut down politely: the far side is by definition
      // not answering, so waiting for it to acknowledge anything would block
      // on exactly the thing that is already broken.
      clearInterval(timer);
      void peer.close();
    });
  }, intervalMs);
  timer.unref?.();

  void peer.closed.then(() => clearInterval(timer));
  return () => clearInterval(timer);
}
