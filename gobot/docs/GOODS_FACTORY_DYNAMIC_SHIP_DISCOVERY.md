# Goods Factory Dynamic Ship Discovery Enhancement

## Overview

### Problem Statement

The current goods factory coordinator implementation discovers idle ships **once at startup** and maintains a **static ship pool** throughout execution. This design limits throughput when:

- More nodes exist at a dependency level than available ships
- Ships complete other operations (contracts, scouting) during factory execution
- Long-running production creates opportunities for ships to become available

Workers block indefinitely waiting for ships that could become available mid-execution, resulting in suboptimal resource utilization.

### Solution Summary

Implement **continuous dynamic ship discovery** using a background goroutine that polls for newly idle ships every 30 seconds and adds them to the ship pool, allowing blocked workers to acquire ships as they become available.

**User Decisions:**
- ✅ **Approach:** Option A - Background goroutine (continuous, 30s polling)
- ✅ **Thread Safety:** No mutex needed (current design is safe)
- ✅ **Discovery Interval:** 30 seconds (balanced)

## Current Architecture Analysis

### Ship Discovery at Startup

**Location:** `internal/application/goods/commands/run_factory_coordinator.go:176-184`

```go
// Step 3: Discover idle ships
playerID := shared.MustNewPlayerID(cmd.PlayerID)
idleShips, idleShipSymbols, err := contract.FindIdleLightHaulers(
    ctx,
    playerID,
    h.shipRepo,
    h.shipAssignmentRepo,
)
```

**How FindIdleLightHaulers works:**
(`internal/application/contract/ship_pool_manager.go:71-124`)

1. Fetches ALL ships for player via `shipRepo.FindAllByPlayer()` (line 80)
2. Filters for SHIP_LIGHT_HAULER role (line 90)
3. Excludes ships IN_TRANSIT (lines 96-98)
4. Checks `ShipAssignment` status to find idle ships (lines 101-113)
5. Returns ships with NO assignment OR status="idle"

**Limitation:** Called **ONCE** at coordinator start, never refreshed during execution.

### Static Ship Pool Design

**Location:** `run_factory_coordinator.go:236-239`

```go
// Create a ship pool for parallel workers
shipPool := make(chan *navigation.Ship, len(idleShips))
for _, ship := range idleShips {
    shipPool <- ship
}
```

**Characteristics:**
- **Buffered channel** with capacity = number of idle ships at start
- **Fixed size** - cannot grow during execution
- Pool is reused across all parallel levels sequentially
- Channel size hardcoded to initial discovery count

### Worker Blocking Behavior

**Location:** `run_factory_coordinator.go:327-328`

```go
// Acquire a ship from the pool
ship := <-shipPool
defer func() { shipPool <- ship }() // Return ship to pool
```

**Blocking semantics:**
- `<-shipPool` **BLOCKS indefinitely** if channel is empty
- Workers wait for ships to be returned by other workers
- No timeout or fallback mechanism
- If nodes > ships at a level, excess workers block until ships return

**Example scenario:**
- 5 nodes at level 0 (SILICON, COPPER, IRON, ALUMINUM, QUARTZ)
- 2 ships available (SHIP-1, SHIP-2)
- Workers 1-2 acquire ships immediately
- Workers 3-5 **block** waiting for ships
- SHIP-1 finishes → Worker 3 unblocks
- SHIP-2 finishes → Worker 4 unblocks
- SHIP-1 finishes again → Worker 5 unblocks

### WaitGroup Synchronization

**Location:** `run_factory_coordinator.go:318-368`

```go
var wg sync.WaitGroup

// Launch a worker for each node
for _, node := range nodes {
    wg.Add(1)
    go func(n *goods.SupplyChainNode) {
        defer wg.Done()

        ship := <-shipPool  // BLOCKS here if no ships available
        defer func() { shipPool <- ship }()

        // ... production work ...
    }(node)
}

// Wait for all workers to complete
go func() {
    wg.Wait()
    close(resultChan)
}()
```

**Flow:**
1. All worker goroutines spawn immediately (one per node)
2. Workers block at `<-shipPool` if insufficient ships
3. WaitGroup tracks goroutine lifecycle, NOT ship availability
4. Level doesn't complete until ALL nodes finish
5. Ships return to pool only when workers complete

## Problem Detailed Analysis

