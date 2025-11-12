# SpaceTraders Go Bot

Go implementation of the SpaceTraders bot, designed to scale to 100+ concurrent containers through goroutine-based concurrency.

## Architecture

This project follows **Hexagonal Architecture** (Ports & Adapters) with **CQRS** pattern:

```
├── cmd/                        # Application entrypoints
│   ├── spacetraders/          # CLI binary
│   └── spacetraders-daemon/   # Daemon binary
├── internal/                   # Private application code
│   ├── domain/                # Business logic (no dependencies)
│   │   ├── navigation/        # Navigation entities & value objects
│   │   ├── player/            # Player entities
│   │   ├── container/         # Container entities
│   │   └── shared/            # Shared domain types
│   ├── application/           # Use cases (CQRS commands/queries)
│   │   ├── navigation/        # Navigation command handlers
│   │   ├── player/            # Player command handlers
│   │   └── common/            # Shared application logic
│   ├── adapters/              # Infrastructure implementations
│   │   ├── persistence/       # GORM repositories
│   │   ├── api/               # SpaceTraders HTTP client
│   │   ├── grpc/              # gRPC server implementation
│   │   └── cli/               # Cobra CLI commands
│   └── infrastructure/        # Cross-cutting concerns
│       ├── database/          # Database connection setup
│       ├── logging/           # Structured logging
│       └── config/            # Configuration management
├── pkg/                       # Public libraries
│   └── proto/                 # Protobuf definitions and generated code
├── ortools-service/           # Python OR-Tools gRPC microservice
└── test/                      # Tests
    ├── unit/                  # Unit tests
    ├── features/              # BDD tests (godog)
    └── helpers/               # Test utilities
```

## Technology Stack

- **CLI Framework**: [cobra](https://github.com/spf13/cobra)
- **gRPC**: [grpc-go](https://github.com/grpc/grpc-go)
- **ORM**: [GORM](https://gorm.io/) (PostgreSQL + SQLite for tests)
- **HTTP Client**: `net/http` (stdlib)
- **Rate Limiting**: [golang.org/x/time/rate](https://pkg.go.dev/golang.org/x/time/rate)
- **Logging**: [zap](https://github.com/uber-go/zap)
- **Testing**: [testify](https://github.com/stretchr/testify), [godog](https://github.com/cucumber/godog)
- **Configuration**: [viper](https://github.com/spf13/viper)

## POC Scope: NavigateShip Vertical Slice

The initial implementation focuses on a complete end-to-end flow for ship navigation:

1. User invokes CLI: `./spacetraders navigate --ship AGENT-1 --destination X1-C3`
2. CLI sends gRPC request to daemon
3. Daemon creates container (goroutine)
4. Container dispatches `NavigateShipCommand` via mediator
5. Handler calls OR-Tools service for route planning
6. Handler executes navigation steps (orbit → navigate → dock)
7. Container logs to database
8. Response returned to CLI

## Getting Started

### Prerequisites

- Go 1.25+
- PostgreSQL (for production)
- Python 3.11+ (for OR-Tools service)

### Installation

```bash
# Install Go dependencies
go mod download

# Build binaries
make build

# Or build individually
go build -o bin/spacetraders ./cmd/spacetraders
go build -o bin/spacetraders-daemon ./cmd/spacetraders-daemon
```

### Running

```bash
# Start the daemon
./bin/spacetraders-daemon

# In another terminal, use the CLI
./bin/spacetraders navigate --ship AGENT-1 --destination X1-C3
```

## Testing

```bash
# Run all tests
go test ./...

# Run with coverage
go test -cover ./...
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out

# Run BDD tests
godog features/
```

## Development

See [docs/GO_MIGRATION_ARCHITECTURE.md](../bot/docs/GO_MIGRATION_ARCHITECTURE.md) for detailed architecture documentation.

## License

MIT
