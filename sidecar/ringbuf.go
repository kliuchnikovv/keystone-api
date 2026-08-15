package sidecar

// DefaultRingCapacity is how many pushes a sender keeps for replay (spec
// §10.1).
//
// 1000 is the specified figure. It is a trade-off between memory and how long
// a reconnect can take before history is lost: a house producing a push a
// second gets about sixteen minutes of buffer, which comfortably covers a
// plugin restart, and a house producing a hundred a second gets ten seconds,
// which does not — see RingBuffer.Since on how that case is reported rather
// than hidden.
const DefaultRingCapacity = 1000

// RingBuffer holds the most recent pushes for replay after a reconnect.
//
// It belongs to the sender and outlives any single connection. That placement
// is the whole point: a buffer scoped to a connection would be empty at
// exactly the moment replay is needed.
//
// Not safe for concurrent use on its own; Publisher owns the lock.
type RingBuffer struct {
	frames []Frame
	// next is where the following Append goes; count saturates at cap.
	next  int
	count int
}

// NewRingBuffer allocates a buffer. A capacity below 1 gets the default.
func NewRingBuffer(capacity int) *RingBuffer {
	if capacity < 1 {
		capacity = DefaultRingCapacity
	}
	return &RingBuffer{frames: make([]Frame, capacity)}
}

// Append records a push, evicting the oldest when full.
func (rb *RingBuffer) Append(f Frame) {
	rb.frames[rb.next] = f
	rb.next = (rb.next + 1) % len(rb.frames)
	if rb.count < len(rb.frames) {
		rb.count++
	}
}

// Len returns how many frames are buffered.
func (rb *RingBuffer) Len() int { return rb.count }

// Cap returns the buffer's capacity.
func (rb *RingBuffer) Cap() int { return len(rb.frames) }

// Oldest returns the lowest seq still held, or 0 when empty.
func (rb *RingBuffer) Oldest() uint64 {
	if rb.count == 0 {
		return 0
	}
	return rb.frames[(rb.next-rb.count+len(rb.frames))%len(rb.frames)].Seq
}

// Newest returns the highest seq held, or 0 when empty.
func (rb *RingBuffer) Newest() uint64 {
	if rb.count == 0 {
		return 0
	}
	return rb.frames[(rb.next-1+len(rb.frames))%len(rb.frames)].Seq
}

// Since returns every buffered frame with seq greater than sinceSeq, in order,
// and reports whether anything between sinceSeq and the buffer was lost.
//
// gap is the honest answer to "can I trust that I have everything since seq
// N". It is true when the caller's position had already been overwritten, so
// some pushes are gone for good and no amount of replaying will bring them
// back. A caller seeing gap should re-read state from the devices rather than
// carry on with a picture it believes is complete but is not (spec §10.2).
//
// sinceSeq of 0 means "everything you still have", and is not a gap: the
// caller is not claiming to have seen anything.
func (rb *RingBuffer) Since(sinceSeq uint64) (frames []Frame, gap bool) {
	if rb.count == 0 {
		return nil, false
	}

	oldest := rb.Oldest()
	// The caller wants sinceSeq+1 onwards. If the buffer starts later than
	// that, the frames in between were evicted.
	gap = sinceSeq > 0 && oldest > sinceSeq+1

	start := (rb.next - rb.count + len(rb.frames)) % len(rb.frames)
	for i := 0; i < rb.count; i++ {
		f := rb.frames[(start+i)%len(rb.frames)]
		if f.Seq > sinceSeq {
			frames = append(frames, f)
		}
	}
	return frames, gap
}

// Reset empties the buffer. Used when a sender's seq counter restarts, so
// stale frames cannot be replayed under numbers that now mean something else.
func (rb *RingBuffer) Reset() {
	rb.next = 0
	rb.count = 0
	clear(rb.frames)
}
