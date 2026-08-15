// Shared helpers for the schema tooling: locating the canonical schemas,
// loading them, and building a compiler that resolves the relative $refs they
// use between each other.
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative, sep } from "node:path";
import { fileURLToPath } from "node:url";

export const repoRoot = fileURLToPath(new URL("..", import.meta.url));
export const canonicalDir = join(repoRoot, "schemas", "sidecar", "v1");

/** Absolute paths of every .json file under dir, recursively, sorted. */
export function listJson(dir, { skip = [] } = {}) {
  const out = [];
  const walk = (d) => {
    for (const name of readdirSync(d).sort()) {
      if (skip.includes(name)) continue;
      const p = join(d, name);
      if (statSync(p).isDirectory()) walk(p);
      else if (name.endsWith(".json")) out.push(p);
    }
  };
  walk(dir);
  return out;
}

export function readJson(path) {
  try {
    return JSON.parse(readFileSync(path, "utf8"));
  } catch (err) {
    throw new Error(`${relative(repoRoot, path)}: ${err.message}`);
  }
}

/**
 * Every schema's $id follows its path under the canonical directory, so the
 * relative $refs the schemas use between themselves ("common.json",
 * "../objects/device.json") resolve without any extra mapping.
 */
export function idFor(absPath) {
  const rel = relative(canonicalDir, absPath).split(sep).join("/");
  return `https://keystone.io/schemas/sidecar/v1/${rel}`;
}

/**
 * The two index files are data, not validation schemas: they carry `methods`
 * and `topics` keys that ajv would treat as unknown keywords, and their $refs
 * point at $defs meant for the runtime rather than for validating the index
 * itself. They are checked separately by lint-schemas.
 */
export const INDEX_FILES = ["methods/index.json", "topics/index.json"];

export function isIndex(absPath) {
  const rel = relative(canonicalDir, absPath).split(sep).join("/");
  return INDEX_FILES.includes(rel);
}

/** All canonical schemas as { path, rel, id, doc }. */
export function loadCanonical() {
  return listJson(canonicalDir, { skip: ["testdata"] }).map((path) => ({
    path,
    rel: relative(canonicalDir, path).split(sep).join("/"),
    id: idFor(path),
    doc: readJson(path),
    index: isIndex(path),
  }));
}
