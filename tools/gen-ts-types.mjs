// Generates ts/sidecar/src/types.gen.ts from the canonical JSON Schemas.
//
// TypeScript is generated and Go is not, and that asymmetry is deliberate:
// JSON Schema maps onto TypeScript honestly (oneOf becomes a discriminated
// union, description becomes JSDoc), while Go generators produce the wrong
// types for this protocol — see CONTRIBUTING.md.
//
// The output is committed. `make verify-gen` re-runs this and fails on a diff.
//
// ---------------------------------------------------------------------------
// A note on naming, because the obvious implementation produces garbage.
//
// json-schema-to-typescript hoists a named type for every subschema carrying a
// `title`, and disambiguates collisions with a numeric suffix. Our schemas use
// `title` as human documentation on ordinary properties, so compiling them
// as-is yields exports called `Params1`, `Timestamp2` and `Error1` — names
// that mean nothing and would be part of the package's public API forever.
//
// So we take naming away from the generator. Every `title` is stripped from a
// working copy of the schemas, and re-applied only on the exact subschemas we
// intend to export, using the names in EXPORTS below. Prose lives in
// `description`, which is what becomes JSDoc regardless, so nothing is lost.
import { cpSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";

import { compile } from "json-schema-to-typescript";

import { canonicalDir, listJson, readJson, repoRoot } from "./schemas.mjs";

const OUT = join(repoRoot, "ts", "sidecar", "src", "types.gen.ts");

const methodIndex = readJson(join(canonicalDir, "methods", "index.json")).methods;
const cap = (s) => s[0].toUpperCase() + s.slice(1);

/** TypeScript name -> $ref into the canonical schemas, relative to canonicalDir. */
const EXPORTS = {
  Frame: "envelope.json",
  RequestFrame: "request.json",
  ResponseFrame: "response.json",
  PushFrame: "push.json",
  ErrorBody: "error.json",

  DeviceRef: "common.json#/$defs/deviceRef",
  FeatureKey: "common.json#/$defs/featureKey",
  StateKey: "common.json#/$defs/stateKey",
  ActionKey: "common.json#/$defs/actionKey",
  EventKey: "common.json#/$defs/eventKey",
  DeviceType: "common.json#/$defs/deviceType",
  Topic: "common.json#/$defs/topic",
  TopicFilter: "common.json#/$defs/topicFilter",
  Seq: "common.json#/$defs/seq",
  IsoTime: "common.json#/$defs/isoTime",
  Semver: "common.json#/$defs/semver",
  ProtocolVersion: "common.json#/$defs/protocolVersion",
  PluginName: "common.json#/$defs/pluginName",
  StateOrigin: "common.json#/$defs/origin",
  ErrorCode: "common.json#/$defs/errorCode",
  RequestId: "common.json#/$defs/requestId",

  Feature: "objects/device.json#/$defs/feature",
  DiscoveredDevice: "objects/device.json#/$defs/discoveredDevice",
  CommissionableDevice: "objects/device.json#/$defs/commissionableDevice",
  StateValue: "objects/device.json#/$defs/stateValue",
  AttrRef: "objects/device.json#/$defs/attrRef",

  HealthStatus: "objects/health.json",
  CapabilityId: "objects/capability.json#/$defs/capabilityId",
  CoreFeature: "objects/capability.json#/$defs/coreFeature",
  ConfigFlowStep: "objects/configFlowStep.json#/$defs/step",
  ConfigFlowStepBase: "objects/configFlowStep.json#/$defs/base",
  ConfigFlowSessionId: "objects/configFlowStep.json#/$defs/sessionId",

  StatePayload: "topics/state.json",
  EventPayload: "topics/event.json",
  LifecyclePayload: "topics/lifecycle.json",
  CommissioningPayload: "topics/commissioning.json",
  HealthPayload: "topics/plugin.health.json",
  LogPayload: "topics/plugin.log.json",
  MetricsPayload: "topics/plugin.metrics.json",
};

for (const name of Object.keys(methodIndex)) {
  EXPORTS[`${cap(name)}Params`] = `methods/${name}.json#/$defs/params`;
  EXPORTS[`${cap(name)}Result`] = `methods/${name}.json#/$defs/result`;
}

// ------------------------------------------------- working copy of the schemas

const work = mkdtempSync(join(tmpdir(), "keystone-schemas-"));
try {
  cpSync(canonicalDir, work, {
    recursive: true,
    filter: (src) => !src.includes("/testdata"),
  });

  // Strip every `title`, and every annotation sitting beside a `$ref`.
  //
  // Draft 2020-12 allows `$ref` to carry siblings, and the schemas use that to
  // say why a shared definition is being referenced *here* — useful prose in
  // the canonical files and in the Go docs. But the generator treats a $ref
  // with siblings as a distinct schema and emits a numbered clone of the
  // target, so leaving them in produces exports called `IsoTime2` and
  // `DeviceRef1`. The reference itself is what matters in TypeScript; the
  // note is lost from the JSDoc and kept where it was written.
  const normalise = (node) => {
    if (Array.isArray(node)) {
      node.forEach(normalise);
      return;
    }
    if (node === null || typeof node !== "object") return;
    delete node.title;
    if (typeof node.$ref === "string") {
      for (const key of Object.keys(node)) {
        if (key !== "$ref") delete node[key];
      }
      return;
    }
    for (const value of Object.values(node)) normalise(value);
  };

  const docs = new Map();
  for (const path of listJson(work)) {
    const doc = readJson(path);
    normalise(doc);
    docs.set(path, doc);
  }

  // Re-apply titles only where we want an exported name.
  for (const [name, ref] of Object.entries(EXPORTS)) {
    const [file, pointer] = ref.split("#");
    const path = join(work, file);
    const doc = docs.get(path);
    if (!doc) throw new Error(`${name}: no such schema ${file}`);

    let target = doc;
    for (const part of (pointer ?? "").split("/").filter(Boolean)) {
      target = target?.[part.replace(/~1/g, "/").replace(/~0/g, "~")];
      if (!target) throw new Error(`${name}: ${ref} does not resolve`);
    }
    target.title = name;
  }

  for (const [path, doc] of docs) {
    writeFileSync(path, JSON.stringify(doc, null, 2));
  }

  // A synthetic root whose only job is to pull every exported type into one
  // compilation unit. The wrapper interface itself is stripped afterwards.
  const root = {
    $schema: "https://json-schema.org/draft/2020-12/schema",
    $id: "https://keystone.io/schemas/sidecar/v1/__root.json",
    title: "__Root",
    type: "object",
    required: Object.keys(EXPORTS),
    additionalProperties: false,
    properties: Object.fromEntries(
      Object.entries(EXPORTS).map(([name, ref]) => [name, { $ref: ref }]),
    ),
  };

  let ts = await compile(root, "__Root", {
    cwd: `${work}/`,
    bannerComment: "",
    additionalProperties: false,
    declareExternallyReferenced: true,
    format: true,
    style: { printWidth: 88 },
    // The protocol's opaque fields — params, result, payload, a state value —
    // have no schema at this layer by design: a peer routes a frame without
    // knowing what is inside it. `unknown` forces the consumer to narrow.
    // `any` would let a typo through silently.
    unknownAny: true,
  });

  ts = ts.replace(/export interface __Root \{[\s\S]*?\n\}\n/, "");

  // Method name -> params/result, so a typed call() can resolve both from the
  // method string alone. Generated rather than hand-written because the source
  // of truth is methods/index.json and this must never drift from it.
  const entries = Object.keys(methodIndex)
    .map(
      (name) =>
        `  ${name}: { params: ${cap(name)}Params; result: ${cap(name)}Result };`,
    )
    .join("\n");

  const methodMap = `
/**
 * Every v1 method, with its parameter and result types.
 *
 * Generated from methods/index.json. Lets a typed \`call()\` resolve both
 * halves from the method name alone:
 *
 * \`\`\`ts
 * declare function call<M extends MethodName>(
 *   method: M,
 *   params: MethodMap[M]["params"],
 * ): Promise<MethodMap[M]["result"]>;
 * \`\`\`
 */
export interface MethodMap {
${entries}
}

/** Union of the v1 method names. */
export type MethodName = keyof MethodMap;

/** Which side initiates each method in v1. The transport itself is symmetric. */
export const METHOD_DIRECTION = {
${Object.entries(methodIndex)
  .map(([name, m]) => `  ${name}: ${JSON.stringify(m.direction)},`)
  .join("\n")}
} as const satisfies Record<MethodName, "core->plugin" | "plugin->core">;
`;

  const banner = `/**
 * Generated from schemas/sidecar/v1 — DO NOT EDIT.
 *
 * Regenerate with \`make gen\`. \`make verify-gen\` re-runs the generator and
 * fails on a diff, so a hand edit here is caught in CI rather than shipped.
 *
 * The Go package's types are hand-written instead, and held to the same
 * schemas by a corpus round-trip and a reflective struct diff. CONTRIBUTING.md
 * explains why the two languages are treated differently.
 */

`;

  mkdirSync(dirname(OUT), { recursive: true });
  writeFileSync(OUT, banner + ts.trimStart() + methodMap);

  const written = readFileSync(OUT, "utf8");
  const count = written.match(/^export (interface|type|const) /gm)?.length ?? 0;
  console.log(`types.gen.ts written — ${count} exported type(s)`);
} finally {
  rmSync(work, { recursive: true, force: true });
}
