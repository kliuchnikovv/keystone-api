package schema_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/kliuchnikovv/keystone-api/sidecar"
	"github.com/kliuchnikovv/keystone-api/sidecar/schema"
)

// corpusDir is the canonical fixture corpus at the repository root. Both
// language test suites read the same files, so a schema change that is not
// reflected in both type sets fails here rather than in production.
const corpusDir = "../../schemas/sidecar/v1/testdata/frames"

func TestValidFixturesPassAndRoundTripThroughGo(t *testing.T) {
	v, err := schema.New()
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	files, err := filepath.Glob(filepath.Join(corpusDir, "valid", "*.json"))
	if err != nil {
		t.Fatalf("Glob() = %v", err)
	}
	if len(files) == 0 {
		t.Fatal("the corpus is empty — the fixtures did not resolve")
	}

	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile() = %v", err)
			}

			if err := v.ValidateFrame(raw); err != nil {
				t.Fatalf("the schema rejects a fixture that should be valid: %v", err)
			}

			// The hand-written Go types must accept everything the schema
			// does. This half is what a code generator would have given us for
			// free; here it is a test instead.
			var frame sidecar.Frame
			if err := json.Unmarshal(raw, &frame); err != nil {
				t.Fatalf("the Go types cannot decode a valid frame: %v", err)
			}
			if err := frame.Validate(); err != nil {
				t.Fatalf("Frame.Validate rejects a schema-valid frame: %v", err)
			}

			// And re-encoding must produce something the schema still accepts.
			// Without this a field the Go types silently drop would go
			// unnoticed until a plugin in another language stopped seeing it.
			out, err := json.Marshal(&frame)
			if err != nil {
				t.Fatalf("Marshal() = %v", err)
			}
			if err := v.ValidateFrame(out); err != nil {
				t.Fatalf("a frame is invalid after a Go round trip: %v\n  in:  %s\n  out: %s",
					err, strings.TrimSpace(string(raw)), out)
			}

			assertNoFieldsDropped(t, raw, out)
		})
	}
}

func TestInvalidFixturesAreRejected(t *testing.T) {
	v, err := schema.New()
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	files, err := filepath.Glob(filepath.Join(corpusDir, "invalid", "*.json"))
	if err != nil {
		t.Fatalf("Glob() = %v", err)
	}
	if len(files) == 0 {
		t.Fatal("there are no negative fixtures")
	}

	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile() = %v", err)
			}
			if err := v.ValidateFrame(raw); err == nil {
				t.Errorf("the schema accepts a fixture that should be rejected:\n  %s",
					strings.TrimSpace(string(raw)))
			}
		})
	}
}

// assertNoFieldsDropped compares the top-level keys before and after a Go
// round trip.
//
// A field present in the schema but missing from the Go struct decodes to
// nothing and disappears on re-encode. The frame still validates — the field
// was probably optional — so nothing else would catch it, and the loss would
// only surface as a plugin in another language quietly not receiving data it
// was sent.
func assertNoFieldsDropped(t *testing.T, before, after []byte) {
	t.Helper()

	var b, a map[string]json.RawMessage
	if err := json.Unmarshal(before, &b); err != nil {
		t.Fatalf("Unmarshal(before) = %v", err)
	}
	if err := json.Unmarshal(after, &a); err != nil {
		t.Fatalf("Unmarshal(after) = %v", err)
	}

	var missing []string
	for k := range b {
		if _, ok := a[k]; !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("the Go types dropped these fields on a round trip: %v", missing)
	}
}

// TestFrameStructMatchesTheEnvelopeSchema is the drift gate that replaces code
// generation for Go.
//
// It walks the three frame schemas and asserts that every property they define
// has a corresponding json tag on sidecar.Frame, and that every tag on Frame
// corresponds to a property. That is the guarantee a generator would have
// provided; enforcing it here costs a hundred lines and buys hand-written
// types with json.RawMessage where it matters and errors.Is support — see
// CONTRIBUTING.md.
func TestFrameStructMatchesTheEnvelopeSchema(t *testing.T) {
	root := schema.FS()

	schemaFields := map[string]bool{}
	for _, name := range []string{"request.json", "response.json", "push.json"} {
		raw, err := fs.ReadFile(root, name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		var doc struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s is not valid JSON: %v", name, err)
		}
		for prop := range doc.Properties {
			schemaFields[prop] = true
		}
	}

	goFields := map[string]bool{}
	rt := reflect.TypeOf(sidecar.Frame{})
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		goFields[strings.Split(tag, ",")[0]] = true
	}

	for prop := range schemaFields {
		if !goFields[prop] {
			t.Errorf("the schemas define %q but sidecar.Frame has no field for it", prop)
		}
	}
	for field := range goFields {
		if !schemaFields[field] {
			t.Errorf("sidecar.Frame has a field %q that no frame schema defines", field)
		}
	}
}

