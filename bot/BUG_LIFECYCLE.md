# Bug Lifecycle - End-to-End Workflow

Complete workflow from bug discovery through fix deployment.

## Overview

```
Operations Officer → Flag Captain → Admiral → Bug-Fixer → Fix Deployed
  (discovers)      (detects pattern) (reviews) (fixes code)  (resolved)
```

## Phase 1: Discovery (Operations Officer)

**When:** Ship operation encounters unexpected error

**Actions:**
1. Attempt immediate recovery (retry, refuel, navigate, etc.)
2. If recovery succeeds: Log CRITICAL_ERROR to Captain's Log
3. If recovery fails: Stop operation, log CRITICAL_ERROR, escalate to Flag Captain

**Log Entry Requirements:**
```python
bot_captain_log_entry(
  agent="AGENT_SYMBOL",
  entry_type="CRITICAL_ERROR",
  operator="Operations Officer",
  ship="SHIP-1",
  narrative="[Full diagnostic: what happened, why, ship state, sequence]",
  error="[Exact error message + stack trace]",
  resolution="[Immediate action + long-term fix needed + pattern notes]"
)
```

**Example:**
```
🔴 Hour 0: SHIP-1 InsufficientFuelError
Operations Officer:
- Recovered: Navigated to fuel station, refueled, restarted daemon
- Logged: CRITICAL_ERROR with full diagnostic
- Noted: Initial fuel 350, route consumed 116/trip, ran out after 3 trips
```

## Phase 2: Pattern Detection (Flag Captain)

**When:** After each operation completes or every 2 hours during AFK

**Actions:**
1. Query Captain's Log for recent CRITICAL_ERROR entries
2. Analyze for patterns (same error type, common factors)
3. If pattern detected (3+ errors in 24hr): Escalate to Admiral
4. If single occurrence: Continue monitoring

**Pattern Detection:**
```python
# Query recent errors
captain_log_search(agent="AGENT_SYMBOL", tag="error", timeframe=24)

# Look for:
- Same error message appearing 3+ times
- Multiple ships failing with similar symptoms
- Errors clustered in time (within 1-2 hours)
- Common operation type or system
```

**Escalation Threshold:**
| Errors | Timeframe | Action |
|--------|-----------|--------|
| 1 error | Any | Log it, monitor |
| 2 errors | <24hr, same type | Note pattern, watch closely |
| **3+ errors** | **<24hr, same type** | **ESCALATE TO ADMIRAL** |

**Example:**
```
🟡 Hour 3: Pattern Emerging
Flag Captain detects:
- SHIP-1 (Hour 0) + SHIP-3 (Hour 3) = 2 occurrences
- Same error: InsufficientFuelError
- Same operation: Trading in X1-HU87
- Same route distance: 80-90 units
Action: Note pattern, continue monitoring

🔴 Hour 6: Pattern Confirmed
Flag Captain detects:
- SHIP-5 (Hour 6) = 3rd occurrence
- Pattern: All trading ops, all X1-HU87, all routes >80 units
Action: ESCALATE TO ADMIRAL
```

## Phase 3: Escalation (Flag Captain → Admiral)

**When:** Pattern threshold reached (3+ errors in 24hr)

**Escalation Report Format:**
```markdown
🚨 BUG PATTERN DETECTED - ADMIRAL INTERVENTION REQUIRED

Error: InsufficientFuelError
Occurrences: 3 ships (SHIP-1, SHIP-3, SHIP-5) in 6 hours
Pattern: All trading operations in X1-HU87 system
Common Factor: Intelligence Officer route planning (all routes 80-90 units)

Root Cause (Suspected):
Intelligence Officer not validating round-trip fuel before approving routes.
Formula used: distance × 1.0 (CRUISE) but doesn't account for 3+ round trips.

Impact:
- 3 ships stranded (recovered manually)
- ~45 minutes downtime per ship
- Estimated 180k credits lost profit

Immediate Actions Taken:
- Operations Officer recovered all 3 ships (refueled)
- Continuing operations with manual fuel monitoring

Long-term Fix Required:
1. Add fuel validation to Intelligence Officer route planning
2. Operations Officer should check fuel before EACH trip, not just start
3. Add auto-refuel trigger at 75 fuel threshold

Evidence (Captain's Log entries):
- 2025-10-08 14:23 - SHIP-1 CRITICAL_ERROR (log ID: abc123)
- 2025-10-08 16:45 - SHIP-3 CRITICAL_ERROR (log ID: def456)
- 2025-10-08 19:12 - SHIP-5 CRITICAL_ERROR (log ID: ghi789)
```

