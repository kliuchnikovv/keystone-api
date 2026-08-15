# Changelog — sidecar protocol

Versions here cover both the Go module (`sidecar/vX.Y.Z`) and the npm package
`@keystone/sidecar-protocol`, which release together under one number.

## Unreleased

First implementation of the protocol described in keystone's
`docs/sidecar-protocol-v1.md`.

### Added

- Canonical JSON Schemas for the whole v1 surface: envelope, error taxonomy,
  16 methods with a machine-readable index, device/health/capability/config-flow
  objects, and 7 topic payloads with a pattern index.
- Go package: wire types, NDJSON codec, topic matching, ring buffer, publisher,
  the symmetric `Peer`, `RunPlugin`, `Connect`, and the `schema` validator.
- npm package: the same surface for TypeScript.
- Cross-language conformance harness, run in both directions.

### Decided

Two holes in the spec draft were closed rather than carried forward:

- **No topic segment may contain a dot.** Topics are dot-separated and every
  well-known shape interpolates identifiers assuming one segment each. Both
  `deviceRef` and keystone's dotted vendor feature keys (`aqara.vibration`)
  would have shifted every following segment and silently broken `*` matching.
  Vendor keys use a colon on the wire: `aqara:vibration`.
- **`retryable` is not required on the wire.** §9 makes retryability a property
  of the code, so a receiver falls back to the taxonomy when the field is
  absent. Requiring it would break senders that omit it; defaulting it to false
  in the parser would turn every omitted `device.offline` into a permanent
  failure and stop the core retrying sleeping devices.

The protocol is otherwise as written. See
[OPEN_QUESTIONS.md](../OPEN_QUESTIONS.md) for what v1 deliberately leaves open.
