# Manufacturing System Comprehensive Refactoring Plan V2

## Implementation Status

| Phase | Status | Date | Notes |
|-------|--------|------|-------|
| Phase 1: Domain Layer Enrichment | COMPLETED | 2025-11-28 | All domain files created, build verified |
| Phase 2: Service Decomposition | COMPLETED | 2025-11-28 | 8 focused services extracted, build verified |
| Phase 3: Dependency Injection | COMPLETED | 2025-11-28 | Coordinator created, managers refactored to delegate to focused services |
| Phase 4: Interface Refinement (ISP) | COMPLETED | 2025-11-28 | 11 focused interfaces created in interfaces.go |

---

## Executive Summary

This plan builds on [V1](./MANUFACTURING_SOLID_REFACTORING_PLAN.md) (service extraction) by adding:
- **Domain Layer Enrichment** - Move business logic from application to domain
- **Circular Dependency Elimination** - Replace callback setters with constructor injection
- **Interface Segregation** - Split large interfaces into focused contracts
- **Self-Documenting Code** - Replace comments with well-named abstractions

---

## Current Architecture Analysis

### Domain Layer (1,899 lines) - RICH MODEL

| File | Lines | Type | Assessment |
|------|-------|------|------------|
| `task.go` | 627 | Aggregate Root | Rich - 52 methods, state machine |
| `pipeline.go` | 540 | Aggregate Root | Rich - 46 methods, task orchestration |
| `factory_state.go` | 489 | Domain Service + VO | Rich - 42 methods, supply tracking |
| `errors.go` | 132 | Error Types | Complete |
| `ports.go` | 111 | Repository Interfaces | Complete |

**Verdict:** Domain is behavior-rich but missing some business logic that leaked to application layer.

### Application Layer (~4,500 lines) - NEEDS REFACTORING

| File | Lines | Issues |
|------|-------|--------|
| `pipeline_lifecycle_manager.go` | 846 | **8 responsibilities** - God object |
| `task_assignment_manager.go` | 616 | **7 responsibilities** - God object |
| `state_recovery_manager.go` | 401 | 3 mixed concerns |
| `worker_lifecycle_manager.go` | 346 | Circular dependency via callbacks |
| `orphaned_cargo_handler.go` | 413 | Type assertions to concrete types |
| `purchaser.go` | 255 | Business constants in wrong layer |

---

## Phase 1: Domain Layer Enrichment

### 1.1 Supply Level Value Object

**New File:** `internal/domain/manufacturing/supply_level.go`

**Why:** Supply multipliers in `purchaser.go:17-31` are domain rules, not application logic.

```go
package manufacturing

// SupplyLevel represents market supply abundance and encapsulates
// business rules about purchasing and collection viability.
type SupplyLevel string

const (
    SupplyLevelAbundant SupplyLevel = "ABUNDANT"
    SupplyLevelHigh     SupplyLevel = "HIGH"
    SupplyLevelModerate SupplyLevel = "MODERATE"
    SupplyLevelLimited  SupplyLevel = "LIMITED"
    SupplyLevelScarce   SupplyLevel = "SCARCE"
)

// purchaseMultipliers defines safe purchase fractions to prevent supply crashes.
// Key business rule: Never deplete supply beyond safe thresholds.
var purchaseMultipliers = map[SupplyLevel]float64{
    SupplyLevelAbundant: 0.80, // Plenty of buffer
    SupplyLevelHigh:     0.60, // Sweet spot - maintain stability
    SupplyLevelModerate: 0.40, // Careful - could drop to LIMITED
    SupplyLevelLimited:  0.20, // Very careful - critical supply
    SupplyLevelScarce:   0.10, // Minimal - supply nearly depleted
}

// PurchaseMultiplier returns the safe purchase fraction based on supply level.
func (s SupplyLevel) PurchaseMultiplier() float64 {
    if mult, ok := purchaseMultipliers[s]; ok {
        return mult
    }
    return 0.40 // Default to moderate
}

// IsFavorableForCollection returns true if supply is HIGH or ABUNDANT,
// indicating the factory has produced enough output to collect.
func (s SupplyLevel) IsFavorableForCollection() bool {
    return s == SupplyLevelHigh || s == SupplyLevelAbundant
}

// IsSaturated returns true if market already has high supply,
// making it a poor target for selling.
func (s SupplyLevel) IsSaturated() bool {
    return s == SupplyLevelHigh || s == SupplyLevelAbundant
}

// AllowsPurchase returns true if supply level permits buying.
// SCARCE supply should not be depleted further.
func (s SupplyLevel) AllowsPurchase() bool {
    return s != SupplyLevelScarce
}

// Order returns numeric ordering for comparison.
// Higher order = more supply available.
func (s SupplyLevel) Order() int {
    switch s {
    case SupplyLevelAbundant:
        return 5
    case SupplyLevelHigh:
        return 4
    case SupplyLevelModerate:
        return 3
    case SupplyLevelLimited:
        return 2
    case SupplyLevelScarce:
        return 1
    default:
        return 0
    }
}

// ParseSupplyLevel converts string to SupplyLevel with validation.
func ParseSupplyLevel(s string) SupplyLevel {
    switch s {
    case "ABUNDANT":
        return SupplyLevelAbundant
    case "HIGH":
        return SupplyLevelHigh
    case "MODERATE":
        return SupplyLevelModerate
    case "LIMITED":
        return SupplyLevelLimited
    case "SCARCE":
        return SupplyLevelScarce
    default:
        return SupplyLevelModerate
    }
}
```

