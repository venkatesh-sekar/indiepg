# indiepg — build automation
#
# The panel is a single static binary with an embedded SPA. The frontend is
# built once with Node at compile time; the server never needs Node at runtime.

BINARY      := indiepg
PKG         := github.com/venkatesh-sekar/indiepg
WEB_DIR     := web
WEB_DIST    := internal/server/web/dist
DEV_STATE   := ./indiepg-dev.db
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -s -w -X $(PKG)/internal/core.Version=$(VERSION)

GO          ?= go
GOFLAGS     ?=

.DEFAULT_GOAL := build

.PHONY: build test vet tidy web run reset clean fmt fmt-check verify verify-web

## build: compile the static binary (CGO disabled, pure-Go deps only)
build:
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/indiepg

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

## fmt-check: fail if any tracked Go source is not gofmt-clean (gate check; does not rewrite)
fmt-check:
	@unformatted="$$(gofmt -l $$(git ls-files '*.go'))"; rc=$$?; \
	if [ $$rc -ne 0 ]; then \
	  echo "gofmt failed (exit $$rc) — likely a Go file it could not parse"; \
	  exit $$rc; \
	fi; \
	if [ -n "$$unformatted" ]; then \
	  echo "These Go files are not gofmt-clean (run 'make fmt'):"; \
	  echo "$$unformatted"; \
	  exit 1; \
	fi

## verify: run the full backend verify gate — fmt-check, vet, test, static build
verify: fmt-check vet test build

## verify-web: run the web verify gate (needs Node) — fresh install, typecheck, build, test
verify-web:
	cd $(WEB_DIR) && npm ci && npm run typecheck && npm run build && npm test

## tidy: tidy and verify the module graph
tidy:
	$(GO) mod tidy

## run: build and run the server locally (writable dev state, no root needed).
## On first run it prints a generated admin password — copy it to log in.
run: build
	./$(BINARY) serve --state $(DEV_STATE)

## reset: wipe local dev state (the SQLite db + WAL) for a clean slate
reset:
	rm -f $(DEV_STATE) $(DEV_STATE)-shm $(DEV_STATE)-wal

## clean: remove build artifacts
clean:
	rm -f $(BINARY)
	rm -rf $(WEB_DIR)/node_modules $(WEB_DIR)/dist
