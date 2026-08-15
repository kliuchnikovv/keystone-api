package sidecar

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestErrorIsMatchesSentinelByCode(t *testing.T) {
	// The point of the sentinels: callers branch on identity, not on the text
	// of a message that the far side is free to change.
	err := Errorf(CodeDeviceNotFound, "node 3 is not in the fabric")

	if !errors.Is(err, ErrDeviceNotFound) {
		t.Error("errors.Is(err, ErrDeviceNotFound) = false, want true")
	}
	if errors.Is(err, ErrDeviceOffline) {
		t.Error("errors.Is matched a different code")
	}

	// And through wrapping, which is how it will actually be encountered.
	wrapped := fmt.Errorf("reading onoff.value: %w", err)
	if !errors.Is(wrapped, ErrDeviceNotFound) {
		t.Error("errors.Is through fmt wrapping = false, want true")
	}
}

func TestErrorWrapExposesLocalCause(t *testing.T) {
	cause := errors.New("dial tcp: connection refused")
	err := ErrNetworkUnavailable.Wrap(cause)

	if !errors.Is(err, cause) {
		t.Error("the wrapped cause is not reachable via errors.Is")
	}
	if !errors.Is(err, ErrNetworkUnavailable) {
		t.Error("wrapping lost the code identity")
	}

	// The cause is local. Putting it on the wire would leak this side's
	// internals to the far side, which has no use for them.
	b, err2 := json.Marshal(err)
	if err2 != nil {
		t.Fatalf("Marshal() = %v", err2)
	}
	if got := string(b); strings.Contains(got, "connection refused") {
		t.Errorf("the local cause was serialised: %s", got)
	}
}

func TestErrorSentinelsAreNotMutatedByWith(t *testing.T) {
	// The sentinels double as templates. If WithDetails or WithMessage mutated
	// them in place, one caller adding details would poison every later use of
	// that sentinel process-wide.
	custom := ErrDeviceOffline.WithDetails(map[string]any{"deviceRef": "matter:3"})
	if ErrDeviceOffline.Details != nil {
		t.Error("WithDetails mutated the sentinel")
	}
	if custom.Details["deviceRef"] != "matter:3" {
		t.Error("WithDetails did not set the details on the copy")
	}

	msg := ErrDeviceOffline.WithMessage("node 3 did not answer")
	if ErrDeviceOffline.Message == "node 3 did not answer" {
		t.Error("WithMessage mutated the sentinel")
	}
	if msg.Code != CodeDeviceOffline {
		t.Error("WithMessage lost the code")
	}
	if !msg.Retryable {
		t.Error("WithMessage lost retryability")
	}
}

