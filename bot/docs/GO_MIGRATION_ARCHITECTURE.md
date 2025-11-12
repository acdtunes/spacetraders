# Go Migration Architecture

## Executive Summary

This document outlines the architecture for migrating the SpaceTraders bot from Python to Go to achieve scalability to 100+ concurrent containers/ships. The migration maintains the PostgreSQL database schema, preserves the CQRS architectural pattern, and extracts OR-Tools route optimization to a Python gRPC microservice.

**Core Goals:**
- Scale from ~20 to 100+ concurrent containers without performance degradation
- Eliminate Python GIL bottleneck through true parallelism (goroutines)
- Maintain existing database schema and data (zero data migration)
- Preserve DDD/Hexagonal Architecture with CQRS patterns
- Leverage mature OR-Tools Python bindings via gRPC
- 100% unit test coverage with BDD acceptance tests

**Approach:**
- Separate binaries: `spacetraders` (CLI) and `spacetraders-daemon` (orchestrator)
- Hexagonal Architecture: Domain → Application → Adapters
- Simplified CQRS: Command/Query dispatch without behaviors (for POC)
- Goroutine-based container execution with channel communication
- GORM for database abstraction (PostgreSQL prod, SQLite :memory: tests)
- Python microservice for OR-Tools routing optimization
- BDD testing with Gherkin syntax (godog framework)
- Full vertical slice POC: NavigateShip command end-to-end

---

## Current State: Python Architecture

### Architecture Overview

```
┌────────────────────────────────────────────────────────┐
│          Python CLI (asyncio)                          │
└───────────────────┬────────────────────────────────────┘
                    │ Unix socket (JSON-RPC 2.0)
┌───────────────────▼────────────────────────────────────┐
│     Python Daemon (asyncio single event loop)          │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐             │
│  │ asyncio  │  │ asyncio  │  │ asyncio  │             │
│  │ Task 1   │  │ Task 2   │  │ Task N   │             │
│  └─────┬────┘  └─────┬────┘  └─────┬────┘             │
│        │             │              │                  │
│        └─────────────┴──────────────┘                  │
│                      │                                 │
│         ┌────────────▼───────────────┐                 │
│         │   Shared Resources         │                 │
│         │   (threading.Lock GIL)     │                 │
│         └────────────┬───────────────┘                 │
└──────────────────────┼─────────────────────────────────┘
                       │
        ┌──────────────┼──────────────┐
        │              │              │
        ▼              ▼              ▼
   PostgreSQL    OR-Tools       SpaceTraders
   (SQLAlchemy)  (in-process)   API (requests)
```

### Performance Bottlenecks

1. **GIL Contention**: Single-threaded execution despite asyncio concurrency
2. **Rate Limiter Lock**: Global `threading.Lock` creates queuing at scale
3. **OR-Tools CPU Blocking**: VRP solving (2-10s) holds GIL, freezing other containers
4. **Event Loop Starvation**: Single asyncio loop for 100+ tasks causes degradation
5. **Memory Overhead**: ~50MB per asyncio Task

### What Works Well

1. **PostgreSQL schema**: Well-designed with proper indexes, connection pooling
2. **CQRS pattern**: Clear separation of commands/queries, mediator dispatch
3. **OR-Tools routing**: Mature Python bindings, sophisticated algorithms
4. **Domain model**: Ship state machines, navigation logic, fuel constraints
5. **Container abstraction**: Lifecycle management, restart policies, logging

---

## Target Architecture: Go + Python Microservice

### High-Level Architecture

```
┌─────────────────────────────────────────────────────────────┐
│              spacetraders (Go CLI binary)                    │
│                                                              │
│  ┌──────────────────────────────────────────────────────┐   │
│  │  cobra CLI framework                                 │   │
│  │  • navigate, dock, orbit, refuel commands            │   │
│  │  • ship list, player info queries                    │   │
│  │  • daemon status, logs                               │   │
│  └────────────────────┬─────────────────────────────────┘   │
└───────────────────────┼──────────────────────────────────────┘
                        │
                        │ Unix socket
                        │ gRPC
                        │
┌───────────────────────▼──────────────────────────────────────┐
│        spacetraders-daemon (Go daemon binary)                │
│                                                              │
│  ┌────────────────────────────────────────────────────────┐ │
│  │              Container Orchestrator                    │ │
│  │                                                        │ │
│  │  ┌───────────┐  ┌───────────┐  ┌───────────┐         │ │
│  │  │ goroutine │  │ goroutine │  │ goroutine │  ...    │ │
│  │  │Container 1│  │Container 2│  │Container N│         │ │
│  │  └─────┬─────┘  └─────┬─────┘  └─────┬─────┘         │ │
│  │        │              │              │                │ │
│  │        └──────────────┴──────────────┘                │ │
│  │                       │                               │ │
│  └───────────────────────┼───────────────────────────────┘ │
│                          │                                  │
│  ┌───────────────────────▼───────────────────────────────┐ │
│  │            Shared Services Layer                      │ │
│  │  ┌─────────────────────────────────────────────────┐  │ │
│  │  │ Mediator (CQRS)                                 │  │ │
│  │  │  • Command/Query dispatch                       │  │ │
│  │  │  • Handler registry                             │  │ │
│  │  │  • Pipeline behaviors (logging, validation)     │  │ │
│  │  └─────────────────────────────────────────────────┘  │ │
│  │                                                        │ │
│  │  ┌─────────────────────────────────────────────────┐  │ │
│  │  │ Infrastructure Services                         │  │ │
│  │  │  • API Client (rate-limited via channels)       │  │ │
│  │  │  • Database pool (GORM)                          │  │ │
│  │  │  • OR-Tools gRPC client                         │  │ │
│  │  │  • Route cache                                  │  │ │
│  │  └─────────────────────────────────────────────────┘  │ │
│  └────────────────────────────────────────────────────────┘ │
└────────────────┬──────────────┬──────────────┬──────────────┘
                 │              │              │
                 │              │              │
    ┌────────────▼───┐   ┌──────▼──────┐   ┌──▼─────────────┐
    │  PostgreSQL    │   │  OR-Tools   │   │  SpaceTraders  │
    │  (existing)    │   │  Service    │   │  API           │
    │                │   │  (Python)   │   │  (external)    │
    └────────────────┘   └─────────────┘   └────────────────┘
                              │
                         ┌────▼────┐
                         │ gRPC    │
                         │ TCP     │
                         │ :50051  │
                         └─────────┘
```

### Component Breakdown

#### 1. Go CLI Binary (`spacetraders`)

**Purpose**: User-facing command-line interface

**Key Responsibilities:**
- Parse command-line arguments and flags (via cobra)
- Connect to daemon via Unix socket
- Send gRPC requests to daemon
- Display responses to user (formatted output)
- Error handling and user feedback

**Not Responsible For:**
- Business logic
- Database access
- API calls
- Container management

**Communication:**
- → Daemon: Unix socket at `/var/run/spacetraders.sock` using gRPC

---

#### 2. Go Daemon Binary (`spacetraders-daemon`)

**Purpose**: Long-running process managing all bot operations

**Key Responsibilities:**
- Container lifecycle management (start, stop, restart, recovery)
- gRPC server for CLI requests
- Mediator/CQRS command dispatch
- Goroutine-based concurrency
- Database persistence
- API rate limiting and coordination
- Ship assignment locking
- Container logging and monitoring

**Subsystems:**

**A. gRPC Server**
- Listens on Unix socket `/var/run/spacetraders.sock`
- Handles CLI requests (navigate, dock, orbit, ship list, etc.)
- Translates gRPC calls to mediator commands/queries
- Streams container logs to CLI

**B. Container Orchestrator**
- Creates goroutines for each container
- Manages container lifecycle (STARTING → RUNNING → STOPPED)
- Implements restart policies (NO, ON_FAILURE, ALWAYS)
- Graceful shutdown (context cancellation)
- Zombie assignment recovery on daemon restart

**C. Mediator (CQRS)**
- Command/Query dispatch via handler registry
- Pipeline behaviors (logging, validation)
- Handler lifecycle management
- Request/response type safety

**D. Infrastructure Services**
- API client with global rate limiting (2 req/sec coordination)
- Database connection pool (configurable size)
- OR-Tools gRPC client with connection pooling
- Route cache (in-memory LRU)

**Communication:**
- ← CLI: Unix socket gRPC (receives requests)
- → PostgreSQL: SQL over TCP (via GORM)
- → OR-Tools: gRPC over TCP (routing requests)
- → SpaceTraders API: HTTP/2 (fleet operations)

