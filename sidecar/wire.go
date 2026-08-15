package sidecar

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ProtocolV1 is the protocol major version this package speaks. It is what
// goes in the hello handshake, and the only thing compatibility is decided by
// (spec §5.2).
const ProtocolV1 = "v1"

// FrameType discriminates the three kinds of message on the wire.
type FrameType string

const (
	FrameRequest  FrameType = "request"
	FrameResponse FrameType = "response"
	FramePush     FrameType = "push"
)

// Frame is any single message. One NDJSON line is exactly one Frame.
//
// The three message kinds share one struct rather than having three types with
// a discriminating UnmarshalJSON. That would mean decoding each line twice —
// once to peek at "type", once for real — on a path that carries every state
// change in the house.
//
// Params, Result and Payload stay as json.RawMessage so a Peer can route a
// frame without knowing what is inside it. Decoding into map[string]any here
// would allocate on every push and then force the eventual consumer to
// re-encode and re-decode to get a typed value.
type Frame struct {
	Type FrameType `json:"type"`

	// Request and response.
	ID     string          `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`

	// Push.
	Topic Topic  `json:"topic,omitempty"`
	Seq   uint64 `json:"seq,omitempty"`
	// TS is a pointer because encoding/json's omitempty does nothing for a
	// struct-typed field: as a value, every request and response frame would
	// serialise a redundant "ts":null. Frames are read by humans under socat,
	// and Go 1.24's omitzero is not available to us while the module targets
	// 1.23.
	TS      *Time           `json:"ts,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Timestamp returns the frame's time, or the zero time if it has none.
func (f *Frame) Timestamp() time.Time {
	if f.TS == nil {
		return time.Time{}
	}
	return f.TS.Time()
}

// Validate checks the structural rules of spec §4 — the ones that hold for
// every frame regardless of method or topic.
//
// This is not schema validation. It is the cheap always-on check that lets the
// runtime trust the fields it is about to read; the schemas under
// sidecar/schema are the thorough, optional layer on top.
func (f *Frame) Validate() error {
	switch f.Type {
	case FrameRequest:
		if f.ID == "" {
			return Errorf(CodeProtocolMalformed, "request frame has no id")
		}
		if f.Method == "" {
			return Errorf(CodeProtocolMalformed, "request %s has no method", f.ID)
		}
		if f.Result != nil || f.Error != nil || f.Topic != "" || f.Seq != 0 {
			return Errorf(CodeProtocolMalformed, "request %s carries response or push fields", f.ID)
		}

	case FrameResponse:
		if f.ID == "" {
			return Errorf(CodeProtocolMalformed, "response frame has no id")
		}
		// Exactly one of result / error. A JSON null result decodes to the
		// four bytes "null" rather than a nil RawMessage, so an explicit null
		// result is correctly distinguished from an absent one.
		hasResult, hasError := f.Result != nil, f.Error != nil
		if hasResult == hasError {
			if hasResult {
				return Errorf(CodeProtocolMalformed, "response %s carries both result and error", f.ID)
			}
			return Errorf(CodeProtocolMalformed, "response %s carries neither result nor error", f.ID)
		}
		if f.Method != "" || f.Topic != "" || f.Seq != 0 {
			return Errorf(CodeProtocolMalformed, "response %s carries request or push fields", f.ID)
		}

	case FramePush:
		if f.Topic == "" {
			return Errorf(CodeProtocolMalformed, "push frame has no topic")
		}
		if !f.Topic.Valid() {
			return Errorf(CodeProtocolMalformed, "push topic %q is malformed", f.Topic)
		}
		if f.Seq == 0 {
			return Errorf(CodeProtocolMalformed, "push on %s has no seq", f.Topic)
		}
		if f.TS == nil || f.TS.IsZero() {
			return Errorf(CodeProtocolMalformed, "push on %s has no ts", f.Topic)
		}
		if f.ID != "" || f.Method != "" || f.Result != nil || f.Error != nil {
			return Errorf(CodeProtocolMalformed, "push on %s carries request or response fields", f.Topic)
		}

	case "":
		return Errorf(CodeProtocolMalformed, "frame has no type")

	default:
		return Errorf(CodeProtocolMalformed, "unknown frame type %q", f.Type)
	}
	return nil
}

// String renders a frame for logs: enough to follow a session, short enough to
// leave on in production.
func (f *Frame) String() string {
	switch f.Type {
	case FrameRequest:
		return fmt.Sprintf("request %s %s", f.ID, f.Method)
	case FrameResponse:
		if f.Error != nil {
			return fmt.Sprintf("response %s error %s", f.ID, f.Error.Code)
		}
		return fmt.Sprintf("response %s ok", f.ID)
	case FramePush:
		return fmt.Sprintf("push #%d %s", f.Seq, f.Topic)
	default:
		return fmt.Sprintf("frame %q", f.Type)
	}
}

// timeLayout is RFC 3339 in UTC with exactly three fractional digits.
//
// The fixed precision is load-bearing, not cosmetic. Go's time.Time marshals
// RFC3339Nano, which drops trailing zeros and so varies in width; JavaScript's
// toISOString always emits three digits. Left alone, the two implementations
// serialise the same instant differently and every cross-language golden
// fixture diverges for no semantic reason.
const timeLayout = "2006-01-02T15:04:05.000Z"

// Time is a wire timestamp: RFC 3339, UTC, millisecond precision.
type Time time.Time

// Now returns the current time truncated to the wire's precision, so a value
// survives a round-trip unchanged.
func Now() Time { return Time(time.Now().UTC().Truncate(time.Millisecond)) }

// NewTime converts a time.Time, normalising it to UTC and milliseconds.
func NewTime(t time.Time) Time { return Time(t.UTC().Truncate(time.Millisecond)) }

// Time returns the underlying time.Time.
func (t Time) Time() time.Time { return time.Time(t) }

// IsZero reports whether the timestamp is unset.
func (t Time) IsZero() bool { return time.Time(t).IsZero() }

func (t Time) String() string { return time.Time(t).UTC().Format(timeLayout) }

func (t Time) MarshalJSON() ([]byte, error) {
	if t.IsZero() {
		return []byte("null"), nil
	}
	b := make([]byte, 0, len(timeLayout)+2)
	b = append(b, '"')
	b = time.Time(t).UTC().AppendFormat(b, timeLayout)
	return append(b, '"'), nil
}

func (t *Time) UnmarshalJSON(b []byte) error {
	s := string(b)
	if s == "null" {
		*t = Time{}
		return nil
	}
	s = strings.Trim(s, `"`)

	parsed, err := time.Parse(timeLayout, s)
	if err != nil {
		// Be liberal on the way in. A peer that sends RFC3339Nano, or seconds
		// precision, or a non-UTC offset is technically out of spec, but
		// rejecting the frame would lose a real event over a formatting
		// detail. We normalise and carry on; the schema validator is where
		// that gets reported if anyone cares.
		parsed, err = time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return fmt.Errorf("sidecar: %q is not an RFC 3339 timestamp: %w", s, err)
		}
	}
	*t = NewTime(parsed)
	return nil
}
