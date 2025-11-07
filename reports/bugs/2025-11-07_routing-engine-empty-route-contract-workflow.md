# Bug Report: Routing Engine Returns Empty Route in Contract Workflow

**Date:** 2025-11-07
**Severity:** HIGH
**Status:** NEW
**Reporter:** Captain

## Summary
The `contract_batch_workflow` MCP tool fails completely with routing errors. All 10/10 contract iterations failed with "No route found. The routing engine returned an empty route plan with no steps." Meanwhile, scout operations using the same routing engine work perfectly, indicating a divergence in how the two workflows interact with the routing engine.

## Impact
- **Operations Affected:** Contract workflow completely blocked, ENDURANCE-1 cannot execute any contracts
- **Credits Lost:** Estimated 20,000+ credits per hour (missed contract opportunities)
- **Duration:** Discovered at Check-in #1 (6 minutes into AFK session), likely ongoing
- **Workaround:** None available - manual contract execution would require human intervention

## Steps to Reproduce
1. Start AFK session with ship ENDURANCE-1 at X1-HZ85-J58 (DOCKED)
2. Ship has full cargo (40/40 units, unknown item)
3. Ship has 162/400 fuel (40%)
4. Execute MCP command: `contract_batch_workflow(ship="ENDURANCE-1", count=10)`
5. Observe all 10 iterations fail with routing error

## Expected Behavior
The routing engine should:
1. Find valid route from current location (X1-HZ85-J58) to destination
2. Return route plan with at least one TRAVEL step
3. Handle refueling stops automatically if needed
4. Navigate ship successfully to purchase/delivery waypoints

Based on scout operations working correctly, the routing engine IS capable of pathfinding in system X1-HZ85.

## Actual Behavior
The routing engine returns an empty route plan (no steps), causing NavigateShipCommand to raise ValueError:

```
ValueError: No route found. The routing engine returned an empty route plan with no steps.
This may indicate waypoints are missing from the cache or the destination is unreachable.
```

This occurs at line 296-299 in `navigate_ship.py`:

```python
if not steps:
    raise ValueError(
        f"No route found. The routing engine returned an empty route plan with no steps. "
        f"This may indicate waypoints are missing from the cache or the destination is unreachable."
    )
```

All 10 contract iterations fail with identical error, suggesting systematic issue rather than transient problem.

## Evidence

### Ship State (ENDURANCE-1)
```
Location:       X1-HZ85-J58
System:         Unknown
Status:         DOCKED

Fuel:           162/400 (40%)
Cargo:          40/40 (FULL - unknown item)
Engine Speed:   36

Waypoint Type:  ASTEROID_BASE
Traits:         HOLLOWED_INTERIOR, PIRATE_BASE, MARKETPLACE
```

**Critical Observation:** Ship has full cargo. This may be preventing navigation attempts or causing routing engine to fail.

### Scout Ship State (ENDURANCE-2, working correctly)
```
Location:       X1-HZ85-B7
System:         Unknown
Status:         IN_TRANSIT

Fuel:           0/0 (0%)
Cargo:          0/0
Engine Speed:   9

Waypoint Type:  ASTEROID_BASE
Traits:         HOLLOWED_INTERIOR, OUTPOST, MARKETPLACE
```

**Contrast:** Scout ships have zero fuel capacity (probe satellites), empty cargo, and are successfully navigating using the same routing engine.

### Error Message (from Captain's context)
```
Failed executing NavigateShipCommand: No route found. The routing engine returned an empty route plan with no steps. This may indicate waypoints are missing from the cache or the destination is unreachable.

ValueError: No route found. The routing engine returned an empty route plan with no steps. This may indicate waypoints are missing from the cache or the destination is unreachable.

Contract workflow failed: Iteration 1-10: All failed with same error
```

### Code Flow Analysis

