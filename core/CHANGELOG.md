# Changelog — core API

Versions here cover the Go module (`core/vX.Y.Z`) and `@keystone/core-api`.

Independent of the sidecar protocol's versions. Do not bump one because the
other moved.

## Unreleased

### Added

- `keystone.v1` protobuf definitions, moved from the keystone repository:
  `DeviceService`, `StateService`, `RuleService` and their common messages.
- TypeScript generation alongside Go, so the web UI consumes a published
  package rather than reaching across the tree by relative path.

### Changed

- `go_package` on every file now names this module. Generated code declares its
  own import path, so the option had to move with the files.
- `buf lint` excepts `RPC_REQUEST_STANDARD_NAME`, `RPC_RESPONSE_STANDARD_NAME`
  and `RPC_REQUEST_RESPONSE_UNIQUE`. The API is already published and served;
  satisfying them means renaming every request and response message, which
  breaks every generated client for no behavioural gain. That is a v2 decision.
- gRPC, protobuf and their `x/` transitive dependencies are pinned to their last
  Go 1.23-compatible releases, because keystone — the consumer — is on 1.23 and
  `go mod tidy` otherwise raises this module to 1.25.