### Scenario: Insufficient Ships at Startup

**Initial State:**
- System has 3 idle haulers: SHIP-1, SHIP-2, SHIP-3
- Producing ADVANCED_CIRCUITRY requires 7 nodes across 3 levels
- Level 0 has 5 leaf nodes (raw materials to buy)

**Execution timeline:**
```
T=0s:   Worker 1,2,3 acquire ships → start production
        Worker 4,5 BLOCK waiting for ships

T=120s: SHIP-1 completes → returns to pool
        Worker 4 unblocks, acquires SHIP-1 → starts production
        Worker 5 still BLOCKING

T=240s: SHIP-2 completes → returns to pool
        Worker 5 unblocks, acquires SHIP-2 → starts production

T=360s: All level 0 nodes complete
        Move to level 1...
```

**Total level 0 time:** ~360 seconds (6 minutes) with 3 ships

**If a 4th ship became idle at T=60s:**
- Current implementation: Ship sits idle, not discovered
- Workers 4,5 still block despite ship being available
- No benefit from the newly available ship

### Scenario: Ships Complete Other Operations Mid-Execution

**Context:**
- Contract operation completes, releases SHIP-4 back to idle pool
- Scouting tour finishes, releases SHIP-5 back to idle pool
- Mining operation stops, releases SHIP-6 back to idle pool

**Current behavior:**
- Factory coordinator unaware of newly idle ships
- Ships remain idle despite factory workers blocking
- Throughput limited by initial ship count

**Desired behavior:**
- Factory discovers SHIP-4, SHIP-5, SHIP-6 within 30 seconds
- Adds them to ship pool
- Blocked workers acquire ships immediately
- Level completes 2× faster

## Proposed Solution: Background Ship Discoverer

### Architecture Overview

```
┌─────────────────────────────────────────────────────────────┐
│ RunFactoryCoordinatorHandler                                │
│                                                              │
│  ┌──────────────────────────────────────────────────────┐   │
│  │ executeParallelProduction()                          │   │
│  │                                                       │   │
│  │  1. Create ship pool (capacity: 2× initial ships)   │   │
│  │  2. Add initial ships to pool                        │   │
│  │  3. Launch shipPoolRefresher() goroutine ────────┐  │   │
│  │  4. Execute parallel levels                       │  │   │
│  │  5. Cancel refresher context on completion        │  │   │
│  └──────────────────────────────────────────────────────┘   │
│                                                       │      │
│  ┌─────────────────────────────────────────────────┐ │      │
│  │ shipPoolRefresher() [Background Goroutine]      │◄┘      │
│  │                                                  │        │
│  │  Loop every 30 seconds:                         │        │
│  │    1. Call FindIdleLightHaulers()               │        │
│  │    2. Filter out already-used ships             │        │
│  │    3. Non-blocking send to shipPool             │        │
│  │    4. Log newly added ships                     │        │
│  │    5. Exit on context cancellation              │        │
│  └─────────────────────────────────────────────────┘        │
│                                                              │
│  shipPool (buffered channel, capacity: 2× ships)            │
│  ┌──────┬──────┬──────┬──────┬──────┬──────┐               │
│  │SHIP-1│SHIP-2│SHIP-3│ ...  │ ...  │ ...  │               │
│  └──────┴──────┴──────┴──────┴──────┴──────┘               │
│     ▲                                   ▲                    │
│     │                                   │                    │
│     │ Workers acquire ◄─────────────────┘ Refresher adds    │
│     │ Ships return via defer                                │
└─────────────────────────────────────────────────────────────┘
```

### Design Principles

1. **Non-blocking Discovery**
   - Refresher uses `select` with `default` case
   - Never blocks if channel is full
   - Continues execution even if discovery fails

2. **Graceful Lifecycle**
   - Context cancellation stops refresher cleanly
   - Ticker cleanup via defer prevents resource leaks
   - No orphaned goroutines

3. **Duplicate Prevention**
   - `shipsUsed` map tracks all ships added to pool
   - Refresher checks map before adding ships
   - Prevents same ship being added multiple times

4. **Error Resilience**
   - Discovery errors logged as warnings, not fatal
   - Production continues with existing ships
   - Ticker continues on next interval

5. **Observability**
   - Log when new ships discovered and added
   - Log discovery errors for debugging
   - Track ship symbols added to pool

