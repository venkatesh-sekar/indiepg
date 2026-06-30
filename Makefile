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

# ---- e2e (Docker-based backend integration harness; see test/e2e) ----
E2E_DIR                 := test/e2e
E2E_DOCKER              := $(E2E_DIR)/docker
E2E_BASE_IMAGE          := indiepg-e2e-base:latest
E2E_PREINSTALLED_IMAGE  := indiepg-e2e-preinstalled:latest
# DOCKER_CONTEXT is forced to the live daemon (the active desktop-linux context
# points at a dead socket in the dev environment). Override on the CLI if needed.
E2E_DOCKER_CONTEXT      ?= default
# SCENARIO=TestName runs just that scenario; empty runs the whole suite.
SCENARIO                ?=
E2E_RUN                 := $(if $(SCENARIO),-run $(SCENARIO),)

.PHONY: build test vet tidy web run reset clean fmt fmt-check verify verify-web \
        e2e e2e-images e2e-base e2e-preinstalled e2e-clean

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

## e2e-base: build the systemd base image with the freshly-built panel binary
e2e-base: build
	cp -f $(BINARY) $(E2E_DOCKER)/indiepg
	DOCKER_CONTEXT=$(E2E_DOCKER_CONTEXT) docker build -f $(E2E_DOCKER)/Dockerfile.base -t $(E2E_BASE_IMAGE) $(E2E_DOCKER)

## e2e-preinstalled: build the provisioned image (boots base, runs install, commits)
e2e-preinstalled: e2e-base
	DOCKER_CONTEXT=$(E2E_DOCKER_CONTEXT) bash $(E2E_DOCKER)/build-preinstalled.sh

## e2e-images: build both e2e images (base + preinstalled)
e2e-images: e2e-preinstalled

## e2e: build the images and run the e2e suite. Run one scenario with
##      `make e2e SCENARIO=TestBackupFull`. The build tag keeps these out of
##      `go test ./...`, so the per-push gate is unaffected.
e2e: e2e-images
	DOCKER_CONTEXT=$(E2E_DOCKER_CONTEXT) $(GO) test -tags e2e ./$(E2E_DIR)/... -count=1 -timeout 30m -v $(E2E_RUN)

## e2e-clean: remove the e2e images and the staged binary
e2e-clean:
	-DOCKER_CONTEXT=$(E2E_DOCKER_CONTEXT) docker rmi $(E2E_PREINSTALLED_IMAGE) $(E2E_BASE_IMAGE)
	-rm -f $(E2E_DOCKER)/indiepg

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