### 1.2 Task Priority Calculator (Domain Service)

**New File:** `internal/domain/manufacturing/priority_calculator.go`

**Why:** Priority calculation in `task_assignment_manager.go:161-336` is pure business logic.

```go
package manufacturing

import (
    "time"
    "github.com/jnewman/spacetraders-go/internal/domain/shared"
)

// Priority calculation constants - domain rules
const (
    PriorityAcquireDeliver = 10  // Feed factories
    PriorityCollectSell    = 10  // Balanced with acquire_deliver
    PriorityLiquidate      = 100 // High priority - recover costs

    MaxAgingBonus      = 100 // Cap to prevent runaway priority
    AgingRatePerMinute = 2   // +2 priority per minute waiting

    // Supply-based priority boosts for ACQUIRE_DELIVER tasks
    SupplyPriorityAbundant = 30 // Best prices at abundant supply
    SupplyPriorityHigh     = 20 // Good prices
    SupplyPriorityModerate = 0  // Base priority
)

// TaskPriorityCalculator computes effective task priority using
// base priority, aging bonus, and supply-based adjustments.
type TaskPriorityCalculator struct {
    clock shared.Clock
}

// NewTaskPriorityCalculator creates a calculator with the given clock.
func NewTaskPriorityCalculator(clock shared.Clock) *TaskPriorityCalculator {
    return &TaskPriorityCalculator{clock: clock}
}

// CalculateEffectivePriority returns the task's priority including aging bonus.
// Aging prevents task starvation by gradually increasing priority over time.
func (c *TaskPriorityCalculator) CalculateEffectivePriority(task *ManufacturingTask) int {
    basePriority := task.Priority()
    agingBonus := c.calculateAgingBonus(task.CreatedAt())
    return basePriority + agingBonus
}

func (c *TaskPriorityCalculator) calculateAgingBonus(createdAt time.Time) int {
    ageMinutes := int(c.clock.Now().Sub(createdAt).Minutes())
    bonus := ageMinutes * AgingRatePerMinute
    if bonus > MaxAgingBonus {
        return MaxAgingBonus
    }
    return bonus
}

// PriorityFromSupply returns priority boost based on source market supply.
// Higher supply = better prices = higher priority to buy now.
func PriorityFromSupply(supply SupplyLevel) int {
    switch supply {
    case SupplyLevelAbundant:
        return SupplyPriorityAbundant
    case SupplyLevelHigh:
        return SupplyPriorityHigh
    default:
        return SupplyPriorityModerate
    }
}

// BasePriorityForTaskType returns the base priority for a task type.
func BasePriorityForTaskType(taskType TaskType) int {
    switch taskType {
    case TaskTypeAcquireDeliver:
        return PriorityAcquireDeliver
    case TaskTypeCollectSell:
        return PriorityCollectSell
    case TaskTypeLiquidate:
        return PriorityLiquidate
    default:
        return PriorityAcquireDeliver
    }
}
```

### 1.3 Worker Reservation Policy (Domain Service)