---

#### 3. Python OR-Tools Service (`ortools-service`)

**Purpose**: Route optimization and pathfinding microservice

**Key Responsibilities:**
- Dijkstra pathfinding with fuel constraints
- TSP (Traveling Salesman Problem) tour optimization
- VRP (Vehicle Routing Problem) fleet partitioning
- Route caching and memoization

**gRPC API:**
```protobuf
service RoutingService {
  rpc PlanRoute(RouteRequest) returns (RouteResponse);
  rpc OptimizeTour(TourRequest) returns (TourResponse);
  rpc PartitionFleet(VRPRequest) returns (VRPResponse);
}
```

**Why Keep This in Python:**
- OR-Tools Python bindings are official and mature
- Complex constraint solver API (Go bindings less complete)
- Routing is stateless (natural service boundary)
- Can upgrade independently of main daemon

**Communication:**
- ← Daemon: gRPC over TCP (port 50051)

---

#### 4. PostgreSQL Database (Existing)

**Schema Preservation:**
- All existing tables remain unchanged
- No data migration required
- Existing indexes, constraints, JSON columns preserved

**Key Tables:**
- `players`: Agent tokens, credits, metadata
- `containers`: Container state, config, restart policy
- `container_logs`: Structured logs with deduplication
- `ship_assignments`: Ship→Container lock table
- `market_data`: Cached market prices
- `waypoints`, `system_graphs`: Navigation data cache

**Migration Strategy:**
- Python SQLAlchemy → Go GORM (ORM with dialect abstraction)
- Mappers convert DB rows ↔ domain entities
- Connection pooling configured similarly

---

#### 5. SpaceTraders API (External)

**No Changes:**
- HTTP client in Go (net/http standard library)
- Rate limiting coordinated via channels (2 req/sec global)
- Retry logic with exponential backoff
- Error handling (429 Too Many Requests)

---

## Technology Stack

### Go Packages

| Component | Library | Rationale |
|-----------|---------|-----------|
| **CLI Framework** | `spf13/cobra` | Industry standard (kubectl, docker, gh) |
| **gRPC** | `google.golang.org/grpc` | Official gRPC Go implementation |
| **Protobuf** | `google.golang.org/protobuf` | Protocol buffer serialization |
| **Database ORM** | `gorm.io/gorm` | SQL dialect abstraction (PostgreSQL + SQLite :memory:) |
| **PostgreSQL Driver** | `gorm.io/driver/postgres` | PostgreSQL for production |
| **SQLite Driver** | `gorm.io/driver/sqlite` | In-memory SQLite for unit tests |
| **HTTP Client** | `net/http` (stdlib) | Built-in, excellent performance |
| **Rate Limiting** | `golang.org/x/time/rate` | Token bucket, no lock contention |
| **Logging** | `uber-go/zap` | Structured logging, high performance |
| **Configuration** | `spf13/viper` | Config file + env vars (integrates with cobra) |
| **Testing** | `stretchr/testify` | Assertions, mocks, suites |
| **BDD Testing** | `cucumber/godog` | Gherkin/Cucumber for Go (acceptance tests) |

### Python Stack (OR-Tools Service Only)

| Component | Library | Rationale |
|-----------|---------|-----------|
| **OR-Tools** | `ortools` (pip) | Official constraint solver |
| **gRPC** | `grpcio` | gRPC server implementation |
| **Protobuf** | `protobuf` | Message serialization |

### Infrastructure

- **Database**: PostgreSQL (existing, unchanged)
- **IPC**: Unix domain sockets (CLI ↔ Daemon)
- **RPC**: gRPC (Daemon ↔ OR-Tools)
- **Process Management**: systemd (Linux) or launchd (macOS)

---

## Hexagonal Architecture (Ports & Adapters)

### Layer Structure

The codebase follows Hexagonal Architecture with clear dependency rules:

```
┌─────────────────────────────────────────────────────────────┐
│                         Domain                              │
│  Pure business logic, no external dependencies              │
│  • Entities (Ship, Player, Route)                           │
│  • Value Objects (Waypoint, Fuel, FlightMode)               │
│  • Domain Events                                            │
│  • Domain Errors                                            │
└─────────────────────────────────────────────────────────────┘
                            ▲
                            │ depends on
┌───────────────────────────┴─────────────────────────────────┐
│                      Application                            │
│  Use cases via CQRS (Commands/Queries + Handlers)           │
│  • Commands (NavigateShip, DockShip, RefuelShip)            │
│  • Queries (GetShip, ListShips, GetPlayer)                  │
│  • Handlers (orchestrate domain + ports)                    │
│  • Mediator (dispatch requests to handlers)                 │
└─────────────────────────────────────────────────────────────┘
                            ▲
                            │ depends on
┌───────────────────────────┴─────────────────────────────────┐
│                   Ports (Interfaces)                        │
│  Define contracts for external dependencies                 │
│  • Repositories (PlayerRepository, ShipRepository)          │
│  • External Services (APIClient, RoutingClient)             │
└─────────────────────────────────────────────────────────────┘
                            ▲
                            │ implements
┌───────────────────────────┴─────────────────────────────────┐
│                      Adapters                               │
│  Infrastructure implementations                             │
│  • Database (GORM repositories)                             │
│  • API Client (SpaceTraders HTTP client)                    │
│  • Routing Client (OR-Tools gRPC client)                    │
│  • CLI (cobra commands)                                     │
│  • gRPC Server (daemon service)                             │
└─────────────────────────────────────────────────────────────┘
```

**Dependency Rule**: Dependencies point inward only
- Domain depends on nothing
- Application depends on Domain + Ports
- Adapters depend on Application + Ports (and implement Ports)

**Testing Strategy**:
- **Domain**: Pure unit tests (no mocks needed)
- **Application**: Unit tests with mocked repositories/clients
- **Adapters**: Integration tests with real dependencies (in-memory DB, test API)

---

## Simplified CQRS Pattern in Go

### Core Concepts

The CQRS (Command Query Responsibility Segregation) pattern separates write operations (Commands) from read operations (Queries). Both are dispatched through a simple Mediator.

**POC Simplification**: No pipeline behaviors (logging, validation) for initial implementation. Focus on core dispatch mechanism.

### Go Implementation

#### Commands and Queries

```go
// Command: Write operation
type NavigateShipCommand struct {
    ShipSymbol  string
    Destination string
    PlayerID    int
}

// Query: Read operation
type GetShipQuery struct {
    ShipSymbol string
    PlayerID   int
}
```

#### Handlers

```go
// Handler interface
type RequestHandler interface {
    Handle(ctx context.Context, request any) (any, error)
}

// Concrete command handler
type NavigateShipHandler struct {
    shipRepo    ShipRepository     // Port/interface
    apiClient   APIClient          // Port/interface
    routeClient RoutingClient      // Port/interface
}

func (h *NavigateShipHandler) Handle(ctx context.Context, req any) (any, error) {
    cmd := req.(*NavigateShipCommand)

    // Orchestrate domain logic using ports
    ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
    if err != nil {
        return nil, err
    }

    route, err := h.routeClient.PlanRoute(ctx, ...)
    if err != nil {
        return nil, err
    }

    // Execute navigation via domain entity
    ship.Navigate(route.Destination)

    // Persist via API
    err = h.apiClient.NavigateShip(ctx, cmd.ShipSymbol, cmd.Destination)
    return &NavigateShipResponse{...}, err
}
```

#### Simplified Mediator (No Behaviors)

```go
type Mediator interface {
    Send(ctx context.Context, request any) (any, error)
}

type mediator struct {
    handlers map[reflect.Type]RequestHandler
}

func (m *mediator) Send(ctx context.Context, request any) (any, error) {
    reqType := reflect.TypeOf(request)
    handler, ok := m.handlers[reqType]
    if !ok {
        return nil, fmt.Errorf("no handler registered for %s", reqType)
    }

    // Direct dispatch - no behaviors/middleware
    return handler.Handle(ctx, request)
}

// Registration
func NewMediator(handlers map[reflect.Type]RequestHandler) Mediator {
    return &mediator{handlers: handlers}
}
```

#### Handler Registration (Dependency Injection)

