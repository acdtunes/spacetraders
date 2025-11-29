# Daemon Restart Resilience - Bug Fix Plan

**Created:** 2025-11-28
**Priority:** Critical
**Impact:** Operations become non-functional after daemon restart, cargo stranded on ships

---

## Executive Summary

The manufacturing system has a fundamental design flaw: **worker containers are intentionally skipped during daemon recovery, but there is no mechanism to restart them**. This causes:

1. **Task pipeline stalls** - EXECUTING tasks become orphaned
2. **Stranded cargo** - Ships with purchased goods can't sell them
3. **Capital loss** - Investment in cargo is never recovered
4. **Context cancellation cascades** - Daemon restarts trigger mass task failures

---

## Root Cause Analysis

### The Core Problem

```
Daemon Restart
    │
    ├─► Coordinator containers: RECOVERED ✓
    │
    └─► Worker containers: SKIPPED ✗ (intentionally)
              │
              └─► Tasks in EXECUTING state: ORPHANED
                        │
                        └─► Ships with cargo: STRANDED
```

**Location:** `internal/adapters/grpc/daemon_server.go:447-463`

```go
// Workers are managed by their parent coordinator and should not be recovered independently
if coordinatorID, hasCoordinator := config["coordinator_id"].(string); hasCoordinator && coordinatorID != "" {
    s.markWorkerInterrupted(ctx, containerModel, coordinatorID)
    failedCount++
    continue  // ← WORKER NEVER RESTARTED
}
```

**The assumption:** Coordinators will restart their workers.
**The reality:** Coordinators only load state, they don't restart workers.

---

## Bug #1: Workers Never Restarted After Daemon Restart

### Symptom
- EXECUTING tasks stay in EXECUTING state forever
- Ships with cargo become idle but have unsold goods
- New tasks can't use these ships (cargo full)

### Root Cause
**File:** `internal/application/trading/commands/run_parallel_manufacturing_coordinator.go:391-409`

```go
func (h *RunParallelManufacturingCoordinatorHandler) recoverState(ctx context.Context, playerID int) error {
    result, err := h.stateRecoverer.RecoverState(ctx, playerID)
    // ...

    // Load recovered pipelines into pipeline manager
    for id, pipeline := range result.ActivePipelines {
        pipelineMgr.AddActivePipeline(id, pipeline)
    }

    // Check if any recovered pipelines are already complete
    h.pipelineManager.CheckAllPipelinesForCompletion(ctx)

    return nil  // ← NO CODE TO RESTART WORKERS
}
```

### Fix Required

Add worker restart logic after state recovery:

```go
func (h *RunParallelManufacturingCoordinatorHandler) recoverState(ctx context.Context, playerID int) error {
    result, err := h.stateRecoverer.RecoverState(ctx, playerID)
    if err != nil {
        return err
    }

    // Load recovered pipelines
    for id, pipeline := range result.ActivePipelines {
        pipelineMgr.AddActivePipeline(id, pipeline)
    }

    // NEW: Restart workers for EXECUTING/ASSIGNED tasks
    if err := h.restartInterruptedWorkers(ctx, playerID); err != nil {
        logger.Log("ERROR", fmt.Sprintf("Failed to restart workers: %v", err), nil)
    }

    h.pipelineManager.CheckAllPipelinesForCompletion(ctx)
    return nil
}

// NEW FUNCTION
func (h *RunParallelManufacturingCoordinatorHandler) restartInterruptedWorkers(ctx context.Context, playerID int) error {
    // Find tasks that were EXECUTING when daemon died
    executingTasks, err := h.taskRepo.FindByStatus(ctx, playerID, manufacturing.TaskStatusExecuting)
    if err != nil {
        return err
    }

    for _, task := range executingTasks {
        shipSymbol := task.AssignedShip()
        if shipSymbol == "" {
            continue
        }

        // Check if ship has cargo (determines which phase to resume)
        ship, err := h.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
        if err != nil {
            continue
        }

        // Determine resume point based on ship state
        if task.TaskType() == manufacturing.TaskTypeCollectSell {
            if !ship.Cargo().IsEmpty() {
                // Ship has cargo - skip COLLECT, resume from SELL
                task.MarkCollectPhaseComplete()
            }
        }

        // Restart worker container for this task
        if err := h.startWorkerForTask(ctx, cmd, task, shipSymbol); err != nil {
            logger.Log("ERROR", fmt.Sprintf("Failed to restart worker for task %s: %v",
                task.ID()[:8], err), nil)
        } else {
            logger.Log("INFO", fmt.Sprintf("Restarted worker for task %s on ship %s",
                task.ID()[:8], shipSymbol), nil)
        }
    }

    return nil
}
```

