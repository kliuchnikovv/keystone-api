// Package sidecar implements version 1 of the keystone sidecar protocol: the
// transport contract between keystone-core and any plugin.
//
// # What is here
//
// Wire types, NDJSON framing, and the peer runtime — handshake, heartbeat,
// request multiplexing, topic matching, the push ring buffer and resume/replay.
// That is the whole of it. Feature builders, declarative handler registration
// and config-flow ergonomics belong to the SDKs built on top of this.
//
// # Symmetry
//
// There is no client type and no server type. A [Peer] can send requests and
// answer them; it can emit pushes and receive them. Which side does what is a
// matter of policy, not of type.
//
// This matters because the same package backs both ends: keystone-core imports
// it to drive plugins, and a plugin imports it to serve them. [RunPlugin] and
// [Connect] are conveniences over [Peer], not separate implementations.
//
// A [Peer] takes an [io.ReadWriteCloser], so a unix socket, a net.Pipe and a
// TCP connection are indistinguishable to it. Tests use net.Pipe; production
// uses a unix socket.
//
// # Wire format
//
// Newline-delimited JSON. One line is one frame; a frame is a request, a
// response, or a push. This is deliberately inspectable:
//
//	socat UNIX-CONNECT:/run/keystone/matter.sock -
//
// shows live traffic in a readable form, which is why the framing is NDJSON
// rather than anything length-prefixed or binary.
//
// # Plugin side
//
//	mux := sidecar.NewMux().
//		HandleFunc("discover", func(ctx context.Context, r *sidecar.Request) (any, error) {
//			return []Device{...}, nil
//		}).
//		HandleFunc("invokeAction", handleAction)
//
//	err := sidecar.RunPlugin(ctx, sidecar.PluginOptions{
//		Name:         "matter",
//		Version:      "1.2.0",
//		Capabilities: []string{"realtime.state-stream", "commission.setup-code"},
//		Handler:      mux,
//		OnConnect: func(p *sidecar.Peer) {
//			p.Publish("state.matter:3.onoff.value", map[string]any{"value": true})
//		},
//	})
//
// # Core side
//
//	client, err := sidecar.Connect(ctx, socketPath, sidecar.CoreOptions{Version: "0.5.2"})
//	if err != nil {
//		return err
//	}
//	defer client.Close()
//
//	if err := client.Subscribe(ctx, "state.**", "lifecycle.**"); err != nil {
//		return err
//	}
//	for push := range client.Pushes() {
//		...
//	}
//
// # Errors
//
// Failures reported by the far side arrive as [*Error], carrying a dotted code
// from the §9 taxonomy. Use [errors.Is] against the sentinels, and
// [IsRetryable] to decide whether to back off and try again:
//
//	if errors.Is(err, sidecar.ErrDeviceOffline) { ... }
//	if sidecar.IsRetryable(err) { ... }
//
// # Specification
//
// The normative document is docs/sidecar-protocol-v1.md in the keystone
// repository; section references in this package point at it. The JSON Schemas
// under schemas/sidecar/v1 are the machine-readable contract, and the optional
// subpackage sidecar/schema validates against them.
package sidecar
