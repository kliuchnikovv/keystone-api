/**
 * Generated from schemas/sidecar/v1 — DO NOT EDIT.
 *
 * Regenerate with `make gen`. `make verify-gen` re-runs the generator and
 * fails on a diff, so a hand edit here is caught in CI rather than shipped.
 *
 * The Go package's types are hand-written instead, and held to the same
 * schemas by a corpus round-trip and a reflective struct diff. CONTRIBUTING.md
 * explains why the two languages are treated differently.
 */

/**
 * Any single message on the wire. One NDJSON line is exactly one frame.
 *
 * This is the schema to validate raw inbound bytes against; the per-method and per-topic payload schemas are a second, optional layer applied after routing.
 */
export type Frame = {
  type: "request" | "response" | "push";
} & (RequestFrame | ResponseFrame | PushFrame);
/**
 * Unique within a session and within the sending direction. A ULID is conventional; any unique string is legal (spec §4.1).
 */
export type RequestId = string;
/**
 * Exactly one per request, matched by `id` (spec §4.2).
 *
 * Exactly one of `result` / `error` is present. A response carrying both, or neither, is malformed — the `oneOf` below is what enforces that, since JSON Schema has no direct way to say "exactly one of these keys".
 */
export type ResponseFrame = {
  type: "response";
  id: RequestId;
  /**
   * Shape is determined by the method of the originating request. `null` is a legal result and is distinct from an absent one.
   */
  result?: {
    [k: string]: unknown;
  };
  error?: ErrorBody;
} & (
  | {
      result: unknown;
    }
  | {
      error: unknown;
    }
);
/**
 * Dotted `namespace.identifier`. The standard taxonomy in §9.1 is listed for tooling and autocomplete, but the pattern is authoritative: a plugin may mint codes in its own namespace and the receiver must not reject them.
 */
export type ErrorCode = (
  | (
      | "protocol.unsupported"
      | "protocol.malformed"
      | "device.not_found"
      | "device.offline"
      | "device.unsupported"
      | "feature.unsupported"
      | "plugin.not_ready"
      | "plugin.busy"
      | "plugin.internal"
      | "auth.invalid_credentials"
      | "auth.expired"
      | "network.timeout"
      | "network.unavailable"
      | "permission.denied"
      | "validation.bad_params"
    )
  | {
      [k: string]: unknown;
    }
) &
  string;
/**
 * Concrete, fully-qualified push topic. Contains no wildcards — see `topicFilter` for the subscription form.
 */
export type Topic = string;
/**
 * Monotonically increasing counter, per sender, starting at 1 and incrementing by exactly 1 for every push actually written (spec §4.3).
 *
 * Capped at 2^53-1 rather than 2^64-1 so the value survives a round-trip through a JavaScript `number` without precision loss. A plugin emitting 10,000 pushes per second would reach the ceiling in roughly 28,000 years, so the cap costs nothing and putting `seq` on the wire as a string or a BigInt would cost readability.
 */
export type Seq = number;
/**
 * RFC 3339 / ISO 8601 instant in UTC with exactly three fractional digits, e.g. `2026-08-15T21:04:11.123Z`.
 *
 * The fixed precision is not cosmetic. Go's time.Time marshals RFC3339Nano with variable precision while JavaScript's toISOString always emits three digits, so without pinning it the two implementations produce different bytes for the same instant and every golden fixture diverges.
 */
export type IsoTime = string;
/**
 * One dot-separated component of a topic name.
 *
 * Topics are dot-separated (spec §8.1) and every well-known topic shape assumes each interpolated value occupies exactly one segment. A value containing a dot would silently shift every following segment and break `*` matching — `state.*.*.*` would stop matching a device whose ref contains a dot, with no error anywhere. So no segment may contain `.`, whitespace, or the wildcard characters.
 *
 * Identifiers that are naturally dotted must substitute another separator when crossing the wire. Keystone's own convention for vendor-prefixed feature keys is `aqara.vibration` in Go; on the wire that becomes `aqara:vibration`.
 */
