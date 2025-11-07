# Routing Engine Graph Format Fix

**Date:** 2025-11-07
**Severity:** CRITICAL (P0) - Complete navigation failure
**Status:** FIXED

## Summary

Fixed critical bug where NavigateShipCommand passed a nested graph structure to the routing engine instead of a flat waypoint dictionary, causing 100% navigation failure across all systems.

## Root Cause

The routing engine interface expects `graph: Dict[str, Waypoint]` (a flat dictionary mapping waypoint symbols to Waypoint objects). However, NavigateShipCommand was passing a nested structure:

```python
# WRONG (was passing this):
enriched_graph = {
    "waypoints": waypoint_objects,  # Dict[str, Waypoint]
    "edges": graph.get("edges", [])  # List[Dict]
}
route_plan = self._routing_engine.find_optimal_path(
    graph=enriched_graph,  # Nested structure - WRONG!
    ...
)
```

This caused the routing engine to fail because it tries to iterate over the graph dictionary:

```python
# In ortools_engine.py, line 195
for neighbor_symbol, neighbor in graph.items():
```

When `graph` is a nested structure, this iteration gets:
- `("waypoints", Dict[str, Waypoint])`
- `("edges", List[Dict])`

Instead of actual waypoint objects, causing pathfinding to fail and return `None`.

## The Fix

Changed NavigateShipCommand to pass the flat waypoint dictionary directly:

```python
# CORRECT (now passing this):
route_plan = self._routing_engine.find_optimal_path(
    graph=waypoint_objects,  # Flat Dict[str, Waypoint] - CORRECT!
    start=ship.current_location.symbol,
    goal=request.destination_symbol,
    current_fuel=ship.fuel.current,
    fuel_capacity=ship.fuel_capacity,
    engine_speed=ship.engine_speed,
    prefer_cruise=True
)
```

The routing engine calculates distances between waypoints on-the-fly using their x, y coordinates. It does NOT need pre-computed edges from the graph structure.

## Why This Bug Occurred

The previous fix (commit 4934d51) successfully enriched waypoints with trait data from the waypoints table (fixing the has_fuel=False bug), but mistakenly created a nested graph structure thinking the routing engine needed both waypoints AND edges.

The routing engine interface clearly shows it only needs waypoints:

```python
def find_optimal_path(
    self,
    graph: Dict[str, Waypoint],  # NOT a nested structure!
    start: str,
    goal: str,
    ...
) -> Optional[Dict[str, Any]]:
```

## Test Coverage Added

Created integration tests to prevent regression:

### Test 1: Routing engine receives flat Dict[str, Waypoint]
- Verifies routing engine accepts flat dictionary
- Verifies routing engine rejects nested structure
- Confirms distance calculation works

### Test 2: Routing engine finds path with flat waypoint dictionary
- Creates test waypoints with coordinates and fuel stations
- Calls routing engine with flat dictionary
- Verifies pathfinding succeeds
- Confirms fuel costs are calculated

**Test files:**
- `tests/bdd/features/navigation/routing_engine_graph_format.feature`
- `tests/bdd/steps/navigation/test_routing_engine_graph_format_steps.py`

## Verification

### Test Results
- **New tests:** 2/2 passed
- **Navigation tests:** 118/118 passed
- **Full test suite:** 1167/1167 passed
- **Warnings:** 0
- **Failures:** 0

### Impact
- Fixes 100% navigation failure in all systems
- Restores contract workflow functionality
- Unblocks all revenue operations
- Resolves AFK session failures

## Files Changed

1. **src/application/navigation/commands/navigate_ship.py**
   - Line 196-204: Changed to pass `waypoint_objects` instead of `enriched_graph`
   - Removed unnecessary nested graph structure construction

2. **tests/bdd/features/navigation/routing_engine_graph_format.feature** (NEW)
   - Integration tests for routing engine graph format

3. **tests/bdd/steps/navigation/test_routing_engine_graph_format_steps.py** (NEW)
   - Step definitions for routing engine tests

## Key Insights

1. **Interface contracts matter:** The routing engine interface explicitly defines the expected input format. Passing a different format breaks the contract.

2. **The routing engine is self-sufficient:** It calculates distances dynamically from waypoint coordinates. It does NOT need pre-computed edges.

3. **Graph enrichment still works:** Waypoints are correctly enriched with has_fuel flags from the waypoints table. The enrichment logic is correct - only the format passed to routing engine was wrong.

4. **System architecture is sound:** The dual-cache strategy (system_graphs for structure, waypoints for traits) works correctly. The bug was just in how the enriched data was passed to the routing engine.

## Related Bug Reports

- **Previous:** `2025-11-07_19-00_routing-engine-graph-missing-fuel-data.md` (FIXED)
  - Root cause: Graph enrichment missing
  - Fix: Added waypoint trait enrichment
  - But introduced THIS bug by creating nested structure

- **This bug:** `2025-11-07_20-00_routing-engine-pathfinding-complete-failure.md` (FIXED)
  - Root cause: Nested graph structure instead of flat dictionary
  - Fix: Pass flat waypoint dictionary directly
  - Result: Navigation restored, all operations working

## Lessons Learned

1. **Always check interface contracts** when modifying code that crosses architectural boundaries
2. **Integration tests are critical** for catching format mismatches between components
3. **Graph structure assumptions** should be documented clearly in routing engine interface
4. **TDD catches bugs early** - these tests would have caught the bug immediately if written first