```go
// Wire up dependencies in main.go or container
func SetupMediator(
    db *gorm.DB,
    apiClient APIClient,
    routeClient RoutingClient,
) Mediator {
    // Create repositories
    shipRepo := NewGormShipRepository(db)
    playerRepo := NewGormPlayerRepository(db)

    // Create handlers with dependencies
    handlers := map[reflect.Type]RequestHandler{
        reflect.TypeOf(&NavigateShipCommand{}): &NavigateShipHandler{
            shipRepo:    shipRepo,
            apiClient:   apiClient,
            routeClient: routeClient,
        },
        reflect.TypeOf(&GetShipQuery{}): &GetShipHandler{
            shipRepo: shipRepo,
        },
    }

    return NewMediator(handlers)
}
```

### Benefits

- **Simplicity**: No behavior pipeline complexity in POC
- **Type Safety**: Compile-time type checking
- **Testability**: Easy to mock handlers (just implement interface)
- **Explicit**: All dependencies declared upfront
- **Extensible**: Can add behaviors later if needed

---

## IPC Design

### CLI ↔ Daemon: Unix Socket + gRPC

**Protocol Definition** (`daemon.proto`):

```protobuf
syntax = "proto3";
package daemon;

service DaemonService {
  // Container lifecycle
  rpc StartContainer(StartContainerRequest) returns (StartContainerResponse);
  rpc StopContainer(StopContainerRequest) returns (StopContainerResponse);
  rpc ListContainers(ListContainersRequest) returns (ListContainersResponse);
  rpc GetContainerLogs(LogsRequest) returns (stream LogEntry);

  // Ship operations (delegated to mediator)
  rpc NavigateShip(NavigateShipRequest) returns (NavigateShipResponse);
  rpc DockShip(DockShipRequest) returns (DockShipResponse);
  rpc OrbitShip(OrbitShipRequest) returns (OrbitShipResponse);
  rpc RefuelShip(RefuelShipRequest) returns (RefuelShipResponse);

  // Queries
  rpc ListShips(ListShipsRequest) returns (ListShipsResponse);
  rpc GetShip(GetShipRequest) returns (GetShipResponse);
  rpc GetPlayer(GetPlayerRequest) returns (GetPlayerResponse);
}

message NavigateShipRequest {
  string ship_symbol = 1;
  string destination = 2;
  int32 player_id = 3;
}

message NavigateShipResponse {
  string status = 1;
  int32 arrival_time_seconds = 2;
  string error = 3;
}

message LogEntry {
  string timestamp = 1;
  string level = 2;
  string message = 3;
  map<string, string> metadata = 4;
}
```

**Transport**: Unix domain socket at `/var/run/spacetraders.sock`

**Why Unix Socket:**
- Low latency (5-50 microseconds vs 1-5ms for TCP loopback)
- File system permissions for access control
- Single-machine deployment (no network configuration)
- Standard for daemon communication (Docker, containerd, systemd)

**Why gRPC:**
- Type-safe RPC with code generation
- Streaming support (container logs)
- Built-in error handling, retries, timeouts
- Well-documented, industry standard

---

### Daemon ↔ OR-Tools: TCP + gRPC

**Protocol Definition** (`routing.proto`):

```protobuf
syntax = "proto3";
package routing;

service RoutingService {
  rpc PlanRoute(RouteRequest) returns (RouteResponse);
  rpc OptimizeTour(TourRequest) returns (TourResponse);
  rpc PartitionFleet(VRPRequest) returns (VRPResponse);
}

message RouteRequest {
  string system_symbol = 1;
  string start_waypoint = 2;
  string goal_waypoint = 3;
  int32 current_fuel = 4;
  int32 fuel_capacity = 5;
  int32 engine_speed = 6;
  repeated Waypoint waypoints = 7;
}

message Waypoint {
  string symbol = 1;
  double x = 2;
  double y = 3;
  bool has_fuel = 4;
}

message RouteResponse {
  repeated RouteStep steps = 1;
  int32 total_fuel_cost = 2;
  int32 total_time_seconds = 3;
  double total_distance = 4;
}

message RouteStep {
  enum Action {
    TRAVEL = 0;
    REFUEL = 1;
  }
  Action action = 1;
  string waypoint = 2;
  int32 fuel_cost = 3;
  int32 time_seconds = 4;
}

message TourRequest {
  string system_symbol = 1;
  string start_waypoint = 2;
  repeated string waypoints = 3;
  int32 fuel_capacity = 4;
  int32 engine_speed = 5;
  repeated Waypoint all_waypoints = 6;
}

message TourResponse {
  repeated string visit_order = 1;
  repeated RouteStep combined_route = 2;
  int32 total_time_seconds = 3;
}

message VRPRequest {
  string system_symbol = 1;
  repeated string ship_symbols = 2;
  repeated string market_waypoints = 3;
  map<string, ShipConfig> ship_configs = 4;
  repeated Waypoint all_waypoints = 5;
}

message ShipConfig {
  string current_location = 1;
  int32 fuel_capacity = 2;
  int32 engine_speed = 3;
}

message VRPResponse {
  map<string, ShipTour> assignments = 1;
}

message ShipTour {
  repeated string waypoints = 1;
  repeated RouteStep route = 2;
}
```

**Transport**: TCP socket on `localhost:50051`

**Why TCP (not Unix socket):**
- OR-Tools service may run on different machine later (scalability)
- Standard gRPC transport
- Connection pooling built-in

**Connection Management:**
- Daemon maintains connection pool to OR-Tools service
- Automatic reconnection on failure
- Health checks via gRPC health protocol

---

## Container Orchestration

### Goroutine-Based Containers

Each container runs as an independent goroutine with:
- Dedicated context for cancellation
- Channel for lifecycle events
- Access to shared services (mediator, API client, DB pool)

**Container Lifecycle:**

```
STARTING ──→ RUNNING ──→ STOPPED
              │    ▲         ▲
              │    │         │
              │    │         │
              └────┴─→ FAILED┘
                       │
                  (restart policy)
```

**States:**
- **STARTING**: Container goroutine spawned, initializing resources
- **RUNNING**: Executing command handler logic
- **STOPPED**: Gracefully terminated (success or user-requested stop)
- **FAILED**: Terminated with error

**Restart Policies:**
- **NO**: Do not restart on failure
- **ON_FAILURE**: Restart only if exit code != 0
- **ALWAYS**: Restart regardless of exit code

**Restart Behavior:**
- Exponential backoff (1s, 2s, 4s, 8s, max 60s)
- Max restart attempts configurable
- Restart counter persisted to DB

### Channel Communication Patterns

**A. Container → Orchestrator (lifecycle events)**

```go
type ContainerEvent struct {
    ContainerID string
    Type        EventType  // STARTED, STOPPED, FAILED, LOG
    Timestamp   time.Time
    Data        interface{}
}

// Orchestrator receives events from all containers
eventChan := make(chan ContainerEvent, 100)

// Container sends events
eventChan <- ContainerEvent{
    ContainerID: "navigate-123",
    Type:        EventTypeStarted,
}
```

**B. API Client Rate Limiting (token bucket)**

```go
type APIRequest struct {
    Method   string
    Path     string
    Response chan APIResponse
}

// Global request queue
apiQueue := make(chan APIRequest, 1000)

// Rate limiter goroutine (single consumer)
go func() {
    limiter := rate.NewLimiter(2, 2)  // 2 req/sec, burst 2
    for req := range apiQueue {
        limiter.Wait(ctx)
        resp := executeHTTPRequest(req)
        req.Response <- resp
    }
}()

// Containers send requests
respChan := make(chan APIResponse)
apiQueue <- APIRequest{Method: "GET", Path: "/my-ships", Response: respChan}
resp := <-respChan
```

**C. Database Connection Pool**

```go
// Shared GORM DB instance (handles connection pooling internally)
db, _ := gorm.Open(postgres.Open(databaseURL), &gorm.Config{})
sqlDB, _ := db.DB()
sqlDB.SetMaxOpenConns(20)
sqlDB.SetMaxIdleConns(5)
sqlDB.SetConnMaxLifetime(time.Hour)

// Containers use pool (goroutine-safe)
func (h *NavigateShipHandler) Handle(ctx context.Context, req any) (any, error) {
    // Connection acquired from pool
    var ship Ship
    err := db.WithContext(ctx).Where("symbol = ?", symbol).First(&ship).Error
    // Connection returned to pool automatically
}
```

### Graceful Shutdown

**Signal Handling:**
```go
func main() {
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    // Listen for signals
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

    go func() {
        <-sigChan
        log.Info("shutdown signal received")
        cancel()  // Cancels all container contexts
    }()

    // Start daemon
    daemon.Run(ctx)
}
```