func TestRetryableDefaultsFromCodeWhenOmitted(t *testing.T) {
	// The bug this guards against: retryable is optional on the wire, so plain
	// decoding leaves it false. A peer that omits the field would have every
	// device.offline treated as permanent, and the core would stop retrying
	// sleeping devices. It would present as "some devices just never come
	// back" — an awful thing to diagnose from a bug report.
	tests := []struct {
		name string
		json string
		want bool
	}{
		{"omitted retryable code", `{"code":"device.offline","message":"asleep"}`, true},
		{"omitted non-retryable code", `{"code":"device.not_found","message":"gone"}`, false},
		{"explicit true", `{"code":"device.not_found","message":"gone","retryable":true}`, true},
		{"explicit false overrides the default", `{"code":"device.offline","message":"gone","retryable":false}`, false},
		{"unknown code defaults to not retryable", `{"code":"aqara.unknown","message":"?"}`, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var e Error
			if err := json.Unmarshal([]byte(tc.json), &e); err != nil {
				t.Fatalf("Unmarshal() = %v", err)
			}
			if e.Retryable != tc.want {
				t.Errorf("Retryable = %v, want %v", e.Retryable, tc.want)
			}
			if got := IsRetryable(&e); got != tc.want {
				t.Errorf("IsRetryable() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDefaultRetryableTable(t *testing.T) {
	// Spec §9.1. The distinction is whether waiting could change the answer.
	retryable := []Code{
		CodeDeviceOffline,
		CodePluginNotReady,
		CodePluginBusy,
		CodePluginInternal,
		CodeAuthExpired,
		CodeNetworkTimeout,
		CodeNetworkUnavailable,
	}
	notRetryable := []Code{
		CodeProtocolUnsupported,
		CodeProtocolMalformed,
		CodeDeviceNotFound,
		CodeDeviceUnsupported,
		CodeFeatureUnsupported,
		CodeAuthInvalidCredentials,
		CodePermissionDenied,
		CodeValidationBadParams,
	}

	for _, c := range retryable {
		if !DefaultRetryable(c) {
			t.Errorf("DefaultRetryable(%q) = false, want true", c)
		}
	}
	for _, c := range notRetryable {
		if DefaultRetryable(c) {
			t.Errorf("DefaultRetryable(%q) = true, want false", c)
		}
	}
}

func TestIsRetryableTransportErrors(t *testing.T) {
	// A call that never reached the far side can be retried: it did not
	// happen, and the supervisor is already reconnecting.
	if !IsRetryable(ErrNotConnected) {
		t.Error("IsRetryable(ErrNotConnected) = false, want true")
	}
	if !IsRetryable(fmt.Errorf("calling discover: %w", ErrNotConnected)) {
		t.Error("IsRetryable through wrapping = false, want true")
	}

	// A closed peer will not reconnect, so retrying is pointless.
	if IsRetryable(ErrPeerClosed) {
		t.Error("IsRetryable(ErrPeerClosed) = true, want false")
	}

	if IsRetryable(nil) {
		t.Error("IsRetryable(nil) = true, want false")
	}
	if IsRetryable(errors.New("something else")) {
		t.Error("IsRetryable of an unrelated error = true, want false")
	}
}

func TestCodeOf(t *testing.T) {
	if got := CodeOf(Errorf(CodeDeviceOffline, "x")); got != CodeDeviceOffline {
		t.Errorf("CodeOf() = %q, want %q", got, CodeDeviceOffline)
	}
	if got := CodeOf(fmt.Errorf("wrapped: %w", ErrPluginBusy)); got != CodePluginBusy {
		t.Errorf("CodeOf through wrapping = %q, want %q", got, CodePluginBusy)
	}
	if got := CodeOf(errors.New("plain")); got != "" {
		t.Errorf("CodeOf of a plain error = %q, want empty", got)
	}
	if got := CodeOf(nil); got != "" {
		t.Errorf("CodeOf(nil) = %q, want empty", got)
	}
}

func TestAsErrorClassifiesPlainErrors(t *testing.T) {
	// A handler returning a bare error has not thought about the taxonomy.
	// plugin.internal is the honest answer; guessing a more specific code from
	// the message would be worse than admitting we do not know.
	e := asError(errors.New("boom"))
	if e.Code != CodePluginInternal {
		t.Errorf("Code = %q, want %q", e.Code, CodePluginInternal)
	}
	if e.Message != "boom" {
		t.Errorf("Message = %q, want %q", e.Message, "boom")
	}
	if !e.Retryable {
		t.Error("plugin.internal should default to retryable")
	}

	// An error that already carries a code passes through untouched.
	orig := Errorf(CodeDeviceNotFound, "gone")
	if got := asError(fmt.Errorf("wrapped: %w", orig)); got != orig {
		t.Errorf("asError rewrote an error that already had a code: %v", got)
	}

	if asError(nil) != nil {
		t.Error("asError(nil) is not nil")
	}
}

func TestErrorRoundTripsThroughJSON(t *testing.T) {
	orig := Errorf(CodeDeviceNotFound, "node 3 not found in fabric").
		WithDetails(map[string]any{"nodeId": "3"})

	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}

	var got Error
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal() = %v", err)
	}
	if got.Code != orig.Code || got.Message != orig.Message || got.Retryable != orig.Retryable {
		t.Errorf("round trip changed the error: %+v -> %+v", orig, got)
	}
	if got.Details["nodeId"] != "3" {
		t.Errorf("details lost in the round trip: %v", got.Details)
	}
}

func TestNilErrorIsSafe(t *testing.T) {
	var e *Error
	if got := e.Error(); got != "<nil>" {
		t.Errorf("(*Error)(nil).Error() = %q", got)
	}
	if e.Is(ErrDeviceOffline) {
		t.Error("(*Error)(nil).Is() = true, want false")
	}
}