### Files to Modify
| File | Change |
|------|--------|
| `run_parallel_manufacturing_coordinator.go` | Add `restartInterruptedWorkers()` function |
| `run_parallel_manufacturing_coordinator.go` | Call it from `recoverState()` |

---

## Bug #2: Task State Reset Releases Ship Assignment Prematurely

### Symptom
- COLLECT_SELL task: ship collects cargo, daemon restarts
- Recovery releases ship assignment
- Original ship (with cargo) becomes orphaned
- Task retries with DIFFERENT ship (no cargo) → fails

### Root Cause
**File:** `internal/application/trading/services/manufacturing/state_recovery_manager.go:151-162`

```go
// Step 2b: Reset interrupted EXECUTING tasks
if task.Status() == manufacturing.TaskStatusExecuting {
    shipSymbol := task.AssignedShip()
    if err := task.RollbackExecution(); err == nil {
        _ = m.taskRepo.Update(ctx, task)
        if shipSymbol != "" && m.shipAssignmentRepo != nil {
            _ = m.shipAssignmentRepo.Release(ctx, shipSymbol, playerID, "task_recovery")
            // ← BUG: Ship still has cargo but assignment is released!
        }
    }
}
```

### Fix Required

**Option A: Don't release ship assignment for SELL tasks**

```go
if task.Status() == manufacturing.TaskStatusExecuting {
    shipSymbol := task.AssignedShip()

    // Check if this is a SELL task - preserve ship assignment
    isSellTask := task.TaskType() == manufacturing.TaskTypeCollectSell ||
                  task.TaskType() == manufacturing.TaskTypeLiquidate

    if err := task.RollbackExecution(); err == nil {
        _ = m.taskRepo.Update(ctx, task)

        // Only release ship if NOT a sell task
        // Sell tasks need their original ship (has the cargo)
        if shipSymbol != "" && m.shipAssignmentRepo != nil && !isSellTask {
            _ = m.shipAssignmentRepo.Release(ctx, shipSymbol, playerID, "task_recovery")
        }
    }
}
```

**Option B: Validate ship cargo before releasing (preferred)**

```go
if task.Status() == manufacturing.TaskStatusExecuting {
    shipSymbol := task.AssignedShip()

    if err := task.RollbackExecution(); err == nil {
        _ = m.taskRepo.Update(ctx, task)

        // Check if ship has cargo before releasing
        shouldRelease := true
        if shipSymbol != "" && m.shipRepo != nil {
            ship, err := m.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
            if err == nil && ship != nil && !ship.Cargo().IsEmpty() {
                // Ship has cargo - DON'T release, task needs this ship
                shouldRelease = false
                logger.Log("INFO", fmt.Sprintf("Preserving ship %s assignment (has cargo)", shipSymbol), nil)
            }
        }

        if shouldRelease && shipSymbol != "" && m.shipAssignmentRepo != nil {
            _ = m.shipAssignmentRepo.Release(ctx, shipSymbol, playerID, "task_recovery")
        }
    }
}
```

### Files to Modify
| File | Change |
|------|--------|
| `state_recovery_manager.go` | Add cargo check before releasing ship assignment |
| `state_recovery_manager.go` | Add `shipRepo` dependency |
| `state_recovery_manager.go` | Constructor update to inject `shipRepo` |