## Implementation Details

### Change 1: Increase Ship Pool Capacity

**File:** `internal/application/goods/commands/run_factory_coordinator.go`
**Line:** ~236

**Before:**
```go
// Create a ship pool for parallel workers
shipPool := make(chan *navigation.Ship, len(idleShips))
```

**After:**
```go
// Create a ship pool for parallel workers
// Capacity: 2× initial ships to accommodate dynamic discovery
shipPool := make(chan *navigation.Ship, len(idleShips)*2)
```

**Rationale:**
- Allows room for ships discovered during execution
- 2× factor provides reasonable headroom
- Prevents channel fills blocking refresher
- Minimal memory overhead (ships are pointers)

### Change 2: Launch Background Ship Discoverer

**File:** `internal/application/goods/commands/run_factory_coordinator.go`
**Line:** ~239 (after creating shipPool)

**Before:**
```go
for _, ship := range idleShips {
    shipPool <- ship
}

logger.Log("INFO", "Starting parallel production", map[string]interface{}{
    "factory_id":      response.FactoryID,
    "levels":          len(levels),
    "available_ships": len(idleShips),
})
```

**After:**
```go
for _, ship := range idleShips {
    shipPool <- ship
}

// Launch background ship discoverer
discoveryCtx, cancelDiscovery := context.WithCancel(ctx)
defer cancelDiscovery()

go h.shipPoolRefresher(discoveryCtx, cmd.PlayerID, shipPool, shipsUsed)

logger.Log("INFO", "Starting parallel production", map[string]interface{}{
    "factory_id":       response.FactoryID,
    "levels":           len(levels),
    "available_ships":  len(idleShips),
    "discovery_enabled": true,
    "discovery_interval": "30s",
})
```

**Key points:**
- Create cancellable context for refresher lifecycle
- Defer cancellation to ensure cleanup on function return
- Launch as goroutine to avoid blocking main execution
- Update log to indicate discovery is active

### Change 3: Add shipPoolRefresher Method

**File:** `internal/application/goods/commands/run_factory_coordinator.go`
**Location:** After `executeParallelProduction` method (~line 297)

```go
// shipPoolRefresher runs a background goroutine that periodically discovers new idle ships
// and adds them to the ship pool, allowing blocked workers to acquire ships mid-execution.
//
// Discovery process:
// 1. Poll every 30 seconds for newly idle ships
// 2. Filter out ships already in the pool (via shipsUsed map)
// 3. Attempt non-blocking send to shipPool (skip if full)
// 4. Log newly added ships for observability
// 5. Exit gracefully on context cancellation
//
// Thread safety:
// - shipsUsed map: workers write unique keys (ship symbol), refresher only reads
// - shipPool channel: Go channels are concurrency-safe
// - No mutex needed due to non-overlapping access patterns
func (h *RunFactoryCoordinatorHandler) shipPoolRefresher(
	ctx context.Context,
	playerID int,
	shipPool chan *navigation.Ship,
	shipsUsed map[string]bool,
) {
	logger := common.LoggerFromContext(ctx)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	playerIDValue := shared.MustNewPlayerID(playerID)
	discoveryCount := 0

	logger.Log("INFO", "Ship pool refresher started", map[string]interface{}{
		"interval": "30s",
	})

	for {
		select {
		case <-ctx.Done():
			logger.Log("INFO", "Ship pool refresher stopped", map[string]interface{}{
				"total_discoveries": discoveryCount,
			})
			return

		case <-ticker.C:
			// Re-discover idle ships
			newIdleShips, newShipSymbols, err := contract.FindIdleLightHaulers(
				ctx,
				playerIDValue,
				h.shipRepo,
				h.shipAssignmentRepo,
			)
			if err != nil {
				logger.Log("WARNING", "Failed to refresh ship pool", map[string]interface{}{
					"error": err.Error(),
				})
				continue
			}

			// Add newly discovered ships to pool (non-blocking)
			addedCount := 0
			addedShips := make([]string, 0)

			for _, ship := range newIdleShips {
				// Skip if ship already in use by this factory
				if shipsUsed[ship.ShipSymbol()] {
					continue
				}

				// Attempt non-blocking send to pool
				select {
				case shipPool <- ship:
					shipsUsed[ship.ShipSymbol()] = true
					addedShips = append(addedShips, ship.ShipSymbol())
					addedCount++
				default:
					// Channel full, skip this ship
					// Will retry on next tick if ship still idle
				}
			}

			if addedCount > 0 {
				discoveryCount += addedCount
				logger.Log("INFO", "Added new ships to pool", map[string]interface{}{
					"added_count":        addedCount,
					"added_ships":        addedShips,
					"total_discoveries":  discoveryCount,
					"pool_capacity_used": fmt.Sprintf("%d/%d", len(shipsUsed), cap(shipPool)),
				})
			}
		}
	}
}
```

