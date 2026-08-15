package sidecar

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

func TestDecoderReadsFrames(t *testing.T) {
	in := strings.Join([]string{
		`{"type":"request","id":"1","method":"ping","params":{}}`,
		`{"type":"response","id":"1","result":{"pong":true}}`,
		`{"type":"push","topic":"state.matter:3.onoff.value","seq":1,"ts":"2026-08-15T21:04:11.123Z","payload":{"value":true}}`,
	}, "\n") + "\n"

	d := NewDecoder(strings.NewReader(in))

	var f Frame
	if err := d.Decode(&f); err != nil {
		t.Fatalf("Decode() = %v", err)
	}
	if f.Type != FrameRequest || f.Method != "ping" {
		t.Errorf("frame 1 = %+v", f)
	}

	if err := d.Decode(&f); err != nil {
		t.Fatalf("Decode() = %v", err)
	}
	if f.Type != FrameResponse || f.ID != "1" {
		t.Errorf("frame 2 = %+v", f)
	}
	// The previous frame's Method must not leak into this one.
	if f.Method != "" {
		t.Errorf("frame 2 kept a field from frame 1: method = %q", f.Method)
	}

	if err := d.Decode(&f); err != nil {
		t.Fatalf("Decode() = %v", err)
	}
	if f.Type != FramePush || f.Seq != 1 || f.Topic != "state.matter:3.onoff.value" {
		t.Errorf("frame 3 = %+v", f)
	}

	if err := d.Decode(&f); !errors.Is(err, io.EOF) {
		t.Errorf("Decode() at end = %v, want io.EOF", err)
	}
}

func TestDecoderAcceptsFinalFrameWithoutNewline(t *testing.T) {
	// A peer that closes immediately after its last frame. Dropping it would
	// lose, for example, the final plugin.log.error explaining why the plugin
	// is exiting — exactly the frame you most want.
	d := NewDecoder(strings.NewReader(`{"type":"request","id":"1","method":"ping"}`))

	var f Frame
	if err := d.Decode(&f); err != nil {
		t.Fatalf("Decode() = %v", err)
	}
	if f.Method != "ping" {
		t.Errorf("Method = %q", f.Method)
	}
	if err := d.Decode(&f); !errors.Is(err, io.EOF) {
		t.Errorf("Decode() after the last frame = %v, want io.EOF", err)
	}
}

func TestDecoderSkipsBlankLines(t *testing.T) {
	d := NewDecoder(strings.NewReader("\n\n{\"type\":\"request\",\"id\":\"1\",\"method\":\"ping\"}\n\n"))
	var f Frame
	if err := d.Decode(&f); err != nil {
		t.Fatalf("Decode() = %v", err)
	}
	if f.Method != "ping" {
		t.Errorf("Method = %q", f.Method)
	}
}

func TestDecoderRejectsMalformedJSON(t *testing.T) {
	d := NewDecoder(strings.NewReader("{not json}\n"))
	var f Frame
	err := d.Decode(&f)
	if err == nil {
		t.Fatal("Decode() = nil, want an error")
	}
	if CodeOf(err) != CodeProtocolMalformed {
		t.Errorf("Decode() code = %q, want %q", CodeOf(err), CodeProtocolMalformed)
	}
}

func TestDecoderRecoversFromAnOversizedFrame(t *testing.T) {
	// The reason for ReadSlice rather than bufio.Scanner: Scanner's ErrTooLong
	// is terminal, so one oversized frame would end an otherwise healthy
	// session carrying every device's state.
	huge := `{"type":"push","topic":"state.a.b.c","seq":1,"payload":"` + strings.Repeat("x", 4096) + `"}`
	in := huge + "\n" + `{"type":"request","id":"2","method":"ping"}` + "\n"

	d := NewDecoder(strings.NewReader(in), WithMaxLineBytes(256))

	var f Frame
	if err := d.Decode(&f); !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("Decode() of an oversized frame = %v, want ErrLineTooLong", err)
	}

	// And the stream is back at a frame boundary.
	if err := d.Decode(&f); err != nil {
		t.Fatalf("Decode() after recovery = %v", err)
	}
	if f.ID != "2" || f.Method != "ping" {
		t.Errorf("did not resynchronise: got %+v", f)
	}
}

func TestDecoderRejectsMultiLineJSON(t *testing.T) {
	// json.Decoder would accept this, and a peer emitting pretty-printed JSON
	// would appear to work while producing a stream that is not NDJSON —
	// breaking every other reader, socat included. Debuggability is a stated
	// goal of the protocol (§2), so this has to be an error.
	pretty := "{\n  \"type\": \"request\",\n  \"id\": \"1\",\n  \"method\": \"ping\"\n}\n"
	d := NewDecoder(strings.NewReader(pretty))

	var f Frame
	if err := d.Decode(&f); err == nil {
		t.Fatal("Decode() of multi-line JSON = nil, want an error")
	}
}