---

## Bug #3: No Phase Tracking for Multi-Phase Tasks

### Symptom
- COLLECT_SELL task: COLLECT phase completes (ship has cargo)
- Daemon restarts before SELL phase
- Task retries from beginning (tries COLLECT again)
- COLLECT fails because factory supply is now LOW

### Root Cause
**File:** `internal/domain/manufacturing/task.go`

Tasks don't track which phase completed. `RollbackExecution()` always resets to READY:

```go
func (t *ManufacturingTask) RollbackExecution() error {
    if t.status != TaskStatusExecuting {
        return ErrInvalidStatusTransition
    }
    t.status = TaskStatusReady  // ← Always resets to beginning
    return nil
}
```

### Fix Required

Add phase tracking to task entity:

```go
type ManufacturingTask struct {
    // ... existing fields ...

    // Phase completion tracking for multi-phase tasks
    collectPhaseCompleted  bool      // COLLECT_SELL: did we collect from factory?
    acquirePhaseCompleted  bool      // ACQUIRE_DELIVER: did we buy from market?
    phaseCompletedAt       time.Time // When phase completed
}

// Mark COLLECT phase as complete (ship has cargo)
func (t *ManufacturingTask) MarkCollectPhaseComplete() {
    t.collectPhaseCompleted = true
    t.phaseCompletedAt = time.Now()
}

// Mark ACQUIRE phase as complete (ship has purchased goods)
func (t *ManufacturingTask) MarkAcquirePhaseComplete() {
    t.acquirePhaseCompleted = true
    t.phaseCompletedAt = time.Now()
}

// Check if should skip to SELL/DELIVER phase
func (t *ManufacturingTask) ShouldSkipToSecondPhase() bool {
    switch t.taskType {
    case TaskTypeCollectSell:
        return t.collectPhaseCompleted
    case TaskTypeAcquireDeliver:
        return t.acquirePhaseCompleted
    default:
        return false
    }
}

// Updated rollback - preserves phase completion
func (t *ManufacturingTask) RollbackExecution() error {
    if t.status != TaskStatusExecuting {
        return ErrInvalidStatusTransition
    }
    // Don't reset phase flags - they indicate completed work
    t.status = TaskStatusReady
    return nil
}
```

### Database Migration Required

```sql
-- migrations/022_add_task_phase_tracking.up.sql
ALTER TABLE manufacturing_tasks
ADD COLUMN collect_phase_completed BOOLEAN DEFAULT FALSE,
ADD COLUMN acquire_phase_completed BOOLEAN DEFAULT FALSE,
ADD COLUMN phase_completed_at TIMESTAMP WITH TIME ZONE;

-- migrations/022_add_task_phase_tracking.down.sql
ALTER TABLE manufacturing_tasks
DROP COLUMN collect_phase_completed,
DROP COLUMN acquire_phase_completed,
DROP COLUMN phase_completed_at;
```

### Files to Modify
| File | Change |
|------|--------|
| `internal/domain/manufacturing/task.go` | Add phase tracking fields and methods |
| `internal/adapters/persistence/models.go` | Add columns to TaskModel |
| `internal/adapters/persistence/task_repository.go` | Map new fields |
| `migrations/022_add_task_phase_tracking.up.sql` | Create migration |
| Task worker handler | Call `MarkCollectPhaseComplete()` after collect |

---

## Bug #4: Orphaned Cargo Not Recovered

### Symptom
- Task cancelled (orphaned, no pipeline)
- Ship has cargo from cancelled task
- Cargo value never recovered
- Ship becomes unusable (cargo full)

### Root Cause
**File:** `internal/application/trading/services/manufacturing/state_recovery_manager.go:104-119`

