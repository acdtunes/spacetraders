# Bug Reporting Guide - AI Agents

**For:** Operations Officer, Intelligence Officer, Fleet Controller, Scout Coordinator, Flag Captain

## Purpose

This guide defines what constitutes a "bug" (requires Admiral intervention) vs "operational issue" (agents handle autonomously), and how to report bugs via Captain's Log.

## Bug vs Operational Issue

### ✅ Operational Issues (Agents Handle)

**These are NORMAL and agents recover autonomously:**

| Issue | Example | Agent Response |
|-------|---------|----------------|
| Ship in transit | Navigate called while moving | Wait for arrival, retry |
| Rate limited | 429 from SpaceTraders API | Wait 2s, retry |
| Insufficient fuel | Ship has 50 fuel, route needs 100 | Navigate to fuel station, refuel, retry |
| Low profit | Route earning <150k/trip | Stop daemon, request new route from Intelligence Officer |
| Market activity weak | Buyer demand exhausted | Wait 3-5 min, retry (or stop and replan) |
| Asteroid depleted | Yields drop to 0 | Stop daemon, report (request new asteroid) |

**Don't log CRITICAL_ERROR for these - they're expected gameplay.**

### 🚨 Bugs (Log CRITICAL_ERROR)

**These are UNEXPECTED and require Admiral review:**

| Bug Type | Example | Symptoms |
|----------|---------|----------|
| **Daemon crash** | Process exits unexpectedly | status=stopped, exit_code ≠ 0, <planned duration |
| **Retry loop** | Same error 3+ times despite retry logic | Operation stuck, no progress after 10+ minutes |
| **Data corruption** | Registry shows daemon exists but it's not running | Assignment locked, can't reassign ship |
| **State desync** | Ship shows DOCKED but API says IN_TRANSIT | Operations fail with contradictory state errors |
| **Null/missing data** | Market price returns null | API response missing expected fields |
| **Logic error** | Route planner creates impossible route | Fuel requirement > ship capacity |
| **Exception** | AttributeError, KeyError, TypeError | Code crashes with Python exception |

**Always log CRITICAL_ERROR for these - Admiral needs to fix code.**

## How to Report Bugs

### Captain's Log CRITICAL_ERROR Format

```python
mcp__spacetraders-bot__bot_captain_log_entry(
  agent="AGENT_SYMBOL",
  entry_type="CRITICAL_ERROR",
  operator="Operations Officer",  # Your role
  ship="SHIP-1",                   # Affected ship
  narrative="[DETAILED DESCRIPTION]",
  error="[EXACT ERROR MESSAGE]",
  resolution="[ACTIONS TAKEN + FIX NEEDED]"
)
```

### Required Fields

**1. narrative** (Detective work - be thorough)

Include:
- **What happened:** Sequence of events leading to error
- **Ship state:** Location, fuel, cargo, nav status before error
- **Error frequency:** First time? Recurring? How many times?
- **Diagnosis:** Your analysis of root cause (why you think it happened)
- **Context:** Market conditions, daemon logs, assignment state, related operations

**Example:**
```
"SHIP-1 trading daemon crashed after 3 trips with InsufficientFuelError. Ship attempted
navigation from X1-HU87-A2 to D42 (87 units) with only 12 fuel remaining. Ship is now
stranded at A2.

DIAGNOSIS: Route planner did not validate round-trip fuel requirements before starting
daemon. Initial fuel was 350/400, consumed ~116 fuel per round trip (58 units × 2 ×
1.0 fuel/unit in CRUISE). After 3 trips, fuel dropped to 12 (below DRIFT minimum of 1).
System should have switched to DRIFT or triggered refuel at 75 fuel threshold.

CONTEXT: This is the 3rd occurrence this week - SHIP-3 failed similarly on 2025-10-06,
SHIP-5 on 2025-10-07. All were trading operations in X1-HU87 with routes >80 units."
```

**2. error** (Exact technical details)

Include:
- Exact error message (copy/paste from logs)
- Error type (if known: AttributeError, InsufficientFuelError, etc.)
- Where it occurred (file:line if visible in logs, or MCP tool name)
- Stack trace (if available in daemon logs)