## Phase 4: Investigation (Admiral → Bug-Fixer)

**When:** Admiral receives escalation from Flag Captain

**Admiral Actions:**
1. Review escalation report
2. Spawn Bug-Fixer agent with context
3. Provide access to Captain's Log entries

**Admiral Spawns Bug-Fixer:**
```python
Task(
  description="Fix InsufficientFuelError pattern",
  prompt="""You are the Bug-Fixer Specialist.

  Flag Captain has escalated a bug pattern: InsufficientFuelError affecting 3 ships
  in X1-HU87 trading operations. Ships run out of fuel after 3 trips despite initial
  validation passing.

  Captain's Log entries: abc123, def456, ghi789

  Review the CRITICAL_ERROR entries, analyze the root cause, create tests that
  reproduce the issue, implement a fix, and validate the solution.

  See .claude/agents/bug-fixer.md for your full instructions.""",
  subagent_type="general-purpose"
)
```

## Phase 5: Bug Fix (Bug-Fixer Agent)

**Bug-Fixer Workflow:**

### 1. Review Captain's Log Diagnostics
```python
# Query detailed error entries
bot_captain_log_search(agent="AGENT_SYMBOL", tag="error", timeframe=24)

# Extract from each entry:
# - narrative: Full diagnostic context
# - error: Exact error message
# - resolution: Recovery attempts and suggestions
```

### 2. Understand the Bug
- Operations Officer hypothesis: "Route planner doesn't validate multi-trip fuel"
- Common factor: All routes 80-90 units, all trading operations
- Error occurs after 3 trips (not immediately)

### 3. Find/Create Tests
```gherkin
Scenario: Multi-trip trading with inadequate fuel planning
  Given a ship with 350 fuel units
  And a trading route of 87 units requiring 3+ round trips
  When I start a trading daemon for 10 trips
  Then the daemon should validate multi-trip fuel requirements
  And the operation should trigger refuel before fuel drops below 100
```

### 4. Validate Failure
```bash
pytest tests/test_trading_fuel_steps.py -v
# FAILED - Daemon runs out of fuel after 3 trips
```

### 5. Implement Fix
**File:** `src/spacetraders_bot/operations/intelligence_officer.py`

**Before:**
```python
def validate_route(ship, route):
    fuel_needed = route.distance * 2 * 1.0  # Single round trip
    if ship.fuel > fuel_needed:
        return True, "Route approved"
    return False, "Insufficient fuel"
```

**After:**
```python
def validate_route(ship, route, min_trips=5):
    fuel_per_trip = route.distance * 2 * 1.0  # One round trip
    fuel_needed = fuel_per_trip * min_trips    # Multiple trips
    fuel_buffer = 100                           # Safety margin

    if ship.fuel >= (fuel_needed + fuel_buffer):
        return True, "Route approved"
    return False, f"Insufficient fuel for {min_trips} trips"
```

### 6. Validate Fix
```bash
pytest tests/test_trading_fuel_steps.py -v
# PASSED - Route validation now requires fuel for 5+ trips
```

### 7. Report to Admiral
```markdown
# Bug Fix Report: Multi-Trip Fuel Validation

**Source**: Captain's Log pattern (3 CRITICAL_ERROR entries in 6 hours)

## ROOT CAUSE
Intelligence Officer validated fuel for single round trip but didn't account
for continuous daemon operations requiring 5+ trips. Ships exhausted fuel
mid-operation.

## FIX APPLIED
Added multi-trip validation with 100-unit safety buffer.
File: src/spacetraders_bot/operations/intelligence_officer.py:78

## TESTS ADDED
- test_multi_trip_fuel_validation
- test_refuel_trigger_at_threshold

## VALIDATION
All tests passing. Deployed to production. Monitoring for 24hr.

## PREVENTION
Added comment requiring multi-trip validation for all continuous operations.
```

## Phase 6: Deployment (Admiral)

**Admiral Actions:**
1. Review bug-fixer report
2. Verify tests pass locally
3. Commit fix with descriptive message
4. Deploy updated bot
5. Monitor Captain's Log for 24 hours
6. Confirm pattern resolved

