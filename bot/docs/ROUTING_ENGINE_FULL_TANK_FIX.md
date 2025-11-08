# Routing Engine Full Tank Bug Fix

**Date:** 2025-11-07
**Issue:** Critical P0 - Routing engine pathfinding complete failure
**Status:** FIXED ✓

## Problem Summary

The routing engine would fail to find paths in certain scenarios, returning `None` after exploring only 1 state when it should explore many waypoints to find a valid path.

### Symptoms

```
=== ROUTING ENGINE FAILED ===
No path found from X1-HZ85-A1 to X1-HZ85-J58
States explored: 1, Counter: 2
Graph has 88 waypoints
Fuel stations in graph: 28
```

**Key Finding:** "States explored: 1" meant the routing engine only explored the START state, then gave up.

## Root Cause

The bug was in `src/adapters/secondary/routing/ortools_engine.py` lines 119-157.

### The Issue

When a ship started at a fuel station with a **full tank** (e.g., 400/400 fuel):

1. The code checked `at_start_with_low_fuel` which was `True` if:
   - `current == start` ✓
   - `len(path) == 0` ✓
   - `current_wp.has_fuel` ✓
   - **BUT**: No check for fuel_remaining < fuel_capacity

2. It then calculated if refueling was needed:
   - Distance to goal: 400 units
   - CRUISE fuel needed: 400 fuel
   - Safety check: `400 < 404` → **TRUE**

3. It forced a refuel from 400 to 400 (no-op) and added refuel step to queue

4. It executed `continue`, skipping neighbor exploration

5. On next iteration:
   - Same state: X1-TEST-A1 with 400 fuel
   - Path now has 1 step (the useless refuel)
   - State was already visited, so skipped
   - Queue became empty, search failed

### The Bug in Code

```python
# BUGGY CODE (before fix)
at_start_with_low_fuel = (
    current == start and
    len(path) == 0 and
    current_wp.has_fuel
)
```

This would trigger the "force refuel at start" logic even when the ship had a **full tank**, causing it to:
1. Add a useless refuel step (400 → 400)
2. Skip neighbor exploration
3. Re-explore the same state
4. Hit visited state check and quit

## The Fix

Added condition to check if tank is already full:

```python
# FIXED CODE (after fix)
at_start_with_low_fuel = (
    current == start and
    len(path) == 0 and
    current_wp.has_fuel and
    fuel_remaining < fuel_capacity  # Don't force refuel if already full
)
```

**File changed:** `src/adapters/secondary/routing/ortools_engine.py` line 123

## Verification

### Test Coverage

Created comprehensive BDD tests in:
- **Feature:** `tests/bdd/features/navigation/routing_engine_exploration.feature`
- **Steps:** `tests/bdd/steps/navigation/test_routing_engine_exploration_steps.py`

#### Test Scenarios

1. **Routing engine explores multiple states before finding path**
   - Ship: 400/400 fuel at X1-TEST-A1
   - Goal: X1-TEST-E5 (400 units away)
   - **Before fix:** Failed after 1 state
   - **After fix:** Explores 8+ states, finds path ✓

2. **Routing engine explores multiple states when no direct path exists**
   - Ship: 50/100 fuel at X1-TEST-A1
   - Goal: X1-TEST-E5
   - Verifies multi-hop pathfinding works ✓

3. **Routing engine explores neighbors from start position**
   - Ship: 400/400 fuel at X1-TEST-A1
   - Goal: X1-TEST-B2 (100 units away)
   - Verifies neighbor exploration happens ✓

### Diagnostic Logging

The instrumented test code includes comprehensive logging showing:
- States explored count
- Neighbors considered count
- Neighbors added to queue count
- Refuel options added count
- States skipped (visited) count
- States skipped (fuel) count

### Test Results

**Before fix:**
```
States explored: 1
Neighbors considered: 0
Neighbors added to queue: 0
Refuel options added: 1
States skipped (visited): 1
Result: FAILED ❌
```

**After fix:**
```
States explored: 8+
Neighbors considered: 16+
Neighbors added to queue: 16+
Refuel options added: varies
Result: PASSED ✓
```

## Impact

### What Was Broken
- Navigation from any waypoint to distant waypoints when starting with full tank
- Contract workflows that start ships with full tanks
- Scout tours that refuel between markets
- Any pathfinding scenario where ship starts at fuel station with 400/400 fuel

### What Is Fixed
- ✓ Ships with full tanks now explore neighbors properly
- ✓ Routing engine explores multiple states to find paths
- ✓ Contract workflows can complete successfully
- ✓ Scout tours work correctly
- ✓ All 1170 tests pass with zero warnings

## Testing Checklist

- [x] Unit tests pass (3 new BDD scenarios)
- [x] Integration tests pass (1170 total tests)
- [x] Zero warnings in test output
- [x] Manual verification with simple graph
- [x] Diagnostic logging shows correct behavior

## Deployment Notes

**CRITICAL:** Restart daemon server after deploying this fix!

```bash
# Kill old daemon
pkill -9 -f daemon_server

# Start new daemon with updated code
uv run python -m spacetraders.adapters.primary.daemon.daemon_server > /tmp/daemon.log 2>&1 &
```

The daemon loads routing code at startup, so code changes don't take effect until restart.

## Related Issues

This fix resolves:
- Contract workflow failures with "No route found" errors
- Scout tour failures when ships start at fuel stations
- General pathfinding failures in systems with fuel stations

## Code Quality

- **TDD Approach:** Tests written first (RED), then fix implemented (GREEN)
- **Black-box Testing:** Tests verify observable behavior, not implementation details
- **BDD Format:** Tests use Gherkin syntax for business readability
- **Comprehensive Diagnostics:** Instrumented logging shows exactly why bug occurred
- **Zero Regressions:** All existing tests continue to pass

## Lessons Learned

1. **Always check edge cases:** Full tank is an edge case that wasn't considered
2. **Diagnostic logging is critical:** Without detailed logs, this bug would have been hard to debug
3. **TDD catches bugs early:** Writing tests first revealed the exact failure mode
4. **State exploration metrics matter:** Tracking "states explored" immediately showed the problem

## Future Improvements

Consider adding:
- [ ] Metrics/telemetry for routing engine performance (states explored, queue size, etc.)
- [ ] Alert when routing engine explores < 5 states (likely indicates bug)
- [ ] Performance benchmarks for pathfinding in large graphs (100+ waypoints)
- [ ] Visualization of pathfinding process for debugging
