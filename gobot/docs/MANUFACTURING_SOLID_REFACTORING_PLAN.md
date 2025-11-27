# Manufacturing System SOLID Refactoring Plan

## Overview

This document outlines the refactoring of two large manufacturing system files to follow SOLID principles:

| File | Current | Target | Reduction |
|------|---------|--------|-----------|
| `run_manufacturing_task_worker.go` | 916 lines | ~100 lines | 89% |
| `run_parallel_manufacturing_coordinator.go` | 2167 lines | ~400 lines | 82% |

**Location for new services:** `internal/application/trading/services/manufacturing/`

## Current SOLID Violations

### Task Worker (916 lines)

| Principle | Violation |
|-----------|-----------|
| **SRP** | Handler does task dispatching, navigation, docking, market queries, purchase loops, selling, ledger recording, metrics |
| **OCP** | Switch statement for task types requires modification to add new types |
| **DRY** | Navigate+dock pattern repeated 5x, purchase loop duplicated, ledger recording repeated 5x |

### Coordinator (2167 lines)

| Principle | Violation |
|-----------|-----------|
| **SRP** | Handles pipeline management, task assignment, ship discovery, worker lifecycle, factory state, state recovery, stuck detection, orphaned cargo handling |
| **OCP** | Hardcoded task type logic scattered throughout |

## Target Architecture

### Strategy Pattern for Task Execution

```
                    TaskExecutor (Interface)
                           │
          ┌────────────────┼────────────────┐
          │                │                │
  AcquireDeliverExecutor   CollectSellExecutor   LiquidateExecutor
```

### Service Extraction

```
┌─────────────────────────────────┐    ┌─────────────────────────────────────┐
│  TaskWorkerHandler (thin)       │    │  CoordinatorHandler (thin)          │
│         (~100 lines)            │    │         (~400 lines)                │
├─────────────────────────────────┤    ├─────────────────────────────────────┤
│  ├── TaskExecutorRegistry       │    │  ├── PipelineLifecycleManager       │
│  ├── ManufacturingNavigator     │    │  ├── TaskAssignmentManager          │
│  ├── ManufacturingPurchaser     │    │  ├── WorkerLifecycleManager         │
│  ├── ManufacturingSeller        │    │  ├── StateRecoveryManager           │
│  └── ManufacturingLedgerRecorder│    │  ├── OrphanedCargoHandler           │
└─────────────────────────────────┘    │  └── FactoryStateManager            │
                                       └─────────────────────────────────────┘
```

## Implementation Phases

### Phase 1: ManufacturingNavigator (Low Complexity)

**File:** `internal/application/trading/services/manufacturing/navigator.go` (~80 lines)

**Responsibility:** Navigate ships and dock at waypoints for manufacturing operations.

```go
type Navigator interface {
    // NavigateAndDock navigates to destination and docks in one operation
    NavigateAndDock(ctx context.Context, shipSymbol, destination string, playerID shared.PlayerID) (*navigation.Ship, error)

    // NavigateTo navigates without docking
    NavigateTo(ctx context.Context, shipSymbol, destination string, playerID shared.PlayerID) error

    // Dock docks the ship at current location
    Dock(ctx context.Context, shipSymbol string, playerID shared.PlayerID) error

    // ReloadShip fetches fresh ship state from repository
    ReloadShip(ctx context.Context, shipSymbol string, playerID shared.PlayerID) (*navigation.Ship, error)
}

type ManufacturingNavigator struct {
    mediator common.Mediator
    shipRepo navigation.ShipRepository
}
```

**Consolidates code from:**
- `executeLiquidate` navigation (lines 230-262)
- `executeAcquireDeliver` source navigation (lines 387-410)
- `executeAcquireDeliver` factory navigation (lines 535-559)
- `executeCollectSell` factory navigation (lines 659-683)
- `executeCollectSell` market navigation (lines 828-853)

---

### Phase 2: ManufacturingLedgerRecorder (Low Complexity)

**File:** `internal/application/trading/services/manufacturing/ledger_recorder.go` (~100 lines)

**Responsibility:** Record manufacturing transactions in the ledger.

