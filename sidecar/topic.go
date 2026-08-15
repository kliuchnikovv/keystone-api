package sidecar

import (
	"strings"
)

// Topic is a concrete push subject: dot-separated segments, no wildcards
// (spec §8.1).
//
// The well-known shapes are:
//
//	state.<deviceRef>.<feature>.<stateKey>
//	event.<deviceRef>.<eventName>
//	lifecycle.<deviceRef>.online | offline | removed
//	commissioning.<sessionId>.progress
//	plugin.health.<component>
//	plugin.log.<level>
//	plugin.metrics
//
// A plugin may publish topics outside this set; the core simply will not
// understand their payloads.
type Topic string

// Segments splits the topic on dots.
func (t Topic) Segments() []string {
	if t == "" {
		return nil
	}
	return strings.Split(string(t), ".")
}

// Valid reports whether the topic is well-formed: at least one segment, no
// empty segments, no wildcard characters, no whitespace.
//
// The prohibition on '.' inside a value is why this check exists. Every
// well-known topic shape interpolates identifiers — a device ref, a feature
// key, a session id — and assumes each occupies exactly one segment. A value
// containing a dot shifts every segment after it, and pattern matching then
// fails silently: state.*.*.* simply stops matching that device, with nothing
// logged anywhere. Catching it at publish time turns a mystery into an error.
func (t Topic) Valid() bool {
	if t == "" {
		return false
	}
	for _, seg := range t.Segments() {
		if seg == "" || strings.ContainsAny(seg, " \t\r\n*") {
			return false
		}
	}
	return true
}

// Category is the first segment — state, event, lifecycle, commissioning,
// plugin. Empty for a malformed topic.
func (t Topic) Category() string {
	s, _, _ := strings.Cut(string(t), ".")
	return s
}

// Filter is a subscription pattern over topics (spec §8.2).
//
// The grammar has exactly two wildcards:
//
//	"*"   matches exactly one segment
//	"**"  matches one or more trailing segments, and may only appear last
//
// Any other segment is a case-sensitive literal. Note that "**" matches one or
// more, not zero or more: "state.**" selects state.matter:3.onoff.value but
// not the bare topic "state".
type Filter string

// Validate reports whether the filter is well-formed.
func (f Filter) Validate() error {
	if f == "" {
		return Errorf(CodeValidationBadParams, "topic filter is empty")
	}
	segs := strings.Split(string(f), ".")
	for i, seg := range segs {
		switch {
		case seg == "":
			return Errorf(CodeValidationBadParams, "topic filter %q has an empty segment", f)
		case seg == "**" && i != len(segs)-1:
			// A non-final ** would make matching ambiguous — "a.**.b" could
			// split anywhere — and no well-known topic shape needs it.
			return Errorf(CodeValidationBadParams, "topic filter %q: ** may only appear as the last segment", f)
		case seg != "*" && seg != "**" && strings.Contains(seg, "*"):
			// Partial wildcards ("stat*") are not part of the grammar.
			// Accepting them here and ignoring them at match time would be a
			// filter that silently matches nothing.
			return Errorf(CodeValidationBadParams, "topic filter %q: %q mixes a wildcard with literal text", f, seg)
		case strings.ContainsAny(seg, " \t\r\n"):
			return Errorf(CodeValidationBadParams, "topic filter %q contains whitespace", f)
		}
	}
	return nil
}

// Match reports whether the filter selects the topic.
func (f Filter) Match(t Topic) bool {
	return matchSegments(strings.Split(string(f), "."), t.Segments())
}

func matchSegments(pattern, topic []string) bool {
	for i, p := range pattern {
		if p == "**" {
			// ** matches one or more trailing segments, not zero or more.
			// So "state.**" covers state.matter:3.onoff.value but not the
			// bare topic "state" — subscribing to a category should not
			// deliver the category itself as if it were an event.
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

// Matcher is a compiled set of filters.
//
// The zero Matcher matches nothing, and that is the intended default: a peer
// that has not subscribed receives silence rather than every push in the
// house. Deny-by-default also means a core that forgets to subscribe sees an
// obvious symptom instead of a subtle load problem.
type Matcher struct {
	filters []Filter
	// Pre-split so matching a push does not re-split the pattern each time.
	// Pushes are the highest-volume frame by a wide margin.
	patterns [][]string
}

// NewMatcher compiles a filter set, rejecting malformed filters.
func NewMatcher(filters ...Filter) (*Matcher, error) {
	m := &Matcher{}
	if err := m.Set(filters...); err != nil {
		return nil, err
	}
	return m, nil
}

// Set replaces the filter set.
//
// Replace, not merge: `subscribe` is defined as setting the filter set (§8.2),
// which makes re-sending the full desired set after a reconnect the correct
// and idempotent thing to do.
func (m *Matcher) Set(filters ...Filter) error {
	patterns := make([][]string, 0, len(filters))
	for _, f := range filters {
		if err := f.Validate(); err != nil {
			return err
		}
		patterns = append(patterns, strings.Split(string(f), "."))
	}
	m.filters = append(m.filters[:0], filters...)
	m.patterns = patterns
	return nil
}

// Match reports whether any filter selects the topic.
func (m *Matcher) Match(t Topic) bool {
	if m == nil {
		return false
	}
	segs := t.Segments()
	for _, p := range m.patterns {
		if matchSegments(p, segs) {
			return true
		}
	}
	return false
}

// Filters returns the current filter set.
func (m *Matcher) Filters() []Filter {
	if m == nil {
		return nil
	}
	out := make([]Filter, len(m.filters))
	copy(out, m.filters)
	return out
}

// Empty reports whether the matcher would reject everything.
func (m *Matcher) Empty() bool { return m == nil || len(m.patterns) == 0 }
