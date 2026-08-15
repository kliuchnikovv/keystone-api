package sidecar

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

// pipePeers wires two peers together over net.Pipe and serves both.
func pipePeers(t *testing.T, a, b Options) (*Peer, *Peer) {
	t.Helper()

	ca, cb := net.Pipe()
	pa, pb := NewPeer(ca, a), NewPeer(cb, b)

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = pa.Serve(ctx) }()
	go func() { _ = pb.Serve(ctx) }()

	t.Cleanup(func() {
		cancel()
		_ = pa.Close()
		_ = pb.Close()
	})
	return pa, pb
}

func TestPeerCall(t *testing.T) {
	type params struct {
		DeviceRef string `json:"deviceRef"`
	}
	type result struct {
		Value bool `json:"value"`
	}

	server := NewMux().HandleFunc("readState", func(_ context.Context, r *Request) (any, error) {
		var p params
		if err := r.Bind(&p); err != nil {
			return nil, err
		}
		if p.DeviceRef != "matter:3" {
			return nil, ErrDeviceNotFound
		}
		return result{Value: true}, nil
	})

	client, _ := pipePeers(t, Options{}, Options{Handler: server})

	var got result
	if err := client.Call(context.Background(), "readState", params{DeviceRef: "matter:3"}, &got); err != nil {
		t.Fatalf("Call() = %v", err)
	}
	if !got.Value {
		t.Errorf("result = %+v, want Value true", got)
	}
}

func TestPeerCallIsConcurrent(t *testing.T) {
	// Request multiplexing: many calls in flight on one connection, each
	// matched to its own reply by id. Without it every device operation would
	// serialise behind the slowest one.
	server := NewMux().HandleFunc("echo", func(_ context.Context, r *Request) (any, error) {
		var p struct {
			N int `json:"n"`
		}
		if err := r.Bind(&p); err != nil {
			return nil, err
		}
		// Stagger the replies so they cannot come back in request order.
		time.Sleep(time.Duration(20-p.N) * time.Millisecond)
		return p, nil
	})

	client, _ := pipePeers(t, Options{}, Options{Handler: server})

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			var got struct {
				N int `json:"n"`
			}
			if err := client.Call(context.Background(), "echo", map[string]int{"n": n}, &got); err != nil {
				t.Errorf("Call(%d) = %v", n, err)
				return
			}
			if got.N != n {
				t.Errorf("call %d got reply for %d — replies were cross-wired", n, got.N)
			}
		}(i)
	}
	wg.Wait()
}

func TestPeerCallReturnsTypedError(t *testing.T) {
	server := NewMux().HandleFunc("readState", func(context.Context, *Request) (any, error) {
		return nil, ErrDeviceOffline.WithDetails(map[string]any{"deviceRef": "matter:3"})
	})

	client, _ := pipePeers(t, Options{}, Options{Handler: server})

	err := client.Call(context.Background(), "readState", nil, nil)
	if err == nil {
		t.Fatal("Call() = nil, want an error")
	}
	// The code survives the round trip, which is what lets the core decide to
	// back off rather than surface a failure to the user.
	if !errors.Is(err, ErrDeviceOffline) {
		t.Errorf("errors.Is(err, ErrDeviceOffline) = false; err = %v", err)
	}
	if !IsRetryable(err) {
		t.Error("IsRetryable() = false for device.offline")
	}
	var e *Error
	if errors.As(err, &e); e.Details["deviceRef"] != "matter:3" {
		t.Errorf("details lost across the wire: %v", e.Details)
	}
}

