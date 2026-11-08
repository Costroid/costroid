VERSION ?= dev
COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null)
GOFILES_CHECK = test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }

.PHONY: check test check-short bench-view bench-ingest fuzz fixture build image notice e2e e2e-bite lint lint-vulnerabilities lint-shell lint-workflows lint-deadcode

# Runs everything, with Postgres in Docker. A missing Docker fails this target.
check: test
	$(MAKE) bench-view

# Runs check without the timing gate, which CI runs as a job of its own.
test:
	@$(GOFILES_CHECK)
	go vet -tags e2e ./...
	staticcheck -tags e2e ./...
	go test -count=1 ./...

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

# Places the recording of workflow run RUN in repository RECORD_REPO
# (owner/name) for scenario SCENARIO under its adapter's testdata, after
# verifying it again. It downloads with gh.
fixture:
	@test -n "$(RECORD_REPO)" || { echo "set RECORD_REPO to the owner/name of the repository that ran the recording"; exit 2; }
	go run ./scripts/record fixture --run "$(RUN)" --scenario "$(SCENARIO)" --repo "$(RECORD_REPO)"

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)" -o bin/costroid ./cmd/costroid

# Writes NOTICE from the modules a release build links and the licence and
# notice files each holds in the module cache. check fails until it is run
# after a dependency changes.
notice:
	go run ./scripts/notice -o NOTICE

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

# The lint tools, each pinned by version and checksum and installed under
# bin/lint on first use. The go command checks each Go module against the
# checksum database at sum.golang.org. shellcheck is Ubuntu 26.04's amd64 build,
# checked against the SHA-256 that the archive's signed index lists; the
# checksum, not the transport, is the check.
LINT_BIN = $(CURDIR)/bin/lint
ACTIONLINT_VERSION = v1.7.12
DEADCODE_VERSION = v0.52.0
GOVULNCHECK_VERSION = v1.9.0
SHELLCHECK_DEB = shellcheck_0.11.0-2_amd64.deb
SHELLCHECK_SHA256 = 06990eaf21b5dfa3d855e492f7ca2d6fcacf63b490a73a545850c50cf7af2bce
SHELLCHECK_URL = http://archive.ubuntu.com/ubuntu/pool/universe/s/shellcheck/$(SHELLCHECK_DEB)
ACTIONLINT = $(LINT_BIN)/actionlint-$(ACTIONLINT_VERSION)/actionlint
DEADCODE = $(LINT_BIN)/deadcode-$(DEADCODE_VERSION)/deadcode
GOVULNCHECK = $(LINT_BIN)/govulncheck-$(GOVULNCHECK_VERSION)/govulncheck
SHELLCHECK = $(LINT_BIN)/$(SHELLCHECK_DEB:.deb=)/shellcheck
GO_INSTALL_CHECKED = GOSUMDB=sum.golang.org GONOSUMDB= GOPRIVATE= GOINSECURE= GOBIN=$(@D) go install

# Reports known vulnerabilities in code the module calls, shell findings in the
# scripts, workflow findings, with shellcheck run on every step's script, and
# functions no program reaches.
lint: lint-vulnerabilities lint-shell lint-workflows lint-deadcode

lint-vulnerabilities: $(GOVULNCHECK)
	$(GOVULNCHECK) ./...

lint-shell: $(SHELLCHECK)
	$(SHELLCHECK) action/*.sh e2e/*.sh scripts/*.sh

lint-workflows: $(ACTIONLINT) $(SHELLCHECK)
	$(ACTIONLINT) -shellcheck=$(SHELLCHECK) -pyflakes= .github/workflows/*.yml recipes/github-*.yml

# Fails on a function that no program of the module reaches and .deadcode-allow
# does not name, and on a name in .deadcode-allow that a program now reaches.
# Each line of the list is the date it was added, the function as
# package.Name under the module, and the change that will call it. Packages
# named *test hold helpers that only tests call, and are not judged.
lint-deadcode: $(DEADCODE)
	$(DEADCODE) -f '{{range .Funcs}}{{$$.Path}}.{{.Name}}{{"\n"}}{{end}}' ./... >$(LINT_BIN)/deadcode.found
	awk ' \
		FILENAME == ARGV[1] { \
			if (/^(#.*)?$$/) next; \
			if ($$1 !~ /^20[0-9][0-9]-[01][0-9]-[0-3][0-9]$$/ || NF < 3) { \
				printf ".deadcode-allow:%d: want a date, a function and the change that calls it\n", FNR; bad = 1; next \
			} \
			allowed[$$2] = FNR; next \
		} \
		{ sub(/^github\.com\/Costroid\/costroid\//, "") } \
		/^([a-z0-9_]+\/)*[a-z0-9_]*test\./ { next } \
		{ found[$$0] = 1 } \
		!($$0 in allowed) { print "unreachable and not in .deadcode-allow: " $$0; bad = 1 } \
		END { \
			for (name in allowed) if (!(name in found)) { \
				printf ".deadcode-allow:%d: %s is reached now; remove the line\n", allowed[name], name; bad = 1 \
			} \
			exit bad \
		}' .deadcode-allow $(LINT_BIN)/deadcode.found

$(ACTIONLINT):
	$(GO_INSTALL_CHECKED) github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)

$(DEADCODE):
	$(GO_INSTALL_CHECKED) golang.org/x/tools/cmd/deadcode@$(DEADCODE_VERSION)

$(GOVULNCHECK):
	$(GO_INSTALL_CHECKED) golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

$(SHELLCHECK):
	mkdir -p $(@D)
	curl -fsSL --retry 3 -o $(@D)/$(SHELLCHECK_DEB) $(SHELLCHECK_URL)
	echo '$(SHELLCHECK_SHA256)  $(@D)/$(SHELLCHECK_DEB)' | sha256sum --check --strict
	dpkg-deb --fsys-tarfile $(@D)/$(SHELLCHECK_DEB) | tar -xO ./usr/bin/shellcheck >$@.partial
	rm $(@D)/$(SHELLCHECK_DEB)
	chmod +x $@.partial
	$@.partial --version
	mv $@.partial $@