**New File:** `internal/domain/manufacturing/reservation_policy.go`

**Why:** Deadlock prevention logic in `task_assignment_manager.go:365-416` is a domain policy.

```go
package manufacturing

// Worker reservation constants - domain rules for deadlock prevention
const (
    MinCollectSellWorkers    = 3 // Reserve capacity for COLLECT_SELL
    MinAcquireDeliverWorkers = 3 // Reserve capacity for ACQUIRE_DELIVER
)

// TaskTypeAllocations tracks current worker assignments by task type.
type TaskTypeAllocations struct {
    CollectSellCount       int
    AcquireDeliverCount    int
    TotalWorkers           int
    HasReadyCollectSell    bool
    HasReadyAcquireDeliver bool
}

// WorkerReservationPolicy prevents task type starvation in manufacturing.
// Without reservation, one task type could monopolize all workers,
// causing deadlock (e.g., all workers doing ACQUIRE_DELIVER, nothing collecting).
type WorkerReservationPolicy struct{}

// NewWorkerReservationPolicy creates a new reservation policy.
func NewWorkerReservationPolicy() *WorkerReservationPolicy {
    return &WorkerReservationPolicy{}
}

// ShouldAssign determines if a task type should be assigned given current allocations.
// Returns true if assignment won't starve the other task type.
func (p *WorkerReservationPolicy) ShouldAssign(taskType TaskType, alloc TaskTypeAllocations) bool {
    // Calculate available capacity for each type
    availableForCollectSell := alloc.TotalWorkers - alloc.AcquireDeliverCount
    availableForAcquireDeliver := alloc.TotalWorkers - alloc.CollectSellCount

    switch taskType {
    case TaskTypeCollectSell:
        // Don't assign if it would starve ACQUIRE_DELIVER tasks
        if alloc.HasReadyAcquireDeliver && availableForAcquireDeliver <= MinAcquireDeliverWorkers {
            return false
        }
        return true

    case TaskTypeAcquireDeliver:
        // Don't assign if it would starve COLLECT_SELL tasks
        if alloc.HasReadyCollectSell && availableForCollectSell <= MinCollectSellWorkers {
            return false
        }
        return true

    case TaskTypeLiquidate:
        // Liquidation is high priority, always allow
        return true

    default:
        return true
    }
}

// CalculateReservedCapacity returns how many workers should be reserved for each type.
func (p *WorkerReservationPolicy) CalculateReservedCapacity(totalWorkers int) (collectSell, acquireDeliver int) {
    // Reserve minimum or 20% of total, whichever is greater
    reserve := totalWorkers / 5
    if reserve < MinCollectSellWorkers {
        reserve = MinCollectSellWorkers
    }
    return reserve, reserve
}
```

### 1.4 Task Readiness Specification

**New File:** `internal/domain/manufacturing/task_readiness_spec.go`

**Why:** Readiness logic scattered across `supply_monitor.go` and `task_assignment_manager.go`.

```go
package manufacturing

// ReadinessConditions holds market/factory state for readiness evaluation.
type ReadinessConditions struct {
    SourceSupply     SupplyLevel
    FactorySupply    SupplyLevel
    SellMarketSupply SupplyLevel
    DependenciesMet  bool
    IsRawMaterial    bool
    FactoryReady     bool // Has factory reached collection threshold?
}

// TaskReadinessSpecification encapsulates task readiness business rules.
// A task is ready when all preconditions for execution are met.
type TaskReadinessSpecification struct{}

// NewTaskReadinessSpecification creates a new specification.
func NewTaskReadinessSpecification() *TaskReadinessSpecification {
    return &TaskReadinessSpecification{}
}

// CanExecute returns true if the task can be executed given current conditions.
func (s *TaskReadinessSpecification) CanExecute(task *ManufacturingTask, cond ReadinessConditions) bool {
    switch task.TaskType() {
    case TaskTypeAcquireDeliver:
        return s.canExecuteAcquireDeliver(cond)
    case TaskTypeCollectSell:
        return s.canExecuteCollectSell(cond)
    case TaskTypeLiquidate:
        return true // Liquidation always allowed
    default:
        return false
    }
}

func (s *TaskReadinessSpecification) canExecuteAcquireDeliver(cond ReadinessConditions) bool {
    // Must have dependencies met (unless raw material)
    if !cond.DependenciesMet && !cond.IsRawMaterial {
        return false
    }
    // Source market must have purchasable supply
    return cond.SourceSupply.AllowsPurchase()
}

func (s *TaskReadinessSpecification) canExecuteCollectSell(cond ReadinessConditions) bool {
    // Factory must have produced output (HIGH or ABUNDANT supply)
    if !cond.FactorySupply.IsFavorableForCollection() && !cond.FactoryReady {
        return false
    }
    // Sell market should not be saturated
    return !cond.SellMarketSupply.IsSaturated()
}

// ShouldSkipSellMarket returns true if sell market is too saturated.
func (s *TaskReadinessSpecification) ShouldSkipSellMarket(supply SupplyLevel) bool {
    return supply.IsSaturated()
}
```