func TestPeerUnknownMethodIsAnsweredNotIgnored(t *testing.T) {
	// The caller is blocked on a reply for this id. Silence would turn "I do
	// not implement that" into a timeout tens of seconds later, which reads
	// like a dead plugin rather than a missing method.
	client, _ := pipePeers(t, Options{}, Options{Handler: NewMux()})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := client.Call(ctx, "noSuchMethod", nil, nil)
	if err == nil {
		t.Fatal("Call() = nil, want an error")
	}
	if !errors.Is(err, ErrProtocolUnsupported) {
		t.Errorf("err = %v, want protocol.unsupported", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Error("the call timed out instead of being answered")
	}
}

func TestPeerNilHandlerAnswersUnsupported(t *testing.T) {
	// A peer that only consumes pushes still has to answer requests, or the
	// far side hangs.
	client, _ := pipePeers(t, Options{}, Options{})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := client.Call(ctx, "ping", nil, nil); !errors.Is(err, ErrProtocolUnsupported) {
		t.Errorf("Call() = %v, want protocol.unsupported", err)
	}
}

func TestPeerHandlerPanicBecomesAnError(t *testing.T) {
	// A plugin author's nil map dereference should fail that one call, not
	// take down the connection and every other device with it.
	server := NewMux().
		HandleFunc("boom", func(context.Context, *Request) (any, error) {
			var m map[string]string
			//lint:ignore SA5000 the nil map write is the point: this is the
			// shape of the accidental panic a plugin author actually writes.
			m["x"] = "y"
			return nil, nil
		}).
		HandleFunc("ping", func(context.Context, *Request) (any, error) {
			return PingResult{Pong: true}, nil
		})

	client, _ := pipePeers(t, Options{}, Options{Handler: server})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := client.Call(ctx, "boom", nil, nil)
	if err == nil {
		t.Fatal("a panicking handler returned no error")
	}
	if CodeOf(err) != CodePluginInternal {
		t.Errorf("code = %q, want %q", CodeOf(err), CodePluginInternal)
	}

	// And the connection still works.
	var pong PingResult
	if err := client.Call(ctx, "ping", nil, &pong); err != nil {
		t.Fatalf("the connection died with the handler: %v", err)
	}
	if !pong.Pong {
		t.Error("ping did not answer after a panic")
	}
}

func TestPeerCallHonoursContext(t *testing.T) {
	release := make(chan struct{})
	defer close(release)

	server := NewMux().HandleFunc("slow", func(ctx context.Context, _ *Request) (any, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return struct{}{}, nil
	})

	client, _ := pipePeers(t, Options{}, Options{Handler: server})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := client.Call(ctx, "slow", nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Call() = %v, want DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Call took %v — it waited past its own deadline", elapsed)
	}
}

func TestCallsFailFastOnDrop(t *testing.T) {
	// The single most valuable thing teardown does. Twenty calls are in
	// flight when the connection dies; each must learn immediately rather than
	// block for its own CallTimeout. Without the pending channels being
	// closed, a core losing its plugin would sit for thirty seconds per
	// in-flight operation before it could even start reconnecting.
	release := make(chan struct{})
	defer close(release)

	started := make(chan struct{}, 32)
	server := NewMux().HandleFunc("slow", func(ctx context.Context, _ *Request) (any, error) {
		started <- struct{}{}
		<-release
		return struct{}{}, nil
	})

	ca, cb := net.Pipe()
	client := NewPeer(ca, Options{CallTimeout: 30 * time.Second})
	srv := NewPeer(cb, Options{Handler: server})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = client.Serve(ctx) }()
	go func() { _ = srv.Serve(ctx) }()

	const n = 20
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() { errs <- client.Call(context.Background(), "slow", nil, nil) }()
	}

	// Wait until every call is actually in flight on the far side.
	for i := 0; i < n; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of %d calls reached the handler", i, n)
		}
	}

	client.Close()

	deadline := time.After(2 * time.Second)
	for i := 0; i < n; i++ {
		select {
		case err := <-errs:
			if !errors.Is(err, ErrNotConnected) {
				t.Errorf("call %d = %v, want ErrNotConnected", i, err)
			}
		case <-deadline:
			t.Fatalf("only %d of %d calls returned within 2s — they are waiting out CallTimeout", i, n)
		}
	}
}

func TestPeerCallAfterCloseIsNotConnected(t *testing.T) {
	client, _ := pipePeers(t, Options{}, Options{Handler: NewMux()})
	client.Close()

	if err := client.Call(context.Background(), "ping", nil, nil); !errors.Is(err, ErrNotConnected) {
		t.Errorf("Call() after Close = %v, want ErrNotConnected", err)
	}
}