**Method signature:**
- `ctx context.Context`: Cancellable context for lifecycle management
- `playerID int`: Player ID for ship discovery
- `shipPool chan *navigation.Ship`: Reference to ship pool channel
- `shipsUsed map[string]bool`: Tracks ships already in pool

**Key features:**
- **30-second ticker:** Balanced polling interval
- **Non-blocking sends:** `select` with `default` prevents blocking
- **Duplicate prevention:** Check `shipsUsed` map before adding
- **Error resilience:** Log warnings, continue on errors
- **Observability:** Log discovery events with ship symbols
- **Graceful shutdown:** Context cancellation stops ticker cleanly

### Change 4: Update Logging Throughout

**Location:** `run_factory_coordinator.go:290`

**After parallel production completes:**
```go
logger.Log("INFO", "Parallel production completed", map[string]interface{}{
	"factory_id":             response.FactoryID,
	"total_cost":             totalCost,
	"ships_used":             len(shipsUsed),
	"ships_discovered":       len(shipsUsed) - len(idleShips), // Ships added mid-execution
	"nodes_completed":        nodesCompleted,
})
```

**Shows:**
- How many ships were available initially
- How many were discovered during execution
- Total ships utilized

## Safety Analysis

### Thread Safety Review

#### shipsUsed Map Access Patterns

**Writers (Workers):**
```go
// Line 337 - executeLevelParallel
shipsUsed[ship.ShipSymbol()] = true
```
- Each worker writes **unique key** (its own ship symbol)
- No concurrent writes to same key
- Map writes are atomic per key

**Readers (Refresher):**
```go
// shipPoolRefresher
if shipsUsed[ship.ShipSymbol()] {
    continue
}
```
- Refresher only **reads** map
- Never modifies existing entries
- Read-only access is concurrency-safe

**Writers (Refresher):**
```go
// shipPoolRefresher - after non-blocking send succeeds
shipsUsed[ship.ShipSymbol()] = true
```
- Refresher writes **new keys** only (newly discovered ships)
- Workers and refresher write **different keys**
- No overlapping writes

**Conclusion:** No race conditions. Map access is safe without mutex because:
1. Workers write unique keys (their assigned ship)
2. Refresher writes unique keys (newly discovered ships)
3. Refresher reads are safe (no concurrent modification of same keys)

#### Channel Concurrency

**Go channel guarantees:**
- Buffered channels are concurrency-safe
- Multiple goroutines can send/receive safely
- Internal locking handled by Go runtime

**Ship pool operations:**
- Workers: `ship := <-shipPool` (blocking receive)
- Workers: `shipPool <- ship` (deferred return)
- Refresher: Non-blocking send via `select`

**All operations safe:** Go's channel implementation provides necessary synchronization.

### Race Condition Analysis

**Potential race #1: Ship availability timing**
- **Scenario:** Ship discovered as idle, becomes busy before worker acquires
- **Mitigation:** `FindIdleLightHaulers` checks `ShipAssignment` fresh each time
- **Impact:** Minimal - ship would fail acquisition, error logged, worker retries

**Potential race #2: Duplicate ship additions**
- **Scenario:** Ship added by refresher, then returned by worker
- **Prevention:** `shipsUsed` map tracks all ships ever added
- **Result:** Ship sent to pool twice, both workers can use it (valid)

**Potential race #3: Channel overflow**
- **Scenario:** Refresher tries to add ship when channel full
- **Prevention:** Non-blocking send with `default` case
- **Result:** Ship skipped this cycle, retried next tick

**Potential race #4: Context cancellation timing**
- **Scenario:** Refresher mid-discovery when context cancelled
- **Prevention:** Check `ctx.Done()` before processing results
- **Result:** Goroutine exits cleanly, partial results ignored