### 1.5 Liquidation Policy

**New File:** `internal/domain/manufacturing/liquidation_policy.go`

**Why:** `MinLiquidateCargoValue = 10000` in `orphaned_cargo_handler.go` is a domain constant.

```go
package manufacturing

// Liquidation policy constants - domain rules for orphaned cargo
const (
    // MinLiquidateCargoValue is the minimum cargo value worth liquidating.
    // Below this threshold, cargo is jettisoned instead of sold.
    MinLiquidateCargoValue = 10000

    // MaxJettisonUnits is the maximum units to jettison in one operation.
    MaxJettisonUnits = 100
)

// LiquidationPolicy determines how to handle orphaned cargo.
type LiquidationPolicy struct{}

// NewLiquidationPolicy creates a new policy.
func NewLiquidationPolicy() *LiquidationPolicy {
    return &LiquidationPolicy{}
}

// ShouldLiquidate returns true if cargo is valuable enough to sell.
func (p *LiquidationPolicy) ShouldLiquidate(estimatedValue int) bool {
    return estimatedValue >= MinLiquidateCargoValue
}

// ShouldJettison returns true if cargo should be discarded.
func (p *LiquidationPolicy) ShouldJettison(estimatedValue int) bool {
    return estimatedValue < MinLiquidateCargoValue
}
```

### 1.6 Enrich Task Entity

**Modify:** `internal/domain/manufacturing/task.go`

Add methods that consolidate business logic:

```go
// GetFirstDestination returns the initial destination waypoint for the task.
// For ACQUIRE_DELIVER: source market, For COLLECT_SELL: factory, For LIQUIDATE: best sell market.
func (t *ManufacturingTask) GetFirstDestination() string {
    switch t.taskType {
    case TaskTypeAcquireDeliver:
        return t.sourceMarket
    case TaskTypeCollectSell, TaskTypeLiquidate:
        return t.factorySymbol
    default:
        return t.sourceMarket
    }
}

// RequiresHighFactorySupply returns true if this task type needs high factory supply.
func (t *ManufacturingTask) RequiresHighFactorySupply() bool {
    return t.taskType == TaskTypeCollectSell
}

// RequiresSellMarketCheck returns true if task needs sell market saturation check.
func (t *ManufacturingTask) RequiresSellMarketCheck() bool {
    return t.taskType == TaskTypeCollectSell
}

// IsSupplyGated returns true if task execution depends on supply levels.
func (t *ManufacturingTask) IsSupplyGated() bool {
    return t.taskType == TaskTypeAcquireDeliver || t.taskType == TaskTypeCollectSell
}

// GetFinalDestination returns where the task delivers goods.
func (t *ManufacturingTask) GetFinalDestination() string {
    switch t.taskType {
    case TaskTypeAcquireDeliver:
        return t.factorySymbol // Deliver to factory
    case TaskTypeCollectSell, TaskTypeLiquidate:
        return t.targetMarket // Sell at market
    default:
        return t.targetMarket
    }
}
```

---

## Phase 2: Service Decomposition

### 2.1 Decompose TaskAssignmentManager (616 -> ~150 lines)

**Current Responsibilities (7):**
1. Task assignment logic
2. Supply checking (market saturation)
3. Factory supply favorability
4. Ship selection (closest algorithm)
5. In-memory state tracking (assignedTasks map)
6. Database reconciliation
7. Task type reservation logic

**Extract into:**

