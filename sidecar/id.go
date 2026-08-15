package sidecar

import (
	"crypto/rand"
	"encoding/binary"
	"sync/atomic"
	"time"
)

// crockford is the ULID alphabet: base32 without I, L, O or U, so a
// hand-transcribed id from a log cannot be misread.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// idCounter makes ids unique within a process without a lock.
var idCounter atomic.Uint64

func init() {
	// Seed from crypto/rand so two processes started in the same millisecond
	// do not produce colliding ids. Nothing security-relevant depends on this
	// — request ids only have to be unique per session and direction — but a
	// collision would cross-wire two in-flight calls, which is a horrible bug
	// to diagnose.
	var seed [8]byte
	if _, err := rand.Read(seed[:]); err == nil {
		idCounter.Store(binary.BigEndian.Uint64(seed[:]))
	}
}

// NewID returns a unique, lexicographically sortable request id.
//
// The format is ULID-shaped: 48 bits of millisecond timestamp followed by 80
// bits of counter, Crockford base32, 26 characters. Sortable means a log sorted
// by id is in roughly causal order, which is worth a lot when reading a
// transcript of a session that went wrong.
//
// Hand-rolled rather than taken from a library because the spec asks for "a
// ULID or any unique string" (§4.1), and a dependency in the root package
// would be paid for by every plugin that imports it.
func NewID() string {
	ms := uint64(time.Now().UnixMilli())
	n := idCounter.Add(1)

	var b [26]byte
	// 10 characters of timestamp: 48 bits at 5 bits each, most significant
	// first.
	for i := 9; i >= 0; i-- {
		b[i] = crockford[ms&0x1f]
		ms >>= 5
	}
	// 16 characters of counter.
	for i := 25; i >= 10; i-- {
		b[i] = crockford[n&0x1f]
		n >>= 5
	}
	return string(b[:])
}
