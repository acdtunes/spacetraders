# Collect/Sell Priority Implementation Plan

## Overview

Enhance the manufacturing system to prioritize "collect/sell" opportunities over full manufacturing pipelines. When factories already have HIGH/ABUNDANT supply of goods, we should simply collect and sell those goods rather than manufacturing from scratch.

**Key Benefits:**
- Faster execution (2 tasks vs 4+ tasks)
- Lower risk (no production waiting)
- Higher throughput (ships immediately productive)
- Better resource utilization (skip unnecessary work)

## Background

### Current Manufacturing Flow

```
1. Find demand market (IMPORT with SCARCE/LIMITED supply, WEAK/RESTRICTED activity)
2. Build full dependency tree (raw materials → intermediate goods → final product)
3. Execute pipeline: ACQUIRE → DELIVER → COLLECT → SELL
```

### Problem

The current `BuildDependencyTree()` in `supply_chain_resolver.go:89` **always forces fabrication** for the root good:

```go
// Force fabrication for the target good (root), even if available in markets
return r.buildTreeRecursive(ctx, targetGood, systemSymbol, playerID, visited, []string{}, true)
```

This means even when a factory has HIGH/ABUNDANT supply of the target product, we still:
- Build the full dependency tree with all inputs
- Create ACQUIRE → DELIVER → COLLECT → SELL tasks
- Waste time and resources on unnecessary manufacturing

### Solution: Smart Root Node Handling

**Check factory supply for the root good BEFORE building the tree:**
- If factory has HIGH/ABUNDANT supply → create collect-only node (no children)
- If factory has lower supply → build full dependency tree as before

This is a **single change** to `BuildDependencyTree()` - no new finders needed.

## Design

### Modified BuildDependencyTree

**File:** `internal/application/goods/services/supply_chain_resolver.go`

```go
// BuildDependencyTree constructs a complete dependency tree for producing a target good.
// MODIFIED: If factory already has HIGH/ABUNDANT supply, creates a collect-only node
// instead of building the full manufacturing tree.
func (r *SupplyChainResolver) BuildDependencyTree(
    ctx context.Context,
    targetGood string,
    systemSymbol string,
    playerID int,
) (*goods.SupplyChainNode, error) {
    // NEW: Check if factory already has sufficient supply for the target good
    factory, err := r.findFactory(ctx, targetGood, systemSymbol, playerID)
    if err != nil {
        return nil, fmt.Errorf("error finding factory for %s: %w", targetGood, err)
    }
    if factory == nil {
        return nil, fmt.Errorf("no factory in system %s exports %s", systemSymbol, targetGood)
    }

    // NEW: If factory has HIGH or ABUNDANT supply, skip manufacturing
    if factory.Supply == "HIGH" || factory.Supply == "ABUNDANT" {
        // Factory already has supply - just collect it
        node := goods.NewSupplyChainNode(targetGood, goods.AcquisitionFabricate)
        node.WaypointSymbol = factory.WaypointSymbol
        node.SupplyLevel = factory.Supply
        // No children - this creates a COLLECT-only pipeline
        return node, nil
    }

    // Factory supply is low - build full manufacturing tree
    visited := make(map[string]bool)
    return r.buildTreeRecursive(ctx, targetGood, systemSymbol, playerID, visited, []string{}, true)
}
```

### How It Works

**Before (always builds full tree):**
```
BuildDependencyTree("ADVANCED_CIRCUITRY") →
  ADVANCED_CIRCUITRY (FABRICATE)
    ├── ELECTRONICS (BUY)
    └── MICROPROCESSORS (BUY)

Pipeline: ACQUIRE → DELIVER → ACQUIRE → DELIVER → COLLECT → SELL (6 tasks)
```

**After (checks factory supply first):**
```
Factory has HIGH supply:
BuildDependencyTree("ADVANCED_CIRCUITRY") →
  ADVANCED_CIRCUITRY (FABRICATE, no children)

Pipeline: COLLECT → SELL (2 tasks)

Factory has MODERATE supply:
BuildDependencyTree("ADVANCED_CIRCUITRY") →
  ADVANCED_CIRCUITRY (FABRICATE)
    ├── ELECTRONICS (BUY)
    └── MICROPROCESSORS (BUY)

Pipeline: ACQUIRE → DELIVER → ACQUIRE → DELIVER → COLLECT → SELL (6 tasks)
```

### PipelinePlanner Handles Empty Children

The existing `PipelinePlanner.createTasksFromTree()` already handles nodes with empty children:

```go
// From pipeline_planner.go:106-178
func (p *PipelinePlanner) createTasksFromTree(...) (string, error) {
    if node.AcquisitionMethod == goods.AcquisitionBuy {
        return p.createAcquireTask(planCtx, node)
    }

    // FABRICATE node: Create tasks for all children first
    for _, child := range node.Children {  // EMPTY when factory has supply!
        // This loop doesn't execute when Children is empty
    }

    // Create COLLECT task (depends on deliveries - empty when no children)
    collectTask := manufacturing.NewCollectTask(...)
    // ...
}
```

