package sidecar

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"time"
)

// Mode selects which side of the socket a plugin takes.
type Mode int

const (
	// ModeListen has the plugin listen on $KEYSTONE_PLUGIN_SOCKET and the core
	// dial it. This is v1's arrangement.
	ModeListen Mode = iota

	// ModeDial has the plugin dial a socket the core is listening on.
	ModeDial
)

// PluginOptions configures RunPlugin.
type PluginOptions struct {
	// Name and Version identify the plugin in the handshake. Name should match
	// metadata.name in its plugin.yaml.
	Name    string
	Version string

	// Capabilities are advertised in hello. They must be a subset of what the
	// manifest declares — the core checks, and a mismatch is an install-time
	// failure rather than a 3am one.
	Capabilities []string

	// SocketPath defaults to $KEYSTONE_PLUGIN_SOCKET.
	SocketPath string

	// Mode defaults to ModeListen.
	Mode Mode

	// Handler answers method calls. hello, ping, subscribe, unsubscribe and
	// resume are installed automatically on top of it, so a plugin only writes
	// the methods that are actually about its transport. A plugin that wants
	// one of the defaults for itself can simply register its own.
	Handler *Mux

	// OnConnect runs once the handshake completes. This is where a plugin
	// starts publishing.
	//
	// It runs on its own goroutine and may block for the life of the
	// connection; it is cancelled when the connection ends.
	OnConnect func(ctx context.Context, p *Peer)

	// OnDisconnect runs after a connection ends, before the next is accepted.
	OnDisconnect func(err error)

	RingCapacity int
	Logger       *slog.Logger
	Clock        Clock

	// ProtocolVersion overrides the advertised protocol. For testing version
	// negotiation; leave it empty.
	ProtocolVersion string
}

func (o *PluginOptions) withDefaults() error {
	if o.Name == "" {
		return errors.New("sidecar: PluginOptions.Name is required — it identifies the plugin in the handshake")
	}
	if o.Version == "" {
		o.Version = "0.0.0"
	}
	if o.Handler == nil {
		o.Handler = NewMux()
	}
	if o.RingCapacity <= 0 {
		o.RingCapacity = DefaultRingCapacity
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if o.Clock == nil {
		o.Clock = SystemClock{}
	}
	if o.ProtocolVersion == "" {
		o.ProtocolVersion = ProtocolV1
	}
	if o.SocketPath == "" {
		path, err := SocketPathFromEnv()
		if err != nil {
			return err
		}
		o.SocketPath = path
	}
	return nil
}

// RunPlugin serves the protocol until ctx is cancelled.
//
// It blocks. In ModeListen it accepts connections one at a time and survives
// the core reconnecting; the publisher — and with it the seq counter and the
// ring buffer — persists across those reconnects, which is what makes resume
// able to replay anything.
//
//	mux := sidecar.NewMux().
//		HandleFunc("discover", handleDiscover).
//		HandleFunc("invokeAction", handleAction)
//
//	err := sidecar.RunPlugin(ctx, sidecar.PluginOptions{
//		Name:         "matter",
//		Version:      "1.2.0",
//		Capabilities: []string{"realtime.state-stream"},
//		Handler:      mux,
//		OnConnect: func(ctx context.Context, p *sidecar.Peer) {
//			p.Publish("lifecycle.matter:3.online", map[string]any{"deviceRef": "matter:3"})
//		},
//	})
func RunPlugin(ctx context.Context, opts PluginOptions) error {
	if err := opts.withDefaults(); err != nil {
		return err
	}

	// One publisher for the whole run, not one per connection. A publisher
	// scoped to a connection would have an empty ring buffer at exactly the
	// moment the core reconnects and calls resume, making replay structurally
	// impossible.
	pub := newPublisher(nil, opts.RingCapacity, opts.Clock)

	switch opts.Mode {
	case ModeDial:
		return runPluginDial(ctx, opts, pub)
	default:
		return runPluginListen(ctx, opts, pub)
	}
}

func runPluginListen(ctx context.Context, opts PluginOptions, pub *Publisher) error {
	ln, err := ListenUnix(opts.SocketPath)
	if err != nil {
		return err
	}
	defer func() {
		_ = ln.Close()
		// Leaving the socket file behind would make the next start fail with
		// "address already in use" for a socket nobody is listening on.
		_ = removeSocketFile(opts.SocketPath)
	}()

	// Accept blocks and cannot observe a context; closing the listener is what
	// wakes it.
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	opts.Logger.Info("sidecar: listening", "plugin", opts.Name, "socket", opts.SocketPath)

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		serveConnection(ctx, conn, opts, pub)
	}
}

