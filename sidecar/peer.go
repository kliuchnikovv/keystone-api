package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"
)

// DefaultCallTimeout bounds a request whose caller supplied no deadline.
//
// Thirty seconds is generous for a device operation and short enough that a
// wedged plugin does not pin a goroutine forever. Commissioning legitimately
// takes longer and should pass its own context.
const DefaultCallTimeout = 30 * time.Second

// DefaultHandlerLimit bounds concurrent inbound handlers.
//
// Handlers block on hardware: a readState against a sleeping Zigbee device can
// take seconds. A serial dispatcher would let one such call stall the
// heartbeat and get the whole connection torn down as unresponsive, so
// handlers run concurrently — but not without bound, or a burst of requests
// against a slow transport would spawn goroutines without limit.
const DefaultHandlerLimit = 64

// Options configures a Peer.
type Options struct {
	// Handler answers inbound requests. A nil Handler answers every request
	// with protocol.unsupported, which is correct for a peer that only
	// consumes pushes.
	Handler Handler

	// OnPush receives inbound pushes. Called from the read pump, so it must
	// not block: hand off to a channel and return.
	OnPush func(ctx context.Context, p *Push)

	Logger *slog.Logger

	// CallTimeout defaults to DefaultCallTimeout.
	CallTimeout time.Duration

	// RingCapacity sizes this peer's outbound push buffer. Defaults to
	// DefaultRingCapacity.
	RingCapacity int

	// MaxLineBytes defaults to DefaultMaxLineBytes.
	MaxLineBytes int

	// WriteTimeout bounds a single frame write. Defaults to
	// DefaultWriteTimeout. Only takes effect on a connection that supports
	// deadlines, which every net.Conn does.
	WriteTimeout time.Duration

	// HandlerLimit defaults to DefaultHandlerLimit.
	HandlerLimit int

	// Clock defaults to SystemClock. Injected so tests can drive heartbeats
	// and timeouts without sleeping out the ten- and thirty-second intervals
	// the spec mandates.
	Clock Clock

	// NewID defaults to the package's ULID-shaped generator.
	NewID func() string
}