When `Children` is empty:
- No ACQUIRE tasks
- No DELIVER tasks
- COLLECT task has no dependencies (immediately ready)
- Final SELL task depends only on COLLECT

**Result:** Pipeline with only 2 tasks.

### Score Naturally Reflects Complexity

The existing score calculation in `manufacturing_opportunity.go` already favors shallow trees:

```go
depthScore := 100.0 - float64(o.treeDepth)*20.0
```

| Tree Type | Depth | Depth Score |
|-----------|-------|-------------|
| Collect-only (no children) | 1 | 80 |
| Simple manufacturing | 2 | 60 |
| Complex manufacturing | 3+ | 40 or less |

Collect-only opportunities naturally rank higher.

## Implementation

### Step 1: Modify BuildDependencyTree

**File:** `internal/application/goods/services/supply_chain_resolver.go`

**Changes:**
1. Move factory lookup BEFORE the recursive tree building
2. Check if `factory.Supply` is HIGH or ABUNDANT
3. If yes, return a node with no children
4. If no, proceed with existing tree building logic

```go
func (r *SupplyChainResolver) BuildDependencyTree(
    ctx context.Context,
    targetGood string,
    systemSymbol string,
    playerID int,
) (*goods.SupplyChainNode, error) {
    // Step 1: Find the factory for the target good
    factory, err := r.findFactory(ctx, targetGood, systemSymbol, playerID)
    if err != nil {
        return nil, fmt.Errorf("error finding factory for %s: %w", targetGood, err)
    }
    if factory == nil {
        return nil, fmt.Errorf("no factory in system %s exports %s", systemSymbol, targetGood)
    }

    // Step 2: Check factory supply - if HIGH/ABUNDANT, skip manufacturing
    if factory.Supply == "HIGH" || factory.Supply == "ABUNDANT" {
        node := goods.NewSupplyChainNode(targetGood, goods.AcquisitionFabricate)
        node.WaypointSymbol = factory.WaypointSymbol
        node.SupplyLevel = factory.Supply
        // No children = collect-only pipeline
        return node, nil
    }

    // Step 3: Factory supply is low - build full manufacturing tree
    visited := make(map[string]bool)
    return r.buildTreeRecursive(ctx, targetGood, systemSymbol, playerID, visited, []string{}, true)
}
```

### Step 2: Update Tree Depth Calculation

The `TotalDepth()` method in `supply_chain_node.go` should return 1 for nodes with no children:

```go
func (n *SupplyChainNode) TotalDepth() int {
    if len(n.Children) == 0 {
        return 1  // Leaf node or collect-only node
    }
    // ... existing recursive depth calculation
}
```

Verify this is already the case (it likely is).

### Step 3: No Changes Needed to Coordinator

The `FindHighDemandManufacturables()` continues to work as before:
1. Finds demand markets
2. Calls `BuildDependencyTree()` - which now returns collect-only trees when appropriate
3. Creates opportunities with appropriate tree depths
4. Score calculation ranks collect-only higher

**No separate finder needed.**

## Example Scenarios

### Scenario 1: Factory Has HIGH Supply

```
Demand: X1-AB12-A1 needs ADVANCED_CIRCUITRY (SCARCE supply, WEAK activity, price: 8,500)

BuildDependencyTree("ADVANCED_CIRCUITRY"):
  1. Find factory → X1-AB12-C3
  2. Check supply → HIGH
  3. Return collect-only node

Result:
  - Tree depth: 1
  - Score: Higher (depthScore = 80)
  - Pipeline: COLLECT → SELL (2 tasks)
```

### Scenario 2: Factory Has MODERATE Supply

```
Demand: X1-AB12-A1 needs ADVANCED_CIRCUITRY (SCARCE supply, WEAK activity, price: 8,500)

BuildDependencyTree("ADVANCED_CIRCUITRY"):
  1. Find factory → X1-AB12-C3
  2. Check supply → MODERATE (not HIGH/ABUNDANT)
  3. Build full tree with children

Result:
  - Tree depth: 2
  - Score: Lower (depthScore = 60)
  - Pipeline: ACQUIRE(ELECTRONICS) → DELIVER → ACQUIRE(MICROPROCESSORS) → DELIVER → COLLECT → SELL
```

### Scenario 3: Mixed Opportunities

```
Opportunities discovered:
  1. LASER_RIFLES (factory has HIGH supply) → depth 1, score 85
  2. ADVANCED_CIRCUITRY (factory has MODERATE supply) → depth 2, score 68
  3. MACHINERY (factory has ABUNDANT supply) → depth 1, score 82

Sorted by score:
  1. LASER_RIFLES (collect-only) ← Ships assigned first
  2. MACHINERY (collect-only)
  3. ADVANCED_CIRCUITRY (full manufacturing)
```

## Multi-Ship Pipeline Support

The existing infrastructure already supports multi-ship pipelines:

- `TaskQueue` manages tasks with priorities
- Workers pull tasks from shared queue
- Dependencies prevent out-of-order execution

**For complex manufacturing pipelines with many tasks:**
- Multiple ships can execute parallel ACQUIRE tasks
- After deliveries complete, one ship does COLLECT → SELL