**Container Cancellation:**
```go
func runContainer(ctx context.Context, config ContainerConfig) error {
    for {
        select {
        case <-ctx.Done():
            log.Info("container cancelled, shutting down gracefully")
            return ctx.Err()
        case <-ticker.C:
            // Do work
        }
    }
}
```

### Zombie Assignment Recovery

**On Daemon Restart:**

1. Query database for containers in RUNNING state
2. Check if assigned ships still exist (via API)
3. Release ship assignments for non-existent ships
4. Recreate container goroutines for valid assignments
5. Restore container state from DB config

**Implementation Pattern:**
```go
func (o *Orchestrator) RecoverZombieContainers(ctx context.Context) error {
    // Find running containers from DB
    containers := o.db.ListContainersByStatus(ctx, "RUNNING")

    for _, container := range containers {
        // Verify ship still exists
        ship, err := o.apiClient.GetShip(ctx, container.ShipSymbol)
        if err != nil {
            // Ship doesn't exist, release assignment
            o.db.ReleaseShipAssignment(ctx, container.ShipSymbol)
            o.db.UpdateContainerStatus(ctx, container.ID, "FAILED")
            continue
        }

        // Recreate container goroutine
        go o.runContainer(ctx, container.Config)
    }
}
```

---

## OR-Tools Integration

### Python gRPC Service Architecture

**Service Structure:**

```
ortools-service/
├── server.py              # gRPC server entrypoint
├── routing_service.py     # gRPC service implementation
├── routing_engine.py      # OR-Tools solver logic (from existing Python code)
├── routing_pb2.py         # Generated protobuf code
├── routing_pb2_grpc.py    # Generated gRPC stubs
└── requirements.txt       # ortools, grpcio, protobuf
```

**Server Responsibilities:**
- gRPC server listening on port 50051
- Thread pool for concurrent requests (10 workers)
- Request validation and error handling
- Logging and monitoring
- Cache management (in-memory LRU)

**Routing Engine Responsibilities** (existing Python code):
- Dijkstra pathfinding with fuel constraints
- TSP optimization via OR-Tools `RoutingModel`
- VRP fleet partitioning via OR-Tools multi-vehicle routing
- Distance matrix caching

**Deployment:**
- Runs as systemd service (`ortools-service.service`)
- Health checks via gRPC health protocol
- Auto-restart on failure
- Logs to journald or file

### Caching Strategy

**Cache Levels:**

