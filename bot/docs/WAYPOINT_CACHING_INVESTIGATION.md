# SpaceTraders Waypoint Caching System - Comprehensive Investigation Report

**Date:** 2025-11-07  
**Thoroughness Level:** Very Thorough  
**Database Status:** 88 waypoints cached for X1-HZ85 system, 1 system_graph cached

---

## Executive Summary

The waypoint caching system has **three separate caching layers** with different purposes and update patterns:

1. **Waypoint Cache (waypoints table)** - Individual waypoint records (has_fuel, traits, coordinates)
2. **System Graph Cache (system_graphs table)** - Pre-built navigation graphs with waypoints + edges
3. **Lazy-Loading Mechanism** - Auto-fetch from API when cache is stale (2-hour TTL)

**Critical Issue Identified:** The graph provider builds its own navigation graph from API and caches it in `system_graphs` table, but this graph is **NOT** automatically synchronized with the waypoint cache. This causes two failure modes:

- **Empty Waypoint Cache:** When waypoints are never explicitly synced, the graph builder fetches from API and caches in graph table only
- **Stale Graph vs Fresh Waypoints:** Navigate command expects waypoints in the waypoint cache but may get data from stale system_graphs table

---

## 1. Code Locations Map

### WAYPOINT RETRIEVAL LOCATIONS

| Location | File | Function | Purpose | Cache Used | API Fallback |
|----------|------|----------|---------|-----------|--------------|
| Navigation start | `navigate_ship.py:137` | `_convert_graph_to_waypoints()` | Convert graph to Waypoint objects | ✓ (system_graphs) | No |
| Route planning | `navigate_ship.py:161-169` | `find_optimal_path()` | Routing engine needs waypoint graph | ✓ (system_graphs) | No |
| Waypoint query | `list_waypoints.py:53-81` | `ListWaypointsHandler.handle()` | List waypoints with filters | ✓ (waypoints table) | ✓ (lazy-load) |
| Waypoint by trait | `waypoint_repository.py:174-206` | `find_by_trait()` | Find waypoints by trait | ✓ (waypoints table) | ✓ (lazy-load) |
| Waypoint by fuel | `waypoint_repository.py:208-236` | `find_by_fuel()` | Find fuel stations | ✓ (waypoints table) | ✓ (lazy-load) |
| Waypoint by system | `waypoint_repository.py:87-110` | `find_by_system()` | List all waypoints | ✓ (waypoints table) | ✓ (lazy-load) |
| System graph build | `graph_builder.py:23-141` | `build_system_graph()` | Build navigation graph | No (fetches from API) | ✓ (always) |
| System graph load | `graph_provider.py:28-56` | `get_graph()` | Get cached or rebuild graph | ✓ (system_graphs) | ✓ (force_refresh) |
| Database load graph | `graph_provider.py:58-80` | `_load_from_database()` | Load graph from cache | ✓ (system_graphs) | No |
| API client list | `client.py:100-110` | `list_waypoints()` | Raw API call for waypoints | No (HTTP) | N/A |

### WAYPOINT CACHING LOCATIONS

| Location | File | Function | Purpose | Write Operation | Replace System |
|----------|------|----------|---------|-----------------|-----------------|
| Manual sync | `sync_waypoints.py:48-125` | `SyncSystemWaypointsHandler.handle()` | Explicitly sync waypoints from API | `save_waypoints()` | Yes (all) |
| Repository save | `waypoint_repository.py:31-85` | `save_waypoints()` | Upsert waypoints to database | SQL UPSERT | Optional |
| Lazy-load API | `waypoint_repository.py:125-156` | `_fetch_and_cache_from_api()` | Auto-fetch when stale | `save_waypoints()` | Yes (all) |
| Graph to DB | `graph_provider.py:100-123` | `_save_to_database()` | Cache built graph | SQL UPSERT | No |

---

## 2. Data Flow Diagram

