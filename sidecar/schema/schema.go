// Package schema validates sidecar protocol frames against the canonical JSON
// Schemas.
//
// It is a separate package so the root sidecar package stays stdlib-only: a
// plugin that only speaks the protocol pulls in no dependencies, and only one
// that wants validation links the validator.
//
// Validation is off by default in production and on in tests, which is what
// spec §13 asks for. The cost is a schema walk per frame, and on the push path
// that is real; the value is catching a contract violation at the boundary
// where it is still attributable, rather than three layers deeper as a
// confusing nil.
//
//	v, err := schema.New()
//	if err != nil {
//		return err
//	}
//	if err := v.ValidateFrame(raw); err != nil {
//		// the far side sent something the contract does not allow
//	}
//
// The compile-once pattern and the error rendering follow keystone's
// internal/plugin/validator.go, which does the same job for plugin manifests.
package schema

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// enPrinter renders jsonschema's error kinds in English. The library
// localises its messages and needs to be told which locale to use.
var enPrinter = message.NewPrinter(language.English)

// v1 holds the canonical schemas.
//
// This directory is a generated mirror of schemas/sidecar/v1 at the repository
// root — go:embed cannot reference a path containing "..", so the canonical
// copy has to be duplicated here. `make verify-gen` fails if the two drift.
//
//go:embed v1
var v1 embed.FS

// BaseURI is the $id prefix the schemas use. It matches the convention
// already established by keystone's plugin manifest schema.
const BaseURI = "https://keystone.io/schemas/sidecar/v1/"

// FS returns the embedded schemas, for tooling that wants to publish them —
// a registry check, an editor integration, a validator in another language.
func FS() fs.FS {
	sub, err := fs.Sub(v1, "v1")
	if err != nil {
		panic("sidecar/schema: embedded schemas are unreadable: " + err.Error())
	}
	return sub
}

// Validator checks frames and payloads against the schemas.
//
// Safe for concurrent use. Compilation happens once, lazily, on first use.
type Validator struct {
	compileOnce sync.Once
	compileErr  error

	compiler *jsonschema.Compiler
	frame    *jsonschema.Schema

	mu      sync.RWMutex
	byRef   map[string]*jsonschema.Schema
	methods map[string]methodEntry
	topics  []topicEntry
}

type methodEntry struct {
	paramsRef string
	resultRef string
	Direction string
}

type topicEntry struct {
	pattern    []string
	payloadRef string
}

// New returns a Validator with the schemas compiled.
func New() (*Validator, error) {
	v := &Validator{}
	if err := v.compile(); err != nil {
		return nil, err
	}
	return v, nil
}

func (v *Validator) compile() error {
	v.compileOnce.Do(func() { v.compileErr = v.doCompile() })
	return v.compileErr
}

func (v *Validator) doCompile() error {
	c := jsonschema.NewCompiler()
	// AssertFormat makes "format": "date-time" a real check rather than an
	// annotation. Timestamps are one of the few places a peer can be subtly
	// wrong in a way that only shows up later.
	c.AssertFormat()

	root := FS()
	err := fs.WalkDir(root, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}
		raw, err := fs.ReadFile(root, path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			return fmt.Errorf("%s is not valid JSON: %w", path, err)
		}
		return c.AddResource(BaseURI+path, doc)
	})
	if err != nil {
		return fmt.Errorf("sidecar/schema: %w", err)
	}

	v.compiler = c
	v.byRef = make(map[string]*jsonschema.Schema)

	v.frame, err = c.Compile(BaseURI + "envelope.json")
	if err != nil {
		return fmt.Errorf("sidecar/schema: envelope.json does not compile: %w", err)
	}

	if err := v.loadIndexes(root); err != nil {
		return err
	}
	return nil
}

// loadIndexes reads the method and topic inventories.
//
// These are data rather than schemas: they are what lets ValidateParams and
// ValidatePush dispatch without a hand-maintained switch statement that would
// inevitably drift from the schemas beside it.
func (v *Validator) loadIndexes(root fs.FS) error {
	var methodIndex struct {
		Methods map[string]struct {
			Params    map[string]string `json:"params"`
			Result    map[string]string `json:"result"`
			Direction string            `json:"direction"`
		} `json:"methods"`
	}
	raw, err := fs.ReadFile(root, "methods/index.json")
	if err != nil {
		return fmt.Errorf("sidecar/schema: reading the method index: %w", err)
	}
	if err := json.Unmarshal(raw, &methodIndex); err != nil {
		return fmt.Errorf("sidecar/schema: the method index is not valid JSON: %w", err)
	}

	v.methods = make(map[string]methodEntry, len(methodIndex.Methods))
	for name, m := range methodIndex.Methods {
		v.methods[name] = methodEntry{
			paramsRef: resolveRef("methods/", m.Params["$ref"]),
			resultRef: resolveRef("methods/", m.Result["$ref"]),
			Direction: m.Direction,
		}
	}

	var topicIndex struct {
		Topics []struct {
			Pattern string            `json:"pattern"`
			Payload map[string]string `json:"payload"`
		} `json:"topics"`
	}
	raw, err = fs.ReadFile(root, "topics/index.json")
	if err != nil {
		return fmt.Errorf("sidecar/schema: reading the topic index: %w", err)
	}
	if err := json.Unmarshal(raw, &topicIndex); err != nil {
		return fmt.Errorf("sidecar/schema: the topic index is not valid JSON: %w", err)
	}

	for _, t := range topicIndex.Topics {
		v.topics = append(v.topics, topicEntry{
			pattern:    strings.Split(t.Pattern, "."),
			payloadRef: resolveRef("topics/", t.Payload["$ref"]),
		})
	}

	// Most specific first, so a literal pattern wins over a wildcarded one
	// that would also match.
	sort.SliceStable(v.topics, func(i, j int) bool {
		return wildcards(v.topics[i].pattern) < wildcards(v.topics[j].pattern)
	})
	return nil
}