```go
if task.PipelineID() == "" {
    shipSymbol := task.AssignedShip()
    if err := task.Cancel("orphaned task - no pipeline"); err == nil {
        _ = m.taskRepo.Update(ctx, task)
    }
    // Release ship if assigned
    if shipSymbol != "" && m.shipAssignmentRepo != nil {
        _ = m.shipAssignmentRepo.Release(ctx, shipSymbol, playerID, "orphaned_task")
    }
    continue  // ← CARGO ON SHIP IS NEVER CHECKED/RECOVERED
}
```

### Fix Required

Check for stranded cargo and create LIQUIDATE task:

```go
if task.PipelineID() == "" {
    shipSymbol := task.AssignedShip()

    // Check if ship has cargo that needs recovery
    if shipSymbol != "" && m.shipRepo != nil {
        ship, err := m.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
        if err == nil && ship != nil && !ship.Cargo().IsEmpty() {
            // Create LIQUIDATE task to recover cargo value
            for _, item := range ship.Cargo().Items() {
                liquidateTask := manufacturing.NewLiquidationTask(
                    playerID,
                    shipSymbol,
                    item.Symbol,
                    item.Units,
                    "", // Let task find best sell market
                )
                if err := m.taskRepo.Add(ctx, liquidateTask); err == nil {
                    m.taskQueue.Enqueue(liquidateTask)
                    logger.Log("INFO", fmt.Sprintf(
                        "Created LIQUIDATE task for stranded %d %s on ship %s",
                        item.Units, item.Symbol, shipSymbol), nil)
                }
            }
            // Don't release ship - it's now assigned to LIQUIDATE task
            continue
        }
    }

    // No cargo - safe to cancel and release
    if err := task.Cancel("orphaned task - no pipeline"); err == nil {
        _ = m.taskRepo.Update(ctx, task)
    }
    if shipSymbol != "" && m.shipAssignmentRepo != nil {
        _ = m.shipAssignmentRepo.Release(ctx, shipSymbol, playerID, "orphaned_task")
    }
    continue
}
```

### Files to Modify
| File | Change |
|------|--------|
| `state_recovery_manager.go` | Add cargo check and LIQUIDATE task creation |
| `state_recovery_manager.go` | Add `shipRepo` dependency |

---

## Bug #5: Context Cancellation Cascades

### Symptom
- Daemon shutdown triggers context cancellation
- All operations fail with "context canceled"
- Database writes fail mid-transaction
- State becomes inconsistent

### Root Cause

Operations use the daemon's context, which is cancelled on shutdown:

```go
// Daemon shutdown
cancel()  // ← All child contexts cancelled immediately

// Worker still running
err := taskRepo.Update(ctx, task)  // ← "context canceled"
```

### Fix Required

**A. Add graceful shutdown timeout**

**File:** `cmd/spacetraders-daemon/main.go`

```go
func main() {
    // ... existing setup ...

    // Handle signals
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

    go func() {
        <-sigChan
        log.Println("Shutdown signal received, waiting for operations to complete...")

        // Give operations 30 seconds to complete
        shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
        defer cancel()

        if err := server.GracefulShutdown(shutdownCtx); err != nil {
            log.Printf("Graceful shutdown failed: %v", err)
        }

        os.Exit(0)
    }()

    // ... existing run logic ...
}
```

**B. Use separate context for database operations**

**File:** `internal/adapters/grpc/container_runner.go`

```go
func (r *ContainerRunner) Run(ctx context.Context, ...) {
    // Create a separate context for cleanup operations
    // This context survives parent cancellation for cleanup
    cleanupCtx := context.Background()

    defer func() {
        // Use cleanupCtx for final state persistence
        if err := r.persistFinalState(cleanupCtx, container); err != nil {
            logger.Log("ERROR", fmt.Sprintf("Failed to persist final state: %v", err), nil)
        }
    }()

    // ... existing run logic with ctx ...
}
```

**C. Add context cancellation check before critical operations**

```go
// Before any database write in workers
select {
case <-ctx.Done():
    // Context cancelled - use background context for cleanup
    cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    _ = taskRepo.Update(cleanupCtx, task)
    return ctx.Err()
default:
    // Normal operation
    return taskRepo.Update(ctx, task)
}
```

