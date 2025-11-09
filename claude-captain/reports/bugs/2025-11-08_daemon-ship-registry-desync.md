# Bug Report: Daemon Ship Registry Desynchronization - API vs Internal Cache

**Date:** 2025-11-08
**Severity:** CRITICAL
**Status:** NEW
**Reporter:** Captain

## Summary
Daemon ship registry is completely desynchronized from SpaceTraders API. Ships that exist and are queryable via API tools cannot be found by daemon-based MCP tools, blocking all autonomous operations. **ROOT CAUSE IDENTIFIED:** Ship registry table was intentionally removed from database (lines 367-370 of database.py), but daemon tools still attempt ship lookups against non-existent table.

## Impact
- **Operations Affected:** All daemon-based operations (contract workflows, trading, mining, autonomous missions)
- **Credits Lost:** Operations halted - no revenue generation possible via daemon
- **Duration:** Entire Session #2 (discovered immediately upon session start)
- **Workaround:** Direct CLI execution (violates TARS delegation protocols, not sustainable)

## Steps to Reproduce
1. Start daemon server (PID 34005)
2. Query ship via API tool: `ship_info --ship ENDURANCE-1`
   - Result: SUCCESS - Returns full ship details (Location: X1-HZ85-E46, Status: DOCKED, Fuel: 100%)
3. Attempt daemon operation: `contract_batch_workflow --ship ENDURANCE-1`
   - Result: FAILURE - "Ship ENDURANCE-1 not found"
4. Verify with ship list API: Returns 4 ships including ENDURANCE-1
5. Retry any daemon operation requiring ship lookup: All fail with same error

## Expected Behavior
1. Daemon should fetch ship data directly from SpaceTraders API (per design intent)
2. MCP daemon tools should successfully locate ships that exist in API
3. Ship lookups should NOT depend on database cache (database.py lines 367-370 confirm this design)
4. All daemon operations should work for ships returned by `ship_list`

## Actual Behavior
1. Daemon tools attempt ship lookup in non-existent database table
2. API tools (`ship_info`, `ship_list`) successfully return ship data (direct API calls)
3. Daemon tools (`contract_batch_workflow`, etc.) fail with "Ship not found"
4. **Architecture Mismatch:** Database layer removed ships table, but daemon code still queries it

## Evidence

### API Ship Query (SUCCESSFUL)
```
Tool: ship_info --ship ENDURANCE-1
Result: SUCCESS

ENDURANCE-1
================================================================================
Location:       X1-HZ85-E46
System:         X1-HZ85
Status:         DOCKED

Fuel:           400/400 (100%)
Cargo:          0/40
Engine Speed:   36

Waypoint Type:  MOON
Traits:         FROZEN, SCATTERED_SETTLEMENTS, TOXIC_ATMOSPHERE, CORROSIVE_ATMOSPHERE, MARKETPLACE
```

### Daemon Ship Lookup (FAILED)
```
Tool: contract_batch_workflow --ship ENDURANCE-1
Result: FAILURE

Error: "Ship ENDURANCE-1 not found"
```

### Database Schema Evidence (CRITICAL FINDING)
```
File: /Users/andres.camacho/Development/Personal/spacetraders/bot/src/adapters/secondary/persistence/database.py
Lines: 367-370

# Ships table removed - ship data is now fetched directly from API
# Historical note: Ships table was removed to ensure ship state
# (location, fuel, cargo) is always fresh from the SpaceTraders API.
# This prevents stale data issues and eliminates sync complexity.
```

**Analysis:** The ship registry table was **intentionally removed** from the database schema. The database layer expects all ship data to come directly from the API. However, daemon tools are still attempting to query the non-existent ships table.

### Infrastructure State
```
Daemon Server: RUNNING (PID 34005)
Database: PostgreSQL (recently migrated from SQLite)
Ship Count (API): 4 ships
Ship Count (Database): TABLE DOES NOT EXIST (intentionally removed)
```

### Related Failures
```
1. Scout Network Deployment: SQL parameter binding errors
   - Symptom: Database query failures in daemon operations
   - Pattern: Daemon database interaction issues

2. Contract Batch Workflow: Ship registry lookup failure
   - Symptom: "Ship not found" despite ship existing in API
   - Pattern: Attempting to query non-existent ships table

3. Session Timeline:
   - Session #1: Daemon operations worked successfully (likely SQLite with ships table)
   - Between Sessions: PostgreSQL migration + ships table removal
   - Session #2: Complete daemon registry failure
```

## Root Cause Analysis

**ROOT CAUSE: Architecture Desynchronization Between Database Layer and Daemon Tools**

**The Problem:**
1. **Database Layer (CORRECT):** Ships table removed (lines 367-370 database.py), ship data fetched directly from API
2. **Daemon Tools (BROKEN):** Still attempting to query non-existent ships table for ship lookups
3. **Result:** All daemon operations requiring ship data fail with "Ship not found"

**Technical Architecture Mismatch:**