func TestValidateParamsAndResult(t *testing.T) {
	v, err := schema.New()
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	tests := []struct {
		name   string
		check  func() error
		wantOK bool
	}{
		{
			"valid hello params",
			func() error {
				return v.ValidateParams("hello", []byte(
					`{"protocol":"v1","plugin":"matter","version":"1.2.0"}`))
			},
			true,
		},
		{
			"hello params missing the plugin name",
			func() error {
				return v.ValidateParams("hello", []byte(`{"protocol":"v1","version":"1.2.0"}`))
			},
			false,
		},
		{
			"hello params with a bad protocol form",
			func() error {
				return v.ValidateParams("hello", []byte(
					`{"protocol":"1","plugin":"matter","version":"1.2.0"}`))
			},
			false,
		},
		{
			"valid subscribe params",
			func() error {
				return v.ValidateParams("subscribe", []byte(`{"topics":["state.**","plugin.log.error"]}`))
			},
			true,
		},
		{
			"valid ping result",
			func() error { return v.ValidateResult("ping", []byte(`{"pong":true}`)) },
			true,
		},
		{
			"ping result with pong false",
			func() error { return v.ValidateResult("ping", []byte(`{"pong":false}`)) },
			false,
		},
		{
			// An unknown method is not a validation failure. A peer may
			// implement methods this build has never heard of, and rejecting
			// them would turn the validator into a compatibility barrier.
			"unknown method is not rejected",
			func() error { return v.ValidateParams("someFutureMethod", []byte(`{"x":1}`)) },
			true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.check()
			if tc.wantOK && err != nil {
				t.Errorf("got %v, want nil", err)
			}
			if !tc.wantOK && err == nil {
				t.Error("got nil, want an error")
			}
		})
	}
}

func TestValidatePush(t *testing.T) {
	v, err := schema.New()
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	tests := []struct {
		topic   string
		payload string
		wantOK  bool
	}{
		{"state.matter:3.onoff.value", `{"value":true,"origin":"device_report"}`, true},
		{"state.matter:3.onoff.value", `{"origin":"device_report"}`, false},        // value is required
		{"state.matter:3.onoff.value", `{"value":true,"origin":"guessed"}`, false}, // not in the enum
		{"lifecycle.matter:3.offline", `{"deviceRef":"matter:3","reason":"session_timeout"}`, true},
		{"lifecycle.matter:3.offline", `{}`, false}, // deviceRef is required
		{"plugin.health.controller", `{"status":"degraded","counters":{"peers":3}}`, true},
		{"plugin.health.controller", `{"status":"on fire"}`, false},
		{"plugin.log.error", `{"message":"attribute decode failed"}`, true},
		{"plugin.metrics", `{"metrics":{"queue_depth":2}}`, true},
		// A topic outside the known set is not an error, for the same reason
		// an unknown method is not.
		{"vendor.aqara.something", `{"anything":true}`, true},
	}

	for _, tc := range tests {
		t.Run(tc.topic+" "+tc.payload, func(t *testing.T) {
			err := v.ValidatePush(tc.topic, []byte(tc.payload))
			if tc.wantOK && err != nil {
				t.Errorf("got %v, want nil", err)
			}
			if !tc.wantOK && err == nil {
				t.Error("got nil, want an error")
			}
		})
	}
}

func TestMethodInventoryMatchesTheGoConstants(t *testing.T) {
	// The schemas are the source of truth for the method surface. If someone
	// adds a method to the index without a constant — or the reverse — a
	// caller ends up passing a string literal that the other side answers with
	// protocol.unsupported, and the mismatch is only found at runtime.
	v, err := schema.New()
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	constants := map[string]bool{
		sidecar.MethodHello:        true,
		sidecar.MethodPing:         true,
		sidecar.MethodShutdown:     true,
		sidecar.MethodGetHealth:    true,
		sidecar.MethodDiscover:     true,
		sidecar.MethodCommission:   true,
		sidecar.MethodDecommission: true,
		sidecar.MethodReadState:    true,
		sidecar.MethodWriteState:   true,
		sidecar.MethodInvokeAction: true,
		sidecar.MethodSubscribe:    true,
		sidecar.MethodUnsubscribe:  true,
		sidecar.MethodResume:       true,
		sidecar.MethodConfigFlow:   true,
		sidecar.MethodGetConfig:    true,
		sidecar.MethodSetConfig:    true,
	}

	inventory := v.Methods()
	for name := range inventory {
		if !constants[name] {
			t.Errorf("methods/index.json lists %q but the package has no Method constant for it", name)
		}
	}
	for name := range constants {
		if _, ok := inventory[name]; !ok {
			t.Errorf("the package has a constant for %q but methods/index.json does not list it", name)
		}
	}

	// Every v1 method is core-to-plugin except hello.
	if got := inventory[sidecar.MethodHello]; got != "plugin->core" {
		t.Errorf("hello direction = %q, want plugin->core", got)
	}
}
