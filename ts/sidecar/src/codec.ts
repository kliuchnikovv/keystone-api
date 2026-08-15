/**
 * NDJSON framing.
 */
import type { Readable, Writable } from "node:stream";

import type { Frame } from "./types.gen.js";
import { frameProblem } from "./wire.js";
import { ProtocolError, Code } from "./errors.js";

/**
 * Bounds a single frame. Far more than any legitimate one — the largest
 * realistic frame is a discover result for a house full of devices — and the
 * bound is what stops a peer that never sends a newline from growing the
 * buffer without limit.
 */
export const DEFAULT_MAX_LINE_BYTES = 1 << 20;

/**
 * Matter attribute values routinely contain BigInt — node ids, EUI64
 * addresses, 64-bit counters — and JSON.stringify throws on them outright.
 *
 * This is not hypothetical: unhandled, that single throw travelled up through
 * the emitter in the Matter sidecar and killed the whole realtime path on its
 * first event, over and over. It belongs in the shared kernel rather than in
 * each sidecar that rediscovers it.
 *
 * Values inside the safe-integer range become numbers so the Go side keeps
 * parsing them as numbers; anything larger becomes a string, because silently
 * rounding a 64-bit id is worse than changing its type.
 */
export function jsonReplacer(_key: string, value: unknown): unknown {
  if (typeof value !== "bigint") return value;
  return value >= BigInt(Number.MIN_SAFE_INTEGER) && value <= BigInt(Number.MAX_SAFE_INTEGER)
    ? Number(value)
    : value.toString();
}

/**
 * Serialises a frame, or returns undefined if it cannot be serialised.
 *
 * Never throws. Losing one update is bad; taking down the connection that
 * carries every other device's state is far worse.
 */
export function encodeFrame(frame: Frame): string | undefined {
  try {
    return JSON.stringify(frame, jsonReplacer);
  } catch {
    return undefined;
  }
}

/** Thrown for a frame over the size limit. Recoverable: the decoder skips to
 * the next newline, so one oversized frame does not end the session. */
export class LineTooLongError extends Error {
  constructor(readonly bytes: number) {
    super(`sidecar: frame exceeds the maximum line length (${bytes} bytes)`);
    this.name = "LineTooLongError";
  }
}

export interface DecoderOptions {
  maxLineBytes?: number;
  /** Called for a line that could not be decoded, instead of throwing. */
  onError?: (err: Error) => void;
}

/**
 * Incremental NDJSON decoder.
 *
 * Feed it chunks; it returns the complete frames in each. Splitting on
 * newlines rather than parsing incrementally is what keeps the stream true
 * NDJSON: a JSON parser would happily accept a value spread over several
 * lines, so a peer emitting pretty-printed JSON would appear to work while
 * producing a stream that breaks every other reader, `socat` included.
 * Debuggability is a stated goal of the protocol (§2), not a nicety.
 */
export class NdjsonDecoder {
  #buffer = "";
  #maxLineBytes: number;
  #onError: ((err: Error) => void) | undefined;

  constructor(opts: DecoderOptions = {}) {
    this.#maxLineBytes = opts.maxLineBytes ?? DEFAULT_MAX_LINE_BYTES;
    this.#onError = opts.onError;
  }

  /** Feeds a chunk and returns whatever complete frames it completed. */
  push(chunk: Buffer | string): Frame[] {
    this.#buffer += typeof chunk === "string" ? chunk : chunk.toString("utf8");

    const frames: Frame[] = [];
    let newline: number;
    while ((newline = this.#buffer.indexOf("\n")) >= 0) {
      const line = this.#buffer.slice(0, newline).replace(/\r$/, "");
      this.#buffer = this.#buffer.slice(newline + 1);
      const frame = this.#decodeLine(line);
      if (frame) frames.push(frame);
    }

    if (this.#buffer.length > this.#maxLineBytes) {
      // Drop the partial line and resynchronise on the next newline rather
      // than letting a peer that never terminates a frame grow this forever.
      this.#buffer = "";
      this.#report(new LineTooLongError(this.#maxLineBytes));
    }
    return frames;
  }

  /** Decodes anything left in the buffer at end of stream — a final frame
   * with no trailing newline, which is legal enough: the peer is closing and
   * this is the last thing it had to say. */
  flush(): Frame[] {
    const rest = this.#buffer.trim();
    this.#buffer = "";
    if (!rest) return [];
    const frame = this.#decodeLine(rest);
    return frame ? [frame] : [];
  }

  #decodeLine(line: string): Frame | undefined {
    if (!line.trim()) return undefined;

    let parsed: unknown;
    try {
      parsed = JSON.parse(line);
    } catch (err) {
      this.#report(
        new ProtocolError(Code.ProtocolMalformed, `frame is not valid JSON: ${String(err)}`),
      );
      return undefined;
    }

    const frame = parsed as Frame;
    const problem = frameProblem(frame);
    if (problem) {
      this.#report(new ProtocolError(Code.ProtocolMalformed, problem));
      return undefined;
    }
    return frame;
  }

  #report(err: Error): void {
    if (this.#onError) this.#onError(err);
    else throw err;
  }
}

/**
 * Writes frames as NDJSON.
 *
 * Every frame is serialised fully and written in one call. NDJSON has no
 * external framing, so two interleaved writes splice into a line that is not a
 * frame — and unlike WebSocket there is no resynchronisation point, so the
 * session is corrupt from then on.
 */
export class FrameWriter {
  #sink: Writable;
  #onDrop: ((frame: Frame) => void) | undefined;

  constructor(sink: Writable, opts: { onDrop?: (frame: Frame) => void } = {}) {
    this.#sink = sink;
    this.#onDrop = opts.onDrop;
  }

  /** Writes a frame. Returns false if it could not be serialised or the sink
   * is finished. */
  write(frame: Frame): boolean {
    const line = encodeFrame(frame);
    if (line === undefined) {
      this.#onDrop?.(frame);
      return false;
    }
    if (this.#sink.destroyed || this.#sink.writableEnded) return false;
    return this.#sink.write(line + "\n");
  }
}

/** Reads a stream as an async iterable of frames. */
export async function* decodeStream(
  src: Readable,
  opts: DecoderOptions = {},
): AsyncIterable<Frame> {
  const dec = new NdjsonDecoder(opts);
  for await (const chunk of src) {
    for (const frame of dec.push(chunk as Buffer)) yield frame;
  }
  for (const frame of dec.flush()) yield frame;
}
