# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

SpaceTraders Go Bot - A production-quality Go implementation of a SpaceTraders bot using **Hexagonal Architecture** with **CQRS** pattern, designed to scale to 100+ concurrent operations through goroutine-based concurrency.

**Core Components:**
- **CLI** (`cmd/spacetraders`) - User-facing command-line interface
- **Daemon** (`cmd/spacetraders-daemon`) - gRPC server managing background operations
- **Routing Service** (`cmd/routing-service`) - Python OR-Tools microservice for pathfinding/optimization

## Building

### Build Commands

```bash
# Build all binaries
make build

# Build individually
make build-cli              # CLI only
make build-daemon           # Daemon only
make build-routing-service  # Routing service manager

# Development setup (install tools + dependencies)
make dev-setup
```

### Running Services

```bash
# Option 1: Daemon only (mock routing)
make run-daemon

# Option 2: With Python routing service
make run-with-routing       # Starts both services

# Option 3: Manual
./bin/routing-service       # Terminal 1
ROUTING_SERVICE_ADDR=localhost:50051 ./bin/spacetraders-daemon  # Terminal 2

# CLI usage
./bin/spacetraders ship navigate --ship AGENT-1 --destination X1-C3
```

### Other Commands

```bash
make proto         # Regenerate protobuf files (Go + Python)
make lint          # Run golangci-lint
make fmt           # Format code with gofmt + goimports
make clean         # Remove build artifacts
```

## Architecture

### Hexagonal Architecture (Ports & Adapters)

The codebase follows strict layering with dependency inversion:

```
Domain Layer (core business logic)
    ↑
Application Layer (CQRS commands/queries)
    ↑
Adapter Layer (infrastructure implementations)
```

**Key principle:** Dependencies point inward. Domain has zero external dependencies. Application layer depends only on domain. Adapters depend on application/domain through interfaces (ports).

### Domain Layer (`internal/domain/`)

Contains pure business logic organized into bounded contexts:

#### Entities (Aggregate Roots)

1. **Ship** (`navigation/ship.go`) - 374 lines, 30+ methods
   - **3-state machine**: DOCKED ↔ IN_ORBIT ↔ IN_TRANSIT
   - **Navigation operations**: Depart(), Dock(), StartTransit(), Arrive()
   - **Idempotent helpers**: EnsureInOrbit(), EnsureDocked()
   - **Fuel management**: ConsumeFuel(), Refuel(), RefuelToFull()
   - **Navigation calculations**: CanNavigateTo(), FuelRequired(), TravelTime(), SelectOptimalFlightMode()
   - **Cargo queries**: HasCargoSpace(), AvailableCargoSpace(), IsCargoEmpty(), IsCargoFull()

2. **Route** (`navigation/route.go`) - 253 lines
   - **States**: PLANNED → EXECUTING → COMPLETED/FAILED/ABORTED
   - **Route execution**: StartExecution(), AdvanceToNextSegment(), MarkAsCompleted()
   - **Linear progression** through RouteSegment value objects
   - **Auto-completion** when final segment reached

3. **Container** (`container/container.go`) - 313 lines
   - **States**: PENDING → RUNNING → COMPLETED/FAILED/STOPPED
   - **Iteration control**: Supports infinite loops (-1) or finite iterations
   - **Restart policy**: Max 3 restarts, tracks restart count
   - **Graceful shutdown**: RUNNING → STOPPING → STOPPED

#### Value Objects (Immutable)

Located in `domain/shared/`:

- **Waypoint** - Spatial location with Euclidean distance calculation
- **Fuel** - Immutable fuel state (operations return new instances)
- **Cargo** - Inventory manifest with CargoItem[] details
- **FlightMode** - Strategy pattern for 4 modes (CRUISE, DRIFT, BURN, STEALTH)
- **Errors** - Domain-specific error types (ErrInvalidState, ErrInsufficientFuel, etc.)

**Important:** Value objects are immutable. Operations like `fuel.Consume(10)` return a new Fuel instance.

### Application Layer (`internal/application/`)

Implements **CQRS pattern** via **Mediator**:

#### Mediator Pattern