export type DeviceRef = string;
/**
 * One dot-separated component of a topic name.
 *
 * Topics are dot-separated (spec §8.1) and every well-known topic shape assumes each interpolated value occupies exactly one segment. A value containing a dot would silently shift every following segment and break `*` matching — `state.*.*.*` would stop matching a device whose ref contains a dot, with no error anywhere. So no segment may contain `.`, whitespace, or the wildcard characters.
 *
 * Identifiers that are naturally dotted must substitute another separator when crossing the wire. Keystone's own convention for vendor-prefixed feature keys is `aqara.vibration` in Go; on the wire that becomes `aqara:vibration`.
 */
export type FeatureKey = string;
/**
 * One dot-separated component of a topic name.
 *
 * Topics are dot-separated (spec §8.1) and every well-known topic shape assumes each interpolated value occupies exactly one segment. A value containing a dot would silently shift every following segment and break `*` matching — `state.*.*.*` would stop matching a device whose ref contains a dot, with no error anywhere. So no segment may contain `.`, whitespace, or the wildcard characters.
 *
 * Identifiers that are naturally dotted must substitute another separator when crossing the wire. Keystone's own convention for vendor-prefixed feature keys is `aqara.vibration` in Go; on the wire that becomes `aqara:vibration`.
 */
export type StateKey = string;
/**
 * One dot-separated component of a topic name.
 *
 * Topics are dot-separated (spec §8.1) and every well-known topic shape assumes each interpolated value occupies exactly one segment. A value containing a dot would silently shift every following segment and break `*` matching — `state.*.*.*` would stop matching a device whose ref contains a dot, with no error anywhere. So no segment may contain `.`, whitespace, or the wildcard characters.
 *
 * Identifiers that are naturally dotted must substitute another separator when crossing the wire. Keystone's own convention for vendor-prefixed feature keys is `aqara.vibration` in Go; on the wire that becomes `aqara:vibration`.
 */
export type ActionKey = string;
/**
 * One dot-separated component of a topic name.
 *
 * Topics are dot-separated (spec §8.1) and every well-known topic shape assumes each interpolated value occupies exactly one segment. A value containing a dot would silently shift every following segment and break `*` matching — `state.*.*.*` would stop matching a device whose ref contains a dot, with no error anywhere. So no segment may contain `.`, whitespace, or the wildcard characters.
 *
 * Identifiers that are naturally dotted must substitute another separator when crossing the wire. Keystone's own convention for vendor-prefixed feature keys is `aqara.vibration` in Go; on the wire that becomes `aqara:vibration`.
 */
export type EventKey = string;
/**
 * Coarse classification used for UI grouping and rule defaults.
 *
 * Deliberately an open string, not an enum: a plugin may report a type this version of the core has never heard of, and that must not be a protocol error. The well-known set is `light`, `switch`, `plug`, `sensor`, `motion_sensor`, `contact_sensor`, `thermostat`, `cover`, `lock`, `media_player`, `speaker`, `fan`, `air_purifier`, `button`, `alarm`, `appliance`, `vacuum`, `camera`, `doorbell`, `ev_charger`, `water_heater`, `valve`, `energy_meter`.
 */
export type DeviceType = string;
/**
 * Subscription pattern over topics (spec §8.2).
 *
 * - `*` matches exactly one segment
 * - `**` matches one or more trailing segments and may only appear last
 * - anything else is a case-sensitive literal
 *
 * Note that `**` matches *one or more*, not zero or more: `state.**` matches `state.matter:3.onoff.value` but does not match the bare topic `state`.
 */
export type TopicFilter = string;
/**
 * Informational only — used in logs and user-facing UI. Compatibility is decided by the `protocol` field, never by this.
 */
