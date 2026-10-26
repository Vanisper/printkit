GO ?= go
CHROME ?=
VERSION ?= $(shell git describe --tags --always --dirty)

.PHONY: fmt fmt-check vet test race integration acceptance check build run

fmt:
	$(GO) fmt ./...

fmt-check:
	@test -z "$$(gofmt -l cmd internal)" || (gofmt -l cmd internal; exit 1)

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

integration:
	PRINTKIT_CHROME_TEST=1 PRINTKIT_CHROME_PATH="$(CHROME)" $(GO) test -count=1 ./...

acceptance:
	PRINTKIT_ACCEPTANCE_TEST=1 PRINTKIT_CHROME_PATH="$(CHROME)" $(GO) test -count=1 -run '^TestAcceptance' ./...

check: fmt-check vet test

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="-X main.version=$(VERSION)" -o bin/printkit ./cmd/printkit

run:
	$(GO) run ./cmd/printkit serve --data-dir work/dev
