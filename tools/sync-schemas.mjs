// Copies the canonical schemas into the two language-package mirrors.
//
// The mirrors exist because neither packaging system can reach outside its own
// directory: //go:embed refuses a path containing "..", and npm's `files` field
// does the same. Both packages must ship the schemas — a plugin that turns on
// runtime validation has to have them at hand — so the canonical directory is
// copied rather than referenced.
//
// The mirrors are committed, and `make verify-gen` fails if they drift. Never
// edit one by hand; edit schemas/sidecar/v1 and re-run this.
import { cpSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import { canonicalDir, repoRoot } from "./schemas.mjs";

const NOTICE = `Generated mirror — do not edit.

The canonical schemas live in schemas/sidecar/v1 at the repository root.
Regenerate this directory with \`make sync-schemas\`.

This copy exists because //go:embed and npm's \`files\` cannot reference a path
outside the package directory.
`;

const mirrors = [
  join(repoRoot, "sidecar", "schema", "v1"),
  join(repoRoot, "ts", "sidecar", "schemas", "v1"),
];

for (const dir of mirrors) {
  rmSync(dir, { recursive: true, force: true });
  mkdirSync(dir, { recursive: true });

  // testdata is the shared conformance corpus; both language test suites read
  // it from the canonical location, so it does not belong in a shipped mirror.
  cpSync(canonicalDir, dir, {
    recursive: true,
    filter: (src) => !src.includes(`${"/"}testdata`),
  });

  writeFileSync(join(dir, "README.md"), NOTICE);
}

console.log(`schemas mirrored into ${mirrors.length} package(s)`);
