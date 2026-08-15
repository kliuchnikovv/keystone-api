/**
 * The outbound push path: sequence counter, ring buffer, subscription filter.
 */
import type { Frame, PushFrame, ResumeResult } from "./types.gen.js";
import type { FrameWriter } from "./codec.js";
import { NotConnectedError, ProtocolError, Code } from "./errors.js";
import { DEFAULT_RING_CAPACITY, RingBuffer } from "./ring.js";
import { TopicMatcher } from "./topic.js";
import { isValidTopic, wireTime } from "./wire.js";

export type { ResumeResult };

export interface PublisherOptions {
  ringCapacity?: number;
  now?: () => Date;
}

/**
 * Owns everything about sending pushes.
 *
 * It outlives any single connection: `rebind` swaps in a new writer after a
 * reconnect, keeping the counter and the buffer. That is the entire reason
 * replay can work — a publisher scoped to a connection would have an empty
 * buffer at exactly the moment resume is called.
 */
export class Publisher {
  #writer: FrameWriter | undefined;
  #ring: RingBuffer;
  #matcher = new TopicMatcher();
  #seq = 0;
  #dropped = 0;
  #now: () => Date;

  constructor(writer?: FrameWriter, opts: PublisherOptions = {}) {
    this.#writer = writer;
    this.#ring = new RingBuffer(opts.ringCapacity ?? DEFAULT_RING_CAPACITY);
    this.#now = opts.now ?? (() => new Date());
  }

  /**
   * Emits a push, if the far side has subscribed to the topic.
   *
   * A push matching no filter is dropped and — importantly — is not numbered.
   * Numbering everything and then filtering would leave the receiver with
   * permanent seq gaps for topics it deliberately never asked for, and gap
   * detection (§10.2) could no longer distinguish "you missed something" from
   * "you did not want that". Since the receiver's only recovery from a real
   * gap is re-reading every device, false positives are expensive.
   *
   * Throws NotConnectedError when there is nothing to write to. The frame is
   * buffered first, so it is still replayable — which is the case replay
   * exists for.
   */
  publish(topic: string, payload: unknown): void {
    if (!isValidTopic(topic)) {
      throw new ProtocolError(Code.ValidationBadParams, `topic ${JSON.stringify(topic)} is malformed`);
    }
    if (!this.#matcher.match(topic)) {
      this.#dropped++;
      return;
    }

    this.#seq++;
    const frame: PushFrame = {
      type: "push",
      topic,
      seq: this.#seq,
      ts: wireTime(this.#now()),
      payload,
    } as PushFrame;

    // Buffer before writing: if the write fails because the connection just
    // died, the frame is still replayable once the far side reconnects.
    this.#ring.append(frame);

    if (!this.#writer) throw new NotConnectedError();
    this.#writer.write(frame as Frame);
  }

  /** Replaces the subscription filter set (spec §8.2). */
  setFilters(filters: readonly string[]): void {
    this.#matcher.set(filters);
  }

  get filters(): readonly string[] {
    return this.#matcher.filters;
  }

  get seq(): number {
    return this.#seq;
  }

  /** How many pushes the filter rejected. */
  get dropped(): number {
    return this.#dropped;
  }

  /** Points the publisher at a new connection, keeping counter and buffer. */
  rebind(writer: FrameWriter | undefined): void {
    this.#writer = writer;
  }

  /**
   * Replays everything newer than sinceSeq and reports what to expect.
   *
   * The buffered frames are written before this returns, so by the time the
   * caller sees the result the backlog is already on the wire ahead of it —
   * ordering the caller relies on to know when live traffic begins.
   */
  resume(sinceSeq: number): ResumeResult {
    const { frames, gap } = this.#ring.since(sinceSeq);
    const result: ResumeResult = {
      resumed: true,
      sending: frames.length,
      // The sender's position before this call. It is what lets the caller
      // detect a restart: if this comes back lower than the since_seq it
      // asked from, our counter went back to the beginning, and a caller that
      // does not clamp will discard every later push as a duplicate.
      seq: this.#seq,
      ...(gap ? { gap: true } : {}),
    };

    if (this.#writer) {
      for (const f of frames) this.#writer.write(f as Frame);
    }
    return result;
  }

  /** Restarts the counter and empties the buffer. */
  reset(): void {
    this.#seq = 0;
    this.#dropped = 0;
    this.#ring.reset();
  }
}
