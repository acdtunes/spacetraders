# Hybrid Ship Caching Implementation Plan (SIMPLIFIED)

## Executive Summary

**Context:** Autonomous bot running TODAY. Need minimal viable caching to ensure consistency.

**Approach:** Evidence-based, pragmatic implementation. Build what's proven needed, measure what's uncertain, defer what's speculative.

**Timeline:**
- **Today (Morning)**: Phase 1-2 + instrumentation (3 hours)
- **Today (Afternoon)**: Bot runs, collect performance data
- **Today (Evening)**: Phase 4 IF measurements show need (2 hours)
- **Later**: Phase 3 deferred until proven needed

**Scope Reduction:**
- Original plan: ~2,000 lines, 103 test scenarios, 3-4 weeks
- Simplified plan: ~150 lines, 10 test scenarios, 1 day
- **95% less code, same core value**

---

## Architecture Context

### Current State
- **Database storage:** Ships stored in SQLite
- **Explicit sync:** Manual CLI command `sync ships --player-id X`
- **Write operations:** Update domain state manually, persist to DB
- **Read operations:** Load from DB, reconstruct waypoints from graph
- **API resilience:** ✅ Already implemented (retry/backoff, rate limiting)

### Problems (Evidence-Based)
1. ✅ **PROVEN: Stale data risk** - DB can drift from API after mutations
2. ❓ **UNPROVEN: Performance bottleneck** - No benchmarks of waypoint reconstruction
3. ❌ **NON-ISSUE: Manual sync burden** - Bot auto-syncs on startup
4. ❌ **NON-ISSUE: Staleness detection** - No auto-refresh mechanism planned

### Solution: Minimal Viable Caching
- ✅ **Auto-sync after mutations** - Ensures consistency (MUST DO)
- ✅ **Timestamp for debugging** - Track last sync (MUST DO)
- ✅ **Simple cache warming** - Sync on bot startup (MUST DO)
- 📊 **Waypoint caching** - Build IF measurements show need (CONDITIONAL)
- ⏸️ **Single ship sync** - Defer until fleet >50 ships (YAGNI)
- ⏸️ **Staleness queries** - Defer until auto-refresh exists (YAGNI)

---

## Phase 1: Auto-Sync After Mutations (SIMPLIFIED)

### Goal
Capture API responses and automatically update DB after write operations.

**Priority:** 🔥 CRITICAL - Must complete today (morning)
**Effort:** 2 hours
**Complexity:** LOW - Reuse existing code

### Implementation Strategy

**SIMPLIFICATION:** Reuse existing `_convert_api_ship_to_entity()` from `sync_ships.py` instead of creating new factory method.

**Why:** The conversion logic already exists (lines 101-187 in sync_ships.py). Don't duplicate it.

---

#### 1.1 Add Repository Update Flag
**File:** `src/spacetraders/adapters/secondary/persistence/ship_repository.py`

**Update the update() method signature (line 164):**
```python
def update(self, ship: Ship, from_api: bool = False) -> None:
    """
    Update ship with optional API sync flag.

    Args:
        ship: Ship entity to update
        from_api: If True, indicates data came from API (used for timestamp tracking)
    """
    # Existing update logic
    # Note: from_api flag will be used in Phase 2 for synced_at timestamp
```

**Lines changed:** ~5

---

#### 1.2 Update All Command Handlers
Apply this pattern to 4 handlers: DockShipHandler, OrbitShipHandler, RefuelShipHandler, NavigateShipHandler

**Pattern to apply:**
```python
# After API call succeeds
result = self._api.{operation}(ship.ship_symbol)

# Extract ship data from response
ship_data = result.get('data', {}).get('ship')
if not ship_data:
    raise DomainException(f"API returned no ship data for {operation}")

# Import conversion function (at top of file)
from ..commands.sync_ships import SyncShipsHandler

# Convert API response to Ship entity (reuse existing logic)
ship = SyncShipsHandler._convert_api_ship_to_entity(
    ship_data,
    request.player_id
)

# Update with from_api=True flag
self._ship_repo.update(ship, from_api=True)
```

**Files to modify:**
1. `dock_ship.py` (line 90-96)
2. `orbit_ship.py` (line 89-95)
3. `refuel_ship.py` (line 115-127)
4. `navigate_ship.py` (line 288-300)

**Lines changed:** ~15 lines per handler × 4 = 60 lines total

