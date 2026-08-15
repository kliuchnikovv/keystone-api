/**
 * Wire types and frame predicates.
 *
 * The generated types in types.gen.ts are the schema-derived shapes; this
 * module adds the narrowing helpers and the constants that go with them.
 */
import type { Frame, PushFrame, RequestFrame, ResponseFrame } from "./types.gen.js";

export type { Frame, PushFrame, RequestFrame, ResponseFrame };

/** Protocol major version this package speaks (spec §5.2). */
export const PROTOCOL_V1 = "v1";

export const FRAME_REQUEST = "request";
export const FRAME_RESPONSE = "response";
export const FRAME_PUSH = "push";

export function isRequest(f: Frame): f is RequestFrame {
  return f.type === FRAME_REQUEST;
}

export function isResponse(f: Frame): f is ResponseFrame {
  return f.type === FRAME_RESPONSE;
}

export function isPush(f: Frame): f is PushFrame {
  return f.type === FRAME_PUSH;
}

/**
 * Structural validation from spec §4 — the rules that hold for every frame
 * regardless of method or topic.
 *
 * Not schema validation: this is the cheap always-on check that lets the
 * runtime trust the fields it is about to read. Returns a reason, or undefined
 * if the frame is well-formed.
 */
export function frameProblem(f: Frame): string | undefined {
  if (!f || typeof f !== "object") return "not an object";

  switch (f.type) {
    case FRAME_REQUEST: {
      const r = f as RequestFrame;
      if (!r.id) return "request frame has no id";
      if (!r.method) return `request ${r.id} has no method`;
      return undefined;
    }
    case FRAME_RESPONSE: {
      const r = f as ResponseFrame;
      if (!r.id) return "response frame has no id";
      // Exactly one of result / error. A null result is present and distinct
      // from an absent one, which is why this tests key presence rather than
      // truthiness.
      const hasResult = "result" in r;
      const hasError = "error" in r && r.error !== undefined;
      if (hasResult && hasError) return `response ${r.id} carries both result and error`;
      if (!hasResult && !hasError) return `response ${r.id} carries neither result nor error`;
      return undefined;
    }
    case FRAME_PUSH: {
      const p = f as PushFrame;
      if (!p.topic) return "push frame has no topic";
      if (!isValidTopic(p.topic)) return `push topic ${JSON.stringify(p.topic)} is malformed`;
      if (!p.seq || p.seq < 1) return `push on ${p.topic} has no seq`;
      if (!p.ts) return `push on ${p.topic} has no ts`;
      return undefined;
    }
    default:
      return `unknown frame type ${JSON.stringify((f as { type?: unknown }).type)}`;
  }
}

/**
 * A topic is well-formed when it has at least one segment and no segment is
 * empty or contains whitespace or a wildcard.
 *
 * The ban on "." inside a value is the point. Every well-known topic shape
 * interpolates identifiers and assumes each occupies one segment; a value
 * containing a dot shifts everything after it and pattern matching then fails
 * silently, with nothing logged.
 */
export function isValidTopic(topic: string): boolean {
  if (!topic) return false;
  return topic.split(".").every((seg) => seg.length > 0 && !/[\s*]/.test(seg));
}

/** Short rendering for logs — enough to follow a session. */
export function describeFrame(f: Frame): string {
  if (isRequest(f)) return `request ${f.id} ${f.method}`;
  if (isResponse(f)) return f.error ? `response ${f.id} error ${f.error.code}` : `response ${f.id} ok`;
  if (isPush(f)) return `push #${f.seq} ${f.topic}`;
  return `frame ${JSON.stringify((f as { type?: unknown }).type)}`;
}

/**
 * Formats a timestamp for the wire: UTC, exactly three fractional digits.
 *
 * toISOString already produces this shape; the function exists so the
 * requirement is stated in one place and matches the Go side's Time type,
 * which has to work harder for it — Go's RFC3339Nano drops trailing zeros, so
 * without pinning the two languages serialise the same instant differently and
 * every cross-language golden fixture diverges.
 */
export function wireTime(d: Date = new Date()): string {
  return d.toISOString().replace(/\.(\d{3})\d*Z$/, ".$1Z");
}
