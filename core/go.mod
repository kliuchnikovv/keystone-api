// Package core holds the external keystone API surface: the protobuf
// definitions served over gRPC to CLIs, UIs and agents.
//
// It shares a repository with the sidecar protocol for discoverability only.
// The two modules are versioned independently and neither imports the other.
module github.com/kliuchnikovv/keystone-api/core

// Held at 1.23 to match keystone, which is the consumer. `go mod tidy` will
// raise this the moment a dependency demands it — grpc v1.83 does — so the
// gRPC and protobuf requirements below are pinned to the last releases that
// build on 1.23. Raise all three together, deliberately, or keystone cannot
// consume this module.
go 1.23

require (
	google.golang.org/grpc v1.67.1
	google.golang.org/protobuf v1.35.1
)

require (
	golang.org/x/net v0.29.0 // indirect
	golang.org/x/sys v0.25.0 // indirect
	golang.org/x/text v0.18.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20240814211410-ddb44dafa142 // indirect
)
