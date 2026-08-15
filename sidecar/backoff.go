package sidecar

import (
	"context"
	"math/rand"
	"time"
)

// Backoff schedules reconnection attempts.
//
// The shape is the one keystone's Matter adapter arrived at: start fast so a
// plugin restart is barely noticed, then back off so a plugin that is
// genuinely broken is not restarted in a tight loop for the rest of the day.
type Backoff struct {
	Min    time.Duration
	Max    time.Duration
	Factor float64

	// Jitter spreads retries so several plugins that died together — a host
	// reboot, a failed upgrade — do not reconnect in lockstep and arrive as
	// one thundering herd.
	Jitter float64

	current time.Duration
}

// NewBackoff returns the default policy: 1s doubling to 30s, with jitter.
func NewBackoff() *Backoff {
	return &Backoff{
		Min:    time.Second,
		Max:    30 * time.Second,
		Factor: 2,
		Jitter: 0.2,
	}
}

func (b *Backoff) withDefaults() {
	if b.Min <= 0 {
		b.Min = time.Second
	}
	if b.Max <= 0 {
		b.Max = 30 * time.Second
	}
	if b.Factor <= 1 {
		b.Factor = 2
	}
}

// Next returns the next delay and advances the schedule.
func (b *Backoff) Next() time.Duration {
	b.withDefaults()

	if b.current == 0 {
		b.current = b.Min
	} else {
		b.current = time.Duration(float64(b.current) * b.Factor)
		if b.current > b.Max {
			b.current = b.Max
		}
	}

	d := b.current
	if b.Jitter > 0 {
		spread := float64(d) * b.Jitter
		d = time.Duration(float64(d) - spread + rand.Float64()*2*spread)
	}
	if d < 0 {
		d = b.Min
	}
	return d
}

// Reset returns the schedule to its start, after a successful connection.
func (b *Backoff) Reset() { b.current = 0 }

// Wait sleeps for the next delay. It returns false if ctx ended first.
func (b *Backoff) Wait(ctx context.Context) bool {
	t := time.NewTimer(b.Next())
	defer t.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