export type Semver = string;
/**
 * Major version of the sidecar protocol. Incompatible changes bump this; within one major only backward-compatible additions are permitted (spec §5.2).
 */
export type ProtocolVersion = string;
/**
 * Identifier of the plugin, matching the `metadata.name` of its plugin.yaml manifest.
 */
export type PluginName = string;
/**
 * What caused a state value to be reported.
 *
 * `device_report` — the device told us, unprompted or via subscription.
 * `user_write` — echo of a write this core issued.
 * `adapter_derived` — the plugin computed it rather than reading it.
 */
export type StateOrigin = "device_report" | "user_write" | "adapter_derived";
/**
 * One dotted capability identifier. The pattern is shared verbatim with plugin.yaml's `spec.capabilities`.
 */
export type CapabilityId = string;
/**
 * Advertised by the core in the hello response — what the core itself supports.
 *
 * `seq.replay` — the core tracks `seq` and will call `resume` after a reconnect. A plugin that sees this must keep its ring buffer.
 * `topic.filtering` — the core sends `subscribe` filters; a plugin that sees this may drop unmatched pushes before numbering them.
 */
export type CoreFeature = "seq.replay" | "topic.filtering";
/**
 * A complete step: the common fields plus exactly one type-specific branch.
 */
export type ConfigFlowStep = ConfigFlowStepBase &
  (
    | {
        type?: "form";
        schema: {};
        /**
         * Widget overrides keyed by field name — `password`, `textarea`, `select`. Advisory: a renderer that does not know a hint falls back to the type.
         */
        uiHints?: {
          [k: string]: string;
        };
        defaults?: {};
      }
    | {
        type?: "oauth";
        authUrl: string;
        callbackPath?: string;
      }
    | {
        type?: "qr-scan";
        hint?: string;
        /**
         * Whether the user may type the payload instead. True for Matter setup codes, which are printed as digits as well as a QR.
         */
        allowManualEntry?: boolean;
      }
    | {
        type?: "progress";
        sessionId: ConfigFlowSessionId;
        progress?: number;
        message?: string;
        cancellable?: boolean;
      }
    | {
        type?: "confirm";
        confirmLabel?: string;
        cancelLabel?: string;
      }
    | {
        type?: "pick-device";
        devices: CommissionableDevice[];
        multiple?: boolean;
      }
    | {
        type?: "manual-action";
        instruction: string;
        imageUrl?: string;
      }
    | {
        type?: "error";
        error: ErrorBody;
        retryStep?: string;
      }
    | {
        type?: "complete";
        message?: string;
        /**
         * Lets the core jump straight to what was just added.
         */
        deviceRefs?: DeviceRef[];
      }
  );
/**
 * One dot-separated component of a topic name.
 *
 * Topics are dot-separated (spec §8.1) and every well-known topic shape assumes each interpolated value occupies exactly one segment. A value containing a dot would silently shift every following segment and break `*` matching — `state.*.*.*` would stop matching a device whose ref contains a dot, with no error anywhere. So no segment may contain `.`, whitespace, or the wildcard characters.
 *
 * Identifiers that are naturally dotted must substitute another separator when crossing the wire. Keystone's own convention for vendor-prefixed feature keys is `aqara.vibration` in Go; on the wire that becomes `aqara:vibration`.
 */
export type ConfigFlowSessionId = string;
/**
 * What a plugin advertises in `hello`. Unknown ids are legal and simply unused by the core.
 */
export type CapabilityList = CapabilityId[];
/**
 * What the core advertises back in the hello response. Closed enum, unlike plugin capabilities: a plugin must not have to guess what an unknown core feature implies about its own obligations.
 */
export type CoreFeatureList = CoreFeature[];
export type DiscoverResult = DiscoveredDevice[];
/**
 * A complete step: the common fields plus exactly one type-specific branch.
 */
