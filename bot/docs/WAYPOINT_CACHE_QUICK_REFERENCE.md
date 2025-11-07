# Waypoint Caching System - Quick Reference

## Three Cache Layers

```
┌─────────────────────────────────────────────────────────────────────┐
│                        WAYPOINT CACHING LAYERS                      │
└─────────────────────────────────────────────────────────────────────┘

┌──────────────────────────────┐
│   1. WAYPOINT TABLE          │
│   (waypoints)                │
├──────────────────────────────┤
│ - Individual waypoint rows   │
│ - 88 rows for X1-HZ85        │
│ - Traits + orbitals as JSON  │
│ - synced_at timestamp        │
│ - TTL: 2 hours (7200s)       │
│ - Population:                │
│   • Manual sync via CLI      │
│   • Lazy-load (if stale +    │
│     player_id)               │
├──────────────────────────────┤
│ Used by:                     │
│ • ListWaypointsQuery         │
│ • Contract operations        │
│ • Navigation validation ✗    │
└──────────────────────────────┘
         
         ↓ (should sync but doesn't)
         
┌──────────────────────────────┐
│   2. SYSTEM_GRAPHS TABLE     │
│   (system_graphs)            │
├──────────────────────────────┤
│ - Single graph per system    │
│ - 1 row for X1-HZ85          │
│ - JSON blob:                 │
│   {waypoints: {...},         │
│    edges: [...]}             │
│ - last_updated timestamp     │
│ - TTL: NONE (∞)              │
│ - Population:                │
│   • Auto via GraphBuilder    │
│   • When navigation needed   │
├──────────────────────────────┤
│ Used by:                     │
│ • Navigation routing         │
│ • Path planning              │
│ • Validation ✓               │
└──────────────────────────────┘

         ↓ (ephemeral)

┌──────────────────────────────┐
│   3. GRAPH WAYPOINTS (MEM)   │
│   (graph["waypoints"])       │
├──────────────────────────────┤
│ - Dict in NavigateShipCommand│
│ - Built from system_graphs   │
│ - Converted to Waypoint objs │
│ - TTL: 0 (immediate)         │
│ - Population:                │
│   • Auto via graph_provider  │
├──────────────────────────────┤
│ Used by:                     │
│ • Route validation           │
│ • Routing engine             │
└──────────────────────────────┘
```

## The Critical Mismatch

```
SCENARIO: Ship navigates to X1-HZ85

1. NavigateShipCommand
   ├─ graph_provider.get_graph(system)
   │  └─ GraphBuilder fetches 88 waypoints from API
   │     └─ Saves to system_graphs table ✓
   │     └─ Does NOT save to waypoints table ✗
   ├─ Convert graph to Waypoint objects
   └─ Navigation succeeds ✓

2. Later, user queries waypoints
   ├─ ListWaypointsQuery
   │  └─ waypoint_repository.find_by_system(system)
   │     ├─ Check waypoints table
   │     └─ Table is EMPTY! ✗
   │        (because navigation only populated system_graphs)
   └─ Return [] (empty list)

3. User sees: "No waypoints found. Please sync waypoints from API first."
   BUT: Waypoints ARE cached (just in the wrong table!)
```

## Data Flow Comparison

### WORKING PATH (Manual Sync)
```
CLI: spacetraders shipyard sync-waypoints X1-HZ85
  → SyncSystemWaypointsHandler
    → API.list_waypoints() → waypoint_repository.save_waypoints()
      → UPDATE/INSERT waypoints table ✓
```

### BROKEN PATH (Navigation)
```
CLI: spacetraders navigate --ship AGENT1-1 --to X1-HZ85-AB12
  → NavigateShipCommand
    → graph_provider.get_graph(system)
      → GraphBuilder.build_system_graph()
        → API.list_waypoints() → graph_provider._save_to_database()
          → UPDATE/UPDATE system_graphs table ✓
          → Does NOT call waypoint_repository.save_waypoints() ✗
```

### LAZY-LOAD PATH (Partial)
```
CLI: spacetraders waypoint list --system X1-HZ85 --agent AGENT1
  → ListWaypointsQuery(system, player_id)
    → waypoint_repository.find_by_system(system, player_id)
      ├─ Check is_cache_stale(system)
      │  └─ If stale AND player_id provided:
      │     └─ _fetch_and_cache_from_api(system, player_id)
      │        → API.list_waypoints() → save_waypoints()
      │          → UPDATE/INSERT waypoints table ✓
      └─ Return waypoints
```

## TTL Comparison

