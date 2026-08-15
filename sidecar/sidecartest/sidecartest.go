// Package sidecartest provides a controllable peer for testing code that
// talks the sidecar protocol.
//
// It is a port of the fakeSidecar harness from keystone's Matter adapter tests
// (internal/adapters/matter/wsclient_test.go), retargeted from an httptest
// WebSocket server to a real unix socket. Every affordance it grew there is
// load-bearing, and each one exists because a real failure was hard to
// reproduce without it:
//
//   - Handler returning a result or an *Error, so error paths are reachable.
//   - Push, to emit on a live connection at a moment of the test's choosing.
//   - Drop, to sever a connection with no close handshake — process death,
//     not a graceful shutdown, which is the case reconnection logic gets wrong.
//   - Accepts, to prove a reconnection actually happened rather than assuming
//     it from the absence of an error.
//   - Stall, to accept a connection and then never read it. A wedged peer
//     looks exactly like an idle one to a blocked Read, and this is the only
//     way to exercise the heartbeat's real purpose.
package sidecartest

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/kliuchnikovv/keystone-api/sidecar"
)

// Peer is a scriptable protocol peer listening on a unix socket.
type Peer struct {
	t    *testing.T
	path string
	ln   net.Listener

	mu       sync.Mutex
	handler  func(*sidecar.Request) (any, error)
	current  *sidecar.Peer
	accepts  int
	stall    bool
	sessions []*sidecar.Peer

	closeOnce sync.Once
	done      chan struct{}
}

// Option configures a Peer.
type Option func(*Peer)

// WithHandler sets the request handler. Returning an error makes the peer
// answer with it; returning an *Error controls the code the far side sees.
func WithHandler(fn func(*sidecar.Request) (any, error)) Option {
	return func(p *Peer) { p.handler = fn }
}

// WithStall makes the peer accept connections and then never read them.
//
// This is the case a blocking read cannot detect: the process is alive, the
// socket is open, and nothing is happening. Without it there is no way to
// distinguish "quiet house" from "wedged plugin", which is precisely what the
// heartbeat exists to do.
func WithStall() Option {
	return func(p *Peer) { p.stall = true }
}

// Start listens on a unix socket in the test's temp directory and serves until
// the test ends.
func Start(t *testing.T, opts ...Option) *Peer {
	t.Helper()

	p := &Peer{
		t:    t,
		done: make(chan struct{}),
		handler: func(*sidecar.Request) (any, error) {
			return struct{}{}, nil
		},
	}
	for _, o := range opts {
		o(p)
	}

	// Socket paths are limited to about 104 bytes on darwin and 108 on Linux,
	// and t.TempDir() under a long test name can exceed that. A short name in
	// the temp dir keeps well clear.
	dir, err := os.MkdirTemp("", "kstest")
	if err != nil {
		t.Fatalf("sidecartest: temp dir: %v", err)
	}
	p.path = filepath.Join(dir, "p.sock")

	ln, err := net.Listen("unix", p.path)
	if err != nil {
		t.Fatalf("sidecartest: listen: %v", err)
	}
	p.ln = ln

	go p.acceptLoop()
	t.Cleanup(func() {
		p.Close()
		_ = os.RemoveAll(dir)
	})
	return p
}

// Path returns the socket path to dial.
func (p *Peer) Path() string { return p.path }

func (p *Peer) acceptLoop() {
	for {
		conn, err := p.ln.Accept()
		if err != nil {
			return
		}

		p.mu.Lock()
		p.accepts++
		stall := p.stall
		p.mu.Unlock()

		if stall {
			// Hold the connection open and read nothing. Deliberately no
			// goroutine serving it — that is the point.
			continue
		}
		go p.serve(conn)
	}
}

func (p *Peer) serve(conn net.Conn) {
	peer := sidecar.NewPeer(conn, sidecar.Options{
		Handler: sidecar.HandlerFunc(func(_ context.Context, r *sidecar.Request) (any, error) {
			p.mu.Lock()
			h := p.handler
			p.mu.Unlock()
			return h(r)
		}),
	})

	p.mu.Lock()
	p.current = peer
	p.sessions = append(p.sessions, peer)
	p.mu.Unlock()

	_ = peer.Serve(context.Background())

	p.mu.Lock()
	if p.current == peer {
		p.current = nil
	}
	p.mu.Unlock()
}

// SetHandler replaces the handler mid-test, so a peer can start failing (or
// start succeeding) part way through a scenario.
func (p *Peer) SetHandler(fn func(*sidecar.Request) (any, error)) {
	p.mu.Lock()
	p.handler = fn
	p.mu.Unlock()
}

// Publisher returns the current connection's publisher, or nil if nothing is
// connected.
func (p *Peer) Publisher() *sidecar.Publisher {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.current == nil {
		return nil
	}
	return p.current.Publisher()
}

// Push emits on the live connection. Reports whether there was one.
func (p *Peer) Push(topic sidecar.Topic, payload any) bool {
	p.mu.Lock()
	peer := p.current
	p.mu.Unlock()
	if peer == nil {
		return false
	}
	return peer.Publish(topic, payload) == nil
}

// Drop severs the current connection with no close handshake.
//
// This is process death, not a graceful shutdown — the case reconnection logic
// gets wrong, because a clean close gives the far side a signal that a crash
// does not.
func (p *Peer) Drop() {
	p.mu.Lock()
	peer := p.current
	p.current = nil
	p.mu.Unlock()
	if peer != nil {
		_ = peer.Close()
	}
}

// Accepts returns how many connections have been accepted.
//
// Assert on this rather than on the absence of errors: a test that "passes"
// because a call succeeded on the original connection proves nothing about
// reconnection.
func (p *Peer) Accepts() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.accepts
}

// Connected reports whether a connection is currently being served.
func (p *Peer) Connected() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.current != nil
}

// WaitAccepts blocks until at least n connections have been accepted.
func (p *Peer) WaitAccepts(n int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if p.Accepts() >= n {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

// WaitConnected blocks until a connection is being served.
func (p *Peer) WaitConnected(within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if p.Connected() {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

// Close stops the listener and any live connection.
func (p *Peer) Close() {
	p.closeOnce.Do(func() {
		close(p.done)
		_ = p.ln.Close()

		p.mu.Lock()
		sessions := p.sessions
		p.sessions = nil
		p.current = nil
		p.mu.Unlock()

		for _, s := range sessions {
			_ = s.Close()
		}
	})
}

// Pipe returns two peers connected by an in-memory pipe.
//
// For tests about protocol behaviour rather than about sockets: no file
// system, no listener, no reconnection. The peer runtime cannot tell the
// difference, which is the payoff of taking an io.ReadWriteCloser.
func Pipe(t *testing.T, a, b sidecar.Options) (*sidecar.Peer, *sidecar.Peer) {
	t.Helper()

	ca, cb := net.Pipe()
	pa := sidecar.NewPeer(ca, a)
	pb := sidecar.NewPeer(cb, b)

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