```go
type LedgerRecorder interface {
    RecordPurchase(ctx context.Context, params PurchaseRecordParams) error
    RecordSale(ctx context.Context, params SaleRecordParams) error
    RecordDelivery(ctx context.Context, params DeliveryRecordParams) error
}

type PurchaseRecordParams struct {
    PlayerID      int
    TaskID        string
    Good          string
    Quantity      int
    PricePerUnit  int
    TotalCost     int
    SourceMarket  string
    Factory       string
    Description   string
}

type SaleRecordParams struct {
    PlayerID      int
    TaskID        string
    Good          string
    Quantity      int
    PricePerUnit  int
    TotalRevenue  int
    Market        string
    NetProfit     int
    Description   string
}
```

**Consolidates code from:** Lines 303-318, 501-517, 602-617, 796-811, 897-913

---

### Phase 3: ManufacturingPurchaser (Medium Complexity)

**File:** `internal/application/trading/services/manufacturing/purchaser.go` (~150 lines)

**Responsibility:** Execute supply-aware purchase loops.

```go
type Purchaser interface {
    // ExecutePurchaseLoop performs iterative purchasing until cargo full or limit reached
    ExecutePurchaseLoop(ctx context.Context, params PurchaseLoopParams) (*PurchaseResult, error)

    // CalculateSupplyAwareLimit determines safe purchase quantity based on supply level
    CalculateSupplyAwareLimit(supply string, tradeVolume int) int
}

type PurchaseLoopParams struct {
    ShipSymbol        string
    PlayerID          shared.PlayerID
    Good              string
    TaskID            string
    DesiredQty        int     // 0 = fill cargo
    Market            string
    Factory           string  // For ledger context
    RequireHighSupply bool    // For COLLECT_SELL: require HIGH/ABUNDANT
}

type PurchaseResult struct {
    TotalUnitsAdded int
    TotalCost       int
    Rounds          int
}
```

**Supply-Aware Multipliers:**
| Supply Level | Multiplier | Description |
|--------------|------------|-------------|
| ABUNDANT | 0.80 | Plenty of buffer |
| HIGH | 0.60 | Sweet spot - maintain stability |
| MODERATE | 0.40 | Careful - could drop to LIMITED |
| LIMITED | 0.20 | Very careful - critical supply |
| SCARCE | 0.10 | Minimal - supply nearly depleted |

**Consolidates code from:**
- `calculateSupplyAwareLimit` (lines 326-346)
- `executeAcquireDeliver` purchase loop (lines 412-522)
- `executeCollectSell` purchase loop (lines 685-816)

---

### Phase 4: ManufacturingSeller (Low Complexity)

**File:** `internal/application/trading/services/manufacturing/seller.go` (~80 lines)

**Responsibility:** Execute cargo selling operations.

```go
type Seller interface {
    // SellCargo sells cargo at current market
    SellCargo(ctx context.Context, params SellParams) (*SellResult, error)

    // DeliverToFactory sells cargo to factory (delivering inputs)
    DeliverToFactory(ctx context.Context, params SellParams) (*SellResult, error)
}

type SellParams struct {
    ShipSymbol string
    PlayerID   shared.PlayerID
    Good       string
    Quantity   int
    TaskID     string
    Market     string // For ledger context
}

type SellResult struct {
    UnitsSold    int
    TotalRevenue int
    PricePerUnit int
}
```

**Consolidates code from:** Lines 274-300, 572-599, 866-893

---

### Phase 5: TaskExecutor Strategy Pattern (Medium Complexity)

**Files:**
- `internal/application/trading/services/manufacturing/task_executor.go` (~50 lines)
- `internal/application/trading/services/manufacturing/acquire_deliver_executor.go` (~100 lines)
- `internal/application/trading/services/manufacturing/collect_sell_executor.go` (~100 lines)
- `internal/application/trading/services/manufacturing/liquidate_executor.go` (~50 lines)

**Strategy Interface:**
```go
// TaskExecutor executes a specific type of manufacturing task.
// Implements the Strategy pattern for task type dispatch.
type TaskExecutor interface {
    Execute(ctx context.Context, params TaskExecutionParams) error
    TaskType() manufacturing.TaskType
}

type TaskExecutionParams struct {
    Task        *manufacturing.ManufacturingTask
    ShipSymbol  string
    PlayerID    int
    ContainerID string
}
```