func wildcards(segs []string) int {
	n := 0
	for _, s := range segs {
		if s == "*" || s == "**" {
			n++
		}
	}
	return n
}

// resolveRef turns a $ref relative to an index file into an absolute URI.
func resolveRef(dir, ref string) string {
	if ref == "" {
		return ""
	}
	if strings.HasPrefix(ref, "http") {
		return ref
	}
	// "../objects/device.json#/$defs/x" -> "objects/device.json#/$defs/x"
	for strings.HasPrefix(ref, "../") {
		ref = strings.TrimPrefix(ref, "../")
		dir = ""
	}
	return BaseURI + dir + ref
}

// schemaFor compiles and caches a schema by absolute URI.
func (v *Validator) schemaFor(uri string) (*jsonschema.Schema, error) {
	v.mu.RLock()
	s, ok := v.byRef[uri]
	v.mu.RUnlock()
	if ok {
		return s, nil
	}

	s, err := v.compiler.Compile(uri)
	if err != nil {
		return nil, fmt.Errorf("sidecar/schema: %s does not compile: %w", uri, err)
	}

	v.mu.Lock()
	v.byRef[uri] = s
	v.mu.Unlock()
	return s, nil
}

// ValidateFrame checks raw bytes against the envelope schema.
func (v *Validator) ValidateFrame(raw []byte) error {
	if err := v.compile(); err != nil {
		return err
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("sidecar/schema: not valid JSON: %w", err)
	}
	return render("frame", v.frame.Validate(doc))
}

// ValidateParams checks a method's parameters.
//
// An unknown method is not an error here: a peer may implement methods this
// build has never heard of, and rejecting them would make the validator a
// compatibility barrier rather than a check.
func (v *Validator) ValidateParams(method string, raw []byte) error {
	return v.validateAgainst(method+" params", v.methodRef(method, true), raw)
}

// ValidateResult checks a method's result.
func (v *Validator) ValidateResult(method string, raw []byte) error {
	return v.validateAgainst(method+" result", v.methodRef(method, false), raw)
}

func (v *Validator) methodRef(method string, params bool) string {
	if err := v.compile(); err != nil {
		return ""
	}
	m, ok := v.methods[method]
	if !ok {
		return ""
	}
	if params {
		return m.paramsRef
	}
	return m.resultRef
}

// ValidatePush checks a push payload against the schema for its topic.
//
// A topic matching no known pattern is not an error, for the same reason as an
// unknown method.
func (v *Validator) ValidatePush(topic string, raw []byte) error {
	if err := v.compile(); err != nil {
		return err
	}
	segs := strings.Split(topic, ".")
	for _, t := range v.topics {
		if matchPattern(t.pattern, segs) {
			return v.validateAgainst(topic+" payload", t.payloadRef, raw)
		}
	}
	return nil
}

func (v *Validator) validateAgainst(what, uri string, raw []byte) error {
	if uri == "" {
		return nil
	}
	if err := v.compile(); err != nil {
		return err
	}
	s, err := v.schemaFor(uri)
	if err != nil {
		return err
	}

	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("sidecar/schema: %s is not valid JSON: %w", what, err)
	}
	return render(what, s.Validate(doc))
}

// Methods returns the method inventory, for tooling.
func (v *Validator) Methods() map[string]string {
	if err := v.compile(); err != nil {
		return nil
	}
	out := make(map[string]string, len(v.methods))
	for name, m := range v.methods {
		out[name] = m.Direction
	}
	return out
}

// matchPattern applies the same wildcard grammar as the runtime's topic
// filters (spec §8.2).
func matchPattern(pattern, topic []string) bool {
	for i, p := range pattern {
		if p == "**" {
			return len(topic) > i
		}
		if i >= len(topic) {
			return false
		}
		if p != "*" && p != topic[i] {
			return false
		}
	}
	return len(pattern) == len(topic)
}

// render turns a validation failure into something readable.
//
// jsonschema's default output is a nested tree that is thorough and unusable
// in a log line. What a reader needs is which field was wrong and why, so the
// leaf causes are flattened into one message — the same choice keystone's
// manifest validator makes.
func render(what string, err error) error {
	if err == nil {
		return nil
	}
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return fmt.Errorf("sidecar/schema: %s: %w", what, err)
	}

	causes := leafCauses(ve, nil)
	if len(causes) == 0 {
		return fmt.Errorf("sidecar/schema: %s is invalid", what)
	}
	sort.Strings(causes)
	return fmt.Errorf("sidecar/schema: %s is invalid: %s", what, strings.Join(causes, "; "))
}

func leafCauses(ve *jsonschema.ValidationError, acc []string) []string {
	if len(ve.Causes) == 0 {
		loc := strings.Join(ve.InstanceLocation, "/")
		if loc == "" {
			loc = "(root)"
		}
		return append(acc, fmt.Sprintf("%s: %s", loc, ve.ErrorKind.LocalizedString(enPrinter)))
	}
	for _, c := range ve.Causes {
		acc = leafCauses(c, acc)
	}
	return acc
}