1. **Daemon-side route cache (Go)**:
   - LRU cache (1000 entries)
   - Key: `(system, start, goal, fuel_capacity, engine_speed)`
   - TTL: 1 hour (waypoints don't change often)
   - Reduces OR-Tools calls by ~40%

2. **OR-Tools service cache (Python)**:
   - Pathfinding result cache (existing implementation)
   - Distance matrix cache (N×N for system)
   - Persists across requests within service process

**Cache Invalidation:**
- System graph changes (rare, manual invalidation)
- Waypoint fuel availability changes (market updates)

### Failure Handling

**OR-Tools service unavailable:**
- Circuit breaker pattern in daemon
- Fallback: Simple Dijkstra without TSP optimization
- Alert/log warning but continue operations

**Timeout Handling:**
- VRP timeout: 30 seconds (configured in OR-Tools)
- gRPC timeout: 35 seconds (allows 5s buffer)
- If timeout, return partial solution or error

---

## Database Compatibility with GORM

### Schema Preservation

**Zero changes to existing schema:**
- All tables, columns, indexes remain identical
- JSON columns continue to store flexible data
- Composite primary keys preserved
- Foreign key constraints unchanged

**Migration Path:**

Python SQLAlchemy → Go GORM

**Why GORM:**
- **SQL dialect abstraction**: PostgreSQL (prod) + SQLite :memory: (tests) with same code
- **ORM convenience**: Auto-migrations, associations, hooks
- **Battle-tested**: Used by thousands of Go projects
- **No lock-in**: Can drop down to raw SQL when needed

### GORM Model Mapping

```go
// GORM model (database representation)
type ContainerModel struct {
    ContainerID   string `gorm:"primaryKey"`
    PlayerID      int    `gorm:"primaryKey"`
    Type          string
    Status        string
    Config        string `gorm:"type:jsonb"` // PostgreSQL JSONB
    RestartPolicy string
    CreatedAt     time.Time
    UpdatedAt     time.Time
}

func (ContainerModel) TableName() string {
    return "containers" // Existing table name
}

// Domain entity (business logic)
type Container struct {
    ID            string
    PlayerID      int
    Type          string
    Status        ContainerStatus
    Config        ContainerConfig
    RestartPolicy RestartPolicy
}

// Repository with GORM
type GormContainerRepository struct {
    db *gorm.DB
}

func (r *GormContainerRepository) Save(ctx context.Context, container *Container) error {
    // Map domain → GORM model
    model := &ContainerModel{
        ContainerID:   container.ID,
        PlayerID:      container.PlayerID,
        Type:          container.Type,
        Status:        string(container.Status),
        Config:        container.Config.ToJSON(),
        RestartPolicy: string(container.RestartPolicy),
    }

    // Upsert (create or update)
    return r.db.WithContext(ctx).
        Clauses(clause.OnConflict{
            Columns:   []clause.Column{{Name: "container_id"}, {Name: "player_id"}},
            DoUpdates: clause.AssignmentColumns([]string{"status", "config", "updated_at"}),
        }).
        Create(model).Error
}

func (r *GormContainerRepository) FindByID(ctx context.Context, id string, playerID int) (*Container, error) {
    var model ContainerModel
    err := r.db.WithContext(ctx).
        Where("container_id = ? AND player_id = ?", id, playerID).
        First(&model).Error
    if err != nil {
        return nil, err
    }

    // Map GORM model → domain entity
    return &Container{
        ID:            model.ContainerID,
        PlayerID:      model.PlayerID,
        Type:          model.Type,
        Status:        ContainerStatus(model.Status),
        Config:        ParseConfig(model.Config),
        RestartPolicy: RestartPolicy(model.RestartPolicy),
    }, nil
}
```

### Database Initialization (Multi-Dialect)

**Production (PostgreSQL):**
```go
import (
    "gorm.io/driver/postgres"
    "gorm.io/gorm"
)

func NewProductionDB(dsn string) (*gorm.DB, error) {
    db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
        Logger: logger.Default.LogMode(logger.Info),
    })
    if err != nil {
        return nil, err
    }

    // Connection pool
    sqlDB, _ := db.DB()
    sqlDB.SetMaxOpenConns(20)
    sqlDB.SetMaxIdleConns(5)
    sqlDB.SetConnMaxLifetime(time.Hour)

    return db, nil
}
```

**Testing (SQLite in-memory):**
```go
import (
    "gorm.io/driver/sqlite"
    "gorm.io/gorm"
)

func NewTestDB() (*gorm.DB, error) {
    db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
        Logger: logger.Default.LogMode(logger.Silent),
    })
    if err != nil {
        return nil, err
    }

    // Auto-migrate schema for tests
    db.AutoMigrate(
        &ContainerModel{},
        &PlayerModel{},
        &ContainerLogModel{},
        // ... other models
    )

    return db, nil
}
```

### Benefits of GORM Approach

✅ **Same code, different dialects**
- Unit tests use SQLite :memory: (fast, isolated)
- Integration tests use PostgreSQL (real behavior)
- No SQL dialect differences in code

✅ **Type-safe queries**
- Compile-time checks on struct fields
- Auto-completion in IDEs
- Reduced typo errors

✅ **Clean domain separation**
- GORM models in adapters layer
- Domain entities have no GORM tags
- Mapper pattern keeps concerns separated

✅ **Testing convenience**
- No need to setup PostgreSQL for unit tests
- Each test gets fresh :memory: database
- Parallel test execution (no shared state)

### Log Deduplication

**Preserve existing pattern:**
```go
type LogModel struct {
    ContainerID string `gorm:"primaryKey"`
    PlayerID    int    `gorm:"primaryKey"`
    Hash        string `gorm:"primaryKey"`
    Level       string
    Message     string
    Count       int
    FirstSeen   time.Time
    LastSeen    time.Time
}

func (r *GormLogRepository) WriteLog(ctx context.Context, log *ContainerLog) error {
    hash := sha256.Sum256([]byte(log.Message + log.Level))
    hashStr := hex.EncodeToString(hash[:])

    model := &LogModel{
        ContainerID: log.ContainerID,
        PlayerID:    log.PlayerID,
        Hash:        hashStr,
        Level:       log.Level,
        Message:     log.Message,
        Count:       1,
        FirstSeen:   time.Now(),
        LastSeen:    time.Now(),
    }

    // Upsert: increment count if exists, insert if new
    return r.db.WithContext(ctx).
        Clauses(clause.OnConflict{
            Columns: []clause.Column{
                {Name: "container_id"},
                {Name: "player_id"},
                {Name: "hash"},
            },
            DoUpdates: clause.Assignments(map[string]interface{}{
                "count":     gorm.Expr("container_logs.count + 1"),
                "last_seen": time.Now(),
            }),
        }).
        Create(model).Error
}
```

---

## Testing Strategy

### Testing Philosophy

**100% unit test coverage** for all business logic with **BDD acceptance tests** for user-facing features.

**Hexagonal Architecture testing benefits:**
- **Domain**: Pure unit tests (no mocks, no dependencies)
- **Application**: Unit tests with mocked ports (repositories, clients)
- **Adapters**: Integration tests with real dependencies

### Unit Testing with In-Memory SQLite

**Test Database Setup:**

```go
// test/helpers/db.go
package helpers

import (
    "gorm.io/driver/sqlite"
    "gorm.io/gorm"
)

func NewTestDB(t *testing.T) *gorm.DB {
    db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
        Logger: logger.Default.LogMode(logger.Silent),
    })
    require.NoError(t, err)

    // Auto-migrate all models
    err = db.AutoMigrate(
        &models.ContainerModel{},
        &models.PlayerModel{},
        &models.LogModel{},
    )
    require.NoError(t, err)

    // Cleanup after test
    t.Cleanup(func() {
        sqlDB, _ := db.DB()
        sqlDB.Close()
    })

    return db
}
```

**Repository Unit Test Example:**

```go
// internal/adapters/persistence/container_repository_test.go
package persistence_test

import (
    "context"
    "testing"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"

    "yourproject/internal/domain"
    "yourproject/internal/adapters/persistence"
    "yourproject/test/helpers"
)

func TestContainerRepository_Save(t *testing.T) {
    // Arrange
    db := helpers.NewTestDB(t)
    repo := persistence.NewGormContainerRepository(db)

    container := &domain.Container{
        ID:            "test-container-1",
        PlayerID:      123,
        Type:          "navigate",
        Status:        domain.StatusRunning,
        RestartPolicy: domain.RestartAlways,
    }

    // Act
    err := repo.Save(context.Background(), container)

    // Assert
    require.NoError(t, err)

    // Verify persistence
    found, err := repo.FindByID(context.Background(), "test-container-1", 123)
    require.NoError(t, err)
    assert.Equal(t, container.ID, found.ID)
    assert.Equal(t, container.Status, found.Status)
}

func TestContainerRepository_Upsert(t *testing.T) {
    // Arrange
    db := helpers.NewTestDB(t)
    repo := persistence.NewGormContainerRepository(db)

    container := &domain.Container{
        ID:       "test-1",
        PlayerID: 123,
        Status:   domain.StatusRunning,
    }

    // Act - Insert
    err := repo.Save(context.Background(), container)
    require.NoError(t, err)

    // Act - Update
    container.Status = domain.StatusStopped
    err = repo.Save(context.Background(), container)
    require.NoError(t, err)

    // Assert - No duplicate, status updated
    found, err := repo.FindByID(context.Background(), "test-1", 123)
    require.NoError(t, err)
    assert.Equal(t, domain.StatusStopped, found.Status)
}
```

**Handler Unit Test with Mocks:**

```go
// internal/application/navigation/navigate_ship_handler_test.go
package navigation_test

import (
    "context"
    "testing"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/mock"

    "yourproject/internal/application/navigation"
    "yourproject/internal/domain"
)

// Mock repository (using testify/mock)
type MockShipRepository struct {
    mock.Mock
}

func (m *MockShipRepository) FindBySymbol(ctx context.Context, symbol string, playerID int) (*domain.Ship, error) {
    args := m.Called(ctx, symbol, playerID)
    if args.Get(0) == nil {
        return nil, args.Error(1)
    }
    return args.Get(0).(*domain.Ship), args.Error(1)
}

// Mock API client
type MockAPIClient struct {
    mock.Mock
}

func (m *MockAPIClient) NavigateShip(ctx context.Context, symbol, destination string) error {
    args := m.Called(ctx, symbol, destination)
    return args.Error(0)
}

func TestNavigateShipHandler_Success(t *testing.T) {
    // Arrange
    mockShipRepo := new(MockShipRepository)
    mockAPIClient := new(MockAPIClient)
    mockRoutingClient := new(MockRoutingClient)

    handler := &navigation.NavigateShipHandler{
        ShipRepo:    mockShipRepo,
        APIClient:   mockAPIClient,
        RouteClient: mockRoutingClient,
    }

    ship := &domain.Ship{
        Symbol:   "TEST-1",
        PlayerID: 123,
        Location: "X1-A1",
        Status:   domain.NavStatusDocked,
    }

    route := &domain.Route{
        Steps: []domain.RouteStep{
            {Action: domain.ActionTravel, Waypoint: "X1-B2"},
        },
    }

    // Setup mocks
    mockShipRepo.On("FindBySymbol", mock.Anything, "TEST-1", 123).Return(ship, nil)
    mockRoutingClient.On("PlanRoute", mock.Anything, mock.Anything).Return(route, nil)
    mockAPIClient.On("NavigateShip", mock.Anything, "TEST-1", "X1-B2").Return(nil)

    cmd := &navigation.NavigateShipCommand{
        ShipSymbol:  "TEST-1",
        Destination: "X1-B2",
        PlayerID:    123,
    }

    // Act
    result, err := handler.Handle(context.Background(), cmd)

    // Assert
    assert.NoError(t, err)
    assert.NotNil(t, result)
    mockShipRepo.AssertExpectations(t)
    mockAPIClient.AssertExpectations(t)
}
```

### BDD Testing with Godog (Gherkin)

**Godog** is the official Cucumber/Gherkin implementation for Go.

**Installation:**
```bash
go get github.com/cucumber/godog/cmd/godog@latest
```

**Feature File Example:**

```gherkin
# features/navigation/navigate_ship.feature
Feature: Navigate Ship
  As a fleet commander
  I want to navigate ships between waypoints
  So that I can move cargo and explore systems

  Background:
    Given a player with ID 123 and agent "TEST-AGENT"
    And the player has a ship "TEST-1" at waypoint "X1-A1"
    And the ship "TEST-1" is docked

  Scenario: Successfully navigate to destination in same system
    When I navigate ship "TEST-1" to "X1-B2"
    Then the ship "TEST-1" should be in transit
    And the ship "TEST-1" should arrive at "X1-B2" eventually

  Scenario: Cannot navigate when ship already in transit
    Given the ship "TEST-1" is in transit to "X1-C3"
    When I attempt to navigate ship "TEST-1" to "X1-B2"
    Then I should receive an error "ship is already in transit"

  Scenario: Navigate with refuel stop
    Given waypoint "X1-A1" has no fuel
    And waypoint "X1-M1" has fuel
    And waypoint "X1-B2" has no fuel
    When I navigate ship "TEST-1" to "X1-B2"
    Then the route should include waypoint "X1-M1" for refueling
    And the ship "TEST-1" should refuel at "X1-M1"
    And the ship "TEST-1" should arrive at "X1-B2" eventually
```

**Step Definitions:**

```go
// features/navigation/navigate_ship_steps.go
package navigation_test

import (
    "context"

    "github.com/cucumber/godog"
    "yourproject/internal/domain"
    "yourproject/test/helpers"
)

type NavigationContext struct {
    db          *gorm.DB
    mediator    application.Mediator
    players     map[int]*domain.Player
    ships       map[string]*domain.Ship
    lastError   error
    lastResponse any
}

func (ctx *NavigationContext) aPlayerWithIDAndAgent(playerID int, agent string) error {
    player := &domain.Player{
        ID:          playerID,
        AgentSymbol: agent,
        Credits:     100000,
    }

    ctx.players[playerID] = player

    // Save to DB via repository
    repo := persistence.NewGormPlayerRepository(ctx.db)
    return repo.Save(context.Background(), player)
}

func (ctx *NavigationContext) thePlayerHasAShipAtWaypoint(shipSymbol, waypoint string) error {
    ship := &domain.Ship{
        Symbol:       shipSymbol,
        PlayerID:     123, // From background
        Location:     waypoint,
        NavStatus:    domain.NavStatusDocked,
        FuelCurrent:  400,
        FuelCapacity: 400,
    }

    ctx.ships[shipSymbol] = ship
    return nil
}

func (ctx *NavigationContext) theShipIsDocked(shipSymbol string) error {
    ship := ctx.ships[shipSymbol]
    ship.NavStatus = domain.NavStatusDocked
    return nil
}

func (ctx *NavigationContext) iNavigateShipTo(shipSymbol, destination string) error {
    cmd := &navigation.NavigateShipCommand{
        ShipSymbol:  shipSymbol,
        Destination: destination,
        PlayerID:    123,
    }

    result, err := ctx.mediator.Send(context.Background(), cmd)
    ctx.lastResponse = result
    ctx.lastError = err
    return nil
}

func (ctx *NavigationContext) theShipShouldBeInTransit(shipSymbol string) error {
    ship := ctx.ships[shipSymbol]
    if ship.NavStatus != domain.NavStatusInTransit {
        return fmt.Errorf("expected ship to be in transit, got %s", ship.NavStatus)
    }
    return nil
}

func (ctx *NavigationContext) iShouldReceiveAnError(expectedMsg string) error {
    if ctx.lastError == nil {
        return fmt.Errorf("expected error '%s', got nil", expectedMsg)
    }

    if !strings.Contains(ctx.lastError.Error(), expectedMsg) {
        return fmt.Errorf("expected error containing '%s', got '%s'", expectedMsg, ctx.lastError)
    }

    return nil
}

// Initialize godog suite
func InitializeScenario(ctx *godog.ScenarioContext) {
    navCtx := &NavigationContext{
        players: make(map[int]*domain.Player),
        ships:   make(map[string]*domain.Ship),
    }

    // Setup test DB before each scenario
    ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
        navCtx.db = helpers.NewTestDB(nil) // Custom helper for godog
        navCtx.mediator = setupTestMediator(navCtx.db)
        return ctx, nil
    })

    // Register step definitions
    ctx.Step(`^a player with ID (\d+) and agent "([^"]*)"$`, navCtx.aPlayerWithIDAndAgent)
    ctx.Step(`^the player has a ship "([^"]*)" at waypoint "([^"]*)"$`, navCtx.thePlayerHasAShipAtWaypoint)
    ctx.Step(`^the ship "([^"]*)" is docked$`, navCtx.theShipIsDocked)
    ctx.Step(`^I navigate ship "([^"]*)" to "([^"]*)"$`, navCtx.iNavigateShipTo)
    ctx.Step(`^the ship "([^"]*)" should be in transit$`, navCtx.theShipShouldBeInTransit)
    ctx.Step(`^I should receive an error "([^"]*)"$`, navCtx.iShouldReceiveAnError)
}
```

**Running BDD Tests:**

```bash
# Run all features
godog features/

