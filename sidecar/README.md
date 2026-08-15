# sidecar

The Go half of the keystone sidecar protocol v1: the transport contract between
keystone-core and any plugin.

```bash
go get github.com/kliuchnikovv/keystone-api/sidecar
```

## What is here

Wire types, NDJSON framing, and the peer runtime — handshake, heartbeat,
request multiplexing, topic matching, the push ring buffer and resume/replay.

That is the whole of it. Feature builders, declarative handler registration and
config-flow ergonomics belong to `sdk-go`, which is built on top of this. The
tell: if an API mentions a specific device capability, it is SDK; if it
mentions a frame, it is kernel.

## Dependencies

The root package is stdlib-only, so a plugin that only speaks the protocol
links nothing extra. The optional `sidecar/schema` subpackage adds
`santhosh-tekuri/jsonschema` — the same validator keystone already uses for
plugin manifests — and only if you import it.

## Plugin side

```go
mux := sidecar.NewMux().
    HandleFunc("discover", func(ctx context.Context, r *sidecar.Request) (any, error) {
        return devices, nil
    }).
    HandleFunc("invokeAction", handleAction)

err := sidecar.RunPlugin(ctx, sidecar.PluginOptions{
    Name:         "matter",
    Version:      "1.2.0",
    Capabilities: []string{"realtime.state-stream", "commission.setup-code"},
    Handler:      mux,
    OnConnect: func(ctx context.Context, p *sidecar.Peer) {
        p.Publish("lifecycle.matter:3.online", map[string]any{"deviceRef": "matter:3"})
    },
})
```

`hello`, `ping`, `subscribe`, `unsubscribe`, `resume` and `shutdown` are
installed for you, so a plugin writes only the methods that are actually about
its transport. Registering your own takes precedence.

The plugin listens on `$KEYSTONE_PLUGIN_SOCKET` and the core dials it. See
[OPEN_QUESTIONS.md §7](../OPEN_QUESTIONS.md) for why that direction.

## Core side

```go
client, err := sidecar.Connect(ctx, socketPath, sidecar.CoreOptions{Version: "0.5.2"})
if err != nil {
    return err
}
defer client.Close()

if err := client.Subscribe(ctx, "state.**", "lifecycle.**"); err != nil {
    return err
}

for {
    select {
    case push := <-client.Pushes():
        handle(push)
    case up := <-client.StatusChanges():
        // Distinguishes a quiet house from a dead plugin. Without it both
        // look like an idle channel.
        log.Info("plugin connectivity changed", "connected", up)
    case <-ctx.Done():
        return ctx.Err()
    }
}
```

`Client` owns reconnection. `Pushes()` stays open across a plugin restart —
deliberately, so a consumer ranging over it does not exit the moment the plugin
bounces — and a call made while the plugin is down returns `ErrNotConnected`,
which `IsRetryable` reports as worth retrying.

## Errors

```go
if errors.Is(err, sidecar.ErrDeviceOffline) { ... }
if sidecar.IsRetryable(err) { backoff() }
```

Failures from the far side arrive as `*sidecar.Error` carrying a dotted code
from the §9 taxonomy. Note that `retryable` is optional on the wire —
retryability is a property of the code — so the decoder fills it in from the
taxonomy when a peer omits it. Without that, an omitted `device.offline` would
read as a permanent failure and the core would stop retrying sleeping devices.

## Symmetry

There is no client type and no server type. A `Peer` can send requests and
answer them; it can emit pushes and receive them. `RunPlugin` and `Connect` are
thin conveniences over the same object, and `Request.Peer()` lets a handler
push or call back while it is still deciding what to answer — which is how
commissioning reports progress instead of leaving the user watching a spinner.

`NewPeer` takes an `io.ReadWriteCloser`, so a unix socket, a `net.Pipe` and an
stdio pair are indistinguishable to it.

## Testing against it

`sidecar/sidecartest` provides a scriptable peer on a real unix socket:

```go
peer := sidecartest.Start(t, sidecartest.WithHandler(func(r *sidecar.Request) (any, error) {
    return result, nil
}))

client, err := sidecar.Connect(ctx, peer.Path(), sidecar.CoreOptions{})
peer.Drop()                       // process death, no close handshake
peer.WaitAccepts(2, time.Second)  // prove a reconnect actually happened
```

`sidecartest.WithStall()` accepts a connection and never reads it — a peer that
is alive but no longer making progress, which is the only case the heartbeat
exists for and the one a blocking read cannot detect.

## Validation

Off by default in production, on in tests, as spec §13 asks:

```go
v, err := schema.New()
if err := v.ValidateFrame(raw); err != nil { ... }
if err := v.ValidatePush(topic, payload); err != nil { ... }
```

## Versioning

Tags are `sidecar/vX.Y.Z` — Go's submodule convention, derived from the
directory path. `sidecar-v1.0.0` will never resolve.

The protocol major and the module major are the same number. Protocol v2 will
live at `sidecar/v2/` as `github.com/kliuchnikovv/keystone-api/sidecar/v2`, so
both majors can coexist in one build.
