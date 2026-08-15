# core

The external keystone API: protobuf definitions served over gRPC to CLIs, UIs
and agents.

Request/response shaped and comparatively heavyweight, which is what
distinguishes it from [`../sidecar`](../sidecar) — the internal, event-heavy
contract between the core and its plugins.

## Relationship to the sidecar protocol

None, deliberately. The two modules share a repository for discoverability —
"every keystone contract is in one place" — and nothing else. They have
separate `go.mod` files, separate tag namespaces, and neither imports the
other. Do not bump one because the other moved.

## Generating

```bash
make proto          # Go + TypeScript stubs
make proto-lint
make proto-breaking # against main
```

Generated code is not committed; `core/gen/` is gitignored. Consumers get the
Go stubs through the module and the TypeScript ones through `@keystone/core-api`.

## Versioning

Tags are `core/vX.Y.Z`. The prefix is Go's submodule convention, derived from
the directory path — `core-v1.0.0` will not resolve.

```bash
go get github.com/kliuchnikovv/keystone-api/core@v1.0.0
```

## Migrating from keystone

These files previously lived at `proto/keystone/v1/` in the keystone
repository, with `go_package` pointing at `keystone/gen/go/...`. That option is
now rewritten to this module's path — generated code declares its own import
path, so it had to move with the files.

keystone's own `buf.yaml`, `buf.gen.yaml` and `proto/` directory can be deleted
once its build consumes the stubs from here. Its web UI previously generated
TypeScript from `../../proto` by relative path; that now comes from
`@keystone/core-api`.
