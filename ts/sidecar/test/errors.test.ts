import { describe, expect, it } from "vitest";

import {
  Code,
  ProtocolError,
  NotConnectedError,
  PeerClosedError,
  codeOf,
  defaultRetryable,
  isRetryable,
  toErrorBody,
} from "../src/errors.js";

describe("defaultRetryable", () => {
  // Spec §9.1. The distinction is whether waiting could change the answer.
  const retryable = [
    Code.DeviceOffline,
    Code.PluginNotReady,
    Code.PluginBusy,
    Code.PluginInternal,
    Code.AuthExpired,
    Code.NetworkTimeout,
    Code.NetworkUnavailable,
  ];
  const not = [
    Code.ProtocolUnsupported,
    Code.ProtocolMalformed,
    Code.DeviceNotFound,
    Code.DeviceUnsupported,
    Code.FeatureUnsupported,
    Code.AuthInvalidCredentials,
    Code.PermissionDenied,
    Code.ValidationBadParams,
  ];

  for (const c of retryable) it(`${c} is retryable`, () => expect(defaultRetryable(c)).toBe(true));
  for (const c of not) it(`${c} is not retryable`, () => expect(defaultRetryable(c)).toBe(false));

  it("treats an unknown code as not retryable", () => {
    // A wrong retry can hammer a device; a missed retry surfaces an error
    // someone can act on.
    expect(defaultRetryable("aqara.unknown")).toBe(false);
  });
});

describe("ProtocolError.fromBody", () => {
  // The bug this guards against: retryable is optional on the wire, so a peer
  // that omits it would have every device.offline treated as permanent, and
  // the core would stop retrying sleeping devices. It presents as "some
  // devices just never come back".
  const cases: [string, Record<string, unknown>, boolean][] = [
    ["omitted, retryable code", { code: Code.DeviceOffline, message: "asleep" }, true],
    ["omitted, non-retryable code", { code: Code.DeviceNotFound, message: "gone" }, false],
    ["explicit true", { code: Code.DeviceNotFound, message: "gone", retryable: true }, true],
    ["explicit false overrides the default", { code: Code.DeviceOffline, message: "x", retryable: false }, false],
    ["unknown code", { code: "aqara.unknown", message: "?" }, false],
  ];

  for (const [name, body, want] of cases) {
    it(name, () => {
      const err = ProtocolError.fromBody(body as never);
      expect(err.retryable).toBe(want);
      expect(isRetryable(err)).toBe(want);
    });
  }
});

describe("error helpers", () => {
  it("round-trips through the wire body", () => {
    const orig = new ProtocolError(Code.DeviceNotFound, "node 3 not found", {
      details: { nodeId: "3" },
    });
    const back = ProtocolError.fromBody(orig.body());

    expect(back.code).toBe(orig.code);
    expect(back.message).toBe(orig.message);
    expect(back.retryable).toBe(orig.retryable);
    expect(back.details).toEqual({ nodeId: "3" });
  });

  it("keeps the local cause off the wire", () => {
    // The far side has no use for our stack, and it would leak internals
    // across the boundary.
    const err = new ProtocolError(Code.NetworkUnavailable, "upstream is down", {
      cause: new Error("dial tcp: connection refused"),
    });
    expect(JSON.stringify(err.body())).not.toContain("connection refused");
    expect(err.cause).toBeInstanceOf(Error);
  });

  it("classifies an unclassified throw as plugin.internal", () => {
    // Something not already a ProtocolError has not been checked against the
    // taxonomy. Guessing a more specific code from the message would be worse
    // than admitting we do not know.
    expect(toErrorBody(new Error("boom"))).toMatchObject({
      code: Code.PluginInternal,
      message: "boom",
      retryable: true,
    });
    expect(toErrorBody("a string")).toMatchObject({ code: Code.PluginInternal });
  });

  it("treats a failure to reach the far side as retryable", () => {
    // The call never happened, and the supervisor is already reconnecting.
    expect(isRetryable(new NotConnectedError())).toBe(true);
    // A closed peer will not reconnect, so retrying is pointless.
    expect(isRetryable(new PeerClosedError())).toBe(false);
    expect(isRetryable(new Error("something else"))).toBe(false);
    expect(isRetryable(undefined)).toBe(false);
  });

  it("exposes the code of a ProtocolError only", () => {
    expect(codeOf(new ProtocolError(Code.PluginBusy, "x"))).toBe(Code.PluginBusy);
    expect(codeOf(new Error("plain"))).toBeUndefined();
  });
});