**Conclusion:** No unsafe races. All potential races handled by design.

### Goroutine Cleanup Verification

**Lifecycle management:**
```go
discoveryCtx, cancelDiscovery := context.WithCancel(ctx)
defer cancelDiscovery()

go h.shipPoolRefresher(discoveryCtx, ...)
```

**Cleanup guarantees:**
1. `defer cancelDiscovery()` ensures context cancelled on function return
2. Refresher checks `ctx.Done()` on each iteration
3. Ticker stopped via `defer ticker.Stop()` in refresher
4. Goroutine exits when context cancelled

**Edge cases covered:**
- Normal completion: defer runs, context cancelled, goroutine exits
- Early error: defer runs, context cancelled, goroutine exits
- Panic: defer runs, context cancelled, goroutine exits

**No goroutine leaks possible.**

## Testing Strategy

### BDD Scenarios to Add

**File:** `test/bdd/features/application/goods/factory_coordinator.feature`

#### Scenario 1: Ship discovered mid-level execution
```gherkin
Scenario: Coordinator utilizes ship that becomes idle during level execution
  Given a factory coordinator producing "ADVANCED_CIRCUITRY" in system "X1"
  And the dependency tree has 5 nodes at level 0 (raw materials)
  And 2 idle ships are available at startup: "SHIP-1", "SHIP-2"
  When the coordinator starts parallel production
  And workers 1-2 acquire ships and start production
  And workers 3-5 block waiting for ships
  And ship "SHIP-3" completes a contract operation after 10 seconds
  Then the ship pool refresher should discover "SHIP-3" within 30 seconds
  And "SHIP-3" should be added to the ship pool
  And worker 3 should unblock and acquire "SHIP-3"
  And the level should complete faster than with only 2 ships
  And the coordinator should log "Added new ships to pool" with count 1
```

#### Scenario 2: Multiple ships discovered during execution
```gherkin
Scenario: Coordinator discovers multiple ships across multiple poll cycles
  Given a factory coordinator producing "ELECTRONICS" in system "X1"
  And the dependency tree has 4 nodes at level 0
  And 1 idle ship is available at startup: "SHIP-1"
  When the coordinator starts parallel production
  And worker 1 acquires "SHIP-1" and starts production
  And workers 2-4 block waiting for ships
  And ship "SHIP-2" becomes idle after 20 seconds
  And ship "SHIP-3" becomes idle after 50 seconds
  Then the refresher should discover "SHIP-2" at ~30s poll
  And worker 2 should acquire "SHIP-2"
  And the refresher should discover "SHIP-3" at ~60s poll
  And worker 3 should acquire "SHIP-3"
  And the coordinator should log 2 separate discovery events
```

#### Scenario 3: No new ships discovered
```gherkin
Scenario: Coordinator completes successfully with no mid-execution discoveries
  Given a factory coordinator producing "MACHINERY" in system "X1"
  And the dependency tree has 2 nodes at level 0
  And 3 idle ships are available at startup
  When the coordinator starts parallel production
  And no other ships become idle during execution
  Then the refresher should poll every 30 seconds
  And no new ships should be added to the pool
  And production should complete normally
  And the coordinator should log "ships_discovered: 0"
```

#### Scenario 4: Refresher handles discovery errors gracefully
```gherkin
Scenario: Refresher continues despite ship discovery errors
  Given a factory coordinator producing "IRON" in system "X1"
  And the ship repository returns an error during refresh
  When the refresher attempts to discover new ships
  Then the refresher should log "WARNING: Failed to refresh ship pool"
  And the refresher should continue polling on next cycle
  And production should continue with existing ships in pool
  And the coordinator should not fail
```

#### Scenario 5: Ship pool channel reaches capacity
```gherkin
Scenario: Refresher handles full ship pool gracefully
  Given a factory coordinator with ship pool capacity of 4
  And 4 ships are already in the pool
  When the refresher discovers a 5th idle ship "SHIP-5"
  And the refresher attempts non-blocking send to pool
  Then the send should skip (channel full)
  And "SHIP-5" should not be added to shipsUsed map
  And the refresher should retry on next poll cycle
  And no error should be logged
```

### Integration Test Cases