# Run specific feature
godog features/navigation/navigate_ship.feature

# Run with tags
godog --tags=@smoke features/

# Generate code coverage
godog --format=pretty --format=junit:report.xml features/
```

### Test Organization

```
test/
├── helpers/
│   ├── db.go              # Test database setup (SQLite :memory:)
│   ├── mediator.go        # Test mediator wiring
│   └── fixtures.go        # Test data factories
│
├── unit/
│   ├── domain/
│   │   ├── ship_test.go           # Domain entity tests
│   │   └── navigation_test.go     # Domain logic tests
│   │
│   ├── application/
│   │   ├── handlers/
│   │   │   ├── navigate_ship_handler_test.go
│   │   │   └── dock_ship_handler_test.go
│   │   └── mediator_test.go
│   │
│   └── adapters/
│       ├── persistence/
│       │   ├── container_repository_test.go
│       │   └── player_repository_test.go
│       └── api/
│           └── client_test.go
│
└── features/                      # BDD acceptance tests
    ├── navigation/
    │   ├── navigate_ship.feature
    │   └── navigate_ship_steps.go
    ├── containers/
    │   ├── container_lifecycle.feature
    │   └── container_lifecycle_steps.go
    └── main_test.go              # Godog test suite entry point
```

### Testing Best Practices

**1. Use table-driven tests for multiple scenarios:**

```go
func TestShip_Navigate(t *testing.T) {
    tests := []struct {
        name        string
        ship        *domain.Ship
        destination string
        wantErr     bool
        errMsg      string
    }{
        {
            name: "success_from_docked",
            ship: &domain.Ship{Status: domain.NavStatusDocked},
            destination: "X1-B2",
            wantErr: false,
        },
        {
            name: "fail_already_in_transit",
            ship: &domain.Ship{Status: domain.NavStatusInTransit},
            destination: "X1-B2",
            wantErr: true,
            errMsg: "already in transit",
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            err := tt.ship.Navigate(tt.destination)
            if tt.wantErr {
                assert.Error(t, err)
                assert.Contains(t, err.Error(), tt.errMsg)
            } else {
                assert.NoError(t, err)
            }
        })
    }
}
```

**2. Use test fixtures for common data:**

```go
// test/helpers/fixtures.go
package helpers

func NewTestShip(symbol string, opts ...ShipOption) *domain.Ship {
    ship := &domain.Ship{
        Symbol:       symbol,
        PlayerID:     123,
        Location:     "X1-A1",
        NavStatus:    domain.NavStatusDocked,
        FuelCurrent:  400,
        FuelCapacity: 400,
    }

    for _, opt := range opts {
        opt(ship)
    }

    return ship
}

type ShipOption func(*domain.Ship)

func WithLocation(location string) ShipOption {
    return func(s *domain.Ship) {
        s.Location = location
    }
}

func InTransit() ShipOption {
    return func(s *domain.Ship) {
        s.NavStatus = domain.NavStatusInTransit
    }
}

// Usage:
ship := helpers.NewTestShip("TEST-1", helpers.WithLocation("X1-B2"), helpers.InTransit())
```

**3. Parallel test execution:**

```go
func TestSomething(t *testing.T) {
    t.Parallel() // Run in parallel with other tests

    db := helpers.NewTestDB(t) // Each test gets isolated :memory: DB
    // ... test logic
}
```

**4. Clean assertions with testify:**

```go
// Bad
if ship.Status != domain.NavStatusDocked {
    t.Errorf("expected status %s, got %s", domain.NavStatusDocked, ship.Status)
}

// Good
assert.Equal(t, domain.NavStatusDocked, ship.Status)

// Even better - with message
assert.Equal(t, domain.NavStatusDocked, ship.Status,
    "ship should be docked after successful dock command")
```

### Coverage Goals

**Minimum coverage requirements:**
- Domain layer: 100% (pure logic, easy to test)
- Application layer: >90% (business rules)
- Adapters layer: >70% (integration points)

**Run coverage:**
```bash
go test -cover ./...
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

---

## Vertical Slice POC: NavigateShip

### Scope

Implement a complete end-to-end flow for the `NavigateShip` command:

1. User invokes CLI: `./spacetraders navigate --ship MYAGENT-1 --destination X1-GZ7-C3`
2. CLI sends gRPC request to daemon
3. Daemon creates container (goroutine)
4. Container dispatches `NavigateShipCommand` via mediator
5. Handler calls OR-Tools service for route planning
6. Handler executes navigation steps (orbit → navigate → dock)
7. Container logs to database
8. Response returned to CLI with ETA
9. Container terminates successfully

### Components to Implement

#### 1. CLI Command (`cmd/spacetraders/main.go`)

**Cobra command:**
- `navigate` subcommand with flags: `--ship`, `--destination`, `--agent`
- Connect to daemon gRPC server
- Send `NavigateShipRequest`
- Display response (ETA, status, errors)

**Success Criteria:**
- CLI connects to daemon via Unix socket
- Flags parsed correctly
- Error messages displayed clearly
- Output formatted for readability

---

#### 2. Daemon gRPC Server (`cmd/spacetraders-daemon/main.go`)

**gRPC service implementation:**
- Implement `NavigateShip` RPC from `daemon.proto`
- Translate gRPC request → `NavigateShipCommand`
- Send command to mediator
- Return response to CLI

**Success Criteria:**
- Daemon starts and listens on Unix socket
- gRPC server handles NavigateShip requests
- Errors returned via gRPC status codes

---

#### 3. CQRS Mediator (`internal/mediator/`)

**Components:**
- `Mediator` interface and implementation
- Handler registry (map of request type → handler)
- Pipeline behaviors (logging)