func (o *Options) withDefaults() {
	if o.CallTimeout <= 0 {
		o.CallTimeout = DefaultCallTimeout
	}
	if o.RingCapacity <= 0 {
		o.RingCapacity = DefaultRingCapacity
	}
	if o.HandlerLimit <= 0 {
		o.HandlerLimit = DefaultHandlerLimit
	}
	if o.WriteTimeout <= 0 {
		o.WriteTimeout = DefaultWriteTimeout
	}
	if o.Clock == nil {
		o.Clock = SystemClock{}
	}
	if o.NewID == nil {
		o.NewID = NewID
	}
	if o.Logger == nil {
		// Discard rather than the default logger: a library that writes to
		// stderr uninvited is a nuisance in a process that has its own
		// logging.
		o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
}

// Peer is one end of a protocol connection.
//
// There is no client type and no server type. A Peer can send requests and
// answer them; it can emit pushes and receive them. Which side does what is
// policy, expressed by what you pass in Options, not by which type you chose.
//
// The zero value is not usable; construct with NewPeer.
type Peer struct {
	conn io.ReadWriteCloser
	enc  *Encoder
	dec  *Decoder
	opts Options
	log  *slog.Logger

	pub *Publisher

	pendingMu sync.Mutex
	pending   map[string]chan *Frame

	// handlerSlots bounds concurrent inbound handlers.
	handlerSlots chan struct{}

	sessionMu sync.RWMutex
	session   SessionInfo

	closeOnce sync.Once
	done      chan struct{}

	errMu sync.Mutex
	err   error
}

// NewPeer wraps a connection.
//
// The connection is an io.ReadWriteCloser rather than a net.Conn so that a
// unix socket, a net.Pipe and an stdio pair are all equally usable. Tests run
// over net.Pipe; production runs over a unix socket; neither the peer nor a
// handler can tell the difference.
func NewPeer(conn io.ReadWriteCloser, opts Options) *Peer {
	opts.withDefaults()

	p := &Peer{
		conn:         conn,
		enc:          NewEncoder(conn, WithWriteTimeout(opts.WriteTimeout)),
		dec:          NewDecoder(conn, WithMaxLineBytes(opts.MaxLineBytes)),
		opts:         opts,
		log:          opts.Logger,
		pending:      make(map[string]chan *Frame),
		handlerSlots: make(chan struct{}, opts.HandlerLimit),
		done:         make(chan struct{}),
	}
	p.pub = newPublisher(p.enc, opts.RingCapacity, opts.Clock)
	return p
}

// Publisher exposes the outbound push machinery — the seq counter, the ring
// buffer and the subscription filter.
func (p *Peer) Publisher() *Publisher { return p.pub }

// Done is closed when the peer stops serving, for any reason.
func (p *Peer) Done() <-chan struct{} { return p.done }

// Err returns why the peer stopped, or nil if it is still running or was
// closed deliberately.
func (p *Peer) Err() error {
	p.errMu.Lock()
	defer p.errMu.Unlock()
	return p.err
}

// Session returns what the handshake agreed. Zero until hello completes.
func (p *Peer) Session() SessionInfo {
	p.sessionMu.RLock()
	defer p.sessionMu.RUnlock()
	return p.session
}

func (p *Peer) setSession(s SessionInfo) {
	p.sessionMu.Lock()
	p.session = s
	p.sessionMu.Unlock()
}

// Serve runs the read pump until the connection ends or ctx is cancelled.
//
// It returns nil for a clean shutdown — EOF, Close, or a cancelled context —
// and the underlying failure otherwise. Either way the peer is finished; a
// supervisor reconnects by constructing a new one.
func (p *Peer) Serve(ctx context.Context) error {
	defer p.teardown()

	// Cancelling ctx must wake a blocked Read, and a blocked Read cannot
	// observe a context. Closing the connection is what wakes it.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = p.conn.Close()
		case <-stop:
		}
	}()

	for {
		var f Frame
		err := p.dec.Decode(&f)
		switch {
		case err == nil:

		case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
			return nil

		case errors.Is(err, ErrLineTooLong):
			// Recoverable by design: the decoder has already skipped to the
			// next frame boundary. Dropping one oversized frame beats ending a
			// session that is carrying every device's state.
			p.log.Warn("sidecar: dropped an oversized frame")
			continue

		case CodeOf(err) == CodeProtocolMalformed:
			// One bad line does not condemn the stream either. The far side
			// has a bug; log it and keep going, so a single malformed push
			// does not take out the connection.
			p.log.Warn("sidecar: dropped a malformed frame", "err", err)
			continue

		default:
			if ctx.Err() != nil || p.isClosed() {
				return nil
			}
			p.setErr(err)
			return err
		}

		if err := f.Validate(); err != nil {
			p.log.Warn("sidecar: dropped an invalid frame", "err", err, "frame", f.String())
			continue
		}
		p.route(ctx, &f)
	}
}

// route dispatches one decoded frame.
func (p *Peer) route(ctx context.Context, f *Frame) {
	switch f.Type {
	case FrameRequest:
		p.serveRequest(ctx, f)

	case FrameResponse:
		p.pendingMu.Lock()
		ch, ok := p.pending[f.ID]
		p.pendingMu.Unlock()
		if !ok {
			// A reply to a call that already gave up, or a duplicate. Nothing
			// to do but say so.
			p.log.Warn("sidecar: response with no waiter", "id", f.ID)
			return
		}
		clone := *f
		select {
		case ch <- &clone:
		default:
			// The channel is buffered to 1 and we are its only sender, so this
			// means the far side replied twice to one id.
			p.log.Warn("sidecar: duplicate response", "id", f.ID)
		}

	case FramePush:
		if p.opts.OnPush == nil {
			return
		}
		push := &Push{Topic: f.Topic, Seq: f.Seq, Payload: f.Payload}
		if f.TS != nil {
			push.TS = *f.TS
		}
		p.opts.OnPush(ctx, push)
	}
}

// serveRequest runs a handler and writes its response.
func (p *Peer) serveRequest(ctx context.Context, f *Frame) {
	req := &Request{ID: f.ID, Method: f.Method, Params: f.Params, peer: p}

	// Bound concurrency without dropping work: if every slot is taken, wait
	// for one. Refusing the request instead would turn a busy plugin into a
	// failing one, and the far side has no way to tell those apart.
	select {
	case p.handlerSlots <- struct{}{}:
	case <-p.done:
		return
	case <-ctx.Done():
		return
	}

	go func() {
		defer func() { <-p.handlerSlots }()

		result, err := p.dispatch(ctx, req)

		resp := Frame{Type: FrameResponse, ID: req.ID}
		if err != nil {
			resp.Error = asError(err)
		} else {
			raw, merr := json.Marshal(orEmptyObject(result))
			if merr != nil {
				// The handler produced something unserialisable. Answering
				// with an error is the only option that does not hang the
				// caller.
				p.log.Error("sidecar: handler result would not marshal",
					"method", req.Method, "err", merr)
				resp.Error = Errorf(CodePluginInternal, "result for %s could not be encoded: %v", req.Method, merr)
			} else {
				resp.Result = raw
			}
		}

		if werr := p.enc.Encode(&resp); werr != nil && !p.isClosed() {
			p.log.Warn("sidecar: could not write response", "id", req.ID, "err", werr)
		}
	}()
}

