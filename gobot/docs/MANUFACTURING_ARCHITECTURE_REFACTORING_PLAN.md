# Manufacturing System Architecture Refactoring Plan

## Executive Summary

Comprehensive architectural refactoring of the SpaceTraders manufacturing system to enable:
1. **Performance**: Reduce API calls, predictive supply polling, ship pre-positioning
2. **Multi-Pipeline Coordination**: Share intermediate goods, prevent factory conflicts
3. **Profitability Intelligence**: Forecast profits, dynamic quantities, priority selection

The current 1,278-line coordinator will be decomposed into focused services following Single Responsibility Principle.

### Design Decisions
- **Scope**: Implement all 4 phases
- **Intermediate Good Pool**: Included (enables pipelines to share intermediate products)
- **Profitability Mode**: Advisory only (log forecasts, don't block pipeline creation)

---

## Current State Analysis

### Pain Points

| Issue | Location | Impact |
|-------|----------|--------|
| **Monolithic Coordinator** | `run_parallel_manufacturing_coordinator.go` (1,278 lines) | Hard to test, maintain, extend |
| **Mixed Concerns** | Single handler does opportunity scanning, pipeline management, task assignment, ship discovery, completion handling | Violates SRP |
| **Independent Pipelines** | No sharing of intermediate goods | Redundant manufacturing |
| **Factory Conflicts** | Multiple pipelines may compete for same factory | Race conditions |
| **Reactive Polling** | SupplyMonitor polls every 30s regardless of factory state | Wasted API calls |
| **Idle Ships** | Ships wait until tasks are ready | Poor utilization |
| **No Profitability Insight** | Pipelines started without knowing expected profit | Potential losses |

### Current Architecture

```
RunParallelManufacturingCoordinatorHandler (1,278 LOC)
├── scanAndCreatePipelines()           [lines 269-381]
├── hasPipelineForGood()               [lines 383-394]
├── assignTasks()                      [lines 396-514]
├── findCollectTaskShip()              [lines 516-536]
├── getTaskSourceLocation()            [lines 538-561]
├── findClosestShip()                  [lines 563-590]
├── assignTaskToShip()                 [lines 592-703]
├── assignFreedShipToTask()            [lines 783-852]
├── handleTaskCompletion()             [lines 854-896]
├── updateDependentTasks()             [lines 898-978]
├── updateFactoryStateOnDelivery()     [lines 980-1043]
├── checkPipelineCompletion()          [lines 1045-1157]
└── recoverState()                     [lines 1159-1278]
```

---

## Target Architecture

```
+------------------------------------------------------------------+
|                        APPLICATION LAYER                          |
+------------------------------------------------------------------+
|  +-----------------------+    +---------------------------+       |
|  | ManufacturingOrchest- |    | ProfitabilityService      |       |
|  | rator (thin ~300 LOC) |<-->| - ForecastPipelineProfit  |       |
|  | - Event coordination  |    | - CalculateOptimalQty     |       |
|  | - Lifecycle mgmt      |    | - RankOpportunities       |       |
|  +-----------+-----------+    +---------------------------+       |
|              |                                                    |
|   +----------+----------+                                         |
|   |          |          |                                         |
|   v          v          v                                         |
|  +--------+ +----------+ +------------+ +----------------------+  |
|  |TaskAssn| |ShipDispat| |PipelineLif-| |SharedResourceCoord-  |  |
|  |Service | |chService | |ecycleServ  | |inator                |  |
|  +--------+ +----------+ +------------+ | - FactoryReservations|  |
|                                         | - IntermediateGoodPool|  |
|  +-------------------------+            +----------------------+  |
|  | PredictiveSupplyMonitor |  +-----------------------------+     |
|  | - DeliveryTracking      |  | ShipPrePositioningService   |     |
|  | - ProductionEstimation  |  | - PredictNextTask           |     |
|  +-------------------------+  +-----------------------------+     |
+------------------------------------------------------------------+
                            |
                            v
+------------------------------------------------------------------+
|                         DOMAIN LAYER                              |
+------------------------------------------------------------------+
|  Existing:                    NEW Entities:                       |
|  +-------------------+        +---------------------+             |
|  | ManufacturingPipe |        | FactoryReservation  |             |
|  | ManufacturingTask |        | ProfitForecast      |             |
|  | FactoryState      |        | IntermediateGoodPool|             |
|  +-------------------+        | ProductionEstimate  |             |
|                               +---------------------+             |
+------------------------------------------------------------------+
```

---

## New Domain Entities

### 1. FactoryReservation

**Purpose:** Prevents multiple pipelines from delivering to the same factory simultaneously.

**Location:** `internal/domain/manufacturing/factory_reservation.go`

```go
type FactoryReservation struct {
    id             string
    factorySymbol  string
    outputGood     string
    pipelineID     string
    playerID       int
    reservedAt     time.Time
    expiresAt      time.Time
    deliverySlots  []DeliverySlot
    status         ReservationStatus  // ACTIVE, FULFILLED, EXPIRED, CANCELLED
}

type DeliverySlot struct {
    inputGood    string
    expectedShip string
    windowStart  time.Time
    windowEnd    time.Time
}

type ReservationStatus string

const (
    ReservationStatusActive    ReservationStatus = "ACTIVE"
    ReservationStatusFulfilled ReservationStatus = "FULFILLED"
    ReservationStatusExpired   ReservationStatus = "EXPIRED"
    ReservationStatusCancelled ReservationStatus = "CANCELLED"
)
```

**State Machine:**
```
ACTIVE → FULFILLED (all inputs delivered)
ACTIVE → EXPIRED (time elapsed without fulfillment)
ACTIVE → CANCELLED (pipeline cancelled)
```

### 2. ProfitForecast

**Purpose:** Predicts pipeline profitability before execution (advisory mode - logs only).

**Location:** `internal/domain/manufacturing/profit_forecast.go`

```go
type ProfitForecast struct {
    id                string
    pipelineID        string
    targetGood        string
    estimatedCost     int
    estimatedRevenue  int
    estimatedProfit   int
    profitMargin      float64
    breakEvenQuantity int
    confidenceLevel   ConfidenceLevel  // HIGH, MEDIUM, LOW
    priceSnapshots    []PriceSnapshot
    actualProfit      *int             // Set after pipeline completes
    validUntil        time.Time
    createdAt         time.Time
}

type PriceSnapshot struct {
    good           string
    waypointSymbol string
    price          int
    supply         string
    activity       string
    capturedAt     time.Time
}

type ConfidenceLevel string

const (
    ConfidenceLevelHigh   ConfidenceLevel = "HIGH"   // All prices fresh (<5min)
    ConfidenceLevelMedium ConfidenceLevel = "MEDIUM" // Some prices stale
    ConfidenceLevelLow    ConfidenceLevel = "LOW"    // Market volatility detected
)
```

**Validation Flow:**
```
Pipeline Created → Forecast Generated → Pipeline Executes → Actual Profit Recorded
                                                                    ↓
                                          Forecast vs Actual Comparison Logged
```

### 3. IntermediateGoodPool

**Purpose:** Enables sharing of intermediate goods between pipelines.

**Location:** `internal/domain/manufacturing/intermediate_good_pool.go`

```go
type IntermediateGoodPool struct {
    id                 int
    good               string
    systemSymbol       string
    availableUnits     int
    reservedUnits      int
    updatedAt          time.Time
}

type PoolReservation struct {
    id         string
    poolID     int
    pipelineID string
    units      int
    reservedAt time.Time
    expiresAt  time.Time
    consumed   bool
}
```

**Example Use Case:**
```
Pipeline A: Manufacturing LASER_RIFLES needs ELECTRONICS
Pipeline B: Manufacturing ELECTRONICS (intermediate)

Instead of Pipeline A manufacturing its own ELECTRONICS:
1. Pipeline B completes COLLECT task for ELECTRONICS
2. Pipeline B adds surplus ELECTRONICS to IntermediateGoodPool
3. Pipeline A reserves ELECTRONICS from pool
4. Pipeline A skips its own ELECTRONICS manufacturing chain
```

### 4. ProductionEstimate

**Purpose:** Predicts when a factory will have goods ready for smart polling.

**Location:** `internal/domain/manufacturing/production_estimate.go`

```go
type ProductionEstimate struct {
    id                 string
    factorySymbol      string
    outputGood         string
    pipelineID         string
    inputsDeliveredAt  map[string]time.Time
    allInputsAt        *time.Time
    estimatedReadyAt   time.Time
    productionRate     float64  // Historical units/hour
    confidenceLevel    ConfidenceLevel
    createdAt          time.Time
}
```

**Polling Algorithm:**
```
Time relative to estimatedReadyAt    Poll Interval
─────────────────────────────────    ─────────────
> 5 minutes before                   2 minutes (slow)
0-5 minutes before                   10 seconds (fast)
> 5 minutes after                    5 seconds (urgent)
```

---

## New Application Services

### Phase 1: Core Service Extraction

#### 1.1 TaskAssignmentService

**Location:** `internal/application/trading/services/task_assignment_service.go`

**Responsibilities:**
- Assign ready tasks to idle ships
- Find optimal ship for each task (distance-based)
- Handle SELL task ship affinity (must use COLLECT ship)
- Reassign freed ships immediately

```go
type TaskAssignmentService interface {
    // AssignReadyTasks assigns all ready tasks to available ships
    AssignReadyTasks(ctx context.Context, playerID int, maxConcurrent int) (assigned int, err error)

    // AssignShipToTask assigns a specific ship to a specific task
    AssignShipToTask(ctx context.Context, ship *navigation.Ship, task *manufacturing.ManufacturingTask) error

    // ReassignFreedShip immediately assigns a freed ship to the next ready task
    ReassignFreedShip(ctx context.Context, shipSymbol string, playerID int) error

    // GetAssignedTasks returns current task-to-ship assignments
    GetAssignedTasks() map[string]string  // taskID -> shipSymbol
}
```

**Dependencies:**
- `TaskRepository`
- `ShipRepository`
- `TaskQueue`
- `DaemonClient` (for container management)

#### 1.2 PipelineLifecycleService

**Location:** `internal/application/trading/services/pipeline_lifecycle_service.go`

**Responsibilities:**
- Create pipelines from opportunities
- Handle task completion events
- Update dependent task readiness
- Check pipeline completion/failure
- Recovery after daemon restart

```go
type PipelineLifecycleService interface {
    // CreatePipelinesFromOpportunities scans for opportunities and creates pipelines
    CreatePipelinesFromOpportunities(ctx context.Context, systemSymbol string, playerID int, maxPipelines int) error

    // HandleTaskCompletion processes task completion and updates dependencies
    HandleTaskCompletion(ctx context.Context, taskID string, success bool) error

    // GetActivePipelines returns all non-terminal pipelines
    GetActivePipelines() []*manufacturing.ManufacturingPipeline

    // HasPipelineForGood checks if a pipeline already exists for a good
    HasPipelineForGood(good string) bool

    // RecoverState rebuilds in-memory state after daemon restart
    RecoverState(ctx context.Context, playerID int) error
}
```

**Dependencies:**
- `PipelineRepository`
- `TaskRepository`
- `FactoryStateTracker`
- `ManufacturingDemandFinder`
- `PipelinePlanner`
- `ProfitabilityService` (advisory logging)

#### 1.3 ShipDispatchService

**Location:** `internal/application/trading/services/ship_dispatch_service.go`

**Responsibilities:**
- Find idle ships for manufacturing
- Track ship-to-container assignments
- Release ships when containers complete

```go
type ShipDispatchService interface {
    // GetIdleShips returns ships available for manufacturing tasks
    GetIdleShips(ctx context.Context, playerID int) ([]*navigation.Ship, error)

    // AssignShipToContainer creates ship-container assignment
    AssignShipToContainer(ctx context.Context, shipSymbol string, containerID string) error

    // ReleaseShip removes ship-container assignment
    ReleaseShip(ctx context.Context, shipSymbol string, reason string) error

    // IsShipAvailable checks if ship is idle
    IsShipAvailable(ctx context.Context, shipSymbol string) bool
}
```

### Phase 2: Multi-Pipeline Coordination

#### 2.1 SharedResourceCoordinator

**Location:** `internal/application/trading/services/shared_resource_coordinator.go`

**Responsibilities:**
- Manage factory reservations
- Manage intermediate good pool
- Prevent conflicts between pipelines

```go
type SharedResourceCoordinator interface {
    // Factory Reservations
    ReserveFactory(ctx context.Context, factorySymbol string, pipelineID string, duration time.Duration) (*manufacturing.FactoryReservation, error)
    ReleaseReservation(ctx context.Context, reservationID string) error
    IsFactoryAvailable(ctx context.Context, factorySymbol string) bool
    GetActiveReservations(ctx context.Context, factorySymbol string) ([]*manufacturing.FactoryReservation, error)

    // Intermediate Good Pool
    AddToPool(ctx context.Context, good string, units int, sourcePipeline string) error
    ReserveFromPool(ctx context.Context, good string, units int, consumerPipeline string) (*manufacturing.PoolReservation, error)
    ConsumeReservation(ctx context.Context, reservationID string) error
    GetPoolAvailability(ctx context.Context, good string) (available int, reserved int, err error)

    // Cleanup
    ExpireStaleReservations(ctx context.Context) error
}
```

### Phase 3: Profitability Intelligence

#### 3.1 ProfitabilityService (Advisory Mode)

**Location:** `internal/application/trading/services/profitability_service.go`

**Responsibilities:**
- Forecast pipeline profit before execution
- Calculate optimal quantities
- Rank opportunities by ROI
- Validate forecasts against actuals (for learning)

**Note:** Operates in advisory mode - forecasts are logged but do NOT block pipeline creation.

```go
type ProfitabilityService interface {
    // ForecastPipelineProfit calculates expected profit (advisory - logs only)
    ForecastPipelineProfit(ctx context.Context, opportunity *trading.ManufacturingOpportunity, systemSymbol string, playerID int) (*manufacturing.ProfitForecast, error)

    // CalculateOptimalQuantity determines best quantity based on cargo capacity and margins
    CalculateOptimalQuantity(ctx context.Context, opportunity *trading.ManufacturingOpportunity, cargoCapacity int) (optimalQty int, expectedProfit int, err error)

    // RankOpportunitiesByROI sorts opportunities by expected return
    RankOpportunitiesByROI(ctx context.Context, opportunities []*trading.ManufacturingOpportunity) ([]*trading.ManufacturingOpportunity, error)

    // ValidateForecast compares forecast vs actual for learning/tuning
    ValidateForecast(ctx context.Context, forecastID string, actualProfit int) error
}
```

**Logging Output Example:**
```
[PROFIT_FORECAST] Pipeline abc123 for LASER_RIFLES
  Estimated Cost:    45,000 cr
  Estimated Revenue: 78,000 cr
  Estimated Profit:  33,000 cr (73% margin)
  Confidence: HIGH

[PROFIT_VALIDATION] Pipeline abc123 completed
  Forecast Profit:  33,000 cr
  Actual Profit:    31,500 cr
  Variance:         -4.5%
  Confidence: HIGH (validated)
```

### Phase 4: Performance Optimization

#### 4.1 PredictiveSupplyMonitor

**Location:** `internal/application/trading/services/predictive_supply_monitor.go`

**Responsibilities:**
- Track input deliveries to factories
- Estimate when supply will reach HIGH
- Adaptive polling based on production estimates
- Reduce API calls by 30-50%

```go
type PredictiveSupplyMonitor interface {
    // RecordDelivery tracks when an input is delivered to a factory
    RecordDelivery(ctx context.Context, factorySymbol string, good string, quantity int, timestamp time.Time) error

    // RecordAllInputsDelivered marks factory as ready for production estimation
    RecordAllInputsDelivered(ctx context.Context, factorySymbol string, pipelineID string) error

    // EstimateProductionReady predicts when supply will reach HIGH
    EstimateProductionReady(ctx context.Context, factorySymbol string, pipelineID string) (*manufacturing.ProductionEstimate, error)

    // GetFactoriesToPoll returns factories due for polling based on adaptive intervals
    GetFactoriesToPoll(ctx context.Context) ([]*manufacturing.FactoryState, error)

    // Run starts the background polling loop
    Run(ctx context.Context)

    // SetAdaptivePollInterval enables/disables smart polling
    SetAdaptivePollInterval(enabled bool)
}
```

#### 4.2 ShipPrePositioningService

**Location:** `internal/application/trading/services/ship_prepositioning_service.go`

**Responsibilities:**
- Predict which task a ship will likely execute next
- Calculate optimal idle position
- Pre-position ships to reduce travel time

```go
type ShipPrePositioningService interface {
    // PredictNextTask predicts the most likely next task for a ship
    PredictNextTask(ctx context.Context, shipSymbol string, playerID int) (*manufacturing.ManufacturingTask, float64, error)

    // CalculateOptimalIdlePosition finds best waiting position given pending tasks
    CalculateOptimalIdlePosition(ctx context.Context, shipSymbol string, pendingTasks []*manufacturing.ManufacturingTask) (*shared.Waypoint, error)

    // PrePositionShip navigates idle ship to optimal position
    PrePositionShip(ctx context.Context, ship *navigation.Ship, targetPosition *shared.Waypoint) error

    // GetShipETA returns estimated arrival time at current destination
    GetShipETA(ctx context.Context, shipSymbol string) (time.Time, error)
}
```

---

## Database Migrations

### Migration: factory_reservations

```sql
-- Migration: 0XX_add_factory_reservations.up.sql

CREATE TABLE factory_reservations (
    id VARCHAR(36) PRIMARY KEY,
    factory_symbol VARCHAR(50) NOT NULL,
    output_good VARCHAR(50) NOT NULL,
    pipeline_id VARCHAR(36) NOT NULL REFERENCES manufacturing_pipelines(id) ON DELETE CASCADE,
    player_id INT NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'ACTIVE',
    reserved_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    delivery_slots JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT unique_active_factory_reservation
        UNIQUE (factory_symbol, output_good, status)
        WHERE status = 'ACTIVE'
);

CREATE INDEX idx_factory_reservations_pipeline ON factory_reservations(pipeline_id);
CREATE INDEX idx_factory_reservations_factory ON factory_reservations(factory_symbol);
CREATE INDEX idx_factory_reservations_status ON factory_reservations(status) WHERE status = 'ACTIVE';
```

### Migration: profit_forecasts

```sql
-- Migration: 0XX_add_profit_forecasts.up.sql

CREATE TABLE profit_forecasts (
    id VARCHAR(36) PRIMARY KEY,
    pipeline_id VARCHAR(36) REFERENCES manufacturing_pipelines(id) ON DELETE CASCADE,
    target_good VARCHAR(50) NOT NULL,
    estimated_cost INT NOT NULL,
    estimated_revenue INT NOT NULL,
    estimated_profit INT NOT NULL,
    profit_margin DECIMAL(5,2),
    break_even_quantity INT,
    confidence_level VARCHAR(20) NOT NULL,
    price_snapshots JSONB NOT NULL,
    actual_profit INT,
    valid_until TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_profit_forecasts_pipeline ON profit_forecasts(pipeline_id);
CREATE INDEX idx_profit_forecasts_good ON profit_forecasts(target_good);
```

### Migration: intermediate_good_pools

```sql
-- Migration: 0XX_add_intermediate_good_pools.up.sql

CREATE TABLE intermediate_good_pools (
    id SERIAL PRIMARY KEY,
    good VARCHAR(50) NOT NULL,
    system_symbol VARCHAR(20) NOT NULL,
    available_units INT NOT NULL DEFAULT 0,
    reserved_units INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT unique_pool_good UNIQUE (good, system_symbol)
);

CREATE TABLE pool_reservations (
    id VARCHAR(36) PRIMARY KEY,
    pool_id INT NOT NULL REFERENCES intermediate_good_pools(id) ON DELETE CASCADE,
    pipeline_id VARCHAR(36) NOT NULL,
    units INT NOT NULL,
    reserved_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    consumed BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE INDEX idx_pool_reservations_pool ON pool_reservations(pool_id);
CREATE INDEX idx_pool_reservations_pipeline ON pool_reservations(pipeline_id);
CREATE INDEX idx_pool_reservations_active ON pool_reservations(consumed) WHERE consumed = FALSE;
```

---

## Repository Interfaces

Add to `internal/domain/manufacturing/ports.go`:

```go
// FactoryReservationRepository manages factory reservation persistence
type FactoryReservationRepository interface {
    Create(ctx context.Context, reservation *FactoryReservation) error
    Update(ctx context.Context, reservation *FactoryReservation) error
    FindByID(ctx context.Context, id string) (*FactoryReservation, error)
    FindActiveByFactory(ctx context.Context, factorySymbol string) (*FactoryReservation, error)
    FindByPipelineID(ctx context.Context, pipelineID string) ([]*FactoryReservation, error)
    ExpireStale(ctx context.Context) (int, error)
    Delete(ctx context.Context, id string) error
}

// ProfitForecastRepository manages profit forecast persistence
type ProfitForecastRepository interface {
    Create(ctx context.Context, forecast *ProfitForecast) error
    Update(ctx context.Context, forecast *ProfitForecast) error
    FindByID(ctx context.Context, id string) (*ProfitForecast, error)
    FindByPipelineID(ctx context.Context, pipelineID string) (*ProfitForecast, error)
    FindUnvalidated(ctx context.Context, playerID int) ([]*ProfitForecast, error)
    Delete(ctx context.Context, id string) error
}

// IntermediateGoodPoolRepository manages intermediate good pool persistence
type IntermediateGoodPoolRepository interface {
    FindOrCreate(ctx context.Context, good string, systemSymbol string) (*IntermediateGoodPool, error)
    Update(ctx context.Context, pool *IntermediateGoodPool) error
    FindByGood(ctx context.Context, good string, systemSymbol string) (*IntermediateGoodPool, error)

    CreateReservation(ctx context.Context, reservation *PoolReservation) error
    ConsumeReservation(ctx context.Context, reservationID string) error
    FindActiveReservations(ctx context.Context, poolID int) ([]*PoolReservation, error)
    ExpireStaleReservations(ctx context.Context) (int, error)
}
```

---

## Implementation Order

### Phase 1: Foundation (Extract Services)

| Step | Task | Files |
|------|------|-------|
| 1.1 | Create `TaskAssignmentService` interface and implementation | `services/task_assignment_service.go` |
| 1.2 | Create `PipelineLifecycleService` interface and implementation | `services/pipeline_lifecycle_service.go` |
| 1.3 | Create `ShipDispatchService` interface and implementation | `services/ship_dispatch_service.go` |
| 1.4 | Refactor coordinator to use new services (~300 LOC) | `commands/run_parallel_manufacturing_coordinator.go` |
| 1.5 | Add BDD tests for extracted services | `test/bdd/features/application/` |

### Phase 2: Multi-Pipeline Coordination

| Step | Task | Files |
|------|------|-------|
| 2.1 | Add `FactoryReservation` domain entity | `domain/manufacturing/factory_reservation.go` |
| 2.2 | Add `FactoryReservationRepository` interface | `domain/manufacturing/ports.go` |
| 2.3 | Implement repository | `adapters/persistence/factory_reservation_repository.go` |
| 2.4 | Add database migration | `migrations/0XX_add_factory_reservations.up.sql` |
| 2.5 | Create `SharedResourceCoordinator` | `services/shared_resource_coordinator.go` |
| 2.6 | Integrate reservations into `PipelineLifecycleService` | - |
| 2.7 | Add BDD tests | `test/bdd/features/domain/manufacturing/` |

### Phase 3: Profitability Intelligence

| Step | Task | Files |
|------|------|-------|
| 3.1 | Add `ProfitForecast` domain entity | `domain/manufacturing/profit_forecast.go` |
| 3.2 | Add `ProfitForecastRepository` interface | `domain/manufacturing/ports.go` |
| 3.3 | Implement repository | `adapters/persistence/profit_forecast_repository.go` |
| 3.4 | Add database migration | `migrations/0XX_add_profit_forecasts.up.sql` |
| 3.5 | Create `ProfitabilityService` (advisory mode) | `services/profitability_service.go` |
| 3.6 | Integrate into pipeline creation (logging only) | - |
| 3.7 | Add forecast validation on pipeline completion | - |
| 3.8 | Add BDD tests | `test/bdd/features/application/` |

### Phase 4: Performance Optimization

| Step | Task | Files |
|------|------|-------|
| 4.1 | Add `ProductionEstimate` domain entity | `domain/manufacturing/production_estimate.go` |
| 4.2 | Create `PredictiveSupplyMonitor` | `services/predictive_supply_monitor.go` |
| 4.3 | Add `IntermediateGoodPool` domain entity | `domain/manufacturing/intermediate_good_pool.go` |
| 4.4 | Add `IntermediateGoodPoolRepository` interface | `domain/manufacturing/ports.go` |
| 4.5 | Implement repository | `adapters/persistence/intermediate_good_pool_repository.go` |
| 4.6 | Add database migration | `migrations/0XX_add_intermediate_good_pools.up.sql` |
| 4.7 | Integrate pool into `SharedResourceCoordinator` | - |
| 4.8 | Create `ShipPrePositioningService` | `services/ship_prepositioning_service.go` |
| 4.9 | Integrate pre-positioning into `TaskAssignmentService` | - |
| 4.10 | Add BDD tests | `test/bdd/features/application/` |

---

## Critical Files to Modify

| File | Changes |
|------|---------|
| `internal/application/trading/commands/run_parallel_manufacturing_coordinator.go` | Refactor from 1,278 to ~300 lines; delegate to services |
| `internal/domain/manufacturing/ports.go` | Add repository interfaces for new entities |
| `internal/application/trading/services/supply_monitor.go` | Replace with PredictiveSupplyMonitor |
| `internal/domain/manufacturing/pipeline.go` | Reference ProfitForecast and FactoryReservation |
| `internal/adapters/persistence/models.go` | Add new database models |

---

## Success Metrics

| Metric | Target | Measurement |
|--------|--------|-------------|
| **Coordinator size** | < 300 lines | Lines of code (from 1,278) |
| **API calls reduced** | 30-50% reduction | Supply polling call count |
| **Factory conflicts** | Zero | Concurrent pipeline conflicts |
| **Profitability tracking** | 100% | Pipelines with forecast vs actual comparison |
| **Ship utilization** | < 30 seconds | Average idle time |
| **Intermediate goods shared** | Track | Goods reused across pipelines |
| **Test coverage** | 100% | BDD feature coverage for new services |

---

## Risk Mitigation

| Risk | Mitigation |
|------|------------|
| **Breaking existing functionality** | Phase 1 extracts without changing behavior; BDD tests ensure correctness |
| **Reservation deadlocks** | Reservations auto-expire; cleanup job runs periodically |
| **Forecast inaccuracy** | Advisory mode; validation logs enable tuning over time |
| **Pool starvation** | Reservations expire; producing pipelines prioritized |
| **Increased complexity** | Each service has single responsibility; comprehensive tests |

---

## Future Enhancements (Out of Scope)

- **Hard profit gating**: Block unprofitable pipelines (requires tuning first)
- **Cross-system manufacturing**: Route between star systems
- **Machine learning forecasts**: Use historical data for better predictions
- **Dynamic ship allocation**: Reallocate ships between manufacturing and other tasks