| Cache | TTL | Expires | Trigger |
|-------|-----|---------|---------|
| **waypoints** | 2 hrs | Age-based (synced_at) | Stale timestamp |
| **system_graphs** | ∞ | Never | force_refresh only |
| **Graph waypoints** | 0 | Immediate | End of command |

**Problem:** system_graphs never expires, so stale graph data can be served indefinitely.

## Lazy-Loading Requirements

For lazy-load to work, **ALL** must be true:

```
✓ Cache is stale (synced_at is NULL or > 2 hours old)
✓ player_id is provided
✓ api_client_factory is configured (it is in container.py:356)
```

**Why it fails in production:**
- Navigate command doesn't pass player_id to graph_provider
- Graph provider can't lazy-load (no API authentication context)
- GraphBuilder always fetches fresh from API (no lazy-load option)
- Waypoint table never gets populated during navigation

## File Dependency Map

```
NavigateShipCommand
├─ graph_provider.get_graph(system) ✓
│  └─ SystemGraphProvider
│     └─ GraphBuilder
│        └─ API.list_waypoints() ✓
│           └─ system_graphs table
└─ waypoint_repository NOT CALLED ✗

ListWaypointsQuery
├─ waypoint_repository.find_by_system(system, player_id) ✓
│  ├─ Check waypoints table
│  ├─ Lazy-load (if stale + player_id) ✓
│  └─ API.list_waypoints() ✓
│     └─ waypoints table
└─ graph_provider NOT CALLED ✗
```

## Key Code Locations

### Graph Building (NO waypoint sync)
- **File:** `src/adapters/secondary/routing/graph_builder.py:23-141`
- **Issue:** Fetches from API, builds graph, saves to system_graphs, never updates waypoints table

### Graph Provider (NO waypoint repository integration)
- **File:** `src/adapters/secondary/routing/graph_provider.py:28-123`
- **Issue:** Only caches in system_graphs, doesn't know about waypoint table

### Waypoint Repository (lazy-load implementation)
- **File:** `src/adapters/secondary/persistence/waypoint_repository.py:87-156`
- **Status:** Correctly implements lazy-load, but only used by ListWaypointsQuery

### Navigate Command (uses graph, not waypoint table)
- **File:** `src/application/navigation/commands/navigate_ship.py:85-194`
- **Issue:** Gets waypoints from graph_provider, validates from graph, never updates waypoint table

### Manual Sync (ONLY way to populate waypoints table)
- **File:** `src/application/shipyard/commands/sync_waypoints.py:48-125`
- **Status:** Correctly updates waypoints table, but requires manual invocation

## Fix Options

### Option A: Unify Caches (Recommended)
Make GraphBuilder populate BOTH tables when building graph:
```python
# In graph_builder.py
waypoint_entities = convert_to_waypoints(api_response)
waypoint_repository.save_waypoints(waypoint_entities)  # ← ADD THIS
graph = build_graph_structure(waypoint_entities)
```

### Option B: Route Through Waypoint Repository
Pass waypoint_repository to GraphBuilder, let it decide where to cache:
```python
# In navigate_ship.py
waypoint_repo = get_waypoint_repository()
graph_result = graph_provider.get_graph_with_sync(system, waypoint_repo)
```

### Option C: Add Graph TTL
Add expiration check to system_graphs cache:
```python
# In graph_provider.py
if age > ttl_seconds:
    rebuild_from_api()
```

## Testing Implications

- **Unit tests:** Pass (use mocks, don't test cache sync)
- **Integration tests:** Pass (both caches populated in test setup)
- **Production:** Fails (only system_graphs populated by navigation)
- **AFK mode:** Fails (daemon uses graph cache, CLI queries waypoint cache)

This is why the bug doesn't show up in tests!

## Production Symptoms

1. User runs: `spacetraders waypoint list --system X1-HZ85`
   - Result: "No waypoints found"
   - Reason: Navigation populated system_graphs, not waypoints

2. User runs navigation successfully, then waypoint query fails
   - Reason: Different cache tables used

3. AFK mode container runs fine, but later queries fail
   - Reason: Graph cache never expires, waypoint cache becomes stale

4. Manual sync fixes it temporarily
   - Reason: Manual sync correctly populates waypoints table

## Summary

The system has **three separate waypoint caches** that are never synchronized:
1. **waypoints table** - Populated by manual sync + lazy-load
2. **system_graphs table** - Populated by graph building (navigation)
3. **Graph waypoints dict** - Ephemeral (discarded after command)

Navigation uses #2, queries use #1, validation uses #3. When #2 is populated but #1 is empty, users see "cache empty" errors even though waypoints ARE cached (just in the wrong place).

