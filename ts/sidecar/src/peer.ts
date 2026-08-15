/**
 * The symmetric protocol peer.
 */
import type { Duplex } from "node:stream";

import type { Frame, PushFrame, RequestFrame, ResponseFrame } from "./types.gen.js";
import { FrameWriter, NdjsonDecoder, type DecoderOptions } from "./codec.js";
import { NotConnectedError, ProtocolError, Code, toErrorBody } from "./errors.js";
import { Mux, Request, type Push } from "./mux.js";
import { Publisher } from "./publisher.js";
import { isPush, isRequest, isResponse } from "./wire.js";

export { Request, Mux };
export type { Push };

/** Bounds a request whose caller supplied no timeout. */
export const DEFAULT_CALL_TIMEOUT_MS = 30_000;

/** Minimal logger. Defaults to silence — a library that writes to stderr
 * uninvited is a nuisance in a process that has its own logging. */
export interface Logger {
  warn(msg: string, fields?: Record<string, unknown>): void;
  error(msg: string, fields?: Record<string, unknown>): void;
}

const SILENT: Logger = { warn: () => {}, error: () => {} };

export interface PeerOptions {
  /** Answers inbound requests. Without one, every request is rejected with
   * protocol.unsupported — correct for a peer that only consumes pushes. */
  handler?: Mux;
  /** Receives inbound pushes. */
  onPush?: (push: Push) => void;
  logger?: Logger;
  callTimeoutMs?: number;
  ringCapacity?: number;
  /**
   * An externally owned publisher to bind to this connection.
   *
   * A plugin passes the one that outlives its connections, so the seq counter
   * and the ring buffer survive a reconnect — which is what makes resume able
   * to replay anything. Without this the peer creates its own, which is right
   * for a single-connection peer.
   */
  publisher?: Publisher;
  maxLineBytes?: number;
  newId?: () => string;
  now?: () => Date;
}

/** Session facts settled by the handshake. */
export interface SessionInfo {
  session: string;
  peerName: string;
  peerVersion: string;
  capabilities: string[];
  features: string[];
}

interface Pending {
  resolve: (frame: ResponseFrame) => void;
  reject: (err: Error) => void;
  timer: NodeJS.Timeout | undefined;
}

let counter = 0;

/** ULID-shaped enough: unique per process and lexicographically sortable, so a
 * log sorted by id is in roughly causal order. */
function defaultId(): string {
  counter = (counter + 1) % Number.MAX_SAFE_INTEGER;
  return `${Date.now().toString(36)}${counter.toString(36).padStart(8, "0")}`;
}

/**
 * One end of a protocol connection.
 *
 * There is no client type and no server type. A Peer can send requests and
 * answer them; it can emit pushes and receive them. Which side does what is
 * policy, expressed by the options you pass, not by which class you chose.
 *
 * It takes a Duplex, so a unix socket, a socket pair and an in-memory stream
 * are all equally usable — tests run over the last, production over the first,
 * and neither the peer nor a handler can tell.
 */
export class Peer {
  #socket: Duplex;
  #writer: FrameWriter;
  #decoder: NdjsonDecoder;
  #opts: Required<Pick<PeerOptions, "callTimeoutMs" | "newId">> & PeerOptions;
  #log: Logger;

  #pending = new Map<string, Pending>();
  #publisher: Publisher;
  #ownsPublisher: boolean;

  #closed = false;
  #closedResolve!: () => void;
  readonly closed: Promise<void>;

  #session: SessionInfo | undefined;

  constructor(socket: Duplex, opts: PeerOptions = {}) {
    this.#socket = socket;
    this.#log = opts.logger ?? SILENT;
    this.#opts = {
      ...opts,
      callTimeoutMs: opts.callTimeoutMs ?? DEFAULT_CALL_TIMEOUT_MS,
      newId: opts.newId ?? defaultId,
    };

