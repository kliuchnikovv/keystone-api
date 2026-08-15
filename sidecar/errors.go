package sidecar

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Code is a dotted error identifier, namespace first (spec §9).
//
// The taxonomy below is the standard set. A plugin may mint codes in its own
// namespace and a receiver must not reject them — an unknown code is simply
// one whose retryability the receiver cannot guess, and [DefaultRetryable]
// returns false for it.
type Code string

const (
	// Protocol-level failures. Never retryable: the sender needs to change
	// what it is doing, not do it again.
	CodeProtocolUnsupported Code = "protocol.unsupported"
	CodeProtocolMalformed   Code = "protocol.malformed"

	// Device-level failures.
	CodeDeviceNotFound    Code = "device.not_found"
	CodeDeviceOffline     Code = "device.offline"
	CodeDeviceUnsupported Code = "device.unsupported"

	// The device is there and reachable, but cannot do this.
	CodeFeatureUnsupported Code = "feature.unsupported"

	// Plugin-level failures.
	CodePluginNotReady Code = "plugin.not_ready"
	CodePluginBusy     Code = "plugin.busy"
	CodePluginInternal Code = "plugin.internal"

	// Vendor credentials.
	CodeAuthInvalidCredentials Code = "auth.invalid_credentials"
	CodeAuthExpired            Code = "auth.expired"

	// Upstream connectivity — a cloud API, a hub, a border router.
	CodeNetworkTimeout     Code = "network.timeout"
	CodeNetworkUnavailable Code = "network.unavailable"

	// Refused by policy rather than by circumstance.
	CodePermissionDenied Code = "permission.denied"

	// The call itself was wrong.
	CodeValidationBadParams Code = "validation.bad_params"
)

// DefaultRetryable reports whether a code is retryable when the sender did not
// say (spec §9.1).
//
// Retryability is a property of the code, which is what makes this table the
// right fallback rather than a guess. The distinction is whether waiting could
// plausibly change the answer: a sleeping device will wake, an expired token
// will be refreshed, a malformed request will still be malformed.
func DefaultRetryable(c Code) bool {
	switch c {
	case CodeDeviceOffline,
		CodePluginNotReady,
		CodePluginBusy,
		CodePluginInternal,
		CodeAuthExpired,
		CodeNetworkTimeout,
		CodeNetworkUnavailable:
		return true
	default:
		// Includes every unknown code. Refusing to retry something we do not
		// understand is the safe direction: a wrong retry can hammer a device,
		// a missed retry surfaces an error the user can act on.
		return false
	}
}

// Error is a failure reported by the far side, or produced locally in the same
// shape so that both look alike to a caller.
type Error struct {
	Code      Code           `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details,omitempty"`

	// wrapped is the local cause, if any. Never serialised — the far side has
	// no use for our stack, and it would leak internals across the boundary.
	wrapped error
}

// Errorf builds an *Error with the taxonomy's default retryability.
func Errorf(code Code, format string, a ...any) *Error {
	return &Error{
		Code:      code,
		Message:   fmt.Sprintf(format, a...),
		Retryable: DefaultRetryable(code),
	}
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.wrapped != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.wrapped)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap exposes the local cause to errors.Is and errors.As.
func (e *Error) Unwrap() error { return e.wrapped }

// Is makes errors.Is work against the sentinels below, so callers write
//
//	errors.Is(err, sidecar.ErrDeviceOffline)
//
// rather than comparing codes by hand or, worse, matching on the message.
func (e *Error) Is(target error) bool {
	if e == nil {
		return false
	}
	var t *Error
	if errors.As(target, &t) {
		return t != nil && t.Code == e.Code
	}
	return false
}

// WithDetails attaches machine-readable context. Returns a copy, so the
// sentinels can be used as templates without being mutated.
func (e *Error) WithDetails(details map[string]any) *Error {
	c := *e
	c.Details = details
	return &c
}

// WithMessage replaces the message, keeping the code and retryability.
func (e *Error) WithMessage(format string, a ...any) *Error {
	c := *e
	c.Message = fmt.Sprintf(format, a...)
	return &c
}

// Wrap records a local cause. The cause stays local: it is reachable through
// errors.Is and errors.As on this side, and is not put on the wire.
func (e *Error) Wrap(err error) *Error {
	c := *e
	c.wrapped = err
	return &c
}

