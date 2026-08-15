package sidecar

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// DefaultMaxLineBytes bounds a single frame.
//
// A megabyte is far more than any legitimate frame — the largest realistic one
// is a discover result for a house full of devices — and the bound is what
// stops a peer that never sends a newline from growing our buffer without
// limit.
const DefaultMaxLineBytes = 1 << 20

// ErrLineTooLong reports a frame over the size limit. It is recoverable: the
// decoder discards the rest of that line and resynchronises on the next
// newline, so one oversized frame does not end the session.
var ErrLineTooLong = errors.New("sidecar: frame exceeds the maximum line length")

// Decoder reads NDJSON frames from a stream.
type Decoder struct {
	r       *bufio.Reader
	maxLine int
	buf     bytes.Buffer
}

// DecoderOption configures a Decoder.
type DecoderOption func(*Decoder)

// WithMaxLineBytes overrides DefaultMaxLineBytes.
func WithMaxLineBytes(n int) DecoderOption {
	return func(d *Decoder) {
		if n > 0 {
			d.maxLine = n
		}
	}
}

// NewDecoder wraps a reader.
func NewDecoder(r io.Reader, opts ...DecoderOption) *Decoder {
	d := &Decoder{
		r:       bufio.NewReaderSize(r, 8<<10),
		maxLine: DefaultMaxLineBytes,
	}
	for _, o := range opts {
		o(d)
	}
	return d
}

// Decode reads one frame.
//
// It returns io.EOF at a clean end of stream, ErrLineTooLong for an oversized
// frame (after resynchronising), and a *Error with code protocol.malformed for
// a line that is not a valid frame.
//
// The implementation reads with bufio.Reader.ReadSlice rather than the two
// obvious alternatives, for reasons that both bite in production:
//
// bufio.Scanner returns ErrTooLong permanently — the scanner is finished, and
// a single oversized frame would end an otherwise healthy session.
//
// json.Decoder is worse: it happily accepts a JSON value spread over several
// lines, so a peer emitting pretty-printed JSON would appear to work while
// producing a stream that is not NDJSON. That breaks every other reader,
// including `socat`, and the protocol's debuggability is a stated goal (§2)
// rather than a nicety.
func (d *Decoder) Decode(f *Frame) error {
	line, err := d.readLine()
	if err != nil {
		return err
	}
	if len(line) == 0 {
		// Blank lines are not frames. Tolerating them costs nothing and makes
		// a hand-written or shell-generated stream work as expected.
		return d.Decode(f)
	}

	*f = Frame{}
	if err := json.Unmarshal(line, f); err != nil {
		return Errorf(CodeProtocolMalformed, "frame is not valid JSON: %v", err)
	}
	return nil
}

// readLine returns one line without its terminator, or ErrLineTooLong having
// consumed and discarded the oversized line.
func (d *Decoder) readLine() ([]byte, error) {
	d.buf.Reset()
	for {
		chunk, err := d.r.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			if d.buf.Len()+len(chunk) > d.maxLine {
				if derr := d.discardLine(); derr != nil {
					return nil, derr
				}
				return nil, ErrLineTooLong
			}
			d.buf.Write(chunk)
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) && (d.buf.Len() > 0 || len(chunk) > 0) {
				// A final frame with no trailing newline. Legal enough: the
				// peer is closing and this is the last thing it had to say.
				d.buf.Write(chunk)
				return bytes.TrimRight(d.buf.Bytes(), "\r\n"), nil
			}
			return nil, err
		}
		if d.buf.Len()+len(chunk) > d.maxLine {
			return nil, ErrLineTooLong
		}
		d.buf.Write(chunk)
		return bytes.TrimRight(d.buf.Bytes(), "\r\n"), nil
	}
}

// discardLine consumes up to and including the next newline, so the stream is
// positioned at a frame boundary again.
func (d *Decoder) discardLine() error {
	for {
		_, err := d.r.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return err
	}
}

// Encoder writes NDJSON frames to a stream.
//
// Safe for concurrent use, and it has to be: the runtime writes responses from
// handler goroutines, pushes from wherever the plugin produces them, and
// heartbeats from a timer. A partially interleaved write would corrupt the
// stream permanently — unlike WebSocket, NDJSON has no external framing, so
// there is no resynchronisation point once two frames are spliced together.
// Every frame is marshalled fully, then written in exactly one Write under
// the mutex.
type Encoder struct {
	mu sync.Mutex
	w  io.Writer

	// Reused between frames. Encoding happens on every push, and the buffer is
	// already serialised by the mutex.
	buf bytes.Buffer
}

// NewEncoder wraps a writer.
func NewEncoder(w io.Writer) *Encoder { return &Encoder{w: w} }

// Encode marshals and writes a frame followed by a newline.
func (e *Encoder) Encode(f *Frame) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.buf.Reset()
	enc := json.NewEncoder(&e.buf)
	// SetEscapeHTML(false) keeps the output readable under `socat`: the
	// default turns < > & into <-style escapes, which is pointless here
	// and makes device names and log messages hard to read.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(f); err != nil {
		return fmt.Errorf("sidecar: encoding %s: %w", f, err)
	}

	// json.Encoder.Encode already appended the newline.
	if _, err := e.w.Write(e.buf.Bytes()); err != nil {
		return err
	}
	if flusher, ok := e.w.(interface{ Flush() error }); ok {
		return flusher.Flush()
	}
	return nil
}
