// Package core holds the external keystone API surface: the protobuf
// definitions served over gRPC to CLIs, UIs and agents.
//
// It shares a repository with the sidecar protocol for discoverability only.
// The two modules are versioned independently and neither imports the other.
module github.com/kliuchnikovv/keystone-api/core

go 1.23
