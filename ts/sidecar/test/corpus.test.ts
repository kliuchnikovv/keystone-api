/**
 * The shared fixture corpus, run against the TypeScript types.
 *
 * These are the same files the Go suite reads. A schema change that is not
 * reflected in both type sets fails here rather than in production.
 */
import { readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

import { frameProblem, isPush, isRequest, isResponse } from "../src/wire.js";
import { encodeFrame } from "../src/codec.js";
import { NdjsonDecoder } from "../src/codec.js";
import type { Frame } from "../src/types.gen.js";

const corpus = fileURLToPath(
  new URL("../../../schemas/sidecar/v1/testdata/frames/", import.meta.url),
);

function fixtures(kind: "valid" | "invalid"): { name: string; raw: string }[] {
  return readdirSync(join(corpus, kind))
    .filter((n) => n.endsWith(".json"))
    .sort()
    .map((name) => ({ name, raw: readFileSync(join(corpus, kind, name), "utf8").trim() }));
}

describe("valid fixtures", () => {
  const files = fixtures("valid");

  it("the corpus resolved", () => {
    expect(files.length).toBeGreaterThan(0);
  });

  for (const { name, raw } of files) {
    it(`${name} is structurally valid and survives a round trip`, () => {
      const frame = JSON.parse(raw) as Frame;

      expect(frameProblem(frame)).toBeUndefined();

      // Exactly one predicate must claim it.
      const claims = [isRequest(frame), isResponse(frame), isPush(frame)].filter(Boolean);
      expect(claims).toHaveLength(1);

      // Re-encoding must produce something that still decodes to the same
      // thing. A field the types silently dropped would go unnoticed until a
      // peer in another language stopped receiving data it was sent.
      const encoded = encodeFrame(frame);
      expect(encoded).toBeDefined();
      expect(JSON.parse(encoded!)).toEqual(JSON.parse(raw));
    });
  }
});

describe("invalid fixtures", () => {
  const files = fixtures("invalid");

  it("there are negative fixtures", () => {
    expect(files.length).toBeGreaterThan(0);
  });

  // The decoder enforces structure, not the full schema — some negative
  // fixtures are schema violations that only a validator catches (a
  // nanosecond-precision timestamp, a seq beyond the safe integer range).
  // Those are the schema suite's job; here we check the ones frameProblem
  // is responsible for.
  const structural = new Set([
    "no-type.json",
    "unknown-type.json",
    "request-no-id.json",
    "request-no-method.json",
    "response-result-and-error.json",
    "response-neither.json",
    "push-no-seq.json",
    "push-seq-zero.json",
    "push-topic-with-wildcard.json",
    "push-topic-empty-segment.json",
  ]);

  for (const { name, raw } of files.filter((f) => structural.has(f.name))) {
    it(`${name} is rejected`, () => {
      const dropped: Error[] = [];
      const dec = new NdjsonDecoder({ onError: (e) => dropped.push(e) });
      const frames = dec.push(raw + "\n");

      expect(frames).toHaveLength(0);
      expect(dropped).toHaveLength(1);
    });
  }
});