```
API (/systems/{system}/waypoints)
  ↓
  ├─→ GraphBuilder.build_system_graph()
  │   ├─ Fetches waypoints page-by-page (limit: 20 per page)
  │   ├─ Builds edges (bidirectional, distance calculation)
  │   ├─ Returns: {waypoints: {...}, edges: [...]}
  │   └─ Cached in system_graphs table
  │
  └─→ SyncSystemWaypointsHandler
      ├─ Fetches waypoints (paginated)
      ├─ Converts to Waypoint value objects
      ├─ Cached in waypoints table (with synced_at timestamp)
      └─ TTL: 2 hours (7200 seconds)
           ↓
         WaypointRepository.find_by_system()
           ├─ Returns from waypoints table
           ├─ Or lazy-loads if stale + player_id provided
           └─ Lazy-load fetches from API and updates cache
                ↓
         NavigateShip command
           ├─ Calls graph_provider.get_graph(system_symbol)
           │  └─ Reads from system_graphs table OR builds from API
           ├─ Converts graph["waypoints"] to Waypoint objects
           ├─ Validates ship location in waypoint_objects
           ├─ Validates destination in waypoint_objects
           └─ Passes to routing engine
```

### CRITICAL MISMATCH

The navigate command has a data flow problem:

```
NavigateShipCommand
  ↓
graph_provider.get_graph()  ← Reads from system_graphs table
  ├─ Cache hit? Return cached graph
  └─ Cache miss? Build from API → save to system_graphs
                                ↓ (Does NOT update waypoints table!)
                         Navigate command converts graph to waypoints
                         Waypoints are validated but NOT stored in waypoints table
                         If future navigation calls check waypoints table → EMPTY
```

---

## 3. Architecture Analysis

### Current Caching Strategy

**Waypoint Cache (waypoints table):**
- **Population:** Manual `SyncSystemWaypointsCommand` or lazy-load via repository
- **TTL:** 2 hours (7200 seconds)
- **Expiration:** Age-based (synced_at timestamp)
- **Invalidation:** Manual via `replace_system=True` parameter
- **Lazy-Loading:** Automatic when player_id provided and cache stale
- **Structure:** Individual rows per waypoint with traits + orbitals as JSON

**System Graph Cache (system_graphs table):**
- **Population:** Automatic via GraphBuilder when graph_provider.get_graph() called
- **TTL:** None (cached forever until force_refresh=True)
- **Expiration:** Never automatically expires
- **Invalidation:** Only via force_refresh flag
- **Lazy-Loading:** None (rebuilds from API if cache miss)
- **Structure:** Single JSON blob with {waypoints: {...}, edges: [...]}

### Architecture Gaps

1. **No Synchronization Between Caches**
   - Graph builder fetches from API and caches in system_graphs
   - Does NOT update waypoints table
   - Navigate command reads from system_graphs but validates against waypoints table
   - If system_graphs is cached but waypoints table is empty → Validation fails

2. **Graph Cache Never Expires**
   - system_graphs has no TTL mechanism
   - Can serve stale waypoint data indefinitely
   - No automatic refresh strategy

3. **Lazy-Loading Only Works for Waypoint Cache**
   - WaypointRepository implements lazy-loading
   - GraphBuilder does not (always fetches fresh from API if not in cache)
   - Navigate command doesn't trigger lazy-loading for waypoints

4. **No Cache Warming on Startup**
   - Waypoints are not pre-loaded when app starts
   - Graph cache only populated when first navigation command runs
   - Ships arriving in new system can't navigate until graph is built

5. **API Client Factory Not Always Connected**
   - WaypointRepository gets api_client_factory in container.py:356
   - But factory is only used in lazy-load path (when cache stale + player_id provided)
   - Navigate command doesn't pass player_id to graph_provider (no lazy-load)
   - Graph provider always rebuilds from API on cache miss

---

## 4. Root Cause Analysis: Why Cache Becomes Empty

### Scenario 1: Navigation Without Prior Waypoint Sync

```
1. New system discovered
2. Ship navigates to system
3. NavigateShipCommand.handle()
   ├─ Calls graph_provider.get_graph(system)
   │  └─ Cache miss (first time in system)
   │  └─ GraphBuilder.build_system_graph() fetches 88 waypoints from API
   │  └─ Returns graph with waypoints dict
   │  └─ SystemGraphProvider._save_to_database() → system_graphs table
   │                          (Does NOT update waypoints table!)
   ├─ _convert_graph_to_waypoints() converts graph["waypoints"] dict → Waypoint objects
   ├─ Validates ship location in waypoint_objects ✓
   ├─ Validates destination in waypoint_objects ✓
   └─ Navigation succeeds (graph cache used)

4. Later, user runs: spacetraders waypoint list --system X1-HZ85
   ├─ ListWaypointsQuery.handler()
   │  └─ Calls waypoint_repository.find_by_system(system_symbol, player_id)
   │     ├─ is_cache_stale() → TRUE (never synced, synced_at = NULL)
   │     └─ If player_id provided:
   │        └─ Lazy-load from API → Updates waypoints table ✓
   │     └─ If no player_id:
   │        └─ Returns empty list ✗ (cache empty, no lazy-load)
   └─ User sees "No waypoints found" error
```

