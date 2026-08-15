package sidecar

import (
	"context"
	"time"
)

// Heartbeat intervals from spec §6.1.
const (
	// DefaultPingInterval is how often the pinging side sends a ping.
	DefaultPingInterval = 10 * time.Second

	// DefaultPingTimeout is how long it waits for the pong before declaring
	// the connection dead.
	DefaultPingTimeout = 30 * time.Second
)

// Heartbeat configures liveness checking.
type Heartbeat struct {
	Interval time.Duration
	Timeout  time.Duration
}

// DefaultHeartbeat returns the intervals the spec mandates.
func DefaultHeartbeat() Heartbeat {
	return Heartbeat{Interval: DefaultPingInterval, Timeout: DefaultPingTimeout}
}

func (h Heartbeat) withDefaults() Heartbeat {
	if h.Interval <= 0 {
		h.Interval = DefaultPingInterval
	}
	if h.Timeout <= 0 {
		h.Timeout = DefaultPingTimeout
	}
	return h
}

// StartHeartbeat pings the far side on an interval and closes the peer if a
// pong does not come back in time. It returns immediately; the loop stops when
// ctx is cancelled or the peer closes.
//
// Over AF_UNIX a peer whose process dies usually EOFs on its own, so this
// matters less than it did over TCP. What it still catches is the harder case:
// a process that is alive, holds the socket open, and has stopped making
// progress — a deadlock, a runaway GC pause, a native library wedged on a USB
// dongle. To a blocked Read that is indistinguishable from a quiet house.
//
// The peer is closed rather than politely shut down. The far side is by
// definition not answering, so waiting for it to acknowledge a shutdown would
// block on exactly the thing that is already broken.
func (p *Peer) StartHeartbeat(ctx context.Context, hb Heartbeat) {
	hb = hb.withDefaults()

	go func() {
		ticker := p.opts.Clock.NewTicker(hb.Interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-p.done:
				return
			case <-ticker.C():
			}

			pingCtx, cancel := context.WithTimeout(ctx, hb.Timeout)
			err := p.Call(pingCtx, MethodPing, struct{}{}, nil)
			cancel()

			if err == nil {
				continue
			}
			if ctx.Err() != nil || p.isClosed() {
				return
			}

			p.log.Warn("sidecar: heartbeat failed, dropping the connection",
				"err", err, "timeout", hb.Timeout.String())
			p.setErr(Errorf(CodeNetworkTimeout, "peer did not answer a ping within %s", hb.Timeout))
			_ = p.Close()
			return
		}
	}()
}
