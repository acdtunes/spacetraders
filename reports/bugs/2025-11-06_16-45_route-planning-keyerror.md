# Bug Report: Route Planning KeyError on Empty Waypoint Cache

**Date:** 2025-11-06 16:45:00
**Severity:** CRITICAL
**Status:** NEW
**Reporter:** Captain

## Summary
The `plan_route` query crashes with KeyError when attempting to plan routes to waypoints not in the local cache. This occurs because the graph provider returns an empty waypoints dictionary, and the route planner tries to access the 'symbol' key on empty data dictionaries without proper validation.

## Impact
- **Operations Affected:** All navigation operations, contract execution, mining operations, scouting
- **Credits Lost:** Unable to quantify - all autonomous operations blocked
- **Duration:** Blocks operations from agent initialization until waypoint cache is manually populated
- **Workaround:** None identified - requires manual waypoint synchronization via bot CLI (if such command exists) or new MCP tool implementation

## Steps to Reproduce
1. Start with fresh agent ENDURANCE (player_id: 1) with empty waypoint cache
2. Ship ENDURANCE-1 located at X1-HZ85-A1 (HQ)
3. Attempt to plan route to any waypoint besides current location:
   ```
   mcp__spacetraders-bot__plan_route
     ship: ENDURANCE-1
     destination: X1-HZ85-B1
   ```
4. Observe KeyError crash

## Expected Behavior
When planning route to waypoint not in cache, the system should:
1. Detect cache miss gracefully
2. Fetch waypoint data from SpaceTraders API via graph builder
3. Cache the fetched data for future use
4. Proceed with route planning

OR alternatively:
- Return descriptive error: "Waypoint not found in cache, please sync waypoints first"
- Handle empty data dict without crashing

## Actual Behavior
The route planner crashes with:
```
Failed executing PlanRouteQuery: 'symbol'
Traceback (most recent call last):
  File ".../bot/src/application/navigation/queries/plan_route.py", line 89, in handle
    symbol=data["symbol"],
           ~~~~^^^^^^^^^^
KeyError: 'symbol'
```

## Evidence

### Ship State
```
ENDURANCE-1
================================================================================
Location:       X1-HZ85-A1
System:         X1-HZ85
Status:         DOCKED

Fuel:           392/400 (98%)
Cargo:          0/40
Engine Speed:   36
```

### Error Message
```
mcp__spacetraders-bot__plan_route result:
Failed executing PlanRouteQuery: 'symbol'

Traceback:
  File ".../bot/src/application/navigation/queries/plan_route.py", line 89, in handle
    symbol=data["symbol"],
           ~~~~^^^^^^^^^^
KeyError: 'symbol'
```

### Code Analysis - plan_route.py (lines 80-97)

```python
# 4. Get system graph
system_symbol = current_location.system_symbol
graph_result = graph_provider.get_graph(system_symbol, force_refresh=False)
waypoints_dict = graph_result.graph.get("waypoints", {})

# Convert dict to Waypoint objects
graph: Dict[str, Waypoint] = {}
for symbol, data in waypoints_dict.items():
    graph[symbol] = Waypoint(
        symbol=data["symbol"],  # LINE 89 - CRASHES HERE
        x=data["x"],
        y=data["y"],
        system_symbol=data.get("system_symbol"),
        waypoint_type=data.get("type"),
        traits=tuple(data.get("traits", [])),
        has_fuel=data.get("has_fuel", False),
        orbitals=tuple(data.get("orbitals", []))
    )
```

### Code Analysis - graph_builder.py (lines 79-90)

The graph builder properly structures waypoint data:
```python
graph["waypoints"][waypoint["symbol"]] = {
    "type": waypoint.get("type"),
    "x": waypoint.get("x"),
    "y": waypoint.get("y"),
    "traits": traits,
    "has_fuel": has_fuel,
    "orbitals": [o["symbol"] for o in waypoint.get("orbitals", [])],
}
```

**Note:** The graph builder does NOT include "symbol" in the waypoint data dict - it uses the symbol as the KEY only. This is the mismatch causing the KeyError.

### Code Analysis - graph_provider.py (lines 58-80)

```python
def _load_from_database(self, system_symbol: str) -> Optional[dict]:
    """Load graph from database cache"""
    try:
        with self.db.connection() as conn:
            cursor = conn.cursor()
            cursor.execute(
                "SELECT graph_data FROM system_graphs WHERE system_symbol = ?",
                (system_symbol,),
            )
            row = cursor.fetchone()

            if row:
                graph_json = row[0]
                graph = json.loads(graph_json)
                logger.debug(f"Cache hit for {system_symbol}")
                return graph
            else:
                logger.debug(f"Cache miss for {system_symbol}")
                return None  # Returns None when cache empty
```

When database cache is empty, `_load_from_database` returns None, but the system doesn't trigger API fetch because `force_refresh=False`.

## Root Cause Analysis

**Primary Issue:** Data structure mismatch between graph builder and route planner

