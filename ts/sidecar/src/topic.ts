/**
 * Topic matching (spec §8.2).
 *
 *   "*"   matches exactly one segment
 *   "**"  matches one or more trailing segments, and may only appear last
 *
 * Anything else is a case-sensitive literal. Note that "**" matches one or
 * more, not zero or more: "state.**" selects state.matter:3.onoff.value but
 * not the bare topic "state" — subscribing to a category should not deliver
 * the category itself as though it were an event.
 */
import { ProtocolError, Code } from "./errors.js";

/** Throws if the filter is not well-formed. */
export function validateFilter(filter: string): void {
  const bad = (why: string): never => {
    throw new ProtocolError(Code.ValidationBadParams, `topic filter ${JSON.stringify(filter)}: ${why}`);
  };

  if (!filter) bad("is empty");

  const segs = filter.split(".");
  segs.forEach((seg, i) => {
    if (!seg) bad("has an empty segment");
    // A non-final ** would make matching ambiguous — "a.**.b" could split
    // anywhere — and no well-known topic shape needs it.
    if (seg === "**" && i !== segs.length - 1) bad("** may only appear as the last segment");
    // Partial wildcards ("stat*") are not in the grammar. Accepting one and
    // ignoring it at match time would produce a filter that silently matches
    // nothing, with no error to explain why the subscription looks dead.
    if (seg !== "*" && seg !== "**" && seg.includes("*")) {
      bad(`${JSON.stringify(seg)} mixes a wildcard with literal text`);
    }
    if (/\s/.test(seg)) bad("contains whitespace");
  });
}

/** Whether a filter selects a topic. */
export function matchTopic(filter: string, topic: string): boolean {
  return matchSegments(filter.split("."), topic.split("."));
}

function matchSegments(pattern: string[], topic: string[]): boolean {
  for (let i = 0; i < pattern.length; i++) {
    const p = pattern[i]!;
    if (p === "**") return topic.length > i;
    if (i >= topic.length) return false;
    if (p !== "*" && p !== topic[i]) return false;
  }
  return pattern.length === topic.length;
}

/**
 * A compiled filter set.
 *
 * An empty matcher matches nothing, and that is the intended default: a peer
 * that has not subscribed receives silence rather than every push in the
 * house. Deny-by-default also means a core that forgets to subscribe sees an
 * obvious symptom instead of a subtle load problem.
 */
export class TopicMatcher {
  #filters: string[] = [];
  #patterns: string[][] = [];

  constructor(filters: readonly string[] = []) {
    this.set(filters);
  }

  /**
   * Replaces the filter set.
   *
   * Replace, not merge: subscribe is defined as setting the filter set (§8.2),
   * which makes re-sending the full desired set after a reconnect both correct
   * and idempotent.
   *
   * Atomic — one bad filter rejects the whole call, so a caller is never left
   * subscribed to a subset it did not ask for.
   */
  set(filters: readonly string[]): void {
    for (const f of filters) validateFilter(f);
    this.#filters = [...filters];
    this.#patterns = filters.map((f) => f.split("."));
  }

  match(topic: string): boolean {
    const segs = topic.split(".");
    return this.#patterns.some((p) => matchSegments(p, segs));
  }

  get filters(): readonly string[] {
    return [...this.#filters];
  }

  get empty(): boolean {
    return this.#patterns.length === 0;
  }
}