export type ConfigFlowResult = ConfigFlowStepBase &
  (
    | {
        type?: "form";
        schema: {};
        /**
         * Widget overrides keyed by field name — `password`, `textarea`, `select`. Advisory: a renderer that does not know a hint falls back to the type.
         */
        uiHints?: {
          [k: string]: string;
        };
        defaults?: {};
      }
    | {
        type?: "oauth";
        authUrl: string;
        callbackPath?: string;
      }
    | {
        type?: "qr-scan";
        hint?: string;
        /**
         * Whether the user may type the payload instead. True for Matter setup codes, which are printed as digits as well as a QR.
         */
        allowManualEntry?: boolean;
      }
    | {
        type?: "progress";
        sessionId: ConfigFlowSessionId;
        progress?: number;
        message?: string;
        cancellable?: boolean;
      }
    | {
        type?: "confirm";
        confirmLabel?: string;
        cancelLabel?: string;
      }
    | {
        type?: "pick-device";
        devices: CommissionableDevice[];
        multiple?: boolean;
      }
    | {
        type?: "manual-action";
        instruction: string;
        imageUrl?: string;
      }
    | {
        type?: "error";
        error: ErrorBody;
        retryStep?: string;
      }
    | {
        type?: "complete";
        message?: string;
        /**
         * Lets the core jump straight to what was just added.
         */
        deviceRefs?: DeviceRef[];
      }
  );

/**
 * Initiated by either side, expects exactly one response carrying the same `id` (spec §4.1).
 *
 * Both peers may send requests at any time. There is no client and no server at this layer — see the `symmetry` note in the repository README.
 */
export interface RequestFrame {
  type: "request";
  id: RequestId;
  /**
   * See methods/index.json for the v1 inventory. An unknown method must be answered with `protocol.unsupported`, never ignored — the caller is blocked on a response.
   */
  method: string;
  /**
   * Shape is determined by `method`. Absent is equivalent to `{}`.
   */
  params?: {
    [k: string]: unknown;
  };
}
/**
 * Structured error carried in a response frame (spec §9).
 */
export interface ErrorBody {
  code: ErrorCode;
  /**
   * Shown to operators and in logs. Not parsed; put anything a caller needs to branch on in `code` or `details`.
   */
  message: string;
  /**
   * Deliberately NOT required.
   *
   * Retryability is a property of the code (§9.1), and the receiver is expected to fall back to the taxonomy default when the field is absent. Making it required would mean a sender that omits it produces a hard failure; making it default to `false` in the parser would be worse still — every omitted `device.offline` would become non-retryable and the core would stop waking sleeping devices.
   *
   * Senders should set it explicitly. Receivers must tolerate its absence.
   */
  retryable?: boolean;
  /**
   * Free-form. Conventionally carries the identifiers that failed, e.g. `{"deviceRef": "matter:3"}`.
   */
  details?: {};
}
/**
 * Asynchronous, unacknowledged message (spec §4.3). Nothing is sent back and no `id` is involved.
 *
 * `seq` numbers only the pushes that were actually written to this stream. Pushes filtered out by the receiver's subscription are never numbered — numbering them would leave permanent gaps for topics the receiver never asked for, which would destroy the gap-detection signal in §10.2.
 */
export interface PushFrame {
  type: "push";
  topic: Topic;
  seq: Seq;
  ts: IsoTime;
  /**
   * Shape is determined by `topic` — see topics/index.json.
   */
  payload?: {
    [k: string]: unknown;
  };
}
/**
 * A functional module of a device — the unit a rule or the UI talks to. One RGB bulb has several: `onoff`, `brightness`, `color`, `color_temp`. Modelled on Matter clusters but transport-independent.
 */
export interface Feature {
  key: FeatureKey;
  states?: StateKey[];
  actions?: ActionKey[];
  /**
   * Events have no persistent value — a button press is an event, the button's battery level is a state.
   */
  events?: EventKey[];
  /**
   * Static per-feature parameters the core needs in order to interpret values — ranges, units, supported modes. Free-form; the core passes what it does not understand through to the UI.
   */
  config?: {};
}
/**
 * A device the transport can already use. The core decides separately whether to admit it to the registry.
 */