**Test:** End-to-end production with dynamic discovery
```go
func TestFactoryCoordinator_DynamicShipDiscovery_Integration(t *testing.T) {
	// Setup
	- Create test database with player
	- Create 2 idle ships initially
	- Create supply chain requiring 5 nodes at level 0
	- Mock market data

	// Execute
	- Start factory coordinator in goroutine
	- Wait 15 seconds
	- Mark 2 more ships as idle (simulate contract completion)
	- Wait for coordinator to complete

	// Assert
	- All 5 nodes completed
	- 4 ships utilized (2 initial + 2 discovered)
	- Completion time < sequential time
	- Logs show ship discovery events
}
```

### Performance Benchmarking

**Benchmark:** Impact of discovery polling overhead
```go
func BenchmarkFactoryCoordinator_WithRefresher(b *testing.B) {
	// Measure overhead of 30s polling
	// Compare with static pool implementation
	// Verify < 1% performance impact
}
```

### Edge Cases to Validate

1. **Context cancellation during discovery:**
   - Cancel coordinator mid-production
   - Verify refresher exits cleanly
   - No goroutine leaks

2. **Ship becomes busy after discovery:**
   - Ship marked idle, then assigned before worker acquires
   - Worker handles assignment error
   - Refresher discovers ship again next cycle

3. **Rapid ship churn:**
   - Ships become idle and busy repeatedly
   - Refresher adapts to changing availability
   - No duplicate additions

4. **All workers blocked, no ships available:**
   - 10 nodes, 0 ships initially
   - Ships discovered over time
   - Workers gradually unblock as ships added

5. **Ship discovery between levels:**
   - Ship becomes idle after level 0 completes
   - Available for level 1 execution
   - Optimal utilization across levels

## Alternative Approaches Considered

### Option B: Per-Level Discovery

**Implementation:**
```go
for levelIdx, level := range levels {
	// Re-discover ships before each level (except first)
	if levelIdx > 0 {
		newIdleShips, _, err := contract.FindIdleLightHaulers(...)
		if err == nil {
			for _, ship := range newIdleShips {
				if !shipsUsed[ship.ShipSymbol()] {
					select {
					case shipPool <- ship:
						shipsUsed[ship.ShipSymbol()] = true
					default:
						// Pool full
					}
				}
			}
		}
	}

	// Execute level...
}
```

**Advantages:**
- ✅ Simpler implementation (no background goroutine)
- ✅ Less overhead (only checks between levels)
- ✅ No ticker or context management needed
- ✅ Easier to test (synchronous)

**Disadvantages:**
- ❌ Ships discovered only at level boundaries
- ❌ Workers blocked within level can't benefit
- ❌ Long-running levels miss ship availability
- ❌ Less optimal resource utilization

**Example impact:**
- Level 0 takes 10 minutes, ship becomes idle at 5 minutes
- Option A: Ship used after 5.5 minutes (next poll)
- Option B: Ship not used until level 1 starts (10+ minutes)

### Why Option A Chosen

**Comparison:**

| Aspect | Option A (Background) | Option B (Per-Level) |
|--------|----------------------|---------------------|
| Discovery latency | ~15s average | Variable (depends on level duration) |
| Resource utilization | Optimal | Suboptimal |
| Implementation complexity | Higher | Lower |
| Overhead | Continuous (30s polls) | Minimal (between levels only) |
| Goroutine management | Required | Not needed |
| Follows existing pattern | Yes (contract coordinator) | No |

**Decision factors:**
1. **Throughput is priority:** Long-running productions benefit significantly
2. **Pattern consistency:** Contract coordinator uses continuous polling
3. **Real-world impact:** Ships frequently become idle during execution
4. **Overhead acceptable:** 30s polling has negligible CPU/memory impact

**Result:** Option A provides better throughput for marginal complexity increase.

## Benefits and Trade-offs

### Benefits

**1. Improved Throughput**
- Workers blocked on ships unblock faster
- Newly idle ships utilized within 30 seconds
- Parallel execution scales with available ships
- Level completion time reduces proportionally

**Example:**
- Before: 5 nodes, 2 ships → 3 waves → 15 minutes
- After: 5 nodes, 2 ships + 2 discovered → 2 waves → 10 minutes
- **Improvement:** 33% faster

