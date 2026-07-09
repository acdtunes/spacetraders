.PHONY: build test clean proto lint install-tools routing-service build-watchkeeper run-watchkeeper build-captain run-captain cli-reference restart-daemon install-cli launchd install-launchd deploy assert-live-stamp

# ---- Build stamp (sp-898q) --------------------------------------------------
# Injected into internal/infrastructure/buildinfo at LINK time so every binary's
# startup banner names its own commit + build time. This retires the stale-binary
# hazard (L42): a merged fix is invisible if the live binary predates it.
#
# BUILD_TIME is captured here, at build — never time.Now() at runtime. The stamp
# must freeze the moment the binary is linked, not report when it started. All
# three are overridable (e.g. COMMIT=deadbeef make build) for reproducible builds.
BUILDINFO_PKG := github.com/andrescamacho/spacetraders-go/internal/infrastructure/buildinfo
COMMIT        ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
BUILD_TIME    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
VERSION       ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS       := -X '$(BUILDINFO_PKG).Commit=$(COMMIT)' -X '$(BUILDINFO_PKG).BuildTime=$(BUILD_TIME)' -X '$(BUILDINFO_PKG).Version=$(VERSION)'

# Build all binaries
build: build-cli build-daemon build-routing-service build-watchkeeper

# Build CLI binary
build-cli:
	@echo "Building CLI..."
	@go build -ldflags "$(LDFLAGS)" -o bin/spacetraders ./cmd/spacetraders

# Install the CLI on PATH: symlink bin/spacetraders into ~/.local/bin (idempotent).
# Config resolution finds config.yaml next to the real binary, so the shim works
# from any cwd.
install-cli: build-cli
	@mkdir -p $(HOME)/.local/bin
	@ln -sf $(abspath bin/spacetraders) $(HOME)/.local/bin/spacetraders
	@echo "Linked $(HOME)/.local/bin/spacetraders -> $(abspath bin/spacetraders)"

# Build daemon binary
build-daemon:
	@echo "Building daemon..."
	@go build -ldflags "$(LDFLAGS)" -o bin/spacetraders-daemon ./cmd/spacetraders-daemon

# Build routing service manager binary
build-routing-service:
	@echo "Building routing service manager..."
	@go build -o bin/routing-service ./cmd/routing-service

# Build watchkeeper supervisor binary
build-watchkeeper:
	@echo "Building watchkeeper..."
	@go build -ldflags "$(LDFLAGS)" -o bin/watchkeeper ./cmd/watchkeeper

# build-captain: back-compat alias for build-watchkeeper (muscle memory / scripts)
build-captain: build-watchkeeper

# Run watchkeeper supervisor locally
run-watchkeeper: build-watchkeeper
	@./bin/watchkeeper

# run-captain: back-compat alias for run-watchkeeper
run-captain: run-watchkeeper

# Restart the daemon (used by the watchkeeper fix pipeline's default restart_cmd)
restart-daemon: build-daemon
	@if launchctl list com.spacetraders.daemon >/dev/null 2>&1; then \
		launchctl kickstart -k gui/$$(id -u)/com.spacetraders.daemon; \
		echo "daemon restarted via launchd"; \
	else \
		if [ -f /tmp/spacetraders-daemon.pid ]; then \
			kill $$(cat /tmp/spacetraders-daemon.pid) 2>/dev/null || true; \
			sleep 2; \
		fi; \
		nohup ./bin/spacetraders-daemon >> daemon.log 2>&1 & \
		echo "daemon restarted (log: daemon.log)"; \
	fi

# ---- launchd deploy (sp-898q) -----------------------------------------------
# The live ~/Library/LaunchAgents plists are RENDERED from committed templates
# in ../deploy/launchd/*.plist.template. Load-bearing keys (BD_REAL, ExitTimeOut,
# the gobot path) live in the repo instead of being hand-maintained in a file
# that a regen clobbers — the historical whole-fleet outage when BD_REAL was
# dropped (sp-sk68) can no longer recur (retires captain-supervisor-plist-*).
#
# Paths are derived from where this Makefile actually runs ($(CURDIR)), so the
# rendered plist always points at the checkout you deploy FROM — deploy from the
# production checkout, never a worktree. All vars are overridable.
LAUNCHD_HOME ?= $(HOME)
GOBOT_DIR    ?= $(CURDIR)
REPO_ROOT    ?= $(patsubst %/,%,$(dir $(GOBOT_DIR)))
BD_REAL      ?= $(LAUNCHD_HOME)/IdeaProjects/bin/bd
AGENTS_DIR   ?= $(LAUNCHD_HOME)/Library/LaunchAgents
LAUNCHD_TPL  := $(abspath $(CURDIR)/../deploy/launchd)
LAUNCHD_OUT  := $(LAUNCHD_TPL)/build

