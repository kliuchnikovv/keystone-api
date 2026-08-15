# Migrating keystone onto keystone-api

What has to change in the keystone repository now that the contracts live here.
Nothing in keystone has been touched yet — this is the plan, not a record.

## 1. The Matter adapter (the substantial one)

Today `internal/adapters/matter` speaks a WebSocket JSON-RPC dialect whose
contract is duplicated by hand in two files:

- `internal/adapters/matter/protocol.go`
- `sidecars/matter-server/src/protocol.ts`, whose header says it "must stay
  byte-compatible" with the Go file

That byte-compatibility is maintained by discipline. Replacing it is the whole
point of this repository.

**Core side.** `wsclient.go` and most of `adapter.go`'s connection management
are superseded by `sidecar.Connect`. The logic that moves rather than
disappears:

| keystone | becomes |
|---|---|
| `WSClient.Call` multiplexing | `Peer.Call` |
| `connectLoop` backoff | `Backoff`, wired into `Client` |
| `acceptSeq` replay de-duplication | `Client.acceptPush` |
| `resubscribe` clamp and gap handling | `Client.resume` |
| `setConnected` status broadcast | `Client.StatusChanges()` |
| `protocol.go` error taxonomy | `sidecar.Error` and the §9 codes |

What stays in keystone is the part that is genuinely about Matter:
`mapping.go`, `matter_gen.go`, and the translation between
`ports.TransportEvent` and protocol topics.

**Sidecar side.** `sidecars/matter-server/src/wsServer.ts` and `protocol.ts` are
replaced by `@keystone/sidecar-protocol`. `controller.ts` keeps its matter.js
work and registers handlers on a `Mux` instead of switching on method names.

Two fragments from `wsServer.ts` have already been lifted into the shared
package, so delete rather than port them: `jsonReplacer` and the never-throwing
frame encoder. Both encode production failures already paid for once.

**Ordering.** Both sides have to switch together — the protocols are not
compatible. Do it on a branch, with the conformance suite green before merging.

## 2. Two gaps that block full extraction

The Matter adapter uses two optional capabilities that v1 has no methods for:

- `CommissionableDiscoverer` — scanning for unpaired devices
- `CameraStreamer` — WebRTC signalling (no media crosses the engine)

Both are advertised in `hello.capabilities` and documented in
`objects/capability.json`, but §7 defines nothing to call. See
[OPEN_QUESTIONS.md §8](./OPEN_QUESTIONS.md) — the shapes are known, since the
existing Matter dialect already has all four methods, and both additions are
cleanly additive.

**Matter cannot be lifted out of the core until these land.** That is the
forcing function for closing that question, and it belongs to Phase C.

## 3. The proto files

`proto/keystone/v1/`, `buf.yaml` and `buf.gen.yaml` can be deleted from
keystone once its build consumes the stubs from here:

```bash
go get github.com/kliuchnikovv/keystone-api/core@core/v1.0.0
```

Import paths change from `github.com/kliuchnikovv/keystone/gen/go/keystone/v1`
to `github.com/kliuchnikovv/keystone-api/core/gen/go/keystone/v1`. The package
alias `keystonev1` is unchanged, so in practice this is one import line per
file in `internal/api/grpcapi`.

`apps/web/buf.gen.yaml` generated TypeScript from `../../proto` by relative
path, which only worked because it lived in the same checkout. That becomes a
dependency on `@keystone/core-api`.

The `proto` and `proto-check` targets in keystone's Makefile go away with them.

## 4. The plugin manifest

`internal/plugin/manifest_schema.json` already describes `spec.capabilities` as
"identical to the sidecar v1 hello handshake". That vocabulary now has a
canonical definition in `schemas/sidecar/v1/objects/capability.json`.

Worth reconciling so the two cannot drift — a plugin declaring a capability it
does not implement should fail at install time rather than at 3am. Not urgent,
and not a code change: the manifest schema can `$ref` the shared registry, or
CI can check the two lists agree.

## 5. Dependency note

`sidecar` is stdlib-only. `sidecar/schema` uses
`santhosh-tekuri/jsonschema/v6`, which keystone already depends on for manifest
validation, so importing it adds no new name to the dependency tree.

The `core` module pulls in gRPC and protobuf, but keystone only builds those
under `-tags=grpc` today and that stays true — the base build remains thin.

## Suggested order

1. Adopt `core` for the protos. Small, independent, and proves the module
   resolves.
2. Close OPEN_QUESTIONS §8 — add the commissionable-discovery and WebRTC
   methods to the protocol.
3. Migrate the Matter adapter and matter-server together, on a branch.
4. Extract `keystone-plugin-matter`, and let the core stop knowing about
   Matter at all.