export interface DiscoveredDevice {
  deviceRef: DeviceRef;
  type: DeviceType;
  /**
   * Often empty. The core prefers the user's own label when one exists.
   */
  name?: string;
  manufacturer?: string;
  model?: string;
  features?: Feature[];
  /**
   * Opaque string map — firmware version, endpoint numbers, vendor ids. Carried through to the UI and diagnostics.
   */
  metadata?: {
    [k: string]: string;
  };
}
/**
 * A device advertising itself as ready to pair but not yet in any fabric. Everything here comes from the advertisement — the device has not been interviewed, so there are no features and no state.
 *
 * Discovery does not remove the need for a setup code: the passcode is never advertised. It only spares the user from working out which device is which.
 */
export interface CommissionableDevice {
  /**
   * One dot-separated component of a topic name.
   *
   * Topics are dot-separated (spec §8.1) and every well-known topic shape assumes each interpolated value occupies exactly one segment. A value containing a dot would silently shift every following segment and break `*` matching — `state.*.*.*` would stop matching a device whose ref contains a dot, with no error anywhere. So no segment may contain `.`, whitespace, or the wildcard characters.
   *
   * Identifiers that are naturally dotted must substitute another separator when crossing the wire. Keystone's own convention for vendor-prefixed feature keys is `aqara.vibration` in Go; on the wire that becomes `aqara:vibration`.
   */
  ref: string;
  name?: string;
  type?: DeviceType;
  vendorId?: number;
  productId?: number;
  /**
   * Matches the digits printed next to the device's QR code, so the UI can tell two identical bulbs apart.
   */
  discriminator?: number;
}
/**
 * A single attribute reading. `value` may be any JSON type — bool, number, string, or a structured object for compound attributes like colour.
 */
export interface StateValue {
  value: unknown;
  /**
   * Free-form but conventional: `celsius`, `percent`, `watts`, `lux`, `ppm`, `seconds`. Absent means dimensionless.
   */
  unit?: string;
  origin?: StateOrigin;
  /**
   * 0..1, for values a plugin inferred rather than measured. Absent means certain.
   */
  confidence?: number;
}
/**
 * Addresses one attribute: which device, which feature, which key.
 */
export interface AttrRef {
  deviceRef: DeviceRef;
  feature: FeatureKey;
  key: StateKey;
}
/**
 * The plugin's own view of its condition. Used both as the result of `getHealth` and as the payload of `plugin.health.<component>` pushes — one definition, two references, so a poll and a push can never disagree about shape.
 */
export interface HealthStatus {
  /**
   * `ok` — operating normally.
   * `degraded` — still serving, but something is wrong: upstream is flaky, pushes are being dropped, a subset of devices is unreachable.
   * `failing` — not serving. The core may restart the plugin.
   */
  status: "ok" | "degraded" | "failing";
  message?: string;
  /**
   * Free-form numeric gauges — peers, open sessions, dropped pushes, queue depth. Surfaced in the UI and useful in a bug report; nothing branches on them.
   */
  counters?: {
    [k: string]: number;
  };
}
/**
 * Fields every step kind carries, regardless of type.
 */
export interface ConfigFlowStepBase {
  type:
    | "form"
    | "oauth"
    | "qr-scan"
    | "progress"
    | "confirm"
    | "pick-device"
    | "manual-action"
    | "error"
    | "complete";
  description?: string;
  /**
   * What the core should pass as `step` when the user advances. Absent on terminal steps (`complete`, and `error` without a retry).
   */
  next?: string;
}
/**
 * A single attribute reading. `value` may be any JSON type — bool, number, string, or a structured object for compound attributes like colour.
 */
