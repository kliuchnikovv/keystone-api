// Command conformance is one half of the cross-language conformance harness.
//
// It runs a Go peer in either role against a TypeScript peer in the other,
// over a real unix socket, replaying the full session transcript from spec
// §12: handshake, subscribe, discover, pushes, a hard drop, reconnect, resume
// with replay, heartbeat, shutdown.
//
// Running it in both directions is the point. Go-as-core against
// TS-as-plugin exercises the arrangement production uses; the reverse is what
// actually proves the symmetry claim, because a peer that only ever answers
// would pass the first direction with half the implementation missing.
//
// Usage:
//
//	conformance -role=plugin -socket=/tmp/x.sock
//	conformance -role=core   -socket=/tmp/x.sock
//
// It exits non-zero with a description on the first violation. The
// counterpart lives in ts/sidecar/test/conformance.mjs.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/kliuchnikovv/keystone-api/sidecar"
)

// The devices the plugin role reports, so the core role can assert on them.
var discovered = []map[string]any{
	{"deviceRef": "matter:2", "type": "light", "name": "Kitchen"},
	{"deviceRef": "matter:5", "type": "motion_sensor", "name": "Hall"},
}

func main() {
	role := flag.String("role", "plugin", "plugin or core")
	socket := flag.String("socket", "", "unix socket path")
	timeout := flag.Duration("timeout", 30*time.Second, "overall deadline")
	flag.Parse()

	if *socket == "" {
		fail("-socket is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	var err error
	switch *role {
	case "plugin":
		err = runPluginRole(ctx, *socket)
	case "core":
		err = runCoreRole(ctx, *socket)
	default:
		fail("unknown role %q", *role)
	}

	if err != nil {
		fail("%v", err)
	}
	fmt.Printf("conformance: go %s role ok\n", *role)
}

// runPluginRole serves the transcript's plugin half.
func runPluginRole(ctx context.Context, socket string) error {
	handler := sidecar.NewMux().
		HandleFunc(sidecar.MethodDiscover, func(context.Context, *sidecar.Request) (any, error) {
			return discovered, nil
		}).
		HandleFunc(sidecar.MethodReadState, func(_ context.Context, r *sidecar.Request) (any, error) {
			var params struct {
				DeviceRef string `json:"deviceRef"`
				Feature   string `json:"feature"`
				Key       string `json:"key"`
			}
			if err := r.Bind(&params); err != nil {
				return nil, err
			}
			if params.DeviceRef != "matter:2" {
				return nil, sidecar.ErrDeviceNotFound.WithDetails(map[string]any{
					"deviceRef": params.DeviceRef,
				})
			}
			return map[string]any{"value": true, "origin": "device_report"}, nil
		}).
		HandleFunc(sidecar.MethodInvokeAction, func(_ context.Context, r *sidecar.Request) (any, error) {
			// Answering, then pushing the resulting state change, is the shape
			// every real device operation takes.
			go func() {
				time.Sleep(10 * time.Millisecond)
				_ = r.Peer().Publish("state.matter:2.onoff.value", map[string]any{
					"value": false, "origin": "device_report",
				})
			}()
			return map[string]any{}, nil
		}).
		HandleFunc(sidecar.MethodGetHealth, func(context.Context, *sidecar.Request) (any, error) {
			return map[string]any{
				"status":   "ok",
				"message":  "conformance harness",
				"counters": map[string]float64{"peers": 2},
			}, nil
		}).
		HandleFunc("errorOnPurpose", func(context.Context, *sidecar.Request) (any, error) {
			return nil, sidecar.ErrDeviceOffline.WithDetails(map[string]any{"deviceRef": "matter:5"})
		}).
		// emitBurst exists so the transcript is deterministic.
		//
		// Publishing from OnConnect would race the core's subscribe, and
		// deny-by-default would correctly drop the pushes — the protocol
		// working as designed, but a test that fails intermittently for a
		// reason that has nothing to do with what it is checking.
		HandleFunc("emitBurst", func(_ context.Context, r *sidecar.Request) (any, error) {
			if err := r.Peer().Publish("lifecycle.matter:2.online", map[string]any{
				"deviceRef": "matter:2",
			}); err != nil {
				return nil, err
			}
			// Deliberately unsubscribed. The core asked for state, lifecycle
			// and event, not plugin.log — so this must be dropped AND must not
			// consume a sequence number. Numbering it would leave the core
			// with a permanent gap for a topic it never wanted, and gap
			// detection could no longer tell "you missed something" from "you
			// did not want that".
			if err := r.Peer().Publish("plugin.log.debug", map[string]any{
				"message": "this topic was not subscribed to",
			}); err != nil {
				return nil, err
			}
			if err := r.Peer().Publish("state.matter:2.onoff.value", map[string]any{
				"value": true, "origin": "device_report",
			}); err != nil {
				return nil, err
			}
			return map[string]any{"emitted": 2}, nil
		})

	return sidecar.RunPlugin(ctx, sidecar.PluginOptions{
		Name:         "conformance",
		Version:      "1.0.0",
		Capabilities: []string{"realtime.state-stream", "commission.setup-code"},
		SocketPath:   socket,
		Handler:      handler,
	})
}

// runCoreRole drives the transcript against whatever plugin is on the socket.
func runCoreRole(ctx context.Context, socket string) error {
	pushes := make(chan sidecar.Push, 64)

	client, err := sidecar.Connect(ctx, socket, sidecar.CoreOptions{
		Version: "0.5.2",
		Backoff: &sidecar.Backoff{Min: 20 * time.Millisecond, Max: 200 * time.Millisecond, Factor: 2},
	})
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer client.Close()

	go func() {
		for p := range client.Pushes() {
			select {
			case pushes <- p:
			default:
			}
		}
	}()

	// --- handshake ---------------------------------------------------------

	session := client.Session()
	if session.PeerName != "conformance" {
		return fmt.Errorf("handshake: peer name is %q, want conformance", session.PeerName)
	}
	if !session.HasCapability("realtime.state-stream") {
		return fmt.Errorf("handshake: capabilities are %v, want realtime.state-stream", session.Capabilities)
	}

	// --- subscribe ---------------------------------------------------------

	if err := client.Subscribe(ctx, "state.**", "lifecycle.**", "event.**"); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	// --- discover ----------------------------------------------------------

	var devices []map[string]any
	if err := client.Call(ctx, sidecar.MethodDiscover, nil, &devices); err != nil {
		return fmt.Errorf("discover: %w", err)
	}
	if len(devices) != len(discovered) {
		return fmt.Errorf("discover returned %d devices, want %d", len(devices), len(discovered))
	}
	if devices[0]["deviceRef"] != "matter:2" {
		return fmt.Errorf("discover[0].deviceRef = %v, want matter:2", devices[0]["deviceRef"])
	}

	// --- readState ---------------------------------------------------------

	var state struct {
		Value  bool   `json:"value"`
		Origin string `json:"origin"`
	}
	if err := client.Call(ctx, sidecar.MethodReadState, map[string]string{
		"deviceRef": "matter:2", "feature": "onoff", "key": "value",
	}, &state); err != nil {
		return fmt.Errorf("readState: %w", err)
	}
	if !state.Value || state.Origin != "device_report" {
		return fmt.Errorf("readState returned %+v", state)
	}

	// --- typed errors ------------------------------------------------------

	err = client.Call(ctx, sidecar.MethodReadState, map[string]string{
		"deviceRef": "matter:999", "feature": "onoff", "key": "value",
	}, nil)
	if code := sidecar.CodeOf(err); code != sidecar.CodeDeviceNotFound {
		return fmt.Errorf("readState of a missing device: code %q, want %q", code, sidecar.CodeDeviceNotFound)
	}
	if sidecar.IsRetryable(err) {
		return fmt.Errorf("device.not_found reported as retryable across the wire")
	}

	err = client.Call(ctx, "errorOnPurpose", nil, nil)
	if code := sidecar.CodeOf(err); code != sidecar.CodeDeviceOffline {
		return fmt.Errorf("errorOnPurpose: code %q, want %q", code, sidecar.CodeDeviceOffline)
	}
	// The retryable flag has to survive the crossing, or the core stops
	// retrying sleeping devices.
	if !sidecar.IsRetryable(err) {
		return fmt.Errorf("device.offline did not come back retryable")
	}
	var perr *sidecar.Error
	if ok := asError(err, &perr); !ok || perr.Details["deviceRef"] != "matter:5" {
		return fmt.Errorf("error details lost across the wire: %v", err)
	}

	// --- unknown method ----------------------------------------------------

	// It must be answered, not ignored: a blocked caller cannot tell silence
	// from a dead plugin.
	unknownCtx, cancelUnknown := context.WithTimeout(ctx, 3*time.Second)
	err = client.Call(unknownCtx, "noSuchMethodExists", nil, nil)
	cancelUnknown()
	if code := sidecar.CodeOf(err); code != sidecar.CodeProtocolUnsupported {
		return fmt.Errorf("unknown method: code %q, want %q", code, sidecar.CodeProtocolUnsupported)
	}

	// --- pushes ------------------------------------------------------------

	// Two from the burst, plus the one invokeAction causes.
	if err := client.Call(ctx, "emitBurst", nil, nil); err != nil {
		return fmt.Errorf("emitBurst: %w", err)
	}
	if err := client.Call(ctx, sidecar.MethodInvokeAction, map[string]any{
		"deviceRef": "matter:2", "feature": "onoff", "action": "turn_off",
	}, nil); err != nil {
		return fmt.Errorf("invokeAction: %w", err)
	}

	seen, err := collectPushes(pushes, 3, 5*time.Second)
	if err != nil {
		return err
	}

	// Sequence numbers start at 1 and increase by exactly 1 — with no hole
	// where the unsubscribed plugin.log.debug push was dropped.
	for i, p := range seen {
		if p.Topic == "plugin.log.debug" {
			return fmt.Errorf("received a push on a topic we never subscribed to")
		}
		if want := uint64(i + 1); p.Seq != want {
			return fmt.Errorf("push %d has seq %d, want %d", i, p.Seq, want)
		}
		if p.TS.IsZero() {
			return fmt.Errorf("push %d on %s has no timestamp", i, p.Topic)
		}
		// The timestamp format has to match byte for byte across languages, or
		// golden fixtures diverge for no semantic reason.
		if got := p.TS.String(); len(got) != len("2026-08-15T21:04:11.123Z") {
			return fmt.Errorf("push %d timestamp %q is not the wire format", i, got)
		}
	}
	if seen[0].Topic != "lifecycle.matter:2.online" {
		return fmt.Errorf("push 0 topic is %q, want lifecycle.matter:2.online", seen[0].Topic)
	}

	// --- heartbeat ---------------------------------------------------------

	var pong struct {
		Pong bool `json:"pong"`
	}
	if err := client.Call(ctx, sidecar.MethodPing, struct{}{}, &pong); err != nil {
		return fmt.Errorf("ping: %w", err)
	}
	if !pong.Pong {
		return fmt.Errorf("ping returned %+v", pong)
	}

	// --- getHealth ---------------------------------------------------------

	var health struct {
		Status string `json:"status"`
	}
	if err := client.Call(ctx, sidecar.MethodGetHealth, nil, &health); err != nil {
		return fmt.Errorf("getHealth: %w", err)
	}
	if health.Status != "ok" {
		return fmt.Errorf("getHealth status is %q, want ok", health.Status)
	}

	// --- resume ------------------------------------------------------------

	before := client.LastSeq()
	res, err := client.Resume(ctx)
	if err != nil {
		return fmt.Errorf("resume: %w", err)
	}
	if !res.Resumed {
		return fmt.Errorf("resume returned resumed=false")
	}
	// We are current, so there is nothing to replay and no gap.
	if res.Sending != 0 {
		return fmt.Errorf("resume wants to send %d frames, want 0 — we were current", res.Sending)
	}
	if res.Gap {
		return fmt.Errorf("resume reported a gap over history we have in full")
	}
	if res.Seq != before {
		return fmt.Errorf("resume reports seq %d, want %d", res.Seq, before)
	}

	// A replay of ground already covered must be discarded rather than
	// re-delivered — otherwise a reconnect re-fires automations for events
	// from before the drop.
	if extra, _ := collectPushes(pushes, 1, 300*time.Millisecond); len(extra) != 0 {
		return fmt.Errorf("resume re-delivered %d pushes we had already accepted", len(extra))
	}

	return nil
}

func collectPushes(ch <-chan sidecar.Push, n int, within time.Duration) ([]sidecar.Push, error) {
	var out []sidecar.Push
	deadline := time.After(within)
	for len(out) < n {
		select {
		case p := <-ch:
			out = append(out, p)
		case <-deadline:
			if n == 1 {
				// Used as a "nothing more should arrive" probe.
				return out, nil
			}
			return out, fmt.Errorf("only %d of %d pushes arrived within %s", len(out), n, within)
		}
	}
	return out, nil
}

func asError(err error, out **sidecar.Error) bool {
	return errors.As(err, out)
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "conformance: "+format+"\n", a...)
	os.Exit(1)
}
