# @keystone/sidecar-protocol

The TypeScript half of the keystone sidecar protocol v1: the transport contract
between keystone-core and any plugin.

```bash
npm install @keystone/sidecar-protocol
```

ESM-only, Node ≥ 20.

## What is here

Wire types, NDJSON framing, and the peer runtime — handshake, heartbeat,
request multiplexing, topic matching, the push ring buffer and resume/replay.
Feature builders and config-flow ergonomics belong to `@keystone/sdk-node`,
which is built on top of this.

The Go package [`sidecar`](https://github.com/kliuchnikovv/keystone-api/tree/main/sidecar)
is the mirror of this one. A conformance suite runs the two against each other
over a real unix socket, in both directions.

## Plugin side

```ts
import { Mux, runPlugin } from "@keystone/sidecar-protocol";

const handler = new Mux()
  .handle("discover", () => devices)
  .handle("invokeAction", async (req) => {
    const { deviceRef, feature, action } = req.bind<InvokeActionParams>();
    await transport.invoke(deviceRef, feature, action);
    return {};
  });

const plugin = await runPlugin({
  name: "matter",
  version: "1.2.0",
  capabilities: ["realtime.state-stream"],
  handler,
  onConnect: (peer) => {
    peer.publish("lifecycle.matter:3.online", { deviceRef: "matter:3" });
  },
});
```

`ping`, `subscribe`, `unsubscribe`, `resume` and `shutdown` are installed for
you. Registering your own takes precedence.

The plugin listens on `$KEYSTONE_PLUGIN_SOCKET` and the core dials it. One
connection at a time: the publisher is shared across connections so its ring
buffer survives a reconnect, and it can only be bound to one writer, so a
second core is refused rather than silently stealing the first one's pushes.

## Core side

```ts
import { connectCore } from "@keystone/sidecar-protocol";

const client = await connectCore(socketPath, {
  version: "0.5.2",
  onPush: (push) => handle(push),
});

await client.subscribe("state.**", "lifecycle.**");
```

This class does not own reconnection — a Node core supervises its own child
process and re-dials itself. When you do, pass the previous connection's
watermark:

```ts
const client2 = await connectCore(socketPath, {
  initialSeq: client.lastSeq,
  onPush: handle,
});
await client2.subscribe(...client.filters);
await client2.resume();
```

Without `initialSeq` the new connection starts at zero, `resume` asks for
everything the plugin still holds, and every replayed push is delivered a
second time — re-firing automations for events from before the drop.

`resume` also applies the restart clamp: a plugin that restarted numbers from 1
again, and a client holding a stale watermark would discard every push from the
new run as a duplicate. For a plugin restarted after a week of uptime, forever.

## Errors

```ts
import { Code, codeOf, isRetryable } from "@keystone/sidecar-protocol";

if (codeOf(err) === Code.DeviceOffline) { ... }
if (isRetryable(err)) await backoff();
```

Failures from the far side arrive as `ProtocolError` carrying a dotted code
from the §9 taxonomy. `retryable` is optional on the wire — retryability is a
property of the code — so it is filled in from the taxonomy when a peer omits
it.

## BigInt

`jsonReplacer` is applied to every frame. Matter attribute values contain
BigInt routinely — node ids, EUI64 addresses, 64-bit counters — and
`JSON.stringify` throws on them outright. Values inside the safe-integer range
become numbers so the Go side keeps parsing them as numbers; anything larger
becomes a string, because silently rounding a 64-bit id is worse than changing
its type.

Frames that cannot be serialised are dropped with a log line rather than
thrown: losing one update beats tearing down the connection carrying every
other device's state.

## Testing

```ts
import { socketPair } from "@keystone/sidecar-protocol/testing";

const { a, b, close } = await socketPair();
```

Real connected unix sockets rather than in-memory streams, so a test stays on
the same code path as production — close, destroy and backpressure all behave
as they will in the field.

## Schemas

The canonical JSON Schemas ship with the package:

```ts
import envelope from "@keystone/sidecar-protocol/schemas/v1/envelope.json" with { type: "json" };
```

Runtime validation needs `ajv`, which is an optional peer dependency — it is
never loaded unless you enable validation.

## Versioning

The npm version tracks the Go module's `sidecar/vX.Y.Z` tag: one
human-visible protocol version, two package registries.