    this.#writer = new FrameWriter(socket, {
      onDrop: (frame) =>
        this.#log.error("sidecar: dropped an unserialisable frame", { frame: frame.type }),
    });

    const decoderOpts: DecoderOptions = {
      onError: (err) => this.#log.warn("sidecar: dropped a bad frame", { err: err.message }),
    };
    if (opts.maxLineBytes !== undefined) decoderOpts.maxLineBytes = opts.maxLineBytes;
    this.#decoder = new NdjsonDecoder(decoderOpts);

    if (opts.publisher) {
      this.#publisher = opts.publisher;
      this.#publisher.rebind(this.#writer);
      this.#ownsPublisher = false;
    } else {
      this.#publisher = new Publisher(this.#writer, {
        ...(opts.ringCapacity !== undefined ? { ringCapacity: opts.ringCapacity } : {}),
        ...(opts.now !== undefined ? { now: opts.now } : {}),
      });
      this.#ownsPublisher = true;
    }

    this.closed = new Promise<void>((resolve) => {
      this.#closedResolve = resolve;
    });

    // A socket error after we have decided to close is not actionable, and an
    // unhandled 'error' event would crash the host process. Composite streams
    // in particular emit an AbortError from destroy(). Swallow it once the
    // peer is closing; report it otherwise.
    socket.on("error", (err) => {
      if (this.#closed) return;
      this.#log.warn("sidecar: socket error", { err: String(err) });
    });
  }

  get publisher(): Publisher {
    return this.#publisher;
  }

  get session(): SessionInfo | undefined {
    return this.#session;
  }

  /** @internal — set by the handshake helpers. */
  setSession(s: SessionInfo): void {
    this.#session = s;
  }

  get isClosed(): boolean {
    return this.#closed;
  }

  /** Runs the read pump until the connection ends. */
  async serve(signal?: AbortSignal): Promise<void> {
    const onAbort = () => void this.close();
    signal?.addEventListener("abort", onAbort, { once: true });

    try {
      for await (const chunk of this.#socket) {
        for (const frame of this.#decoder.push(chunk as Buffer)) {
          this.#route(frame);
        }
      }
      for (const frame of this.#decoder.flush()) this.#route(frame);
    } catch (err) {
      if (!this.#closed) {
        this.#log.warn("sidecar: read failed", { err: String(err) });
      }
    } finally {
      signal?.removeEventListener("abort", onAbort);
      await this.close();
    }
  }

  #route(frame: Frame): void {
    if (isRequest(frame)) {
      void this.#serveRequest(frame);
      return;
    }

    if (isResponse(frame)) {
      const pending = this.#pending.get(frame.id);
      if (!pending) {
        // A reply to a call that already gave up, or a duplicate.
        this.#log.warn("sidecar: response with no waiter", { id: frame.id });
        return;
      }
      this.#pending.delete(frame.id);
      if (pending.timer) clearTimeout(pending.timer);
      pending.resolve(frame);
      return;
    }

    if (isPush(frame)) {
      const p = frame as PushFrame;
      this.#opts.onPush?.({
        topic: p.topic,
        seq: p.seq,
        ts: p.ts,
        payload: p.payload,
      });
    }
  }

  async #serveRequest(frame: RequestFrame): Promise<void> {
    const req = new Request(frame.id, frame.method, frame.params, this);

    let response: ResponseFrame;
    try {
      const handler = this.#opts.handler;
      if (!handler) {
        throw new ProtocolError(
          Code.ProtocolUnsupported,
          `method ${JSON.stringify(req.method)} is not implemented`,
        );
      }
      // A rejected handler fails its own call, not the connection. A plugin
      // author's undefined-property access should cost one operation, not
      // every other device in the house.
      const result = await handler.dispatch(req);
      response = { type: "response", id: req.id, result: result ?? {} } as ResponseFrame;
    } catch (err) {
      response = { type: "response", id: req.id, error: toErrorBody(err) } as ResponseFrame;
    }

    this.#writer.write(response as Frame);
  }

  /**
   * Sends a request and waits for its response.
   *
   * A failure reported by the far side rejects with a ProtocolError carrying
   * its code, so isRetryable works on it.
   */
  async call<T = unknown>(
    method: string,
    params?: unknown,
    opts: { timeoutMs?: number; signal?: AbortSignal } = {},
  ): Promise<T> {
    if (this.#closed) throw new NotConnectedError();

    const id = this.#opts.newId();
    const frame: RequestFrame = { type: "request", id, method } as RequestFrame;
    if (params !== undefined) (frame as { params?: unknown }).params = params;

    const promise = new Promise<ResponseFrame>((resolve, reject) => {
      const timeoutMs = opts.timeoutMs ?? this.#opts.callTimeoutMs;
      const timer = setTimeout(() => {
        this.#pending.delete(id);
        reject(new ProtocolError(Code.NetworkTimeout, `${method} timed out after ${timeoutMs}ms`));
      }, timeoutMs);
      // Do not hold the event loop open for an in-flight call.
      timer.unref?.();

      this.#pending.set(id, { resolve, reject, timer });

      opts.signal?.addEventListener(
        "abort",
        () => {
          this.#pending.delete(id);
          clearTimeout(timer);
          reject(new ProtocolError(Code.ValidationBadParams, `${method} was cancelled`));
        },
        { once: true },
      );
    });

    if (!this.#writer.write(frame as Frame)) {
      this.#pending.delete(id);
      throw new NotConnectedError();
    }

    const response = await promise;
    if (response.error) throw ProtocolError.fromBody(response.error);
    return response.result as T;
  }

  /** Emits a push, subject to the far side's subscription filter. */
  publish(topic: string, payload: unknown): void {
    this.#publisher.publish(topic, payload);
  }

  /** Shuts the peer down. Idempotent. */
  async close(): Promise<void> {
    if (this.#closed) return;
    this.#closed = true;

    // Wake every in-flight call. No more replies are coming, and leaving them
    // to time out individually would keep callers blocked for up to the call
    // timeout after we already knew the answer was never arriving.
    for (const [, pending] of this.#pending) {
      if (pending.timer) clearTimeout(pending.timer);
      pending.reject(new NotConnectedError());
    }
    this.#pending.clear();

    // Detach the publisher from this connection. A publish afterwards should
    // buffer for replay rather than write to a dead socket. If the publisher
    // belongs to the caller it keeps its counter and buffer for the next one.
    this.#publisher.rebind(undefined);
    try {
      this.#socket.destroy();
    } catch {
      // Already torn down. Nothing to do — close is idempotent by contract.
    }
    this.#closedResolve();
  }
}
