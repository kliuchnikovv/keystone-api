// Compiles every canonical schema, enforces repository conventions on them,
// and runs the shared fixture corpus through the envelope schema.
//
// This is the gate that keeps the contract honest. Everything else in the
// repository is derived from these files, so a mistake here propagates into
// both language packages and into every plugin written against them.
import { relative } from "node:path";
import Ajv2020 from "ajv/dist/2020.js";
import addFormats from "ajv-formats";

import {
  canonicalDir,
  idFor,
  listJson,
  loadCanonical,
  readJson,
  repoRoot,
} from "./schemas.mjs";

const problems = [];
const fail = (where, msg) => problems.push(`${where}: ${msg}`);

// ---------------------------------------------------------------- compile

const schemas = loadCanonical();

const ajv = new Ajv2020({
  strict: true,
  allErrors: true,
  // The index files reference $defs that only the runtime resolves; and we add
  // every schema up front, so ajv must not try to fetch anything.
  loadSchema: async (uri) => {
    throw new Error(`unexpected remote $ref: ${uri}`);
  },
});
addFormats(ajv);

for (const s of schemas) {
  if (s.index) continue;
  try {
    ajv.addSchema(s.doc, s.id);
  } catch (err) {
    fail(s.rel, `could not be registered: ${err.message}`);
  }
}

for (const s of schemas) {
  if (s.index) continue;
  try {
    ajv.getSchema(s.id);
  } catch (err) {
    fail(s.rel, `does not compile: ${err.message}`);
  }
}

// ------------------------------------------------------------- conventions

for (const s of schemas) {
  const { doc, rel } = s;

  if (doc.$id !== s.id) {
    fail(rel, `$id is ${JSON.stringify(doc.$id)}, expected ${JSON.stringify(s.id)}`);
  }
  if (doc.$schema !== "https://json-schema.org/draft/2020-12/schema") {
    fail(rel, "must declare draft 2020-12 in $schema");
  }
  if (!doc.title) fail(rel, "missing title");
  if (!doc.description) fail(rel, "missing description");

  // title/description become JSDoc in the generated TypeScript and are copied
  // into the Go doc comments, so a bare $defs entry is a documentation hole in
  // two languages at once.
  //
  // Exception: `params` and `result` in a method file. The file's own
  // description documents the method as a whole, and repeating it on the two
  // halves produces noise rather than documentation.
  const methodFile = rel.startsWith("methods/");
  for (const [name, def] of Object.entries(doc.$defs ?? {})) {
    if (methodFile && (name === "params" || name === "result")) continue;
    if (!def.description && !def.$ref) {
      fail(rel, `$defs/${name} has no description`);
    }
  }
}

// The index files are data. Check their shape by hand rather than pretending
// they are schemas.
{
  const methods = readJson(`${canonicalDir}/methods/index.json`).methods;
  const known = new Set(
    listJson(`${canonicalDir}/methods`)
      .map((p) => relative(`${canonicalDir}/methods`, p))
      .filter((n) => n !== "index.json")
      .map((n) => n.replace(/\.json$/, "")),
  );

  for (const name of Object.keys(methods)) {
    if (!known.has(name)) fail("methods/index.json", `${name} has no methods/${name}.json`);
    known.delete(name);
    const m = methods[name];
    if (!m.params) fail("methods/index.json", `${name} has no params`);
    if (!m.result) fail("methods/index.json", `${name} has no result`);
    if (!["core->plugin", "plugin->core"].includes(m.direction)) {
      fail("methods/index.json", `${name} has an invalid direction ${JSON.stringify(m.direction)}`);
    }
  }
  for (const orphan of known) {
    fail("methods/index.json", `methods/${orphan}.json is not listed`);
  }

  const topics = readJson(`${canonicalDir}/topics/index.json`).topics;
  for (const t of topics) {
    if (!t.pattern) fail("topics/index.json", "an entry has no pattern");
    if (!t.payload?.$ref) fail("topics/index.json", `${t.pattern} has no payload $ref`);
    if (t.pattern.includes("**")) {
      // A `**` here would make the topic->payload mapping ambiguous: two
      // patterns could both claim a topic and there would be no principled way
      // to pick a payload schema.
      fail("topics/index.json", `${t.pattern} may not use ** — payload mapping must be unambiguous`);
    }
  }
}

// ------------------------------------------------------------------ corpus

const envelope = ajv.getSchema(idFor(`${canonicalDir}/envelope.json`));
let checked = 0;

if (!envelope) {
  fail("envelope.json", "did not compile; skipping the corpus");
} else {
  for (const path of listJson(`${canonicalDir}/testdata/frames/valid`)) {
    checked++;
    const doc = readJson(path);
    if (!envelope(doc)) {
      fail(
        relative(repoRoot, path),
        `should be valid but is not:\n    ${ajv.errorsText(envelope.errors, { separator: "\n    " })}`,
      );
    }
  }

  for (const path of listJson(`${canonicalDir}/testdata/frames/invalid`)) {
    checked++;
    const doc = readJson(path);
    if (envelope(doc)) {
      fail(relative(repoRoot, path), "should be rejected but validates");
    }
  }
}

// ------------------------------------------------------------------ report

if (problems.length) {
  console.error(`\n${problems.length} problem(s):\n`);
  for (const p of problems) console.error(`  ${p}`);
  console.error("");
  process.exit(1);
}

console.log(
  `schemas ok — ${schemas.length} schema(s) compiled, ${checked} corpus fixture(s) checked`,
);
