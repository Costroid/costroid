# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 The Costroid Authors

# Reproducible top-level targets (see AGENTS.md → Working here).
# Web targets assume `pnpm install` has been run (CI does it as a setup step).

VERSION ?= 0.1.0-dev

# golangci-lint is pinned and installed into ./bin via the official install
# script (its docs advise against `go install`/`go tool`). Remove
# bin/golangci-lint to force a reinstall after bumping the version.
GOLANGCI_LINT_VERSION := v2.12.2
GOLANGCI_LINT := bin/golangci-lint

.PHONY: dev dev-api dev-web build demo-fixtures demo-build demo-budget demo-manifest demo-screenshot test lint fmt generate release-snapshot sbom vulncheck

## dev: run the Go API and the Vite dev server together (Ctrl-C stops both)
dev:
	$(MAKE) -j2 dev-api dev-web

dev-api:
	go run ./cmd/costroid serve

dev-web:
	pnpm -C web dev

## build: build the dashboard and produce the single binary at bin/costroid.
## go:embed cannot reach outside internal/webdist, so the built dashboard is
## copied into internal/webdist/dist before compiling.
build:
	pnpm -C web build
	rm -rf internal/webdist/dist
	mkdir -p internal/webdist/dist
	cp -R web/dist/. internal/webdist/dist/
	touch internal/webdist/dist/.gitkeep
	go build -ldflags "-X main.version=$(VERSION)" -o bin/costroid ./cmd/costroid

## demo-fixtures: capture the static demo's API fixtures + generate ranges.ts.
## Seeds an isolated synthetic store exactly as `costroid demo` (pinned asOf),
## then writes web/src/demo/fixtures/*.json + web/src/demo/ranges.ts verbatim.
## Re-running reproduces both byte-identically.
demo-fixtures:
	go run ./internal/demofixtures

## demo-build: build the backendless static demo dashboard into web/demo-dist
## (fixture-backed api seam + Latin/Turkish subset font). Never writes web/dist.
demo-build:
	pnpm -C web build --mode demo

## demo-budget: authoritative size gate for web/demo-dist. App payload
## (html+css+js) gz@9 <= 150 KB; subset font wire <= 80 KB. Writes
## demo-artifacts/sizes.json; exits non-zero on breach. Run demo-build first.
demo-budget:
	node scripts/demo-budget.mjs

## demo-manifest: write demo-artifacts/manifest.json (git sha, versions, sizes).
## Requires demo-artifacts/sizes.json (run demo-budget first).
demo-manifest:
	node scripts/demo-manifest.mjs

## demo-screenshot: capture a non-gating first-frame PNG of web/demo-dist into
## demo-artifacts/. Uses $$CHROME_BIN if set, else fetches chrome-headless-shell.
demo-screenshot:
	node scripts/demo-screenshot.mjs

## test: Go tests + web tests
test:
	go test ./...
	pnpm -C web test

## lint: Go vet + golangci-lint (linters + format check); web typecheck + ESLint + Prettier check
lint: $(GOLANGCI_LINT)
	go vet ./...
	$(GOLANGCI_LINT) run
	pnpm -C web typecheck
	pnpm -C web lint
	pnpm -C web format:check

## fmt: apply the same formatters `make lint` checks
fmt: $(GOLANGCI_LINT)
	$(GOLANGCI_LINT) fmt
	pnpm -C web format

## generate: regenerate Go + TS code from contracts/openapi.yaml
generate:
	go tool oapi-codegen -config internal/api/oapi-codegen.yaml contracts/openapi.yaml
	pnpm -C web generate

## release-snapshot: build the current native target through the release config.
release-snapshot:
	goreleaser build --single-target --snapshot --clean

## sbom: catalog Go and pnpm source dependencies as CycloneDX JSON.
sbom:
	mkdir -p release
	SYFT_CHECK_FOR_APP_UPDATE=false syft dir:. --exclude './.git/**' --exclude './.agents/**' --exclude './.claude/**' --exclude './.codex/**' --exclude './bin/**' --exclude './dist/**' --exclude './release/**' --exclude './release-input/**' --exclude './archive-stage/**' --exclude './node_modules/**' --exclude './web/node_modules/**' --exclude './internal/webdist/dist/**' -o cyclonedx-json=release/costroid.cdx.json

## vulncheck: fail when a known vulnerability is reachable from Costroid.
vulncheck:
	CGO_ENABLED=1 go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...

$(GOLANGCI_LINT):
	mkdir -p bin
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/$(GOLANGCI_LINT_VERSION)/install.sh | sh -s -- -b ./bin $(GOLANGCI_LINT_VERSION)
