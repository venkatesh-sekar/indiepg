# pgpanel — build automation
#
# The panel is a single static binary with an embedded SPA. The frontend is
# built once with Node at compile time; the server never needs Node at runtime.

BINARY      := pgpanel
PKG         := github.com/venkatesh-sekar/pgpanel
WEB_DIR     := web
WEB_DIST    := internal/server/web/dist
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -s -w -X $(PKG)/internal/core.Version=$(VERSION)

GO          ?= go
GOFLAGS     ?=

.DEFAULT_GOAL := build

.PHONY: build test vet tidy web run clean fmt

## build: compile the static binary (CGO disabled, pure-Go deps only)
build:
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/pgpanel

## web: build the embedded SPA into internal/server/web/dist
web:
	cd $(WEB_DIR) && npm ci && npm run build

## test: run the full test suite
test:
	$(GO) test ./... -count=1

## vet: run go vet across all packages
vet:
	$(GO) vet ./...

## fmt: format all Go sources
fmt:
	$(GO) fmt ./...

## tidy: tidy and verify the module graph
tidy:
	$(GO) mod tidy

## run: build and run the server locally (writable dev state, no root needed).
## On first run it prints a generated admin password — copy it to log in.
run: build
	./$(BINARY) serve --state ./pgpanel-dev.db

## clean: remove build artifacts
clean:
	rm -f $(BINARY)
	rm -rf $(WEB_DIR)/node_modules $(WEB_DIR)/dist