1. **Graph Builder** (graph_builder.py line 83-90):
   - Uses waypoint symbol as dictionary KEY
   - Does NOT store "symbol" as a value in the waypoint data dict
   - Structure: `{"X1-HZ85-A1": {"type": "...", "x": 0, "y": 0}, ...}`

2. **Route Planner** (plan_route.py line 89):
   - Expects "symbol" to exist as a KEY in the waypoint data dict
   - Crashes with KeyError when trying to access `data["symbol"]`

**Secondary Issue:** Empty cache handling

3. **Graph Provider** (graph_provider.py):
   - When cache is empty AND `force_refresh=False`, returns empty graph structure
   - Should either force API fetch on cache miss OR return clear error

**Tertiary Issue:** Bootstrap paradox

4. No MCP tool exists to sync waypoints from API to cache before first navigation attempt

## Potential Fixes

### Fix 1: Add "symbol" to graph builder data (RECOMMENDED)
**Location:** `adapters/secondary/routing/graph_builder.py` line 83-90

**Change:**
```python
graph["waypoints"][waypoint["symbol"]] = {
    "symbol": waypoint["symbol"],  # ADD THIS LINE
    "type": waypoint.get("type"),
    "x": waypoint.get("x"),
    "y": waypoint.get("y"),
    "system_symbol": system_symbol,  # Also add this for completeness
    "traits": traits,
    "has_fuel": has_fuel,
    "orbitals": [o["symbol"] for o in waypoint.get("orbitals", [])],
}
```

**Rationale:**
- Minimal change with no breaking effects
- Makes data structure self-contained
- Matches expectations in route planner

### Fix 2: Use dictionary key as symbol in route planner
**Location:** `application/navigation/queries/plan_route.py` line 87-97

**Change:**
```python
for symbol, data in waypoints_dict.items():
    graph[symbol] = Waypoint(
        symbol=symbol,  # Use the KEY instead of data["symbol"]
        x=data["x"],
        y=data["y"],
        system_symbol=data.get("system_symbol", system_symbol),  # Fallback to current system
        waypoint_type=data.get("type"),
        traits=tuple(data.get("traits", [])),
        has_fuel=data.get("has_fuel", False),
        orbitals=tuple(data.get("orbitals", []))
    )
```

**Rationale:**
- Also minimal change
- Trusts the dictionary key as source of truth
- Less duplication in data structure

**Tradeoff:** Fix 1 is preferred because it makes the data structure more robust and self-documenting.

### Fix 3: Force API fetch on empty cache
**Location:** `adapters/secondary/routing/graph_provider.py` line 40-48

**Change:**
```python
if not force_refresh:
    graph = self._load_from_database(system_symbol)
    if graph is not None:
        # Also check if graph has waypoints
        if graph.get("waypoints"):
            logger.info(f"Loaded graph for {system_symbol} from database cache")
            return GraphLoadResult(
                graph=graph,
                source="database",
                message=f"Loaded graph for {system_symbol} from database cache",
            )
        else:
            logger.warning(f"Graph for {system_symbol} in cache but has no waypoints, fetching from API")
```

**Rationale:**
- Prevents empty cache from blocking operations
- Auto-populates cache on first use
- May cause unexpected API calls

### Fix 4: Create MCP tool to sync waypoints
**Location:** New tool in `bot/mcp/src/botToolDefinitions.ts`

**Implementation:** Expose existing `SyncSystemWaypointsCommand` via MCP

**Rationale:**
- Allows Captain to manually trigger waypoint sync
- Gives control over when API calls happen
- Complements other fixes

## Recommended Solution

**Implement ALL fixes in order:**

1. **Fix 1 (IMMEDIATE):** Add "symbol" to graph builder data - prevents crash
2. **Fix 3 (IMMEDIATE):** Auto-fetch on empty cache - prevents bootstrap paradox
3. **Fix 4 (SOON):** Create MCP tool for manual sync - gives operational control
4. **Fix 2 (OPTIONAL):** Can skip if Fix 1 implemented, but consider for robustness

## Environment
- Agent: ENDURANCE
- Player ID: 1
- System: X1-HZ85
- Ships Involved: ENDURANCE-1
- Ship Location: X1-HZ85-A1 (HQ)
- MCP Tools Used: plan_route, ship_info
- Database Cache: Empty (fresh agent initialization)

## Related Issues
- Waypoint cache empty after agent registration
- waypoint_list MCP tool returns "No waypoints found"
- No automatic waypoint synchronization on agent bootstrap
- Contract operations blocked (need to navigate to delivery locations)
- Mining operations blocked (need to navigate to asteroid fields)

## Notes for Implementation
- The graph builder is already paginating through API results correctly
- Database schema exists for system_graphs table (database.py line 121-127)
- SyncSystemWaypointsCommand already exists in application layer
- Graph structure includes both waypoints and edges properly
- After fix, need to test with empty cache to verify auto-fetch works
