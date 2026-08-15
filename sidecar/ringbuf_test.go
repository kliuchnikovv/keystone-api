package sidecar

import (
	"testing"
)

func pushFrame(seq uint64) Frame {
	ts := Now()
	return Frame{
		Type:  FramePush,
		Topic: "state.matter:3.onoff.value",
		Seq:   seq,
		TS:    &ts,
	}
}

func seqs(frames []Frame) []uint64 {
	out := make([]uint64, len(frames))
	for i, f := range frames {
		out[i] = f.Seq
	}
	return out
}

func equal(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRingBufferSinceReturnsEverythingNewer(t *testing.T) {
	rb := NewRingBuffer(10)
	for i := uint64(1); i <= 5; i++ {
		rb.Append(pushFrame(i))
	}

	tests := []struct {
		since uint64
		want  []uint64
	}{
		{0, []uint64{1, 2, 3, 4, 5}},
		{1, []uint64{2, 3, 4, 5}},
		{4, []uint64{5}},
		{5, nil},
		// A caller ahead of the sender: nothing to send, and not an error.
		// It happens legitimately when the sender restarts.
		{99, nil},
	}

	for _, tc := range tests {
		got, gap := rb.Since(tc.since)
		if !equal(seqs(got), tc.want) {
			t.Errorf("Since(%d) = %v, want %v", tc.since, seqs(got), tc.want)
		}
		if gap {
			t.Errorf("Since(%d) reported a gap on a buffer that never wrapped", tc.since)
		}
	}
}

func TestRingBufferEvictsOldest(t *testing.T) {
	rb := NewRingBuffer(3)
	for i := uint64(1); i <= 5; i++ {
		rb.Append(pushFrame(i))
	}

	if rb.Len() != 3 {
		t.Errorf("Len() = %d, want 3", rb.Len())
	}
	if rb.Oldest() != 3 {
		t.Errorf("Oldest() = %d, want 3", rb.Oldest())
	}
	if rb.Newest() != 5 {
		t.Errorf("Newest() = %d, want 5", rb.Newest())
	}

	got, _ := rb.Since(0)
	if want := []uint64{3, 4, 5}; !equal(seqs(got), want) {
		t.Errorf("Since(0) = %v, want %v", seqs(got), want)
	}
}

func TestRingBufferReportsGap(t *testing.T) {
	// The signal that matters. A caller that resumes from a position already
	// overwritten has lost pushes for good, and must re-read state from the
	// devices rather than carry on believing its picture is complete (§10.2).
	rb := NewRingBuffer(3)
	for i := uint64(1); i <= 5; i++ {
		rb.Append(pushFrame(i))
	}
	// The buffer now holds 3,4,5.

	tests := []struct {
		since uint64
		gap   bool
		why   string
	}{
		{4, false, "4 is in the buffer, so 5 follows contiguously"},
		{3, false, "3 is in the buffer, so 4 and 5 follow contiguously"},
		{2, false, "the caller has 2 and the buffer starts at 3 — contiguous"},
		{1, true, "the caller has 1 but 2 was evicted — 2 is lost"},
		{0, false, "the caller claims nothing, so nothing can be missing"},
	}

	for _, tc := range tests {
		_, gap := rb.Since(tc.since)
		if gap != tc.gap {
			t.Errorf("Since(%d) gap = %v, want %v (%s)", tc.since, gap, tc.gap, tc.why)
		}
	}
}

func TestRingBufferEmpty(t *testing.T) {
	rb := NewRingBuffer(4)

	if rb.Len() != 0 || rb.Oldest() != 0 || rb.Newest() != 0 {
		t.Errorf("a fresh buffer is not empty: len=%d oldest=%d newest=%d",
			rb.Len(), rb.Oldest(), rb.Newest())
	}
	frames, gap := rb.Since(0)
	if len(frames) != 0 {
		t.Errorf("Since(0) on an empty buffer = %v", seqs(frames))
	}
	if gap {
		t.Error("an empty buffer reported a gap; there is nothing to be missing yet")
	}
}

func TestRingBufferDefaultCapacity(t *testing.T) {
	// Spec §10.1.
	if got := NewRingBuffer(0).Cap(); got != DefaultRingCapacity {
		t.Errorf("NewRingBuffer(0).Cap() = %d, want %d", got, DefaultRingCapacity)
	}
	if got := NewRingBuffer(-1).Cap(); got != DefaultRingCapacity {
		t.Errorf("NewRingBuffer(-1).Cap() = %d, want %d", got, DefaultRingCapacity)
	}
	if got := DefaultRingCapacity; got != 1000 {
		t.Errorf("DefaultRingCapacity = %d, want 1000", got)
	}
}

func TestRingBufferReset(t *testing.T) {
	// Used when a sender's counter restarts. Stale frames must not be
	// replayable under numbers that now mean different events.
	rb := NewRingBuffer(4)
	for i := uint64(1); i <= 4; i++ {
		rb.Append(pushFrame(i))
	}
	rb.Reset()

	if rb.Len() != 0 {
		t.Errorf("Len() after Reset = %d, want 0", rb.Len())
	}
	if frames, _ := rb.Since(0); len(frames) != 0 {
		t.Errorf("Since(0) after Reset = %v, want none", seqs(frames))
	}

	rb.Append(pushFrame(1))
	if got, _ := rb.Since(0); !equal(seqs(got), []uint64{1}) {
		t.Errorf("the buffer is not usable after Reset: %v", seqs(got))
	}
}

// TestRingBufferProperties checks the invariants over many sizes and fill
// levels rather than the handful of cases above.
func TestRingBufferProperties(t *testing.T) {
	for _, capacity := range []int{1, 2, 3, 7, 16, 100} {
		for _, appended := range []int{0, 1, 2, 5, 16, 101, 250} {
			rb := NewRingBuffer(capacity)
			for i := uint64(1); i <= uint64(appended); i++ {
				rb.Append(pushFrame(i))
			}

			held := min(appended, capacity)
			if rb.Len() != held {
				t.Fatalf("cap=%d appended=%d: Len() = %d, want %d",
					capacity, appended, rb.Len(), held)
			}

			for since := uint64(0); since <= uint64(appended)+1; since++ {
				frames, gap := rb.Since(since)

				// Everything returned is newer than the requested position,
				// and strictly increasing.
				var prev uint64
				for _, f := range frames {
					if f.Seq <= since {
						t.Fatalf("cap=%d appended=%d since=%d: returned seq %d",
							capacity, appended, since, f.Seq)
					}
					if f.Seq <= prev {
						t.Fatalf("cap=%d appended=%d since=%d: out of order at %d",
							capacity, appended, since, f.Seq)
					}
					prev = f.Seq
				}

				// gap is exactly "the requested position fell out of the
				// buffer", and is never reported for a caller claiming
				// nothing.
				wantGap := since > 0 && held > 0 && rb.Oldest() > since+1
				if gap != wantGap {
					t.Fatalf("cap=%d appended=%d since=%d: gap = %v, want %v (oldest=%d)",
						capacity, appended, since, gap, wantGap, rb.Oldest())
				}

				// With no gap, the result is the complete contiguous run from
				// since+1 to the newest frame held.
				if !gap && held > 0 && since < rb.Newest() {
					first := max(since+1, rb.Oldest())
					want := int(rb.Newest() - first + 1)
					if len(frames) != want {
						t.Fatalf("cap=%d appended=%d since=%d: got %d frames, want %d",
							capacity, appended, since, len(frames), want)
					}
				}
			}
		}
	}
}