func TestPeerPushDelivery(t *testing.T) {
	received := make(chan *Push, 8)

	_, server := pipePeers(t,
		Options{OnPush: func(_ context.Context, p *Push) { received <- p }},
		Options{Handler: NewMux()},
	)

	// Deny-by-default: nothing arrives until the receiver subscribes.
	if err := server.Publisher().SetFilters("state.**"); err != nil {
		t.Fatalf("SetFilters() = %v", err)
	}

	if err := server.Publish("state.matter:3.onoff.value", map[string]any{
		"value": true, "origin": "device_report",
	}); err != nil {
		t.Fatalf("Publish() = %v", err)
	}

	select {
	case p := <-received:
		if p.Topic != "state.matter:3.onoff.value" {
			t.Errorf("Topic = %q", p.Topic)
		}
		if p.Seq != 1 {
			t.Errorf("Seq = %d, want 1", p.Seq)
		}
		if p.TS.IsZero() {
			t.Error("push arrived with no timestamp")
		}
		var payload struct {
			Value  bool   `json:"value"`
			Origin string `json:"origin"`
		}
		if err := p.Bind(&payload); err != nil {
			t.Fatalf("Bind() = %v", err)
		}
		if !payload.Value || payload.Origin != "device_report" {
			t.Errorf("payload = %+v", payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no push arrived")
	}
}

func TestPeerUnmatchedPushIsNotNumbered(t *testing.T) {
	// The decision that makes gap detection trustworthy. If filtered-out
	// pushes consumed seq numbers, the receiver would see permanent gaps for
	// topics it deliberately never subscribed to — and could no longer tell
	// "you missed something" from "you did not want that". Since its only
	// recovery from a real gap is re-reading every device, false positives are
	// expensive.
	received := make(chan *Push, 8)
	_, server := pipePeers(t,
		Options{OnPush: func(_ context.Context, p *Push) { received <- p }},
		Options{Handler: NewMux()},
	)

	if err := server.Publisher().SetFilters("state.**"); err != nil {
		t.Fatalf("SetFilters() = %v", err)
	}

	// Three pushes, only two of which match.
	mustPublish(t, server, "state.matter:3.onoff.value", map[string]any{"value": true})
	mustPublish(t, server, "plugin.log.debug", map[string]any{"message": "noise"})
	mustPublish(t, server, "state.matter:3.onoff.value", map[string]any{"value": false})

	var got []uint64
	for i := 0; i < 2; i++ {
		select {
		case p := <-received:
			got = append(got, p.Seq)
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d pushes arrived", i)
		}
	}

	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Errorf("seqs = %v, want [1 2] — the dropped push consumed a number", got)
	}
	if n := server.Publisher().Dropped(); n != 1 {
		t.Errorf("Dropped() = %d, want 1", n)
	}
}

func TestPeerMalformedFrameDoesNotKillTheConnection(t *testing.T) {
	// One bad line from a buggy peer must not take out a connection carrying
	// every device's state.
	ca, cb := net.Pipe()

	server := NewPeer(cb, Options{Handler: NewMux().HandleFunc("ping", func(context.Context, *Request) (any, error) {
		return PingResult{Pong: true}, nil
	})})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.Serve(ctx) }()
	t.Cleanup(func() { _ = server.Close() })

	// Write garbage directly, then a real frame.
	go func() {
		_, _ = ca.Write([]byte("this is not json\n"))
		_, _ = ca.Write([]byte(`{"type":"response","id":"nobody-waiting"}` + "\n"))
		_, _ = ca.Write([]byte(`{"type":"request","id":"1","method":"ping"}` + "\n"))
	}()

	dec := NewDecoder(ca)
	done := make(chan *Frame, 1)
	go func() {
		var f Frame
		for {
			if err := dec.Decode(&f); err != nil {
				return
			}
			if f.Type == FrameResponse && f.ID == "1" {
				clone := f
				done <- &clone
				return
			}
		}
	}()

	select {
	case f := <-done:
		if f.Error != nil {
			t.Errorf("ping answered with an error: %v", f.Error)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the connection died on a malformed frame")
	}
}

func TestRequestPeerEnablesCallback(t *testing.T) {
	// Symmetry made usable: a handler pushes on the same connection while it
	// is still deciding what to answer. This is how commissioning reports
	// progress rather than leaving the user watching a spinner.
	progress := make(chan *Push, 8)

	client, _ := pipePeers(t,
		Options{OnPush: func(_ context.Context, p *Push) { progress <- p }},
		Options{Handler: NewMux().HandleFunc("commission", func(_ context.Context, r *Request) (any, error) {
			if err := r.Peer().Publisher().SetFilters("commissioning.**"); err != nil {
				return nil, err
			}
			for _, stage := range []string{"discovered", "pase_established", "operational"} {
				if err := r.Peer().Publish("commissioning.sess_1.progress", map[string]any{
					"stage": stage,
				}); err != nil {
					return nil, err
				}
			}
			return map[string]string{"deviceRef": "matter:9"}, nil
		})},
	)

	var res struct {
		DeviceRef string `json:"deviceRef"`
	}
	if err := client.Call(context.Background(), "commission", nil, &res); err != nil {
		t.Fatalf("Call() = %v", err)
	}
	if res.DeviceRef != "matter:9" {
		t.Errorf("DeviceRef = %q", res.DeviceRef)
	}

	for i, want := range []string{"discovered", "pase_established", "operational"} {
		select {
		case p := <-progress:
			var payload struct {
				Stage string `json:"stage"`
			}
			if err := p.Bind(&payload); err != nil {
				t.Fatalf("Bind() = %v", err)
			}
			if payload.Stage != want {
				t.Errorf("progress %d = %q, want %q", i, payload.Stage, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d progress pushes arrived", i)
		}
	}
}

func TestPeerBindDefaultsAbsentParams(t *testing.T) {
	// A method whose parameters are all optional must work whether the caller
	// sent "params":{} or nothing at all.
	var seen struct {
		Refresh bool `json:"refresh"`
	}
	bound := make(chan struct{}, 1)

	client, _ := pipePeers(t, Options{}, Options{
		Handler: NewMux().HandleFunc("discover", func(_ context.Context, r *Request) (any, error) {
			if err := r.Bind(&seen); err != nil {
				return nil, err
			}
			bound <- struct{}{}
			return []string{}, nil
		}),
	})

	if err := client.Call(context.Background(), "discover", nil, nil); err != nil {
		t.Fatalf("Call() = %v", err)
	}
	select {
	case <-bound:
	case <-time.After(2 * time.Second):
		t.Fatal("the handler never ran")
	}
	if seen.Refresh {
		t.Error("absent params did not bind to the zero value")
	}
}

func TestMuxMethods(t *testing.T) {
	m := NewMux().
		HandleFunc("b", func(context.Context, *Request) (any, error) { return nil, nil }).
		HandleFunc("a", func(context.Context, *Request) (any, error) { return nil, nil })

	got := m.Methods()
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("Methods() = %v, want [a b]", got)
	}
}

func TestMuxHandleReplaces(t *testing.T) {
	// RunPlugin installs defaults for hello, ping and the subscription
	// methods; a plugin with a reason to take one over should just be able to
	// register its own.
	m := NewMux().
		HandleFunc("ping", func(context.Context, *Request) (any, error) {
			return nil, errors.New("first")
		}).
		HandleFunc("ping", func(context.Context, *Request) (any, error) {
			return PingResult{Pong: true}, nil
		})

	got, err := m.HandleRequest(context.Background(), &Request{Method: "ping"})
	if err != nil {
		t.Fatalf("HandleRequest() = %v", err)
	}
	if got.(PingResult).Pong != true {
		t.Error("the first handler was not replaced")
	}
}

func TestPeerEmptyResultIsAnObjectNotNull(t *testing.T) {
	// "result":{} reads better than "result":null in a transcript, and matches
	// what the schemas describe for the methods that return nothing.
	ca, cb := net.Pipe()
	server := NewPeer(cb, Options{Handler: NewMux().HandleFunc("decommission",
		func(context.Context, *Request) (any, error) { return nil, nil })})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.Serve(ctx) }()
	t.Cleanup(func() { _ = server.Close() })

	go func() {
		_, _ = ca.Write([]byte(`{"type":"request","id":"1","method":"decommission"}` + "\n"))
	}()

	var f Frame
	if err := NewDecoder(ca).Decode(&f); err != nil {
		t.Fatalf("Decode() = %v", err)
	}
	if string(f.Result) != "{}" {
		t.Errorf("result = %s, want {}", f.Result)
	}
}

func mustPublish(t *testing.T, p *Peer, topic Topic, payload any) {
	t.Helper()
	if err := p.Publish(topic, payload); err != nil {
		t.Fatalf("Publish(%s) = %v", topic, err)
	}
}