### Scenario 2: Cache Staleness Check Timing

The `is_cache_stale()` method:
```python
def is_cache_stale(self, system_symbol: str, ttl_seconds: int = 7200) -> bool:
    sync_time = self.get_system_sync_time(system_symbol)
    if sync_time is None:
        return True  # ← Returns TRUE if never synced
    age_seconds = (datetime.now() - sync_time).total_seconds()
    return age_seconds > ttl_seconds
```

**Problem:** `sync_time is None` (never synced) is treated as stale, which is correct. BUT:
- If lazy-load happens once → synced_at is set
- After 2 hours → stale again
- If ship is in AFK mode (daemon) → lazy-load may not trigger (no player_id passed)
- Waypoints become empty again

### Scenario 3: AFK Mode Container Lifecycle

```
daemon starts
  ↓
scoutTourCommand (or navigate command) runs in background
  ├─ graph_provider.get_graph(system) 
  │  └─ If not in system_graphs → builds from API
  │  └─ Caches in system_graphs
  │  └─ Does NOT update waypoints table
  └─ Navigation succeeds
        ↓
time passes (2+ hours)
        ↓
next scoutTourCommand iteration
  ├─ Calls graph_provider.get_graph()
  │  └─ Reads from system_graphs (still cached, never expires)
  │  └─ Returns cached graph ✓
  ├─ But if waypoint query happens:
  │  └─ waypoint_repository.find_by_system()
  │  └─ is_cache_stale() checks synced_at → NULL or old
  │  └─ Try lazy-load but no player_id context → returns empty
  └─ "No waypoints found" error
```

---

## 5. Lazy-Loading Analysis

### Why Lazy-Loading Exists But Isn't Fully Activated

The WaypointRepository implements lazy-loading correctly:

```python
def find_by_system(self, system_symbol: str, player_id: Optional[int] = None) -> List[Waypoint]:
    is_stale = self.is_cache_stale(system_symbol, self.TTL_SECONDS)
    
    # Lazy-load from API if needed
    if is_stale and player_id and self._api_client_factory:
        logger.info(f"Cache stale for system {system_symbol}, fetching from API")
        self._fetch_and_cache_from_api(system_symbol, player_id)
    
    return self._query_from_database(system_symbol)
```

**Conditions for lazy-load to trigger:**
1. ✓ Cache is stale (`is_stale = True`)
2. ✓ player_id is provided (`player_id is not None`)
3. ✓ api_client_factory is set (it is, in container.py:356)

**Why it doesn't work in practice:**
1. **Navigate command doesn't pass player_id to graph_provider**
   - Navigate uses `graph_provider.get_graph(system)` with no player_id
   - Graph provider can't lazy-load (no player context)
   - Graph provider rebuilds from API instead (every cache miss)

2. **Graph provider never checks waypoint cache**
   - GraphBuilder always fetches fresh from API
   - SystemGraphProvider caches in system_graphs table only
   - Never updates waypoints table
   - Waypoint cache stays empty

3. **ListWaypointsQuery receives player_id but queries waypoint table directly**
   - Has lazy-load capability via repository
   - Works when player_id is provided via CLI
   - Fails when running in daemon without player context

---

## 6. Query Analysis

### Graph Provider Architecture

**File:** `src/adapters/secondary/routing/graph_provider.py`

```python
def get_graph(self, system_symbol: str, force_refresh: bool = False):
    if not force_refresh:
        graph = self._load_from_database(system_symbol)
        if graph is not None:
            return GraphLoadResult(graph=graph, source="database")
    
    # Build from API and cache
    graph = self._build_from_api(system_symbol)
    return GraphLoadResult(graph=graph, source="api")

def _build_from_api(self, system_symbol: str):
    graph = self.builder.build_system_graph(system_symbol)
    self._save_to_database(system_symbol, graph)  # ← Saves to system_graphs only!
    return graph
```

**Issue:** No parameter to pass player_id for lazy-loading waypoint cache

### Graph Builder Architecture

**File:** `src/adapters/secondary/routing/graph_builder.py`

