package sidecar

import "time"

// Clock abstracts time so tests can drive heartbeats and timeouts without
// sleeping.
//
// The heartbeat interval is ten seconds and the timeout thirty (spec §6.1).
// A test suite that waited those out honestly would take minutes and would be
// flaky under load; one that shortened them to milliseconds would be testing
// different code paths than production runs. Injecting the clock avoids both.
type Clock interface {
	Now() time.Time
	// NewTicker and NewTimer mirror the time package, returning interfaces so
	// a fake can control them.
	NewTicker(d time.Duration) Ticker
	NewTimer(d time.Duration) Timer
}

// Ticker is the subset of time.Ticker the runtime uses.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

// Timer is the subset of time.Timer the runtime uses.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
	Reset(d time.Duration) bool
}

// SystemClock is the real clock, and the default.
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now() }

func (SystemClock) NewTicker(d time.Duration) Ticker { return realTicker{time.NewTicker(d)} }

func (SystemClock) NewTimer(d time.Duration) Timer { return realTimer{time.NewTimer(d)} }

type realTicker struct{ t *time.Ticker }

func (r realTicker) C() <-chan time.Time { return r.t.C }
func (r realTicker) Stop()               { r.t.Stop() }

type realTimer struct{ t *time.Timer }

func (r realTimer) C() <-chan time.Time        { return r.t.C }
func (r realTimer) Stop() bool                 { return r.t.Stop() }
func (r realTimer) Reset(d time.Duration) bool { return r.t.Reset(d) }
