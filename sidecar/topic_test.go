package sidecar

import "testing"

func TestTopicValid(t *testing.T) {
	valid := []Topic{
		"plugin.metrics",
		"state.matter:3.onoff.value",
		"state.17.brightness.level",
		"event.matter:5.motion_detected",
		"lifecycle.zigbee:0x00158d0001a2b3c4.offline",
		"commissioning.sess_abc.progress",
		"plugin.log.error",
		"plugin.health.controller",
		// A single segment is a legal topic.
		"heartbeat",
		// Colons are the recommended separator precisely because dots are not.
		"state.aqara:hub:1.aqara:vibration.value",
	}
	for _, tp := range valid {
		if !tp.Valid() {
			t.Errorf("Topic(%q).Valid() = false, want true", tp)
		}
	}

	invalid := []Topic{
		"",
		".",
		".leading",
		"trailing.",
		"double..dot",
		// Wildcards belong in filters, never in a published topic. A plugin
		// that publishes "state.**" has a formatting bug, and delivering it
		// would make it look like a subscription instead.
		"state.**",
		"state.*.onoff.value",
		"state.matter:3.on off.value",
		"state.matter:3.onoff.value\n",
	}
	for _, tp := range invalid {
		if tp.Valid() {
			t.Errorf("Topic(%q).Valid() = true, want false", tp)
		}
	}
}

func TestFilterValidate(t *testing.T) {
	valid := []Filter{
		"state.**",
		"**",
		"event.matter:3.*",
		"plugin.log.error",
		"state.*.*.*",
		"*.*.*",
	}
	for _, f := range valid {
		if err := f.Validate(); err != nil {
			t.Errorf("Filter(%q).Validate() = %v, want nil", f, err)
		}
	}

	invalid := []Filter{
		"",
		"a..b",
		// ** only at the end: anywhere else the split point is ambiguous.
		"state.**.value",
		"**.value",
		// Partial wildcards are not in the grammar. Silently accepting one
		// would produce a filter that matches nothing, with no error to
		// explain why the subscription looks dead.
		"stat*.matter:3",
		"state.matt*:3.onoff.value",
		"state. .value",
	}
	for _, f := range invalid {
		if err := f.Validate(); err == nil {
			t.Errorf("Filter(%q).Validate() = nil, want an error", f)
		} else if CodeOf(err) != CodeValidationBadParams {
			t.Errorf("Filter(%q).Validate() code = %q, want %q", f, CodeOf(err), CodeValidationBadParams)
		}
	}
}

func TestFilterMatch(t *testing.T) {
	tests := []struct {
		filter Filter
		topic  Topic
		want   bool
	}{
		// Literals.
		{"plugin.metrics", "plugin.metrics", true},
		{"plugin.metrics", "plugin.metric", false},
		{"plugin.metrics", "plugin.metrics.extra", false},
		{"plugin.metrics", "plugin", false},
		{"Plugin.Metrics", "plugin.metrics", false}, // case-sensitive

		// Single-segment wildcard.
		{"state.*.onoff.value", "state.matter:3.onoff.value", true},
		{"state.*.onoff.value", "state.matter:3.brightness.value", false},
		{"state.*", "state.matter:3", true},
		{"state.*", "state.matter:3.onoff", false},
		{"state.*", "state", false},
		{"*", "state", true},
		{"*", "state.matter:3", false},
		{"*.*.*.*", "state.matter:3.onoff.value", true},

		// Multi-segment wildcard.
		{"state.**", "state.matter:3.onoff.value", true},
		{"state.**", "state.matter:3", true},
		// The case that decides whether ** is one-or-more or zero-or-more.
		// One-or-more: subscribing to a category must not deliver the bare
		// category as though it were an event.
		{"state.**", "state", false},
		{"**", "state.matter:3.onoff.value", true},
		{"**", "state", true},
		{"state.**", "event.matter:3.motion", false},

		// Device-specific subscriptions, the shape from spec §8.2.
		{"event.matter:3.*", "event.matter:3.button_pressed", true},
		{"event.matter:3.*", "event.matter:4.button_pressed", false},
		{"plugin.log.error", "plugin.log.error", true},
		{"plugin.log.error", "plugin.log.debug", false},

		// A ref containing a colon is one segment, which is the whole reason
		// colons are the recommended separator.
		{"state.*.*.*", "state.zigbee:0x00158d0001a2b3c4.temperature.value", true},
		{"lifecycle.**", "lifecycle.zigbee:0x00158d0001a2b3c4.offline", true},
	}

	for _, tc := range tests {
		if got := tc.filter.Match(tc.topic); got != tc.want {
			t.Errorf("Filter(%q).Match(%q) = %v, want %v", tc.filter, tc.topic, got, tc.want)
		}
	}
}