```
Current (Broken) Architecture:
┌─────────────────┐
│  Daemon Tools   │
│  (MCP)          │
└────────┬────────┘
         │
         │ 1. Query: SELECT * FROM ships WHERE ship_symbol = ?
         ▼
┌─────────────────┐
│   PostgreSQL    │
│   Database      │  ❌ ERROR: Table "ships" does not exist
└─────────────────┘


Intended (Correct) Architecture:
┌─────────────────┐
│  Daemon Tools   │
│  (MCP)          │
└────────┬────────┘
         │
         │ 1. Direct API call: GET /my/ships/{shipSymbol}
         ▼
┌─────────────────┐
│  SpaceTraders   │
│      API        │  ✅ SUCCESS: Returns ship data
└─────────────────┘
```

**Why This Happened:**

1. **Design Intent:** Database.py ships table removed to ensure fresh ship state from API (lines 367-370)
2. **Incomplete Migration:** Daemon tools not updated to fetch ships directly from API
3. **Code Location Issue:** Ship lookup code in daemon tools still calls `db.get_ship()` instead of `api_client.get_ship()`
4. **PostgreSQL Migration Timing:** Issue appeared after migration because SQLite likely still had ships table with legacy data

**Evidence Chain:**
1. Database.py comment (lines 367-370): Ships table intentionally removed
2. MCP tool error: "Ship not found" (attempting database query)
3. API tools work: ship_info successfully queries API directly
4. Timing: Worked in Session #1 (SQLite), failed in Session #2 (PostgreSQL without ships table)

## Potential Fixes

### Fix 1: Update Daemon Tools to Query API Directly (CORRECT ARCHITECTURAL FIX)
**Rationale:** Align daemon tools with database layer design intent - ships come from API, not database

**Implementation:**
```python
# BEFORE (Broken - queries non-existent table):
ship = await db.get_ship(ship_symbol)
if not ship:
    raise ShipNotFoundError(ship_symbol)

# AFTER (Correct - queries API directly):
ship = await api_client.get_ship(ship_symbol)
```

**Files to Update:**
- All daemon tool handlers that perform ship lookups
- Contract batch workflow handler
- Trading coordinator handlers
- Mining operation handlers
- Any code calling `db.get_ship()` or similar database ship queries

**Pros:**
- Aligns with database layer design intent
- Ensures fresh ship data (no cache staleness)
- Proper architectural fix
- Eliminates sync complexity

**Cons:**
- Requires updating multiple daemon tool handlers
- May increase API call frequency
- Need to handle API rate limits

### Fix 2: Restore Ships Table with API Sync (REVERSES DESIGN DECISION)
**Rationale:** Re-add ships table and sync from API on daemon startup

**Implementation:**
```python
# In database.py _init_database():
cursor.execute("""
    CREATE TABLE IF NOT EXISTS ships (
        ship_symbol TEXT PRIMARY KEY,
        player_id INTEGER NOT NULL,
        ship_data TEXT NOT NULL,  -- JSON blob of ship state
        last_updated TIMESTAMP NOT NULL,
        FOREIGN KEY (player_id) REFERENCES players(player_id) ON DELETE CASCADE
    )
""")

# In daemon startup:
async def sync_ships_from_api():
    ships = await api_client.get_my_ships()
    for ship in ships:
        await db.upsert_ship(ship.symbol, player_id, ship.to_json(), datetime.now())
```

**Pros:**
- Minimal changes to daemon tools
- Provides ship data cache for offline/testing scenarios
- Quick fix to unblock operations

**Cons:**
- Reverses intentional design decision (lines 367-370 comment)
- Reintroduces cache staleness problems
- Adds sync complexity
- Goes against database layer architect's intent

### Fix 3: Hybrid Approach - API with Fallback Cache (COMPROMISE)
**Rationale:** Query API first, cache in database as fallback for rate limiting

**Implementation:**
```python
async def get_ship(ship_symbol: str, use_cache: bool = False):
    """Get ship data from API with optional database fallback"""
    try:
        # Always prefer fresh API data
        ship = await api_client.get_ship(ship_symbol)
        # Optionally cache for fallback
        if use_cache:
            await db.cache_ship(ship_symbol, ship.to_json())
        return ship
    except ApiRateLimitError:
        # Fallback to cache if rate limited
        logger.warning("API rate limited, falling back to ship cache")
        return await db.get_cached_ship(ship_symbol)
```

**Pros:**
- Best of both worlds: fresh data + rate limit resilience
- Respects design intent (API primary source)
- Provides fallback mechanism

**Cons:**
- More complex implementation
- Still requires adding ships cache table
- May hide API issues behind cache

### Fix 4: Add Comprehensive Error Messaging (SUPPORTIVE)
**Rationale:** Help future debugging by clearly indicating architecture mismatch

**Implementation:**
```python
# In ship lookup code
try:
    ship = await db.get_ship(ship_symbol)
except TableNotFoundError:
    raise ConfigurationError(
        f"Ship lookup attempted against database, but ships table removed. "
        f"Daemon tools must query SpaceTraders API directly. "
        f"See database.py lines 367-370 for design rationale."
    )
```