| New File | Responsibility | ~Lines |
|----------|----------------|--------|
| `ship_selector.go` | Find closest ship to waypoint | 50 |
| `assignment_tracker.go` | In-memory assignment state | 60 |
| `assignment_reconciler.go` | Sync state with database | 80 |
| `market_condition_checker.go` | Supply/saturation queries | 100 |

**ship_selector.go:**
```go
type ShipSelector struct {
    shipRepo navigation.ShipRepository
}

func (s *ShipSelector) FindClosestShip(
    ships map[string]*navigation.Ship,
    target *shared.Waypoint,
) (*navigation.Ship, string)

func (s *ShipSelector) GetTaskSourceLocation(
    ctx context.Context,
    task *manufacturing.ManufacturingTask,
    playerID int,
) *shared.Waypoint
```

**assignment_tracker.go:**
```go
type AssignmentTracker struct {
    mu            sync.RWMutex
    assignedTasks map[string]string            // taskID -> shipSymbol
    tasksByShip   map[string]string            // shipSymbol -> taskID
    tasksByType   map[manufacturing.TaskType]int // type -> count
}

func (t *AssignmentTracker) Track(taskID, shipSymbol string, taskType manufacturing.TaskType)
func (t *AssignmentTracker) Untrack(taskID string)
func (t *AssignmentTracker) GetAllocations() manufacturing.TaskTypeAllocations
func (t *AssignmentTracker) IsShipAssigned(shipSymbol string) bool
```

**market_condition_checker.go:**
```go
type MarketConditionChecker struct {
    marketRepo market.MarketRepository
    readinessSpec *manufacturing.TaskReadinessSpecification
}

func (c *MarketConditionChecker) IsSellMarketSaturated(ctx context.Context, market, good string, playerID int) bool
func (c *MarketConditionChecker) IsFactorySupplyFavorable(ctx context.Context, factory, good string, playerID int) bool
func (c *MarketConditionChecker) GetReadinessConditions(ctx context.Context, task *manufacturing.ManufacturingTask, playerID int) manufacturing.ReadinessConditions
```

### 2.2 Decompose PipelineLifecycleManager (846 -> ~100 lines)

**Current Responsibilities (8):**
1. Pipeline creation (FABRICATION)
2. Pipeline creation (COLLECTION)
3. Completion detection
4. Stuck detection
5. Task rescue
6. Market saturation checks
7. Active pipeline tracking
8. Callback management

**Extract into:**

| New File | Responsibility | ~Lines |
|----------|----------------|--------|
| `pipeline_creator.go` | Create both pipeline types | 200 |
| `pipeline_completion_checker.go` | Detect completion | 150 |
| `pipeline_recycler.go` | Stuck detection, recycling | 120 |
| `task_rescuer.go` | Re-enqueue stale tasks | 100 |
| `active_pipeline_registry.go` | Track active pipelines | 80 |

**active_pipeline_registry.go:**
```go
type ActivePipelineRegistry struct {
    mu        sync.RWMutex
    pipelines map[string]*manufacturing.ManufacturingPipeline
}

func (r *ActivePipelineRegistry) Register(pipeline *manufacturing.ManufacturingPipeline)
func (r *ActivePipelineRegistry) Unregister(pipelineID string)
func (r *ActivePipelineRegistry) GetAll() map[string]*manufacturing.ManufacturingPipeline
func (r *ActivePipelineRegistry) HasPipelineForGood(good string) bool
func (r *ActivePipelineRegistry) Count() int
```

---

## Phase 3: Dependency Injection

### 3.1 Problem: Runtime Callback Setters

Current anti-pattern creates circular dependencies:

```go
// WorkerLifecycleManager
func (m *WorkerLifecycleManager) SetTaskAssigner(ta TaskAssigner)
func (m *WorkerLifecycleManager) SetFactoryManager(fm FactoryManager)
func (m *WorkerLifecycleManager) SetPipelineManager(pm PipelineManager)

// TaskAssignmentManager
func (m *TaskAssignmentManager) SetWorkerManager(wm WorkerManager)
func (m *TaskAssignmentManager) SetActivePipelinesGetter(fn func() map[string]*manufacturing.ManufacturingPipeline)
```

### 3.2 Solution: Coordinator Pattern

**New File:** `internal/application/trading/services/manufacturing/manufacturing_coordinator.go`