**Registry (Open/Closed Principle):**
```go
// TaskExecutorRegistry maps task types to their executors.
// Add new executors without modifying existing code.
type TaskExecutorRegistry struct {
    executors map[manufacturing.TaskType]TaskExecutor
}

func NewTaskExecutorRegistry() *TaskExecutorRegistry {
    return &TaskExecutorRegistry{
        executors: make(map[manufacturing.TaskType]TaskExecutor),
    }
}

func (r *TaskExecutorRegistry) Register(executor TaskExecutor) {
    r.executors[executor.TaskType()] = executor
}

func (r *TaskExecutorRegistry) GetExecutor(taskType manufacturing.TaskType) (TaskExecutor, error) {
    executor, ok := r.executors[taskType]
    if !ok {
        return nil, fmt.Errorf("no executor registered for task type: %s", taskType)
    }
    return executor, nil
}
```

**AcquireDeliverExecutor:**
```go
type AcquireDeliverExecutor struct {
    navigator  Navigator
    purchaser  Purchaser
    seller     Seller
    shipRepo   navigation.ShipRepository
}

func (e *AcquireDeliverExecutor) TaskType() manufacturing.TaskType {
    return manufacturing.TaskTypeAcquireDeliver
}

func (e *AcquireDeliverExecutor) Execute(ctx context.Context, params TaskExecutionParams) error {
    // Phase 1: Idempotent check - already has cargo? Skip to delivery
    // Phase 2: Navigate to source market, execute purchase loop
    // Phase 3: Navigate to factory, deliver goods
}
```

---

### Phase 6: Simplify Task Worker Handler (Low Complexity)

**Refactor:** `run_manufacturing_task_worker.go` → ~100 lines

```go
type RunManufacturingTaskWorkerHandler struct {
    executorRegistry *TaskExecutorRegistry
    taskRepo         manufacturing.TaskRepository
}

func (h *RunManufacturingTaskWorkerHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
    cmd := request.(*RunManufacturingTaskWorkerCommand)
    task := cmd.Task
    startTime := time.Now()

    // Create operation context for ledger tracking
    if cmd.ContainerID != "" {
        opContext := shared.NewOperationContext(cmd.ContainerID, "manufacturing_worker")
        ctx = shared.WithOperationContext(ctx, opContext)
    }

    // Mark task as executing
    if err := task.StartExecution(); err != nil {
        return h.failResponse(task, startTime, err.Error()), nil
    }

    // Get executor for task type (Strategy pattern - OCP compliant)
    executor, err := h.executorRegistry.GetExecutor(task.TaskType())
    if err != nil {
        task.Fail(err.Error())
        return h.failResponse(task, startTime, err.Error()), nil
    }

    // Execute task
    err = executor.Execute(ctx, TaskExecutionParams{
        Task:        task,
        ShipSymbol:  cmd.ShipSymbol,
        PlayerID:    cmd.PlayerID,
        ContainerID: cmd.ContainerID,
    })

    if err != nil {
        task.Fail(err.Error())
        h.persistTask(ctx, task)
        metrics.RecordManufacturingTaskCompletion(cmd.PlayerID, string(task.TaskType()), "failed", time.Since(startTime))
        return h.failResponse(task, startTime, err.Error()), nil
    }

    // Mark complete and persist
    task.Complete()
    h.persistTask(ctx, task)

    // Record metrics
    metrics.RecordManufacturingTaskCompletion(cmd.PlayerID, string(task.TaskType()), "completed", time.Since(startTime))

    return h.successResponse(task, startTime), nil
}
```

---

### Phase 7: PipelineLifecycleManager (Medium Complexity)

**File:** `internal/application/trading/services/manufacturing/pipeline_lifecycle_manager.go` (~300 lines)

**Responsibility:** Manage pipeline lifecycle (create, complete, cancel, recycle).

```go
type PipelineManager interface {
    // ScanAndCreatePipelines finds opportunities and creates new pipelines
    ScanAndCreatePipelines(ctx context.Context, params ScanParams) (int, error)

    // CheckPipelineCompletion checks if pipeline is complete and updates status
    CheckPipelineCompletion(ctx context.Context, pipelineID string) (bool, error)

    // RecyclePipeline cancels a stuck pipeline and frees its slot
    RecyclePipeline(ctx context.Context, pipelineID string, playerID int) error

    // DetectStuckPipelines finds pipelines not making progress
    DetectStuckPipelines(ctx context.Context, playerID int) ([]string, error)

    // HasPipelineForGood checks if active pipeline exists for this good
    HasPipelineForGood(good string) bool

    // GetActivePipelines returns all active pipelines
    GetActivePipelines() map[string]*manufacturing.ManufacturingPipeline
}

type ScanParams struct {
    SystemSymbol     string
    PlayerID         int
    MinPurchasePrice int
    MaxPipelines     int
}
```