export interface StatePayload {
  value: unknown;
  /**
   * Free-form but conventional: `celsius`, `percent`, `watts`, `lux`, `ppm`, `seconds`. Absent means dimensionless.
   */
  unit?: string;
  origin?: StateOrigin;
  /**
   * 0..1, for values a plugin inferred rather than measured. Absent means certain.
   */
  confidence?: number;
}
/**
 * Something happened that has no persistent value — a button was pressed, motion was detected, an alarm self-tested.
 *
 * Unlike state, events must not be coalesced: two button presses are two facts, and dropping one loses information that cannot be recovered by reading the device.
 */
export interface EventPayload {
  /**
   * Free-form. A multi-press carries its count, a motion event may carry a zone.
   */
  data?: {};
  feature?: FeatureKey;
}
/**
 * A device appeared, went away, or was removed from the transport by something other than us.
 *
 * `removed` is distinct from a `decommission` we asked for: it means the device left on its own or a peer controller evicted it, and the core must reconcile its registry rather than assume its own view is current.
 */
export interface LifecyclePayload {
  deviceRef: DeviceRef;
  /**
   * Free-form but conventional: `session_active`, `session_timeout`, `removed_by_peer`, `unreachable`, `fabric_removed`.
   */
  reason?: string;
  device?: DiscoveredDevice;
}
/**
 * Progress of an in-flight `commission` call or config flow.
 *
 * Commissioning takes tens of seconds and passes through stages the user can act on — "press the button", "joining network". Without this the UI shows an indeterminate spinner and the user cannot tell a slow pairing from a hung one.
 */
export interface CommissioningPayload {
  /**
   * Transport-specific. Matter's sequence runs roughly: `discovered`, `pase_established`, `attestation`, `credentials`, `network_join`, `operational`, `interviewed`.
   */
  stage: string;
  message?: string;
  /**
   * 0..1. Absent when the plugin cannot estimate, which is honest and better than a fabricated number.
   */
  progress?: number;
  deviceRef?: DeviceRef;
}
/**
 * The plugin's own view of its condition. Used both as the result of `getHealth` and as the payload of `plugin.health.<component>` pushes — one definition, two references, so a poll and a push can never disagree about shape.
 */
export interface HealthPayload {
  /**
   * `ok` — operating normally.
   * `degraded` — still serving, but something is wrong: upstream is flaky, pushes are being dropped, a subset of devices is unreachable.
   * `failing` — not serving. The core may restart the plugin.
   */
  status: "ok" | "degraded" | "failing";
  message?: string;
  /**
   * Free-form numeric gauges — peers, open sessions, dropped pushes, queue depth. Surfaced in the UI and useful in a bug report; nothing branches on them.
   */
  counters?: {
    [k: string]: number;
  };
}
/**
 * A structured log entry, forwarded so the core can show plugin logs in its own UI without tailing files.
 *
 * The level is in the topic rather than the payload so a core can subscribe to `plugin.log.error` alone and not pay for debug traffic it would discard.
 */
export interface LogPayload {
  message: string;
  fields?: {};
  logger?: string;
}
/**
 * Periodic numeric telemetry. Deliberately thin — this is not a metrics system, it is enough to render a plugin's detail page and to make a bug report useful.
 */
export interface MetricsPayload {
  metrics: {
    [k: string]: number;
  };
  /**
   * How often this is emitted, so a consumer can tell a rate from a gauge.
   */
  intervalSeconds?: number;
}
export interface HelloParams {
  protocol: ProtocolVersion;
  plugin: PluginName;
  version: Semver;
  capabilities?: CapabilityList;
}
export interface HelloResult {
  protocol: ProtocolVersion;
  core: Semver;
  /**
   * New on every connection, including reconnects. Useful for correlating both sides' logs across a drop.
   */
  session: string;
  features?: CoreFeatureList;
}
export interface PingParams {}
export interface PingResult {
  pong: true;
}
export interface ShutdownParams {
  deadline: IsoTime;
  /**
   * Why the core is stopping the plugin: `restart`, `upgrade`, `disable`, `core_shutdown`. Advisory; a plugin might use it to decide whether to persist state.
   */
  reason?: string;
}
export interface ShutdownResult {}
export interface GetHealthParams {}
/**
 * The plugin's own view of its condition. Used both as the result of `getHealth` and as the payload of `plugin.health.<component>` pushes — one definition, two references, so a poll and a push can never disagree about shape.
 */
