VERSION ?= dev
GOFILES_CHECK = test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }

.PHONY: check check-short bench-view bench-ingest fuzz build image e2e e2e-bite

# Runs everything, with Postgres in Docker. A missing Docker fails this target.
check:
	@$(GOFILES_CHECK)
	go vet -tags e2e ./...
	staticcheck -tags e2e ./...
	go test -count=1 ./...
	$(MAKE) bench-view

# Runs everything that needs no Docker.
check-short:
	@$(GOFILES_CHECK)
	go vet -tags e2e ./...
	staticcheck -tags e2e ./...
	go test -short -count=1 ./...

# Prints the per-PR view's p99 on one million rows and fails past 200 ms.
bench-view:
	go test -run '^$$' -bench '^BenchmarkPullRequestView$$' -benchtime 3000x -timeout 15m ./internal/server

INGEST_CORES ?= 8

# Prints rows per second through POST /v1/rows, parsing and pricing included,
# for one stream and for four at once, on an instance of INGEST_CORES cores.
bench-ingest:
	GOMAXPROCS=$(INGEST_CORES) go test -run '^$$' -bench '^BenchmarkIngestRows(Concurrently)?$$' -benchtime 10x ./internal/server

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

IMAGE ?= ghcr.io/costroid/costroid:$(VERSION)

image:
	docker build --tag $(IMAGE) --build-arg VERSION=$(VERSION) .

# Brings the Compose stack up, walks the recorded run through every step and
# tears it down. It needs Docker, and it stays out of check.
e2e:
	go test -tags e2e -count=1 -timeout 15m ./e2e

# Proves e2e fails, at every step that states the figure, when a price moves.
e2e-bite:
	./e2e/price-bite.sh