// dispatch runs the handler, converting a panic into an error response.
//
// A plugin author's nil map dereference should fail that one call, not take
// down the connection and with it every other device in the house.
func (p *Peer) dispatch(ctx context.Context, req *Request) (result any, err error) {
	defer func() {
		if r := recover(); r != nil {
			p.log.Error("sidecar: handler panicked", "method", req.Method, "panic", r)
			result, err = nil, Errorf(CodePluginInternal, "handler for %s panicked: %v", req.Method, r)
		}
	}()

	if p.opts.Handler == nil {
		return nil, Errorf(CodeProtocolUnsupported, "method %q is not implemented", req.Method)
	}
	return p.opts.Handler.HandleRequest(ctx, req)
}

// Call sends a request and waits for its response.
//
// result may be nil if the caller does not care about the payload. A failure
// reported by the far side is returned as *Error, so errors.Is against the
// sentinels and IsRetryable both work on it.
func (p *Peer) Call(ctx context.Context, method string, params, result any) error {
	if p.isClosed() {
		return ErrNotConnected
	}

	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.opts.CallTimeout)
		defer cancel()
	}

	id := p.opts.NewID()
	reply := make(chan *Frame, 1)

	p.pendingMu.Lock()
	p.pending[id] = reply
	p.pendingMu.Unlock()

	// Every exit path removes the entry, including the ones that return early
	// below. A leaked entry would keep the map growing for the life of the
	// connection and would deliver a late reply to a channel nobody reads.
	defer func() {
		p.pendingMu.Lock()
		delete(p.pending, id)
		p.pendingMu.Unlock()
	}()

	frame := Frame{Type: FrameRequest, ID: id, Method: method}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return Errorf(CodeValidationBadParams, "params for %s could not be encoded: %v", method, err)
		}
		frame.Params = raw
	}
	if err := asNotConnected(p.enc.Encode(&frame)); err != nil {
		if p.isClosed() {
			return ErrNotConnected
		}
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()

	case <-p.done:
		return ErrNotConnected

	case resp := <-reply:
		if resp.Error != nil {
			return resp.Error
		}
		if result == nil || len(resp.Result) == 0 {
			return nil
		}
		if err := json.Unmarshal(resp.Result, result); err != nil {
			return Errorf(CodeProtocolMalformed, "result of %s could not be decoded: %v", method, err)
		}
		return nil
	}
}

// Publish emits a push, subject to the far side's subscription filter.
func (p *Peer) Publish(topic Topic, payload any) error {
	return p.pub.Publish(topic, payload)
}

// Close shuts the peer down. Idempotent, and safe from any goroutine.
func (p *Peer) Close() error {
	p.teardown()
	return nil
}

func (p *Peer) teardown() {
	p.closeOnce.Do(func() {
		close(p.done)
		_ = p.conn.Close()

		// Every in-flight Call is already selecting on p.done, so closing the
		// channel above is what wakes them — immediately, rather than each
		// waiting out its own CallTimeout.
		//
		// The pending entries are dropped but deliberately NOT closed. Closing
		// a channel that route may be sending on at that instant is a data
		// race, and it buys nothing: done is the signal, and each Call removes
		// its own entry on the way out.
		p.pendingMu.Lock()
		clear(p.pending)
		p.pendingMu.Unlock()
	})
}

func (p *Peer) isClosed() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func (p *Peer) setErr(err error) {
	p.errMu.Lock()
	if p.err == nil {
		p.err = err
	}
	p.errMu.Unlock()
}

// orEmptyObject keeps a nil handler result off the wire as {} rather than
// null. Most methods return nothing, and "result":{} reads better in a
// transcript than "result":null — and matches what the schemas describe.
func orEmptyObject(v any) any {
	if v == nil {
		return struct{}{}
	}
	return v
}