**Success Criteria:**
- Mediator routes `NavigateShipCommand` to handler
- Logging behavior captures request/response
- Type-safe dispatch (compile-time errors for wrong types)

---

#### 4. NavigateShip Command Handler (`internal/application/navigation/`)

**Handler logic:**
- Fetch ship details from API
- Validate ship can navigate (not IN_TRANSIT, sufficient fuel)
- Call OR-Tools gRPC service for route planning
- Execute route steps:
  - If ship is DOCKED: call `orbit_ship` API
  - Call `navigate_ship` API with destination
  - If route requires refueling: call `refuel_ship` API
  - If final destination has dock: call `dock_ship` API
- Persist navigation event to database
- Return response with ETA

**Success Criteria:**
- Ship navigates from start to destination successfully
- Fuel constraints respected (refuel stops if needed)
- Ship state transitions correct (DOCKED → IN_ORBIT → IN_TRANSIT → IN_ORBIT → DOCKED)
- Navigation persisted to database

---

#### 5. OR-Tools gRPC Service (`ortools-service/`)

**Python gRPC server:**
- Extract existing `ortools_engine.py` routing logic
- Implement `PlanRoute` RPC
- Return route with fuel stops and time estimates

**Success Criteria:**
- gRPC server starts on port 50051
- Daemon can connect and call `PlanRoute`
- Returns valid route with steps (TRAVEL, REFUEL)
- Handles errors gracefully (no route found, timeout)

---

#### 6. Database Integration (`internal/infrastructure/persistence/`)

**Repositories:**
- `PlayerRepository`: Fetch player token from database
- `ContainerRepository`: Save container state
- `LogRepository`: Write container logs with deduplication

**Success Criteria:**
- Connection pool established
- Queries execute successfully
- Logs written with deduplication
- Container state persisted

---

#### 7. API Client (`internal/infrastructure/api/`)

**HTTP client:**
- GET `/my/ships/{symbol}` - fetch ship details
- POST `/my/ships/{symbol}/orbit` - put ship in orbit
- POST `/my/ships/{symbol}/navigate` - navigate to waypoint
- POST `/my/ships/{symbol}/dock` - dock ship
- POST `/my/ships/{symbol}/refuel` - refuel ship

**Rate Limiting:**
- Global rate limiter: 2 req/sec via channels
- Queue requests from multiple goroutines
- Retry on 429 with exponential backoff

**Success Criteria:**
- API calls succeed with valid token
- Rate limiting prevents 429 errors
- Errors handled gracefully (ship not found, insufficient fuel)

---

### Data Flow Diagram

```
┌─────────────────────────────────────────────────────────────────┐
│ User Terminal                                                    │
│                                                                  │
│  $ ./spacetraders navigate --ship MYAGENT-1 --destination X1-C3 │
└────────────────────────┬─────────────────────────────────────────┘
                         │
                         │ 1. Parse args, build gRPC request
                         │
┌────────────────────────▼─────────────────────────────────────────┐
│ CLI Binary (spacetraders)                                        │
│                                                                  │
│  grpc.Dial("unix:///var/run/spacetraders.sock")                 │
│  client.NavigateShip(NavigateShipRequest{...})                  │
└────────────────────────┬─────────────────────────────────────────┘
                         │
                         │ 2. gRPC call over Unix socket
                         │
┌────────────────────────▼─────────────────────────────────────────┐
│ Daemon gRPC Server                                               │
│                                                                  │
│  func (s *DaemonServer) NavigateShip(req) {                     │
│      cmd := NavigateShipCommand{...}                            │
│      response := mediator.Send(ctx, cmd)                        │
│      return response                                            │
│  }                                                              │
└────────────────────────┬─────────────────────────────────────────┘
                         │
                         │ 3. Dispatch command to mediator
                         │
┌────────────────────────▼─────────────────────────────────────────┐
│ Mediator (CQRS)                                                  │
│                                                                  │
│  - Find handler for NavigateShipCommand                         │
│  - Execute logging behavior (before)                            │
│  - Call handler.Handle(cmd)                                     │
│  - Execute logging behavior (after)                             │
└────────────────────────┬─────────────────────────────────────────┘
                         │
                         │ 4. Handler processes command
                         │
┌────────────────────────▼─────────────────────────────────────────┐
│ NavigateShipHandler                                              │
│                                                                  │
│  1. apiClient.GetShip(symbol)                                   │
│     ├─→ Rate limiter queue                                      │
│     └─→ HTTP GET /my/ships/MYAGENT-1                            │
│                                                                  │
│  2. routingClient.PlanRoute(start, goal, fuel, ...)     ────────┼─┐
│                                                                  │ │
│  3. Execute route steps:                                        │ │
│     ├─→ apiClient.OrbitShip(symbol)                             │ │
│     ├─→ apiClient.NavigateShip(symbol, destination)             │ │
│     └─→ apiClient.DockShip(symbol)                              │ │
│                                                                  │ │
│  4. logRepo.WriteLog(containerID, "Navigation complete")        │ │
│                                                                  │ │
│  5. return NavigateShipResponse{ETA: ...}                       │ │
└─────────────────────────────────────────────────────────────────┘ │
                                                                     │
                         ┌───────────────────────────────────────────┘
                         │ 5. gRPC call to OR-Tools service
                         │
┌────────────────────────▼─────────────────────────────────────────┐
│ OR-Tools Service (Python gRPC)                                   │
│                                                                  │
│  def PlanRoute(request):                                        │
│      graph = build_system_graph(request.waypoints)              │
│      route = dijkstra_with_fuel(graph, start, goal, ...)        │
│      return RouteResponse(steps=[...])                          │
└────────────────────────┬─────────────────────────────────────────┘
                         │
                         │ 6. Return route (steps, fuel, time)
                         │
                         └─────────────────────────────────────────┐
                                                                     │
┌────────────────────────▼─────────────────────────────────────────┘
│ Handler continues execution                                      │
│  - Executes navigation API calls                                │
│  - Logs to database                                             │
│  - Returns response                                             │
└────────────────────────┬─────────────────────────────────────────┘
                         │
                         │ 7. Response back through mediator
                         │
┌────────────────────────▼─────────────────────────────────────────┐
│ gRPC Server returns NavigateShipResponse                         │
└────────────────────────┬─────────────────────────────────────────┘
                         │
                         │ 8. gRPC response over Unix socket
                         │
┌────────────────────────▼─────────────────────────────────────────┐
│ CLI displays result                                              │
│                                                                  │
│  ✓ Navigating MYAGENT-1 to X1-GZ7-C3                            │
│    Route: X1-GZ7-A1 → X1-GZ7-B1 (refuel) → X1-GZ7-C3           │
│    ETA: 120 seconds                                             │
└──────────────────────────────────────────────────────────────────┘
```

---

### Testing Strategy

**Unit Tests** (Go):
- Mediator handler dispatch
- NavigateShipHandler logic (mocked API client, routing client)
- API client rate limiting
- Database repositories

**Integration Tests** (Go):
- CLI → Daemon gRPC communication
- Daemon → OR-Tools gRPC communication
- Database read/write with real PostgreSQL (test container)

**End-to-End Test**:
1. Start OR-Tools service
2. Start daemon
3. Invoke CLI command
4. Verify ship navigated via SpaceTraders API
5. Verify logs in database
6. Verify container cleaned up

**Manual Validation**:
- Register test agent
- Navigate ship between waypoints
- Observe logs in real-time
- Verify fuel management and refuel stops

---

## Success Criteria

The POC is successful if:

### Functional Requirements

✅ **Navigation works end-to-end**:
- Ship navigates from start to destination
- Fuel constraints respected (refuels if needed)
- Ship state transitions correctly

✅ **OR-Tools integration works**:
- Python service returns valid routes
- gRPC communication stable
- Route optimization applied (TSP for multi-waypoint)

✅ **Database compatibility**:
- Reads/writes to existing PostgreSQL schema
- No data loss or corruption
- Log deduplication works

✅ **CLI user experience**:
- Commands intuitive and clear
- Error messages helpful
- Output formatted well

### Performance Requirements

✅ **Concurrency proven**:
- Can start 10+ navigation containers concurrently
- No event loop starvation
- API rate limiting coordinates correctly

✅ **Response time acceptable**:
- CLI command response: <500ms for simple operations
- Navigation planning: <2 seconds (including OR-Tools call)
- Database queries: <100ms