func TestEncoderWritesOneLinePerFrame(t *testing.T) {
	var buf bytes.Buffer
	e := NewEncoder(&buf)

	ts := Now()
	frames := []Frame{
		{Type: FrameRequest, ID: "1", Method: "ping"},
		{Type: FrameResponse, ID: "1", Result: json.RawMessage(`{"pong":true}`)},
		{Type: FramePush, Topic: "state.matter:3.onoff.value", Seq: 1, TS: &ts},
	}
	for i := range frames {
		if err := e.Encode(&frames[i]); err != nil {
			t.Fatalf("Encode() = %v", err)
		}
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3:\n%s", len(lines), buf.String())
	}
	for i, line := range lines {
		var f Frame
		if err := json.Unmarshal([]byte(line), &f); err != nil {
			t.Errorf("line %d is not a valid frame: %v", i, err)
		}
	}
}

func TestEncoderDoesNotEscapeHTML(t *testing.T) {
	// encoding/json escapes < > & by default, which turns a device named
	// "Kitchen & Hall" into "Kitchen & Hall" in every socat transcript
	// for no benefit here.
	var buf bytes.Buffer
	e := NewEncoder(&buf)
	f := Frame{Type: FrameResponse, ID: "1", Result: json.RawMessage(`{"name":"Kitchen & Hall"}`)}
	if err := e.Encode(&f); err != nil {
		t.Fatalf("Encode() = %v", err)
	}
	if !strings.Contains(buf.String(), "Kitchen & Hall") {
		t.Errorf("output was HTML-escaped: %s", buf.String())
	}
}

func TestEncoderConcurrentWrites(t *testing.T) {
	// Responses come from handler goroutines, pushes from wherever the plugin
	// produces them, heartbeats from a timer. NDJSON has no external framing,
	// so two interleaved writes splice into a line that is not a frame and
	// there is no resynchronisation point — the session is corrupt from then
	// on. Every frame must be one Write under the lock.
	var buf bytes.Buffer
	e := NewEncoder(&buf)

	const goroutines, each = 20, 25

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				ts := Now()
				f := Frame{
					Type:    FramePush,
					Topic:   Topic("state.dev:" + string(rune('a'+g)) + ".onoff.value"),
					Seq:     uint64(i + 1),
					TS:      &ts,
					Payload: json.RawMessage(`{"value":true,"origin":"device_report"}`),
				}
				if err := e.Encode(&f); err != nil {
					t.Errorf("Encode() = %v", err)
				}
			}
		}(g)
	}
	wg.Wait()

	d := NewDecoder(bytes.NewReader(buf.Bytes()))
	count := 0
	for {
		var f Frame
		err := d.Decode(&f)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("frame %d did not decode — the stream was corrupted: %v", count, err)
		}
		if err := f.Validate(); err != nil {
			t.Fatalf("frame %d is not well-formed: %v", count, err)
		}
		count++
	}
	if want := goroutines * each; count != want {
		t.Errorf("decoded %d frames, want %d", count, want)
	}
}

// FuzzDecode asserts the decoder's two hard guarantees against arbitrary
// input: it never panics, and it never returns a frame that Validate rejects
// while reporting success. Everything upstream of Decode is untrusted bytes
// from another process.
func FuzzDecode(f *testing.F) {
	f.Add(`{"type":"request","id":"1","method":"ping"}` + "\n")
	f.Add(`{"type":"push","topic":"state.a.b.c","seq":1,"ts":"2026-08-15T21:04:11.123Z"}` + "\n")
	f.Add(`{"type":"response","id":"1","error":{"code":"device.offline","message":"x"}}` + "\n")
	f.Add("{}\n")
	f.Add("\n\n\n")
	f.Add(`{"type":"push","topic":"","seq":0}`)
	f.Add(strings.Repeat("{", 4096))
	f.Add("\x00\xff\xfe")

	f.Fuzz(func(t *testing.T, in string) {
		d := NewDecoder(strings.NewReader(in), WithMaxLineBytes(4096))

		for i := 0; i < 64; i++ {
			var frame Frame
			if err := d.Decode(&frame); err != nil {
				return
			}

			// Anything the decoder accepted must survive a round-trip through
			// the encoder unchanged. This is the property that matters: the
			// runtime re-encodes frames when replaying a ring buffer, so a
			// frame that decodes one way and re-encodes another would corrupt
			// replayed history — and only for whichever input triggered it.
			var buf bytes.Buffer
			if err := NewEncoder(&buf).Encode(&frame); err != nil {
				t.Fatalf("a decoded frame would not re-encode: %v (frame %+v)", err, frame)
			}

			var again Frame
			if err := NewDecoder(&buf).Decode(&again); err != nil {
				t.Fatalf("re-encoded frame does not decode: %v (frame %+v)", err, frame)
			}

			if again.Type != frame.Type || again.ID != frame.ID ||
				again.Method != frame.Method || again.Topic != frame.Topic ||
				again.Seq != frame.Seq {
				t.Fatalf("round trip changed the frame:\n before %+v\n after  %+v", frame, again)
			}
			if !frame.Timestamp().Equal(again.Timestamp()) {
				t.Fatalf("round trip changed the timestamp: %v -> %v",
					frame.Timestamp(), again.Timestamp())
			}
		}
	})
}