**Git Commit:**
```bash
git add src/spacetraders_bot/operations/intelligence_officer.py
git add tests/test_trading_fuel_steps.py
git commit -m "Fix: Add multi-trip fuel validation to Intelligence Officer

Resolves InsufficientFuelError pattern affecting trading operations.
Ships were running out of fuel after 3 trips despite passing initial
validation.

Root cause: Route validator only checked single-trip fuel, not
continuous daemon operations requiring 5+ trips.

Fix: Added min_trips parameter (default 5) with 100-unit safety buffer.

Captain's Log entries: abc123, def456, ghi789
Tests: test_multi_trip_fuel_validation"

git push
```

## Phase 7: Verification (Flag Captain)

**Post-Deployment Monitoring:**

**Hour 24:**
```python
# Check for recurring errors
captain_log_search(agent="AGENT_SYMBOL", tag="error", timeframe=24)

# Verify:
- No new InsufficientFuelError entries
- All trading operations completing successfully
- Ships maintaining fuel >100 throughout operations
```

**Hour 48:**
```
✅ Pattern Resolved
Flag Captain report:
- 0 InsufficientFuelError occurrences in 48 hours
- 15 trading operations completed successfully
- All ships maintained fuel >100 throughout
- No manual interventions required

Bug fix verified successful. Pattern eliminated.
```

## Complete Timeline Example

```
Hour 0:00 - SHIP-1 encounters InsufficientFuelError
          → Operations Officer recovers, logs CRITICAL_ERROR

Hour 3:00 - SHIP-3 encounters same error
          → Operations Officer recovers, logs CRITICAL_ERROR
          → Flag Captain notes pattern (2 occurrences)

Hour 6:00 - SHIP-5 encounters same error (3rd occurrence)
          → Operations Officer recovers, logs CRITICAL_ERROR
          → Flag Captain detects pattern threshold
          → Flag Captain escalates to Admiral with analysis

Hour 6:15 - Admiral reviews escalation
          → Spawns Bug-Fixer agent with Captain's Log access

Hour 6:20 - Bug-Fixer queries Captain's Log entries
          → Reviews diagnostics from all 3 errors
          → Confirms root cause: multi-trip fuel validation missing

Hour 6:25 - Bug-Fixer creates failing test
          → Test reproduces issue (ship runs out of fuel)

Hour 6:35 - Bug-Fixer implements fix
          → Adds multi-trip fuel validation
          → Tests pass

Hour 6:40 - Bug-Fixer delivers report to Admiral
          → Complete analysis + fix + validation

Hour 6:45 - Admiral reviews report
          → Verifies tests locally
          → Commits fix
          → Deploys updated bot

Hour 6:50 - Operations resume with fixed code
          → Flag Captain monitors Captain's Log

Hour 30:50 (24hr later) - Flag Captain verification
            → 0 new InsufficientFuelError entries
            → 12 successful trading operations
            → Pattern confirmed resolved ✅
```

## Key Benefits

**1. Automated Discovery**
- Operations Officer detects bugs in production
- No manual log mining needed
- Errors captured with full context

**2. Pattern Detection**
- Flag Captain identifies recurring issues
- Prevents bug whack-a-mole (fixing symptoms not root cause)
- Escalates only when pattern confirmed

**3. Diagnostic Intelligence**
- Operations Officer provides root cause hypothesis
- Bug-Fixer has detailed context from production
- Faster diagnosis than manual debugging

**4. Test-Driven Fix**
- Bug-Fixer creates tests that reproduce issue
- Fix validated before deployment
- Prevents regressions

**5. Closed Loop**
- Flag Captain monitors post-deployment
- Verifies pattern eliminated
- Complete bug lifecycle tracking

## Roles Summary

| Role | Responsibility | Tools |
|------|----------------|-------|
| **Operations Officer** | Discover bugs, attempt recovery, log diagnostics | `bot_captain_log_entry` |
| **Flag Captain** | Detect patterns, escalate to Admiral | `bot_captain_log_search` |
| **Admiral (Human)** | Review escalations, spawn Bug-Fixer, deploy fixes | Task tool, git |
| **Bug-Fixer** | Investigate, create tests, implement fix, validate | pytest, code editor |

## Documents Reference

- **`BUG_REPORTING_GUIDE.md`** - Operations Officer logging format
- **`.claude/agents/bug-fixer.md`** - Bug-Fixer agent instructions
- **`docs/agents/templates/flag_captain.md`** - Pattern detection workflow
- **`.claude/agents/operations-officer.md`** - Bug reporting examples

---

**This lifecycle ensures:**
✅ All bugs are discovered and documented automatically
✅ Patterns are detected before they become critical
✅ Root causes are diagnosed with production context
✅ Fixes are test-driven and validated
✅ Resolutions are verified in production

**No bug escapes the system.**