**For collect-only pipelines:**
- Single ship executes COLLECT → SELL
- Very fast turnaround

## Event-Driven Coordinator

### Problem with Current Polling

The current coordinator uses two polling intervals:
```go
opportunityScanInterval := 2 * time.Minute   // Poll for opportunities
shipDiscoveryInterval := 30 * time.Second    // Poll for idle ships
```

**Issues:**
- 30-second ship polling is wasteful (queries when no ships are idle)
- Up to 30-second delay before idle ship gets new work
- Not responsive to actual events

### Solution: Hybrid Event-Driven Design

**1. Event-driven ship reassignment (no polling):**
```go
case workerID := <-workerCompletionChan:
    activeWorkers--
    freedShip := h.getShipFromWorker(workerID)
    pipelineID := h.getPipelineFromWorker(workerID)

    // Check if pipeline completed
    pipeline := h.activePipelines[pipelineID]
    if pipeline.IsCompleted() {
        // Pipeline done - rescan for new opportunities
        h.scanOpportunities(ctx, ...)
        delete(h.activePipelines, pipelineID)
    }

    // Immediately assign freed ship to next task
    if len(opportunities) > 0 {
        h.assignShipToNextTask(ctx, freedShip, opportunities)
    }
```

**2. Hybrid opportunity scanning:**
```go
// Background poll for external market changes (less frequent)
opportunityScanInterval := 5 * time.Minute

// Event triggers for rescanning:
// - Pipeline completes (our actions changed supply levels)
// - All tasks done but ships idle (need new work)
// - Initial startup
```

### New Coordinator Loop

```go
func (h *Handler) Handle(ctx context.Context, cmd *Command) {
    // Initial scan
    h.scanOpportunities(ctx, ...)

    // Background poll (catches external market changes)
    opportunityTicker := time.NewTicker(5 * time.Minute)

    for {
        select {
        case <-opportunityTicker.C:
            // Periodic rescan for external changes
            h.scanOpportunities(ctx, ...)
            h.assignIdleShipsToTasks(ctx)

        case workerID := <-workerCompletionChan:
            // Event-driven: ship completed task
            activeWorkers--
            freedShip := h.getShipFromWorker(workerID)

            // Check if pipeline completed
            if h.isPipelineCompleted(workerID) {
                // Our actions may have changed market - rescan
                h.scanOpportunities(ctx, ...)
            }

            // Immediately reassign ship (zero delay)
            h.assignShipToNextTask(ctx, freedShip)

        case <-ctx.Done():
            return
        }
    }
}
```

### Benefits

| Aspect | Before (Polling) | After (Event-Driven) |
|--------|------------------|---------------------|
| Ship reassignment delay | Up to 30 seconds | Immediate (0 ms) |
| Database queries | Every 30 sec | Only when needed |
| Opportunity freshness | 2 min stale | Rescan on pipeline completion |
| Responsiveness | Fixed intervals | Reacts to actual events |

### Files to Modify

| File | Changes |
|------|---------|
| `run_manufacturing_coordinator.go` | Remove ship polling ticker, add event-driven reassignment |

## Files to Modify

| File | Changes |
|------|---------|
| `internal/application/goods/services/supply_chain_resolver.go` | Modify `BuildDependencyTree()` to check factory supply |
| `internal/application/trading/commands/run_manufacturing_coordinator.go` | Remove ship polling, add event-driven reassignment, hybrid opportunity scanning |

## Files to Verify (No Changes Expected)

| File | Verification |
|------|--------------|
| `internal/domain/goods/supply_chain_node.go` | Confirm `TotalDepth()` returns 1 for childless nodes |
| `internal/application/trading/services/pipeline_planner.go` | Confirm handles empty children correctly |
| `internal/domain/trading/manufacturing_opportunity.go` | Confirm score calculation uses tree depth |

## Testing

### Manual Testing

1. Find a factory with HIGH supply for a manufacturable good
2. Run manufacturing coordinator
3. Verify:
   - Opportunity is discovered
   - Dependency tree has no children (depth = 1)
   - Pipeline has only COLLECT → SELL tasks
   - Execution completes quickly

### Edge Cases

1. **No factory has HIGH supply** → Falls back to full manufacturing (existing behavior)
2. **Factory supply changes between scan and execution** → COLLECT task validates supply before purchase
3. **Multiple factories with different supply levels** → `findFactory()` returns one; could be enhanced to pick highest supply

## Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Factory supply drops before collection | COLLECT task validates supply, can wait or abort |
| Stale market data | Market data has TTL, rescans periodically |
| Breaking existing tests | Change is additive - existing trees still valid when supply is low |

## Success Criteria

1. `BuildDependencyTree()` returns collect-only node when factory has HIGH/ABUNDANT supply
2. Collect-only pipelines have 2 tasks (COLLECT → SELL)
3. Collect-only opportunities score higher than full manufacturing
4. Ships execute collect-only pipelines successfully
5. Full manufacturing still works when factory supply is low
6. Ships are reassigned immediately on task completion (no polling delay)
7. Opportunities are rescanned when pipelines complete
8. Background poll catches external market changes (5-minute interval)
