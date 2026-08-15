/**
 * The push replay buffer (spec §10).
 */
import type { PushFrame } from "./types.gen.js";

/**
 * How many pushes a sender keeps for replay (spec §10.1).
 *
 * A trade-off between memory and how long a reconnect can take before history
 * is lost: a house producing a push a second gets about sixteen minutes of
 * buffer, which comfortably covers a plugin restart. A house producing a
 * hundred a second gets ten seconds, which does not — see `since`, which
 * reports that case rather than hiding it.
 */
export const DEFAULT_RING_CAPACITY = 1000;

export interface SinceResult {
  frames: PushFrame[];
  /**
   * True when the requested position had already been overwritten, so some
   * pushes are gone for good. A caller seeing this should re-read state rather
   * than continue with a picture it believes is complete (§10.2).
   */
  gap: boolean;
}

/**
 * Holds the most recent pushes for replay after a reconnect.
 *
 * It belongs to the sender and outlives any single connection. That placement
 * is the whole point: a buffer scoped to a connection would be empty at
 * exactly the moment replay is needed.
 */
export class RingBuffer {
  #frames: (PushFrame | undefined)[];
  #next = 0;
  #count = 0;

  constructor(capacity: number = DEFAULT_RING_CAPACITY) {
    this.#frames = new Array(capacity > 0 ? capacity : DEFAULT_RING_CAPACITY);
  }

  append(frame: PushFrame): void {
    this.#frames[this.#next] = frame;
    this.#next = (this.#next + 1) % this.#frames.length;
    if (this.#count < this.#frames.length) this.#count++;
  }

  get length(): number {
    return this.#count;
  }

  get capacity(): number {
    return this.#frames.length;
  }

  /** Lowest seq still held, or 0 when empty. */
  get oldest(): number {
    if (this.#count === 0) return 0;
    const i = (this.#next - this.#count + this.#frames.length) % this.#frames.length;
    return this.#frames[i]?.seq ?? 0;
  }

  /** Highest seq held, or 0 when empty. */
  get newest(): number {
    if (this.#count === 0) return 0;
    const i = (this.#next - 1 + this.#frames.length) % this.#frames.length;
    return this.#frames[i]?.seq ?? 0;
  }

  /**
   * Everything newer than sinceSeq, in order, plus whether anything between
   * that position and the buffer was lost.
   *
   * sinceSeq of 0 means "everything you still have", and is never a gap: the
   * caller is not claiming to have seen anything.
   */
  since(sinceSeq: number): SinceResult {
    if (this.#count === 0) return { frames: [], gap: false };

    const gap = sinceSeq > 0 && this.oldest > sinceSeq + 1;

    const frames: PushFrame[] = [];
    const start = (this.#next - this.#count + this.#frames.length) % this.#frames.length;
    for (let i = 0; i < this.#count; i++) {
      const f = this.#frames[(start + i) % this.#frames.length];
      if (f && f.seq > sinceSeq) frames.push(f);
    }
    return { frames, gap };
  }

  /** Empties the buffer. For a genuine restart of the sending side: stale
   * frames must not be replayable under numbers that now mean other events. */
  reset(): void {
    this.#frames = new Array(this.#frames.length);
    this.#next = 0;
    this.#count = 0;
  }
}