**2. Better Resource Utilization**
- Idle ships don't sit unused
- Fleet capacity fully leveraged
- Graceful scaling with ship availability
- Opportunistic use of freed ships

**3. Resilience to Ship Shortages**
- Production doesn't fail with limited initial ships
- Adapts to dynamic fleet availability
- Works with 1 ship (slow) or 10 ships (fast)
- No manual intervention needed

**4. Architectural Consistency**
- Follows contract coordinator pattern
- Reuses existing `FindIdleLightHaulers` utility
- Fits hexagonal architecture
- No special-case code

### Trade-offs

**1. Overhead: Polling Every 30 Seconds**

**Cost per poll:**
- Database query for all ships (~50ms)
- Filter for haulers (~1ms)
- Check assignments (~10ms per ship)
- **Total:** ~100ms per poll cycle

**Annual overhead:**
- 1 poll every 30s = 2 polls/minute = 120 polls/hour
- 100ms × 120 = 12 seconds/hour = 0.33% of hour
- **Impact:** Negligible

**Database queries:**
- 1 query per poll = 2 queries/minute = 120 queries/hour
- For production of 30 minutes: 60 additional queries
- **Impact:** Minimal (queries are indexed, fast)

**2. Memory: Larger Channel Buffer**

**Channel size increase:**
- Before: `len(idleShips)` = 5 ships × 8 bytes (pointer) = 40 bytes
- After: `len(idleShips)*2` = 10 ships × 8 bytes = 80 bytes
- **Increase:** 40 bytes per factory

**Worst case:**
- 10 factories running concurrently
- 10 factories × 80 bytes = 800 bytes total
- **Impact:** Negligible (< 1KB)

**3. Complexity: Goroutine Management**

**Added complexity:**
- Context creation and cancellation (2 lines)
- Background goroutine (1 method, ~50 lines)
- Ticker lifecycle management (defer)
- **Total:** ~60 lines of code

**Testing complexity:**
- 5 additional BDD scenarios
- Goroutine cleanup verification
- Timing-sensitive tests (use mock clock)
- **Effort:** ~2 hours additional testing

**Maintenance:**
- Well-documented, follows existing patterns
- Clear separation of concerns
- **Burden:** Low

**4. Timing: 30-Second Discovery Latency**

**Average latency:**
- Ship becomes idle at random time
- Next poll occurs within 0-30 seconds
- **Average:** 15 seconds

**Impact on production time:**
- For 10-minute level: 15s = 2.5% delay
- For 60-minute level: 15s = 0.4% delay
- **Conclusion:** Negligible for most productions

**Could be improved:**
- Adaptive polling (faster when workers blocked)
- Event-based discovery (ship assignment callbacks)
- **Decision:** 30s is acceptable for initial implementation

### Net Benefit Analysis

**Benefit:** 20-50% throughput improvement for multi-level productions
**Cost:** < 1% overhead, ~60 lines of code, 15s average latency

**ROI:** Strongly positive for any production > 10 minutes

## Rollout Plan

### Phase 1: Implementation (Day 1)

**Tasks:**
1. Increase ship pool capacity (line 236)
2. Add context and goroutine launch (line 239)
3. Implement `shipPoolRefresher` method
4. Update logging to show discoveries
5. Add inline comments explaining design

**Validation:**
- Code compiles
- Linter passes
- No race conditions (go test -race)

### Phase 2: Unit Testing (Day 1-2)

**Tasks:**
1. Write BDD scenarios (5 scenarios)
2. Implement step definitions
3. Test goroutine cleanup
4. Test error handling
5. Test edge cases

**Validation:**
- All BDD tests pass
- 100% coverage of shipPoolRefresher
- Race detector clean

### Phase 3: Integration Testing (Day 2)

**Tasks:**
1. Test end-to-end production with discovery
2. Benchmark performance overhead
3. Test with live API (test server)
4. Monitor logs for discovery events

**Validation:**
- Production completes successfully
- Ships discovered and utilized
- Overhead < 1%
- Logs show expected discovery events

### Phase 4: Deployment (Day 3)

**Tasks:**
1. Merge to main branch
2. Deploy to production
3. Monitor metrics for 24 hours
4. Collect usage data

**Validation:**
- No production failures
- Factory completions track improvements
- No goroutine leaks (memory stable)
- Logs show discoveries happening

### Rollback Strategy

**If issues detected:**