**Example:**
```
"InsufficientFuelError: Cannot navigate in DRIFT mode with 12 fuel (distance 87 requires
~0.3 fuel but ship safety requires >1 fuel minimum)

Stack trace from daemon logs:
  File 'ship_controller.py', line 156, in navigate
    raise InsufficientFuelError(f'Cannot navigate...')
"
```

**3. resolution** (What you did + what's needed)

Include:
- **IMMEDIATE:** Actions you took right now (stopped daemon, moved ship, refueled)
- **LONG-TERM FIX:** Code changes Admiral needs to make
- **PATTERN:** Is this recurring? Reference previous log entries
- **WORKAROUND:** Temporary solution to keep operations running

**Example:**
```
"IMMEDIATE: Manually navigated SHIP-1 to nearest fuel station X1-HU87-A1 (0-distance
orbital parent, no fuel needed). Refueled to 400/400. Restarted daemon with DRIFT mode
forced for remaining operations.

LONG-TERM FIX NEEDED:
1. Intelligence Officer route planner must validate (current_fuel - route_fuel_cost) > 100
   before approving plan
2. Operations Officer must check fuel before EACH navigation (not just at start)
3. Add auto-refuel trigger at 75 fuel threshold in ship_controller.py

PATTERN: 3rd occurrence this week (see log entries: abc123, def456). All trading ops in
X1-HU87 with routes >80 units.

WORKAROUND: Using DRIFT mode for all routes in X1-HU87 until Admiral fixes fuel validation."
```

## Bug Categories & Examples

### 1. Daemon Crashes

**Symptoms:**
- Daemon status shows stopped
- Exit code ≠ 0
- Completed fewer cycles/trips than planned
- Last log entry shows exception or error

**Example Report:**
```python
bot_captain_log_entry(
  entry_type="CRITICAL_ERROR",
  narrative="Daemon 'trader-ship1' crashed after 5 trips (planned: 30). Last log entry:
'AttributeError: NoneType has no attribute price'. Ship was attempting to sell IRON_ORE
at X1-HU87-A2 market.

DIAGNOSIS: sell_cargo() function doesn't handle null market prices. Market API returned
null price for IRON_ORE despite listing it as available. Code attempted to access
price.sellPrice without null check.

CONTEXT: Market was functioning normally for first 5 trips, then price went null on 6th
trip. Possible SpaceTraders API issue or market refresh timing.",

  error="AttributeError: 'NoneType' object has no attribute 'price'
File: ship_controller.py, line 245 in sell_cargo()
  total_price = market_data.price.sellPrice * units",

  resolution="IMMEDIATE: Stopped daemon, released SHIP-1. Ship safely docked at A2 with
40 units IRON_ORE in cargo (didn't sell).

LONG-TERM FIX: Add null check in sell_cargo() before accessing price fields. If null,
wait 30s and retry market fetch (may be temporary API glitch).

PATTERN: First occurrence of this specific error.

WORKAROUND: Manually sold cargo via API, restarted daemon."
)
```

### 2. Retry Loops

**Symptoms:**
- Same error repeating 3+ times
- Retry logic not recovering
- Operation stuck for >10 minutes with no progress

**Example Report:**
```python
bot_captain_log_entry(
  entry_type="CRITICAL_ERROR",
  narrative="Navigation failed 5 consecutive times for SHIP-2 attempting route to
X1-HU87-B9. Error message consistent: 'Ship is in transit' but ship.nav.status shows
'DOCKED'.

DIAGNOSIS: State machine desynchronization. Ship controller believes ship is DOCKED, but
SpaceTraders API reports IN_TRANSIT. Retry logic waited for arrival 3 times (2 minutes
each) but status never changed to ARRIVED.

CONTEXT: Ship was previously on mining route, daemon stopped normally 15 minutes ago.
Assignment was released, ship should be idle. Suspect previous navigation didn't complete
cleanly.",

  error="InvalidShipStateError: Ship status mismatch
Local state: DOCKED
API state: IN_TRANSIT
Navigation destination: None (API shows no active route)",

  resolution="IMMEDIATE: Stopped operation. Attempted manual orbit/dock cycle via API:
  - orbit_ship(SHIP-2) → Success
  - dock_ship(SHIP-2) → Success
  - Now shows DOCKED in both local and API state

LONG-TERM FIX: Add state validation before operations. If local ≠ API state, refresh
from API before proceeding. May need mutex/lock around state transitions in
ship_controller.py to prevent race conditions.

PATTERN: 2nd occurrence this month. Previous: SHIP-4 on 2025-09-28.

WORKAROUND: Manual state reset worked. Ship now operational."
)
```

### 3. Data Corruption

**Symptoms:**
- Registry conflicts (assignment exists, daemon doesn't)
- Missing records
- Stale data that sync doesn't fix
- Ships stuck in "active" status

**Example Report:**
```python
bot_captain_log_entry(
  entry_type="CRITICAL_ERROR",
  narrative="Assignment registry shows SHIP-5 assigned to daemon 'miner-ship5' but
daemon_status returns 'Daemon not found'. Attempted 'daemon_stop miner-ship5' → error:
'No such process'. Ship locked in 'active' status, cannot reassign to new operation.

DIAGNOSIS: Daemon process was killed externally (possibly manual kill -9 or system restart)
without proper cleanup. PID file exists but process doesn't. Assignment registry wasn't
updated.

CONTEXT: Server restarted 2 hours ago (system maintenance). All daemons were running before
restart. 4 other ships auto-recovered via assignments_sync, but SHIP-5 remained stuck.",

  error="RegistryCorruptionError: Assignment exists but daemon missing
Assignment: {ship: SHIP-5, daemon: miner-ship5, status: active, assigned_at: 2025-10-08T12:00:00}
Daemon status: None (process not running, PID file stale)
Cannot release: 'daemon must be stopped first' check failing",

  resolution="IMMEDIATE: Fleet Controller ran 'assignments_sync' which cleared stale entry.
Ship now shows 'idle' status and is available for reassignment.

LONG-TERM FIX:
1. Add daemon health check before marking assignment active
2. On system startup, run full registry cleanup (check all PIDs valid)
3. Add 'force release' option for stuck assignments (bypass daemon check)

PATTERN: Happens after system restarts. Occurred 3 times in last month (all post-restart).

WORKAROUND: assignments_sync fixed it. Recommend running sync after every system restart."
)
```

### 4. Unexpected API Behavior

**Symptoms:**
- API returns null/missing fields
- Data types don't match expected
- Successful API call but wrong result
- Rate limits exceeded despite proper throttling

**Example Report:**
```python
bot_captain_log_entry(
  entry_type="CRITICAL_ERROR",
  narrative="Market API returned null for IRON_ORE price at X1-HU87-D42. Route plan
expected 890cr buy price based on cached data. Daemon attempted purchase, received
'Good not available' error despite market listing showing IRON_ORE with ABUNDANT supply.

DIAGNOSIS: Market data inconsistency between listings endpoint and purchase endpoint.
GET /markets/{market}/goods shows IRON_ORE available, but POST /purchase returns error.
Price field in listings response is null instead of integer.

CONTEXT: Market was functioning normally 30 minutes ago (last successful purchase at
14:30). This started failing at 15:00. Other goods at same market working fine (sold
COPPER_ORE successfully just before this error).",

  error="MarketDataInconsistencyError: API endpoints disagree
GET /systems/X1-HU87/waypoints/X1-HU87-D42/market:
  {goods: [{symbol: IRON_ORE, supply: ABUNDANT, purchasePrice: null, sellPrice: 15}]}

POST /my/ships/SHIP-1/purchase:
  {error: {code: 4602, message: 'Good IRON_ORE not available at this market'}}",

  resolution="IMMEDIATE: Stopped trading daemon for SHIP-1. Ship safely docked at D42
with credits intact (no purchase attempted).

LONG-TERM FIX:
1. Intelligence Officer should validate price != null before planning routes
2. Operations Officer should check market data immediately before purchase (not at daemon
   start) to catch API changes
3. Add retry logic: if purchase fails with 'not available', refresh market data and retry
   once

PATTERN: First occurrence. Possible temporary SpaceTraders API issue or market reset event.

WORKAROUND: Waiting 10 minutes then querying market again. If still null, will switch to
alternative route (X1-HU87-B7 as backup IRON_ORE source)."
)
```

## Flag Captain's Role: Pattern Detection

Flag Captain should query Captain's Log for patterns:

```python
# After each operation completes
captain_log_search(agent="AGENT_SYMBOL", tag="error", timeframe=24)
```

### Escalation Thresholds

| Scenario | Action |
|----------|--------|
| 1 CRITICAL_ERROR | Log it, Operations Officer handles |
| 2 same errors <24hr | Note pattern, monitor |
| 3+ same errors <24hr | **ESCALATE TO ADMIRAL** |
| 2+ errors same ship | Stop ship operations, escalate |
| Ship lost/stranded | **IMMEDIATE ESCALATION** |
| Credits depleted | **IMMEDIATE ESCALATION** |

### Escalation Report Format

```markdown
🚨 BUG PATTERN DETECTED - ADMIRAL INTERVENTION REQUIRED

Error: [Error type]
Occurrences: [Count] in [timeframe]
Pattern: [Common factors]

Root Cause (Suspected):
[Analysis of why this is happening]

Impact:
- [Affected ships]
- [Downtime]
- [Lost profit]

Immediate Actions Taken:
- [Recovery steps]

Long-term Fix Required:
1. [Code change needed]
2. [Validation to add]
3. [Prevention measure]

Evidence (Captain's Log IDs):
- [Timestamp] - [Ship] - [Log ID]
- [Timestamp] - [Ship] - [Log ID]
```

## Best Practices

### For Operations Officer

✅ **DO:**
- Log CRITICAL_ERROR for daemon crashes, retry loops, exceptions
- Include full diagnostic context (ship state, error sequence, your analysis)
- Reference previous occurrences if pattern exists
- Suggest long-term fixes based on root cause
- Include workarounds you applied

❌ **DON'T:**
- Log CRITICAL_ERROR for normal operational issues (fuel low, market weak)
- Just copy error message without analysis
- Assume human knows context - explain everything
- Report bug without attempting recovery first

### For Flag Captain

✅ **DO:**
- Check Captain's Log after each operation completes
- Query for error patterns every 2 hours during AFK
- Escalate after 3 same errors in 24hr
- Include evidence (log IDs) in escalation reports
- Synthesize root cause from multiple error reports

❌ **DON'T:**
- Escalate single operational issues (fuel, market conditions)
- Wait for Admiral to ask - proactively report patterns
- Escalate without attempting specialist recovery first
- Report without full analysis and evidence

## Example Escalation Flow

```
Hour 0: SHIP-1 InsufficientFuelError
  → Operations Officer: Recovers (refuel, restart)
  → Operations Officer: Logs CRITICAL_ERROR
  → Flag Captain: Notes it, continues monitoring

Hour 3: SHIP-3 InsufficientFuelError (same symptoms)
  → Operations Officer: Recovers (refuel, restart)
  → Operations Officer: Logs CRITICAL_ERROR, notes pattern
  → Flag Captain: Queries log, sees 2 occurrences, monitors closely

Hour 6: SHIP-5 InsufficientFuelError (same symptoms)
  → Operations Officer: Recovers (refuel, restart)
  → Operations Officer: Logs CRITICAL_ERROR, references previous 2
  → Flag Captain: Detects pattern (3 errors, <24hr)
  → Flag Captain: ESCALATES TO ADMIRAL with full analysis

Admiral: Reviews all 3 log entries
  → Identifies root cause: Intelligence Officer fuel validation missing
  → Commits fix to intelligence_officer.py
  → Redeploys bot
  → Tests with new route
  → Pattern resolved
```

## Summary

**Bug = Code needs fixing by Admiral**
- Daemon crashes
- Retry loops (same error 3+ times)
- Data corruption
- Unexpected API behavior
- Exceptions

**Operational Issue = Agents handle autonomously**
- Ship in transit
- Rate limits
- Insufficient fuel (recoverable)
- Low profit (replan)
- Market conditions

**Report bugs via Captain's Log CRITICAL_ERROR entries with full diagnostic context.**

**Flag Captain detects patterns and escalates when 3+ same errors occur in 24hr.**