```go
// CoordinatorDependencies groups all external dependencies.
type CoordinatorDependencies struct {
    // Repositories
    PipelineRepo     manufacturing.PipelineRepository
    TaskRepo         manufacturing.TaskRepository
    FactoryStateRepo manufacturing.FactoryStateRepository
    ShipRepo         navigation.ShipRepository
    MarketRepo       market.MarketRepository

    // External services
    Mediator         common.Mediator
    DaemonClient     *grpc.DaemonClient
    ContainerRemover ContainerRemover
    WaypointProvider system.IWaypointProvider

    // Domain services
    Clock            shared.Clock

    // Finders
    DemandFinder               *services.ManufacturingDemandFinder
    CollectionOpportunityFinder *services.CollectionOpportunityFinder
    PipelinePlanner            *services.PipelinePlanner
}

// ManufacturingCoordinator is the composition root for manufacturing services.
// It constructs all services with proper dependency injection and eliminates circular deps.
type ManufacturingCoordinator struct {
    // Shared state (no circular deps)
    registry *ActivePipelineRegistry
    tracker  *AssignmentTracker
    taskQueue *services.TaskQueue

    // Domain services
    priorityCalculator *manufacturing.TaskPriorityCalculator
    reservationPolicy  *manufacturing.WorkerReservationPolicy
    readinessSpec      *manufacturing.TaskReadinessSpecification

    // Focused services (inject what they need at construction)
    shipSelector       *ShipSelector
    conditionChecker   *MarketConditionChecker
    reconciler         *AssignmentReconciler

    // Managers (built last, receive focused services)
    pipelineCreator    *PipelineCreator
    completionChecker  *PipelineCompletionChecker
    recycler           *PipelineRecycler
    taskRescuer        *TaskRescuer
    taskAssignment     *TaskAssignmentManager
    workerLifecycle    *WorkerLifecycleManager
    stateRecovery      *StateRecoveryManager
    orphanedCargo      *OrphanedCargoHandler
    factoryState       *FactoryStateManager
}

// NewManufacturingCoordinator constructs and wires all services.
// Order matters: build leaf services first, then composites.
func NewManufacturingCoordinator(deps CoordinatorDependencies) *ManufacturingCoordinator {
    c := &ManufacturingCoordinator{}

    // 1. Shared state (no deps)
    c.registry = NewActivePipelineRegistry()
    c.tracker = NewAssignmentTracker()
    c.taskQueue = services.NewTaskQueue()

    // 2. Domain services
    c.priorityCalculator = manufacturing.NewTaskPriorityCalculator(deps.Clock)
    c.reservationPolicy = manufacturing.NewWorkerReservationPolicy()
    c.readinessSpec = manufacturing.NewTaskReadinessSpecification()

    // 3. Focused services
    c.shipSelector = NewShipSelector(deps.ShipRepo)
    c.conditionChecker = NewMarketConditionChecker(deps.MarketRepo, c.readinessSpec)
    c.reconciler = NewAssignmentReconciler(deps.TaskRepo, c.tracker)

    // 4. Managers (receive focused services, no callbacks needed)
    c.pipelineCreator = NewPipelineCreator(
        deps.DemandFinder,
        deps.CollectionOpportunityFinder,
        deps.PipelinePlanner,
        deps.PipelineRepo,
        c.taskQueue,
        c.registry,
    )

    c.completionChecker = NewPipelineCompletionChecker(
        deps.PipelineRepo,
        deps.TaskRepo,
        c.registry,
    )

    c.taskAssignment = NewTaskAssignmentManager(
        c.taskQueue,
        c.tracker,
        c.shipSelector,
        c.conditionChecker,
        c.reconciler,
        c.reservationPolicy,
        c.priorityCalculator,
        deps.ShipRepo,
        deps.TaskRepo,
        c.registry.GetAll, // Read-only getter, not a callback
    )

    // ... remaining managers

    return c
}

// GetTaskAssigner returns the task assignment interface.
func (c *ManufacturingCoordinator) GetTaskAssigner() TaskAssigner {
    return c.taskAssignment
}

// GetPipelineCreator returns the pipeline creation interface.
func (c *ManufacturingCoordinator) GetPipelineCreator() PipelineCreator {
    return c.pipelineCreator
}

// etc...
```

---

## Phase 4: Interface Refinement (ISP)

### 4.1 Split TaskAssigner Interface