**Rationale:**
- ✅ Reuses existing, tested conversion logic
- ✅ No code duplication
- ✅ Fails loudly if API returns no data (no silent fallbacks)
- ✅ Simple and maintainable

---

## Phase 2: Cache Metadata (MINIMAL)

### Goal
Track when data was synced for debugging purposes.

**Priority:** 🔥 CRITICAL - Must complete today (morning)
**Effort:** 30 minutes
**Complexity:** TRIVIAL - Single column

### Implementation

**SIMPLIFICATION:** Add ONLY `synced_at` timestamp. Skip `created_at` and `updated_at` (no use case).

#### 2.1 Database Migration
**File:** `src/spacetraders/adapters/secondary/persistence/database.py`

**Update ships table schema (line 84-109):**
```sql
CREATE TABLE IF NOT EXISTS ships (
    ship_symbol TEXT NOT NULL,
    player_id INTEGER NOT NULL,
    current_location_symbol TEXT NOT NULL,
    fuel_current INTEGER NOT NULL,
    fuel_capacity INTEGER NOT NULL,
    cargo_capacity INTEGER NOT NULL,
    cargo_units INTEGER NOT NULL,
    engine_speed INTEGER NOT NULL,
    nav_status TEXT NOT NULL,
    system_symbol TEXT NOT NULL,
    synced_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,  -- NEW: Track last API sync
    PRIMARY KEY (ship_symbol, player_id),
    FOREIGN KEY (player_id) REFERENCES players(player_id) ON DELETE CASCADE
)
```

**Migration script:**
```python
# Run once before bot starts
import sqlite3

conn = sqlite3.connect('var/spacetraders.db')
cursor = conn.cursor()

cursor.execute("PRAGMA table_info(ships)")
columns = [col[1] for col in cursor.fetchall()]

if 'synced_at' not in columns:
    cursor.execute("""
        ALTER TABLE ships
        ADD COLUMN synced_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
    """)
    conn.commit()
    print("✅ Added synced_at column")
else:
    print("✅ synced_at column already exists")

conn.close()
```

**Lines changed:** 1 column + 15 lines migration = 16 lines

---

#### 2.2 Update ShipRepository
**File:** `src/spacetraders/adapters/secondary/persistence/ship_repository.py`

**Modify update() method (line 164):**
```python
def update(self, ship: Ship, from_api: bool = False) -> None:
    """Update ship with optional API sync tracking"""
    with self._db.get_connection() as conn:
        cursor = conn.cursor()

        if from_api:
            # Data from API: update synced_at
            cursor.execute("""
                UPDATE ships SET
                    current_location_symbol = ?,
                    fuel_current = ?,
                    ...,
                    synced_at = CURRENT_TIMESTAMP
                WHERE ship_symbol = ? AND player_id = ?
            """, (...))
        else:
            # Local update: preserve synced_at
            cursor.execute("""
                UPDATE ships SET
                    current_location_symbol = ?,
                    fuel_current = ?,
                    ...
                WHERE ship_symbol = ? AND player_id = ?
            """, (...))

        conn.commit()
```

**Lines changed:** ~10 lines

---

## Phase 3: Single Ship Sync

**Status:** ⏸️ DEFERRED

**Why:** No proven need for syncing individual ships vs full fleet. Full sync with 10 ships takes <2 seconds.

**When to build:** When fleet >50 ships AND full sync takes >5 seconds

**Effort if needed later:** 2 hours

---

## Phase 4: Waypoint Caching (CONDITIONAL)

### Decision Criteria
Build this ONLY if measurements show performance problems.

**Priority:** 📊 MEASURE FIRST - Decide tonight based on data
**Effort:** 2 hours IF needed
**Complexity:** MEDIUM

### Step 1: Add Instrumentation (Today Morning - 15 minutes)

**File:** `src/spacetraders/adapters/secondary/persistence/ship_repository.py`

**Add to `_reconstruct_waypoint()` method (line 229):**
```python
import time

def _reconstruct_waypoint(self, symbol: str, system_symbol: str) -> Waypoint:
    """Reconstruct Waypoint from graph"""
    start = time.perf_counter()

    # ... existing reconstruction logic ...

    elapsed = time.perf_counter() - start
    logger.info(f"Waypoint reconstruction {symbol}: {elapsed*1000:.2f}ms")

    return waypoint
```

### Step 2: Collect Data (Today Afternoon)

