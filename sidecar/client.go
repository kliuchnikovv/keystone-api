package sidecar

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"time"
)

// DefaultPushBuffer sizes the channel Pushes reads from.
//
// Deep enough to absorb a burst — a discover that brings a house online at
// once — without letting a consumer that has stopped reading pin unbounded
// memory.
const DefaultPushBuffer = 256

// CoreOptions configures Connect.
type CoreOptions struct {
	// Version identifies the core in the handshake.
	Version string

	// Session mints session ids. Defaults to a generated one.
	Session func() string

	// Features advertised to the plugin. Defaults to seq.replay and
	// topic.filtering, which is what this implementation actually does.
	Features []string

	// Handler answers requests from the plugin. Optional: v1 defines no
	// plugin-to-core methods beyond hello, which is handled internally.
	Handler Handler

	// Heartbeat defaults to the spec's 10s/30s.
	Heartbeat Heartbeat

	// Backoff controls reconnection. Defaults to 1s doubling to 30s.
	Backoff *Backoff

	// PushBuffer defaults to DefaultPushBuffer.
	PushBuffer int

	Logger *slog.Logger
	Clock  Clock
}

func (o *CoreOptions) withDefaults() {
	if o.Version == "" {
		o.Version = "0.0.0"
	}
	if o.Session == nil {
		o.Session = func() string { return "sess_" + NewID() }
	}
	if o.Features == nil {
		o.Features = []string{FeatureSeqReplay, FeatureTopicFiltering}
	}
	if o.PushBuffer <= 0 {
		o.PushBuffer = DefaultPushBuffer
	}
	if o.Backoff == nil {
		o.Backoff = NewBackoff()
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if o.Clock == nil {
		o.Clock = SystemClock{}
	}
	o.Heartbeat = o.Heartbeat.withDefaults()
}

// Client is the core's end of a plugin connection.
//
// It owns reconnection, so a caller sees one long-lived object rather than a
// succession of connections: Pushes stays open across a plugin restart, and a
// Call made while the plugin is down fails with ErrNotConnected — which
// IsRetryable reports as worth retrying — rather than blocking.
type Client struct {
	path string
	opts CoreOptions
	log  *slog.Logger

	mu        sync.RWMutex
	peer      *Peer
	connected bool
	session   SessionInfo

	// lastSeq is the highest push sequence accepted. It is what resume asks
	// from after a reconnect.
	seqMu   sync.Mutex
	lastSeq uint64

	filterMu sync.Mutex
	filters  []Filter

	pushes chan Push
	status chan bool

	closeOnce sync.Once
	done      chan struct{}
}

// Connect dials a plugin socket and keeps the connection up until Close.
//
// It returns once the first connection has been established and the handshake
// has completed, so a caller can subscribe and start issuing calls
// immediately.
func Connect(ctx context.Context, path string, opts CoreOptions) (*Client, error) {
	opts.withDefaults()

	c := &Client{
		path:   path,
		opts:   opts,
		log:    opts.Logger,
		pushes: make(chan Push, opts.PushBuffer),
		status: make(chan bool, 8),
		done:   make(chan struct{}),
	}

	conn, err := DialUnix(ctx, path)
	if err != nil {
		return nil, err
	}

	ready := make(chan error, 1)
	go c.run(context.WithoutCancel(ctx), conn, ready)

	select {
	case err := <-ready:
		if err != nil {
			_ = c.Close()
			return nil, err
		}
		return c, nil
	case <-ctx.Done():
		_ = c.Close()
		return nil, ctx.Err()
	}
}

// run serves the first connection and then reconnects for the life of the
// client.
func (c *Client) run(ctx context.Context, first io.ReadWriteCloser, ready chan<- error) {
	conn := first
	backoff := c.opts.Backoff
	reported := false

	for {
		select {
		case <-c.done:
			return
		default:
		}

		err := c.serve(ctx, conn, func(err error) {
			if !reported {
				reported = true
				ready <- err
			}
		})

		if c.isClosed() {
			return
		}
		c.setConnected(false)
		c.log.Warn("sidecar: plugin connection lost, reconnecting", "path", c.path, "err", err)

		if !backoff.Wait(ctx) {
			return
		}

		next, derr := DialUnix(ctx, c.path)
		if derr != nil {
			if c.isClosed() {
				return
			}
			// The plugin is not back yet. Go round again: the backoff at the
			// top of the loop is what paces this, so a plugin that never
			// returns is retried at 30s rather than in a tight loop.
			c.log.Warn("sidecar: reconnect failed", "path", c.path, "err", derr)
			conn = nil
			continue
		}
		backoff.Reset()
		conn = next
	}
}

// serve runs one connection: handshake, resubscribe, resume, then pump.
//
// A nil conn means the last dial failed; there is nothing to serve and the
// caller loops back into the backoff.
func (c *Client) serve(ctx context.Context, conn io.ReadWriteCloser, report func(error)) error {
	if conn == nil {
		return nil
	}

	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	peer := NewPeer(conn, Options{
		Handler: c.handler(),
		OnPush:  c.acceptPush,
		Logger:  c.log,
		Clock:   c.opts.Clock,
	})

	c.mu.Lock()
	c.peer = peer
	c.mu.Unlock()

	served := make(chan error, 1)
	go func() { served <- peer.Serve(connCtx) }()

	// The plugin greets first, so the handshake completes when its hello
	// arrives — which our handler answers. Wait for that rather than driving
	// it from here.
	if err := c.awaitHandshake(connCtx, peer); err != nil {
		report(err)
		_ = peer.Close()
		<-served
		return err
	}

	peer.StartHeartbeat(connCtx, c.opts.Heartbeat)
	c.setConnected(true)

	if err := c.resubscribe(connCtx, peer); err != nil {
		// Not fatal. The connection is up and calls work; the subscription can
		// be retried on the next reconnect. Tearing down a working connection
		// because one setup call failed would be worse.
		c.log.Warn("sidecar: could not restore the subscription", "err", err)
	}

	report(nil)
	return <-served
}

// awaitHandshake waits for the plugin's hello to be answered.
func (c *Client) awaitHandshake(ctx context.Context, peer *Peer) error {
	deadline := time.NewTimer(c.opts.Heartbeat.Timeout)
	defer deadline.Stop()

	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-peer.Done():
			return ErrNotConnected
		case <-deadline.C:
			return Errorf(CodeProtocolUnsupported,
				"plugin did not send hello within %s", c.opts.Heartbeat.Timeout)
		case <-tick.C:
			if s := peer.Session(); s.Established() {
				c.mu.Lock()
				c.session = s
				c.mu.Unlock()
				return nil
			}
		}
	}
}