### Files to Modify
| File | Change |
|------|--------|
| `cmd/spacetraders-daemon/main.go` | Add graceful shutdown handler |
| `internal/adapters/grpc/daemon_server.go` | Add `GracefulShutdown()` method |
| `internal/adapters/grpc/container_runner.go` | Use cleanup context for final state |
| Task worker handlers | Check context before critical ops |

---

## Bug #6: Factory Input Configuration Mismatch

### Symptom
```
Failed to record delivery: unknown input good FERTILIZERS for factory X1-YZ19-E49
Failed to record delivery: unknown input good LIQUID_HYDROGEN for factory X1-YZ19-E49
```

### Root Cause

Factory state tracker has outdated/incorrect input good configuration. Deliveries are being made but not recorded because the factory doesn't recognize the input.

### Fix Required

**A. Validate factory configuration against API**

```go
func (t *FactoryStateTracker) ValidateConfiguration(ctx context.Context, apiClient APIClient) error {
    for factorySymbol, state := range t.factories {
        // Fetch actual factory configuration from API
        waypoint, err := apiClient.GetWaypoint(ctx, factorySymbol)
        if err != nil {
            continue
        }

        // Update required inputs from API
        actualInputs := extractFactoryInputs(waypoint)
        state.SetRequiredInputs(actualInputs)
    }
    return nil
}
```

**B. Log warning but record delivery anyway**

```go
func (t *FactoryStateTracker) RecordDelivery(factorySymbol, good string, quantity int) error {
    state := t.factories[factorySymbol]

    if !state.HasInputGood(good) {
        // Log warning but don't fail - API might have updated
        logger.Log("WARN", fmt.Sprintf(
            "Recording delivery of %s to %s - not in expected inputs (API may have updated)",
            good, factorySymbol), nil)
    }

    state.AddDelivery(good, quantity)
    return nil
}
```

### Files to Modify
| File | Change |
|------|--------|
| `internal/application/trading/services/manufacturing/factory_state_tracker.go` | Add validation method |
| `internal/application/trading/services/manufacturing/factory_state_tracker.go` | Make RecordDelivery lenient |

---

## Implementation Order

### Phase 1: Stop the Bleeding (Week 1)

| Priority | Bug | Estimated Effort |
|----------|-----|------------------|
| P0 | Bug #2: Ship assignment release | 2 hours |
| P0 | Bug #4: Orphaned cargo recovery | 3 hours |
| P0 | Bug #5: Context cancellation (graceful shutdown) | 4 hours |

### Phase 2: Core Resilience (Week 2)

| Priority | Bug | Estimated Effort |
|----------|-----|------------------|
| P1 | Bug #1: Worker restart after recovery | 8 hours |
| P1 | Bug #3: Phase tracking (with migration) | 6 hours |

### Phase 3: Polish (Week 3)

| Priority | Bug | Estimated Effort |
|----------|-----|------------------|
| P2 | Bug #6: Factory configuration validation | 3 hours |
| P2 | Additional logging and monitoring | 4 hours |

---

## Testing Strategy

### Unit Tests

```go
// Test worker restart after recovery
func TestRecoverState_RestartsExecutingWorkers(t *testing.T) {
    // Setup: task in EXECUTING state, ship with cargo
    // Act: recoverState()
    // Assert: worker container started for task
}

// Test ship assignment preserved for sell tasks
func TestStateRecovery_PreservesShipAssignmentForSellTasks(t *testing.T) {
    // Setup: COLLECT_SELL task in EXECUTING, ship has cargo
    // Act: RecoverState()
    // Assert: ship assignment NOT released
}

// Test orphaned cargo creates LIQUIDATE task
func TestStateRecovery_CreatesLiquidateForOrphanedCargo(t *testing.T) {
    // Setup: orphaned task with ship that has cargo
    // Act: RecoverState()
    // Assert: LIQUIDATE task created and enqueued
}
```