func runPluginDial(ctx context.Context, opts PluginOptions, pub *Publisher) error {
	backoff := NewBackoff()

	for {
		conn, err := DialUnix(ctx, opts.SocketPath)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			opts.Logger.Warn("sidecar: could not connect, retrying",
				"socket", opts.SocketPath, "err", err)
			if !backoff.Wait(ctx) {
				return nil
			}
			continue
		}
		backoff.Reset()
		serveConnection(ctx, conn, opts, pub)
		if ctx.Err() != nil {
			return nil
		}
	}
}

// serveConnection runs one connection to completion.
func serveConnection(ctx context.Context, conn net.Conn, opts PluginOptions, pub *Publisher) {
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// The plugin's own handlers first, then the protocol defaults on top of a
	// copy — so registering, say, your own subscribe still works.
	mux := NewMux()
	for _, method := range opts.Handler.Methods() {
		mux.Handle(method, opts.Handler)
	}

	peer := NewPeer(conn, Options{
		Handler:      mux,
		Logger:       opts.Logger,
		RingCapacity: opts.RingCapacity,
		Clock:        opts.Clock,
	})

	// The publisher outlives the connection; point it at this one.
	pub.Rebind(peer.enc)
	peer.pub = pub

	installDefaults(mux, opts, peer, pub)

	// The plugin greets first, once the connection is up. See
	// OPEN_QUESTIONS.md §7 for why this direction.
	ready := make(chan struct{})
	go func() {
		defer close(ready)
		hello := Hello{
			Protocol:     opts.ProtocolVersion,
			Plugin:       opts.Name,
			Version:      opts.Version,
			Capabilities: opts.Capabilities,
		}
		if _, err := peer.Hello(connCtx, hello); err != nil {
			opts.Logger.Warn("sidecar: handshake failed", "err", err)
			_ = peer.Close()
			return
		}
		if opts.OnConnect != nil {
			go opts.OnConnect(connCtx, peer)
		}
	}()

	err := peer.Serve(connCtx)
	<-ready

	// Detach the publisher: a Publish between connections should buffer for
	// replay, not write to a dead socket.
	pub.Rebind(nil)

	if opts.OnDisconnect != nil {
		opts.OnDisconnect(err)
	}
	opts.Logger.Info("sidecar: connection ended", "plugin", opts.Name, "err", err)
}

// installDefaults registers the protocol-level methods every plugin needs,
// without overwriting anything the plugin registered itself.
func installDefaults(mux *Mux, opts PluginOptions, peer *Peer, pub *Publisher) {
	existing := make(map[string]bool)
	for _, m := range opts.Handler.Methods() {
		existing[m] = true
	}

	add := func(method string, h Handler) {
		if !existing[method] {
			mux.Handle(method, h)
		}
	}

	add(MethodPing, PingHandler)
	for method, h := range SubscriptionHandlers(pub) {
		add(method, h)
	}

	// A shutdown request means the core is stopping us. Closing the peer is
	// the whole of a graceful stop from the protocol's side; anything the
	// plugin needs to persist belongs in its own shutdown handler, which it
	// registers itself and which then takes precedence over this.
	add(MethodShutdown, HandlerFunc(func(_ context.Context, r *Request) (any, error) {
		var params struct {
			Deadline *Time  `json:"deadline"`
			Reason   string `json:"reason"`
		}
		_ = r.Bind(&params)
		opts.Logger.Info("sidecar: shutdown requested", "reason", params.Reason)

		// Close after the response goes out, not before — the core is waiting
		// for an acknowledgement, and cutting the socket first would look like
		// a crash during shutdown.
		go func() {
			time.Sleep(50 * time.Millisecond)
			_ = peer.Close()
		}()
		return struct{}{}, nil
	}))
}

func removeSocketFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return nil
	}
	return os.Remove(path)
}