- **Mediator** (`common/mediator.go`) - Type-safe request dispatcher using reflection
- **Registration**: Handlers register at startup via `RegisterHandler(handler)`
- **Dispatch**: `mediator.Send(ctx, command)` routes to appropriate handler

#### Commands (Write Operations)

Located in `application/{domain}/commands/`:

1. **RegisterPlayer** - Creates new player in database
2. **SyncPlayer** - Fetches player data from API and updates database
3. **NavigateShip** - Orchestrates ship navigation (orbit → navigate → dock)

Each command has:
- Request type (e.g., `NavigateRouteCommand`)
- Response type (e.g., `NavigateRouteResponse`)
- Handler with `Handle(ctx, request)` method

##### Navigation Command Hierarchy

**CRITICAL:** The codebase has TWO navigation commands with different purposes:

1. **NavigateRouteCommand** (HIGH-LEVEL) ✅
   - **USE THIS for all application workflows**
   - Handles complete navigation with route planning
   - Features: multi-hop routing, refueling stops, flight mode optimization
   - Used by: Business logic, workflows, CLI commands
   - Location: `application/ship/commands/navigate_route.go`

2. **NavigateDirectCommand** (LOW-LEVEL) ⚠️
   - **INTERNAL USE ONLY** - used by RouteExecutor
   - Simple atomic single-hop navigation (orbit → navigate API call)
   - NO route planning, NO refueling, NO optimization
   - Used by: RouteExecutor (executing planned route segments)
   - Location: `application/ship/commands/navigate_direct.go`

**Rule:** Always use `NavigateRouteCommand` unless you're implementing low-level route execution logic.

#### Queries (Read Operations)

Located in `application/{domain}/queries/`:

1. **GetPlayer** - Fetch single player by ID or agent symbol
2. **GetShip** - Fetch single ship by symbol
3. **ListShips** - Fetch all ships for a player
4. **ListPlayers** - Fetch all players

#### Ports (Interfaces)

Defined in `application/common/ports.go`:

- **ShipRepository** - Ship persistence operations
- **PlayerRepository** - Player persistence operations
- **APIClient** - SpaceTraders HTTP client
- **RoutingClient** - OR-Tools routing service
- **GraphProvider** - Waypoint graph for navigation

**Pattern:** Application layer depends only on interfaces. Concrete implementations live in adapters layer.

### Adapter Layer (`internal/adapters/`)

Infrastructure implementations:

#### Persistence (`adapters/persistence/`)

- **GORM ORM** with PostgreSQL
- **8 database models**: Player, Ship, Waypoint, Contract, Market, ContainerLog, etc.
- **Hybrid strategy**:
  - Ships: Fetched fresh from API (not cached)
  - Waypoints: Cached in database with TTL
  - Container logs: 60-second deduplication
- **Model-to-DTO conversion** at all boundaries

#### API (`adapters/api/`)

- **SpaceTradersClient**: HTTP client with 2 req/sec rate limiting
- **Exponential backoff** retries (max 5 attempts)
- **GraphBuilder**: Constructs navigation graphs from waypoints
- **Dual-cache strategy**: In-memory + database fallback

#### CLI (`adapters/cli/`)

- **Cobra framework** with 10+ command files
- **DaemonClient**: gRPC wrapper for daemon communication
- **Player resolution**: Flags → user config → error
- **Global flags**: `--socket`, `--player-id`, `--agent`, `--verbose`

#### gRPC Server (`adapters/grpc/`)

- **Unix domain sockets** for IPC (default: `/tmp/spacetraders-daemon.sock`)
- **DaemonServer**: Orchestrates background operations via ContainerRunner
- **ContainerRunner**: Executes operations in goroutines with database logging
- **Graceful shutdown**: 30-second timeout for in-flight operations

#### Routing (`adapters/routing/`)

- **gRPC client** for Python OR-Tools service
- **Mock implementation** for development without Python service
- **Real implementation**: Dijkstra pathfinding with fuel constraints

### Routing Service (`services/routing-service/`)

**Python gRPC microservice** using Google OR-Tools:

#### Algorithms