**Before (6 methods):**
```go
type TaskAssigner interface {
    AssignTasks(ctx, params) (int, error)
    GetTaskSourceLocation(ctx, task, playerID) *Waypoint
    FindClosestShip(ships, target) (*Ship, string)
    IsSellMarketSaturated(ctx, market, good, playerID) bool
    IsFactorySupplyFavorable(ctx, factory, good, playerID) bool
    ReconcileAssignedTasksWithDB(ctx, playerID) error
}
```

**After (3 focused interfaces):**
```go
// TaskAssigner assigns tasks to ships.
type TaskAssigner interface {
    AssignTasks(ctx context.Context, params AssignParams) (int, error)
}

// ShipLocator finds ships for task assignment.
type ShipLocator interface {
    FindClosestShip(ships map[string]*navigation.Ship, target *shared.Waypoint) (*navigation.Ship, string)
    GetTaskSourceLocation(ctx context.Context, task *manufacturing.ManufacturingTask, playerID int) *shared.Waypoint
}

// MarketChecker evaluates market conditions.
type MarketChecker interface {
    IsSellMarketSaturated(ctx context.Context, market, good string, playerID int) bool
    IsFactorySupplyFavorable(ctx context.Context, factory, good string, playerID int) bool
}
```

### 4.2 Split PipelineManager Interface

**Before (8+ methods):**
```go
type PipelineManager interface {
    ScanAndCreatePipelines(ctx, params) (int, error)
    CheckPipelineCompletion(ctx, pipelineID) (bool, error)
    RecyclePipeline(ctx, pipelineID, playerID) error
    DetectAndRecycleStuckPipelines(ctx, playerID) error
    RescueReadyCollectSellTasks(ctx, playerID) error
    HasPipelineForGood(good string) bool
    GetActivePipelines() map[string]*Pipeline
    SetActivePipelinesGetter(fn func() map[string]*Pipeline)
}
```

**After (4 focused interfaces):**
```go
// PipelineCreator creates new manufacturing pipelines.
type PipelineCreator interface {
    ScanAndCreatePipelines(ctx context.Context, params ScanParams) (int, error)
}

// PipelineCompletionChecker detects and marks complete pipelines.
type PipelineCompletionChecker interface {
    CheckPipelineCompletion(ctx context.Context, pipelineID string) (bool, error)
}

// PipelineRecycler handles stuck pipeline recovery.
type PipelineRecycler interface {
    DetectStuckPipelines(ctx context.Context, playerID int) ([]string, error)
    RecyclePipeline(ctx context.Context, pipelineID string, playerID int) error
}

// PipelineRegistry tracks active pipelines.
type PipelineRegistry interface {
    GetAll() map[string]*manufacturing.ManufacturingPipeline
    HasPipelineForGood(good string) bool
    Count() int
}
```

---

## Phase 5: Self-Documenting Code

### 5.1 Comment-to-Method Transformations

| Location | Comment | New Method |
|----------|---------|------------|
| `purchaser.go:95-98` | "PRE-CHECK: If ship already has cargo..." | `shipAlreadyHasRequiredCargo(ship, good) bool` |
| `purchaser.go:114-151` | Long supply check comment | Use `readinessSpec.CanExecute()` |
| `task_assignment_manager.go:242-246` | "Task Type Reservation..." | Use `reservationPolicy.ShouldAssign()` |
| `state_recovery_manager.go:104-118` | "CRITICAL: Skip orphaned tasks..." | `isOrphanedTask(task) bool` |
| `pipeline_lifecycle_manager.go:573-575` | "Time-based detection removed" | Remove comment (document in commit) |
| `orphaned_cargo_handler.go:156-188` | Long task matching comment | `findMatchingManufacturingTask(ship) *Task` |

### 5.2 Method Naming Improvements

| Current | Improved | Reason |
|---------|----------|--------|
| `ReconcileAssignedTasksWithDB` | `SyncAssignmentsFromDatabase` | Clearer action |
| `CheckPipelineCompletion` | `CheckAndTransitionToTerminal` | Describes side effect |
| `RescueReadyCollectSellTasks` | `RevalidateAndEnqueueStaleTasks` | More accurate |
| `HandleShipsWithExistingCargo` | `ProcessOrphanedCargo` | Domain terminology |
| `DetectAndRecycleStuckPipelines` | `RecoverStuckPipelines` | Single concept |