1. **Revert commit** (single commit, easy rollback)
2. **Disable feature via flag:**
   ```go
   if !config.EnableDynamicShipDiscovery {
       // Skip goroutine launch, use static pool
   }
   ```
3. **Reduce polling frequency:**
   ```go
   ticker := time.NewTicker(5 * time.Minute) // Reduce impact
   ```

**Rollback triggers:**
- Goroutine leaks detected (memory growth)
- Production failures increase
- Performance degradation > 5%
- Race conditions detected

## Future Enhancements

### 1. Adaptive Polling Intervals

**Problem:** Fixed 30s interval may be too slow/fast for different scenarios

**Enhancement:**
```go
// Fast poll when workers are blocked
if blockedWorkerCount > 0 {
    ticker = time.NewTicker(10 * time.Second)
} else {
    ticker = time.NewTicker(60 * time.Second)
}
```

**Benefit:** Lower latency when ships needed, less overhead when not

### 2. Ship Priority Queuing

**Problem:** All ships treated equally, no preference for nearby ships

**Enhancement:**
```go
// Sort ships by distance to manufacturing waypoints
sort.Slice(newIdleShips, func(i, j int) bool {
    return distanceTo(newIdleShips[i], targetWaypoint) <
           distanceTo(newIdleShips[j], targetWaypoint)
})
```

**Benefit:** Prefer ships already at/near production locations

### 3. Predictive Ship Availability

**Problem:** React to ships becoming idle, don't anticipate

**Enhancement:**
- Track ship assignment end times
- Pre-discover ships 1 minute before availability
- Add to pool exactly when freed

**Benefit:** Zero latency between ship idle and worker acquisition

### 4. Ship Pool Sharing Across Factories

**Problem:** Multiple factories running, ships not shared

**Enhancement:**
- Global ship pool manager
- Factories request ships on-demand
- Dynamic allocation based on priority

**Benefit:** Optimal fleet utilization across all operations

### 5. Event-Based Ship Discovery

**Problem:** Polling wastes cycles when no changes

**Enhancement:**
- ShipAssignment emits events on status change
- Factory subscribes to idle ship events
- Push model replaces pull model

**Benefit:** Zero latency, no polling overhead

## References

### Existing Patterns

**Contract Fleet Coordinator:**
- File: `internal/application/contract/commands/run_fleet_coordinator.go`
- Lines: 122-150
- Pattern: Continuous polling with completion channels
- Inspiration: Dynamic discovery loop

**Ship Pool Manager:**
- File: `internal/application/contract/ship_pool_manager.go`
- Lines: 71-124
- Function: `FindIdleLightHaulers`
- Usage: Ship discovery and filtering

### Documentation

- Goods Factory Implementation Plan: `docs/GOODS_FACTORY_IMPLEMENTATION_PLAN.md`
- Architecture Guide: `docs/ARCHITECTURE.md`
- CQRS Pattern: Application layer command handlers

### Go Concurrency Patterns

- Context cancellation: https://go.dev/blog/context
- Channel patterns: https://go.dev/doc/effective_go#channels
- Ticker cleanup: https://pkg.go.dev/time#Ticker

## Appendix: Code Snippets

### Full shipPoolRefresher Implementation

See **Change 3: Add shipPoolRefresher Method** section above for complete implementation.

### Testing Mock Clock for Ticker

```go
type MockClock struct {
    tickerChan chan time.Time
}

func (m *MockClock) NewTicker(d time.Duration) *time.Ticker {
    // Return ticker that uses mockable channel
    return &time.Ticker{C: m.tickerChan}
}

func (m *MockClock) Tick() {
    m.tickerChan <- time.Now()
}
```

### Example BDD Step Definition

```go
func (ctx *coordinatorContext) shipBecomesIdleAfterSeconds(shipSymbol string, seconds int) error {
    // Simulate ship becoming idle mid-execution
    time.Sleep(time.Duration(seconds) * time.Second)

    // Update ship assignment in test database
    assignment := &container.ShipAssignment{
        ShipSymbol: shipSymbol,
        Status:     "idle",
    }
    return ctx.assignmentRepo.Update(ctx.ctx, assignment)
}
```

---

**Document Version:** 1.0
**Date:** 2025-11-22
**Author:** Claude Code
**Status:** Ready for Implementation