func TestMatcherEmptyMatchesNothing(t *testing.T) {
	// Deny-by-default. A peer that has not subscribed gets silence, so a core
	// that forgets to subscribe sees an obvious symptom rather than a quiet
	// bandwidth problem.
	var zero *Matcher
	if zero.Match("state.matter:3.onoff.value") {
		t.Error("nil Matcher matched a topic, want no match")
	}

	m, err := NewMatcher()
	if err != nil {
		t.Fatalf("NewMatcher() = %v", err)
	}
	if !m.Empty() {
		t.Error("NewMatcher() with no filters is not Empty")
	}
	if m.Match("state.matter:3.onoff.value") {
		t.Error("empty Matcher matched a topic, want no match")
	}
}

func TestMatcherAnyFilterMatches(t *testing.T) {
	m, err := NewMatcher("state.**", "plugin.log.error")
	if err != nil {
		t.Fatalf("NewMatcher() = %v", err)
	}

	for _, tp := range []Topic{"state.matter:3.onoff.value", "plugin.log.error"} {
		if !m.Match(tp) {
			t.Errorf("Match(%q) = false, want true", tp)
		}
	}
	for _, tp := range []Topic{"plugin.log.debug", "event.matter:3.motion", "state"} {
		if m.Match(tp) {
			t.Errorf("Match(%q) = true, want false", tp)
		}
	}
}

func TestMatcherSetReplaces(t *testing.T) {
	// subscribe sets the filter set rather than adding to it (§8.2), which is
	// what makes re-sending the full set after a reconnect correct.
	m, err := NewMatcher("state.**")
	if err != nil {
		t.Fatalf("NewMatcher() = %v", err)
	}
	if err := m.Set("event.**"); err != nil {
		t.Fatalf("Set() = %v", err)
	}
	if m.Match("state.matter:3.onoff.value") {
		t.Error("old filter still matches after Set")
	}
	if !m.Match("event.matter:3.motion") {
		t.Error("new filter does not match after Set")
	}
	if got := m.Filters(); len(got) != 1 || got[0] != "event.**" {
		t.Errorf("Filters() = %v, want [event.**]", got)
	}
}

func TestMatcherSetRejectsBadFilterAtomically(t *testing.T) {
	m, err := NewMatcher("state.**")
	if err != nil {
		t.Fatalf("NewMatcher() = %v", err)
	}
	// A subscribe carrying one bad filter must not half-apply: the caller
	// would then be subscribed to something it never asked for and would have
	// no way to tell.
	if err := m.Set("event.**", "bad.**.filter"); err == nil {
		t.Fatal("Set with an invalid filter = nil, want an error")
	}
	if !m.Match("state.matter:3.onoff.value") {
		t.Error("a rejected Set changed the filter set")
	}
	if m.Match("event.matter:3.motion") {
		t.Error("a rejected Set applied one of its filters")
	}
}

func TestFiltersIsACopy(t *testing.T) {
	m, err := NewMatcher("state.**")
	if err != nil {
		t.Fatalf("NewMatcher() = %v", err)
	}
	got := m.Filters()
	got[0] = "event.**"
	if m.Match("event.matter:3.motion") {
		t.Error("mutating the slice from Filters() changed the matcher")
	}
}

func TestTopicCategory(t *testing.T) {
	tests := map[Topic]string{
		"state.matter:3.onoff.value": "state",
		"plugin.metrics":             "plugin",
		"heartbeat":                  "heartbeat",
		"":                           "",
	}
	for tp, want := range tests {
		if got := tp.Category(); got != want {
			t.Errorf("Topic(%q).Category() = %q, want %q", tp, got, want)
		}
	}
}