# Render templates -> $(LAUNCHD_OUT) (gitignored). SAFE: writes only into the
# repo build dir, never touches the live launchd agents.
launchd:
	@LAUNCHD_HOME='$(LAUNCHD_HOME)' GOBOT_DIR='$(GOBOT_DIR)' REPO_ROOT='$(REPO_ROOT)' BD_REAL='$(BD_REAL)' \
		./scripts/render-launchd.sh '$(LAUNCHD_TPL)' '$(LAUNCHD_OUT)'

# Install rendered plists into ~/Library/LaunchAgents and reload each unit.
# ORCHESTRATOR-RUN at deploy: this MUTATES the live launchd configuration.
install-launchd: launchd
	@uid=$$(id -u); \
	for f in $(LAUNCHD_OUT)/*.plist; do \
		label=$$(basename "$$f" .plist); \
		cp "$$f" "$(AGENTS_DIR)/$$label.plist"; \
		launchctl bootout gui/$$uid/$$label 2>/dev/null || true; \
		launchctl bootstrap gui/$$uid "$(AGENTS_DIR)/$$label.plist"; \
		echo "installed + reloaded $$label"; \
	done

# Assert the freshly-built commit is the one actually running: grep the live
# service logs for the current short commit, fail loudly if a stale binary is
# still up (sp-898q, retires L42's stale-binary clause). ORCHESTRATOR-RUN.
STAMP_COMMIT := $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
assert-live-stamp:
	@echo "Asserting live binaries are at commit $(STAMP_COMMIT)..."
	@ok=0; \
	for i in $$(seq 1 30); do \
		if grep -q "$(STAMP_COMMIT)" "$(GOBOT_DIR)/captain-supervisor.log" 2>/dev/null \
			&& grep -q "$(STAMP_COMMIT)" "$(GOBOT_DIR)/daemon.log" 2>/dev/null; then \
			ok=1; break; \
		fi; \
		sleep 1; \
	done; \
	if [ $$ok -ne 1 ]; then \
		echo "DEPLOY ASSERT FAILED: captain-supervisor.log / daemon.log do not show commit $(STAMP_COMMIT)." >&2; \
		echo "A stale binary is likely still running. Inspect: launchctl list | grep spacetraders" >&2; \
		exit 1; \
	fi; \
	echo "OK: live binaries report commit $(STAMP_COMMIT)"

# Full deploy: build stamped binaries, install/reload launchd, restart, then
# assert the live stamp. ORCHESTRATOR-RUN from the PRODUCTION checkout.
deploy: build install-launchd
	@uid=$$(id -u); \
	launchctl kickstart -k gui/$$uid/com.spacetraders.daemon 2>/dev/null || true; \
	launchctl kickstart -k gui/$$uid/com.spacetraders.captain 2>/dev/null || true
	@$(MAKE) assert-live-stamp

# Regenerate the captain CLI reference from the spacetraders binary help tree
cli-reference: build-cli
	@./scripts/gen-cli-reference.sh ./bin/spacetraders ../captain/CLI_REFERENCE.md

# Run all tests
# Note: LC_DYSYMTAB warning is a known macOS/Go 1.24 issue and is harmless
test:
	@echo "Running tests..."
	@go test -v -race -cover ./... 2>&1 | grep -v "LC_DYSYMTAB" || true

# Run tests with summary report (colored output) - FAST mode (no race/cover)
test-summary:
	@./run-tests.sh

# Run tests - FAST mode (no race/cover, ~18s)
test-fast:
	@./run-tests.sh

# Run tests with race detection (~22s)
test-race:
	@./run-tests.sh --race

# Run tests with full checks - race + coverage (~31s)
test-full:
	@./run-tests.sh --race --cover

# Run tests with coverage report
test-coverage:
	@echo "Running tests with coverage..."
	@go test -v -race -coverprofile=coverage.out ./... 2>&1 | grep -v "LC_DYSYMTAB" || true
	@go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated at coverage.html"

# Run BDD tests
test-bdd:
	@echo "Running BDD tests..."
	@go test ./test/bdd/... -v

# Run BDD tests with pretty format
test-bdd-pretty:
	@echo "Running BDD tests (pretty format)..."
	@go test ./test/bdd/... -v -godog.format=pretty

# Run BDD tests for specific domain
test-bdd-ship:
	@echo "Running BDD tests for Ship entity..."
	@go test ./test/bdd/... -v -godog.paths=test/bdd/features/domain/navigation/ship_entity.feature

test-bdd-route:
	@echo "Running BDD tests for Route entity..."
	@go test ./test/bdd/... -v -godog.paths=test/bdd/features/domain/navigation/route_entity.feature

test-bdd-container:
	@echo "Running BDD tests for Container entity..."
	@go test ./test/bdd/... -v -godog.paths=test/bdd/features/domain/container/container_entity.feature

test-bdd-values:
	@echo "Running BDD tests for value objects..."
	@go test ./test/bdd/... -v -godog.paths=test/bdd/features/domain/shared/

test-bdd-navigate:
	@echo "Running BDD tests for navigate ship handler..."
	@go test ./test/bdd/... -v -godog.paths=test/bdd/features/application/navigate_ship_handler.feature

# Run unit tests only
test-unit:
	@echo "Running unit tests..."
	@go test -v -race -cover ./internal/... ./pkg/... 2>&1 | grep -v "LC_DYSYMTAB" || true

# Run linter
lint:
	@echo "Running linter..."
	@golangci-lint run

# Format code
fmt:
	@echo "Formatting code..."
	@go fmt ./...
	@goimports -w .

# Generate protobuf code
proto:
	@echo "Generating Go protobuf code..."
	@protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		pkg/proto/daemon/*.proto
	@protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		pkg/proto/routing/*.proto
	@echo "Generating Python protobuf code for routing service..."
	@cd services/routing-service && bash generate_protos.sh

# Clean build artifacts
clean:
	@echo "Cleaning..."
	@rm -rf bin/
	@rm -f coverage.out coverage.html
	@rm -f *.log
	@rm -rf services/routing-service/venv
	@rm -rf services/routing-service/generated
	@find services/routing-service -name "*.pyc" -delete
	@find services/routing-service -name "__pycache__" -delete

# Install development tools
install-tools:
	@echo "Installing development tools..."
	@go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	@go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
	@go install github.com/cucumber/godog/cmd/godog@latest
	@go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	@go install golang.org/x/tools/cmd/goimports@latest

# Download dependencies
deps:
	@echo "Downloading dependencies..."
	@go mod download
	@go mod tidy

# Run daemon locally (without routing service)
run-daemon:
	@echo "Starting daemon..."
	@./bin/spacetraders-daemon

# Run routing service
run-routing-service:
	@echo "Starting routing service..."
	@cd services/routing-service && bash run.sh

# Run daemon with routing service
run-with-routing: build-daemon build-routing-service
	@echo "Starting routing service in background..."
	@cd services/routing-service && bash run.sh &
	@sleep 3
	@echo "Starting daemon with routing service..."
	@ROUTING_SERVICE_ADDR=localhost:50051 ./bin/spacetraders-daemon

# Calibrate the market model artifact (sp-1ek0 P0)
calibrate-market-model:
	@cd services/routing-service && ./venv/bin/python -m model.calibrate

# Run CLI (example: make run-cli CMD="ship list")
run-cli:
	@./bin/spacetraders $(CMD)

# Development setup
dev-setup: install-tools deps
	@echo "Development environment setup complete!"

# Docker build
docker-build:
	@echo "Building Docker images..."
	@docker build -t spacetraders-daemon -f Dockerfile.daemon .
	@docker build -t spacetraders-cli -f Dockerfile.cli .

# Help
help:
	@echo "Available targets:"
	@echo "  build                - Build all binaries (CLI, daemon, routing service)"
	@echo "  build-cli            - Build CLI binary only"
	@echo "  install-cli          - Build CLI and symlink it into ~/.local/bin (on PATH)"
	@echo "  build-daemon         - Build daemon binary only"
	@echo "  build-routing-service - Build routing service manager binary"
	@echo "  test                 - Run all tests (with race + cover)"
	@echo "  test-summary         - Run tests with colored summary (fast mode)"
	@echo "  test-fast            - Run tests in fast mode (~18s, no race/cover)"
	@echo "  test-race            - Run tests with race detection (~22s)"
	@echo "  test-full            - Run tests with full checks (~31s, race + cover)"
	@echo "  test-coverage        - Run tests with coverage report"
	@echo "  test-bdd             - Run all BDD tests (domain layer)"
	@echo "  test-bdd-pretty      - Run BDD tests with colored output"
	@echo "  test-bdd-ship        - Run BDD tests for Ship entity"
	@echo "  test-bdd-route       - Run BDD tests for Route entity"
	@echo "  test-bdd-container   - Run BDD tests for Container entity"
	@echo "  test-bdd-values      - Run BDD tests for value objects"
	@echo "  test-bdd-navigate    - Run BDD tests for navigate ship handler"
	@echo "  test-unit            - Run unit tests only"
	@echo "  lint                 - Run linter"
	@echo "  fmt                  - Format code"
	@echo "  proto                - Generate protobuf code (Go and Python)"
	@echo "  clean                - Remove build artifacts"
	@echo "  install-tools        - Install development tools"
	@echo "  deps                 - Download and tidy dependencies"
	@echo "  run-daemon           - Run daemon locally (without routing service)"
	@echo "  run-routing-service  - Run routing service only"
	@echo "  run-with-routing     - Run daemon with routing service"
	@echo "  run-cli              - Run CLI (use CMD='...' to pass commands)"
	@echo "  dev-setup            - Set up development environment"
	@echo "  docker-build         - Build Docker images"
	@echo "  launchd              - Render launchd plists from templates into deploy/launchd/build (safe)"
	@echo "  install-launchd      - Render + install plists into ~/Library/LaunchAgents and reload (deploy)"
	@echo "  deploy               - Build stamped binaries, install launchd, restart, assert live stamp (deploy)"
	@echo "  assert-live-stamp    - Fail if the live service logs do not show the current commit"
	@echo "  help                 - Show this help message"
