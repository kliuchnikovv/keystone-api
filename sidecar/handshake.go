package sidecar

import (
	"context"
	"slices"
)

// Method names of the v1 inventory (spec §7). Constants so a typo is a compile
// error rather than a protocol.unsupported at runtime.
const (
	// Lifecycle.
	MethodHello     = "hello"
	MethodPing      = "ping"
	MethodShutdown  = "shutdown"
	MethodGetHealth = "getHealth"

	// Device operations.
	MethodDiscover     = "discover"
	MethodCommission   = "commission"
	MethodDecommission = "decommission"
	MethodReadState    = "readState"
	MethodWriteState   = "writeState"
	MethodInvokeAction = "invokeAction"

	// Subscriptions.
	MethodSubscribe   = "subscribe"
	MethodUnsubscribe = "unsubscribe"
	MethodResume      = "resume"

	// Configuration.
	MethodConfigFlow = "configFlow"
	MethodGetConfig  = "getConfig"
	MethodSetConfig  = "setConfig"
)

// Core-side features advertised in the hello response.
const (
	// FeatureSeqReplay tells the plugin the core tracks seq and will call
	// resume after a reconnect — so the plugin must keep its ring buffer.
	FeatureSeqReplay = "seq.replay"

	// FeatureTopicFiltering tells the plugin the core sends subscribe filters,
	// so unmatched pushes may be dropped before they are numbered.
	FeatureTopicFiltering = "topic.filtering"
)

// Hello is the handshake request (spec §5.1).
type Hello struct {
	Protocol     string   `json:"protocol"`
	Plugin       string   `json:"plugin"`
	Version      string   `json:"version"`
	Capabilities []string `json:"capabilities,omitempty"`
}

// HelloResult is the handshake response.
type HelloResult struct {
	Protocol string   `json:"protocol"`
	Core     string   `json:"core"`
	Session  string   `json:"session"`
	Features []string `json:"features,omitempty"`
}

// SessionInfo is what the handshake settled, from either side's point of view.
type SessionInfo struct {
	Session      string
	PeerName     string
	PeerVersion  string
	Capabilities []string
	Features     []string
}

// HasCapability reports whether the far side advertised a capability.
func (s SessionInfo) HasCapability(id string) bool {
	return slices.Contains(s.Capabilities, id)
}

// HasFeature reports whether the far side advertised a core feature.
func (s SessionInfo) HasFeature(id string) bool {
	return slices.Contains(s.Features, id)
}

// Established reports whether the handshake has completed.
func (s SessionInfo) Established() bool { return s.Session != "" }

// NegotiateProtocol checks a peer's declared version against what we speak.
//
// Only the major matters. Within one major the protocol changes additively, so
// a newer peer sends fields an older one ignores and everything keeps working
// (§5.2). Across majors nothing is assumed, which is why the mismatch is fatal
// rather than a warning.
func NegotiateProtocol(theirs string, supported ...string) error {
	if len(supported) == 0 {
		supported = []string{ProtocolV1}
	}
	if theirs == "" {
		return Errorf(CodeProtocolUnsupported, "peer did not declare a protocol version")
	}
	if slices.Contains(supported, theirs) {
		return nil
	}
	return Errorf(CodeProtocolUnsupported,
		"peer speaks protocol %s; this build speaks %v", theirs, supported)
}

// Hello performs the handshake as the initiating side, and records the result
// on the peer.
func (p *Peer) Hello(ctx context.Context, h Hello) (HelloResult, error) {
	if h.Protocol == "" {
		h.Protocol = ProtocolV1
	}

	var res HelloResult
	if err := p.Call(ctx, MethodHello, h, &res); err != nil {
		return res, err
	}
	if err := NegotiateProtocol(res.Protocol); err != nil {
		return res, err
	}

	p.setSession(SessionInfo{
		Session:      res.Session,
		PeerName:     res.Core,
		PeerVersion:  res.Core,
		Capabilities: h.Capabilities,
		Features:     res.Features,
	})
	return res, nil
}

// HelloHandler answers a hello on the accepting side.
//
// fn receives the far side's declaration and returns ours. The protocol check
// happens before fn is called, so a handler never has to think about version
// negotiation.
func HelloHandler(fn func(context.Context, Hello) (HelloResult, error)) Handler {
	return HandlerFunc(func(ctx context.Context, r *Request) (any, error) {
		var h Hello
		if err := r.Bind(&h); err != nil {
			return nil, err
		}
		if err := NegotiateProtocol(h.Protocol); err != nil {
			// The caller is expected to close the connection after this
			// response goes out (§5.2). Continuing would mean serving a peer
			// whose frames we have already said we cannot interpret.
			return nil, err
		}

		res, err := fn(ctx, h)
		if err != nil {
			return nil, err
		}
		if res.Protocol == "" {
			res.Protocol = ProtocolV1
		}
		if res.Session == "" {
			res.Session = "sess_" + NewID()
		}

		if peer := r.Peer(); peer != nil {
			peer.setSession(SessionInfo{
				Session:      res.Session,
				PeerName:     h.Plugin,
				PeerVersion:  h.Version,
				Capabilities: h.Capabilities,
				Features:     res.Features,
			})
		}
		return res, nil
	})
}

// PingResult is the heartbeat response body.
type PingResult struct {
	Pong bool `json:"pong"`
}

// PingHandler answers heartbeats. Register it on any peer that can be pinged.
var PingHandler Handler = HandlerFunc(func(context.Context, *Request) (any, error) {
	return PingResult{Pong: true}, nil
})

// SubscribeParams is the subscription filter set.
type SubscribeParams struct {
	Topics []Filter `json:"topics"`
}

// SubscribeResult acknowledges a subscribe.
type SubscribeResult struct {
	Subscribed bool `json:"subscribed"`
}

// UnsubscribeResult acknowledges an unsubscribe.
type UnsubscribeResult struct {
	Unsubscribed bool `json:"unsubscribed"`
}

// ResumeParams asks for replay from a position.
type ResumeParams struct {
	SinceSeq uint64 `json:"since_seq"`
}

// SubscriptionHandlers returns the subscribe, unsubscribe and resume handlers
// backed by a publisher.
//
// These are pure protocol plumbing with no plugin-specific content, so every
// plugin would otherwise write the same three handlers — and would have to get
// the resume ordering right, which is the fiddly part.
func SubscriptionHandlers(pub *Publisher) map[string]Handler {
	return map[string]Handler{
		MethodSubscribe: HandlerFunc(func(_ context.Context, r *Request) (any, error) {
			var params SubscribeParams
			if err := r.Bind(&params); err != nil {
				return nil, err
			}
			if err := pub.SetFilters(params.Topics...); err != nil {
				return nil, err
			}
			return SubscribeResult{Subscribed: true}, nil
		}),

		MethodUnsubscribe: HandlerFunc(func(context.Context, *Request) (any, error) {
			if err := pub.SetFilters(); err != nil {
				return nil, err
			}
			// The counter is deliberately not reset. A later subscribe
			// continues where this left off, so a resume across the quiet
			// period still lines up.
			return UnsubscribeResult{Unsubscribed: true}, nil
		}),

		MethodResume: HandlerFunc(func(_ context.Context, r *Request) (any, error) {
			var params ResumeParams
			if err := r.Bind(&params); err != nil {
				return nil, err
			}
			return pub.Resume(params.SinceSeq)
		}),
	}
}