export interface GetHealthResult {
  /**
   * `ok` — operating normally.
   * `degraded` — still serving, but something is wrong: upstream is flaky, pushes are being dropped, a subset of devices is unreachable.
   * `failing` — not serving. The core may restart the plugin.
   */
  status: "ok" | "degraded" | "failing";
  message?: string;
  /**
   * Free-form numeric gauges — peers, open sessions, dropped pushes, queue depth. Surfaced in the UI and useful in a bug report; nothing branches on them.
   */
  counters?: {
    [k: string]: number;
  };
}
export interface DiscoverParams {
  /**
   * Ask the transport to re-interview devices rather than answer from cache. Slower, and may wake battery-powered devices.
   */
  refresh?: boolean;
}
export interface CommissionParams {
  sessionId?: ConfigFlowSessionId;
  /**
   * Setup code or QR payload, e.g. a Matter `MT:...` string. Absent for transports where pairing is a permit-join mode rather than a per-device operation.
   */
  payload?: string;
  /**
   * One dot-separated component of a topic name.
   *
   * Topics are dot-separated (spec §8.1) and every well-known topic shape assumes each interpolated value occupies exactly one segment. A value containing a dot would silently shift every following segment and break `*` matching — `state.*.*.*` would stop matching a device whose ref contains a dot, with no error anywhere. So no segment may contain `.`, whitespace, or the wildcard characters.
   *
   * Identifiers that are naturally dotted must substitute another separator when crossing the wire. Keystone's own convention for vendor-prefixed feature keys is `aqara.vibration` in Go; on the wire that becomes `aqara:vibration`.
   */
  target?: string;
  /**
   * For transports that must hand the device a network to join — Matter Wi-Fi and Thread commissioning. Kept as an open object rather than typed fields because each transport wants different keys, and this is the one place where a wrong guess would force a v2.
   */
  network?: {};
  extra?: {
    [k: string]: string;
  };
}
export interface CommissionResult {
  deviceRef: DeviceRef;
  device?: DiscoveredDevice;
}
export interface DecommissionParams {
  deviceRef: DeviceRef;
  /**
   * Drop local state even if the device cannot be reached to be told. Leaves the device believing it is still paired, so the UI should say so.
   */
  force?: boolean;
}
export interface DecommissionResult {}
/**
 * Addresses one attribute: which device, which feature, which key.
 */
export interface ReadStateParams {
  deviceRef: DeviceRef;
  feature: FeatureKey;
  key: StateKey;
}
/**
 * A single attribute reading. `value` may be any JSON type — bool, number, string, or a structured object for compound attributes like colour.
 */