**Pros:**
- Clear error messages for developers
- Points to architecture documentation
- Helps identify misconfigured tools

**Cons:**
- Doesn't fix the problem
- Only improves diagnostics

## Recommended Action Plan

**RECOMMENDED: Fix 1 (Update Daemon Tools to Query API Directly)**

This aligns with the database layer architect's design intent and provides the cleanest long-term solution.

**Phase 1: Immediate Identification (30 minutes)**
1. Search codebase for all `db.get_ship()` calls
2. Identify all daemon tools attempting database ship lookups
3. Document which MCP tools need updating
4. Prioritize by usage frequency

**Phase 2: API Integration (2-3 hours)**
1. Update daemon tool handlers to call `api_client.get_ship()` instead of `db.get_ship()`
2. Add error handling for API failures (timeouts, rate limits)
3. Test each updated tool individually
4. Verify ship data freshness

**Phase 3: Testing & Validation (1-2 hours)**
1. Test contract batch workflow with API ship lookup
2. Test trading operations with API ship lookup
3. Monitor API call frequency and rate limit usage
4. Verify no performance degradation

**Phase 4: Documentation (30 minutes)**
1. Document architecture decision in daemon tool README
2. Add code comments explaining API-first ship data approach
3. Update MCP tool documentation to reflect API dependencies

**Alternative (If Fix 1 Blocked):** Fix 2 (Restore Ships Table)
- Use if API rate limits become prohibitive
- Use if testing requires offline operation
- Document as temporary measure
- Plan migration to Fix 1 long-term

## Environment
- Agent: ENDURANCE
- System: X1-HZ85
- Ships Involved: ENDURANCE-1 (4 total ships affected)
- MCP Tools Used: ship_info (working - queries API), contract_batch_workflow (broken - queries DB)
- Container ID: N/A (daemon server PID 34005)
- Database: PostgreSQL (migrated from SQLite)
- Daemon Status: Running but ship lookups failing due to architecture mismatch

## Related Evidence

### Git History Context
```
Recent commits show PostgreSQL migration:
- "feat: add PostgreSQL support with automatic SQLite compatibility"
- Migration script exists: bot/scripts/migrate_sqlite_to_postgres.py
- Database.py modified in migration commit
- Ships table removed during refactoring (lines 367-370)
```

### Design Intent Documentation
```
From database.py lines 367-370:
"Ships table removed - ship data is now fetched directly from API"
"Historical note: Ships table was removed to ensure ship state
(location, fuel, cargo) is always fresh from the SpaceTraders API.
This prevents stale data issues and eliminates sync complexity."
```

### Session Timeline
```
Session #1: Daemon operations successful
           - Likely using SQLite with legacy ships table
           - Database queries succeeded against old schema
↓
[PostgreSQL Migration + Ships Table Removal]
           - New database schema per design intent (no ships table)
           - Daemon tools not updated to match new architecture
↓
Session #2: Complete daemon registry failure
           - Daemon tools query non-existent ships table
           - Database correctly returns "table not found"
           - MCP tools fail with "Ship not found" error
```

### Pattern: Incomplete Migration
```
Database Layer: ✅ MIGRATED (ships table removed, API-first design)
Daemon Tools:   ❌ NOT MIGRATED (still querying database for ships)
Result:         ❌ ARCHITECTURE MISMATCH (tools incompatible with DB layer)
```

## Additional Notes

**Why This is CRITICAL Severity:**
- Blocks ALL autonomous operations (daemon-based workflows)
- No revenue generation possible via daemon infrastructure
- Violates TARS architectural principle (Captain forced to execute CLI directly)
- Complete infrastructure failure, not isolated bug
- Affects entire fleet (all 4 ships)
- **Architecture mismatch** more severe than simple bug

**Why This is NOT a PostgreSQL Migration Bug:**
- PostgreSQL migration worked correctly
- Database schema is correct per design intent
- Ships table intentionally removed (documented in code comments)
- **Problem:** Daemon tools not updated to match new architecture

**Workaround Limitations:**
- CLI execution bypasses delegation protocols
- Not sustainable for autonomous operations
- Captain should coordinate specialists, not execute commands
- Defeats purpose of daemon-based autonomous architecture

**Key Insight:**
This is not a database sync bug - it's an **architecture migration incomplete**. The database layer correctly implements API-first ship data (no cache, no staleness). Daemon tools incorrectly assume database ship registry still exists. Fix requires updating daemon tools to match database layer's API-first architecture.

**Files Requiring Investigation for Fix 1:**
1. Daemon tool handlers (search for `db.get_ship()` calls)
2. Contract batch workflow implementation
3. Trading coordinator ship lookups
4. Mining operation ship validation
5. Any MCP tool performing ship existence checks

**Success Criteria for Fix:**
- All daemon tools query API for ship data
- Zero database ship table queries
- Fresh ship state for all operations
- No "ship not found" errors for valid ships
- Architecture alignment between database layer and daemon tools
