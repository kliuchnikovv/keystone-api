#!/usr/bin/env node
/**
 * Cross-language conformance harness.
 *
 * Runs the Go peer and the TypeScript peer against each other over a real unix
 * socket, in BOTH directions:
 *
 *   1. TS plugin  <- Go core
 *   2. Go plugin  <- TS core
 *
 * The second direction is the one that matters. A peer that only ever answers
 * would pass the first with half the implementation missing, so running only
 * the production arrangement would let a broken symmetry claim through — and
 * symmetry is the property this repository advertises.
 *
 * Usage:  node ts/sidecar/test/conformance.mjs
 *
 * Requires the Go binary at bin/conformance and the TS package built to
 * ts/sidecar/dist. The Makefile target does both.
 */
import { spawn } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import { existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const repoRoot = fileURLToPath(new URL("../../../", import.meta.url));
const goBinary = join(repoRoot, "bin", "conformance");
const dist = join(repoRoot, "ts", "sidecar", "dist", "index.js");

if (!existsSync(goBinary)) {
  fail(`missing ${rel(goBinary)} — run \`make conformance\`, which builds it`);
}
if (!existsSync(dist)) {
  fail(`missing ${rel(dist)} — run \`make conformance\`, which builds it`);
}

const {
  Mux,
  runPlugin,
  connectCore,
  Code,
  ProtocolError,
  codeOf,
  isRetryable,
} = await import(dist);

// The devices the plugin role reports, matching the Go half exactly. A
// mismatch here is the point of the exercise, so they are asserted rather
// than shared.
const DISCOVERED = [
  { deviceRef: "matter:2", type: "light", name: "Kitchen" },
  { deviceRef: "matter:5", type: "motion_sensor", name: "Hall" },
];

let failures = 0;

await withSocket(async (socket) => {
  section("TS plugin <- Go core");
  const plugin = await startTsPlugin(socket);
  try {
    if (await runGo(["-role=core", `-socket=${socket}`])) {
      ok("the Go core drove the TypeScript plugin through the full transcript");
    }
  } finally {
    await plugin.stop();
  }
});

await withSocket(async (socket) => {
  section("Go plugin <- TS core");
  const go = spawnGo(["-role=plugin", `-socket=${socket}`]);
  try {
    await waitForSocket(socket);
    const before = failures;
    await runTsCore(socket);
    if (failures === before) {
      ok("the TypeScript core drove the Go plugin through the full transcript");
    }
  } finally {
    go.kill("SIGTERM");
    await once(go, "exit");
  }
});

if (failures > 0) {
  console.error(`\nconformance: ${failures} check(s) failed`);
  process.exit(1);
}
console.log("\nconformance: both directions ok");

// ---------------------------------------------------------------- TS plugin

async function startTsPlugin(socket) {
  const handler = new Mux()
    .handle("discover", () => DISCOVERED)
    .handle("readState", (req) => {
      const { deviceRef } = req.bind();
      if (deviceRef !== "matter:2") {
        throw new ProtocolError(Code.DeviceNotFound, "no such device", {
          details: { deviceRef },
        });
      }
      return { value: true, origin: "device_report" };
    })
    .handle("invokeAction", (req) => {
      // Answering, then pushing the resulting state change, is the shape every
      // real device operation takes.
      setTimeout(() => {
        try {
          req.peer.publish("state.matter:2.onoff.value", {
            value: false,
            origin: "device_report",
          });
        } catch {
          // The core may already have gone; not this test's concern.
        }
      }, 10);
      return {};
    })
    .handle("getHealth", () => ({
      status: "ok",
      message: "conformance harness",
      counters: { peers: 2 },
    }))
    .handle("errorOnPurpose", () => {
      throw new ProtocolError(Code.DeviceOffline, "device is offline", {
        details: { deviceRef: "matter:5" },
      });
    })
    // emitBurst exists so the transcript is deterministic. Publishing from
    // onConnect would race the core's subscribe, and deny-by-default would
    // correctly drop the pushes — the protocol working as designed, but a test
    // that fails intermittently for a reason unrelated to what it checks.
    .handle("emitBurst", (req) => {
      req.peer.publish("lifecycle.matter:2.online", { deviceRef: "matter:2" });
      // Deliberately unsubscribed. The core asked for state, lifecycle and
      // event, not plugin.log — so this must be dropped AND must not consume a
      // sequence number. Numbering it would leave the core with a permanent
      // gap for a topic it never wanted, and gap detection could no longer
      // tell "you missed something" from "you did not want that".
      req.peer.publish("plugin.log.debug", { message: "this topic was not subscribed to" });
      req.peer.publish("state.matter:2.onoff.value", { value: true, origin: "device_report" });
      return { emitted: 2 };
    });

  return runPlugin({
    name: "conformance",
    version: "1.0.0",
    capabilities: ["realtime.state-stream", "commission.setup-code"],
    socketPath: socket,
    handler,
  });
}

// ------------------------------------------------------------------ TS core

async function runTsCore(socket) {
  const pushes = [];
  const client = await connectCore(socket, {
    version: "0.5.2",
    onPush: (p) => pushes.push(p),
  });

  try {
    // --- handshake ---
    check(client.session?.peerName === "conformance", `peer name is ${client.session?.peerName}`);
    check(
      client.session?.capabilities.includes("realtime.state-stream"),
      `capabilities are ${JSON.stringify(client.session?.capabilities)}`,
    );

    // --- subscribe ---
    await client.subscribe("state.**", "lifecycle.**", "event.**");

    // --- discover ---
    const devices = await client.call("discover");
    check(devices.length === DISCOVERED.length, `discover returned ${devices.length} devices`);
    check(devices[0].deviceRef === "matter:2", `discover[0] is ${devices[0].deviceRef}`);

    // --- readState ---
    const state = await client.call("readState", {
      deviceRef: "matter:2",
      feature: "onoff",
      key: "value",
    });
    check(state.value === true && state.origin === "device_report", `readState -> ${JSON.stringify(state)}`);

    // --- typed errors ---
    const notFound = await client
      .call("readState", { deviceRef: "matter:999", feature: "onoff", key: "value" })
      .then(() => undefined, (e) => e);
    check(codeOf(notFound) === Code.DeviceNotFound, `missing device -> ${codeOf(notFound)}`);
    check(!isRetryable(notFound), "device.not_found came back retryable");

    const offline = await client.call("errorOnPurpose").then(() => undefined, (e) => e);
    check(codeOf(offline) === Code.DeviceOffline, `errorOnPurpose -> ${codeOf(offline)}`);
    // The retryable flag has to survive the crossing, or the core stops
    // retrying sleeping devices.
    check(isRetryable(offline), "device.offline did not come back retryable");
    check(
      offline?.details?.deviceRef === "matter:5",
      `error details lost across the wire: ${JSON.stringify(offline?.details)}`,
    );

    // --- unknown method ---
    // Must be answered, not ignored: a blocked caller cannot tell silence from
    // a dead plugin.
    const unknown = await client.call("noSuchMethodExists").then(() => undefined, (e) => e);
    check(
      codeOf(unknown) === Code.ProtocolUnsupported,
      `unknown method -> ${codeOf(unknown)} (a timeout here means it was ignored)`,
    );

    // --- pushes ---
    await client.call("emitBurst");
    await client.call("invokeAction", {
      deviceRef: "matter:2",
      feature: "onoff",
      action: "turn_off",
    });
    await waitFor(() => pushes.length >= 3, 5000, "three pushes");

    check(
      !pushes.some((p) => p.topic === "plugin.log.debug"),
      "received a push on a topic we never subscribed to",
    );

    // Sequence numbers start at 1 and increase by exactly 1 — with no hole
    // where the unsubscribed push was dropped.
    pushes.slice(0, 3).forEach((p, i) => {
      check(p.seq === i + 1, `push ${i} has seq ${p.seq}, want ${i + 1}`);
      // The timestamp format has to match byte for byte across languages, or
      // golden fixtures diverge for no semantic reason.
      check(
        /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/.test(p.ts),
        `push ${i} timestamp ${JSON.stringify(p.ts)} is not the wire format`,
      );
    });
    check(
      pushes[0].topic === "lifecycle.matter:2.online",
      `push 0 topic is ${pushes[0].topic}`,
    );

    // --- heartbeat ---
    const pong = await client.call("ping", {});
    check(pong.pong === true, `ping -> ${JSON.stringify(pong)}`);

    // --- getHealth ---
    const health = await client.call("getHealth");
    check(health.status === "ok", `getHealth -> ${health.status}`);

    // --- resume ---
    const before = client.lastSeq;
    const res = await client.resume();
    check(res.resumed === true, "resume did not report resumed");
    check(res.sending === 0, `resume wants to send ${res.sending} frames — we were current`);
    check(!res.gap, "resume reported a gap over history we hold in full");
    check(res.seq === before, `resume reports seq ${res.seq}, want ${before}`);

    // A replay of ground already covered must be discarded rather than
    // re-delivered, or a reconnect re-fires automations for events from before
    // the drop.
    const countBefore = pushes.length;
    await sleep(300);
    check(
      pushes.length === countBefore,
      `resume re-delivered ${pushes.length - countBefore} pushes we had already accepted`,
    );
  } finally {
    await client.close();
  }
}

// ----------------------------------------------------------------- plumbing

function spawnGo(args) {
  const child = spawn(goBinary, args, { stdio: ["ignore", "pipe", "pipe"] });
  child.stdout.on("data", (d) => process.stdout.write(`  go: ${d}`));
  child.stderr.on("data", (d) => process.stderr.write(`  go: ${d}`));
  return child;
}

async function runGo(args) {
  const child = spawnGo(args);
  const [code] = await once(child, "exit");
  if (code !== 0) {
    failures++;
    console.error(`  FAIL: the Go half exited ${code}`);
    return false;
  }
  return true;
}

function once(emitter, event) {
  return new Promise((resolve) => emitter.once(event, (...a) => resolve(a)));
}

async function withSocket(fn) {
  // Short path: unix socket names are capped around 104 bytes on darwin, and a
  // long temp path fails at bind with an error that looks nothing like a
  // length problem.
  const dir = await mkdtemp(join(tmpdir(), "ksconf"));
  try {
    await fn(join(dir, "p.sock"));
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
}

async function waitForSocket(path, timeoutMs = 5000) {
  await waitFor(() => existsSync(path), timeoutMs, `the plugin to create ${rel(path)}`);
  // The file exists a moment before the listener is ready to accept.
  await sleep(50);
}

async function waitFor(cond, timeoutMs, what) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (await cond()) return;
    await sleep(5);
  }
  throw new Error(`timed out waiting for ${what}`);
}

function sleep(ms) {
  return new Promise((r) => setTimeout(r, ms));
}

function section(name) {
  console.log(`\n${name}`);
}

function ok(msg) {
  console.log(`  ok: ${msg}`);
}

function check(condition, detail) {
  if (condition) return;
  failures++;
  console.error(`  FAIL: ${detail}`);
}

function rel(p) {
  return p.startsWith(repoRoot) ? p.slice(repoRoot.length) : p;
}

function fail(msg) {
  console.error(`conformance: ${msg}`);
  process.exit(1);
}