export interface ReadStateResult {
  value: unknown;
  /**
   * Free-form but conventional: `celsius`, `percent`, `watts`, `lux`, `ppm`, `seconds`. Absent means dimensionless.
   */
  unit?: string;
  origin?: StateOrigin;
  /**
   * 0..1, for values a plugin inferred rather than measured. Absent means certain.
   */
  confidence?: number;
}
export interface WriteStateParams {
  deviceRef: DeviceRef;
  feature: FeatureKey;
  key: StateKey;
  /**
   * Any JSON type; the feature decides.
   */
  value: {
    [k: string]: unknown;
  };
}
export interface WriteStateResult {}
export interface InvokeActionParams {
  deviceRef: DeviceRef;
  feature: FeatureKey;
  action: ActionKey;
  /**
   * Shape is action-specific — `move` carries pan/tilt/zoom axes, `set` carries the value.
   */
  params?: {};
}
export interface InvokeActionResult {
  /**
   * Action-specific return value. Absent for actions that only have an effect.
   */
  data?: {
    [k: string]: unknown;
  };
}
export interface SubscribeParams {
  topics: TopicFilter[];
}
export interface SubscribeResult {
  subscribed: true;
}
export interface UnsubscribeParams {}
export interface UnsubscribeResult {
  unsubscribed: true;
}
export interface ResumeParams {
  /**
   * 0 means "send everything you still have".
   */
  since_seq: number;
}
export interface ResumeResult {
  resumed: true;
  /**
   * How many buffered pushes follow before live traffic resumes. 0 is normal — it means the caller was already current.
   */
  sending: number;
  /**
   * The plugin's current counter. Present so the caller can detect a restart and clamp.
   */
  seq?: number;
  /**
   * True when `since_seq` fell off the back of the ring buffer.
   */
  gap?: boolean;
}
export interface ConfigFlowParams {
  /**
   * `init` starts a new flow. Otherwise the `next` (or `retryStep`) of the previous step.
   */
  step: string;
  sessionId?: ConfigFlowSessionId;
  data?: {};
}
export interface GetConfigParams {}
export interface GetConfigResult {
  config: {};
  /**
   * Lets the core render a settings form without reading plugin.yaml. Optional — the manifest remains the authoritative copy.
   */
  schema?: {};
}
export interface SetConfigParams {
  config: {};
}
export interface SetConfigResult {
  /**
   * The config was accepted and stored but will not take effect until the plugin restarts.
   */
  restartRequired?: boolean;
}

/**
 * Every v1 method, with its parameter and result types.
 *
 * Generated from methods/index.json. Lets a typed `call()` resolve both
 * halves from the method name alone:
 *
 * ```ts
 * declare function call<M extends MethodName>(
 *   method: M,
 *   params: MethodMap[M]["params"],
 * ): Promise<MethodMap[M]["result"]>;
 * ```
 */
export interface MethodMap {
  hello: { params: HelloParams; result: HelloResult };
  ping: { params: PingParams; result: PingResult };
  shutdown: { params: ShutdownParams; result: ShutdownResult };
  getHealth: { params: GetHealthParams; result: GetHealthResult };
  discover: { params: DiscoverParams; result: DiscoverResult };
  commission: { params: CommissionParams; result: CommissionResult };
  decommission: { params: DecommissionParams; result: DecommissionResult };
  readState: { params: ReadStateParams; result: ReadStateResult };
  writeState: { params: WriteStateParams; result: WriteStateResult };
  invokeAction: { params: InvokeActionParams; result: InvokeActionResult };
  subscribe: { params: SubscribeParams; result: SubscribeResult };
  unsubscribe: { params: UnsubscribeParams; result: UnsubscribeResult };
  resume: { params: ResumeParams; result: ResumeResult };
  configFlow: { params: ConfigFlowParams; result: ConfigFlowResult };
  getConfig: { params: GetConfigParams; result: GetConfigResult };
  setConfig: { params: SetConfigParams; result: SetConfigResult };
}

/** Union of the v1 method names. */
export type MethodName = keyof MethodMap;

/** Which side initiates each method in v1. The transport itself is symmetric. */
export const METHOD_DIRECTION = {
  hello: "plugin->core",
  ping: "core->plugin",
  shutdown: "core->plugin",
  getHealth: "core->plugin",
  discover: "core->plugin",
  commission: "core->plugin",
  decommission: "core->plugin",
  readState: "core->plugin",
  writeState: "core->plugin",
  invokeAction: "core->plugin",
  subscribe: "core->plugin",
  unsubscribe: "core->plugin",
  resume: "core->plugin",
  configFlow: "core->plugin",
  getConfig: "core->plugin",
  setConfig: "core->plugin",
} as const satisfies Record<MethodName, "core->plugin" | "plugin->core">;
