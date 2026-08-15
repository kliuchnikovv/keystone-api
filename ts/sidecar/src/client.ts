/**
 * The core-side role wrapper.
 */
import type { Socket } from "node:net";

import { Peer, type Logger, type Push } from "./peer.js";
import { Mux } from "./mux.js";
import { NotConnectedError } from "./errors.js";
import {
  CoreFeature,
  Method,
  helloHandler,
  startHeartbeat,
  type HeartbeatOptions,
} from "./handshake.js";
import { dialUnix } from "./transport.js";
import { PROTOCOL_V1 } from "./wire.js";
import type { CoreFeature as CoreFeatureId, ResumeResult } from "./types.gen.js";

export interface CoreOptions {
  /** Identifies the core in the handshake. */
  version?: string;
  /** Advertised to the plugin. Defaults to what this implementation actually
   * does.
   *
   * Closed to the known set, unlike plugin capabilities: a plugin must not
   * have to guess what an unknown core feature implies about its own
   * obligations. */
  features?: CoreFeatureId[];
  /** Answers requests from the plugin. Optional: v1 defines no
   * plugin-to-core methods beyond hello, which is handled internally. */
  handler?: Mux;
  onPush?: (push: Push) => void;
  /**
   * The sequence number this caller has already processed.
   *
   * Pass the previous connection's `lastSeq` when re-dialling. Unlike the Go
   * Client, this class does not own reconnection — a Node core supervises its
   * own child process and re-dials itself — so without carrying the watermark
   * forward the new connection starts at 0, resume asks for everything the
   * plugin still has, and every replayed push is delivered a second time.
   * Automations would re-fire for events from before the drop.
   */
  initialSeq?: number;
  heartbeat?: HeartbeatOptions;
  logger?: Logger;
}

/**
 * The core's end of a plugin connection.
 *
 * Unlike the Go Client this does not own reconnection — Node callers
 * supervise their own child processes, and re-dialling is a one-liner. What it
 * does own is the sequence bookkeeping: replayed duplicates are dropped, and
 * the clamp after a plugin restart is applied.
 */
export class CoreClient {
  #peer: Peer;
  #lastSeq: number;
  #filters: string[] = [];
  #stopHeartbeat: (() => void) | undefined;

  private constructor(peer: Peer, initialSeq: number) {
    this.#peer = peer;
    this.#lastSeq = initialSeq;
  }

  static async connect(path: string, opts: CoreOptions = {}): Promise<CoreClient> {
    const socket: Socket = await dialUnix(path);

    const mux = new Mux();
    if (opts.handler) {
      for (const method of opts.handler.methods) {
        mux.handle(method, (req) => opts.handler!.dispatch(req));
      }
    }
    mux.handle(
      Method.Hello,
      helloHandler(() => ({
        protocol: PROTOCOL_V1,
        core: opts.version ?? "0.0.0",
        session: `sess_${Math.random().toString(36).slice(2, 12)}`,
        features: opts.features ?? [CoreFeature.SeqReplay, CoreFeature.TopicFiltering],
      })),
    );

    let client!: CoreClient;
    const peer = new Peer(socket, {
      handler: mux,
      ...(opts.logger ? { logger: opts.logger } : {}),
      onPush: (push) => client.#acceptPush(push, opts.onPush),
    });
    client = new CoreClient(peer, opts.initialSeq ?? 0);

    void peer.serve();

    // The plugin greets first, so wait for its hello to land before returning
    // — a caller that immediately subscribes should not race the handshake.
    await waitFor(() => peer.session !== undefined, 30_000, "the plugin to send hello");

    client.#stopHeartbeat = startHeartbeat(peer, opts.heartbeat ?? {});
    return client;
  }

  get peer(): Peer {
    return this.#peer;
  }

  get session() {
    return this.#peer.session;
  }

  get lastSeq(): number {
    return this.#lastSeq;
  }

  async call<T = unknown>(method: string, params?: unknown): Promise<T> {
    if (this.#peer.isClosed) throw new NotConnectedError();
    return this.#peer.call<T>(method, params);
  }

  /** Sets the push filter set, remembering it so it can be restored after a
   * reconnect. */
  async subscribe(...filters: string[]): Promise<void> {
    this.#filters = [...filters];
    await this.call(Method.Subscribe, { topics: filters });
  }

  get filters(): readonly string[] {
    return [...this.#filters];
  }

  /**
   * Asks the plugin to replay from the last sequence accepted, and clamps if
   * the plugin restarted.
   */
  async resume(): Promise<ResumeResult> {
    const since = this.#lastSeq;
    const res = await this.call<ResumeResult>(Method.Resume, { since_seq: since });

    // The clamp. If the plugin's counter is behind where we think we are, the
    // plugin restarted and its numbering began again — the seq values we hold
    // refer to a previous run's events. Without this, every push from the
    // restarted plugin looks like a duplicate we already have, and we discard
    // all of them until the counter catches up. For a plugin restarted after a
    // week that is never: the house simply stops updating.
    if (typeof res.seq === "number" && res.seq < since) {
      this.#lastSeq = res.seq;
    }
    return res;
  }

  async close(): Promise<void> {
    this.#stopHeartbeat?.();
    await this.#peer.close();
  }

  #acceptPush(push: Push, forward: ((p: Push) => void) | undefined): void {
    // resume deliberately over-sends rather than risk under-sending, so this
    // is the normal path after a reconnect, not an error. Forwarding a
    // duplicate would re-fire automations for events from before the drop.
    if (push.seq <= this.#lastSeq) return;
    this.#lastSeq = push.seq;
    forward?.(push);
  }
}

/** Connects to a plugin socket. */
export function connectCore(path: string, opts: CoreOptions = {}): Promise<CoreClient> {
  return CoreClient.connect(path, opts);
}

async function waitFor(cond: () => boolean, timeoutMs: number, what: string): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (cond()) return;
    await new Promise((r) => setTimeout(r, 1));
  }
  throw new Error(`sidecar: timed out waiting for ${what}`);
}
