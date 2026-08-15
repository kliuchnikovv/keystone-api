/**
 * keystone sidecar protocol v1 — the TypeScript half of the shared kernel.
 *
 * Wire types, NDJSON framing, and the symmetric peer runtime: handshake,
 * heartbeat, request multiplexing, topic matching, the push ring buffer and
 * resume/replay. Feature builders and config-flow ergonomics belong to the
 * SDKs built on top of this.
 *
 * Plugin side:
 *
 *     const plugin = await runPlugin({
 *       name: "matter",
 *       version: "1.2.0",
 *       capabilities: ["realtime.state-stream"],
 *       handler: new Mux().handle("discover", async () => devices),
 *       onConnect: (peer) => peer.publish("state.matter:3.onoff.value", { value: true }),
 *     });
 *
 * Core side:
 *
 *     const client = await connectCore(socketPath, {
 *       version: "0.5.2",
 *       onPush: (push) => console.log(push.topic, push.payload),
 *     });
 *     await client.subscribe("state.**", "lifecycle.**");
 */
export * from "./types.gen.js";

export {
  PROTOCOL_V1,
  FRAME_REQUEST,
  FRAME_RESPONSE,
  FRAME_PUSH,
  isRequest,
  isResponse,
  isPush,
  frameProblem,
  isValidTopic,
  describeFrame,
  wireTime,
} from "./wire.js";

export {
  Code,
  ProtocolError,
  NotConnectedError,
  PeerClosedError,
  defaultRetryable,
  isRetryable,
  codeOf,
  toErrorBody,
  errors,
  type ProtocolErrorInit,
} from "./errors.js";

export {
  DEFAULT_MAX_LINE_BYTES,
  LineTooLongError,
  NdjsonDecoder,
  FrameWriter,
  decodeStream,
  encodeFrame,
  jsonReplacer,
  type DecoderOptions,
} from "./codec.js";

export { TopicMatcher, matchTopic, validateFilter } from "./topic.js";
export { RingBuffer, DEFAULT_RING_CAPACITY, type SinceResult } from "./ring.js";
export { Publisher, type PublisherOptions } from "./publisher.js";

export {
  Peer,
  Mux,
  Request,
  DEFAULT_CALL_TIMEOUT_MS,
  type PeerOptions,
  type Push,
  type SessionInfo,
  type Logger,
} from "./peer.js";

export {
  Method,
  CoreFeature,
  DEFAULT_PING_INTERVAL_MS,
  DEFAULT_PING_TIMEOUT_MS,
  negotiateProtocol,
  sendHello,
  helloHandler,
  pingHandler,
  subscriptionHandlers,
  startHeartbeat,
  type HeartbeatOptions,
} from "./handshake.js";

export {
  SOCKET_ENV_VAR,
  socketPathFromEnv,
  dialUnix,
  listenUnix,
} from "./transport.js";

export { runPlugin, type PluginOptions, type RunningPlugin } from "./plugin.js";
export { connectCore, CoreClient, type CoreOptions } from "./client.js";