**Contract Workflow → NavigateShipCommand Flow:**
1. `batch_contract_workflow.py:358` - NavigateShipCommand created for seller market
2. `navigate_ship.py:161` - Calls `routing_engine.find_optimal_path()`
3. `ortools_engine.py:50-76` - find_optimal_path executes
4. `navigate_ship.py:171-175` - Checks if route_plan is None
5. `navigate_ship.py:289-300` - Validates route_plan has steps (FAILS HERE)

**Scout Workflow → Routing Engine Flow:**
1. `scout_markets.py:141` - Calls `routing_engine.optimize_fleet_tour()`
2. Different routing method used (VRP solver vs Dijkstra pathfinding)
3. Successfully navigates to markets

### Key Code Differences

**NavigateShipCommand uses:**
```python
route_plan = self._routing_engine.find_optimal_path(
    graph=waypoint_objects,
    start=ship.current_location.symbol,
    goal=request.destination_symbol,
    current_fuel=ship.fuel.current,
    fuel_capacity=ship.fuel_capacity,
    engine_speed=ship.engine_speed,
    prefer_cruise=True
)
```

**Scout operations use:**
```python
assignments = routing_engine.optimize_fleet_tour(
    graph=graph,
    markets=request.markets,
    ship_locations=ship_locations,
    fuel_capacity=fuel_capacity,
    engine_speed=engine_speed
)
```

Different routing methods → different behavior.

## Root Cause Analysis

**Primary Hypothesis: Waypoint Cache Empty/Incomplete**

The error message explicitly states "waypoints are missing from the cache." Analysis of code flow:

1. **NavigateShipCommand** at line 132-144 validates waypoint cache:
   ```python
   # Validate waypoint cache has waypoints
   if not waypoint_objects:
       raise ValueError(f"No waypoints found for system {system_symbol}...")

   # Validate ship location exists
   if ship.current_location.symbol not in waypoint_objects:
       raise ValueError(f"Waypoint {ship.current_location.symbol} not found...")

   # Validate destination exists
   if request.destination_symbol not in waypoint_objects:
       raise ValueError(f"Waypoint {request.destination_symbol} not found...")
   ```

2. **If these checks pass** (no exception raised), then waypoint_objects contains valid data
3. **BUT** the routing engine still returns empty route plan
4. **This suggests**: Graph data structure issue, not waypoint absence

**Secondary Hypothesis: Full Cargo Blocking Navigation**

Ship has 40/40 cargo. Possible scenarios:
- Routing engine checks cargo space and returns None/empty if full
- Domain logic prevents navigation with full cargo
- Contract workflow doesn't jettison cargo before navigation attempts

**Evidence Against This:**
- No cargo checks in `find_optimal_path()` signature or implementation
- NavigateShipCommand doesn't validate cargo space
- Error occurs at routing engine level, not domain validation level

**Tertiary Hypothesis: Graph Provider Returns Incompatible Data Structure**

Scout workflow converts graph to Waypoint objects explicitly:
```python
# scout_markets.py:111-123
graph = {}
for wp_symbol, wp_data in graph_data['waypoints'].items():
    graph[wp_symbol] = Waypoint(
        symbol=wp_symbol,
        waypoint_type=wp_data.get('type', 'UNKNOWN'),
        x=wp_data.get('x', 0),
        y=wp_data.get('y', 0),
        system_symbol=request.system,
        traits=tuple(wp_data.get('traits', [])),
        has_fuel=wp_data.get('has_fuel', False)
    )
```

NavigateShipCommand uses `_convert_graph_to_waypoints()` (line 137):
```python
waypoint_objects = self._convert_graph_to_waypoints(graph)
```

**Potential Issue:** Graph conversion may produce empty dict or malformed Waypoint objects, causing routing engine to fail silently.

**Most Likely Root Cause:**
System "Unknown" in ship_info output suggests the graph provider is returning incomplete or malformed graph data. The system_symbol extraction (line 132) may be producing "Unknown" instead of "X1-HZ85", causing graph lookup to fail.

