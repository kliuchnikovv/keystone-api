package sidecar

import (
	"encoding/json"
	"testing"
	"time"
)

func TestFrameValidate(t *testing.T) {
	raw := json.RawMessage(`{}`)
	ts := Now()

	tests := []struct {
		name  string
		frame Frame
		ok    bool
	}{
		{"request", Frame{Type: FrameRequest, ID: "1", Method: "ping"}, true},
		{"request with params", Frame{Type: FrameRequest, ID: "1", Method: "ping", Params: raw}, true},
		{"request without id", Frame{Type: FrameRequest, Method: "ping"}, false},
		{"request without method", Frame{Type: FrameRequest, ID: "1"}, false},
		{"request carrying a topic", Frame{Type: FrameRequest, ID: "1", Method: "ping", Topic: "state.a.b.c"}, false},

		{"response with result", Frame{Type: FrameResponse, ID: "1", Result: raw}, true},
		{"response with error", Frame{Type: FrameResponse, ID: "1", Error: ErrDeviceOffline}, true},
		{"response with both", Frame{Type: FrameResponse, ID: "1", Result: raw, Error: ErrDeviceOffline}, false},
		{"response with neither", Frame{Type: FrameResponse, ID: "1"}, false},
		{"response without id", Frame{Type: FrameResponse, Result: raw}, false},
		{"response carrying a method", Frame{Type: FrameResponse, ID: "1", Result: raw, Method: "ping"}, false},

		{"push", Frame{Type: FramePush, Topic: "state.matter:3.onoff.value", Seq: 1, TS: &ts}, true},
		{"push without seq", Frame{Type: FramePush, Topic: "state.matter:3.onoff.value", TS: &ts}, false},
		{"push without ts", Frame{Type: FramePush, Topic: "state.matter:3.onoff.value", Seq: 1}, false},
		{"push without topic", Frame{Type: FramePush, Seq: 1, TS: &ts}, false},
		{"push with a wildcard topic", Frame{Type: FramePush, Topic: "state.**", Seq: 1, TS: &ts}, false},
		{"push carrying an id", Frame{Type: FramePush, Topic: "state.a.b.c", Seq: 1, TS: &ts, ID: "1"}, false},

		{"no type", Frame{ID: "1"}, false},
		{"unknown type", Frame{Type: "notify", ID: "1"}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.frame.Validate()
			if tc.ok && err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
			if !tc.ok {
				if err == nil {
					t.Fatal("Validate() = nil, want an error")
				}
				if CodeOf(err) != CodeProtocolMalformed {
					t.Errorf("Validate() code = %q, want %q", CodeOf(err), CodeProtocolMalformed)
				}
			}
		})
	}
}

func TestResponseNullResultIsNotAbsent(t *testing.T) {
	// A method whose result is legitimately null must not look like a response
	// carrying neither result nor error. json.Unmarshal gives RawMessage the
	// four bytes "null" rather than nil, which is what makes the distinction
	// work — worth a test, because it is easy to "simplify" Validate into
	// treating them the same.
	var f Frame
	if err := json.Unmarshal([]byte(`{"type":"response","id":"1","result":null}`), &f); err != nil {
		t.Fatalf("Unmarshal() = %v", err)
	}
	if f.Result == nil {
		t.Fatal("a null result decoded to a nil RawMessage")
	}
	if err := f.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

func TestTimeMarshalsWithFixedPrecision(t *testing.T) {
	// The whole reason Time exists. Go's RFC3339Nano drops trailing zeros, so
	// a timestamp at an exact second would serialise as "...:11Z" here and as
	// "...:11.000Z" in JavaScript. Every cross-language golden fixture would
	// then differ for no semantic reason.
	tests := []struct {
		in   time.Time
		want string
	}{
		{time.Date(2026, 8, 15, 21, 4, 11, 123000000, time.UTC), `"2026-08-15T21:04:11.123Z"`},
		{time.Date(2026, 8, 15, 21, 4, 11, 0, time.UTC), `"2026-08-15T21:04:11.000Z"`},
		{time.Date(2026, 8, 15, 21, 4, 11, 100000000, time.UTC), `"2026-08-15T21:04:11.100Z"`},
		// Sub-millisecond precision is truncated, not rounded up: a timestamp
		// must never move forward past an event that really followed it.
		{time.Date(2026, 8, 15, 21, 4, 11, 123999999, time.UTC), `"2026-08-15T21:04:11.123Z"`},
		// Non-UTC input is normalised rather than rejected.
		{time.Date(2026, 8, 15, 23, 4, 11, 123000000, time.FixedZone("CEST", 2*3600)), `"2026-08-15T21:04:11.123Z"`},
	}

	for _, tc := range tests {
		b, err := json.Marshal(NewTime(tc.in))
		if err != nil {
			t.Fatalf("Marshal(%v) = %v", tc.in, err)
		}
		if string(b) != tc.want {
			t.Errorf("Marshal(%v) = %s, want %s", tc.in, b, tc.want)
		}
	}
}

func TestTimeUnmarshalIsLiberal(t *testing.T) {
	// Strict on output, liberal on input. A peer sending nanosecond precision
	// or a non-UTC offset is out of spec, but dropping a real event over a
	// formatting detail would be the wrong trade.
	inputs := []string{
		`"2026-08-15T21:04:11.123Z"`,
		`"2026-08-15T21:04:11.123456789Z"`,
		`"2026-08-15T21:04:11Z"`,
		`"2026-08-15T23:04:11.123+02:00"`,
	}
	want := time.Date(2026, 8, 15, 21, 4, 11, 123000000, time.UTC)

	for i, in := range inputs {
		var got Time
		if err := json.Unmarshal([]byte(in), &got); err != nil {
			t.Fatalf("Unmarshal(%s) = %v", in, err)
		}
		// The seconds-precision case has no milliseconds to compare.
		if i == 2 {
			if !got.Time().Equal(want.Truncate(time.Second)) {
				t.Errorf("Unmarshal(%s) = %v", in, got.Time())
			}
			continue
		}
		if !got.Time().Equal(want) {
			t.Errorf("Unmarshal(%s) = %v, want %v", in, got.Time(), want)
		}
	}

	var bad Time
	if err := json.Unmarshal([]byte(`"not a timestamp"`), &bad); err == nil {
		t.Error("Unmarshal of a non-timestamp = nil, want an error")
	}
}

func TestTimeZeroRoundTrip(t *testing.T) {
	var zero Time
	b, err := json.Marshal(zero)
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}
	if string(b) != "null" {
		t.Errorf("Marshal(zero Time) = %s, want null", b)
	}

	var got Time
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal() = %v", err)
	}
	if !got.IsZero() {
		t.Error("null did not decode back to the zero Time")
	}
}

func TestFrameOmitsEmptyFields(t *testing.T) {
	// Frames are read by humans under socat. A ping carrying eight null fields
	// would make a session transcript unreadable.
	b, err := json.Marshal(Frame{Type: FrameRequest, ID: "1", Method: "ping"})
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}
	want := `{"type":"request","id":"1","method":"ping"}`
	if string(b) != want {
		t.Errorf("Marshal() = %s, want %s", b, want)
	}
}
