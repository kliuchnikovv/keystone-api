# Open questions — sidecar protocol v1

Protocol v1 is **stable on everything it specifies** and deliberately silent on
the points below. They are not oversights; they are decisions we refuse to make
before a real plugin has pushed real traffic through the protocol. Closing them
in a hurry and breaking every consumer two months later is the worse outcome.

Everything here is additive-shaped: a v1.1 can answer any of these with new
optional fields or new methods, without a v2.

Track these with the `spec:v1-open-question` label.

---

## 1. Authentication between core and plugin

**Today:** the unix socket lives in a directory only the `keystone` user can
traverse, and `sidecar.PeerCredentials` exposes the peer's uid/gid so either
side can assert who it is talking to. Nothing else.

**Unresolved:** whether uid checking is sufficient for v1. A multi-user or
container-per-plugin deployment wants a per-plugin bearer token in `hello`.

**Shape of the answer:** an optional `token` field in `HelloParams` plus a
`auth.invalid_credentials` rejection path. Both already have room reserved.

## 2. Streaming binary data

**Today:** everything on the wire is JSON. Base64 inside a push payload works
and is awful — it inflates 33%, it blocks the single NDJSON stream while it
transfers, and it defeats `socat` debuggability.

**Unresolved:** how a camera-bridge plugin ships thumbnails or clips. Candidates
are a side-channel unix socket negotiated through a method call, or an HTTP
endpoint on the core that the plugin POSTs to.

**Note:** the existing Matter camera path (`CameraStreamer` in keystone's
`internal/ports/adapter.go`) is WebRTC *signalling only* — no media crosses the
engine — so this is not blocking Matter extraction.

## 3. Bulk operations

**Today:** every `readState` is one round-trip. A rule reading 20 attributes off
one device costs 20 requests.

**Unresolved:** whether to add `readStateBatch` / `writeStateBatch`. Over a unix
socket a round-trip is cheap, so this may be a non-problem — measure before
adding surface.

## 4. Push priority

**Today:** one stream, one seq counter, ordering is arrival order. An energy
meter pushing every second shares a queue with `lifecycle.*.offline`.

**Unresolved:** whether important pushes need their own channel, or whether
topic-based prioritisation on the core side is enough. Starting with the latter.

## 5. Persistent subscription hint in `hello`

**Today:** after a reconnect the core re-sends `subscribe` before `resume`, one
extra round-trip.

**Unresolved:** whether the plugin should remember the last filter set and
re-apply it automatically, advertised via a `hello` capability.

## 6. Schema evolution inside v1

**Policy for now:** additive only — new optional fields, new methods, new
topics. Both sides parse loosely and ignore unknown fields. Removing a field,
narrowing a type, or changing a default is a v2.

**Unresolved:** whether we need a machine-checkable gate for this (a
`buf breaking`-equivalent for JSON Schema) or whether review is enough.

---

## 7. Who listens, who dials

**Decision for v1:** the **plugin listens** on `$KEYSTONE_PLUGIN_SOCKET`, the
**core dials**, and the plugin sends `hello` first once the connection is
accepted.

This reconciles §3 of the spec (the path is handed *to* the plugin) with §5.1
and §12 (the plugin greets first): the core picks the path because the core is
what spawns the plugin process.

`PluginOptions.Mode` accepts `Listen` or `Dial` so the inverse arrangement stays
reachable, but `Listen` is the default and the only one the conformance suite
exercises.

**Why this is written down:** getting it backwards produces two runtimes that
can never connect, and the failure looks like a socket permissions bug.

## 8. Methods for the two optional adapter capabilities

`hello.capabilities` can advertise `discover.commissionable` and
`camera.webrtc`, and `objects/capability.json` documents both — but §7 defines
no methods to exercise them. Nothing in v1 can call them.

Keystone has working implementations of both today, as optional interfaces
alongside `ports.Adapter`: `CommissionableDiscoverer` (scan for unpaired
devices) and `CameraStreamer` (broker WebRTC signalling; no media crosses the
engine). The Matter plugin cannot be lifted out of the core until the protocol
can carry them.

**Not added here** because the agreed scope was the spec as written, and both
are cleanly additive: a `discoverCommissionable` method, and a small
`webrtcOffer` / `webrtcIce` / `webrtcStop` set plus a `webrtc.<sessionId>.signal`
topic. The existing Matter WS dialect already has all four as
`discoverCommissionable` and `webrtcOffer`/`webrtcIce`/`webrtcStop`, so the
shapes are known rather than speculative.

This is the one open question with a scheduled forcing function: Phase C of the
plugin roadmap, extracting Matter into `keystone-plugin-matter`.

## 9. Per-topic `unsubscribe`

§7.3 makes `unsubscribe` all-or-nothing. Narrowing a subscription today means
re-sending `subscribe` with the full desired filter set, which is fine and
idempotent.

If per-topic removal is ever wanted it is an additive v1.1 method, not a field
on the existing one.

## 10. Topic segments and the `.` separator — **closed in v1**

Recorded here because it was a real hole in the spec draft, found while
writing the schemas.

Topics are dot-separated and `state.<deviceRef>.<feature>.<stateKey>` assumes
each interpolated value occupies exactly one segment. A value containing a dot
shifts every segment after it, and `state.*.*.*` silently stops matching — with
no error raised anywhere.

Two identifiers were affected, not one:

- `deviceRef` — a Zigbee IEEE address or a cloud device id may contain a dot.
- `featureKey` — keystone's own documented convention for vendor extensions is
  `aqara.vibration`, which is dotted by design.

**Resolution:** a single `segment` definition in
`schemas/sidecar/v1/common.json` forbids `.`, whitespace and `*`, and every
identifier that appears in a topic derives from it. Vendor-prefixed feature
keys use a colon on the wire: `aqara:vibration`.