## Potential Fixes

### Fix 1: Add Diagnostic Logging to Routing Engine (HIGH PRIORITY)
**Rationale:** Routing engine currently fails silently (returns None). Need visibility into WHY it's failing.

**Implementation:**
1. Add logging to `ortools_engine.py:find_optimal_path()` at line 61-70:
   ```python
   if start not in graph or goal not in graph:
       logger.error(f"Routing failed: start={start} in graph: {start in graph}, goal={goal} in graph: {goal in graph}")
       logger.error(f"Available waypoints in graph: {list(graph.keys())}")
       return None
   ```

2. Add logging before returning empty route plan (line 293-300 validation):
   ```python
   if not steps:
       logger.error(f"Route plan validation failed. Route plan: {route_plan}")
       logger.error(f"Graph waypoints: {list(waypoint_objects.keys())}")
       logger.error(f"Start: {ship.current_location.symbol}, Goal: {request.destination_symbol}")
       raise ValueError(...)
   ```

**Tradeoffs:** Adds logging overhead but critical for debugging routing failures.

### Fix 2: Pre-validate Graph Data in NavigateShipCommand (MEDIUM PRIORITY)
**Rationale:** Ensure graph data is complete before calling routing engine.

**Implementation:**
1. After line 137 (`waypoint_objects = self._convert_graph_to_waypoints(graph)`), add validation:
   ```python
   # Validate graph conversion produced valid waypoints
   if not waypoint_objects:
       logger.error(f"Graph conversion failed. Graph structure: {graph}")
       raise ValueError(f"Failed to convert graph waypoints for system {system_symbol}")

   # Log graph state for debugging
   logger.info(f"Graph loaded for {system_symbol}: {len(waypoint_objects)} waypoints")
   logger.debug(f"Waypoints: {list(waypoint_objects.keys())}")
   ```

**Tradeoffs:** Adds validation overhead but prevents silent failures.

### Fix 3: Implement Waypoint Sync Tool (LONG-TERM)
**Rationale:** Waypoint cache may be empty/stale. Need MCP tool to force sync from API.

**Implementation:**
1. Create `waypoint_sync` MCP tool that calls `ListWaypointsQuery`
2. Pre-sync waypoints before contract workflow starts
3. Add cache validation to NavigateShipCommand

**Tradeoffs:** Requires new tool implementation but provides long-term reliability.

### Fix 4: Handle Full Cargo in Contract Workflow (DEFENSIVE)
**Rationale:** Full cargo (40/40) may be related. Contract workflow should jettison cargo before starting new contract.

**Implementation:**
1. In `batch_contract_workflow.py`, before negotiating contract:
   ```python
   # Check if ship has cargo from previous failed iteration
   ship = self._ship_repository.find_by_symbol(request.ship_symbol, request.player_id)
   if ship.cargo.current > 0:
       logger.warning(f"Ship has {ship.cargo.current} cargo before contract start, jettisoning")
       # Jettison all cargo
       for item in ship.cargo.inventory:
           await self._mediator.send_async(JettisonCargoCommand(...))
   ```

**Tradeoffs:** Defensive fix but may not address root cause.

## Recommended Fix Priority

1. **IMMEDIATE:** Fix 1 (diagnostic logging) - Needed to understand root cause
2. **SHORT-TERM:** Fix 2 (graph validation) - Prevents silent failures
3. **MEDIUM-TERM:** Fix 4 (cargo handling) - Defensive improvement
4. **LONG-TERM:** Fix 3 (waypoint sync tool) - Infrastructure improvement

## Environment
- Agent: ENDURANCE
- System: X1-HZ85
- Ships Involved: ENDURANCE-1 (failing), ENDURANCE-2/3/4 (working)
- MCP Tools Used: contract_batch_workflow, ship_info
- Container ID: Not applicable (direct MCP tool call, not daemon)
