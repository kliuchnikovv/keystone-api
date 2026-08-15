/**
 * Typed errors — the TypeScript half of spec §9.
 */
import type { ErrorBody, ErrorCode } from "./types.gen.js";

export type { ErrorBody, ErrorCode };

/** The standard taxonomy. A plugin may mint codes in its own namespace. */
export const Code = {
  ProtocolUnsupported: "protocol.unsupported",
  ProtocolMalformed: "protocol.malformed",
  DeviceNotFound: "device.not_found",
  DeviceOffline: "device.offline",
  DeviceUnsupported: "device.unsupported",
  FeatureUnsupported: "feature.unsupported",
  PluginNotReady: "plugin.not_ready",
  PluginBusy: "plugin.busy",
  PluginInternal: "plugin.internal",
  AuthInvalidCredentials: "auth.invalid_credentials",
  AuthExpired: "auth.expired",
  NetworkTimeout: "network.timeout",
  NetworkUnavailable: "network.unavailable",
  PermissionDenied: "permission.denied",
  ValidationBadParams: "validation.bad_params",
} as const;

/**
 * Whether retrying could plausibly succeed, when the sender did not say
 * (spec §9.1).
 *
 * The distinction is whether waiting can change the answer: a sleeping device
 * will wake, an expired token will be refreshed, a malformed request will
 * still be malformed. Unknown codes default to false — a wrong retry can
 * hammer a device, while a missed retry surfaces an error someone can act on.
 */
const RETRYABLE: ReadonlySet<string> = new Set([
  Code.DeviceOffline,
  Code.PluginNotReady,
  Code.PluginBusy,
  Code.PluginInternal,
  Code.AuthExpired,
  Code.NetworkTimeout,
  Code.NetworkUnavailable,
]);

export function defaultRetryable(code: string): boolean {
  return RETRYABLE.has(code);
}

/** Options for constructing a ProtocolError. */
export interface ProtocolErrorInit {
  retryable?: boolean;
  details?: Record<string, unknown>;
  cause?: unknown;
}

/**
 * A failure reported by the far side, or produced locally in the same shape so
 * both look alike to a caller.
 */
export class ProtocolError extends Error {
  readonly code: string;
  readonly retryable: boolean;
  readonly details: Record<string, unknown> | undefined;

  constructor(code: string, message: string, init: ProtocolErrorInit = {}) {
    super(message, init.cause !== undefined ? { cause: init.cause } : undefined);
    this.name = "ProtocolError";
    this.code = code;
    this.retryable = init.retryable ?? defaultRetryable(code);
    this.details = init.details;
  }

  /** The wire form. The local cause is deliberately not included — the far
   * side has no use for our stack, and it would leak internals across the
   * boundary. */
  body(): ErrorBody {
    const out: ErrorBody = {
      code: this.code as ErrorCode,
      message: this.message,
      retryable: this.retryable,
    };
    if (this.details) out.details = this.details;
    return out;
  }

  /** Rebuilds a ProtocolError from a wire error body. */
  static fromBody(body: ErrorBody): ProtocolError {
    return new ProtocolError(body.code, body.message, {
      // retryable is optional on the wire: §9 makes retryability a property of
      // the code. Defaulting to false here would silently turn every omitted
      // device.offline into a permanent failure and stop the core retrying
      // sleeping devices — a bug that presents as "some devices just never
      // come back".
      retryable: body.retryable ?? defaultRetryable(body.code),
      ...(body.details ? { details: body.details } : {}),
    });
  }
}

/**
 * Converts any thrown value into the wire shape.
 *
 * Something that is not already a ProtocolError has not been classified
 * against the taxonomy, and plugin.internal is the honest answer for that.
 * Guessing a more specific code from the message would be worse.
 */
export function toErrorBody(err: unknown): ErrorBody {
  if (err instanceof ProtocolError) return err.body();
  const message = err instanceof Error ? err.message : String(err);
  return {
    code: Code.PluginInternal,
    message,
    retryable: defaultRetryable(Code.PluginInternal),
  };
}

/** Transport-level failures. These never cross the wire. */
export class NotConnectedError extends Error {
  constructor(message = "sidecar: not connected") {
    super(message);
    this.name = "NotConnectedError";
  }
}

export class PeerClosedError extends Error {
  constructor(message = "sidecar: peer closed") {
    super(message);
    this.name = "PeerClosedError";
  }
}

/**
 * Whether an operation is worth retrying.
 *
 * A failure to reach the far side counts: the call never happened, and the
 * supervisor is already reconnecting.
 */
export function isRetryable(err: unknown): boolean {
  if (err instanceof NotConnectedError) return true;
  if (err instanceof PeerClosedError) return false;
  if (err instanceof ProtocolError) return err.retryable;
  return false;
}

/** The code of a ProtocolError, or undefined. */
export function codeOf(err: unknown): string | undefined {
  return err instanceof ProtocolError ? err.code : undefined;
}

/** Convenience constructors matching the Go sentinels. */
export const errors = {
  protocolUnsupported: (m = "unsupported protocol version") =>
    new ProtocolError(Code.ProtocolUnsupported, m),
  protocolMalformed: (m = "malformed frame") => new ProtocolError(Code.ProtocolMalformed, m),
  deviceNotFound: (m = "device not found") => new ProtocolError(Code.DeviceNotFound, m),
  deviceOffline: (m = "device is offline") => new ProtocolError(Code.DeviceOffline, m),
  featureUnsupported: (m = "feature or action not supported") =>
    new ProtocolError(Code.FeatureUnsupported, m),
  pluginNotReady: (m = "plugin is not ready") => new ProtocolError(Code.PluginNotReady, m),
  pluginBusy: (m = "plugin is busy") => new ProtocolError(Code.PluginBusy, m),
  pluginInternal: (m = "internal plugin error") => new ProtocolError(Code.PluginInternal, m),
  networkTimeout: (m = "upstream timed out") => new ProtocolError(Code.NetworkTimeout, m),
  permissionDenied: (m = "permission denied") => new ProtocolError(Code.PermissionDenied, m),
  validationBadParams: (m = "invalid parameters") =>
    new ProtocolError(Code.ValidationBadParams, m),
} as const;