**Consolidates code from:** Lines 313-441, 443-454, 1571-1721, 1996-2166

---

### Phase 8: TaskAssignmentManager (Medium Complexity)

**File:** `internal/application/trading/services/manufacturing/task_assignment_manager.go` (~200 lines)

**Responsibility:** Assign ready tasks to idle ships.

```go
type TaskAssigner interface {
    // AssignTasks assigns ready tasks to idle ships
    AssignTasks(ctx context.Context, params AssignParams) (int, error)

    // GetTaskSourceLocation returns the waypoint where task starts
    GetTaskSourceLocation(ctx context.Context, task *manufacturing.ManufacturingTask, playerID int) *shared.Waypoint

    // FindClosestShip finds the ship closest to target waypoint
    FindClosestShip(ships map[string]*navigation.Ship, target *shared.Waypoint) (*navigation.Ship, string)

    // IsSellMarketSaturated checks if market has HIGH/ABUNDANT supply
    IsSellMarketSaturated(ctx context.Context, sellMarket, good string, playerID int) bool
}

type AssignParams struct {
    PlayerID           int
    MaxConcurrentTasks int
    TaskQueue          *TaskQueue
    ActivePipelines    map[string]*manufacturing.ManufacturingPipeline
}
```

**Consolidates code from:** Lines 456-621, 623-655, 657-670, 672-699, 1971-1988

---

### Phase 9: WorkerLifecycleManager (Medium Complexity)

**File:** `internal/application/trading/services/manufacturing/worker_lifecycle_manager.go` (~250 lines)

**Responsibility:** Manage worker container lifecycle.

```go
type WorkerManager interface {
    // AssignTaskToShip creates worker container and assigns ship
    AssignTaskToShip(ctx context.Context, params AssignTaskParams) (string, error)

    // HandleWorkerCompletion processes worker container completion
    HandleWorkerCompletion(ctx context.Context, shipSymbol string) (*TaskCompletion, error)

    // HandleTaskFailure processes failed task (retry or mark failed)
    HandleTaskFailure(ctx context.Context, completion TaskCompletion) error
}

type AssignTaskParams struct {
    Task           *manufacturing.ManufacturingTask
    Ship           *navigation.Ship
    PlayerID       int
    ContainerID    string
    CoordinatorID  string
    PipelineNumber int
    ProductGood    string
}

type TaskCompletion struct {
    TaskID     string
    ShipSymbol string
    PipelineID string
    Success    bool
    Error      error
}
```

**Consolidates code from:** Lines 945-1060, 1062-1137, 1139-1201, 1203-1247, 1249-1326

---

### Phase 10: StateRecoveryManager (Medium Complexity)

**File:** `internal/application/trading/services/manufacturing/state_recovery_manager.go` (~200 lines)

**Responsibility:** Recover coordinator state from database after restart.

```go
type StateRecoverer interface {
    // RecoverState loads pipelines, tasks, and factory states from database
    RecoverState(ctx context.Context, playerID int) (*RecoveryResult, error)
}

type RecoveryResult struct {
    ActivePipelines  map[string]*manufacturing.ManufacturingPipeline
    ReadyTaskCount   int
    InterruptedCount int
    RetriedCount     int
}
```

**Recovery Steps:**
1. Load incomplete pipelines (PLANNING, EXECUTING)
2. Start any PLANNING pipelines
3. Reset interrupted tasks (ASSIGNED, EXECUTING) → READY
4. Re-evaluate PENDING tasks for readiness
5. Recover FAILED tasks that can be retried
6. Load factory states (pending and ready)

**Consolidates code from:** Lines 1724-1945

---

### Phase 11: OrphanedCargoHandler (Medium Complexity)

**File:** `internal/application/trading/services/manufacturing/orphaned_cargo_handler.go` (~200 lines)

**Responsibility:** Handle ships with cargo from interrupted operations.