### 5.3 Type Assertion Elimination

**Before (DIP violation):**
```go
// orphaned_cargo_handler.go:102
h.taskAssigner.(*TaskAssignmentManager).GetAssignmentCount()

// seller.go:249-250
mlr, ok := s.ledgerRecorder.(*ManufacturingLedgerRecorder)
```

**After:**
```go
// Add method to interface
type TaskAssigner interface {
    AssignTasks(ctx context.Context, params AssignParams) (int, error)
    GetAssignmentCount() int // Added
}

// Or use separate interface
type AssignmentCounter interface {
    GetAssignmentCount() int
}
```

---

## Implementation Order

### Stage 1: Domain Foundation (Low Risk)
1. Create `supply_level.go`
2. Create `priority_calculator.go`
3. Create `reservation_policy.go`
4. Create `task_readiness_spec.go`
5. Create `liquidation_policy.go`
6. Add methods to `task.go`

**Validation:** Build compiles, existing code unchanged.

### Stage 2: Service Extraction (Medium Risk)
1. Create `ship_selector.go`
2. Create `assignment_tracker.go`
3. Create `market_condition_checker.go`
4. Create `assignment_reconciler.go`
5. Create `active_pipeline_registry.go`
6. Create `pipeline_creator.go`
7. Create `pipeline_completion_checker.go`
8. Create `pipeline_recycler.go`
9. Create `task_rescuer.go`

**Validation:** All new services have constructors, compile without managers.

### Stage 3: Rewiring (Higher Risk)
1. Refactor `TaskAssignmentManager` to use new services
2. Refactor `PipelineLifecycleManager` to use new services
3. Create `ManufacturingCoordinator`
4. Remove all `Set*` callback methods
5. Update gRPC handler to use coordinator

**Validation:** Manufacturing operations work end-to-end.

### Stage 4: Polish (Low Risk)
1. Split interfaces per Phase 4
2. Apply naming improvements
3. Eliminate type assertions
4. Remove unnecessary comments

---

## Files Summary

### New Domain Files (5)
- `internal/domain/manufacturing/supply_level.go`
- `internal/domain/manufacturing/priority_calculator.go`
- `internal/domain/manufacturing/reservation_policy.go`
- `internal/domain/manufacturing/task_readiness_spec.go`
- `internal/domain/manufacturing/liquidation_policy.go`

### New Application Files (10)
- `internal/application/trading/services/manufacturing/ship_selector.go`
- `internal/application/trading/services/manufacturing/assignment_tracker.go`
- `internal/application/trading/services/manufacturing/assignment_reconciler.go`
- `internal/application/trading/services/manufacturing/market_condition_checker.go`
- `internal/application/trading/services/manufacturing/active_pipeline_registry.go`
- `internal/application/trading/services/manufacturing/pipeline_creator.go`
- `internal/application/trading/services/manufacturing/pipeline_completion_checker.go`
- `internal/application/trading/services/manufacturing/pipeline_recycler.go`
- `internal/application/trading/services/manufacturing/task_rescuer.go`
- `internal/application/trading/services/manufacturing/manufacturing_coordinator.go`

### Modified Files (6)
- `internal/domain/manufacturing/task.go` - Add helper methods
- `internal/application/trading/services/manufacturing/task_assignment_manager.go` - Simplify
- `internal/application/trading/services/manufacturing/pipeline_lifecycle_manager.go` - Simplify
- `internal/application/trading/services/manufacturing/worker_lifecycle_manager.go` - Remove setters
- `internal/application/trading/services/manufacturing/purchaser.go` - Use SupplyLevel
- `internal/adapters/grpc/container_ops_manufacturing.go` - Use coordinator

---

## Metrics

| Metric | Before | After | Change |
|--------|--------|-------|--------|
| Domain layer lines | 1,899 | ~2,400 | +26% |
| TaskAssignmentManager | 616 | ~150 | -76% |
| PipelineLifecycleManager | 846 | ~100 | -88% |
| Circular dependencies | 6+ | 0 | -100% |
| Large interfaces (5+ methods) | 2 | 0 | -100% |
| Type assertions | 4 | 0 | -100% |
| Business logic in app layer | High | Low | Improved |