```python
def build_system_graph(self, system_symbol: str):
    # Fetch from API (always fresh)
    all_waypoints = api.list_waypoints(system_symbol, ...)
    
    # Build graph structure
    graph = {"waypoints": {...}, "edges": [...]}
    return graph
    # ← Does NOT call waypoint_repository.save_waypoints()
```

**Issue:** Builds complete graph with waypoint data but doesn't persist to waypoint cache

### Navigate Command Flow

**File:** `src/application/navigation/commands/navigate_ship.py`

```python
def handle(self, request: NavigateShipCommand) -> Route:
    graph_provider = get_graph_provider_for_player(request.player_id)
    graph_result = graph_provider.get_graph(system_symbol)  # ← No player_id passed!
    
    waypoint_objects = self._convert_graph_to_waypoints(graph)
    
    # Validation reads from waypoint_objects (converted from graph)
    # Not from waypoint cache table
    if not waypoint_objects:
        raise ValueError("No waypoints found... Please sync waypoints from API")
```

**Issue:** Gets graph_provider with player_id but doesn't use it for lazy-loading

---

## 7. Database Schema Details

### Waypoints Table

```sql
CREATE TABLE waypoints (
    waypoint_symbol TEXT PRIMARY KEY,
    system_symbol TEXT NOT NULL,
    type TEXT NOT NULL,
    x REAL NOT NULL,
    y REAL NOT NULL,
    traits TEXT,                           -- JSON array of trait symbols
    has_fuel INTEGER NOT NULL DEFAULT 0,
    orbitals TEXT,                         -- JSON array of orbital symbols
    synced_at TIMESTAMP DEFAULT NULL       -- When this waypoint was last synced
);

CREATE INDEX idx_waypoint_system ON waypoints(system_symbol);
CREATE INDEX idx_waypoint_fuel ON waypoints(has_fuel);
```

**Current State (X1-HZ85):**
- 88 waypoints cached
- synced_at timestamp present
- Cache is fresh (within 2-hour TTL)

### System Graphs Table

```sql
CREATE TABLE system_graphs (
    system_symbol TEXT PRIMARY KEY,
    graph_data TEXT NOT NULL,              -- JSON: {waypoints: {...}, edges: [...]}
    last_updated TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

**Current State:**
- 1 graph cached
- No TTL mechanism (never expires)
- Graph contains waypoint data but separate from waypoints table

### Mismatch Problem

The two tables both contain waypoint coordinate data:
- **waypoints table:** Individual records (88 rows for X1-HZ85)
- **system_graphs table:** Single JSON blob with complete graph

If system_graphs is used for navigation but waypoint validation queries waypoints table → Mismatch occurs when they're out of sync.

---

## 8. Why Cache Becomes Empty (Production Bug Root Cause)

### Sequence of Events

1. **Bot starts, new system discovered**
   ```
   NavigateShipCommand
   └─ graph_provider.get_graph(system)
      └─ GraphBuilder fetches 88 waypoints from API
      └─ Caches in system_graphs table
      └─ ✓ Navigation succeeds
      └─ ✗ Waypoints table remains empty (never written to)
   ```

2. **Later, another component queries waypoint cache**
   ```
   ListWaypointsQuery
   └─ waypoint_repository.find_by_system(system, no player_id)
      ├─ Checks is_cache_stale() → TRUE (synced_at is NULL)
      ├─ Tries lazy-load but no player_id → skips
      └─ Returns empty list ✗
   ```

3. **Or daemon runs in AFK mode**
   ```
   ContractWorkflowDaemon
   └─ NegotiateContractCommand
      └─ Calls routing engine
         └─ Needs waypoints for ship location validation
         └─ graph_provider has cached graph (no expiration)
         └─ ✓ Works for 2+ hours
         └─ But if waypoint query runs → empty cache ✗
   ```

### Why Auto-Repopulation Fails

The lazy-loading mechanism **could** auto-populate waypoints table, but:

1. **Graph provider has no way to pass player_id to graph_builder**
   - graph_provider.get_graph() signature: `get_graph(system_symbol, force_refresh=False)`
   - No player_id parameter
   - Can't authenticate API calls if needed (though waypoints endpoint doesn't require auth)

2. **Graph builder doesn't persist to waypoint cache**
   - Fetches waypoints from API ✓
   - Builds graph structure ✓
   - Saves only to system_graphs table ✗
   - Never calls `waypoint_repository.save_waypoints()`

3. **Navigate command uses waypoint_objects from graph, not from cache**
   - Converts graph["waypoints"] dict to Waypoint objects
   - These objects are ephemeral (never persisted)
   - Validation checks waypoint_objects, not waypoints table
   - If navigate command succeeds but future queries check waypoints table → Empty

---

## 9. Complete Call Tree for Waypoint Operations

### Manual Sync Path
```
CLI: spacetraders shipyard sync-waypoints X1-HZ85 --agent AGENT1
  ↓