```go
type OrphanedCargoManager interface {
    // HandleShipsWithExistingCargo processes idle ships that have cargo
    HandleShipsWithExistingCargo(ctx context.Context, params OrphanedCargoParams) (map[string]*navigation.Ship, error)

    // FindBestSellMarket finds best market to sell orphaned cargo
    FindBestSellMarket(ctx context.Context, currentLocation, good string, playerID int) (string, error)

    // CreateAdHocSellTask creates COLLECT_SELL task for orphaned cargo
    CreateAdHocSellTask(ctx context.Context, ship *navigation.Ship, cargo CargoInfo, playerID int) (*manufacturing.ManufacturingTask, error)
}

type OrphanedCargoParams struct {
    IdleShips          map[string]*navigation.Ship
    PlayerID           int
    MaxConcurrentTasks int
    ActivePipelines    []string
}

type CargoInfo struct {
    Good  string
    Units int
}
```

**Consolidates code from:** Lines 701-943, 1949-1967

---

### Phase 12: FactoryStateManager (Low Complexity)

**File:** `internal/application/trading/services/manufacturing/factory_state_manager.go` (~150 lines)

**Responsibility:** Manage factory state updates and task dependencies.

```go
type FactoryManager interface {
    // UpdateFactoryStateOnDelivery records delivery and updates factory state
    UpdateFactoryStateOnDelivery(ctx context.Context, taskID, shipSymbol, pipelineID string) error

    // CreateContinuedDeliveryTasks creates new ACQUIRE_DELIVER tasks when factory not ready
    CreateContinuedDeliveryTasks(ctx context.Context, completedTask *manufacturing.ManufacturingTask, pipelineID, factorySymbol string) error

    // UpdateDependentTasks marks tasks as READY when dependencies complete
    UpdateDependentTasks(ctx context.Context, completedTaskID, pipelineID string) error
}
```

**Consolidates code from:** Lines 1328-1408, 1413-1482, 1487-1559

---

### Phase 13: Simplify Coordinator Handler (Low Complexity)

**Refactor:** `run_parallel_manufacturing_coordinator.go` → ~400 lines

The coordinator becomes a thin orchestrator that:
1. Initializes all services
2. Runs the main event loop
3. Delegates to appropriate service for each event type

```go
type RunParallelManufacturingCoordinatorHandler struct {
    // Services (injected)
    pipelineManager    PipelineManager
    taskAssigner       TaskAssigner
    workerManager      WorkerManager
    stateRecoverer     StateRecoverer
    orphanedHandler    OrphanedCargoManager
    factoryManager     FactoryManager

    // Existing services
    supplyMonitor      *services.SupplyMonitor
    taskQueue          *services.TaskQueue

    // Channels
    completionChan     chan TaskCompletion
    workerCompletionChan chan string
    taskReadyChan      chan struct{}
}

func (h *RunParallelManufacturingCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
    cmd := request.(*RunParallelManufacturingCoordinatorCommand)

    // Initialize with defaults
    config := h.applyDefaults(cmd)

    // Recover state from database
    recoveryResult, _ := h.stateRecoverer.RecoverState(ctx, cmd.PlayerID)

    // Start supply monitor
    h.startSupplyMonitor(ctx, config)

    // Initial scan and assignment
    h.pipelineManager.ScanAndCreatePipelines(ctx, config.ScanParams)
    h.taskAssigner.AssignTasks(ctx, config.AssignParams)

    // Event-driven main loop
    for {
        select {
        case <-opportunityScanTicker.C:
            h.pipelineManager.ScanAndCreatePipelines(ctx, config.ScanParams)

        case <-stuckPipelineTicker.C:
            stuckIDs, _ := h.pipelineManager.DetectStuckPipelines(ctx, cmd.PlayerID)
            for _, id := range stuckIDs {
                h.pipelineManager.RecyclePipeline(ctx, id, cmd.PlayerID)
            }

        case <-idleShipTicker.C:
            h.taskAssigner.AssignTasks(ctx, config.AssignParams)

        case <-h.taskReadyChan:
            h.taskAssigner.AssignTasks(ctx, config.AssignParams)

        case shipSymbol := <-h.workerCompletionChan:
            completion, _ := h.workerManager.HandleWorkerCompletion(ctx, shipSymbol)
            h.handleCompletion(ctx, completion)
            h.taskAssigner.AssignTasks(ctx, config.AssignParams)

        case <-ctx.Done():
            return &RunParallelManufacturingCoordinatorResponse{}, nil
        }
    }
}
```

---

## Extraction Order & Dependencies