Run bot and watch logs for:
- **Average waypoint reconstruction time** - Is it >50ms?
- **Frequency** - How many reconstructions per minute?
- **Cache hit potential** - How many unique waypoints?

### Step 3: Decide (Today Evening)

**IF** average time >50ms AND frequency >100/minute:
- Build simple waypoint cache (upsert on read, query before graph lookup)
- **Effort:** 2 hours

**ELSE:**
- Ship as-is, waypoint reconstruction is fast enough
- Can revisit later if fleet grows

### Implementation (IF Needed)

**Create waypoints table:**
```sql
CREATE TABLE IF NOT EXISTS waypoints (
    symbol TEXT PRIMARY KEY,
    system_symbol TEXT NOT NULL,
    waypoint_type TEXT NOT NULL,
    x REAL NOT NULL,
    y REAL NOT NULL,
    traits TEXT,  -- JSON array
    synced_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
)
```

**Update _reconstruct_waypoint():**
```python
def _reconstruct_waypoint(self, symbol: str, system_symbol: str) -> Waypoint:
    # Try cache first
    waypoint = self._waypoint_cache.get(symbol)
    if waypoint:
        return waypoint

    # Cache miss: fetch from graph
    waypoint = # ... existing graph logic ...

    # Store in cache
    self._waypoint_cache.set(symbol, waypoint)

    return waypoint
```

**Lines:** ~100 if needed

---

## Phase 5: Cache Warming (ULTRA-SIMPLE)

### Goal
Ensure bot has fresh ship data on startup.

**Priority:** 🔥 CRITICAL - Must complete today (morning)
**Effort:** 5 minutes
**Complexity:** TRIVIAL - One function call

### Implementation

**File:** `<your_bot_main>.py` (wherever bot starts)

```python
async def run_bot(player_id: int):
    """Main bot loop"""
    from src.spacetraders.configuration.container import get_mediator
    from src.spacetraders.application.navigation.commands.sync_ships import SyncShipsCommand

    mediator = get_mediator()

    # Warm cache on startup (reuse existing sync command)
    logger.info(f"Warming cache for player {player_id}...")
    ships = await mediator.send(SyncShipsCommand(player_id=player_id))
    logger.info(f"✅ Cache warmed: {len(ships)} ships synced")

    # Main bot loop
    while True:
        # ... bot logic ...
        await asyncio.sleep(1)
```

**Lines:** 5 lines

**No CLI flags, no config settings, no complexity.** Just call the existing sync command.

---

## Performance Instrumentation

### Add to Multiple Locations

**1. Ship Reads (track frequency):**
```python
# In ship_repository.find_by_symbol()
logger.debug(f"Ship read: {ship_symbol}")
```

**2. Waypoint Reconstruction (track performance):**
```python
# In ship_repository._reconstruct_waypoint()
logger.info(f"Waypoint reconstruction {symbol}: {elapsed_ms:.2f}ms")
```

**3. Bot Loop (track iteration time):**
```python
# In bot main loop
start = time.perf_counter()
# ... bot decision logic ...
elapsed = time.perf_counter() - start
logger.info(f"Bot iteration: {elapsed*1000:.2f}ms")
```

### Analysis Tonight

**Review logs and answer:**
1. How long does waypoint reconstruction take?
2. How many ship reads per bot iteration?
3. Is bot loop fast enough (<100ms per iteration)?

**Decide:** Build Phase 4 cache OR ship as-is

---

## Testing Strategy (SIMPLIFIED)

### BDD Scenarios: 10 Total (Not 103)

**Phase 1: Auto-Sync (5 scenarios)**
```gherkin
Feature: Auto-sync ship state from API

Scenario: Dock syncs ship state
  Given ship "SHIP-1" is in orbit
  When I dock ship "SHIP-1"
  Then ship status in DB should be "DOCKED"
  And synced_at should be recent

Scenario: Orbit syncs ship state
  Given ship "SHIP-1" is docked
  When I orbit ship "SHIP-1"
  Then ship status in DB should be "IN_ORBIT"
  And synced_at should be recent

Scenario: Refuel syncs ship state
  Given ship "SHIP-1" has 50/100 fuel
  When I refuel ship "SHIP-1"
  Then ship fuel in DB should be 100
  And synced_at should be recent

Scenario: Navigate syncs ship state
  Given ship "SHIP-1" is at "X1-A1-B2"
  When I navigate to "X1-A1-C3"
  Then ship location in DB should be "X1-A1-C3"
  And synced_at should be recent

Scenario: API error preserves state
  Given ship "SHIP-1" exists
  When API returns error during dock
  Then ship state in DB should be unchanged
  And synced_at should be unchanged
```

