VERSION ?= dev
GOFILES_CHECK = test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }

.PHONY: check check-short bench-view fuzz build

# Runs everything, with Postgres in Docker. A missing Docker fails this target.
check:
	@$(GOFILES_CHECK)
	go vet ./...
	staticcheck ./...
	go test -count=1 ./...
	$(MAKE) bench-view

# Runs everything that needs no Docker.
check-short:
	@$(GOFILES_CHECK)
	go vet ./...
	staticcheck ./...
	go test -short -count=1 ./...

# Prints the per-PR view's p99 on one million rows and fails past 200 ms.
bench-view:
	go test -run '^$$' -bench '^BenchmarkPullRequestView$$' -benchtime 3000x -timeout 15m ./internal/server

FUZZTIME ?= 30s

# Runs each parser's fuzz target for FUZZTIME.
fuzz:
	go test -run '^$$' -fuzz '^FuzzVerify$$' -fuzztime $(FUZZTIME) ./internal/oidc
	go test -run '^$$' -fuzz '^FuzzReadIdentity$$' -fuzztime $(FUZZTIME) ./internal/oidc
	go test -run '^$$' -fuzz '^FuzzParseJSONTraces$$' -fuzztime $(FUZZTIME) ./internal/otlp
	go test -run '^$$' -fuzz '^FuzzParseJSONLogs$$' -fuzztime $(FUZZTIME) ./internal/otlp
	go test -run '^$$' -fuzz '^FuzzParseProtobuf$$' -fuzztime $(FUZZTIME) ./internal/otlp
	go test -short -run '^$$' -fuzz '^FuzzParseRows$$' -fuzztime $(FUZZTIME) ./internal/server

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/costroid ./cmd/costroid
