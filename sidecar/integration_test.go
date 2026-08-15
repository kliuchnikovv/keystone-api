package sidecar

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// socketPath returns a short path in a fresh temp dir.
//
// Unix socket paths are capped at about 104 bytes on darwin and 108 on Linux,
// and t.TempDir() under a long test name overruns that — producing an "invalid
// argument" from bind that looks nothing like a path-length problem.
func socketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ks")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "p.sock")
}

// runTestPlugin starts a plugin in the background and returns its socket path
// and a function to stop it.
func runTestPlugin(t *testing.T, opts PluginOptions) (string, context.CancelFunc) {
	t.Helper()

	if opts.SocketPath == "" {
		opts.SocketPath = socketPath(t)
	}
	if opts.Name == "" {
		opts.Name = "testplug"
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunPlugin(ctx, opts) }()

	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("RunPlugin did not return after its context was cancelled")
		}
	})

	waitForSocket(t, opts.SocketPath)
	return opts.SocketPath, cancel
}

func waitForSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("the plugin never created %s", path)
}

func TestPluginAndCoreHandshake(t *testing.T) {
	path, _ := runTestPlugin(t, PluginOptions{
		Name:         "matter",
		Version:      "1.2.0",
		Capabilities: []string{"realtime.state-stream", "commission.setup-code"},
		Handler: NewMux().HandleFunc("discover", func(context.Context, *Request) (any, error) {
			return []map[string]string{{"deviceRef": "matter:3", "type": "light"}}, nil
		}),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := Connect(ctx, path, CoreOptions{Version: "0.5.2"})
	if err != nil {
		t.Fatalf("Connect() = %v", err)
	}
	defer client.Close()

	s := client.Session()
	if s.PeerName != "matter" {
		t.Errorf("PeerName = %q, want matter", s.PeerName)
	}
	if s.PeerVersion != "1.2.0" {
		t.Errorf("PeerVersion = %q, want 1.2.0", s.PeerVersion)
	}
	if !s.HasCapability("realtime.state-stream") {
		t.Errorf("capabilities = %v, want realtime.state-stream", s.Capabilities)
	}
	if !s.Established() {
		t.Error("the session is not established after Connect returned")
	}

	var devices []map[string]string
	if err := client.Call(ctx, MethodDiscover, nil, &devices); err != nil {
		t.Fatalf("discover = %v", err)
	}
	if len(devices) != 1 || devices[0]["deviceRef"] != "matter:3" {
		t.Errorf("discover returned %v", devices)
	}
}

func TestSubscribeThenPush(t *testing.T) {
	var peer atomic.Pointer[Peer]

	path, _ := runTestPlugin(t, PluginOptions{
		OnConnect: func(_ context.Context, p *Peer) { peer.Store(p) },
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := Connect(ctx, path, CoreOptions{Version: "0.5.2"})
	if err != nil {
		t.Fatalf("Connect() = %v", err)
	}
	defer client.Close()

	if err := client.Subscribe(ctx, "state.**", "lifecycle.**"); err != nil {
		t.Fatalf("Subscribe() = %v", err)
	}

	p := waitForPeer(t, &peer)
	if err := p.Publish("state.matter:3.onoff.value", map[string]any{
		"value": true, "origin": "device_report",
	}); err != nil {
		t.Fatalf("Publish() = %v", err)
	}

	select {
	case push := <-client.Pushes():
		if push.Topic != "state.matter:3.onoff.value" {
			t.Errorf("Topic = %q", push.Topic)
		}
		if push.Seq != 1 {
			t.Errorf("Seq = %d, want 1", push.Seq)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no push arrived")
	}

	if got := client.LastSeq(); got != 1 {
		t.Errorf("LastSeq() = %d, want 1", got)
	}
}

func TestSubscriptionIsRestoredAfterReconnect(t *testing.T) {
	var peer atomic.Pointer[Peer]
	var connects atomic.Int32

	path, _ := runTestPlugin(t, PluginOptions{
		OnConnect: func(_ context.Context, p *Peer) {
			peer.Store(p)
			connects.Add(1)
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	client, err := Connect(ctx, path, CoreOptions{
		Version: "0.5.2",
		Backoff: &Backoff{Min: 10 * time.Millisecond, Max: 50 * time.Millisecond, Factor: 2},
	})
	if err != nil {
		t.Fatalf("Connect() = %v", err)
	}
	defer client.Close()

	if err := client.Subscribe(ctx, "state.**"); err != nil {
		t.Fatalf("Subscribe() = %v", err)
	}

	first := waitForPeer(t, &peer)
	if err := first.Publish("state.matter:3.onoff.value", map[string]any{"value": true}); err != nil {
		t.Fatalf("Publish() = %v", err)
	}
	drainPush(t, client, 1)

	// Kill the connection without a close handshake, as a crash would.
	peer.Store(nil)
	_ = first.Close()

	// The client reconnects and — crucially — re-sends the subscription. The
	// plugin's filter set lives on its peer, which just died, so a client that
	// forgot to resubscribe would reconnect into permanent silence and look
	// perfectly healthy while doing it.
	second := waitForPeer(t, &peer)
	if connects.Load() < 2 {
		t.Fatalf("the plugin saw %d connections, want at least 2", connects.Load())
	}

	waitFor(t, 3*time.Second, "the client to reconnect", client.Connected)

	if err := second.Publish("state.matter:3.onoff.value", map[string]any{"value": false}); err != nil {
		t.Fatalf("Publish() after reconnect = %v", err)
	}

	select {
	case push := <-client.Pushes():
		if push.Topic != "state.matter:3.onoff.value" {
			t.Errorf("Topic = %q", push.Topic)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no push after reconnect — the subscription was not restored")
	}
}

func TestResumeReplaysWhatWasMissed(t *testing.T) {
	var peer atomic.Pointer[Peer]

	path, _ := runTestPlugin(t, PluginOptions{
		OnConnect: func(_ context.Context, p *Peer) { peer.Store(p) },
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// A slow reconnect gives the test a window in which the plugin is
	// genuinely disconnected. With a fast backoff the pushes below would race
	// the new connection and might arrive live, which would make this test
	// pass without ever exercising replay.
	client, err := Connect(ctx, path, CoreOptions{
		Version: "0.5.2",
		Backoff: &Backoff{Min: 700 * time.Millisecond, Max: time.Second, Factor: 1.1},
	})
	if err != nil {
		t.Fatalf("Connect() = %v", err)
	}
	defer client.Close()

	if err := client.Subscribe(ctx, "state.**"); err != nil {
		t.Fatalf("Subscribe() = %v", err)
	}

	first := waitForPeer(t, &peer)
	if err := first.Publish("state.matter:3.onoff.value", map[string]any{"n": 1}); err != nil {
		t.Fatalf("Publish() = %v", err)
	}
	drainPush(t, client, 1)

	// Drop the connection, then publish into the void.
	//
	// The publisher belongs to the plugin, not to the connection, so these
	// three frames land in the ring buffer even though there is nothing to
	// write them to. That placement is the entire reason replay can work: a
	// per-connection buffer would be empty at exactly this moment.
	peer.Store(nil)
	_ = first.Close()

	waitFor(t, 3*time.Second, "the client to notice the drop", func() bool {
		return !client.Connected()
	})

	for n := 2; n <= 4; n++ {
		// Publishing with no connection reports ErrNotConnected and buffers
		// anyway — which is exactly what we are relying on.
		err := first.Publish("state.matter:3.onoff.value", map[string]any{"n": n})
		if err != nil && !errors.Is(err, ErrNotConnected) {
			t.Fatalf("Publish(%d) = %v", n, err)
		}
	}

	// Reconnection resubscribes and resumes from LastSeq, so the three pushes
	// sent while the socket was down are replayed now.
	var got []uint64
	deadline := time.After(5 * time.Second)
	for len(got) < 3 {
		select {
		case push := <-client.Pushes():
			got = append(got, push.Seq)
		case <-deadline:
			t.Fatalf("only %d of 3 buffered pushes were replayed: %v", len(got), got)
		}
	}

	for i, seq := range got {
		if want := uint64(i + 2); seq != want {
			t.Errorf("replayed push %d has seq %d, want %d (all: %v)", i, seq, want, got)
		}
	}
}

func TestReplayedDuplicatesAreDropped(t *testing.T) {
	// resume deliberately over-sends rather than risk under-sending, so a
	// client must discard what it has already processed. Without this a
	// reconnect would re-deliver state changes the rules engine had already
	// acted on — re-firing automations for events from before the drop.
	c := &Client{
		pushes: make(chan Push, 8),
		log:    discardLogger(),
	}

	for _, seq := range []uint64{1, 2, 3} {
		c.acceptPush(context.Background(), &Push{Topic: "state.a.b.c", Seq: seq})
	}
	// A replay covering ground we already have, plus one genuinely new frame.
	for _, seq := range []uint64{2, 3, 4} {
		c.acceptPush(context.Background(), &Push{Topic: "state.a.b.c", Seq: seq})
	}

	var got []uint64
	for {
		select {
		case p := <-c.pushes:
			got = append(got, p.Seq)
			continue
		default:
		}
		break
	}

	want := []uint64{1, 2, 3, 4}
	if len(got) != len(want) {
		t.Fatalf("delivered %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("delivered %v, want %v", got, want)
		}
	}
}

func TestResumeClampsAfterPluginRestart(t *testing.T) {
	// The highest-value test in the suite.
	//
	// A restarted plugin numbers from 1 again, so its seq values now refer to
	// a different run's events. A client that keeps its old high-water mark
	// treats every push from the new run as a duplicate it has already seen
	// and silently discards all of them — for a plugin restarted after a week
	// of uptime, that is forever. The house simply stops updating, with no
	// error anywhere.
	var peer atomic.Pointer[Peer]
	path := socketPath(t)

	// First run: reach seq 5.
	ctx1, cancel1 := context.WithCancel(context.Background())
	done1 := make(chan error, 1)
	go func() {
		done1 <- RunPlugin(ctx1, PluginOptions{
			Name:       "restarter",
			SocketPath: path,
			OnConnect:  func(_ context.Context, p *Peer) { peer.Store(p) },
		})
	}()
	waitForSocket(t, path)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	client, err := Connect(ctx, path, CoreOptions{
		Version: "0.5.2",
		Backoff: &Backoff{Min: 10 * time.Millisecond, Max: 100 * time.Millisecond, Factor: 2},
	})
	if err != nil {
		cancel1()
		t.Fatalf("Connect() = %v", err)
	}
	defer client.Close()

	if err := client.Subscribe(ctx, "state.**"); err != nil {
		cancel1()
		t.Fatalf("Subscribe() = %v", err)
	}

	p1 := waitForPeer(t, &peer)
	for n := 1; n <= 5; n++ {
		if err := p1.Publish("state.matter:3.onoff.value", map[string]any{"n": n}); err != nil {
			cancel1()
			t.Fatalf("Publish(%d) = %v", n, err)
		}
	}
	drainPush(t, client, 5)

	if got := client.LastSeq(); got != 5 {
		cancel1()
		t.Fatalf("LastSeq() = %d, want 5", got)
	}

	// Restart the plugin. Its counter goes back to zero.
	peer.Store(nil)
	cancel1()
	select {
	case <-done1:
	case <-time.After(3 * time.Second):
		t.Fatal("the first plugin run did not stop")
	}

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	done2 := make(chan error, 1)
	go func() {
		done2 <- RunPlugin(ctx2, PluginOptions{
			Name:       "restarter",
			SocketPath: path,
			OnConnect:  func(_ context.Context, p *Peer) { peer.Store(p) },
		})
	}()
	waitForSocket(t, path)

	p2 := waitForPeer(t, &peer)
	waitFor(t, 5*time.Second, "the client to reconnect", client.Connected)

	// The clamp should have happened during resubscribe/resume.
	waitFor(t, 3*time.Second, "the client to clamp its sequence", func() bool {
		return client.LastSeq() < 5
	})

	if err := p2.Publish("state.matter:3.onoff.value", map[string]any{"n": 1}); err != nil {
		t.Fatalf("Publish() after restart = %v", err)
	}

	select {
	case push := <-client.Pushes():
		if push.Seq != 1 {
			t.Errorf("Seq = %d, want 1 from the restarted plugin", push.Seq)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the push from the restarted plugin was discarded as a duplicate — the clamp did not happen")
	}
}

func TestStatusChangesReportTransitions(t *testing.T) {
	// Without this a subscriber cannot tell a quiet house from a dead plugin:
	// both look like an idle push channel.
	var peer atomic.Pointer[Peer]

	path, _ := runTestPlugin(t, PluginOptions{
		OnConnect: func(_ context.Context, p *Peer) { peer.Store(p) },
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	client, err := Connect(ctx, path, CoreOptions{
		Version: "0.5.2",
		Backoff: &Backoff{Min: 10 * time.Millisecond, Max: 50 * time.Millisecond, Factor: 2},
	})
	if err != nil {
		t.Fatalf("Connect() = %v", err)
	}
	defer client.Close()

	if up := recvStatus(t, client, 3*time.Second); !up {
		t.Error("the first transition was down, want up")
	}

	first := waitForPeer(t, &peer)
	peer.Store(nil)
	_ = first.Close()

	if down := recvStatus(t, client, 3*time.Second); down {
		t.Error("expected a down transition after the connection dropped")
	}
	if up := recvStatus(t, client, 5*time.Second); !up {
		t.Error("expected an up transition after reconnecting")
	}
}

func TestCallWhilePluginIsDownIsRetryable(t *testing.T) {
	path, _ := runTestPlugin(t, PluginOptions{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := Connect(ctx, path, CoreOptions{Version: "0.5.2"})
	if err != nil {
		t.Fatalf("Connect() = %v", err)
	}
	client.Close()

	err = client.Call(ctx, MethodDiscover, nil, nil)
	if !errors.Is(err, ErrNotConnected) {
		t.Fatalf("Call() = %v, want ErrNotConnected", err)
	}
	// A caller checking IsRetryable must be told to try again rather than
	// surface a failure to the user: the operation never happened, and the
	// supervisor is already reconnecting.
	if !IsRetryable(err) {
		t.Error("IsRetryable(ErrNotConnected) = false")
	}
}

func TestListenUnixRemovesAStaleSocket(t *testing.T) {
	// A plugin killed with SIGKILL leaves its socket file behind, and bind
	// then fails with "address already in use" for something nothing is
	// listening on. Without this the plugin would never come back after a
	// crash — the failure mode that most needs it to.
	path := socketPath(t)

	ln, err := ListenUnix(path)
	if err != nil {
		t.Fatalf("ListenUnix() = %v", err)
	}
	// Close the listener but leave the file, as a killed process would.
	if err := ln.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Skip("this platform removes the socket file on Close; nothing to test")
	}

	ln2, err := ListenUnix(path)
	if err != nil {
		t.Fatalf("ListenUnix() over a stale socket = %v", err)
	}
	_ = ln2.Close()
}

func TestListenUnixRefusesALiveSocket(t *testing.T) {
	// The other half: a socket with someone listening on it must not be
	// silently stolen from a running plugin.
	path := socketPath(t)

	ln, err := ListenUnix(path)
	if err != nil {
		t.Fatalf("ListenUnix() = %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	if _, err := ListenUnix(path); err == nil {
		t.Fatal("ListenUnix() over a live socket = nil, want an error")
	}
}

func TestListenUnixRefusesANonSocket(t *testing.T) {
	path := socketPath(t)
	if err := os.WriteFile(path, []byte("not a socket"), 0o600); err != nil {
		t.Fatalf("WriteFile() = %v", err)
	}
	if _, err := ListenUnix(path); err == nil {
		t.Fatal("ListenUnix() over a regular file = nil, want an error")
	}
	// And it must still be there: removing a file someone cares about would be
	// overreach.
	if _, err := os.Stat(path); err != nil {
		t.Error("ListenUnix deleted a regular file that was in the way")
	}
}

// --- helpers ---

func waitForPeer(t *testing.T, p *atomic.Pointer[Peer]) *Peer {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := p.Load(); got != nil {
			return got
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the plugin never reported a connection")
	return nil
}

func waitFor(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func drainPush(t *testing.T, c *Client, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-c.Pushes():
		case <-time.After(3 * time.Second):
			t.Fatalf("only %d of %d pushes arrived", i, n)
		}
	}
}

func recvStatus(t *testing.T, c *Client, within time.Duration) bool {
	t.Helper()
	select {
	case up := <-c.StatusChanges():
		return up
	case <-time.After(within):
		t.Fatal("no status transition")
		return false
	}
}
