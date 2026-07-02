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

.PHONY: dev dev-api dev-web build test lint fmt generate

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

$(GOLANGCI_LINT):
	mkdir -p bin
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/$(GOLANGCI_LINT_VERSION)/install.sh | sh -s -- -b ./bin $(GOLANGCI_LINT_VERSION)
