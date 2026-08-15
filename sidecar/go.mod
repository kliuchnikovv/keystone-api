// Package sidecar is the Go half of the keystone sidecar protocol v1 shared
// kernel: wire types, NDJSON framing, and the symmetric peer runtime.
//
// The dependency budget is deliberate. The root package is stdlib-only, so a
// plugin that only speaks the protocol pulls in nothing. The optional
// sidecar/schema subpackage adds santhosh-tekuri/jsonschema — the same
// validator keystone already uses for plugin manifests — and is only linked in
// if you import it.
module github.com/kliuchnikovv/keystone-api/sidecar

go 1.23