// UnmarshalJSON fills in retryable when the sender omitted it.
//
// This is the reason Error has a custom decoder at all. The field is optional
// on the wire (§9 makes retryability a property of the code), so plain
// decoding would leave the zero value false — and a peer that omits it would
// silently turn every device.offline into a permanent failure, stopping the
// core from ever retrying a sleeping device. The bug would look like "some
// devices just never come back".
func (e *Error) UnmarshalJSON(b []byte) error {
	// A shadow type to get the default decoding without recursing, plus a
	// pointer for the field whose absence we need to detect.
	type plain struct {
		Code      Code           `json:"code"`
		Message   string         `json:"message"`
		Retryable *bool          `json:"retryable"`
		Details   map[string]any `json:"details,omitempty"`
	}
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	e.Code = p.Code
	e.Message = p.Message
	e.Details = p.Details
	if p.Retryable != nil {
		e.Retryable = *p.Retryable
	} else {
		e.Retryable = DefaultRetryable(p.Code)
	}
	return nil
}

// Sentinels for errors.Is. One per standard code.
var (
	ErrProtocolUnsupported    = Errorf(CodeProtocolUnsupported, "unsupported protocol version")
	ErrProtocolMalformed      = Errorf(CodeProtocolMalformed, "malformed frame")
	ErrDeviceNotFound         = Errorf(CodeDeviceNotFound, "device not found")
	ErrDeviceOffline          = Errorf(CodeDeviceOffline, "device is offline")
	ErrDeviceUnsupported      = Errorf(CodeDeviceUnsupported, "device does not support this operation")
	ErrFeatureUnsupported     = Errorf(CodeFeatureUnsupported, "feature or action not supported")
	ErrPluginNotReady         = Errorf(CodePluginNotReady, "plugin is not ready")
	ErrPluginBusy             = Errorf(CodePluginBusy, "plugin is busy")
	ErrPluginInternal         = Errorf(CodePluginInternal, "internal plugin error")
	ErrAuthInvalidCredentials = Errorf(CodeAuthInvalidCredentials, "invalid credentials")
	ErrAuthExpired            = Errorf(CodeAuthExpired, "credentials expired")
	ErrNetworkTimeout         = Errorf(CodeNetworkTimeout, "upstream timed out")
	ErrNetworkUnavailable     = Errorf(CodeNetworkUnavailable, "upstream unavailable")
	ErrPermissionDenied       = Errorf(CodePermissionDenied, "permission denied")
	ErrValidationBadParams    = Errorf(CodeValidationBadParams, "invalid parameters")
)

// Transport-level sentinels. These never cross the wire — they describe this
// side's relationship with the connection, not the far side's answer.
var (
	// ErrNotConnected means the call could not be delivered. Retryable by
	// nature: the supervisor is already reconnecting.
	ErrNotConnected = errors.New("sidecar: not connected")

	// ErrPeerClosed means this peer has been shut down and will not reconnect.
	ErrPeerClosed = errors.New("sidecar: peer closed")

	// ErrHandshakeRequired means a method arrived before hello (spec §5).
	ErrHandshakeRequired = errors.New("sidecar: hello must be the first method")
)

// CodeOf returns the code of the first *Error in the chain, or "" if there is
// none.
func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) && e != nil {
		return e.Code
	}
	return ""
}

// IsRetryable reports whether repeating the operation could plausibly succeed.
//
// A failure to reach the far side counts: the call never happened, and the
// supervisor is already working on reconnecting. Anything else defers to the
// error's own flag, which the decoder has already defaulted from the taxonomy
// when the sender omitted it.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrNotConnected) {
		return true
	}
	if errors.Is(err, ErrPeerClosed) {
		return false
	}
	var e *Error
	if errors.As(err, &e) && e != nil {
		return e.Retryable
	}
	return false
}

// asError converts any handler return into the wire shape.
//
// A handler that returns a plain error has not thought about the taxonomy, and
// plugin.internal is the honest classification for that: something went wrong
// inside the plugin and the core has no better information. Silently guessing
// a more specific code from the message would be worse.
func asError(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) && e != nil {
		return e
	}
	return &Error{
		Code:      CodePluginInternal,
		Message:   err.Error(),
		Retryable: DefaultRetryable(CodePluginInternal),
	}
}
