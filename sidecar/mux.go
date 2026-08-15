package sidecar

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
)

// Request is an inbound method call.
type Request struct {
	ID     string
	Method string
	Params json.RawMessage

	peer *Peer
}

// Bind decodes the parameters into v.
//
// An absent params field binds as an empty object rather than failing, so a
// method whose parameters are all optional works whether the caller sent
// "params":{} or nothing at all.
func (r *Request) Bind(v any) error {
	if v == nil {
		return nil
	}
	raw := r.Params
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return Errorf(CodeValidationBadParams, "params for %s are not valid: %v", r.Method, err)
	}
	return nil
}

// Peer returns the connection this request arrived on.
//
// This is where the protocol's symmetry becomes usable rather than
// theoretical: a handler can publish a push or call a method back on the same
// connection while it is still deciding what to answer. A commissioning
// handler uses it to report progress; a config-flow handler uses it to push
// live updates behind a progress step.
func (r *Request) Peer() *Peer { return r.peer }

// Push is an inbound asynchronous message.
type Push struct {
	Topic   Topic
	Seq     uint64
	TS      Time
	Payload json.RawMessage
}

// Bind decodes the payload into v.
func (p *Push) Bind(v any) error {
	if v == nil {
		return nil
	}
	raw := p.Payload
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return Errorf(CodeProtocolMalformed, "payload for %s is not valid: %v", p.Topic, err)
	}
	return nil
}

// Handler answers inbound requests.
//
// A nil result is encoded as an empty object, which is what most methods
// return. Returning an error that is not an *Error classifies it as
// plugin.internal — see asError.
type Handler interface {
	HandleRequest(ctx context.Context, r *Request) (any, error)
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(ctx context.Context, r *Request) (any, error)

func (f HandlerFunc) HandleRequest(ctx context.Context, r *Request) (any, error) {
	return f(ctx, r)
}

// Mux routes requests by method name.
type Mux struct {
	mu       sync.RWMutex
	handlers map[string]Handler
}

// NewMux returns an empty Mux.
func NewMux() *Mux {
	return &Mux{handlers: make(map[string]Handler)}
}

// Handle registers a handler, replacing any previous one for that method.
//
// Replacing rather than panicking on a duplicate is deliberate: RunPlugin
// installs default handlers for hello, ping, subscribe, unsubscribe and
// resume, and a plugin with a good reason to take one over should be able to
// simply register its own.
func (m *Mux) Handle(method string, h Handler) *Mux {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handlers[method] = h
	return m
}

// HandleFunc registers a function.
func (m *Mux) HandleFunc(method string, fn HandlerFunc) *Mux {
	return m.Handle(method, fn)
}

// Methods lists the registered method names, sorted.
func (m *Mux) Methods() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.handlers))
	for k := range m.handlers {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// HandleRequest implements Handler.
//
// An unregistered method is answered with protocol.unsupported rather than
// ignored. The caller is blocked waiting for a response with this id, and
// silence would hang it until its context expires — turning "I do not
// implement that" into a timeout, which reads like a dead plugin.
func (m *Mux) HandleRequest(ctx context.Context, r *Request) (any, error) {
	m.mu.RLock()
	h, ok := m.handlers[r.Method]
	m.mu.RUnlock()

	if !ok {
		return nil, Errorf(CodeProtocolUnsupported, "method %q is not implemented", r.Method)
	}
	return h.HandleRequest(ctx, r)
}
