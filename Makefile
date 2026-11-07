VERSION ?= dev
GOFILES_CHECK = test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }

.PHONY: check check-short bench-view bench-ingest fuzz fixture build image e2e e2e-bite

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

# Runs every fuzz target in the module for FUZZTIME each. -short keeps the
# Postgres tests out, so no target needs Docker.
fuzz:
	@for package in $$(go list ./...); do \
		targets=$$(go test -short -list '^Fuzz' "$$package") || exit 1; \
		for target in $$(printf '%s\n' "$$targets" | grep '^Fuzz'); do \
			echo "fuzz $$target $$package"; \
			go test -short -run '^$$' -fuzz "^$$target$$" -fuzztime $(FUZZTIME) "$$package" || exit 1; \
		done; \
	done

RECORD_REPO ?= Costroid/costroid-dogfood

# Places the recording of workflow run RUN for scenario SCENARIO under its
# adapter's testdata, after verifying it again. It downloads with gh.
fixture:
	go run ./scripts/record fixture --run "$(RUN)" --scenario "$(SCENARIO)" --repo "$(RECORD_REPO)"

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
