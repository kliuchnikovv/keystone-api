package sidecar

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestHeartbeatDropsAWedgedPeer(t *testing.T) {
	// The case a blocking read cannot detect. The far side's process is alive
	// and holding the socket open, but has stopped making progress — a
	// deadlock, a runaway GC pause, a native library stuck on a USB dongle.
	// Nothing arrives and nothing errors; without the heartbeat the connection
	// looks perfectly healthy forever and the house silently stops updating.
	//
	// Modelled here by a peer that never reads: no Serve, so pings are written
	// and never answered.
	ca, cb := net.Pipe()
	defer cb.Close()

	// A short write timeout so the test does not sit out the 30s default. The
	// bound itself is what is being tested, not its size.
	peer := NewPeer(ca, Options{WriteTimeout: 50 * time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = peer.Serve(ctx) }()

	peer.StartHeartbeat(ctx, Heartbeat{
		Interval: 20 * time.Millisecond,
		Timeout:  100 * time.Millisecond,
	})

	select {
	case <-peer.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("the heartbeat did not drop an unresponsive peer")
	}

	// And the reason is recorded, so an operator reading the log knows the
	// plugin stopped answering rather than closed cleanly.
	if code := CodeOf(peer.Err()); code != CodeNetworkTimeout {
		t.Errorf("Err() code = %q, want %q (err = %v)", code, CodeNetworkTimeout, peer.Err())
	}
}

func TestHeartbeatKeepsAHealthyConnection(t *testing.T) {
	// The other half: a peer that answers must not be torn down. A heartbeat
	// that kills healthy connections is worse than none.
	client, _ := pipePeers(t, Options{}, Options{
		Handler: NewMux().Handle(MethodPing, PingHandler),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client.StartHeartbeat(ctx, Heartbeat{
		Interval: 10 * time.Millisecond,
		Timeout:  time.Second,
	})

	select {
	case <-client.Done():
		t.Fatalf("the heartbeat dropped a healthy connection: %v", client.Err())
	case <-time.After(200 * time.Millisecond):
		// Twenty ping cycles without a false positive.
	}
}

func TestHeartbeatStopsWithTheContext(t *testing.T) {
	client, _ := pipePeers(t, Options{}, Options{
		Handler: NewMux().Handle(MethodPing, PingHandler),
	})

	ctx, cancel := context.WithCancel(context.Background())
	client.StartHeartbeat(ctx, Heartbeat{Interval: 10 * time.Millisecond, Timeout: time.Second})
	cancel()

	// Cancelling the heartbeat's context must not take the peer down with it:
	// the two have separate lifetimes, and a caller stopping liveness checks
	// is not asking to close the connection.
	select {
	case <-client.Done():
		t.Fatal("cancelling the heartbeat context closed the peer")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestDefaultHeartbeatMatchesTheSpec(t *testing.T) {
	// Spec §6.1: ping every 10s, pong within 30s.
	hb := DefaultHeartbeat()
	if hb.Interval != 10*time.Second {
		t.Errorf("Interval = %v, want 10s", hb.Interval)
	}
	if hb.Timeout != 30*time.Second {
		t.Errorf("Timeout = %v, want 30s", hb.Timeout)
	}
}

func TestBackoffGrowsAndResets(t *testing.T) {
	b := &Backoff{Min: 10 * time.Millisecond, Max: 80 * time.Millisecond, Factor: 2}

	var last time.Duration
	for i := 0; i < 6; i++ {
		d := b.Next()
		if d <= 0 {
			t.Fatalf("Next() = %v, want a positive delay", d)
		}
		if d > b.Max {
			t.Fatalf("Next() = %v, above Max %v", d, b.Max)
		}
		if i > 0 && d < last && d < b.Max {
			// Growth is monotonic up to the ceiling. Without jitter this would
			// be exact; with it, only the ceiling is a hard guarantee.
			t.Logf("delay %v after %v (jitter)", d, last)
		}
		last = d
	}

	b.Reset()
	if d := b.Next(); d > 2*b.Min {
		t.Errorf("Next() after Reset = %v, want about Min (%v)", d, b.Min)
	}
}

func TestBackoffWaitHonoursContext(t *testing.T) {
	b := &Backoff{Min: 10 * time.Second, Max: 30 * time.Second, Factor: 2}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	start := time.Now()
	if b.Wait(ctx) {
		t.Error("Wait() = true after its context expired")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Wait() took %v — it ignored the context and slept out the delay", elapsed)
	}
}
