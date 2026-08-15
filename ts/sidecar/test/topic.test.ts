import { describe, expect, it } from "vitest";

import { TopicMatcher, matchTopic, validateFilter } from "../src/topic.js";
import { isValidTopic } from "../src/wire.js";

describe("isValidTopic", () => {
  const valid = [
    "plugin.metrics",
    "state.matter:3.onoff.value",
    "state.17.brightness.level",
    "lifecycle.zigbee:0x00158d0001a2b3c4.offline",
    "heartbeat",
    // Colons are the recommended separator precisely because dots are not.
    "state.aqara:hub:1.aqara:vibration.value",
  ];
  for (const t of valid) {
    it(`accepts ${t}`, () => expect(isValidTopic(t)).toBe(true));
  }

  const invalid = [
    "",
    ".",
    ".leading",
    "trailing.",
    "double..dot",
    // Wildcards belong in filters, never in a published topic: delivering one
    // would make a formatting bug look like a subscription.
    "state.**",
    "state.*.onoff.value",
    "state.matter:3.on off.value",
  ];
  for (const t of invalid) {
    it(`rejects ${JSON.stringify(t)}`, () => expect(isValidTopic(t)).toBe(false));
  }
});

describe("validateFilter", () => {
  for (const f of ["state.**", "**", "event.matter:3.*", "plugin.log.error", "state.*.*.*"]) {
    it(`accepts ${f}`, () => expect(() => validateFilter(f)).not.toThrow());
  }

  for (const f of [
    "",
    "a..b",
    // ** only at the end: anywhere else the split point is ambiguous.
    "state.**.value",
    "**.value",
    // A partial wildcard would produce a filter that silently matches nothing.
    "stat*.matter:3",
    "state.matt*:3.onoff.value",
  ]) {
    it(`rejects ${JSON.stringify(f)}`, () => expect(() => validateFilter(f)).toThrow());
  }
});

describe("matchTopic", () => {
  const cases: [string, string, boolean][] = [
    ["plugin.metrics", "plugin.metrics", true],
    ["plugin.metrics", "plugin.metrics.extra", false],
    ["plugin.metrics", "plugin", false],
    ["Plugin.Metrics", "plugin.metrics", false],

    ["state.*.onoff.value", "state.matter:3.onoff.value", true],
    ["state.*.onoff.value", "state.matter:3.brightness.value", false],
    ["state.*", "state.matter:3", true],
    ["state.*", "state.matter:3.onoff", false],
    ["*", "state", true],
    ["*", "state.matter:3", false],

    ["state.**", "state.matter:3.onoff.value", true],
    ["state.**", "state.matter:3", true],
    // The case that decides whether ** is one-or-more or zero-or-more.
    // One-or-more: subscribing to a category must not deliver the bare
    // category as though it were an event.
    ["state.**", "state", false],
    ["**", "state", true],
    ["state.**", "event.matter:3.motion", false],

    ["event.matter:3.*", "event.matter:3.button_pressed", true],
    ["event.matter:3.*", "event.matter:4.button_pressed", false],
    ["plugin.log.error", "plugin.log.debug", false],

    // A ref containing a colon is one segment — the whole reason colons are
    // the recommended separator.
    ["state.*.*.*", "state.zigbee:0x00158d0001a2b3c4.temperature.value", true],
  ];

  for (const [filter, topic, want] of cases) {
    it(`${filter} vs ${topic} -> ${want}`, () => {
      expect(matchTopic(filter, topic)).toBe(want);
    });
  }
});

describe("TopicMatcher", () => {
  it("matches nothing when empty", () => {
    // Deny-by-default. A core that forgets to subscribe sees an obvious
    // symptom rather than a quiet bandwidth problem.
    const m = new TopicMatcher();
    expect(m.empty).toBe(true);
    expect(m.match("state.matter:3.onoff.value")).toBe(false);
  });

  it("matches if any filter does", () => {
    const m = new TopicMatcher(["state.**", "plugin.log.error"]);
    expect(m.match("state.matter:3.onoff.value")).toBe(true);
    expect(m.match("plugin.log.error")).toBe(true);
    expect(m.match("plugin.log.debug")).toBe(false);
    expect(m.match("state")).toBe(false);
  });

  it("replaces rather than merges on set", () => {
    // subscribe sets the filter set (§8.2), which is what makes re-sending the
    // full set after a reconnect correct.
    const m = new TopicMatcher(["state.**"]);
    m.set(["event.**"]);
    expect(m.match("state.matter:3.onoff.value")).toBe(false);
    expect(m.match("event.matter:3.motion")).toBe(true);
    expect(m.filters).toEqual(["event.**"]);
  });

  it("rejects a bad filter without half-applying the set", () => {
    // A subscribe carrying one bad filter must not leave the caller
    // subscribed to something it never asked for, with no way to tell.
    const m = new TopicMatcher(["state.**"]);
    expect(() => m.set(["event.**", "bad.**.filter"])).toThrow();
    expect(m.match("state.matter:3.onoff.value")).toBe(true);
    expect(m.match("event.matter:3.motion")).toBe(false);
  });
});
