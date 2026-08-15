.PHONY: help gen gen-ts-types sync-schemas verify-gen lint-schemas \
	test test-go test-ts conformance fmt lint \
	proto proto-lint proto-breaking clean

help:
	@grep -hE '^[a-z-]+:.*?##' $(MAKEFILE_LIST) | sed 's/:.*##/\t/' | expand -t24

# ---------------------------------------------------------------- generation

gen: sync-schemas gen-ts-types  ## Regenerate schema mirrors and TS types

sync-schemas:  ## Copy canonical schemas into the Go and npm package mirrors
	node tools/sync-schemas.mjs

gen-ts-types:  ## JSON Schema -> ts/sidecar/src/types.gen.ts
	node tools/gen-ts-types.mjs

# Drift gate. Generated artefacts are committed, so a clean `gen` must be a
# no-op. If this fails someone edited a mirror or types.gen.ts by hand.
verify-gen: gen  ## Fail if generated artefacts are out of date
	git diff --exit-code -- sidecar/schema/v1 ts/sidecar/schemas ts/sidecar/src/types.gen.ts

lint-schemas:  ## Compile every schema and run the fixture corpus through it
	node tools/lint-schemas.mjs

# --------------------------------------------------------------------- tests

test: test-go test-ts  ## Run both language test suites

# GOWORK=off is not optional: see the comment in go.work.
test-go:  ## go test -race in sidecar/ and core/
	cd sidecar && GOWORK=off go test -race ./...
	cd core && GOWORK=off go test -race ./...

test-ts:  ## vitest in ts/sidecar
	npm test

conformance:  ## Go peer <-> TS peer over a real unix socket, both directions
	cd sidecar && GOWORK=off go build -o ../bin/conformance ./internal/conformance
	npm run build --workspace @keystone/sidecar-protocol
	node ts/sidecar/test/conformance.mjs

# --------------------------------------------------------------- style, vet

fmt:  ## gofmt + prettier
	gofmt -s -w sidecar core
	npx prettier --write "ts/sidecar/src/**/*.ts" "ts/sidecar/test/**/*.ts"

lint:  ## go vet + staticcheck + eslint
	cd sidecar && GOWORK=off go vet ./...
	cd core && GOWORK=off go vet ./...
	@if ! command -v staticcheck >/dev/null; then \
		echo "installing staticcheck..."; \
		go install honnef.co/go/tools/cmd/staticcheck@latest; \
	fi
	cd sidecar && GOWORK=off staticcheck ./...
	npm run typecheck

# --------------------------------------------------------------------- proto

proto:  ## Generate Go + TS stubs from core/proto
	cd core && buf generate

proto-lint:
	cd core && buf lint

proto-breaking:
	cd core && buf breaking --against '../.git#branch=main,subdir=core'

clean:
	rm -rf bin core/gen ts/sidecar/dist node_modules
