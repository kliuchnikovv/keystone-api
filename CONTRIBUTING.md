# Contributing

## The contract lives in `schemas/`

Every change to the wire format starts in `schemas/sidecar/v1/`. Not in the Go
structs, not in `types.gen.ts`. A pull request that edits a language package
without a corresponding schema change will fail `verify-gen` or the reflective
drift test, which is the intended outcome.

Changing the wire format means, in order:

1. Edit the canonical schema under `schemas/sidecar/v1/`.
2. Add a fixture to `testdata/frames/valid/` (and `invalid/` if the change
   introduces a new way to be wrong).
3. `make gen` — mirrors and TS types regenerate. Commit the result.
4. Update the hand-written Go structs. The reflective test in `sidecar/schema`
   tells you exactly which field you missed.
5. `make test conformance`.

Additive changes only within a major: new optional fields, new methods, new
topics. Removing a field, narrowing a type or changing a default is a v2. See
[OPEN_QUESTIONS.md](./OPEN_QUESTIONS.md) §6.

## Why the Go types are hand-written

Asked often enough to belong here. JSON Schema is the source of truth, but Go
code generators produce the wrong types for this particular protocol:
`envelope.json` is a `oneOf`, and `params`/`result`/`payload` are deliberately
opaque — the correct Go type for them is `json.RawMessage`, so a `Peer` can
route a frame without knowing its schema. Generators emit `map[string]any` and
an allocation per push on the hot path, and none of them can produce an `Error`
that implements `error` and works with `errors.Is`.

The guarantee a generator would have given is instead enforced in CI by three
gates: the shared fixture corpus, a reflective schema↔struct diff, and the
cross-language conformance run. TypeScript *is* generated, because JSON Schema
maps onto it honestly.

## Running things

```bash
make lint-schemas   # compile every schema, run the corpus
make gen            # regenerate mirrors + TS types
make verify-gen     # fail if anything drifted
make test           # Go + TS
make conformance    # Go peer <-> TS peer over a real unix socket, both ways
make lint fmt
```

Go jobs must run with `GOWORK=off`; the Makefile already does this. Running
`go test` by hand inside `sidecar/` without it will pass on code that a consumer
cannot build.

## Releasing

Two independent tag namespaces. Nothing couples them; do not bump one because
the other moved.

**Sidecar protocol** — the Go module and the npm package release together under
one human-visible version:

```bash
# 1. bump ts/sidecar/package.json to the same X.Y.Z
# 2. update sidecar/CHANGELOG.md and ts/sidecar/CHANGELOG.md
git tag sidecar/v1.2.0
git push origin sidecar/v1.2.0
```

The tag prefix is Go's submodule convention, derived mechanically from the
directory path. `sidecar-v1.2.0` will never resolve; only `sidecar/v1.2.0`
will. There is no setting for this.

**Core API**:

```bash
git tag core/v1.2.0
git push origin core/v1.2.0
```

Verify a sidecar release actually resolves before announcing it:

```bash
cd $(mktemp -d) && go mod init smoke
go get github.com/kliuchnikovv/keystone-api/sidecar@v1.2.0
```

That command is also the check that `go.work` did not hide a missing
requirement.