### Integration Tests

```go
// Test full daemon restart cycle
func TestDaemonRestart_RecoversMidExecutionTask(t *testing.T) {
    // 1. Start manufacturing coordinator
    // 2. Wait for COLLECT_SELL task to reach EXECUTING with cargo
    // 3. Kill daemon
    // 4. Restart daemon
    // 5. Verify task completes successfully
}
```

### Manual Testing Checklist

- [ ] Start manufacturing coordinator
- [ ] Wait for task to reach EXECUTING state
- [ ] Kill daemon with SIGTERM
- [ ] Restart daemon
- [ ] Verify: coordinator recovers
- [ ] Verify: executing tasks resume
- [ ] Verify: ships with cargo sell their goods
- [ ] Verify: no orphaned ships

---

## Monitoring Additions

### New Metrics

```go
// Add to metrics collector
spacetraders_daemon_tasks_recovered_total{status="restarted|orphaned|cargo_recovered"}
spacetraders_daemon_graceful_shutdown_duration_seconds
spacetraders_daemon_context_cancellation_total{operation="task_update|container_log|..."}
```

### New Log Patterns to Alert On

```
pattern: "Failed to restart worker"
severity: ERROR
action: Page on-call

pattern: "Created LIQUIDATE task for stranded"
severity: WARN
action: Track cargo recovery rate

pattern: "context canceled.*task_update"
severity: ERROR
action: Investigate shutdown sequence
```

---

## Summary

| Bug | Impact | Fix Complexity | Priority |
|-----|--------|----------------|----------|
| #1 Workers not restarted | Pipeline stalls | Medium | P0 |
| #2 Ship released with cargo | Cargo stranded | Low | P0 |
| #3 No phase tracking | Retry failures | Medium | P1 |
| #4 Orphaned cargo lost | Capital loss | Low | P0 |
| #5 Context cascades | State corruption | Medium | P0 |
| #6 Factory config mismatch | Delivery failures | Low | P2 |

**Total estimated effort:** 30 hours (including tests)

---

## Appendix: Current State Flow (Broken)

```
DAEMON RUNNING
    │
    ├─ Coordinator: RUNNING
    │     └─ Worker-1: EXECUTING (ship has cargo)
    │     └─ Worker-2: EXECUTING
    │
SIGTERM RECEIVED
    │
    ├─ Context cancelled
    ├─ Workers fail: "context canceled"
    ├─ State inconsistent
    │
DAEMON RESTART
    │
    ├─ Coordinator: RECOVERED ✓
    │     └─ recoverState() called
    │     └─ Pipelines loaded ✓
    │     └─ Workers: NOT RESTARTED ✗
    │
    ├─ Task states: RESET to READY
    │     └─ Ship assignments: RELEASED ✗
    │     └─ Phase progress: LOST ✗
    │
    └─ Result: Ships with cargo orphaned, capital lost
```

## Appendix: Desired State Flow (Fixed)

```
DAEMON RUNNING
    │
    ├─ Coordinator: RUNNING
    │     └─ Worker-1: EXECUTING (ship has cargo, phase=SELL)
    │     └─ Worker-2: EXECUTING
    │
SIGTERM RECEIVED
    │
    ├─ Graceful shutdown (30s timeout)
    ├─ Workers persist state with cleanup context
    ├─ State consistent
    │
DAEMON RESTART
    │
    ├─ Coordinator: RECOVERED ✓
    │     └─ recoverState() called
    │     └─ Pipelines loaded ✓
    │     └─ restartInterruptedWorkers() ✓
    │           └─ Check ship cargo ✓
    │           └─ Set phase flags ✓
    │           └─ Start worker ✓
    │
    ├─ Task states: PRESERVED
    │     └─ Ship assignments: PRESERVED (for sell tasks)
    │     └─ Phase progress: TRACKED in DB
    │
    └─ Result: Tasks resume from correct phase, cargo sold
```
