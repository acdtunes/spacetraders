.PHONY: build test clean proto lint install-tools routing-service

# Build all binaries
build: build-cli build-daemon build-routing-service

# Build CLI binary
build-cli:
	@echo "Building CLI..."
	@go build -o bin/spacetraders ./cmd/spacetraders

# Build daemon binary
build-daemon:
	@echo "Building daemon..."
	@go build -o bin/spacetraders-daemon ./cmd/spacetraders-daemon

# Build routing service manager binary
build-routing-service:
	@echo "Building routing service manager..."
	@go build -o bin/routing-service ./cmd/routing-service

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
	@echo "  help                 - Show this help message"