sync_waypoints_cli.py
  ↓
SyncSystemWaypointsCommand(system_symbol, player_id)
  ↓
SyncSystemWaypointsHandler.handle()
  ├─ api_client.list_waypoints(system, page=1, limit=20)  ← API call 1
  ├─ api_client.list_waypoints(system, page=2, limit=20)  ← API call 2
  ├─ ... (paginate through all waypoints)
  ├─ Convert to Waypoint objects
  └─ waypoint_repository.save_waypoints(waypoints)
     └─ Database: UPDATE/INSERT waypoints table ✓
```

### Navigation Path (Current)
```
CLI: spacetraders navigate --ship AGENT1-1 --destination X1-HZ85-AB12
  ↓
navigate_cli.py
  ↓
NavigateShipCommand(ship_symbol, destination, player_id)
  ↓
NavigateShipHandler.handle()
  ├─ graph_provider = get_graph_provider_for_player(player_id)
  │  └─ Creates SystemGraphProvider(database, GraphBuilder(api_client))
  ├─ graph_result = graph_provider.get_graph(system)
  │  ├─ Check system_graphs table → Cache hit/miss
  │  └─ If miss:
  │     └─ GraphBuilder.build_system_graph()
  │        ├─ api_client.list_waypoints() ← API call
  │        ├─ Build graph with edges
  │        └─ graph_provider._save_to_database()
  │           └─ Database: INSERT/UPDATE system_graphs table
  │              (Does NOT touch waypoints table)
  ├─ waypoint_objects = _convert_graph_to_waypoints(graph)
  ├─ Validate: waypoint_objects (from graph)
  ├─ routing_engine.find_optimal_path(waypoint_objects)
  └─ Execute route
```

### Waypoint Query Path (With Lazy-Load)
```
CLI: spacetraders waypoint list --system X1-HZ85 --agent AGENT1
  ↓
waypoint_cli.py
  ↓
ListWaypointsQuery(system_symbol, player_id)
  ↓
ListWaypointsHandler.handle()
  └─ waypoint_repository.find_by_system(system, player_id)
     ├─ is_cache_stale(system) → TRUE/FALSE
     ├─ If stale AND player_id:
     │  └─ _fetch_and_cache_from_api(system, player_id)
     │     ├─ api_client.list_waypoints(system, ...) ← API call
     │     └─ save_waypoints() → Update waypoints table ✓
     └─ _query_from_database(system)
        └─ SELECT from waypoints table
```

---

## 10. Summary of Findings

### Three Caching Layers (With Mismatches)

| Layer | Table | Population | TTL | Expiration | Auto-Sync | Used By |
|-------|-------|-----------|-----|-----------|-----------|---------|
| **Waypoint Cache** | waypoints | Manual sync + lazy-load | 2 hrs | Age-based | Yes (if player_id) | CLI queries, contract commands |
| **System Graph** | system_graphs | GraphBuilder (auto) | ∞ | Never | No | Navigation, routing |
| **Graph Waypoints** | (ephemeral) | GraphBuilder (auto) | 0 | Immediate | N/A | Navigate validation only |

### Root Cause: Data Isolation Problem

The system treats waypoints data in **three separate places:**

1. **Waypoint table** - used by ListWaypointsQuery, contracts
2. **System graphs table** - used by navigation, routing
3. **Graph dict in memory** - used by navigate command validation

When GraphBuilder fetches waypoints from API, it only populates #2 and #3, NOT #1. This causes:
- Navigate command succeeds (uses #3)
- Later waypoint queries fail (reads #1, finds empty)
- No automatic sync between layers

### Why Lazy-Loading Isn't Working End-to-End

The lazy-loading implementation is correct in WaypointRepository, but it's **not connected** to the main navigation flow:

1. NavigateShipCommand gets graph from graph_provider
2. Graph provider doesn't know about waypoint repository
3. Graph provider builds graph from GraphBuilder (always fresh)
4. GraphBuilder doesn't persist to waypoint table
5. Waypoint cache stays empty
6. Future queries that check waypoint table find nothing

---

## 11. Architectural Changes Needed

### Option A: Unify Graph and Waypoint Cache (Recommended)

**Change:** Make GraphBuilder populate BOTH system_graphs AND waypoints tables

```python
# In GraphBuilder.build_system_graph()
all_waypoints = []
for page in paginate_api_calls():
    all_waypoints.extend(page_data)