1. **PlanRoute (Dijkstra)** - Fuel-constrained pathfinding
   - 90% fuel rule: Force refuel at start if below 90% capacity
   - Automatic refuel stop insertion
   - Flight mode selection: BURN > CRUISE > DRIFT
   - 4-unit fuel safety margin

2. **OptimizeTour (TSP)** - Multi-waypoint tour optimization
   - OR-Tools Constraint Programming with Guided Local Search
   - Configurable return-to-start
   - Default timeout: 5 seconds

3. **PartitionFleet (VRP)** - Distribute markets across ships
   - Vehicle Routing Problem solver
   - Balanced load distribution
   - Disjunction penalty: 10x max distance
   - Default timeout: 30 seconds

#### Running Routing Service

```bash
# Option 1: Via Go binary (recommended)
./bin/routing-service

# Option 2: Direct Python
cd services/routing-service
source venv/bin/activate
python3 server/main.py --host 0.0.0.0 --port 50051

# Setup
cd services/routing-service
python3 -m venv venv
source venv/bin/activate
pip install -r requirements.txt
bash generate_protos.sh
```

## Adding New Features

### Adding a Command/Query

1. **Define types** in `application/{domain}/commands/` or `queries/`:
   ```go
   type MyCommand struct {
       Field string
   }
   type MyCommandResponse struct {
       Result string
   }
   ```

2. **Create handler**:
   ```go
   type MyCommandHandler struct {
       repo ports.MyRepository
   }
   func (h *MyCommandHandler) Handle(ctx context.Context, cmd MyCommand) (*MyCommandResponse, error) {
       // Implementation
   }
   ```

3. **Register handler** in application setup:
   ```go
   mediator.RegisterHandler(&MyCommandHandler{repo: myRepo})
   ```

4. **Add port interface** (if new dependencies needed) to `application/common/ports.go`

### Adding a CLI Command

1. **Create command file** in `internal/adapters/cli/`:
   ```go
   var myCmd = &cobra.Command{
       Use:   "mycommand",
       Short: "Description",
       Run: func(cmd *cobra.Command, args []string) {
           // Use DaemonClient to send gRPC request
       },
   }
   ```

2. **Register in root command** (`cli/root.go`)

3. **Add gRPC method** if needed (update protobuf + handler)

### Adding a Repository Method

1. **Add to port interface** in `application/common/ports.go`
2. **Implement in** `adapters/persistence/{entity}_repository.go`
3. **Update model** in `adapters/persistence/models.go` if schema changes

## Protobuf Workflow

**Definitions:** `pkg/proto/{daemon,routing}/*.proto`

**Regenerate code:**

```bash
make proto
```

This generates:
- Go code: `pkg/proto/{daemon,routing}/*.pb.go`
- Python code: `services/routing-service/generated/*.py`

**After protobuf changes:**
1. Update `.proto` file
2. Run `make proto`
3. Update handler implementations
4. Update client calls

## Important Patterns

### Error Handling

**Domain errors:** Use typed errors from `domain/shared/errors.go`:
```go
if ship.Fuel.Current < required {
    return domain.ErrInsufficientFuel
}
```

**Application errors:** Wrap with context:
```go
if err != nil {
    return nil, fmt.Errorf("failed to fetch ship %s: %w", symbol, err)
}
```

### State Transitions