// handler answers what a plugin may call on the core.
func (c *Client) handler() Handler {
	mux := NewMux()
	mux.Handle(MethodHello, HelloHandler(func(_ context.Context, h Hello) (HelloResult, error) {
		return HelloResult{
			Protocol: ProtocolV1,
			Core:     c.opts.Version,
			Session:  c.opts.Session(),
			Features: c.opts.Features,
		}, nil
	}))

	if c.opts.Handler != nil {
		return fallbackHandler{primary: mux, next: c.opts.Handler}
	}
	return mux
}

// fallbackHandler tries the protocol methods first and defers anything else to
// the caller's handler.
type fallbackHandler struct {
	primary *Mux
	next    Handler
}

func (f fallbackHandler) HandleRequest(ctx context.Context, r *Request) (any, error) {
	f.primary.mu.RLock()
	_, known := f.primary.handlers[r.Method]
	f.primary.mu.RUnlock()
	if known {
		return f.primary.HandleRequest(ctx, r)
	}
	return f.next.HandleRequest(ctx, r)
}

// resubscribe restores the filter set and replays what was missed.
func (c *Client) resubscribe(ctx context.Context, peer *Peer) error {
	c.filterMu.Lock()
	filters := append([]Filter(nil), c.filters...)
	c.filterMu.Unlock()

	if len(filters) == 0 {
		return nil
	}
	if err := peer.Call(ctx, MethodSubscribe, SubscribeParams{Topics: filters}, nil); err != nil {
		return err
	}
	_, err := c.resume(ctx, peer)
	return err
}

