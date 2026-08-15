package sidecar

import (
	"encoding/json"
	"sync"
)

// ResumeResult is what a sender reports when asked to replay (spec §10).
type ResumeResult struct {
	Resumed bool `json:"resumed"`

	// Sending is how many buffered pushes follow before live traffic resumes.
	// Zero is normal: the caller was already current.
	Sending int `json:"sending"`

	// Seq is the sender's counter as it stood before this call.
	//
	// It exists so the caller can detect a restart. If this comes back lower
	// than the since_seq the caller asked from, the sender's counter went back
	// to the beginning — and a caller that does not clamp its expectation to
	// this value will discard every subsequent push as a duplicate until the
	// counter catches up, which for a long-lived plugin is never.
	Seq uint64 `json:"seq"`

	// Gap reports that the requested position had already been overwritten, so
	// some pushes are gone for good. A caller seeing this should re-read state
	// rather than continue with a picture it believes is complete (§10.2).
	Gap bool `json:"gap,omitempty"`
}

// Publisher owns the outbound push path: the sequence counter, the ring
// buffer, and the far side's subscription filter.
//
// It outlives any single connection. Rebind swaps in a new encoder after a
// reconnect, keeping the counter and the buffer — which is the entire reason
// replay can work at all. A publisher scoped to a connection would have an
// empty buffer at exactly the moment resume is called.
type Publisher struct {
	mu    sync.Mutex
	enc   *Encoder
	ring  *RingBuffer
	match *Matcher
	seq   uint64
	clock Clock

	// dropped counts pushes rejected by the filter. Reported through
	// plugin.health so a degraded subscription is visible rather than silent.
	dropped uint64
}

func newPublisher(enc *Encoder, capacity int, clock Clock) *Publisher {
	return &Publisher{
		enc:   enc,
		ring:  NewRingBuffer(capacity),
		match: &Matcher{},
		clock: clock,
	}
}

// Publish emits a push on the topic, if the far side has subscribed to it.
//
// A push that matches no filter is dropped and — importantly — is not
// numbered. Numbering everything and then filtering would leave the receiver
// with permanent seq gaps for topics it deliberately never asked for, and gap
// detection (§10.2) could no longer distinguish "you missed something" from
// "you did not want that". Since the receiver's only recovery from a real gap
// is a full re-read of every device, false positives are expensive.
func (p *Publisher) Publish(topic Topic, payload any) error {
	if !topic.Valid() {
		return Errorf(CodeValidationBadParams, "topic %q is malformed", topic)
	}

	raw, err := json.Marshal(orEmptyObject(payload))
	if err != nil {
		return Errorf(CodePluginInternal, "payload for %s could not be encoded: %v", topic, err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.match.Match(topic) {
		p.dropped++
		return nil
	}

	p.seq++
	ts := NewTime(p.clock.Now())
	frame := Frame{
		Type:    FramePush,
		Topic:   topic,
		Seq:     p.seq,
		TS:      &ts,
		Payload: raw,
	}

	// Buffer before writing. If the write fails because the connection just
	// died, the frame is still replayable once the far side reconnects and
	// resumes — which is the case replay exists for.
	p.ring.Append(frame)

	if p.enc == nil {
		return ErrNotConnected
	}
	return p.enc.Encode(&frame)
}

// SetFilters replaces the subscription filter set (spec §8.2).
func (p *Publisher) SetFilters(filters ...Filter) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.match.Set(filters...)
}

// Filters returns the current filter set.
func (p *Publisher) Filters() []Filter {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.match.Filters()
}

// Seq returns the current counter.
func (p *Publisher) Seq() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.seq
}

// Dropped returns how many pushes the filter rejected.
func (p *Publisher) Dropped() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dropped
}

// Rebind points the publisher at a new connection after a reconnect, keeping
// the counter and the ring buffer.
func (p *Publisher) Rebind(enc *Encoder) {
	p.mu.Lock()
	p.enc = enc
	p.mu.Unlock()
}

// Resume replays everything newer than sinceSeq and reports what the caller
// should expect.
//
// The buffered frames are written before this returns, so by the time the
// caller sees the result the backlog is already on the wire ahead of it —
// ordering the caller relies on to know when live traffic begins.
func (p *Publisher) Resume(sinceSeq uint64) (ResumeResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	frames, gap := p.ring.Since(sinceSeq)
	res := ResumeResult{
		Resumed: true,
		Sending: len(frames),
		Seq:     p.seq,
		Gap:     gap,
	}

	if p.enc == nil {
		return res, ErrNotConnected
	}
	for i := range frames {
		if err := p.enc.Encode(&frames[i]); err != nil {
			return res, err
		}
	}
	return res, nil
}

// Reset restarts the counter and empties the buffer.
//
// Only for a genuine restart of the publishing side. Calling it on a live
// publisher would make the receiver see seq go backwards, which it will read
// as a restart — and it would then clamp and accept frames it has already
// processed as new.
func (p *Publisher) Reset() {
	p.mu.Lock()
	p.seq = 0
	p.dropped = 0
	p.ring.Reset()
	p.mu.Unlock()
}