✅ **Memory efficiency**:
- Daemon baseline: <50MB
- Per-container overhead: <5MB
- Total for 10 containers: <100MB (vs ~500MB in Python)

### Code Quality Requirements

✅ **CQRS pattern implemented correctly**:
- Commands/queries dispatched via mediator
- Handlers registered cleanly
- Behaviors work (logging)

✅ **Error handling robust**:
- API errors handled gracefully
- Database connection failures recoverable
- OR-Tools service unavailable → fallback or clear error

✅ **Testing coverage**:
- Unit tests for critical paths (>70% coverage)
- Integration tests for IPC
- End-to-end test passes

✅ **Code maintainability**:
- Clear package structure
- Documented interfaces
- Idiomatic Go code

---

## Key Decisions and Trade-offs

### Decision 1: Separate Binaries (Option 2)

**Decision**: Use two separate binaries (`spacetraders` CLI and `spacetraders-daemon`)

**Rationale**:
- Clear separation of concerns
- Explicit daemon lifecycle management
- Familiar pattern (Docker, containerd)
- Simpler than auto-managed daemon

**Trade-off**: Users must manage daemon manually (systemd/launchd service recommended)

---

### Decision 2: gRPC over Unix Socket (CLI ↔ Daemon)

**Decision**: Use gRPC instead of JSON-RPC or MessagePack

**Rationale**:
- Type-safe RPC with code generation
- Streaming support (for logs)
- Industry standard, well-documented
- Easy to add HTTP gateway later

**Trade-off**: Slightly higher overhead than raw MessagePack (~10-20%), but negligible for non-hot-path

---

### Decision 3: Python OR-Tools Service

**Decision**: Keep OR-Tools in Python, communicate via gRPC

**Rationale**:
- OR-Tools Python bindings are mature and official
- Go bindings exist but less complete
- Routing is stateless (natural service boundary)
- Can be replaced with Go later if needed

**Trade-off**: Adds network latency (1-5ms) and operational complexity (second service)

---

### Decision 4: Keep CQRS Pattern

**Decision**: Maintain CQRS despite Go not having built-in mediator pattern

**Rationale**:
- Architectural consistency with Python codebase
- Clear separation of write (commands) and read (queries)
- Easy to add behaviors (logging, validation, retries)
- Testability (mock handlers)

**Trade-off**: Requires custom implementation (reflection-based dispatch), adds some complexity

---

### Decision 5: GORM for Database Abstraction

**Decision**: Use `gorm.io/gorm` for database access instead of raw SQL with sqlx

**Rationale**:
- **SQL dialect abstraction**: Single codebase supports PostgreSQL (production) and SQLite :memory: (unit tests)
- **Testing convenience**: No need to setup PostgreSQL for unit tests, each test gets isolated in-memory DB
- **Type safety**: Compile-time checks on struct fields, reduced typos
- **Clean separation**: GORM models in adapters layer, domain entities remain pure
- **Battle-tested**: Used by thousands of Go projects, mature and stable

**Trade-off**: Some ORM "magic", but benefits outweigh concerns for this use case

---

### Decision 6: Simplified Mediator (No Behaviors for POC)

**Decision**: Implement simplified mediator with direct dispatch, no pipeline behaviors initially

**Rationale**:
- Focus on core CQRS dispatch mechanism
- Reduce complexity for POC
- Logging can be added directly in handlers
- Behaviors can be added later if needed

**Trade-off**: Some code duplication (logging in each handler), but acceptable for POC

---

### Decision 7: Vertical Slice POC (NavigateShip)

**Decision**: Implement full NavigateShip flow as first milestone, not incremental layers

**Rationale**:
- Proves entire architecture end-to-end
- Validates all integration points (CLI, daemon, OR-Tools, DB, API)
- De-risks early (find issues before investing heavily)
- Demonstrates value quickly

**Trade-off**: Takes longer to show initial progress, but reduces overall risk

---

## Migration Risks and Mitigations

### Risk 1: OR-Tools gRPC Latency

**Risk**: Network calls to OR-Tools service add 1-5ms latency per route request

**Impact**: Moderate - routing is not on hot path (navigation happens once per trip)

**Mitigation**:
- Aggressive caching in daemon (40% hit rate expected)
- Batch route requests where possible
- Fallback to simple Dijkstra if OR-Tools unavailable

**Acceptance**: For 2-10 second VRP solves, 5ms overhead is <0.5% cost

---

### Risk 2: CQRS Pattern Complexity in Go

**Risk**: Go lacks built-in mediator pattern, requires custom implementation with reflection

**Impact**: Low - implementation is straightforward, but adds learning curve

**Mitigation**:
- Use well-tested reflection patterns (similar to `encoding/json`)
- Comprehensive unit tests for mediator dispatch
- Clear documentation and examples

**Acceptance**: Benefits (testability, separation of concerns) outweigh complexity

---

### Risk 3: Database Migration Errors

**Risk**: Schema mismatches between Python and Go could corrupt data

**Impact**: High - data loss is unacceptable

**Mitigation**:
- Read-only mode for Go daemon initially (validate queries)
- Shadow deployment on test agent (parallel Python/Go)
- Database backup before cutover
- Extensive integration tests with real PostgreSQL

**Acceptance**: Migration will be validated thoroughly before production use

---

### Risk 4: Python Service Becomes Bottleneck

**Risk**: OR-Tools service crashes or becomes unavailable, blocking all navigation

**Impact**: High - cannot navigate without routing

**Mitigation**:
- Circuit breaker pattern in daemon
- Health checks via gRPC health protocol
- Fallback to simple Dijkstra pathfinding (no optimization)
- Auto-restart via systemd

**Acceptance**: Service is stateless and easy to restart; fallback ensures continuity

---

### Risk 5: Go Learning Curve

**Risk**: Team unfamiliar with Go idioms and concurrency patterns

**Impact**: Moderate - could slow initial development

**Mitigation**:
- POC focuses on core patterns (goroutines, channels, interfaces)
- Code reviews emphasize idiomatic Go
- Reference existing Go projects (Docker, Kubernetes) for patterns
- Incremental learning (POC → full migration)

**Acceptance**: Go's simplicity and strong tooling make learning curve manageable

---

## Next Steps After POC

### If POC Succeeds

1. **Expand command coverage**:
   - DockShip, OrbitShip, RefuelShip
   - ScoutMarkets (VRP fleet partitioning)
   - BatchContractWorkflow

2. **Production hardening**:
   - Systemd service files
   - Logging aggregation (structured logs to file/journald)
   - Metrics and monitoring (Prometheus exporter)
   - Graceful shutdown and recovery testing

3. **Migration tooling**:
   - Database validation script (compare Python vs Go queries)
   - Shadow mode (run Go daemon alongside Python, compare results)
   - Cutover checklist

4. **Documentation**:
   - Developer guide (how to add new commands)
   - Deployment guide (systemd setup, configuration)
   - Architecture decision records (ADRs)

### If POC Fails or Reveals Issues

1. **Identify failure mode**:
   - OR-Tools integration issues → Evaluate Go bindings or simplify routing
   - Performance not as expected → Profile and optimize
   - Complexity too high → Simplify architecture (remove CQRS, use simpler patterns)

2. **Adjust approach**:
   - If OR-Tools is blocker: Use Go bindings or simplified routing
   - If CQRS is overkill: Switch to simpler handler registry
   - If gRPC adds too much overhead: Use MessagePack over Unix socket

3. **Re-evaluate decision**:
   - Consider incremental migration (keep Python daemon, migrate CLI only)
   - Consider alternative languages (Rust, Kotlin) if Go proves unsuitable
   - Re-assess performance requirements (maybe 50 containers is sufficient)

---

## Conclusion

This Go migration architecture provides a clear path to scaling the SpaceTraders bot to 100+ concurrent containers while maintaining:
- **Existing database schema** (zero data migration)
- **CQRS architectural principles** (testability, separation of concerns)
- **OR-Tools routing optimization** (mature Python bindings)
- **Clean separation of concerns** (CLI, daemon, routing service)

The **vertical slice POC (NavigateShip)** validates the entire architecture end-to-end before heavy investment, reducing risk and demonstrating value early.

**Success depends on**:
- Goroutine-based concurrency eliminating GIL bottleneck
- Channel-based coordination for API rate limiting
- gRPC for type-safe, efficient IPC
- Python OR-Tools service for proven routing algorithms

This architecture balances **pragmatism** (keep Python where it works) with **performance** (Go where concurrency matters) to achieve the scaling goals.
