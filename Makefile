VERSION ?= dev

.PHONY: check build

check:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }
	go vet ./...
	staticcheck ./...
	go test ./...

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/costroid ./cmd/costroid