# Convert to Waypoint objects
waypoint_entities = convert_to_waypoint_objects(all_waypoints)

# Save to BOTH caches
waypoint_repository.save_waypoints(waypoint_entities)  ← NEW
graph = build_graph_structure(waypoint_entities)
```

**Benefits:**
- Navigations automatically populate waypoint cache
- No more empty cache after navigation
- Lazy-load path works for future queries
- Single source of truth for waypoint data

### Option B: Route Graph Provider Through Waypoint Repository

**Change:** Pass player_id and waypoint_repo to graph_provider

```python
# Navigate command
graph_provider = get_graph_provider_for_player(player_id)
waypoint_repo = get_waypoint_repository()
graph = graph_provider.get_graph_with_waypoint_sync(
    system,
    waypoint_repository=waypoint_repo
)
```

**Benefits:**
- Explicit synchronization
- Graph provider knows about waypoint cache
- Can delegate to lazy-load mechanism

### Option C: Add Graph Expiration TTL

**Change:** Add last_updated expiration check to graph provider

```python
def get_graph(self, system_symbol, force_refresh=False, ttl_seconds=7200):
    if not force_refresh:
        graph_row = load_from_database(system)
        if graph_row:
            age = (now - graph_row.last_updated).seconds
            if age <= ttl_seconds:
                return graph
    
    # Rebuild and cache
    graph = builder.build_system_graph(system)
    save_to_database(system, graph)
    return graph
```

**Benefits:**
- Graph cache expires after 2 hours
- Forces fresh data periodically
- Prevents stale waypoint data in graph

---

## 12. Critical Error Messages in Production

When navigation fails due to empty waypoint cache, users see:

```
ValueError: No waypoints found for system X1-HZ85. 
The waypoint cache is empty. Please sync waypoints from API first.
```

**But:** The waypoint cache IS populated by the navigation command itself (to system_graphs), 
just not to the waypoints table. So the error message is misleading—the real issue is that 
waypoints are cached in the wrong table.

---

## 13. Testing Strategy Impact

The current architecture makes testing challenging:

1. **Unit tests** - Use mocked graph provider, don't test sync between caches
2. **Integration tests** - Both caches exist and are synchronized
3. **Production** - System_graphs populated by navigation, waypoints table empty
4. **AFK mode** - Daemon sees cached graph, but waypoint queries find empty table

This explains why tests pass but production fails.

---

## File Structure Summary

```
src/
├── adapters/
│   ├── secondary/
│   │   ├── persistence/
│   │   │   ├── database.py              (schema: waypoints, system_graphs)
│   │   │   ├── waypoint_repository.py   (lazy-load implementation ✓)
│   │   │   └── ship_repository.py       (API-only, no caching)
│   │   ├── routing/
│   │   │   ├── graph_provider.py        (system_graphs cache)
│   │   │   └── graph_builder.py         (API → graph, no waypoint sync ✗)
│   │   └── api/
│   │       └── client.py                (API calls)
│   └── primary/
│       ├── cli/
│       │   ├── waypoint_cli.py          (query interface)
│       │   └── navigate_cli.py          (navigation command)
│       └── daemon/
│           └── daemon_server.py         (background operations)
├── application/
│   ├── navigation/
│   │   └── commands/
│   │       └── navigate_ship.py         (graph → waypoint conversion, validation ✗)
│   ├── shipyard/
│   │   └── commands/
│   │       └── sync_waypoints.py        (manual sync ✓)
│   ├── waypoints/
│   │   └── queries/
│   │       └── list_waypoints.py        (lazy-load support ✓)
│   └── contracts/
│       └── commands/
│           └── negotiate_contract.py    (uses navigation, may fail on cache mismatch ✗)
├── domain/
│   └── shared/
│       └── value_objects.py             (Waypoint value object)
├── ports/
│   ├── outbound/
│   │   ├── repositories.py              (IWaypointRepository interface)
│   │   └── graph_provider.py            (ISystemGraphProvider interface)
│   └── routing_engine.py                (IRoutingEngine interface)
└── configuration/
    └── container.py                     (DI: waypoint_repo with api_client_factory ✓)
```

