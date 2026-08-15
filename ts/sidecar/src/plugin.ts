/**
 * The plugin-side role wrapper.
 */
import type { Server, Socket } from "node:net";

import { Peer, type Logger, type PeerOptions } from "./peer.js";
import { Mux } from "./mux.js";
import { Publisher } from "./publisher.js";
import { Method, helloHandler, pingHandler, subscriptionHandlers } from "./handshake.js";
import { sendHello } from "./handshake.js";
import { listenUnix, socketPathFromEnv } from "./transport.js";
import { PROTOCOL_V1 } from "./wire.js";
import { DEFAULT_RING_CAPACITY } from "./ring.js";

export interface PluginOptions {
  /** Identifies the plugin in the handshake. Should match metadata.name in
   * its plugin.yaml. */
  name: string;
  version?: string;
  /** Advertised in hello. Must be a subset of what the manifest declares. */
  capabilities?: string[];
  /** Defaults to $KEYSTONE_PLUGIN_SOCKET. */
  socketPath?: string;
  /** Answers method calls. hello, ping, subscribe, unsubscribe and resume are
   * installed on top, so a plugin only writes the methods that are actually
   * about its transport. Registering your own takes precedence. */
  handler?: Mux;
  /** Runs once the handshake completes. This is where a plugin starts
   * publishing. */
  onConnect?: (peer: Peer) => void | Promise<void>;
  onDisconnect?: () => void;
  ringCapacity?: number;
  logger?: Logger;
  signal?: AbortSignal;
}

/** A running plugin. */
export interface RunningPlugin {
  /** The shared publisher. It outlives every connection — which is what makes
   * resume able to replay anything. */
  publisher: Publisher;
  socketPath: string;
  /** Resolves when the plugin stops. */
  done: Promise<void>;
  stop(): Promise<void>;
}

/**
 * Serves the protocol until stopped.
 *
 * The plugin listens and the core dials; the plugin sends hello once a
 * connection is accepted. See OPEN_QUESTIONS.md §7 for why that direction.
 *
 *     const plugin = await runPlugin({
 *       name: "matter",
 *       version: "1.2.0",
 *       capabilities: ["realtime.state-stream"],
 *       handler: new Mux().handle("discover", handleDiscover),
 *       onConnect: (peer) => peer.publish("lifecycle.matter:3.online", { deviceRef: "matter:3" }),
 *     });
 */
export async function runPlugin(opts: PluginOptions): Promise<RunningPlugin> {
  if (!opts.name) {
    throw new Error("sidecar: PluginOptions.name is required — it identifies the plugin in the handshake");
  }

  const socketPath = opts.socketPath ?? socketPathFromEnv();
  const version = opts.version ?? "0.0.0";

  // One publisher for the whole run, not one per connection. A publisher
  // scoped to a connection would have an empty ring buffer at exactly the
  // moment the core reconnects and calls resume, making replay structurally
  // impossible.
  const publisher = new Publisher(undefined, {
    ringCapacity: opts.ringCapacity ?? DEFAULT_RING_CAPACITY,
  });

  const server: Server = await listenUnix(socketPath);

  let stopResolve!: () => void;
  const done = new Promise<void>((resolve) => {
    stopResolve = resolve;
  });

  const peers = new Set<Peer>();
  let active: Peer | undefined;

  server.on("connection", (socket: Socket) => {
    // One connection at a time, matching the Go side, which blocks in accept
    // until the current connection ends.
    //
    // This is not arbitrary: the publisher is shared across connections so its
    // ring buffer survives a reconnect, and it can only be bound to one writer.
    // Accepting a second core concurrently would silently rebind it, and the
    // first core's pushes would start going to the second. v1 has exactly one
    // core per plugin, so refusing is both correct and the honest signal.
    if (active && !active.isClosed) {
      opts.logger?.warn("sidecar: refusing a second connection", { socketPath });
      socket.destroy();
      return;
    }
    void serveConnection(socket, opts, publisher, version, peers, (p) => {
      active = p;
    });
  });

  const stop = async (): Promise<void> => {
    server.close();
    for (const p of peers) await p.close();
    peers.clear();
    stopResolve();
  };

  opts.signal?.addEventListener("abort", () => void stop(), { once: true });
  server.once("close", stopResolve);

  return { publisher, socketPath, done, stop };
}

async function serveConnection(
  socket: Socket,
  opts: PluginOptions,
  publisher: Publisher,
  version: string,
  peers: Set<Peer>,
  setActive: (peer: Peer) => void,
): Promise<void> {
  const mux = new Mux();

  // The plugin's own handlers first, then the protocol defaults only where the
  // plugin has not claimed the name.
  const own = opts.handler;
  if (own) {
    for (const method of own.methods) {
      mux.handle(method, (req) => own.dispatch(req));
    }
  }
  if (!mux.has(Method.Ping)) mux.handle(Method.Ping, pingHandler);
  const subs = subscriptionHandlers(publisher);
  for (const method of subs.methods) {
    if (!mux.has(method)) mux.handle(method, (req) => subs.dispatch(req));
  }
  if (!mux.has(Method.Shutdown)) {
    mux.handle(Method.Shutdown, (req) => {
      // Close after the response goes out, not before — the core is waiting
      // for an acknowledgement, and cutting the socket first would look like a
      // crash during shutdown.
      setTimeout(() => void req.peer.close(), 50).unref?.();
      return {};
    });
  }

  // Passing the shared publisher binds it to this connection while keeping
  // the seq counter and the ring buffer it accumulated across previous ones.
  const peerOpts: PeerOptions = { handler: mux, publisher };
  if (opts.logger) peerOpts.logger = opts.logger;
  const peer = new Peer(socket, peerOpts);
  peers.add(peer);
  setActive(peer);

  const serving = peer.serve();

  try {
    await sendHello(peer, {
      protocol: PROTOCOL_V1,
      plugin: opts.name,
      version,
      ...(opts.capabilities ? { capabilities: opts.capabilities } : {}),
    });
    await opts.onConnect?.(peer);
  } catch (err) {
    opts.logger?.warn("sidecar: handshake failed", { err: String(err) });
    await peer.close();
  }

  await serving;

  peers.delete(peer);
  opts.onDisconnect?.();
}
