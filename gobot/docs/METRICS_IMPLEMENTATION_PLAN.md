# Prometheus/Grafana Metrics Implementation Plan

## Document Information

**Status:** Design Phase
**Created:** 2025-11-22
**Author:** System Design
**Target Completion:** Incremental (7-11 hours total effort)

## Table of Contents

1. [Executive Summary](#executive-summary)
2. [Business Requirements](#business-requirements)
3. [Current System Analysis](#current-system-analysis)
4. [Architecture Design](#architecture-design)
5. [Metric Specifications](#metric-specifications)
6. [Implementation Phases](#implementation-phases)
7. [Technical Design Details](#technical-design-details)
8. [Grafana Dashboard Design](#grafana-dashboard-design)
9. [Configuration & Deployment](#configuration--deployment)
10. [Testing Strategy](#testing-strategy)
11. [Performance Considerations](#performance-considerations)
12. [Security & Best Practices](#security--best-practices)
13. [Migration & Rollout Plan](#migration--rollout-plan)
14. [Appendices](#appendices)

---

## Executive Summary

### Purpose

Implement comprehensive business metrics tracking for the SpaceTraders Go bot using Prometheus and Grafana to provide real-time visibility into operational performance, navigation efficiency, and financial health.

### Scope

**In Scope:**
- Operational metrics (containers, ships, system health)
- Navigation metrics (routes, fuel consumption, distance traveled)
- Financial metrics (credits balance, transactions, profit/loss)
- API & system metrics (request rates, latencies, errors)
- Grafana dashboards for visualization
- Docker-based Prometheus/Grafana deployment

**Out of Scope:**
- Application Performance Monitoring (APM) / distributed tracing
- Log aggregation (separate from metrics)
- Alerting rules (Phase 2 enhancement)
- Custom exporters for external services

### Key Goals

1. **Visibility:** Real-time insight into bot operations
2. **Performance:** Track navigation efficiency and fuel economy
3. **Financial:** Monitor profitability and spending patterns
4. **Reliability:** Detect API issues, container failures, system bottlenecks
5. **Architecture:** Maintain hexagonal architecture principles

### Success Criteria

- ✅ All business metrics tracked in real-time (< 30s lag)
- ✅ Grafana dashboards provide actionable insights
- ✅ No impact on bot performance (< 1% overhead)
- ✅ Zero breaking changes to existing codebase
- ✅ Metrics disabled by default (opt-in)
- ✅ Architecture compliance (no domain layer pollution)

---

## Business Requirements

### User Stories

**As a bot operator, I want to:**

1. **Monitor Operations**
   - See how many containers are running/completed/failed
   - Track container restart patterns to identify issues
   - View ship distribution across locations and states

2. **Optimize Navigation**
   - Analyze route completion rates and failure reasons
   - Track fuel consumption by flight mode
   - Measure fuel efficiency (distance per fuel unit)
   - Identify slow routes or inefficient paths

3. **Track Finances**
   - Monitor real-time credits balance
   - Analyze profit/loss by category (trading, contracts, fuel costs)
   - Measure trade profitability per good
   - Track spending patterns over time

4. **Ensure System Health**
   - Monitor API request rates and error rates
   - Detect rate limiting issues
   - Track command execution times
   - Identify database performance issues

### Prioritized Metrics (User Selection)

**Priority 1 (Must Have):**
- ✅ Operational (ships, routes, containers running)
- ✅ Navigation (routes, fuel, distance)
- ✅ Financial (credits, profit/loss)

**Priority 2 (Should Have):**
- API Health (requests, errors, latency)
- Command/query performance
- Database metrics

**Priority 3 (Nice to Have):**
- Container iteration tracking
- Trade margin analysis
- Market scanner metrics

---

## Current System Analysis

### Existing Infrastructure

#### No Existing Metrics System

**Current State:**
- ❌ No Prometheus client library
- ❌ No HTTP metrics endpoint
- ❌ No monitoring infrastructure
- ❌ No metrics collection code

**Business Metrics (NOT Observability Metrics):**
- `migrations/010_add_factory_metrics_columns.up.sql` → Factory performance columns
- `internal/domain/goods/goods_factory.go` → Business KPIs (nodes completed, speedup)

#### Daemon Server Architecture

**File:** `cmd/spacetraders-daemon/main.go` (408 lines)

**Current Setup:**
```go
// Unix domain socket gRPC server
listener, err := net.Listen("unix", socketPath)
grpcServer := grpc.NewServer()
daemon_pb.RegisterDaemonServiceServer(grpcServer, daemonServer)
grpcServer.Serve(listener)
```

**Key Findings:**
- ✅ Single gRPC server on Unix socket
- ❌ No HTTP server (need to add for Prometheus)
- ✅ Graceful shutdown implemented (30s timeout)
- ✅ Container lifecycle management via `DaemonServer`

#### Container System

**File:** `internal/adapters/grpc/daemon_server.go` (2,153 lines)

**DaemonServer Structure:**
```go
type DaemonServer struct {
    mediator      common.Mediator
    listener      net.Listener
    containers    map[string]*ContainerRunner  // Active containers
    containersMu  sync.RWMutex
    // ...
}
```

**Container Types (16 types):**
- Navigate, Dock, Orbit, Refuel
- Scout, Mining Worker/Coordinator
- Transport Worker, Contract Workflow
- Balancing, Trading, Fleet Assignment
- Purchase, etc.

**Container States:**
```go
PENDING → RUNNING → {COMPLETED, FAILED, STOPPED, INTERRUPTED}
```

**ContainerRunner:** `internal/adapters/grpc/container_runner.go` (498 lines)
- `Start()` → Container execution begins
- `Stop()` → Graceful shutdown
- `execute()` → Main loop with iteration tracking
- `handleError()` → Error handling + restart logic

**Key Metrics Opportunities:**
- Track state transitions (RUNNING → COMPLETED/FAILED)
- Monitor restart count (max 3 restarts)
- Measure runtime duration via `RuntimeDuration()`
- Count iterations (supports infinite loops with -1)

#### Navigation System

**Route Entity:** `internal/domain/navigation/route.go` (322 lines)

**Route States:**
```go
PLANNED → EXECUTING → {COMPLETED, FAILED, ABORTED}
```

**Route Metrics Methods (Already Available):**
```go
func (r *Route) TotalDistance() float64         // Sum of segment distances
func (r *Route) TotalFuelRequired() int         // Sum of fuel costs
func (r *Route) TotalTravelTime() int           // Sum of travel times
func (r *Route) CurrentSegmentIndex() int       // Progress tracking
func (r *Route) RuntimeDuration() time.Duration // Execution time
```

**RouteSegment Structure:**
```go
type RouteSegment struct {
    FromWaypoint   *shared.Waypoint
    ToWaypoint     *shared.Waypoint
    Distance       float64
    FuelRequired   int
    TravelTime     int
    FlightMode     shared.FlightMode
    RequiresRefuel bool
}
```

**Route Execution:** `internal/application/ship/route_executor.go` (100+ lines)
- Executes route segment by segment
- Handles refueling, docking, orbital maneuvers
- Scans markets opportunistically
- Perfect instrumentation point for metrics

**Ship Entity:** `internal/domain/navigation/ship.go` (374 lines)
- 3-state machine: DOCKED ↔ IN_ORBIT ↔ IN_TRANSIT
- `ConsumeFuel(amount)` → Track fuel consumption
- `Refuel()` → Track refuel events
- `CanNavigateTo()`, `SelectOptimalFlightMode()` → Navigation logic

#### Financial System (Ledger)

**Transaction Entity:** `internal/domain/ledger/transaction.go` (237 lines)

**Transaction Structure:**
```go
type Transaction struct {
    id                TransactionID
    playerID          shared.PlayerID
    timestamp         time.Time
    transactionType   TransactionType  // REFUEL, PURCHASE_CARGO, SELL_CARGO, etc.
    category          Category         // FUEL_COSTS, TRADING_REVENUE, etc.
    amount            int              // Positive=income, Negative=expense
    balanceBefore     int
    balanceAfter      int
    description       string
    metadata          map[string]interface{}
    relatedEntityType string
    relatedEntityID   string
}
```

**Transaction Types:**
- `TransactionTypeRefuel`
- `TransactionTypePurchaseCargo`
- `TransactionTypeSellCargo`
- `TransactionTypePurchaseShip`
- `TransactionTypeContractAccepted`
- `TransactionTypeContractFulfilled`

**Categories:**
```go
const (
    CategoryFuelCosts        = "FUEL_COSTS"        // Expense
    CategoryTradingRevenue   = "TRADING_REVENUE"   // Income
    CategoryTradingCosts     = "TRADING_COSTS"     // Expense
    CategoryShipInvestments  = "SHIP_INVESTMENTS"  // Expense
    CategoryContractRevenue  = "CONTRACT_REVENUE"  // Income
)
```

**Profit/Loss Query:** `internal/application/ledger/queries/get_profit_loss.go` (112 lines)
```go
type GetProfitLossResponse struct {
    Period           string
    TotalRevenue     int
    TotalExpenses    int
    NetProfit        int
    RevenueBreakdown map[string]int  // category → amount
    ExpenseBreakdown map[string]int  // category → amount
}
```

**Player Entity:** `internal/domain/player/player.go`
```go
type Player struct {
    ID          shared.PlayerID
    AgentSymbol string
    Credits     int  // Current balance
    // ...
}
```

**Key Findings:**
- ✅ Complete transaction history in database
- ✅ Categorized income/expenses
- ✅ Built-in P&L aggregation query
- ✅ Real-time balance tracking
- **Perfect foundation for financial metrics!**

#### API Client

**File:** `internal/adapters/api/client.go` (200+ lines)

**Client Structure:**
```go
type SpaceTradersClient struct {
    httpClient  *http.Client
    rateLimiter *rate.Limiter    // 2 req/sec, burst 2
    baseURL     string
    maxRetries  int              // Default: 5
    backoffBase time.Duration    // Default: 1s
}
```

**API Features:**
- Rate limiting: 2 requests/second (burst 2)
- Exponential backoff retries (max 5 attempts)
- Automatic 429 (rate limit) handling
- Comprehensive endpoint coverage (20+ endpoints)

**Key Instrumentation Points:**
- Before `httpClient.Do()`: Start timer
- After response: Record duration, status code
- Track rate limiter wait time
- Count retry attempts

#### CQRS Mediator

**File:** `internal/application/common/mediator.go`

**Mediator Pattern:**
```go
response, err := mediator.Send(ctx, command)
```

**Middleware Support:**
```go
med.RegisterMiddleware(common.PlayerTokenMiddleware(playerRepo))
```

**55+ Command/Query Handlers:**
- Ship commands: Navigate, Dock, Orbit, Refuel
- Trading commands: Purchase, Sell
- Contract commands: Accept, Fulfill
- Shipyard commands: PurchaseShip
- Player queries: GetPlayer, ListShips
- Ledger queries: GetProfitLoss, ListTransactions

**Key Opportunity:**
- Add Prometheus middleware to mediator
- **Single point** to track all command/query executions
- Measure handler duration
- Count successes/failures

### Dependency Analysis

**Current Dependencies (go.mod):**
```go
require (
    github.com/google/uuid v1.6.0
    github.com/spf13/viper v1.21.0           // Config management ✅
    golang.org/x/time v0.14.0                // Rate limiting ✅
    google.golang.org/grpc v1.76.0           // gRPC server ✅
    gorm.io/driver/postgres v1.6.0           // Database ✅
    gorm.io/gorm v1.31.1
    github.com/spf13/cobra v1.9.1            // CLI ✅
)
```

**Missing (Need to Add):**
```go
github.com/prometheus/client_golang  // Prometheus client library
```

**Go Version:** `1.24.0` ✅

### Architecture Analysis

**Hexagonal Architecture Layers:**
```
Domain Layer (core business logic)
    ↑ (depends on)
Application Layer (CQRS commands/queries)
    ↑ (depends on)
Adapter Layer (infrastructure implementations)
```

**Dependency Rule:** Dependencies point inward only.

**Metrics Integration Points:**

1. **Adapter Layer (Correct Layer for Metrics):**
   - `internal/adapters/metrics/` ← **NEW DIRECTORY**
   - Prometheus collector implementations
   - NO domain dependencies (only observes)

2. **Application Layer (Instrumentation):**
   - `internal/application/common/prometheus_middleware.go` ← **NEW FILE**
   - Mediator middleware for command/query tracking

3. **Domain Layer (NO CHANGES):**
   - Domain entities remain pure
   - Use existing methods: `RuntimeDuration()`, `TotalFuelRequired()`
   - **ZERO Prometheus imports** (architecture compliance)

**✅ Architecture Compliance Strategy:**
- Metrics code lives in adapter layer
- Domain/application layers unchanged (except middleware)
- Metrics observe events, don't change behavior
- Dependency flow: Adapters → Application → Domain (correct)

---

## Architecture Design

### High-Level Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                     External Systems                         │
│                                                              │
│  ┌──────────────┐              ┌──────────────┐            │
│  │  Prometheus  │◄─────────────│   Grafana    │            │
│  │   (Scraper)  │   Query      │ (Dashboards) │            │
│  └──────┬───────┘              └──────────────┘            │
│         │ Scrape /metrics (15s interval)                    │
└─────────┼──────────────────────────────────────────────────┘
          │
          │ HTTP GET :9090/metrics
          ▼
┌─────────────────────────────────────────────────────────────┐
│               SpaceTraders Daemon Process                    │
│                                                              │
│  ┌──────────────────────────────────────────────────────┐  │
│  │         HTTP Metrics Server (:9090)                  │  │
│  │                                                       │  │
│  │  GET /metrics → promhttp.Handler()                   │  │
│  └──────────────────────┬───────────────────────────────┘  │
│                         │ Reads from                        │
│                         ▼                                   │
│  ┌──────────────────────────────────────────────────────┐  │
│  │       Prometheus Registry (Global Singleton)         │  │
│  │                                                       │  │
│  │  - Container metrics (gauges, counters, histograms)  │  │
│  │  - Navigation metrics (route duration, fuel, etc.)   │  │
│  │  - Financial metrics (balance, P&L, transactions)    │  │
│  │  - API metrics (request count, duration, retries)    │  │
│  │  - Command metrics (mediator middleware)             │  │
│  └──────────────────────┬───────────────────────────────┘  │
│                         │ Updated by                        │
│                         ▼                                   │
│  ┌──────────────────────────────────────────────────────┐  │
│  │         Metrics Collectors (Adapter Layer)           │  │
│  │                                                       │  │
│  │  ┌─────────────────────────────────────────────┐    │  │
│  │  │  ContainerMetricsCollector                  │    │  │
│  │  │  - Polls DaemonServer.containers (10s)      │    │  │
│  │  │  - Queries ContainerRepository              │    │  │
│  │  └─────────────────────────────────────────────┘    │  │
│  │                                                       │  │
│  │  ┌─────────────────────────────────────────────┐    │  │
│  │  │  NavigationMetricsCollector                 │    │  │
│  │  │  - Observes RouteExecutor events            │    │  │
│  │  │  - Tracks fuel consumption                  │    │  │
│  │  └─────────────────────────────────────────────┘    │  │
│  │                                                       │  │
│  │  ┌─────────────────────────────────────────────┐    │  │
│  │  │  FinancialMetricsCollector                  │    │  │
│  │  │  - Polls GetProfitLoss query (60s)          │    │  │
│  │  │  - Observes RecordTransaction command       │    │  │
│  │  └─────────────────────────────────────────────┘    │  │
│  │                                                       │  │
│  │  ┌─────────────────────────────────────────────┐    │  │
│  │  │  APIMetricsCollector                        │    │  │
│  │  │  - Wraps SpaceTradersClient methods         │    │  │
│  │  │  - Tracks request duration, retries         │    │  │
│  │  └─────────────────────────────────────────────┘    │  │
│  └──────────────────────┬───────────────────────────────┘  │
│                         │ Observes                          │
│                         ▼                                   │
│  ┌──────────────────────────────────────────────────────┐  │
│  │           Application Layer (CQRS)                   │  │
│  │                                                       │  │
│  │  ┌─────────────────────────────────────────────┐    │  │
│  │  │  Mediator + PrometheusMiddleware           │    │  │
│  │  │  - Wraps all command/query handlers         │    │  │
│  │  │  - Measures execution duration              │    │  │
│  │  └─────────────────────────────────────────────┘    │  │
│  │                                                       │  │
│  │  ┌─────────────────────────────────────────────┐    │  │
│  │  │  Commands/Queries (55+ handlers)            │    │  │
│  │  │  - NavigateRouteCommand                     │    │  │
│  │  │  - RecordTransactionCommand                 │    │  │
│  │  │  - GetProfitLossQuery                       │    │  │
│  │  └─────────────────────────────────────────────┘    │  │
│  └──────────────────────┬───────────────────────────────┘  │
│                         │ Uses                              │
│                         ▼                                   │
│  ┌──────────────────────────────────────────────────────┐  │
│  │              Domain Layer (Pure Logic)               │  │
│  │                                                       │  │
│  │  - Container entity (state machine, restarts)        │  │
│  │  - Route entity (segments, fuel, distance)           │  │
│  │  - Ship entity (fuel consumption, navigation)        │  │
│  │  - Transaction entity (ledger, categories)           │  │
│  │                                                       │  │
│  │  NO PROMETHEUS IMPORTS ✅                             │  │
│  └──────────────────────────────────────────────────────┘  │
│                                                              │
│  ┌──────────────────────────────────────────────────────┐  │
│  │         gRPC Server (Unix Socket)                    │  │
│  │  - Existing daemon functionality                     │  │
│  │  - ContainerRunner orchestration                     │  │
│  └──────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

### Component Responsibilities

#### 1. HTTP Metrics Server
**File:** `internal/adapters/grpc/daemon_server.go` (modifications)

**Responsibilities:**
- Expose `/metrics` endpoint on port 9090
- Serve Prometheus text format
- Run alongside gRPC server in separate goroutine
- Graceful shutdown on daemon stop

**Implementation:**
```go
type DaemonServer struct {
    // ... existing fields
    metricsServer *http.Server  // NEW
}

func (s *DaemonServer) Start(ctx context.Context) error {
    // Start gRPC server (existing)
    go s.grpcServer.Serve(s.listener)

    // Start HTTP metrics server (NEW)
    if config.Metrics.Enabled {
        go s.startMetricsServer()
    }

    // ... rest of existing code
}

func (s *DaemonServer) startMetricsServer() {
    mux := http.NewServeMux()
    mux.Handle("/metrics", promhttp.Handler())

    s.metricsServer = &http.Server{
        Addr:    fmt.Sprintf(":%d", config.Metrics.Port),
        Handler: mux,
    }

    s.metricsServer.ListenAndServe()
}
```

#### 2. Prometheus Registry
**File:** `internal/adapters/metrics/prometheus_collector.go` (new)

**Responsibilities:**
- Global Prometheus registry (singleton)
- Metric registration
- Namespace/subsystem management
- Collector lifecycle

**Implementation:**
```go
package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
    Registry  *prometheus.Registry
    namespace = "spacetraders"
    subsystem = "daemon"
)

func InitRegistry() {
    Registry = prometheus.NewRegistry()

    // Register collectors
    Registry.MustRegister(NewContainerMetricsCollector())
    Registry.MustRegister(NewNavigationMetricsCollector())
    Registry.MustRegister(NewFinancialMetricsCollector())
    Registry.MustRegister(NewAPIMetricsCollector())
}
```

#### 3. Container Metrics Collector
**File:** `internal/adapters/metrics/container_metrics.go` (new)

**Responsibilities:**
- Track container lifecycle (running/completed/failed counts)
- Monitor container restarts
- Measure container duration
- Poll container state every 10 seconds

**Metrics:**
```go
container_running_total{player_id, container_type}          // Gauge
container_total{player_id, container_type, status}          // Counter
container_duration_seconds{player_id, container_type}       // Histogram
container_restarts_total{player_id, container_type}         // Counter
container_iterations_total{player_id, container_type}       // Counter
```

**Data Sources:**
- `DaemonServer.containers` map (in-memory state)
- `ContainerRepository` (database history)
- `ContainerRunner.RuntimeDuration()` method

#### 4. Navigation Metrics Collector
**File:** `internal/adapters/metrics/navigation_metrics.go` (new)

**Responsibilities:**
- Track route completions/failures
- Measure route duration
- Sum fuel consumption and distance traveled
- Monitor ship state distribution

**Metrics:**
```go
routes_total{player_id, status}                             // Counter
route_duration_seconds{player_id, status}                   // Histogram
route_distance_traveled_total{player_id}                    // Counter
route_fuel_consumed_total{player_id}                        // Counter
route_segments_completed_total{player_id}                   // Counter

fuel_purchased_units_total{player_id, waypoint}             // Counter
fuel_consumed_units_total{player_id, flight_mode}           // Counter
fuel_efficiency_ratio{player_id}                            // Histogram

ships_total{player_id, role, location}                      // Gauge
ship_status_total{player_id, status}                        // Gauge
```

**Data Sources:**
- `RouteExecutor` (observe route execution)
- `Route.RuntimeDuration()`, `TotalDistance()`, `TotalFuelRequired()`
- `Ship.ConsumeFuel()`, `Ship.Refuel()` methods
- `ShipRepository.List()` (periodic poll)

#### 5. Financial Metrics Collector
**File:** `internal/adapters/metrics/financial_metrics.go` (new)

**Responsibilities:**
- Track credits balance
- Monitor transaction volume/amounts
- Calculate profit/loss
- Measure trade profitability

**Metrics:**
```go
player_credits_balance{player_id, agent}                    // Gauge
transactions_total{player_id, type, category}               // Counter
transaction_amount{player_id, type, category}               // Histogram

total_revenue{player_id, category}                          // Gauge
total_expenses{player_id, category}                         // Gauge
net_profit{player_id}                                       // Gauge

trade_profit_per_unit{player_id, good_symbol}               // Histogram
trade_margin_percent{player_id, good_symbol}                // Histogram
```

**Data Sources:**
- `Player.Credits` field
- `RecordTransactionCommand` (observe transaction creation)
- `GetProfitLossQuery` (periodic poll every 60s)
- `Transaction` entity (type, category, amount)

#### 6. API Metrics Collector
**File:** `internal/adapters/metrics/api_metrics.go` (new)

**Responsibilities:**
- Track API request count/duration
- Monitor rate limiting
- Count retry attempts
- Detect error patterns

**Metrics:**
```go
api_requests_total{method, endpoint, status_code}           // Counter
api_request_duration_seconds{method, endpoint}              // Histogram
api_rate_limit_wait_seconds{endpoint}                       // Histogram
api_retries_total{endpoint, reason}                         // Counter
api_errors_total{endpoint, error_type}                      // Counter
```

**Data Sources:**
- `SpaceTradersClient` (wrap HTTP methods)
- `rateLimiter.Wait()` duration
- Retry loop (count attempts, measure backoff)

#### 7. Prometheus Middleware
**File:** `internal/application/common/prometheus_middleware.go` (new)

**Responsibilities:**
- Wrap all command/query handlers
- Measure execution duration
- Count successes/failures
- Integrate with mediator

**Metrics:**
```go
command_duration_seconds{command, status}                   // Histogram
command_total{command, status}                              // Counter
```

**Implementation:**
```go
type PrometheusMiddleware struct {
    next common.Mediator
}

func (m *PrometheusMiddleware) Send(ctx context.Context, request interface{}) (interface{}, error) {
    start := time.Now()
    commandName := reflect.TypeOf(request).Name()

    response, err := m.next.Send(ctx, request)

    duration := time.Since(start).Seconds()
    status := "success"
    if err != nil {
        status = "error"
    }

    commandDuration.WithLabelValues(commandName, status).Observe(duration)
    commandTotal.WithLabelValues(commandName, status).Inc()

    return response, err
}
```

### Data Flow

#### Container Metrics Flow
```
1. ContainerRunner.Start() called
2. Container state transitions: PENDING → RUNNING
3. ContainerMetricsCollector polling (10s interval):
   - Read DaemonServer.containers map
   - Update container_running_total gauge
4. ContainerRunner.execute() completes
5. Container state transitions: RUNNING → COMPLETED
6. Collector detects state change:
   - Decrement container_running_total
   - Increment container_total{status="completed"}
   - Observe container_duration_seconds
```

#### Navigation Metrics Flow
```
1. NavigateRouteCommand executed via mediator
2. RouteExecutor.ExecuteRoute() called
3. For each segment:
   - Ship.ConsumeFuel(amount)
   - NavigationMetricsCollector observes:
     - Increment fuel_consumed_units_total
     - Update fuel_efficiency_ratio
4. Route.CompleteSegment()
5. Collector observes:
   - Increment route_segments_completed_total
6. Route.MarkAsCompleted()
7. Collector observes:
   - Increment routes_total{status="completed"}
   - Observe route_duration_seconds
   - Add to route_distance_traveled_total
   - Add to route_fuel_consumed_total
```

#### Financial Metrics Flow
```
1. RecordTransactionCommand executed
2. Transaction entity created with:
   - Type (SELL_CARGO)
   - Category (TRADING_REVENUE)
   - Amount (+5000 credits)
3. FinancialMetricsCollector observes:
   - Increment transactions_total{type="SELL_CARGO", category="TRADING_REVENUE"}
   - Observe transaction_amount histogram
   - Update player_credits_balance gauge
4. Periodic poll (60s):
   - Execute GetProfitLossQuery
   - Update total_revenue gauges by category
   - Update total_expenses gauges by category
   - Update net_profit gauge
```

---

## Metric Specifications

### Metric Naming Convention

**Format:** `<namespace>_<subsystem>_<name>_<unit>_<type>`

**Example:** `spacetraders_daemon_route_duration_seconds`

**Labels:**
- Use low-cardinality labels (player_id, container_type, status)
- Avoid high-cardinality labels (ship_symbol, waypoint_symbol)
- Aggregate high-cardinality data before exposing

### Operational Metrics

#### Container Metrics

```go
// Current running containers
spacetraders_daemon_container_running_total{player_id="1", container_type="mining_worker"}
Type: Gauge
Labels: player_id, container_type
Source: len(DaemonServer.containers) filtered by type
Update: Poll every 10s

// Container lifecycle events
spacetraders_daemon_container_total{player_id="1", container_type="mining_worker", status="completed"}
Type: Counter
Labels: player_id, container_type, status (completed|failed|stopped)
Source: Container state transitions
Update: On state change

// Container execution duration
spacetraders_daemon_container_duration_seconds{player_id="1", container_type="mining_worker"}
Type: Histogram
Labels: player_id, container_type
Buckets: [1, 5, 10, 30, 60, 300, 600, 1800, 3600]  // seconds
Source: Container.RuntimeDuration()
Update: On container completion

// Container restarts
spacetraders_daemon_container_restarts_total{player_id="1", container_type="mining_worker"}
Type: Counter
Labels: player_id, container_type
Source: Container.RestartCount()
Update: On restart

// Container iterations
spacetraders_daemon_container_iterations_total{player_id="1", container_type="mining_worker"}
Type: Counter
Labels: player_id, container_type
Source: Container.CurrentIteration()
Update: On iteration completion
```

#### Ship Metrics

```go
// Ship count by role/location
spacetraders_daemon_ships_total{player_id="1", role="HAULER", location="X1-C3"}
Type: Gauge
Labels: player_id, role, location
Source: ShipRepository.List() grouped by role/location
Update: Poll every 30s

// Ship state distribution
spacetraders_daemon_ship_status_total{player_id="1", status="docked"}
Type: Gauge
Labels: player_id, status (docked|in_orbit|in_transit)
Source: ShipRepository.List() grouped by nav_status
Update: Poll every 30s
```

### Navigation Metrics

#### Route Metrics

```go
// Route completions/failures
spacetraders_daemon_routes_total{player_id="1", status="completed"}
Type: Counter
Labels: player_id, status (completed|failed|aborted)
Source: Route.MarkAsCompleted() / FailRoute() / AbortRoute()
Update: On route completion

// Route execution duration
spacetraders_daemon_route_duration_seconds{player_id="1", status="completed"}
Type: Histogram
Labels: player_id, status
Buckets: [10, 30, 60, 120, 300, 600, 1200, 1800]  // seconds
Source: Route.RuntimeDuration()
Update: On route completion

// Total distance traveled
spacetraders_daemon_route_distance_traveled_total{player_id="1"}
Type: Counter
Labels: player_id
Source: Route.TotalDistance()
Update: On route completion

// Total fuel consumed
spacetraders_daemon_route_fuel_consumed_total{player_id="1"}
Type: Counter
Labels: player_id
Source: Route.TotalFuelRequired()
Update: On route completion

// Segments completed
spacetraders_daemon_route_segments_completed_total{player_id="1"}
Type: Counter
Labels: player_id
Source: Route.AdvanceToNextSegment()
Update: On segment completion
```

#### Fuel Metrics

```go
// Fuel purchases
spacetraders_daemon_fuel_purchased_units_total{player_id="1", waypoint="X1-C3-STATION"}
Type: Counter
Labels: player_id, waypoint
Source: RefuelShipCommand execution
Update: On refuel

// Fuel consumption by flight mode
spacetraders_daemon_fuel_consumed_units_total{player_id="1", flight_mode="CRUISE"}
Type: Counter
Labels: player_id, flight_mode (CRUISE|DRIFT|BURN|STEALTH)
Source: Ship.ConsumeFuel() in RouteExecutor
Update: On fuel consumption

// Fuel efficiency (distance per fuel unit)
spacetraders_daemon_fuel_efficiency_ratio{player_id="1"}
Type: Histogram
Labels: player_id
Buckets: [0.1, 0.5, 1.0, 2.0, 5.0, 10.0, 20.0]
Calculation: segment.Distance / segment.FuelRequired
Update: On segment completion
```

### Financial Metrics

#### Balance Metrics

```go
// Current credits balance
spacetraders_daemon_player_credits_balance{player_id="1", agent="AGENT-1"}
Type: Gauge
Labels: player_id, agent
Source: Player.Credits
Update: On transaction (RecordTransactionCommand)
```

#### Transaction Metrics

```go
// Transaction count by type/category
spacetraders_daemon_transactions_total{player_id="1", type="SELL_CARGO", category="TRADING_REVENUE"}
Type: Counter
Labels: player_id, type, category
Source: Transaction.TransactionType, Transaction.Category
Update: On RecordTransactionCommand

// Transaction amount distribution
spacetraders_daemon_transaction_amount{player_id="1", type="SELL_CARGO", category="TRADING_REVENUE"}
Type: Histogram
Labels: player_id, type, category
Buckets: [100, 500, 1000, 5000, 10000, 50000, 100000, 500000]
Source: Transaction.Amount (absolute value)
Update: On RecordTransactionCommand
```

#### Profit/Loss Metrics

```go
// Total revenue by category
spacetraders_daemon_total_revenue{player_id="1", category="TRADING_REVENUE"}
Type: Gauge
Labels: player_id, category
Source: GetProfitLossResponse.RevenueBreakdown[category]
Update: Poll every 60s

// Total expenses by category
spacetraders_daemon_total_expenses{player_id="1", category="FUEL_COSTS"}
Type: Gauge
Labels: player_id, category
Source: GetProfitLossResponse.ExpenseBreakdown[category]
Update: Poll every 60s

// Net profit
spacetraders_daemon_net_profit{player_id="1"}
Type: Gauge
Labels: player_id
Source: GetProfitLossResponse.NetProfit
Update: Poll every 60s
```

#### Trade Profitability Metrics

```go
// Profit per unit (buy low, sell high)
spacetraders_daemon_trade_profit_per_unit{player_id="1", good_symbol="IRON_ORE"}
Type: Histogram
Labels: player_id, good_symbol
Buckets: [1, 5, 10, 50, 100, 500, 1000]
Calculation: (sell_price - buy_price)
Source: SellCargoCommand metadata
Update: On cargo sale

// Trade margin percentage
spacetraders_daemon_trade_margin_percent{player_id="1", good_symbol="IRON_ORE"}
Type: Histogram
Labels: player_id, good_symbol
Buckets: [5, 10, 25, 50, 75, 100, 150, 200]
Calculation: ((sell_price - buy_price) / buy_price) * 100
Source: SellCargoCommand metadata
Update: On cargo sale
```

### API & System Metrics

#### API Metrics

```go
// API request count
spacetraders_daemon_api_requests_total{method="POST", endpoint="/my/ships/{symbol}/navigate", status_code="200"}
Type: Counter
Labels: method, endpoint, status_code
Source: SpaceTradersClient HTTP wrapper
Update: On API request completion

// API request duration
spacetraders_daemon_api_request_duration_seconds{method="POST", endpoint="/my/ships/{symbol}/navigate"}
Type: Histogram
Labels: method, endpoint
Buckets: [0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0]
Source: Time between httpClient.Do() start/end
Update: On API request completion

// Rate limit wait time
spacetraders_daemon_api_rate_limit_wait_seconds{endpoint="/my/ships/{symbol}/navigate"}
Type: Histogram
Labels: endpoint
Buckets: [0.1, 0.5, 1.0, 2.0, 5.0]
Source: rateLimiter.Wait() duration
Update: On rate limit wait

// Retry count
spacetraders_daemon_api_retries_total{endpoint="/my/ships/{symbol}/navigate", reason="timeout"}
Type: Counter
Labels: endpoint, reason (timeout|rate_limit|server_error)
Source: Retry loop in SpaceTradersClient
Update: On retry attempt
```

#### Command/Query Metrics

```go
// Command execution duration
spacetraders_daemon_command_duration_seconds{command="NavigateRouteCommand", status="success"}
Type: Histogram
Labels: command, status (success|error)
Buckets: [0.01, 0.05, 0.1, 0.5, 1.0, 5.0, 10.0, 30.0]
Source: PrometheusMiddleware wrapping mediator
Update: On command completion

// Command execution count
spacetraders_daemon_command_total{command="NavigateRouteCommand", status="success"}
Type: Counter
Labels: command, status (success|error)
Source: PrometheusMiddleware wrapping mediator
Update: On command completion
```

---

## Implementation Phases

### Phase 1: Core Infrastructure (2-3 hours)

**Goal:** Establish metrics foundation and HTTP endpoint

**Tasks:**

1. **Add Prometheus dependency**
   - Run: `go get github.com/prometheus/client_golang/prometheus`
   - Run: `go get github.com/prometheus/client_golang/prometheus/promhttp`
   - Update `go.mod` and `go.sum`

2. **Create metrics adapter structure**
   ```
   mkdir -p internal/adapters/metrics
   touch internal/adapters/metrics/prometheus_collector.go
   touch internal/adapters/metrics/container_metrics.go
   ```

3. **Implement Prometheus registry**
   - File: `internal/adapters/metrics/prometheus_collector.go`
   - Create global registry
   - Define namespace/subsystem constants
   - Implement `InitRegistry()` function

4. **Add HTTP metrics server to daemon**
   - File: `internal/adapters/grpc/daemon_server.go`
   - Add `metricsServer *http.Server` field
   - Implement `startMetricsServer()` method
   - Add shutdown to `handleShutdown()`
   - Start in `Start()` if metrics enabled

5. **Add metrics configuration**
   - File: `internal/infrastructure/config/config.go`
   - Add `MetricsConfig` struct
   - Define environment variables (ST_METRICS_*)
   - Load via Viper

6. **Implement container metrics collector**
   - File: `internal/adapters/metrics/container_metrics.go`
   - Define container metrics (gauges, counters, histograms)
   - Implement polling goroutine (10s interval)
   - Read from DaemonServer.containers map
   - Register with Prometheus registry

**Deliverables:**
- ✅ HTTP metrics endpoint running on :9090
- ✅ `/metrics` returns Prometheus text format
- ✅ Basic container metrics exposed
- ✅ Configuration via .env file

**Testing:**
```bash
# Start daemon with metrics enabled
ST_METRICS_ENABLED=true ./bin/spacetraders-daemon

# Verify endpoint
curl http://localhost:9090/metrics | grep spacetraders
```

### Phase 2: Operational Metrics (1-2 hours)

**Goal:** Complete container and ship metrics

**Tasks:**

1. **Enhance container metrics**
   - Track restart count
   - Track iteration count
   - Add container type breakdown
   - Instrument ContainerRunner lifecycle

2. **Implement ship metrics**
   - File: `internal/adapters/metrics/container_metrics.go` (add ship section)
   - Poll ShipRepository.List() every 30s
   - Group by role/location
   - Track ship status distribution (docked/orbit/transit)

3. **Wire metrics to daemon server**
   - Pass DaemonServer reference to collector
   - Access containers map (thread-safe via RWMutex)
   - Access ContainerRepository for historical data

**Deliverables:**
- ✅ Container metrics fully implemented
- ✅ Ship state tracking working
- ✅ Metrics update in real-time

**Testing:**
```bash
# Start a container
./bin/spacetraders container start --type mining_worker

# Check metrics
curl http://localhost:9090/metrics | grep container_running_total
# Expected: spacetraders_daemon_container_running_total{player_id="1",container_type="mining_worker"} 1
```

### Phase 3: Navigation Metrics (1-2 hours)

**Goal:** Track route execution, fuel, and distance

**Tasks:**

1. **Create navigation metrics collector**
   - File: `internal/adapters/metrics/navigation_metrics.go`
   - Define route metrics (duration, distance, fuel)
   - Define fuel metrics (purchases, consumption, efficiency)

2. **Instrument RouteExecutor**
   - File: `internal/application/ship/route_executor.go`
   - Before ExecuteRoute(): Start timer, capture initial fuel
   - After each segment: Track fuel consumed, distance traveled
   - After ExecuteRoute(): Record total metrics
   - Use metrics collector singleton

3. **Observe route lifecycle**
   - Hook into Route.MarkAsCompleted()
   - Hook into Route.FailRoute()
   - Extract metrics via Route methods (TotalDistance, TotalFuelRequired)

4. **Observe fuel events**
   - Hook into Ship.ConsumeFuel()
   - Hook into Ship.Refuel()
   - Track by flight mode

**Deliverables:**
- ✅ Route completion metrics
- ✅ Fuel consumption tracking
- ✅ Distance traveled counter
- ✅ Fuel efficiency histogram

**Testing:**
```bash
# Navigate a ship
./bin/spacetraders ship navigate --ship AGENT-1 --destination X1-C3

# Check metrics
curl http://localhost:9090/metrics | grep route_
# Expected: spacetraders_daemon_routes_total{player_id="1",status="completed"} 1
```

### Phase 4: Financial Metrics (1-2 hours)

**Goal:** Track credits, transactions, and profit/loss

**Tasks:**

1. **Create financial metrics collector**
   - File: `internal/adapters/metrics/financial_metrics.go`
   - Define balance gauge
   - Define transaction counters/histograms
   - Define P&L gauges

2. **Observe transaction creation**
   - Hook into RecordTransactionCommand
   - Extract type, category, amount
   - Update player_credits_balance gauge
   - Increment transaction counters

3. **Implement P&L polling**
   - Create goroutine polling GetProfitLossQuery every 60s
   - Update revenue gauges by category
   - Update expense gauges by category
   - Update net_profit gauge

4. **Track trade profitability (optional)**
   - Hook into SellCargoCommand
   - Calculate profit per unit (sell - buy)
   - Calculate margin percentage
   - Extract from transaction metadata

**Deliverables:**
- ✅ Credits balance tracking
- ✅ Transaction volume metrics
- ✅ P&L aggregation
- ✅ Trade profitability (optional)

**Testing:**
```bash
# Execute a transaction (e.g., refuel)
./bin/spacetraders ship refuel --ship AGENT-1

# Check metrics
curl http://localhost:9090/metrics | grep player_credits
# Expected: spacetraders_daemon_player_credits_balance{player_id="1",agent="AGENT-1"} 150000

curl http://localhost:9090/metrics | grep transactions_total
# Expected: spacetraders_daemon_transactions_total{player_id="1",type="REFUEL",category="FUEL_COSTS"} 1
```

### Phase 5: Mediator & API Instrumentation (1 hour)

**Goal:** Track command performance and API health

**Tasks:**

1. **Implement Prometheus middleware**
   - File: `internal/application/common/prometheus_middleware.go`
   - Wrap mediator.Send()
   - Measure command duration
   - Count successes/failures
   - Extract command name via reflection

2. **Register middleware**
   - File: Application setup (where mediator is created)
   - Add PrometheusMiddleware after existing middleware
   - Ensure proper chaining

3. **Instrument API client**
   - File: `internal/adapters/api/client.go`
   - Wrap httpClient.Do() calls
   - Track request duration
   - Track status codes
   - Track rate limit wait time
   - Track retry attempts

4. **Create API metrics collector**
   - File: `internal/adapters/metrics/api_metrics.go`
   - Define API metrics (requests, duration, retries)
   - Register with Prometheus

**Deliverables:**
- ✅ Command execution metrics
- ✅ API request tracking
- ✅ Rate limiting visibility
- ✅ Retry monitoring

**Testing:**
```bash
# Execute commands
./bin/spacetraders ship navigate --ship AGENT-1 --destination X1-C3

# Check command metrics
curl http://localhost:9090/metrics | grep command_duration
# Expected: spacetraders_daemon_command_duration_seconds{command="NavigateRouteCommand",status="success"}

# Check API metrics
curl http://localhost:9090/metrics | grep api_requests_total
# Expected: spacetraders_daemon_api_requests_total{method="POST",endpoint="/my/ships/{symbol}/navigate",status_code="200"} 1
```

### Phase 6: Grafana Setup (1-2 hours)

**Goal:** Deploy Prometheus/Grafana and create dashboards

**Tasks:**

1. **Create Docker Compose stack**
   - File: `docker-compose.metrics.yml`
   - Prometheus container (port 9090)
   - Grafana container (port 3000)
   - Volume mounts for persistence
   - Network configuration

2. **Create Prometheus config**
   - File: `configs/prometheus/prometheus.yml`
   - Scrape daemon at `host.docker.internal:9090` every 15s
   - Add job labels

3. **Create Grafana provisioning**
   - File: `configs/grafana/provisioning/datasources/prometheus.yml`
   - Auto-configure Prometheus datasource
   - File: `configs/grafana/provisioning/dashboards/dashboards.yml`
   - Auto-load dashboard JSON files

4. **Create Operational Dashboard**
   - File: `configs/grafana/dashboards/operational.json`
   - Panels:
     - Container running count (gauge)
     - Container completions/failures (graph)
     - Container duration percentiles (heatmap)
     - Ship status distribution (pie chart)
     - Container restarts (alert)

5. **Create Navigation Dashboard**
   - File: `configs/grafana/dashboards/navigation.json`
   - Panels:
     - Route completion rate (graph)
     - Route duration percentiles (heatmap)
     - Fuel consumption rate (graph)
     - Distance traveled (counter)
     - Fuel efficiency trend (graph)

6. **Create Financial Dashboard**
   - File: `configs/grafana/dashboards/financial.json`
   - Panels:
     - Credits balance (time series)
     - Revenue vs. Expenses (stacked graph)
     - Net profit trend (graph)
     - Transaction volume by category (bar chart)
     - Trade profitability (histogram)

**Deliverables:**
- ✅ Docker Compose stack running
- ✅ Prometheus scraping daemon
- ✅ Grafana dashboards auto-provisioned
- ✅ All metrics visualized

**Testing:**
```bash
# Start stack
docker-compose -f docker-compose.metrics.yml up -d

# Verify Prometheus
open http://localhost:9090/targets
# Expected: "spacetraders-daemon" target UP

# Verify Grafana
open http://localhost:3000
# Login: admin/admin
# Navigate to Dashboards → Operational
```

### Phase 7: Documentation (30 minutes)

**Goal:** Document metrics system for future maintainers

**Tasks:**

1. **Update CLAUDE.md**
   - Add "Metrics & Monitoring" section
   - Document architecture
   - List all metrics with descriptions
   - Explain Grafana dashboard usage

2. **Create METRICS.md** (optional)
   - Deep dive into metrics system
   - Metric naming conventions
   - Adding new metrics guide
   - Troubleshooting

3. **Update README.md** (if exists)
   - Add quick start for metrics
   - Include dashboard screenshots
   - Link to Grafana

**Deliverables:**
- ✅ Documentation updated
- ✅ Examples provided
- ✅ Troubleshooting guide

---

## Technical Design Details

### Metrics Collector Lifecycle

#### Initialization Sequence

```go
// File: cmd/spacetraders-daemon/main.go

func main() {
    // 1. Load configuration
    cfg := config.LoadConfig()

    // 2. Initialize metrics registry (if enabled)
    if cfg.Metrics.Enabled {
        metrics.InitRegistry()
    }

    // 3. Create application dependencies
    mediator := createMediator(cfg)
    daemonServer := grpc.NewDaemonServer(mediator, ...)

    // 4. Start metrics collectors (if enabled)
    if cfg.Metrics.Enabled {
        metrics.StartCollectors(daemonServer, mediator)
    }

    // 5. Start daemon server (gRPC + HTTP metrics)
    daemonServer.Start(ctx)
}
```

#### Collector Goroutines

**Container Metrics Collector:**
```go
func (c *ContainerMetricsCollector) Start(ctx context.Context) {
    ticker := time.NewTicker(10 * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            c.updateContainerMetrics()
        }
    }
}

func (c *ContainerMetricsCollector) updateContainerMetrics() {
    // Read DaemonServer.containers map (thread-safe)
    c.daemonServer.containersMu.RLock()
    containers := c.daemonServer.containers
    c.daemonServer.containersMu.RUnlock()

    // Reset gauges (to handle container removal)
    c.containerRunningTotal.Reset()

    // Update gauges
    for _, runner := range containers {
        container := runner.Container()
        c.containerRunningTotal.WithLabelValues(
            strconv.Itoa(container.PlayerID()),
            string(container.Type()),
        ).Set(1)
    }
}
```

**Financial Metrics Collector:**
```go
func (c *FinancialMetricsCollector) Start(ctx context.Context) {
    ticker := time.NewTicker(60 * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            c.updateProfitLoss()
        }
    }
}

func (c *FinancialMetricsCollector) updateProfitLoss() {
    // Execute GetProfitLossQuery for each player
    for _, playerID := range c.getPlayerIDs() {
        query := queries.GetProfitLossQuery{PlayerID: playerID}
        response, err := c.mediator.Send(context.Background(), query)
        if err != nil {
            log.Printf("Failed to get profit/loss: %v", err)
            continue
        }

        pl := response.(*queries.GetProfitLossResponse)

        // Update revenue gauges
        for category, amount := range pl.RevenueBreakdown {
            c.totalRevenue.WithLabelValues(
                strconv.Itoa(playerID),
                category,
            ).Set(float64(amount))
        }

        // Update expense gauges
        for category, amount := range pl.ExpenseBreakdown {
            c.totalExpenses.WithLabelValues(
                strconv.Itoa(playerID),
                category,
            ).Set(float64(amount))
        }

        // Update net profit
        c.netProfit.WithLabelValues(
            strconv.Itoa(playerID),
        ).Set(float64(pl.NetProfit))
    }
}
```

#### Graceful Shutdown

```go
func (s *DaemonServer) handleShutdown() {
    // 1. Stop accepting new requests
    s.grpcServer.GracefulStop()

    // 2. Stop metrics HTTP server
    if s.metricsServer != nil {
        ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        s.metricsServer.Shutdown(ctx)
    }

    // 3. Stop metrics collectors (via context cancellation)
    s.cancelFunc()  // Cancels collector goroutines

    // 4. Wait for in-flight containers (existing logic)
    // ...
}
```

### Metric Instrumentation Patterns

#### Event-Based Instrumentation

**Pattern:** Observe domain events without changing behavior

**Example: Route Completion**
```go
// File: internal/application/ship/route_executor.go

func (e *RouteExecutor) ExecuteRoute(ctx context.Context, route *navigation.Route, ship *navigation.Ship, playerID int) error {
    start := time.Now()
    initialFuel := ship.Fuel().Current()

    // Execute route (existing logic)
    err := e.executeRouteSegments(ctx, route, ship, playerID)

    // Instrument metrics (NEW)
    if err == nil {
        metrics.RecordRouteCompletion(
            playerID,
            route.TotalDistance(),
            route.TotalFuelRequired(),
            time.Since(start),
        )
    } else {
        metrics.RecordRouteFailure(playerID)
    }

    return err
}
```

**Collector Implementation:**
```go
// File: internal/adapters/metrics/navigation_metrics.go

func RecordRouteCompletion(playerID int, distance float64, fuel int, duration time.Duration) {
    labels := prometheus.Labels{"player_id": strconv.Itoa(playerID), "status": "completed"}

    routesTotal.With(labels).Inc()
    routeDuration.With(labels).Observe(duration.Seconds())
    routeDistanceTraveled.WithLabelValues(strconv.Itoa(playerID)).Add(distance)
    routeFuelConsumed.WithLabelValues(strconv.Itoa(playerID)).Add(float64(fuel))
}
```

#### Middleware Instrumentation

**Pattern:** Wrap handler with metrics tracking

**Example: Prometheus Middleware**
```go
// File: internal/application/common/prometheus_middleware.go

type PrometheusMiddleware struct {
    next common.Mediator
}

func (m *PrometheusMiddleware) Send(ctx context.Context, request interface{}) (interface{}, error) {
    // Extract command name
    commandName := reflect.TypeOf(request).Elem().Name()

    // Start timer
    start := time.Now()

    // Execute command
    response, err := m.next.Send(ctx, request)

    // Record metrics
    duration := time.Since(start).Seconds()
    status := "success"
    if err != nil {
        status = "error"
    }

    commandDuration.WithLabelValues(commandName, status).Observe(duration)
    commandTotal.WithLabelValues(commandName, status).Inc()

    return response, err
}

func (m *PrometheusMiddleware) RegisterHandler(handler interface{}) {
    m.next.RegisterHandler(handler)
}

func (m *PrometheusMiddleware) RegisterMiddleware(middleware common.Middleware) {
    m.next.RegisterMiddleware(middleware)
}
```

**Registration:**
```go
// File: Application setup

mediator := common.NewMediator()
mediator.RegisterMiddleware(common.PlayerTokenMiddleware(playerRepo))
mediator.RegisterMiddleware(common.NewPrometheusMiddleware(mediator))  // NEW
```

#### Polling Instrumentation

**Pattern:** Periodically query state and update gauges

**Example: Ship Status Tracking**
```go
func (c *ContainerMetricsCollector) updateShipStatus() {
    // Query all ships
    ships, err := c.shipRepo.List(context.Background())
    if err != nil {
        log.Printf("Failed to list ships: %v", err)
        return
    }

    // Reset gauges
    c.shipStatusTotal.Reset()

    // Group by status
    statusCounts := make(map[string]int)
    for _, ship := range ships {
        status := string(ship.NavStatus())
        statusCounts[status]++
    }

    // Update gauges
    for status, count := range statusCounts {
        c.shipStatusTotal.WithLabelValues(
            strconv.Itoa(ships[0].PlayerID()),  // Assumes single player
            status,
        ).Set(float64(count))
    }
}
```

### Label Cardinality Management

#### Low Cardinality (Safe)

```go
// player_id: 1-100 unique values
// container_type: 16 unique values
// Total combinations: 1,600

container_running_total{player_id="1", container_type="mining_worker"}
```

#### High Cardinality (Avoid)

```go
// ship_symbol: 100+ unique values
// waypoint_symbol: 10,000+ unique values
// Total combinations: 1,000,000+

// ❌ WRONG: High cardinality explosion
routes_total{player_id="1", ship_symbol="AGENT-1", from="X1-A1", to="X1-B2"}

// ✅ RIGHT: Aggregate by player
routes_total{player_id="1", status="completed"}
```

#### Aggregation Strategy

**Problem:** Need ship-level metrics without high cardinality

**Solution:** Aggregate in application layer, expose summary metrics

```go
// Track per-ship in memory (not exposed)
type ShipMetrics struct {
    ShipSymbol     string
    RoutesExecuted int
    FuelConsumed   int
}

// Expose aggregated metrics
ships_total{player_id="1", role="HAULER", location="X1-C3"}  // Grouped by role/location
```

---

## Grafana Dashboard Design

### Dashboard 1: Operational Overview

**Purpose:** Monitor system health and container operations

**Layout:**
```
┌─────────────────────────────────────────────────────────────┐
│  Operational Dashboard                          [Refresh: 5s]│
├─────────────────────────────────────────────────────────────┤
│ ┌─────────────┐ ┌─────────────┐ ┌─────────────┐            │
│ │   Running   │ │  Completed  │ │   Failed    │            │
│ │  Containers │ │  (24h)      │ │   (24h)     │            │
│ │     15      │ │    342      │ │      3      │  [Stats]   │
│ └─────────────┘ └─────────────┘ └─────────────┘            │
├─────────────────────────────────────────────────────────────┤
│ Container Lifecycle (Last 6 hours)                          │
│ ┌─────────────────────────────────────────────────────────┐ │
│ │ Completed ▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓          │ │
│ │ Failed    ▒▒▒▒                                           │ │
│ │ Stopped   ░░                                             │ │
│ └─────────────────────────────────────────────────────────┘ │
├─────────────────────────────────────────────────────────────┤
│ Container Distribution by Type         Ship Status          │
│ ┌─────────────────┐                    ┌──────────────┐    │
│ │ Mining: 40%     │                    │ Docked: 65%  │    │
│ │ Trading: 30%    │  [Pie Chart]       │ Orbit: 25%   │    │
│ │ Transport: 20%  │                    │ Transit: 10% │    │
│ │ Scout: 10%      │                    └──────────────┘    │
│ └─────────────────┘                                         │
├─────────────────────────────────────────────────────────────┤
│ Container Duration Percentiles (p50, p95, p99)              │
│ ┌─────────────────────────────────────────────────────────┐ │
│ │         p50 ─────────────────────────────────            │ │
│ │         p95 ─────────────────────────────                │ │
│ │         p99 ─────────────────────                        │ │
│ └─────────────────────────────────────────────────────────┘ │
├─────────────────────────────────────────────────────────────┤
│ Container Restarts (Alert if > 5/hour)                      │
│ ┌─────────────────────────────────────────────────────────┐ │
│ │ Restarts  ▲                                              │ │
│ │          ▲│▲                                              │ │
│ │         ▲ │ ▲                                             │ │
│ └─────────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────┘
```

**Panels:**

1. **Running Containers (Stat)**
   - Query: `spacetraders_daemon_container_running_total`
   - Aggregation: `sum(spacetraders_daemon_container_running_total)`
   - Thresholds: Green < 20, Yellow < 50, Red >= 50

2. **Completed/Failed (Stat)**
   - Query: `increase(spacetraders_daemon_container_total{status="completed"}[24h])`
   - Query: `increase(spacetraders_daemon_container_total{status="failed"}[24h])`

3. **Container Lifecycle (Time Series)**
   - Query: `rate(spacetraders_daemon_container_total[5m])`
   - Group by: `status`
   - Stack: True

4. **Container Distribution (Pie Chart)**
   - Query: `sum(spacetraders_daemon_container_running_total) by (container_type)`

5. **Ship Status (Pie Chart)**
   - Query: `sum(spacetraders_daemon_ship_status_total) by (status)`

6. **Container Duration Percentiles (Heatmap)**
   - Query: `histogram_quantile(0.50, rate(spacetraders_daemon_container_duration_seconds_bucket[5m]))`
   - Query: `histogram_quantile(0.95, rate(spacetraders_daemon_container_duration_seconds_bucket[5m]))`
   - Query: `histogram_quantile(0.99, rate(spacetraders_daemon_container_duration_seconds_bucket[5m]))`

7. **Container Restarts (Time Series + Alert)**
   - Query: `rate(spacetraders_daemon_container_restarts_total[1h])`
   - Alert: `sum(rate(spacetraders_daemon_container_restarts_total[1h])) > 5`

### Dashboard 2: Navigation Performance

**Purpose:** Track route execution and fuel efficiency

**Layout:**
```
┌─────────────────────────────────────────────────────────────┐
│  Navigation Dashboard                       [Refresh: 10s]  │
├─────────────────────────────────────────────────────────────┤
│ ┌─────────────┐ ┌─────────────┐ ┌─────────────┐            │
│ │   Routes    │ │  Distance   │ │    Fuel     │            │
│ │  Completed  │ │  Traveled   │ │  Consumed   │            │
│ │     247     │ │  15,432 km  │ │  8,234 u    │  [Stats]   │
│ └─────────────┘ └─────────────┘ └─────────────┘            │
├─────────────────────────────────────────────────────────────┤
│ Route Completion Rate (Last 24h)                            │
│ ┌─────────────────────────────────────────────────────────┐ │
│ │ Success  ▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓           │ │
│ │ Failed   ▒▒▒                                             │ │
│ │ Aborted  ░                                               │ │
│ └─────────────────────────────────────────────────────────┘ │
├─────────────────────────────────────────────────────────────┤
│ Route Duration (Percentiles)       Fuel Efficiency          │
│ ┌─────────────────┐                ┌──────────────┐        │
│ │ p50: 45s        │                │ 2.5 km/unit  │        │
│ │ p95: 120s       │  [Histogram]   │ [Trend ↗]    │        │
│ │ p99: 180s       │                └──────────────┘        │
│ └─────────────────┘                                         │
├─────────────────────────────────────────────────────────────┤
│ Fuel Consumption by Flight Mode                             │
│ ┌─────────────────────────────────────────────────────────┐ │
│ │ CRUISE ▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓                              │ │
│ │ BURN   ▓▓▓▓▓▓▓▓▓▓                                        │ │
│ │ DRIFT  ▓▓▓▓▓                                             │ │
│ │ STEALTH▓                                                 │ │
│ └─────────────────────────────────────────────────────────┘ │
├─────────────────────────────────────────────────────────────┤
│ Distance Traveled Over Time (Cumulative)                    │
│ ┌─────────────────────────────────────────────────────────┐ │
│ │          ┌────────────────                               │ │
│ │      ┌───┘                                               │ │
│ │  ┌───┘                                                   │ │
│ └─────────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────┘
```

**Panels:**

1. **Routes Completed (Stat)**
   - Query: `increase(spacetraders_daemon_routes_total{status="completed"}[24h])`

2. **Distance Traveled (Stat)**
   - Query: `spacetraders_daemon_route_distance_traveled_total`
   - Unit: km

3. **Fuel Consumed (Stat)**
   - Query: `spacetraders_daemon_route_fuel_consumed_total`
   - Unit: units

4. **Route Completion Rate (Time Series)**
   - Query: `rate(spacetraders_daemon_routes_total[5m])`
   - Group by: `status`

5. **Route Duration Percentiles (Stat)**
   - Query: `histogram_quantile(0.50, rate(spacetraders_daemon_route_duration_seconds_bucket[5m]))`
   - Query: `histogram_quantile(0.95, rate(spacetraders_daemon_route_duration_seconds_bucket[5m]))`
   - Query: `histogram_quantile(0.99, rate(spacetraders_daemon_route_duration_seconds_bucket[5m]))`

6. **Fuel Efficiency (Gauge + Trend)**
   - Query: `spacetraders_daemon_route_distance_traveled_total / spacetraders_daemon_route_fuel_consumed_total`
   - Unit: km/unit

7. **Fuel Consumption by Flight Mode (Bar Gauge)**
   - Query: `sum(spacetraders_daemon_fuel_consumed_units_total) by (flight_mode)`

8. **Distance Traveled (Time Series, Cumulative)**
   - Query: `spacetraders_daemon_route_distance_traveled_total`

### Dashboard 3: Financial Overview

**Purpose:** Monitor credits, revenue, expenses, and profitability

**Layout:**
```
┌─────────────────────────────────────────────────────────────┐
│  Financial Dashboard                        [Refresh: 60s]  │
├─────────────────────────────────────────────────────────────┤
│ ┌─────────────┐ ┌─────────────┐ ┌─────────────┐            │
│ │   Balance   │ │   Revenue   │ │  Expenses   │            │
│ │  245,000 cr │ │  +12,500 cr │ │  -8,300 cr  │  [Stats]   │
│ └─────────────┘ └─────────────┘ └─────────────┘            │
├─────────────────────────────────────────────────────────────┤
│ Credits Balance Over Time                                   │
│ ┌─────────────────────────────────────────────────────────┐ │
│ │ Balance   ┌──────────────────                            │ │
│ │       ┌───┘                                              │ │
│ │   ┌───┘                                                  │ │
│ └─────────────────────────────────────────────────────────┘ │
├─────────────────────────────────────────────────────────────┤
│ Revenue vs. Expenses (Stacked)     Net Profit Trend         │
│ ┌─────────────────┐                ┌──────────────┐        │
│ │ Revenue ▲       │                │   ┌────┐     │        │
│ │ Expenses▼       │  [Graph]       │ ┌─┘    └─┐  │        │
│ │                 │                │─┘        └─ │        │
│ └─────────────────┘                └──────────────┘        │
├─────────────────────────────────────────────────────────────┤
│ Revenue Breakdown by Category                               │
│ ┌─────────────────────────────────────────────────────────┐ │
│ │ Trading:  65% ▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓                │ │
│ │ Contracts:30% ▓▓▓▓▓▓▓▓▓▓▓▓                              │ │
│ │ Other:     5% ▓▓                                         │ │
│ └─────────────────────────────────────────────────────────┘ │
├─────────────────────────────────────────────────────────────┤
│ Expense Breakdown by Category                               │
│ ┌─────────────────────────────────────────────────────────┐ │
│ │ Fuel:     45% ▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓                        │ │
│ │ Trading:  40% ▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓                          │ │
│ │ Ships:    15% ▓▓▓▓▓▓                                    │ │
│ └─────────────────────────────────────────────────────────┘ │
├─────────────────────────────────────────────────────────────┤
│ Trade Profitability by Good (Top 10)                        │
│ ┌─────────────────────────────────────────────────────────┐ │
│ │ IRON_ORE     ████████████████████ 120 cr/unit           │ │
│ │ COPPER       ███████████████ 85 cr/unit                 │ │
│ │ FUEL         ████████ 45 cr/unit                        │ │
│ └─────────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────┘
```

**Panels:**

1. **Credits Balance (Stat + Trend)**
   - Query: `spacetraders_daemon_player_credits_balance`
   - Unit: credits

2. **Revenue (Stat)**
   - Query: `sum(spacetraders_daemon_total_revenue)`

3. **Expenses (Stat)**
   - Query: `sum(spacetraders_daemon_total_expenses)`

4. **Credits Balance Over Time (Time Series)**
   - Query: `spacetraders_daemon_player_credits_balance`

5. **Revenue vs. Expenses (Time Series, Stacked)**
   - Query: `sum(spacetraders_daemon_total_revenue) by (category)`
   - Query: `-sum(spacetraders_daemon_total_expenses) by (category)`
   - Stack: True

6. **Net Profit Trend (Time Series)**
   - Query: `spacetraders_daemon_net_profit`

7. **Revenue Breakdown (Pie Chart)**
   - Query: `sum(spacetraders_daemon_total_revenue) by (category)`

8. **Expense Breakdown (Pie Chart)**
   - Query: `sum(spacetraders_daemon_total_expenses) by (category)`

9. **Trade Profitability (Bar Gauge)**
   - Query: `histogram_quantile(0.50, rate(spacetraders_daemon_trade_profit_per_unit_bucket[24h]))`
   - Group by: `good_symbol`
   - Sort: Descending
   - Limit: 10

---

## Configuration & Deployment

### Environment Variables

**File:** `.env` (add to existing configuration)

```bash
# Metrics Configuration
# ---------------------

# Enable Prometheus metrics (default: false)
ST_METRICS_ENABLED=true

# HTTP metrics server port (default: 9090)
ST_METRICS_PORT=9090

# Metrics endpoint path (default: /metrics)
ST_METRICS_PATH=/metrics

# Prometheus namespace (default: spacetraders)
ST_METRICS_NAMESPACE=spacetraders

# Prometheus subsystem (default: daemon)
ST_METRICS_SUBSYSTEM=daemon

# Metrics Collection Intervals
# -----------------------------

# Container state poll interval (default: 10s)
ST_METRICS_CONTAINER_POLL_INTERVAL=10s

# Ship state poll interval (default: 30s)
ST_METRICS_SHIP_POLL_INTERVAL=30s

# Financial data poll interval (default: 60s)
ST_METRICS_FINANCIAL_POLL_INTERVAL=60s
```

### Configuration Code

**File:** `internal/infrastructure/config/config.go` (additions)

```go
type Config struct {
    // ... existing fields
    Metrics MetricsConfig
}

type MetricsConfig struct {
    Enabled               bool
    Port                  int
    Path                  string
    Namespace             string
    Subsystem             string
    ContainerPollInterval time.Duration
    ShipPollInterval      time.Duration
    FinancialPollInterval time.Duration
}

func LoadConfig() *Config {
    viper.SetDefault("metrics.enabled", false)
    viper.SetDefault("metrics.port", 9090)
    viper.SetDefault("metrics.path", "/metrics")
    viper.SetDefault("metrics.namespace", "spacetraders")
    viper.SetDefault("metrics.subsystem", "daemon")
    viper.SetDefault("metrics.container_poll_interval", "10s")
    viper.SetDefault("metrics.ship_poll_interval", "30s")
    viper.SetDefault("metrics.financial_poll_interval", "60s")

    viper.BindEnv("metrics.enabled", "ST_METRICS_ENABLED")
    viper.BindEnv("metrics.port", "ST_METRICS_PORT")
    viper.BindEnv("metrics.path", "ST_METRICS_PATH")
    viper.BindEnv("metrics.namespace", "ST_METRICS_NAMESPACE")
    viper.BindEnv("metrics.subsystem", "ST_METRICS_SUBSYSTEM")
    viper.BindEnv("metrics.container_poll_interval", "ST_METRICS_CONTAINER_POLL_INTERVAL")
    viper.BindEnv("metrics.ship_poll_interval", "ST_METRICS_SHIP_POLL_INTERVAL")
    viper.BindEnv("metrics.financial_poll_interval", "ST_METRICS_FINANCIAL_POLL_INTERVAL")

    // ... existing config loading

    return &Config{
        // ... existing fields
        Metrics: MetricsConfig{
            Enabled:               viper.GetBool("metrics.enabled"),
            Port:                  viper.GetInt("metrics.port"),
            Path:                  viper.GetString("metrics.path"),
            Namespace:             viper.GetString("metrics.namespace"),
            Subsystem:             viper.GetString("metrics.subsystem"),
            ContainerPollInterval: viper.GetDuration("metrics.container_poll_interval"),
            ShipPollInterval:      viper.GetDuration("metrics.ship_poll_interval"),
            FinancialPollInterval: viper.GetDuration("metrics.financial_poll_interval"),
        },
    }
}
```

### Docker Compose Stack

**File:** `docker-compose.metrics.yml`

```yaml
version: '3.8'

services:
  prometheus:
    image: prom/prometheus:latest
    container_name: spacetraders-prometheus
    ports:
      - "9091:9090"  # Expose on 9091 to avoid conflict with daemon
    volumes:
      - ./configs/prometheus/prometheus.yml:/etc/prometheus/prometheus.yml:ro
      - prometheus-data:/prometheus
    command:
      - '--config.file=/etc/prometheus/prometheus.yml'
      - '--storage.tsdb.path=/prometheus'
      - '--web.console.libraries=/usr/share/prometheus/console_libraries'
      - '--web.console.templates=/usr/share/prometheus/consoles'
      - '--web.enable-lifecycle'
    restart: unless-stopped
    networks:
      - monitoring

  grafana:
    image: grafana/grafana:latest
    container_name: spacetraders-grafana
    ports:
      - "3000:3000"
    environment:
      - GF_SECURITY_ADMIN_USER=admin
      - GF_SECURITY_ADMIN_PASSWORD=admin
      - GF_USERS_ALLOW_SIGN_UP=false
    volumes:
      - ./configs/grafana/provisioning:/etc/grafana/provisioning:ro
      - ./configs/grafana/dashboards:/var/lib/grafana/dashboards:ro
      - grafana-data:/var/lib/grafana
    restart: unless-stopped
    networks:
      - monitoring
    depends_on:
      - prometheus

volumes:
  prometheus-data:
  grafana-data:

networks:
  monitoring:
    driver: bridge
```

### Prometheus Configuration

**File:** `configs/prometheus/prometheus.yml`

```yaml
global:
  scrape_interval: 15s
  evaluation_interval: 15s
  external_labels:
    cluster: 'spacetraders-local'
    environment: 'development'

scrape_configs:
  - job_name: 'spacetraders-daemon'
    static_configs:
      - targets: ['host.docker.internal:9090']  # macOS/Windows
        # - targets: ['172.17.0.1:9090']        # Linux
    scrape_interval: 15s
    scrape_timeout: 10s
    metrics_path: '/metrics'
    scheme: 'http'
```

### Grafana Provisioning

**File:** `configs/grafana/provisioning/datasources/prometheus.yml`

```yaml
apiVersion: 1

datasources:
  - name: Prometheus
    type: prometheus
    access: proxy
    url: http://prometheus:9090
    isDefault: true
    editable: false
    jsonData:
      timeInterval: "15s"
```

**File:** `configs/grafana/provisioning/dashboards/dashboards.yml`

```yaml
apiVersion: 1

providers:
  - name: 'SpaceTraders'
    orgId: 1
    folder: 'SpaceTraders Bot'
    type: file
    disableDeletion: false
    updateIntervalSeconds: 10
    allowUiUpdates: true
    options:
      path: /var/lib/grafana/dashboards
      foldersFromFilesStructure: false
```

### Deployment Steps

1. **Start Daemon with Metrics:**
   ```bash
   # Enable metrics in .env
   echo "ST_METRICS_ENABLED=true" >> .env

   # Start daemon
   ./bin/spacetraders-daemon

   # Verify metrics endpoint
   curl http://localhost:9090/metrics
   ```

2. **Start Monitoring Stack:**
   ```bash
   # Start Prometheus + Grafana
   docker-compose -f docker-compose.metrics.yml up -d

   # Check Prometheus targets
   open http://localhost:9091/targets

   # Check Grafana
   open http://localhost:3000  # admin/admin
   ```

3. **Import Dashboards:**
   - Dashboards auto-provisioned from `configs/grafana/dashboards/`
   - Navigate to Dashboards → Browse → SpaceTraders Bot folder

---

## Testing Strategy

### Unit Tests

**Not Required (BDD Tests Only)**

Per project conventions, all tests must be BDD-style in `test/bdd/`.

### Integration Tests (BDD)

**File:** `test/bdd/features/adapters/metrics/container_metrics.feature`

```gherkin
Feature: Container Metrics Collection
  As a bot operator
  I want to track container lifecycle metrics
  So that I can monitor operational health

  Background:
    Given metrics are enabled
    And the daemon is running
    And the Prometheus registry is initialized

  Scenario: Track running containers
    Given a container of type "mining_worker" is started
    When I query the metrics endpoint
    Then I should see "spacetraders_daemon_container_running_total{container_type=\"mining_worker\"} 1"

  Scenario: Track container completion
    Given a container of type "mining_worker" is running
    When the container completes successfully
    And I query the metrics endpoint
    Then I should see "spacetraders_daemon_container_total{container_type=\"mining_worker\",status=\"completed\"} 1"
    And I should see "spacetraders_daemon_container_running_total{container_type=\"mining_worker\"} 0"

  Scenario: Track container duration
    Given a container of type "mining_worker" runs for 30 seconds
    When the container completes successfully
    And I query the metrics endpoint
    Then the "spacetraders_daemon_container_duration_seconds" histogram should have observations
```

**File:** `test/bdd/features/adapters/metrics/navigation_metrics.feature`

```gherkin
Feature: Navigation Metrics Collection
  As a bot operator
  I want to track route execution metrics
  So that I can optimize navigation efficiency

  Scenario: Track route completion
    Given a route from "X1-A1" to "X1-B2" with distance 100 and fuel 50
    When the route is executed successfully
    And I query the metrics endpoint
    Then I should see "spacetraders_daemon_routes_total{status=\"completed\"} 1"
    And I should see "spacetraders_daemon_route_distance_traveled_total" increased by 100
    And I should see "spacetraders_daemon_route_fuel_consumed_total" increased by 50

  Scenario: Track fuel efficiency
    Given a route segment with distance 100 and fuel 25
    When the segment is completed
    Then the fuel efficiency ratio should be 4.0 km/unit
```

**File:** `test/bdd/features/adapters/metrics/financial_metrics.feature`

```gherkin
Feature: Financial Metrics Collection
  As a bot operator
  I want to track financial metrics
  So that I can monitor profitability

  Scenario: Track credits balance
    Given the player has 100,000 credits
    When a transaction of -5,000 credits is recorded
    And I query the metrics endpoint
    Then I should see "spacetraders_daemon_player_credits_balance 95000"

  Scenario: Track transaction volume
    Given no transactions have been recorded
    When a "REFUEL" transaction of -500 credits is recorded
    And I query the metrics endpoint
    Then I should see "spacetraders_daemon_transactions_total{type=\"REFUEL\",category=\"FUEL_COSTS\"} 1"

  Scenario: Track profit/loss
    Given the profit/loss query returns net profit of 10,000 credits
    When the financial metrics collector polls
    Then I should see "spacetraders_daemon_net_profit 10000"
```

### Manual Testing Checklist

**Phase 1: Core Infrastructure**
- [ ] Metrics endpoint accessible at http://localhost:9090/metrics
- [ ] Prometheus text format valid (run through promtool)
- [ ] Metrics disabled by default (ST_METRICS_ENABLED=false)
- [ ] Graceful shutdown closes HTTP server

**Phase 2: Operational Metrics**
- [ ] Container running count updates on start/stop
- [ ] Container completion increments counter
- [ ] Container failure increments counter
- [ ] Container duration histogram populated
- [ ] Ship status gauges update on poll

**Phase 3: Navigation Metrics**
- [ ] Route completion increments counter
- [ ] Route duration histogram populated
- [ ] Fuel consumption increments counter
- [ ] Distance traveled increments counter
- [ ] Fuel efficiency ratio calculated correctly

**Phase 4: Financial Metrics**
- [ ] Credits balance updates on transaction
- [ ] Transaction counters increment by type/category
- [ ] P&L gauges update on poll
- [ ] Trade profitability histograms populated

**Phase 5: Mediator & API**
- [ ] Command duration histogram populated
- [ ] Command counters increment on execution
- [ ] API request counters increment
- [ ] API duration histogram populated
- [ ] Rate limit wait histogram populated

**Phase 6: Grafana**
- [ ] Prometheus target UP in /targets
- [ ] Dashboards auto-provisioned
- [ ] All panels display data
- [ ] Refresh intervals working
- [ ] Time ranges adjustable

---

## Performance Considerations

### Metrics Overhead

**Target:** < 1% CPU overhead, < 50MB memory overhead

**Benchmarks:**

```go
// File: test/benchmarks/metrics_benchmark_test.go

func BenchmarkMetricsRecording(b *testing.B) {
    // Baseline: Execute command without metrics
    b.Run("Without Metrics", func(b *testing.B) {
        for i := 0; i < b.N; i++ {
            executeCommand()
        }
    })

    // With metrics: Execute command with metrics
    b.Run("With Metrics", func(b *testing.B) {
        for i := 0; i < b.N; i++ {
            executeCommandWithMetrics()
        }
    })

    // Expected: < 5% overhead
}

func BenchmarkPrometheusMiddleware(b *testing.B) {
    mediator := createMediatorWithMetrics()
    command := &NavigateRouteCommand{...}

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        mediator.Send(context.Background(), command)
    }

    // Expected: < 1ms overhead per command
}
```

### Cardinality Management

**Problem:** High cardinality → memory explosion

**Solution:** Limit label values

```go
// ❌ BAD: High cardinality (10,000+ unique waypoints)
routes_total{from="X1-A1-STATION", to="X1-B2-MARKET"}

// ✅ GOOD: Low cardinality (player_id + status only)
routes_total{player_id="1", status="completed"}
```

**Cardinality Analysis:**

| Metric | Labels | Cardinality | Memory Impact |
|--------|--------|-------------|---------------|
| `container_running_total` | player_id (100) × container_type (16) | 1,600 | ~50KB |
| `routes_total` | player_id (100) × status (3) | 300 | ~10KB |
| `player_credits_balance` | player_id (100) × agent (100) | 10,000 | ~300KB |
| `api_requests_total` | method (5) × endpoint (30) × status (10) | 1,500 | ~50KB |
| **Total** | | **~13,400** | **~410KB** |

**Safe:** < 100,000 total time series

### Polling Intervals

**Trade-off:** Freshness vs. Load

```go
// Container metrics: 10s interval
// - Freshness: High (containers change frequently)
// - Load: Low (single map read, no DB query)
ST_METRICS_CONTAINER_POLL_INTERVAL=10s

// Ship metrics: 30s interval
// - Freshness: Medium (ships change less frequently)
// - Load: Medium (DB query, ~100 rows)
ST_METRICS_SHIP_POLL_INTERVAL=30s

// Financial metrics: 60s interval
// - Freshness: Low (balances change slower)
// - Load: High (aggregation query, large table)
ST_METRICS_FINANCIAL_POLL_INTERVAL=60s
```

### Memory Management

**Histogram Buckets:**

```go
// Route duration: 10s to 30min
routeDurationBuckets := []float64{10, 30, 60, 120, 300, 600, 1200, 1800}

// Container duration: 1s to 1 hour
containerDurationBuckets := []float64{1, 5, 10, 30, 60, 300, 600, 1800, 3600}

// API request duration: 100ms to 10s
apiDurationBuckets := []float64{0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0}
```

**Impact:** 8 buckets × 1,000 time series = 8,000 samples (~250KB)

### Scrape Optimization

**Prometheus Scrape Interval:** 15s (default)

**Trade-off:**
- Faster (5s): More granular data, higher load
- Slower (30s): Less granular, lower load

**Recommendation:** 15s (good balance)

---

## Security & Best Practices

### Security Considerations

1. **Metrics Endpoint Exposure**
   - **Current:** HTTP on :9090 (no authentication)
   - **Risk:** Metrics may contain business-sensitive data (credits, profits)
   - **Mitigation:**
     - Bind to localhost only (not 0.0.0.0)
     - Use firewall rules to restrict access
     - Consider basic auth in production

   ```go
   // Production: Bind to localhost only
   s.metricsServer = &http.Server{
       Addr:    "127.0.0.1:9090",  // Not 0.0.0.0:9090
       Handler: mux,
   }
   ```

2. **Data Sanitization**
   - **Risk:** Sensitive data in labels (API tokens, secrets)
   - **Mitigation:** Never use tokens/secrets as labels

   ```go
   // ❌ NEVER do this
   api_requests_total{token="eyJhbGciOiJ..."}

   // ✅ Use player_id instead
   api_requests_total{player_id="1"}
   ```

3. **Denial of Service**
   - **Risk:** Metrics scraping overloads daemon
   - **Mitigation:**
     - Limit scrape timeout (10s)
     - Cache metrics between scrapes
     - Use buffered channels for async updates

### Best Practices

1. **Metric Naming**
   - Use `_total` suffix for counters
   - Use `_seconds` suffix for durations
   - Use consistent namespace/subsystem
   - Follow Prometheus naming conventions

2. **Label Best Practices**
   - Keep label cardinality low (< 1,000 values per label)
   - Use labels for dimensions you'll query
   - Avoid labels with unbounded values
   - Don't change label values over time

3. **Histogram Configuration**
   - Choose buckets based on expected distribution
   - Cover full range of values (min to max)
   - Use more buckets near expected values
   - Avoid too many buckets (< 15)

4. **Error Handling**
   - Never let metrics failures crash daemon
   - Log metrics errors, don't panic
   - Use graceful degradation

   ```go
   func recordMetric() {
       defer func() {
           if r := recover(); r != nil {
               log.Printf("Metrics panic recovered: %v", r)
           }
       }()

       // Metric recording logic
   }
   ```

5. **Testing**
   - Test metrics in isolation (unit tests)
   - Test metrics integration (BDD tests)
   - Verify metrics accuracy (compare to source data)
   - Load test metrics collection

---

## Migration & Rollout Plan

### Rollout Strategy

**Phase-Based Rollout (Recommended):**

1. **Week 1: Core Infrastructure (Internal Testing)**
   - Deploy to development environment
   - Verify metrics endpoint working
   - Test basic container metrics
   - **Rollback Criteria:** HTTP server fails to start

2. **Week 2: Operational Metrics (Alpha Testing)**
   - Enable on single test player
   - Monitor performance impact
   - Validate metrics accuracy
   - **Rollback Criteria:** > 5% performance degradation

3. **Week 3: Navigation + Financial Metrics (Beta Testing)**
   - Enable on 5-10 test players
   - Compare metrics to database queries
   - Fix any discrepancies
   - **Rollback Criteria:** Metrics drift > 10%

4. **Week 4: Full Rollout (Production)**
   - Enable for all players (opt-in)
   - Monitor production load
   - Gather user feedback
   - **Rollback Criteria:** System instability

### Rollback Plan

**Scenario:** Metrics cause performance degradation

**Steps:**

1. **Immediate Mitigation:**
   ```bash
   # Disable metrics in .env
   ST_METRICS_ENABLED=false

   # Restart daemon
   killall spacetraders-daemon
   ./bin/spacetraders-daemon
   ```

2. **Verify Rollback:**
   ```bash
   # Metrics endpoint should return 404
   curl http://localhost:9090/metrics
   # Expected: Connection refused or 404
   ```

3. **Post-Incident:**
   - Identify root cause (profiling, logs)
   - Fix issue in development
   - Re-test before re-deployment

### Compatibility

**Backward Compatibility:**
- ✅ Metrics disabled by default (no breaking changes)
- ✅ No changes to existing APIs
- ✅ No database schema changes
- ✅ No changes to domain entities
- ✅ Existing functionality unaffected

**Forward Compatibility:**
- ✅ Metrics can be added without code changes (via config)
- ✅ Grafana dashboards versioned (JSON files)
- ✅ Prometheus config versioned (YAML file)

---

## Appendices

### Appendix A: Metric Reference

**Complete list of all metrics** (55 total):

#### Container Metrics (5)
1. `spacetraders_daemon_container_running_total` - Gauge - Running containers
2. `spacetraders_daemon_container_total` - Counter - Container lifecycle events
3. `spacetraders_daemon_container_duration_seconds` - Histogram - Execution duration
4. `spacetraders_daemon_container_restarts_total` - Counter - Restart count
5. `spacetraders_daemon_container_iterations_total` - Counter - Iteration count

#### Ship Metrics (2)
6. `spacetraders_daemon_ships_total` - Gauge - Ship count by role/location
7. `spacetraders_daemon_ship_status_total` - Gauge - Ship state distribution

#### Route Metrics (5)
8. `spacetraders_daemon_routes_total` - Counter - Route completions/failures
9. `spacetraders_daemon_route_duration_seconds` - Histogram - Route execution time
10. `spacetraders_daemon_route_distance_traveled_total` - Counter - Total distance
11. `spacetraders_daemon_route_fuel_consumed_total` - Counter - Total fuel
12. `spacetraders_daemon_route_segments_completed_total` - Counter - Segment count

#### Fuel Metrics (3)
13. `spacetraders_daemon_fuel_purchased_units_total` - Counter - Fuel purchases
14. `spacetraders_daemon_fuel_consumed_units_total` - Counter - Fuel usage
15. `spacetraders_daemon_fuel_efficiency_ratio` - Histogram - Fuel efficiency

#### Financial Metrics (7)
16. `spacetraders_daemon_player_credits_balance` - Gauge - Current balance
17. `spacetraders_daemon_transactions_total` - Counter - Transaction count
18. `spacetraders_daemon_transaction_amount` - Histogram - Transaction amounts
19. `spacetraders_daemon_total_revenue` - Gauge - Revenue by category
20. `spacetraders_daemon_total_expenses` - Gauge - Expenses by category
21. `spacetraders_daemon_net_profit` - Gauge - Net profit
22. `spacetraders_daemon_trade_profit_per_unit` - Histogram - Trade profit
23. `spacetraders_daemon_trade_margin_percent` - Histogram - Trade margin

#### API Metrics (5)
24. `spacetraders_daemon_api_requests_total` - Counter - API request count
25. `spacetraders_daemon_api_request_duration_seconds` - Histogram - Request duration
26. `spacetraders_daemon_api_rate_limit_wait_seconds` - Histogram - Rate limit wait
27. `spacetraders_daemon_api_retries_total` - Counter - Retry count
28. `spacetraders_daemon_api_errors_total` - Counter - Error count

#### Command Metrics (2)
29. `spacetraders_daemon_command_duration_seconds` - Histogram - Command duration
30. `spacetraders_daemon_command_total` - Counter - Command execution count

### Appendix B: File Structure

**New Files Created:**

```
spacetraders/
├── internal/
│   ├── adapters/
│   │   ├── metrics/
│   │   │   ├── prometheus_collector.go         [NEW]
│   │   │   ├── container_metrics.go            [NEW]
│   │   │   ├── navigation_metrics.go           [NEW]
│   │   │   ├── financial_metrics.go            [NEW]
│   │   │   └── api_metrics.go                  [NEW]
│   │   └── grpc/
│   │       └── daemon_server.go                [MODIFIED]
│   ├── application/
│   │   └── common/
│   │       └── prometheus_middleware.go        [NEW]
│   └── infrastructure/
│       └── config/
│           └── config.go                       [MODIFIED]
├── configs/
│   ├── prometheus/
│   │   └── prometheus.yml                      [NEW]
│   └── grafana/
│       ├── provisioning/
│       │   ├── datasources/
│       │   │   └── prometheus.yml              [NEW]
│       │   └── dashboards/
│       │       └── dashboards.yml              [NEW]
│       └── dashboards/
│           ├── operational.json                [NEW]
│           ├── navigation.json                 [NEW]
│           └── financial.json                  [NEW]
├── test/
│   └── bdd/
│       └── features/
│           └── adapters/
│               └── metrics/
│                   ├── container_metrics.feature    [NEW]
│                   ├── navigation_metrics.feature   [NEW]
│                   └── financial_metrics.feature    [NEW]
├── docker-compose.metrics.yml                  [NEW]
├── docs/
│   └── METRICS_IMPLEMENTATION_PLAN.md          [NEW - THIS FILE]
└── CLAUDE.md                                   [MODIFIED]
```

### Appendix C: Dependencies

**Go Dependencies (go.mod):**

```go
require (
    github.com/prometheus/client_golang v1.19.0  // NEW
    // ... existing dependencies
)
```

**Docker Images:**

```yaml
prom/prometheus:latest       # ~200MB
grafana/grafana:latest       # ~300MB
```

### Appendix D: Troubleshooting

**Problem:** Metrics endpoint returns 404

**Solution:**
```bash
# Check if metrics are enabled
grep ST_METRICS_ENABLED .env
# Expected: ST_METRICS_ENABLED=true

# Check daemon logs
tail -f /var/log/spacetraders-daemon.log | grep metrics
```

**Problem:** Prometheus can't scrape daemon

**Solution:**
```bash
# Check if daemon is listening
netstat -an | grep 9090
# Expected: tcp4  0  0  *.9090  *.*  LISTEN

# Check Prometheus logs
docker logs spacetraders-prometheus
# Look for scrape errors
```

**Problem:** Metrics values incorrect

**Solution:**
```bash
# Compare metrics to database
psql -d spacetraders -c "SELECT COUNT(*) FROM containers WHERE status='RUNNING';"
curl http://localhost:9090/metrics | grep container_running_total

# Check collector logs
# Look for errors in metric calculation
```

**Problem:** High memory usage

**Solution:**
```bash
# Check cardinality
curl http://localhost:9090/api/v1/label/__name__/values | jq '.data | length'
# If > 100,000: Review label usage

# Identify high-cardinality metrics
curl http://localhost:9090/api/v1/query?query=count%20by%20(__name__)%20({__name__=~".%2B"})
```

### Appendix E: Glossary

**Terms:**

- **Cardinality:** Number of unique time series (metric + label combination)
- **Counter:** Monotonically increasing value (resets on restart)
- **Gauge:** Current value that can go up or down
- **Histogram:** Distribution of values across buckets
- **Label:** Key-value pair attached to metric (e.g., `player_id="1"`)
- **Namespace:** Metric prefix (e.g., `spacetraders`)
- **Scrape:** Prometheus fetching metrics from endpoint
- **Subsystem:** Metric component (e.g., `daemon`)
- **Time Series:** Unique metric + label combination over time

---

## Summary

This implementation plan provides a comprehensive blueprint for adding Prometheus/Grafana metrics to the SpaceTraders Go bot. The design:

1. **Maintains Hexagonal Architecture** - Metrics in adapter layer, zero domain pollution
2. **Tracks Business Metrics** - Operational, navigation, financial as requested
3. **Provides Grafana Dashboards** - 3 dashboards covering all key metrics
4. **Ensures Performance** - < 1% overhead, low cardinality, optimized polling
5. **Follows Best Practices** - Prometheus conventions, security, testing
6. **Enables Incremental Rollout** - Phased deployment with rollback plan

**Estimated Effort:** 7-11 hours total, spread across 7 phases

**Next Steps:** Review plan → Get approval → Begin Phase 1 implementation