| Phase | Service | Complexity | Dependencies | Est. Lines |
|-------|---------|------------|--------------|------------|
| 1 | ManufacturingNavigator | Low | None | ~80 |
| 2 | ManufacturingLedgerRecorder | Low | None | ~100 |
| 3 | ManufacturingPurchaser | Medium | Phase 2 | ~150 |
| 4 | ManufacturingSeller | Low | Phase 2 | ~80 |
| 5 | TaskExecutor Strategy | Medium | Phases 1-4 | ~300 |
| 6 | Simplify Task Worker | Low | Phase 5 | ~100 |
| 7 | PipelineLifecycleManager | Medium | None | ~300 |
| 8 | TaskAssignmentManager | Medium | None | ~200 |
| 9 | WorkerLifecycleManager | Medium | None | ~250 |
| 10 | StateRecoveryManager | Medium | None | ~200 |
| 11 | OrphanedCargoHandler | Medium | Phase 8 | ~200 |
| 12 | FactoryStateManager | Low | None | ~150 |
| 13 | Simplify Coordinator | Low | Phases 7-12 | ~400 |

**Dependency Graph:**
```
Phase 1 (Navigator) ─────┐
Phase 2 (Ledger) ────────┼──> Phase 5 (Strategy) ──> Phase 6 (Worker Handler)
Phase 3 (Purchaser) ─────┤
Phase 4 (Seller) ────────┘

Phase 7 (Pipeline) ──────┐
Phase 8 (Assignment) ────┼──> Phase 13 (Coordinator Handler)
Phase 9 (Worker) ────────┤
Phase 10 (Recovery) ─────┤
Phase 11 (Orphaned) ─────┤
Phase 12 (Factory) ──────┘
```

---

## Critical Files to Reference

| File | Pattern to Follow |
|------|-------------------|
| `internal/application/contract/services/delivery_executor.go` | Navigator pattern, helper methods |
| `internal/application/trading/services/arbitrage_executor.go` | Multi-step workflow with safety checks |
| `internal/domain/manufacturing/task.go` | Domain entity (must not change) |
| `internal/domain/manufacturing/pipeline.go` | Domain entity (must not change) |

---

## Backward Compatibility

- `RunManufacturingTaskWorkerCommand` - Same structure
- `RunManufacturingTaskWorkerResponse` - Same structure
- `RunParallelManufacturingCoordinatorCommand` - Same structure
- `RunParallelManufacturingCoordinatorResponse` - Same structure

All domain entities remain unchanged.

---

## Success Criteria

| Criterion | Metric |
|-----------|--------|
| Task worker size | 916 → ~100 lines (89% reduction) |
| Coordinator size | 2167 → ~400 lines (82% reduction) |
| Single Responsibility | Each service has one responsibility |
| Open/Closed | New task types without modifying existing code |
| Dependency Inversion | Services injectable via interfaces |
| DRY | No code duplication between executors |
| Tests | All existing BDD tests pass |

---

## File Structure After Refactoring

```
internal/application/trading/
├── commands/
│   ├── run_manufacturing_task_worker.go      # Thin (~100 lines)
│   └── run_parallel_manufacturing_coordinator.go  # Thin (~400 lines)
└── services/
    ├── manufacturing/
    │   ├── navigator.go                      # Navigation + docking
    │   ├── ledger_recorder.go               # Ledger transactions
    │   ├── purchaser.go                     # Purchase loops
    │   ├── seller.go                        # Sell operations
    │   ├── task_executor.go                 # Strategy interface + registry
    │   ├── acquire_deliver_executor.go      # ACQUIRE_DELIVER strategy
    │   ├── collect_sell_executor.go         # COLLECT_SELL strategy
    │   ├── liquidate_executor.go            # LIQUIDATE strategy
    │   ├── pipeline_lifecycle_manager.go    # Pipeline create/complete/recycle
    │   ├── task_assignment_manager.go       # Ship selection + assignment
    │   ├── worker_lifecycle_manager.go      # Container management
    │   ├── state_recovery_manager.go        # Database recovery
    │   ├── orphaned_cargo_handler.go        # Orphaned cargo handling
    │   └── factory_state_manager.go         # Factory state + dependencies
    ├── arbitrage_executor.go                # (existing)
    ├── manufacturing_demand_finder.go       # (existing)
    ├── pipeline_planner.go                  # (existing)
    ├── supply_monitor.go                    # (existing)
    └── task_queue.go                        # (existing)
```
