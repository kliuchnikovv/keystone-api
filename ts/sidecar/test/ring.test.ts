import { describe, expect, it } from "vitest";

import { RingBuffer, DEFAULT_RING_CAPACITY } from "../src/ring.js";
import type { PushFrame } from "../src/types.gen.js";

const push = (seq: number): PushFrame =>
  ({ type: "push", topic: "state.matter:3.onoff.value", seq, ts: "2026-08-15T21:04:11.123Z" }) as PushFrame;

const seqs = (frames: PushFrame[]) => frames.map((f) => f.seq);

describe("RingBuffer", () => {
  it("returns everything newer than the requested position", () => {
    const rb = new RingBuffer(10);
    for (let i = 1; i <= 5; i++) rb.append(push(i));

    expect(seqs(rb.since(0).frames)).toEqual([1, 2, 3, 4, 5]);
    expect(seqs(rb.since(3).frames)).toEqual([4, 5]);
    expect(seqs(rb.since(5).frames)).toEqual([]);
    // A caller ahead of the sender: nothing to send, and not an error. It
    // happens legitimately when the sender restarts.
    expect(seqs(rb.since(99).frames)).toEqual([]);
  });

  it("evicts the oldest when full", () => {
    const rb = new RingBuffer(3);
    for (let i = 1; i <= 5; i++) rb.append(push(i));

    expect(rb.length).toBe(3);
    expect(rb.oldest).toBe(3);
    expect(rb.newest).toBe(5);
    expect(seqs(rb.since(0).frames)).toEqual([3, 4, 5]);
  });

  it("reports a gap only when the caller's position was overwritten", () => {
    // The signal that matters. A caller resuming from a position already
    // overwritten has lost pushes for good, and must re-read state rather than
    // carry on believing its picture is complete (§10.2).
    const rb = new RingBuffer(3);
    for (let i = 1; i <= 5; i++) rb.append(push(i));
    // Holds 3, 4, 5.

    expect(rb.since(4).gap).toBe(false); // 4 is held, 5 follows contiguously
    expect(rb.since(3).gap).toBe(false); // 3 is held
    expect(rb.since(2).gap).toBe(false); // caller has 2, buffer starts at 3
    expect(rb.since(1).gap).toBe(true); //  caller has 1 but 2 was evicted
    expect(rb.since(0).gap).toBe(false); // claims nothing, so nothing is missing
  });

  it("is empty and gapless when fresh", () => {
    const rb = new RingBuffer(4);
    expect(rb.length).toBe(0);
    expect(rb.oldest).toBe(0);
    expect(rb.newest).toBe(0);
    expect(rb.since(0)).toEqual({ frames: [], gap: false });
  });

  it("defaults its capacity to the spec figure", () => {
    expect(DEFAULT_RING_CAPACITY).toBe(1000);
    expect(new RingBuffer(0).capacity).toBe(1000);
    expect(new RingBuffer(-1).capacity).toBe(1000);
  });

  it("is usable after a reset", () => {
    const rb = new RingBuffer(4);
    for (let i = 1; i <= 4; i++) rb.append(push(i));
    rb.reset();

    expect(rb.length).toBe(0);
    expect(seqs(rb.since(0).frames)).toEqual([]);

    rb.append(push(1));
    expect(seqs(rb.since(0).frames)).toEqual([1]);
  });

  it("holds its invariants across capacities and fill levels", () => {
    for (const capacity of [1, 2, 3, 7, 16, 100]) {
      for (const appended of [0, 1, 2, 5, 16, 101, 250]) {
        const rb = new RingBuffer(capacity);
        for (let i = 1; i <= appended; i++) rb.append(push(i));

        expect(rb.length).toBe(Math.min(appended, capacity));

        for (let since = 0; since <= appended + 1; since++) {
          const { frames, gap } = rb.since(since);

          // Everything returned is newer than the requested position, and
          // strictly increasing.
          let prev = 0;
          for (const f of frames) {
            expect(f.seq).toBeGreaterThan(since);
            expect(f.seq).toBeGreaterThan(prev);
            prev = f.seq;
          }

          const held = Math.min(appended, capacity);
          expect(gap).toBe(since > 0 && held > 0 && rb.oldest > since + 1);
        }
      }
    }
  });
});