// resume asks the plugin to replay from our last accepted sequence, and clamps
// if the plugin restarted.
func (c *Client) resume(ctx context.Context, peer *Peer) (ResumeResult, error) {
	c.seqMu.Lock()
	since := c.lastSeq
	c.seqMu.Unlock()

	var res ResumeResult
	if err := peer.Call(ctx, MethodResume, ResumeParams{SinceSeq: since}, &res); err != nil {
		return res, err
	}

	// The clamp. If the plugin's counter is behind where we think we are, the
	// plugin restarted and its numbering began again — the seq values we hold
	// refer to a previous run's events. Without this, every push from the
	// restarted plugin looks like a duplicate we have already seen, and we
	// would silently discard all of them until the counter caught up. For a
	// plugin restarted after a week that is never.
	if res.Seq < since {
		c.log.Info("sidecar: plugin restarted, clamping sequence",
			"was", since, "now", res.Seq)
		c.seqMu.Lock()
		c.lastSeq = res.Seq
		c.seqMu.Unlock()
	}

	if res.Gap {
		// Pushes were lost for good. Say so loudly: the caller's picture of
		// device state is now incomplete, and only a re-read can fix it.
		c.log.Warn("sidecar: push history was lost, state may be stale",
			"since", since, "plugin_oldest_available", res.Seq)
	}
	return res, nil
}

// acceptPush forwards a push, discarding replays we have already processed.
func (c *Client) acceptPush(_ context.Context, p *Push) {
	c.seqMu.Lock()
	if p.Seq <= c.lastSeq {
		// A replayed frame we already have. Resume deliberately over-sends
		// rather than risk under-sending, so this is the normal path after a
		// reconnect, not an error.
		c.seqMu.Unlock()
		return
	}
	c.lastSeq = p.Seq
	c.seqMu.Unlock()

	select {
	case c.pushes <- *p:
	default:
		// The consumer has stopped reading. Dropping is the only alternative
		// to blocking the read pump, which would stall responses and
		// heartbeats and get the connection killed as unresponsive.
		c.log.Warn("sidecar: push buffer full, dropping", "topic", p.Topic)
	}
}

// Call invokes a method on the plugin.
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	c.mu.RLock()
	peer, connected := c.peer, c.connected
	c.mu.RUnlock()

	if peer == nil || !connected {
		return ErrNotConnected
	}
	return peer.Call(ctx, method, params, result)
}

// Subscribe sets the push filter set, and remembers it so it can be restored
// after a reconnect.
func (c *Client) Subscribe(ctx context.Context, filters ...Filter) error {
	for _, f := range filters {
		if err := f.Validate(); err != nil {
			return err
		}
	}

	c.filterMu.Lock()
	c.filters = append([]Filter(nil), filters...)
	c.filterMu.Unlock()

	return c.Call(ctx, MethodSubscribe, SubscribeParams{Topics: filters}, nil)
}

// Resume asks the plugin to replay from the last sequence this client
// accepted. Connect and reconnection do this automatically; this is for a
// caller that wants to force it.
func (c *Client) Resume(ctx context.Context) (ResumeResult, error) {
	c.mu.RLock()
	peer := c.peer
	c.mu.RUnlock()
	if peer == nil {
		return ResumeResult{}, ErrNotConnected
	}
	return c.resume(ctx, peer)
}

// Pushes returns the stream of pushes.
//
// The channel is never closed, and deliberately: a consumer ranging over it
// would otherwise exit the moment the plugin restarted. Select on your own
// context alongside it.
func (c *Client) Pushes() <-chan Push { return c.pushes }

// StatusChanges reports connection transitions — true on connect, false on
// disconnect.
//
// Without it a subscriber cannot tell a quiet house from a dead plugin: both
// look like an idle push channel. This is the generalisation of keystone's
// TransportEventAdapterStatus, which exists for exactly that reason.
func (c *Client) StatusChanges() <-chan bool { return c.status }

// Connected reports whether a connection is currently up.
func (c *Client) Connected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connected
}

// Session returns what the handshake agreed.
func (c *Client) Session() SessionInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.session
}

// LastSeq returns the highest push sequence accepted.
func (c *Client) LastSeq() uint64 {
	c.seqMu.Lock()
	defer c.seqMu.Unlock()
	return c.lastSeq
}

// Close stops the client and any live connection. Idempotent.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		close(c.done)
		c.mu.Lock()
		peer := c.peer
		c.connected = false
		c.mu.Unlock()
		if peer != nil {
			_ = peer.Close()
		}
	})
	return nil
}

func (c *Client) isClosed() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

// setConnected records a transition and announces it, but only on a change —
// a subscriber wants edges, not a repeated level.
func (c *Client) setConnected(up bool) {
	c.mu.Lock()
	changed := c.connected != up
	c.connected = up
	c.mu.Unlock()

	if !changed {
		return
	}
	select {
	case c.status <- up:
	default:
		c.log.Warn("sidecar: status channel full, dropping a transition", "connected", up)
	}
}

// discardLogger is a logger that writes nowhere. Used by tests that construct
// a Client directly rather than through Connect.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