**Phase 2: Timestamps (2 scenarios)**
```gherkin
Feature: Track sync timestamps

Scenario: Synced_at updates on API sync
  Given ship "SHIP-1" exists
  When I sync from API with from_api=True
  Then synced_at should be updated

Scenario: Synced_at preserved on local update
  Given ship "SHIP-1" exists
  When I update locally with from_api=False
  Then synced_at should NOT be updated
```

**Phase 4: Waypoint Cache (3 scenarios - IF BUILT)**
```gherkin
Feature: Cache waypoints

Scenario: Cache waypoint on first read
  When I read ship at waypoint "X1-A1-B2"
  Then waypoint should be cached

Scenario: Use cached waypoint on second read
  Given waypoint "X1-A1-B2" is cached
  When I read ship at waypoint "X1-A1-B2"
  Then graph provider should NOT be called

Scenario: Performance improvement
  Given 10 ships at 5 unique waypoints
  When I read all ships twice
  Then second read should be 10x faster
```

**Total:** 7-10 scenarios depending on Phase 4

---

## Implementation Checklist (Today)

### Morning (3 hours)

- [ ] **Phase 1:** Update repository.update() with from_api flag (5 min)
- [ ] **Phase 1:** Update DockShipHandler (15 min)
- [ ] **Phase 1:** Update OrbitShipHandler (15 min)
- [ ] **Phase 1:** Update RefuelShipHandler (15 min)
- [ ] **Phase 1:** Update NavigateShipHandler (15 min)
- [ ] **Phase 2:** Run migration script - add synced_at column (5 min)
- [ ] **Phase 2:** Update repository UPDATE SQL (15 min)
- [ ] **Phase 5:** Add cache warming to bot startup (5 min)
- [ ] **Instrumentation:** Add waypoint timing logs (15 min)
- [ ] **Instrumentation:** Add bot loop timing logs (10 min)
- [ ] **Tests:** Write 7 BDD scenarios (60 min)
- [ ] **Tests:** Run existing 103 tests - ensure no regressions (10 min)

### Afternoon (Bot Running)

- [ ] Monitor logs for waypoint reconstruction times
- [ ] Monitor logs for bot iteration times
- [ ] Check for any errors or unexpected behavior
- [ ] Collect performance metrics

### Evening (IF Needed)

- [ ] **Phase 4:** IF waypoint slow: Build waypoint cache (2 hours)
- [ ] **Tests:** IF Phase 4 built: Add 3 waypoint cache scenarios (30 min)

---

## Success Criteria

✅ **Database consistency:** Ship state matches API after every mutation
✅ **Debugging capability:** synced_at timestamp helps diagnose issues
✅ **Performance:** Bot loop <100ms per iteration
✅ **Reliability:** No stale data issues
✅ **Tests:** All 103 existing tests pass + 7-10 new tests pass

---

## Deferred Features (Build When Needed)

| Feature | Deferred Until | Effort | Risk |
|---------|----------------|--------|------|
| Phase 3: Single ship sync | Fleet >50 ships | 2 hours | NONE |
| Phase 4: Waypoint cache | Proven slow (>50ms) | 2 hours | LOW |
| created_at/updated_at timestamps | Audit trail needed | 30 min | NONE |
| find_stale_ships() queries | Auto-refresh feature | 1 hour | NONE |
| get_sync_status() analytics | Observability needs | 1 hour | NONE |

---

## Appendix: Measurement Commands

**Run this tonight to analyze logs:**
```bash
# Count waypoint reconstructions
grep "Waypoint reconstruction" bot.log | wc -l

# Average waypoint time
grep "Waypoint reconstruction" bot.log | awk '{print $NF}' | sed 's/ms//' | awk '{sum+=$1; count++} END {print sum/count "ms average"}'

# Bot iteration time
grep "Bot iteration" bot.log | awk '{print $NF}' | sed 's/ms//' | sort -n | tail -20

# Unique waypoints accessed
grep "Waypoint reconstruction" bot.log | awk '{print $3}' | sort -u | wc -l
```

**Decision matrix:**
- IF avg waypoint time >50ms AND >100 reconstructions/min → Build cache
- ELSE → Waypoint reconstruction is fast enough, ship as-is

