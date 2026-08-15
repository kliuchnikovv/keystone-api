# keystone-api

Public contracts for [keystone](https://github.com/kliuchnikovv/keystone).

Two independent surfaces share this repository for discoverability. They are
separate modules with separate version histories and no dependency between
them.

| Directory | Audience | Shape |
|---|---|---|
| [`sidecar/`](./sidecar) | plugins ↔ core, internal | NDJSON over a unix socket, event-heavy, seq + replay |
| [`core/`](./core) | CLI, UI, agents, external | protobuf over gRPC, request/response |

## What this repository is — and is not

`keystone-api` is a **shared kernel**, not an SDK.

It ships the transport, the framing, and the semantics of the protocol: message
envelopes, handshake, heartbeat, typed errors, topic matching, the push ring
buffer, and resume/replay. That is the whole of it.

Feature builders, declarative handler registration, config-flow ergonomics and
device-capability helpers belong to the SDKs (`sdk-go`, `sdk-node`), which are
built *on top of* the runtime published here. The tell: if an API mentions a
specific device capability, it is SDK; if it mentions a frame, it is kernel.

## Symmetry

The sidecar packages are role-neutral by construction. A `Peer` can send
requests and answer them; it can emit pushes and receive them. `RunPlugin` and
`Connect` are thin conveniences over the same object.

This is not decoration. The core imports the package to drive plugins; a plugin
imports the same package to serve them; `@keystone/sidecar-protocol` backs both
`matter-server` and the future `@keystone/sdk-node`. The conformance suite
proves it by running the full session transcript in **both** directions —
Go-as-core against TS-as-plugin, and the reverse.

## Layout

```
schemas/sidecar/v1/     canonical JSON Schemas — the source of truth
sidecar/                Go module  github.com/kliuchnikovv/keystone-api/sidecar
ts/sidecar/             npm package @keystone/sidecar-protocol
core/                   Go module  github.com/kliuchnikovv/keystone-api/core
tools/                  schema mirroring, TS type generation, schema linting
```

`schemas/` is the contract. Go types are hand-written and held to the schemas by
a CI drift gate (corpus round-trip + reflective struct↔schema diff); TypeScript
types are generated into `ts/sidecar/src/types.gen.ts` and committed. A future
Python or Rust implementation reads the same `schemas/` directory and owes
nothing to either language package.

## Installing

```bash
go get github.com/kliuchnikovv/keystone-api/sidecar
```

```bash
npm install @keystone/sidecar-protocol
```

## Versioning

Two independent tag namespaces:

- `sidecar/v1.0.0` → the Go sidecar module, and the matching npm release
- `core/v1.0.0` → the Go core module, and `@keystone/core-api`

The tag prefix is Go's submodule convention and is not negotiable: a module in
`sidecar/` is only reachable at tags named `sidecar/vX.Y.Z`.

The protocol major version and the Go module major version are the same number.
Protocol v2 will live at `sidecar/v2/` as module
`github.com/kliuchnikovv/keystone-api/sidecar/v2`, so both majors can coexist in
one build — which is what "plugins and core live independently in majors"
(spec §5.2) requires.

## Stability

v1 is stable on everything it specifies. It is explicitly **not final** on
authentication, binary streaming, bulk operations, push priority, persistent
subscription hints, and the mechanics of schema evolution. See
[OPEN_QUESTIONS.md](./OPEN_QUESTIONS.md) before building anything that depends
on those.

## Development

```bash
make lint-schemas     # compile all schemas, run the fixture corpus
make gen              # regenerate schema mirrors + TS types
make verify-gen       # fail if generated artefacts drifted
make test             # Go + TypeScript
make conformance      # Go peer <-> TS peer over a real unix socket
```

`go.work` exists for local convenience. Every CI job runs `GOWORK=off` — a
workspace hides missing `go.mod` requirements, so a green build inside it says
nothing about whether a consumer can `go get` the module.

## Adopting this in keystone

[MIGRATING-KEYSTONE.md](./MIGRATING-KEYSTONE.md) covers what changes on the
other side: which parts of the Matter adapter are superseded, which move, the
two protocol gaps that block extracting Matter entirely, and the proto import
rewrite.

## Reference

The normative prose specification is
[`docs/sidecar-protocol-v1.md`](https://github.com/kliuchnikovv/keystone/blob/main/docs/sidecar-protocol-v1.md)
in the keystone repository. Where this repository and that document disagree,
the document is right and this is a bug.