**Always use entity methods** for state changes (don't modify fields directly):

```go
// WRONG
ship.NavStatus = navigation.NavStatusInOrbit

// RIGHT
if err := ship.Depart(); err != nil {
    return err
}
```

### Immutability in Value Objects

**Value objects return new instances:**

```go
// Fuel is immutable
newFuel, err := ship.Fuel.Consume(50)
if err != nil {
    return err
}
ship.Fuel = newFuel  // Replace with new instance
```

### Dependency Injection

**Always inject dependencies via constructors:**

```go
type MyHandler struct {
    repo ports.Repository
    client ports.APIClient
}

func NewMyHandler(repo ports.Repository, client ports.APIClient) *MyHandler {
    return &MyHandler{repo: repo, client: client}
}
```

**Never use global state or singletons.**

## Database

### Configuration

**Production:** PostgreSQL (configured via `.env` file)

**Configuration file:** `.env` (in project root - **never commit this file**)

**Primary connection string format:**
```bash
DATABASE_URL=postgresql://username:password@host:port/database
```

**Alternative individual environment variables (with ST_ prefix):**
- `ST_DATABASE_TYPE` - Database type (`postgres` or `sqlite`)
- `ST_DATABASE_HOST` - Database host
- `ST_DATABASE_PORT` - Database port
- `ST_DATABASE_USER` - Database user
- `ST_DATABASE_PASSWORD` - Database password
- `ST_DATABASE_NAME` - Database name
- `ST_DATABASE_SSLMODE` - SSL mode

**Connection Pool Settings:**
- `max_open` - Maximum open connections
- `max_idle` - Maximum idle connections
- `max_lifetime` - Connection max lifetime

**Other important environment variables:**
- `ST_ROUTING_ADDRESS` - Routing service address (e.g., `localhost:50051`)
- `SPACETRADERS_SOCKET` - Daemon socket path (default: `/tmp/spacetraders-daemon.sock`)

### Database Tables

All models are defined in `internal/adapters/persistence/models.go`:

| Table | Primary Key | Description |
|-------|-------------|-------------|
| `players` | `id` (auto) | Player accounts with API tokens |
| `waypoints` | `waypoint_symbol` | Cached waypoint data with coordinates and traits |
| `containers` | `id, player_id` | Background operation containers (goroutines) |
| `container_logs` | `id` (auto) | Container execution logs with 60s deduplication |
| `ship_assignments` | `ship_symbol, player_id` | Ship-to-container assignments |
| `system_graphs` | `system_symbol` | Cached navigation graphs (JSONB) |
| `market_data` | `waypoint_symbol, good_symbol` | Current market prices per good |
| `market_price_history` | `id` (auto) | Historical price tracking |
| `contracts` | `id` | Active contracts with delivery requirements |
| `mining_operations` | `id, player_id` | Mining operation configurations |
| `goods_factories` | `id` | Goods factory workflows |
| `transactions` | `id` | Financial ledger entries |
| `arbitrage_execution_logs` | `id` (auto) | Arbitrage trade execution history |
| `manufacturing_pipelines` | `id` | Manufacturing pipeline definitions |
| `manufacturing_tasks` | `id` | Individual manufacturing tasks |
| `manufacturing_task_dependencies` | `task_id, depends_on_id` | Task dependency graph |
| `manufacturing_factory_states` | `id` (auto) | Factory input/output state tracking |

### Key Model Details

**PlayerModel:**
- Credits are NOT persisted - always fetched fresh from API
- `metadata` stored as JSONB

**WaypointModel:**
- `has_fuel` stored as int (0/1) for SQLite compatibility
- `traits` and `orbitals` stored as JSON text
- `synced_at` for cache TTL management

**ContainerModel:**
- Composite primary key: `id + player_id`
- `parent_container_id` for hierarchical containers (coordinators)
- `config` stored as JSON text

**MarketData:**
- Composite primary key: `waypoint_symbol + good_symbol`
- `trade_type`: EXPORT, IMPORT, or EXCHANGE
- `supply` and `activity` for market dynamics

**TransactionModel:**
- `amount`: Positive for income, negative for expenses
- `balance_before` and `balance_after` for audit trail
- `operation_type`: contract, arbitrage, rebalancing, factory

### Migrations

Migrations are located in `migrations/` directory with up/down pairs:

```bash
# Apply migrations manually
psql -f migrations/001_normalize_id_columns.up.sql

# Key migrations:
# 001 - Normalize ID columns
# 002 - Add foreign key constraints
# 004 - Add mining tables
# 009 - Add goods factories table
# 011 - Add transactions table
# 013 - Add arbitrage execution logs
# 015 - Fix timezone timestamps (CRITICAL)
# 016 - Add market price history
# 018 - Add manufacturing tables
# 019 - Add market trade type
# 020 - Add pipeline type
```

**Migration 015 is critical:** Ensures all timestamp columns use `TIMESTAMP WITH TIME ZONE` (timestamptz) for proper timezone handling.

### Data Strategies

**Always Fresh (No Cache):**
- Ships - Fetched from API on every request
- Player credits - Always from API

**Database Cached:**
- Waypoints - Cached with TTL via `synced_at`
- System graphs - Cached indefinitely
- Market data - Age-filtered queries

**Deduplication:**
- Container logs - 60-second window deduplication

### Foreign Key Relationships

```
players (1) ──< containers (many)
players (1) ──< contracts (many)
players (1) ──< mining_operations (many)
players (1) ──< transactions (many)
players (1) ──< market_data (many)
containers (1) ──< container_logs (many)
containers (1) ──< ship_assignments (many)
manufacturing_pipelines (1) ──< manufacturing_tasks (many)
```

All foreign keys use `ON UPDATE CASCADE, ON DELETE CASCADE` or `ON DELETE SET NULL` for ship assignments

## Common Pitfalls

### 1. Breaking Hexagonal Architecture

**Wrong:** Domain depending on infrastructure:
```go
// In domain/navigation/ship.go
import "gorm.io/gorm"  // NEVER import infrastructure in domain
```

**Right:** Use dependency inversion via ports.

### 2. Modifying State Directly

**Wrong:**
```go
ship.Fuel.Current -= 50  // Fuel is immutable!
```

**Right:**
```go
ship.Fuel, err = ship.Fuel.Consume(50)
```

### 3. Forgetting to Register Handlers

If mediator can't find handler, check registration in application setup.

### 4. Not Using Idempotent Operations

For state transitions, use idempotent methods when appropriate:
```go
changed, err := ship.EnsureInOrbit()  // Returns false if already in orbit
```

### 5. Hardcoding Player/Agent Resolution

Always use CLI's player resolution pattern (flags → config → error).

## File Organization

### When Creating New Files

**Domain entities:** `internal/domain/{context}/{entity}.go`
- Example: `internal/domain/navigation/ship.go`

**Commands:** `internal/application/{context}/commands/{command_name}.go`
- Example: `internal/application/navigation/commands/navigate_ship.go`

**Queries:** `internal/application/{context}/queries/{query_name}.go`
- Example: `internal/application/player/queries/get_player.go`

**Repositories:** `internal/adapters/persistence/{entity}_repository.go`
- Example: `internal/adapters/persistence/ship_repository.go`

**CLI commands:** `internal/adapters/cli/{command_name}.go`
- Example: `internal/adapters/cli/navigate.go`

## Metrics & Monitoring

The daemon includes comprehensive Prometheus metrics and Grafana dashboards for observability. Metrics are **opt-in** and disabled by default.

### Enabling Metrics

Add to your `.env` file:
```bash
ST_METRICS_ENABLED=true
ST_METRICS_PORT=9090
ST_METRICS_HOST=localhost
ST_METRICS_PATH=/metrics
```

Start the daemon:
```bash
./bin/spacetraders-daemon
```

Metrics are now exposed at `http://localhost:9090/metrics` in Prometheus text format.

### Starting the Metrics Stack

Start Prometheus and Grafana with Docker Compose:

```bash
# Start the metrics stack
docker-compose -f docker-compose.metrics.yml up -d

# Check status
docker-compose -f docker-compose.metrics.yml ps

# View logs
docker-compose -f docker-compose.metrics.yml logs -f

# Stop the stack
docker-compose -f docker-compose.metrics.yml down
```

Access points:
- **Prometheus UI:** http://localhost:9091
- **Grafana UI:** http://localhost:3000 (login: admin/admin)

### Available Metrics

#### Container & Operational Metrics
- `spacetraders_daemon_container_running_total` - Currently running containers by type
- `spacetraders_daemon_container_total` - Container lifecycle events (completed/failed/stopped)
- `spacetraders_daemon_container_duration_seconds` - Container execution time distribution
- `spacetraders_daemon_container_restarts_total` - Container restart count
- `spacetraders_daemon_container_iterations_total` - Worker iteration completions
- `spacetraders_daemon_ships_total` - Ship count by role and location
- `spacetraders_daemon_ship_status_total` - Ship count by navigation status

#### Navigation Metrics
- `spacetraders_daemon_routes_total` - Route events by status (completed/failed)
- `spacetraders_daemon_route_duration_seconds` - Route execution time
- `spacetraders_daemon_route_distance_traveled_total` - Cumulative distance
- `spacetraders_daemon_route_fuel_consumed_total` - Cumulative fuel consumption
- `spacetraders_daemon_route_segments_completed_total` - Route segment count
- `spacetraders_daemon_fuel_purchased_units_total` - Fuel purchased by waypoint
- `spacetraders_daemon_fuel_consumed_units_total` - Fuel consumed by flight mode
- `spacetraders_daemon_fuel_efficiency_ratio` - Distance per fuel unit

#### Financial Metrics
- `spacetraders_daemon_player_credits_balance` - Current credits by agent
- `spacetraders_daemon_transactions_total` - Transaction count by type/category
- `spacetraders_daemon_transaction_amount` - Transaction amount distribution
- `spacetraders_daemon_total_revenue` - Revenue by category
- `spacetraders_daemon_total_expenses` - Expenses by category
- `spacetraders_daemon_net_profit` - Net profit (revenue - expenses)
- `spacetraders_daemon_trade_profit_per_unit` - Trade profitability per unit
- `spacetraders_daemon_trade_margin_percent` - Trade margin percentage

#### Command Metrics
- `spacetraders_daemon_commands_total` - Command executions by type and status
- `spacetraders_daemon_command_duration_seconds` - Command execution time

#### API Metrics
- `spacetraders_daemon_api_requests_total` - API requests by method/endpoint/status
- `spacetraders_daemon_api_request_duration_seconds` - API request latency
- `spacetraders_daemon_api_retries_total` - API retry attempts by reason
- `spacetraders_daemon_api_rate_limit_wait_seconds` - Rate limiter wait time

### Grafana Dashboards

Three pre-configured dashboards are automatically loaded:

**1. Operational Dashboard** (`spacetraders-operational`)
- Running containers gauge
- Container completion/failure rates
- Ship status distribution
- Container duration percentiles

**2. Navigation Dashboard** (`spacetraders-navigation`)
- Route completion rates
- Route duration percentiles
- Fuel consumption by flight mode
- Distance traveled
- Fuel efficiency trends

**3. Financial Dashboard** (`spacetraders-financial`)
- Credits balance over time
- Revenue vs expenses by category
- Net profit gauge
- Transaction volume breakdown
- Trade profitability analysis

### Architecture

**Metrics Collection Pattern:**
- Metrics collectors live in `internal/adapters/metrics/`
- Global singleton pattern for easy instrumentation
- Hexagonal architecture compliance (adapters observe domain events)
- Zero impact on domain layer

**Collector Lifecycle:**
- Initialized in `DaemonServer.NewDaemonServer()` if enabled
- Polling collectors (containers, ships, P&L) run as goroutines
- Event-based collectors (routes, transactions) record synchronously
- Graceful shutdown with context cancellation

**Adding New Metrics:**

1. Define metric in appropriate collector:
```go
// In internal/adapters/metrics/my_metrics.go
myMetric := prometheus.NewCounterVec(
    prometheus.CounterOpts{
        Namespace: "spacetraders",
        Subsystem: "daemon",
        Name:      "my_metric_total",
        Help:      "Description of my metric",
    },
    []string{"label1", "label2"},
)
```

2. Register in collector's `Register()` method

3. Record events where they occur:
```go
metrics.RecordMyEvent(playerID, value)
```

### Troubleshooting

**Metrics endpoint returns 404:**
- Check `ST_METRICS_ENABLED=true` in `.env`
- Restart daemon to pick up config changes

**Prometheus shows daemon target as "Down":**
- Verify daemon is running with metrics enabled
- Check `host.docker.internal` resolves from Prometheus container
- On Linux, use `--add-host=host.docker.internal:host-gateway` in docker-compose

**Grafana dashboards show "No Data":**
- Verify Prometheus is scraping successfully (check targets page)
- Ensure daemon has processed some activity to generate metrics
- Check time range in Grafana (default: last 1 hour)

**High memory usage:**
- Metrics retention is handled by Prometheus, not the daemon
- Adjust Prometheus `--storage.tsdb.retention.time` in docker-compose
- Default retention: 15 days

## Ledger System

The daemon includes a comprehensive double-entry ledger system for tracking all financial transactions.

### Transaction Types & Categories

**Transaction Types:**
- `DEBIT` - Money leaving the account (expenses)
- `CREDIT` - Money entering the account (income)

**Transaction Categories:**
- `CARGO_TRADE` - Buy/sell cargo transactions
- `SHIP_PURCHASE` - Ship acquisition costs
- `REFUEL` - Fuel purchase costs
- `CONTRACT_PAYMENT` - Contract rewards and deliveries
- `CONTRACT_ADVANCE` - Upfront contract payments
- `SHIP_MAINTENANCE` - Ship repair and maintenance
- `AGENT_REGISTRATION` - New agent creation costs
- `STARTING_BALANCE` - Initial credits from registration

### Recording Transactions

Transactions are automatically recorded by command handlers:

```go
// In a command handler
import "github.com/andrescamacho/spacetraders-go/internal/application/ledger/commands"

// Record a transaction
recordCmd := &commands.RecordTransactionCommand{
    PlayerID:    playerID,
    Amount:      1000,
    Type:        ledger.TransactionTypeCredit,
    Category:    ledger.CategoryCargoTrade,
    Description: "Sold IRON_ORE",
    Metadata: map[string]string{
        "good":     "IRON_ORE",
        "quantity": "10",
        "waypoint": "X1-AU21-K82",
    },
}
_, err := h.mediator.Send(ctx, recordCmd)
```

### Querying Transactions

**Get Transaction History:**
```bash
./bin/spacetraders ledger transactions --player 1 --limit 20
```

**Get Cash Flow Analysis:**
```bash
./bin/spacetraders ledger cash-flow --player 1 --category CARGO_TRADE
```

**Get Profit & Loss Statement:**
```bash
./bin/spacetraders ledger profit-loss --player 1
```

### Implementation Details

**Domain Layer** (`internal/domain/ledger/`)
- `Transaction` - Immutable transaction entity with validation
- `TransactionID` - Value object for unique transaction IDs
- `TransactionType` - DEBIT/CREDIT enumeration
- `Category` - Transaction category enumeration

**Application Layer** (`internal/application/ledger/`)
- `RecordTransactionCommand` - Creates new transaction entries
- `GetTransactionsQuery` - Retrieves transaction history
- `GetCashFlowQuery` - Analyzes income/expenses by category
- `GetProfitLossQuery` - Generates P&L statements

**Persistence** (`internal/adapters/persistence/`)
- `TransactionRepository` - PostgreSQL storage with indexing
- Automatically tracks created_at timestamps
- Supports filtering by date, category, and type

## Ship Balancing Algorithm

The contract fleet coordinator uses a global assignment tracking algorithm to distribute idle haulers evenly across markets.

### Algorithm Design

**Scoring Formula:**
```
score = (assigned_ships × 100) + (distance × 0.1)
```

Lower scores are better. The algorithm:
1. Counts ships at each market (exact location match, not proximity)
2. Heavily penalizes markets with existing ships (100× weight)
3. Uses distance as a tiebreaker (0.1× weight)
4. Ensures even distribution: 1 ship per market ideal, then 2 per market, etc.

**Example:**
- Market A with 1 ship at 10 units: score = 100 + 1 = 101
- Market B with 0 ships at 90 units: score = 0 + 9 = 9
- **Result:** Market B wins despite being 9× farther

### Implementation

**Domain Layer** (`internal/domain/contract/ship_balancer.go`)
```go
type ShipBalancer struct{}

func (b *ShipBalancer) SelectOptimalBalancingPosition(
    ship *navigation.Ship,
    markets []*shared.Waypoint,
    idleHaulers []*navigation.Ship,
) (*BalancingResult, error)
```

**Key Constants:**
- `AssignmentWeight = 100.0` - Penalty multiplier for existing ships
- `DistanceWeight = 0.1` - Distance factor for fuel efficiency

**Result:**
```go
type BalancingResult struct {
    TargetMarket  *shared.Waypoint
    Score         float64
    AssignedShips int     // Ships at this market
    Distance      float64 // Distance to market
}
```

## Navigation Resilience

The `RouteExecutor` includes resilience mechanisms to handle timing issues in multi-segment routes.

### In-Transit State Handling

**Problem:** Scout tours were failing with "cannot orbit while in transit" errors when navigating rapidly between waypoints.

**Solution:** The executor now checks ship state before each segment:

```go
// In RouteExecutor.executeSegment()
// 1. Reload ship to get latest state
freshShip, err := e.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
*ship = *freshShip

// 2. Wait for any in-transit movement to complete
if ship.NavStatus() == domainNavigation.NavStatusInTransit {
    err := e.waitForCurrentTransit(ctx, ship, playerID)
}

// 3. Now safe to orbit
err := e.ensureShipInOrbit(ctx, ship, playerID)
```

### Key Pattern

**Always reload ship state before operations:**
- Prevents stale state issues
- Handles rapid navigation cycles
- Waits for arrivals before new commands
- Eliminates race conditions

This pattern is critical for:
- Scout tours visiting multiple markets
- Contract delivery routes
- Mining operations with frequent trips

### Error Prevention

The resilience improvements prevent:
1. **"cannot orbit while in transit"** - Ship state machine violations
2. **API 4214 errors** - Duplicate navigation commands
3. **Race conditions** - Stale ship state between segments

## Timezone Awareness

### Database Timestamps

**CRITICAL:** All database timestamps use UTC with explicit timezone awareness.

**Migration 015** (`migrations/015_fix_timezone_timestamps.up.sql`) enforces:
- All timestamp columns use `TIMESTAMP WITH TIME ZONE` (timestamptz)
- Database stores all times in UTC internally
- PostgreSQL `NOW()` returns timestamptz (UTC with timezone offset)
- Application code must use timezone-aware timestamps

**Affected Tables:**
- `containers` (started_at, stopped_at)
- `container_logs` (timestamp)
- `arbitrage_execution_logs` (executed_at, validation_time, execution_time)
- `market_prices` (recorded_at)
- `market_price_history` (recorded_at)
- `transactions` (created_at)

### Go Time Handling

**Always use `time.Now()` for current time:**
```go
now := time.Now()  // Returns time in local timezone with offset
```

**Database insertion:**
```go
// GORM automatically converts time.Time to UTC for timestamptz columns
model.StartedAt = time.Now()  // Stored as UTC in database
```

**Querying:**
```go
// PostgreSQL NOW() returns current time in UTC
query := "SELECT * FROM containers WHERE started_at > NOW() - INTERVAL '10 minutes'"
// Comparisons work correctly because both sides are timezone-aware
```

### Log Timestamps

**Format:** RFC3339 with timezone offset
```
[2025-11-24T15:23:21-03:00] [container-id] INFO: message
```

**Implementation:**
```go
timestamp := time.Now().Format(time.RFC3339)
// Produces: 2025-11-24T15:23:21-03:00 (includes local timezone offset)
```

### Debugging Timezone Issues

**Check current timezone:**
```bash
# System timezone
date
# Output: Mon Nov 24 15:13:00 -03 2025

# PostgreSQL timezone
psql -c "SHOW TIMEZONE;"
# Output: UTC (or America/Argentina/Buenos_Aires)

# Database timestamps
psql -c "SELECT NOW();"
# Output: 2025-11-24 18:13:00+00 (UTC)
```

**Timezone mismatch symptoms:**
1. Log timestamps ahead/behind actual time
2. Query filters returning unexpected results
3. "Recent activity" queries missing records

**Fix:**
- Ensure PostgreSQL uses UTC or correct timezone
- Verify Go application reads system timezone correctly
- Check migration 015 has been applied
- All timestamp columns must be `timestamptz`, not `timestamp`

### Best Practices

1. **Always store UTC in database** - Let PostgreSQL handle timezone conversions
2. **Use `time.Now()` in Go** - Never construct timestamps manually
3. **Include timezone in logs** - Use RFC3339 format
4. **Query with NOW()** - PostgreSQL handles timezone-aware comparisons
5. **Avoid string timestamps** - Use proper time.Time types
6. **Verify across timezones** - Ensure behavior works in different timezone settings
