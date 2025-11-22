# Goods Factory - Integration Testing Guide

## Overview

This document provides step-by-step instructions for manually validating the Goods Factory implementation against a live SpaceTraders API. These integration tests verify end-to-end functionality including worker commands, coordinator orchestration, persistence, and CLI operations.

**Prerequisites:**
- Running SpaceTraders daemon with database connection
- Active SpaceTraders account with API token
- At least 2-3 idle hauler ships in a single system
- Scout ships running in target system (for fresh market data)
- Sufficient credits for purchasing raw materials (~10,000+)

**Test Environment:**
- Test Server: SpaceTraders API (https://api.spacetraders.io/v2)
- Target System: Choose a system with STRONG market activity
- Required Ship Types: SHIP_LIGHT_HAULER (2-3 ships recommended)

## Test Suite 1: Worker Command - Simple Purchase (BUY)

**Objective:** Verify that a worker can buy a raw material from an export market

**Prerequisites:**
- 1 idle hauler ship
- Market selling IRON_ORE with STRONG activity and HIGH supply
- Ship has sufficient cargo capacity (50+ units)

**Test Steps:**

1. **Find a market selling IRON_ORE:**
```bash
spacetraders market get --waypoint <system>-<waypoint>
# Verify: IRON_ORE in exports, supply >= 100, price reasonable
```

2. **Dock ship at market:**
```bash
spacetraders ship dock --ship <ship-symbol>
spacetraders ship navigate --ship <ship-symbol> --destination <market-waypoint>
```

3. **Execute worker command via daemon:**
```bash
# Note: This requires direct daemon container execution
# Worker command is executed internally by coordinator
# Manual test: Use direct purchase command
spacetraders ship purchase --ship <ship-symbol> --good IRON_ORE --quantity 50
```

4. **Verify results:**
```bash
spacetraders ship status --ship <ship-symbol>
# Expected: Cargo contains 50 units of IRON_ORE
# Expected: Credits reduced by (price × 50)
```

**Success Criteria:**
- ✅ Worker successfully navigates to market
- ✅ Worker purchases maximum cargo capacity (or market supply limit)
- ✅ Transaction cost calculated correctly
- ✅ Cargo updated in ship status

**Expected Failure Scenarios:**
- ❌ No cargo space → Error: "no cargo space available"
- ❌ No market sells good → Error: "no market found selling IRON_ORE"

---

## Test Suite 2: Worker Command - Fabrication (FABRICATE)

**Objective:** Verify that a worker can fabricate a manufactured good

**Prerequisites:**
- 1 idle hauler ship
- Manufacturing waypoint that imports IRON_ORE and produces IRON
- Market selling IRON_ORE
- Scout ships polling market data in system

**Test Steps:**

1. **Identify manufacturing waypoint:**
```bash
# Query waypoints in system for FABRICATES exchange
# Look for waypoint with:
#   - Imports: IRON_ORE
#   - Exports: IRON
#   - Exchange type: FABRICATE
# Use waypoint data from database or API
```

2. **Setup: Clear ship cargo:**
```bash
spacetraders ship status --ship <ship-symbol>
# If cargo > 0, sell all cargo first
```

3. **Execute fabrication workflow (manual simulation):**

**Step 3a: Acquire input material (IRON_ORE)**
```bash
# Navigate to market selling IRON_ORE
spacetraders ship navigate --ship <ship-symbol> --destination <market-waypoint>
spacetraders ship dock --ship <ship-symbol>
spacetraders ship purchase --ship <ship-symbol> --good IRON_ORE --quantity 50
```

**Step 3b: Deliver to manufacturing waypoint**
```bash
spacetraders ship navigate --ship <ship-symbol> --destination <manufacturing-waypoint>
spacetraders ship dock --ship <ship-symbol>
```

**Step 3c: Sell inputs to trigger production**
```bash
spacetraders ship sell --ship <ship-symbol> --good IRON_ORE --quantity 50
# This triggers the fabrication process
```

**Step 3d: Poll for output good (IRON) to appear**
```bash
# Wait 30-60 seconds for production
spacetraders market get --waypoint <manufacturing-waypoint>
# Check if IRON now appears in exports
```

**Step 3e: Purchase fabricated good**
```bash
spacetraders ship purchase --ship <ship-symbol> --good IRON --quantity <available>
# Quantity depends on market production rate
```

4. **Verify results:**
```bash
spacetraders ship status --ship <ship-symbol>
# Expected: Cargo contains IRON (quantity varies based on market activity)
```

**Success Criteria:**
- ✅ Worker acquires input materials from market
- ✅ Worker navigates to manufacturing waypoint
- ✅ Worker delivers inputs (sells to waypoint)
- ✅ Worker polls database for market updates (kept fresh by scouts)
- ✅ Worker detects when output good appears in exports
- ✅ Worker purchases fabricated good
- ✅ Variable quantity accepted (whatever market produces)

**Expected Behavior:**
- ⏱️ Production time varies (30s - 10min depending on market activity)
- 📊 Quantity produced is NOT fixed ratio (market-driven, variable amounts)
- ♾️ Polling continues indefinitely until good appears (no timeout)

**Expected Failure Scenarios:**
- ❌ No manufacturing waypoint imports IRON_ORE → Error: "no waypoint found importing IRON_ORE"
- ❌ Scout ships not running → Warning: "market data may be stale"

---

## Test Suite 3: Coordinator - Sequential Production (MVP)

**Objective:** Verify coordinator orchestrates sequential production with single ship

**Prerequisites:**
- 1 idle hauler ship with 100+ cargo capacity
- System with markets and manufacturing for IRON production chain
- Database populated with market data (scouts running)

**Test Steps:**

1. **Verify prerequisites:**
```bash
# Check idle ships
spacetraders ship list --player-id <player-id>
# Expected: At least 1 HAULER with status IDLE

# Verify system has required infrastructure
# - Market sells IRON_ORE
# - Waypoint fabricates IRON from IRON_ORE
```

2. **Start goods factory via CLI:**
```bash
spacetraders goods produce IRON --system <system-symbol> --player-id <player-id>
```

3. **Capture factory ID from output:**
```
✓ Goods factory started successfully
  Factory ID:       goods-factory-abc123
  Target Good:      IRON
  System:           X1-AA123
  Dependency Nodes: 2
  Status:           RUNNING
```

4. **Monitor factory progress:**
```bash
# Poll status every 30 seconds
spacetraders goods status goods-factory-abc123
```

**Expected Output (during execution):**
```
Factory: goods-factory-abc123
Target:  IRON
Status:  ACTIVE
Progress: 50% (1/2 nodes complete)
Quantity: 0 (production in progress)

Dependency Tree:
├── IRON [FABRICATE] ⏳ IN_PROGRESS
│   └── IRON_ORE [BUY] ✅ COMPLETED (50 units acquired)

Active Ships: SHIP-1 (assigned)
Logs:
  [12:30:15] Factory started for IRON in system X1-AA123
  [12:30:16] Discovered 1 idle hauler: SHIP-1
  [12:30:16] Built dependency tree with 2 nodes
  [12:30:17] Sequential execution started
  [12:30:17] Node IRON_ORE (BUY): Started
  [12:30:45] Node IRON_ORE (BUY): Completed - 50 units, 500 credits
  [12:31:00] Node IRON (FABRICATE): Started
  [12:31:05] Inputs delivered to X1-AA123-F1
  [12:31:05] Polling for IRON production (attempt 1)
```

**Expected Output (after completion):**
```
Factory: goods-factory-abc123
Target:  IRON
Status:  COMPLETED
Progress: 100% (2/2 nodes complete)
Quantity: 25 units acquired
Total Cost: 1,250 credits

Dependency Tree:
├── IRON [FABRICATE] ✅ COMPLETED (25 units, 1,250 credits)
│   └── IRON_ORE [BUY] ✅ COMPLETED (50 units, 500 credits)

Completion Time: 2m 15s
Ships Used: 1
```

5. **Verify database persistence:**
```bash
# Check goods_factories table
# Expected: Record with:
#   - status = 'COMPLETED'
#   - quantity_acquired = 25 (or similar, variable)
#   - total_cost = 1250 (or similar)
#   - started_at, completed_at timestamps set
#   - dependency_tree JSON stored
```

6. **Verify ship released:**
```bash
spacetraders ship list --player-id <player-id>
# Expected: SHIP-1 status returned to IDLE
```

**Success Criteria:**
- ✅ Coordinator discovers idle hauler ships via FindIdleLightHaulers
- ✅ Coordinator builds dependency tree (2 nodes for IRON)
- ✅ Coordinator executes nodes sequentially (BUY → FABRICATE)
- ✅ Coordinator uses single ship for all operations (MVP)
- ✅ Coordinator creates factory entity in database (PENDING → ACTIVE → COMPLETED)
- ✅ Coordinator updates metrics (quantity, cost, progress)
- ✅ Coordinator releases ship assignment on completion
- ✅ Factory completes successfully with variable quantity

**Expected Logging:**
```
[INFO] GoodsFactoryCoordinator: Starting production for IRON in X1-AA123
[INFO] GoodsFactoryCoordinator: Discovered 1 idle hauler ship
[INFO] GoodsFactoryCoordinator: Built dependency tree: 2 nodes (1 BUY, 1 FABRICATE)
[INFO] GoodsFactoryCoordinator: Executing sequential production (MVP mode)
[INFO] FactoryWorker: Executing node IRON_ORE (BUY)
[INFO] FactoryWorker: Acquired 50 units of IRON_ORE at 500 credits
[INFO] FactoryWorker: Executing node IRON (FABRICATE)
[INFO] FactoryWorker: Delivered 50 units to manufacturing waypoint
[INFO] FactoryWorker: Polling for IRON production (attempt 1, interval 30s)
[INFO] FactoryWorker: IRON detected in exports, purchasing...
[INFO] FactoryWorker: Acquired 25 units of IRON at 1250 credits
[INFO] GoodsFactoryCoordinator: Production completed - 25 units, 1250 credits
```

---

## Test Suite 4: Coordinator - Multi-Level Dependencies

**Objective:** Verify coordinator handles complex dependency trees with 3+ levels

**Prerequisites:**
- 1 idle hauler ship with 150+ cargo capacity
- System infrastructure for ELECTRONICS production:
  - Market sells SILICON_CRYSTALS
  - Market sells COPPER_ORE
  - Waypoint fabricates COPPER from COPPER_ORE
  - Waypoint fabricates ELECTRONICS from SILICON_CRYSTALS + COPPER

**Dependency Tree:**
```
ELECTRONICS [FABRICATE]
├── SILICON_CRYSTALS [BUY]
└── COPPER [FABRICATE]
    └── COPPER_ORE [BUY]
```

**Test Steps:**

1. **Start production:**
```bash
spacetraders goods produce ELECTRONICS --system <system-symbol> --player-id <player-id>
```

2. **Monitor execution order:**
```bash
# Poll status to observe depth-first traversal
spacetraders goods status <factory-id>
```

**Expected Execution Order (Sequential MVP):**
1. COPPER_ORE [BUY] - Leaf node, processed first
2. COPPER [FABRICATE] - Depends on COPPER_ORE
3. SILICON_CRYSTALS [BUY] - Independent leaf node
4. ELECTRONICS [FABRICATE] - Root node, requires both inputs

3. **Verify completion:**
```bash
spacetraders goods status <factory-id>
# Expected: 4 nodes completed
# Expected: ELECTRONICS acquired (variable quantity)
```

**Success Criteria:**
- ✅ Coordinator builds tree with 4 nodes
- ✅ Coordinator processes nodes in depth-first order
- ✅ Coordinator waits for child completion before parent
- ✅ All 4 nodes complete successfully
- ✅ Final good (ELECTRONICS) acquired

**Performance Expectations:**
- Total time: 5-15 minutes (depends on market activity)
- API calls: ~20-40 (navigation, purchase, sell, dock)
- Database queries: Minimal (scout ships handle market polling)

---

## Test Suite 5: Coordinator - Fleet Discovery

**Objective:** Verify coordinator discovers and filters idle hauler ships

**Prerequisites:**
- Multiple ships with different types and states:
  - SHIP-1: HAULER, IDLE (should be discovered)
  - SHIP-2: PROBE, IDLE (should be filtered out)
  - SHIP-3: HAULER, ACTIVE (should be filtered out)
  - SHIP-4: HAULER, IDLE (should be discovered)

**Test Steps:**

1. **Setup ship states:**
```bash
# Ensure ships are in correct states
# Make SHIP-1 and SHIP-4 idle
# Make SHIP-3 active (assign to another operation)
```

2. **Start factory:**
```bash
spacetraders goods produce IRON --system <system-symbol> --player-id <player-id>
```

3. **Check logs for discovery:**
```bash
spacetraders goods status <factory-id> --verbose
# Expected log:
# "Discovered 2 idle haulers: SHIP-1, SHIP-4"
# "Using ship SHIP-1 for sequential production"
```

**Success Criteria:**
- ✅ Coordinator finds only HAULER ships (excludes PROBE)
- ✅ Coordinator finds only IDLE ships (excludes ACTIVE)
- ✅ Coordinator discovers correct count (2 ships)
- ✅ Coordinator uses first idle ship for MVP (SHIP-1)

**Expected Failure Scenarios:**
- ❌ No idle haulers → Error: "no idle hauler ships available"

---

## Test Suite 6: Coordinator - Player Isolation

**Objective:** Verify coordinator only uses ships belonging to the requesting player

**Prerequisites:**
- Player 1 has idle ships
- Player 2 has idle ships
- Both players in same system

**Test Steps:**

1. **Start factory for Player 1:**
```bash
spacetraders goods produce IRON --system <system> --player-id 1
```

2. **Verify ship discovery:**
```bash
spacetraders goods status <factory-id>
# Expected: Only Player 1 ships discovered
# Expected: No Player 2 ships used
```

**Success Criteria:**
- ✅ Coordinator queries ships with player_id filter
- ✅ Coordinator never discovers other players' ships
- ✅ Production completes using only player's own fleet

---

## Test Suite 7: Error Handling - No Idle Ships

**Objective:** Verify coordinator fails gracefully when no idle ships available

**Prerequisites:**
- All hauler ships ACTIVE or in use

**Test Steps:**

1. **Assign all ships to other operations:**
```bash
# Make all haulers busy
# Ensure no IDLE haulers exist
```

2. **Attempt to start factory:**
```bash
spacetraders goods produce IRON --system <system> --player-id <player-id>
```

3. **Verify error response:**
```
✗ Failed to start goods factory
  Error: no idle hauler ships available in system <system>

  Suggestion: Wait for ships to complete current operations or purchase additional haulers
```

**Success Criteria:**
- ✅ Coordinator fails with clear error message
- ✅ Factory not created in database
- ✅ No partial state left behind

---

## Test Suite 8: Error Handling - Missing Manufacturing Waypoint

**Objective:** Verify coordinator fails when manufacturing waypoint doesn't exist

**Prerequisites:**
- Supply chain requires fabrication of good X
- No waypoint in system imports the required input

**Test Steps:**

1. **Attempt to produce a good with missing manufacturing:**
```bash
# Example: ADVANCED_CIRCUITRY in a system without ELECTRONICS fabrication
spacetraders goods produce ADVANCED_CIRCUITRY --system <limited-system> --player-id <player-id>
```

2. **Verify error during node execution:**
```bash
spacetraders goods status <factory-id>
# Expected:
# Status: FAILED
# Error: "no waypoint found importing SILICON_CRYSTALS in system X1-LIMITED"
```

**Success Criteria:**
- ✅ Coordinator starts successfully
- ✅ Worker fails during node execution
- ✅ Error propagated to coordinator
- ✅ Factory status = FAILED
- ✅ last_error contains descriptive message

---

## Test Suite 9: Persistence and Recovery

**Objective:** Verify factory state persists across daemon restarts

**Test Steps:**

1. **Start long-running production:**
```bash
spacetraders goods produce ADVANCED_CIRCUITRY --system <system> --player-id <player-id>
```

2. **While factory is ACTIVE, stop daemon:**
```bash
# Stop daemon process (SIGTERM)
systemctl stop spacetraders-daemon
# OR
kill <daemon-pid>
```

3. **Verify database state:**
```bash
# Query goods_factories table
# Expected: Factory record exists with status = 'ACTIVE'
# Expected: started_at timestamp set
# Expected: dependency_tree JSON stored
```

4. **Restart daemon:**
```bash
systemctl start spacetraders-daemon
```

5. **Query factory status:**
```bash
spacetraders goods status <factory-id> --player-id <player-id>
```

**Success Criteria:**
- ✅ Factory state persisted during shutdown
- ✅ Factory restored from database on daemon restart
- ✅ Lifecycle state correct (ACTIVE → restored to ACTIVE)
- ✅ Metadata and metrics preserved
- ✅ **Note:** Container does NOT auto-resume (graceful termination in MVP)

**Expected Behavior (MVP):**
- Factory remains in ACTIVE state but container is terminated
- User must manually stop or restart factory
- Future enhancement: Auto-resume capability

---

## Test Suite 10: CLI - Stop Factory

**Objective:** Verify CLI can stop a running factory

**Test Steps:**

1. **Start factory:**
```bash
spacetraders goods produce IRON --system <system> --player-id <player-id>
# Capture factory ID
```

2. **While running, stop factory:**
```bash
spacetraders goods stop <factory-id> --player-id <player-id>
```

3. **Verify response:**
```
✓ Factory stopped successfully
  Factory ID: <factory-id>
  Status:     STOPPED
  Progress:   50% (1/2 nodes complete)

  Production halted. Ship assignments released.
```

4. **Verify database:**
```bash
# Check goods_factories table
# Expected:
#   - status = 'STOPPED'
#   - stopped_at timestamp set
```

5. **Verify ship released:**
```bash
spacetraders ship list --player-id <player-id>
# Expected: Ship status = IDLE (no longer assigned)
```

**Success Criteria:**
- ✅ CLI command stops container
- ✅ Factory transitions to STOPPED state
- ✅ stopped_at timestamp recorded
- ✅ Ship assignment released
- ✅ Partial progress preserved (quantity_acquired, total_cost)

---

## Test Suite 11: CLI - Status with Tree Visualization

**Objective:** Verify CLI displays dependency tree with progress

**Test Steps:**

1. **Start complex production:**
```bash
spacetraders goods produce ELECTRONICS --system <system> --player-id <player-id>
```

2. **Query status with tree flag:**
```bash
spacetraders goods status <factory-id> --tree
```

**Expected Output:**
```
Factory: goods-factory-xyz789
Target:  ELECTRONICS
Status:  ACTIVE
Progress: 75% (3/4 nodes complete)
System:  X1-BB456
Quantity: 0 (production in progress)

Dependency Tree:
├── ELECTRONICS [FABRICATE] ⏳ IN_PROGRESS (polling attempt 2)
    ├── SILICON_CRYSTALS [BUY] ✅ COMPLETED (30 units, 600 credits)
    └── COPPER [FABRICATE] ✅ COMPLETED (20 units, 400 credits)
        └── COPPER_ORE [BUY] ✅ COMPLETED (40 units, 200 credits)

Ships: SHIP-1 (assigned, active at X1-BB456-F4)
Total Cost: 1,200 credits

Recent Logs:
  [13:45:23] Node COPPER_ORE completed - 40 units acquired
  [13:46:01] Node COPPER completed - 20 units acquired
  [13:46:45] Node SILICON_CRYSTALS completed - 30 units acquired
  [13:47:12] Node ELECTRONICS started - polling for production
```

**Success Criteria:**
- ✅ Tree structure displayed correctly
- ✅ Node status icons (⏳ ✅) shown
- ✅ Quantities and costs per node
- ✅ Ship assignments visible
- ✅ Progress percentage accurate

---

## Test Suite 12: Performance Validation

**Objective:** Measure coordinator performance and resource usage

**Test Steps:**

1. **Produce 5 different goods consecutively:**
```bash
for good in IRON COPPER ALUMINUM ELECTRONICS MACHINERY; do
  echo "Producing $good..."
  spacetraders goods produce $good --system <system> --player-id <player-id>
  # Wait for completion
  # Record metrics
done
```

2. **Collect metrics:**
- Total execution time per good
- API calls per good (check daemon logs)
- Database queries per good
- Memory usage (daemon process)
- Success rate

**Success Criteria:**
- ✅ Average production time: 2-10 minutes per good
- ✅ API calls: 10-40 per good (depends on dependency depth)
- ✅ Database queries: Minimal (scouts handle polling)
- ✅ Memory: <5MB per active factory
- ✅ Success rate: 100% (no random failures)

**Performance Targets:**
| Metric | MVP Target | Production Target |
|--------|------------|------------------|
| Simple good (2 nodes) | <3 min | <2 min |
| Medium good (4 nodes) | <8 min | <5 min |
| Complex good (6+ nodes) | <15 min | <10 min |
| API calls per node | <10 | <8 |
| Memory per factory | <5MB | <3MB |

---

## Test Suite 13: Market-Driven Behavior Validation

**Objective:** Verify production acquires variable quantities based on market availability

**Test Steps:**

1. **Produce IRON 3 times in same system:**
```bash
# Run 1
spacetraders goods produce IRON --system <system> --player-id <player-id>
# Note: Quantity acquired = X1

# Run 2 (after market recovers)
spacetraders goods produce IRON --system <system> --player-id <player-id>
# Note: Quantity acquired = X2

# Run 3
spacetraders goods produce IRON --system <system> --player-id <player-id>
# Note: Quantity acquired = X3
```

2. **Verify quantities are different:**
```bash
# X1, X2, X3 should vary based on:
#   - Market supply levels at time of purchase
#   - Ship cargo capacity
#   - Market production rate (for fabrication)
```

**Success Criteria:**
- ✅ Quantities are NOT identical across runs
- ✅ No fixed conversion ratios used (50 ORE → 25 IRON is NOT guaranteed)
- ✅ Production succeeds with whatever quantity is available
- ✅ No errors due to "insufficient quantity"

**Expected Behavior:**
- Worker buys maximum of: min(cargo_space, market_supply)
- Worker fabricates whatever market produces (time-based, not ratio-based)
- Quantities logged clearly in production results

---

## Test Suite 14: Infinite Polling Validation

**Objective:** Verify worker polls indefinitely until production completes

**Prerequisites:**
- Manufacturing waypoint with WEAK market activity (slow production)
- OR stop scout ships temporarily (force stale market data)

**Test Steps:**

1. **Start production with slow market:**
```bash
spacetraders goods produce IRON --system <weak-system> --player-id <player-id>
```

2. **Monitor polling logs:**
```bash
spacetraders goods status <factory-id> --verbose
# Expected logs:
# "Polling for IRON production (attempt 1, next check in 30s)"
# "Polling for IRON production (attempt 2, next check in 60s)"
# "Polling for IRON production (attempt 3, next check in 60s)"
# ...continues indefinitely
```

3. **Wait for production (could take 10-20+ minutes):**
```bash
# Worker continues polling at 60s intervals
# No timeout error occurs
```

4. **Eventually succeeds:**
```bash
spacetraders goods status <factory-id>
# Expected: Status = COMPLETED
# Expected: IRON acquired (variable quantity)
```

**Success Criteria:**
- ✅ Worker polls indefinitely (no timeout)
- ✅ Interval pattern: 30s, 60s, 60s, ...
- ✅ Eventually detects good when it appears
- ✅ No "timeout" or "production failed" errors
- ✅ Graceful exit on context cancellation (daemon stop)

**Expected Warnings:**
```
[WARN] FactoryWorker: Production taking longer than expected (20+ polls)
[INFO] FactoryWorker: Continuing to poll - use 'goods stop' to cancel
```

---

## Test Suite 15: Full End-to-End Validation

**Objective:** Produce a complex good (ADVANCED_CIRCUITRY) with 6+ node tree

**Prerequisites:**
- System with complete manufacturing chain:
  - Raw materials: COPPER_ORE, SILICON_CRYSTALS
  - Intermediate goods: COPPER, ELECTRONICS, MICROPROCESSORS
  - Final good: ADVANCED_CIRCUITRY
- 1-2 idle hauler ships (150+ cargo capacity)
- Sufficient credits (~20,000+)
- Scout ships active in system

**Dependency Tree:**
```
ADVANCED_CIRCUITRY [FABRICATE]
├── ELECTRONICS [FABRICATE]
│   ├── SILICON_CRYSTALS [BUY]
│   └── COPPER [FABRICATE]
│       └── COPPER_ORE [BUY]
└── MICROPROCESSORS [FABRICATE]
    ├── SILICON_CRYSTALS [BUY]
    └── COPPER [FABRICATE]
        └── COPPER_ORE [BUY]
```

**Test Steps:**

1. **Start production:**
```bash
spacetraders goods produce ADVANCED_CIRCUITRY --system <system> --player-id <player-id>
```

2. **Monitor progress (expect 15-30 minutes):**
```bash
watch -n 30 'spacetraders goods status <factory-id> --tree'
```

3. **Observe sequential execution:**
- Worker processes all 6 nodes one at a time
- Depth-first traversal order
- Each node completes before next starts

4. **Verify completion:**
```bash
spacetraders goods status <factory-id>
# Expected:
#   - Status: COMPLETED
#   - Progress: 100% (6/6 nodes)
#   - ADVANCED_CIRCUITRY acquired (variable quantity)
#   - Total cost tracked across all purchases
```

**Success Criteria:**
- ✅ Coordinator builds 6-node tree correctly
- ✅ All nodes execute in valid dependency order
- ✅ No deadlocks or stuck states
- ✅ All intermediate goods acquired successfully
- ✅ Final good (ADVANCED_CIRCUITRY) produced
- ✅ Total time: <30 minutes
- ✅ Database persistence correct
- ✅ Ship released after completion

**This is the ultimate integration test** - if this passes, the Goods Factory MVP is production-ready for sequential execution.

---

## Validation Checklist

Use this checklist to track integration test progress:

### Core Functionality
- [ ] Test Suite 1: Simple Purchase (BUY)
- [ ] Test Suite 2: Fabrication (FABRICATE)
- [ ] Test Suite 3: Sequential Production
- [ ] Test Suite 4: Multi-Level Dependencies
- [ ] Test Suite 5: Fleet Discovery
- [ ] Test Suite 6: Player Isolation

### Error Handling
- [ ] Test Suite 7: No Idle Ships
- [ ] Test Suite 8: Missing Manufacturing Waypoint

### Persistence & CLI
- [ ] Test Suite 9: Persistence and Recovery
- [ ] Test Suite 10: Stop Factory
- [ ] Test Suite 11: Status Visualization

### Performance & Behavior
- [ ] Test Suite 12: Performance Validation
- [ ] Test Suite 13: Market-Driven Behavior
- [ ] Test Suite 14: Infinite Polling

### End-to-End
- [ ] Test Suite 15: Complex Good Production

---

## Known Limitations (MVP)

**Sequential Execution Only:**
- MVP uses single ship for all nodes
- Parallel execution NOT implemented
- Performance limited by sequential bottleneck
- See GOODS_FACTORY_GAP_ANALYSIS.md for upgrade path

**No Auto-Resume:**
- Daemon restart does NOT auto-resume active factories
- User must manually restart or stop
- Future enhancement planned

**Market Data Dependency:**
- Requires scout ships running in target system
- Stale market data may cause long polling times
- No fallback to direct API queries (MVP)

**No Cost Analysis:**
- MVP does not calculate profitability
- No buy vs fabricate cost comparison
- Production may result in net loss
- Future enhancement planned

---

## Reporting Issues

When reporting integration test failures:

1. **Include full context:**
   - Factory ID
   - Target good
   - System symbol
   - Player ID
   - Ship symbols used

2. **Attach logs:**
```bash
spacetraders container logs <container-id>
spacetraders goods status <factory-id> --verbose
```

3. **Database state:**
```sql
SELECT * FROM goods_factories WHERE id = '<factory-id>';
SELECT * FROM ship_assignments WHERE container_id = '<container-id>';
```

4. **Ship states:**
```bash
spacetraders ship list --player-id <player-id>
```

5. **Market data:**
```bash
spacetraders market get --waypoint <manufacturing-waypoint>
```

---

## Next Steps After Integration Testing

Once integration tests pass:

1. **Gap Analysis Review:** Address critical gaps from GOODS_FACTORY_GAP_ANALYSIS.md
2. **Parallel Execution:** Implement multi-ship parallel workers (highest priority)
3. **BDD Step Definitions:** Automate integration tests with godog
4. **Production Deployment:** Deploy to live environment
5. **Monitoring:** Set up observability and alerting
6. **Performance Tuning:** Optimize based on real-world usage

---

**Document Version:** 1.0
**Last Updated:** 2025-11-22
**Status:** Ready for execution
