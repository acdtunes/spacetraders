# Feature Proposal: AFK Mode Infrastructure Improvements

**Date:** 2025-11-06
**Priority:** CRITICAL
**Category:** OPTIMIZATION
**Status:** PROPOSED

## Problem Statement

Agent ENDURANCE's first autonomous AFK session (0.4 hours) resulted in **complete operational failure** with $0 credits earned, 0% fleet utilization, and 100% operations blocked. Three critical infrastructure gaps prevented any autonomous work:

1. **Missing waypoint_sync MCP tool** - Cannot discover markets, asteroids, or navigation waypoints
2. **Broken contract negotiation** - 0/3 contracts negotiated with silent failures
3. **Database authentication split** - MCP and CLI use different databases, causing "player not found" errors

**Root Cause:** The system lacks the foundational infrastructure needed for autonomous operations. While individual components exist (contract handlers, navigation logic, market tools), they cannot function together without critical pre-flight validation, error visibility, and operational fallbacks.

## Current Behavior

### AFK Session Results (0.4 Hours)
```
Session Duration: 0.4 hours (24 minutes)
Credits Earned: $0
Operations Started: 0
Operations Completed: 0
Fleet Utilization: 0%
Success Rate: 0%

Blocker Analysis:
- Contract operations: BLOCKED (0/3 negotiations, silent failures)
- Market scouting: BLOCKED (no waypoint discovery tool)
- Mining operations: BLOCKED (cannot find asteroid fields)
- Trading operations: BLOCKED (cannot discover marketplaces)
- Navigation: BLOCKED (database authentication failures)

Time Breakdown:
- Investigation: 20 minutes
- Bug reporting: 4 minutes
- Productive operations: 0 minutes
```

### Infrastructure Gaps Discovered

**Gap 1: No Pre-Flight Validation**
- TARS starts operations without checking if critical tools exist
- No validation that database is accessible
- No verification that waypoint cache is populated
- Operations fail only when attempting execution, wasting time

**Gap 2: Missing Core MCP Tools**
- `waypoint_sync` - Required to discover system waypoints (CRITICAL)
- `waypoint_list` - Exists but incomplete (read-only, no sync)
- `contract_negotiate` - Batch workflow exists but single negotiation missing
- `player_sync` - Cannot refresh player credits/status from API

**Gap 3: Silent Failure Pattern**
- Contract negotiation: 0/3 success with zero error messages
- Database authentication: "Player not found" with no troubleshooting guidance
- Missing tools: Operations blocked with no user-facing explanation
- TARS receives success responses with empty results, no way to diagnose issues

**Gap 4: No Operational Fallbacks**
- If contracts fail, TARS has no alternative operations
- If waypoint discovery fails, no recovery path
- If database authentication fails, operations halt permanently
- No graceful degradation from complex operations to simple operations

**Gap 5: Database Path Inconsistency**
- MCP server database: `/var/spacetraders.db`
- CLI database: `/bot/var/spacetraders.db`
- Player registration writes to one, operations read from the other
- Result: "Player not found" errors despite successful registration

## Impact

### Financial Impact
- **Credits Lost:** $0 (no operations could execute)
- **Opportunity Cost:** Projected $75k-125k revenue over expected 8-hour AFK session
- **Time Wasted:** 0.4 hours at 0% productivity = full session lost

### Operational Impact
**Fleet Status:**
```
ENDURANCE-1 (40-cargo hauler): IDLE at HQ, 100% fuel, empty cargo
ENDURANCE-2 (Solar probe): IDLE at HQ, 100% fuel
Both ships: 0 operations, 0 navigation attempts, 0 revenue generated
```

**Operations Attempted:**
- Contract batch workflow: Executed but 0/3 negotiations (silent failure)
- Market scouting: Blocked at planning stage (missing waypoint_sync tool)
- Navigation planning: Blocked (database authentication failure)
- Mining operations: Not attempted (cannot find asteroids without waypoints)

**Error Categories:**
- Infrastructure failures: 3 (database, missing tools, silent errors)
- Strategic failures: 0 (strategy is sound, execution impossible)
- API failures: Unknown (errors not surfaced to user)

### Strategic Impact
**Proven Strategy Blocked:**
From strategies.md (Phase 1: Intelligence Network):
- Step 1: Scout ship acquisition (POSSIBLE)
- Step 2: Scout operations (BLOCKED - no waypoint discovery)
- Step 3: Contract operations (BLOCKED - silent failures)

**Evidence:** All Phase 1 strategy requirements are blocked by infrastructure gaps. Cannot progress to revenue-generating operations without foundational tools.

## Proposed Solutions

### Priority 0: Critical Infrastructure (Must Have Before AFK)

#### P0-1: Pre-Flight Validation System

**What We Need:**
A validation system that runs BEFORE starting AFK mode to detect infrastructure gaps and prevent silent failures.

**User Story:**
- As Admiral starting AFK session
- When TARS initializes
- I need TARS to validate all required infrastructure exists and is accessible
- So operations fail fast with clear error messages instead of silently failing 24 minutes into the session
- Expected: Validation completes in <10 seconds, reports all blockers, prevents AFK start if critical issues found

**Required Validations:**

**Database Accessibility:**
- Must verify player exists in database
- Must verify database file is readable/writable
- Must verify MCP and CLI use same database path
- Must report clear error: "Database authentication failed: Player X not found. Run player_register first."

**MCP Tool Coverage:**
- Must verify all tools referenced in agent configs actually exist
- Must verify critical tools callable (waypoint_sync, contract_batch_workflow, scout_markets)
- Must report missing tools: "Required tool 'waypoint_sync' not implemented. Cannot start AFK mode."

**Waypoint Cache State:**
- Must verify waypoint cache populated for current system
- Must count waypoints of critical types (MARKETPLACE, ASTEROID_FIELD)
- Must report empty cache: "Waypoint cache empty for system X1-HZ85. Run waypoint_sync before AFK."

**Fleet Readiness:**
- Must verify ships exist and are accessible
- Must verify ships have fuel for operations
- Must verify ships are in valid states (not IN_TRANSIT during initialization)
- Must report fleet issues: "Ship ENDURANCE-1 has 0% fuel. Refuel before AFK."

**API Connectivity:**
- Should verify SpaceTraders API is reachable
- Should verify player token is valid
- Should report connectivity issues: "SpaceTraders API unreachable. Check network connection."

**Acceptance Criteria:**
1. Must run automatically when TARS initializes AFK mode
2. Must complete validation in <10 seconds for typical systems
3. Must block AFK start if CRITICAL issues found (database, missing tools)
4. Must warn but allow start if MEDIUM issues found (low fuel, small cache)
5. Must provide actionable error messages with remediation steps
6. Must log validation results to mission-logs for debugging

**Success Metrics:**
- Validation catches all 3 infrastructure gaps from this failed session
- TARS refuses to start AFK mode until gaps are fixed
- Admiral receives clear list of actions needed before AFK viable
- Zero silent failures during AFK sessions (all failures caught at pre-flight)

#### P0-2: Waypoint Synchronization Tool

**What We Need:**
MCP tool `waypoint_sync` to populate waypoint cache from SpaceTraders API.

**User Story:**
- As TARS Captain
- When starting operations in a new system
- I need to synchronize all waypoints from SpaceTraders API to local cache
- So I can plan routes, discover markets, and navigate ships autonomously
- Expected: 40-60 waypoints synced in <30 seconds, enabling all navigation operations

**Required Information:**
- System symbol (e.g., "X1-HZ85")
- Player authentication (player_id or agent symbol)
- Waypoint metadata: symbol, type, coordinates, traits

**Expected Behavior:**
```
Syncing waypoints for system X1-HZ85...
✓ Fetched 47 waypoints (3 API pages)
✓ Stored in local cache

Discovered:
- 6 marketplaces
- 2 shipyards
- 8 fuel stations
- 5 asteroid fields

Navigation operations enabled.
```

**Acceptance Criteria:**
1. Must fetch ALL waypoints in system (handle pagination)
2. Must store waypoints in local cache via WaypointRepository
3. Must work with player_id or agent symbol for authentication
4. Must be idempotent (safe to run multiple times)
5. Must display progress for large systems (>50 waypoints)
6. Must complete in <30 seconds for typical systems
7. Must provide clear error messages on API failures

**Success Metrics:**
- Waypoint cache populated with 40-60 waypoints for X1-HZ85
- Navigation operations succeed without KeyError exceptions
- Scout deployment works (can find MARKETPLACE waypoints)
- Contract delivery works (can navigate to delivery waypoints)

**Note:** Detailed specification in separate feature proposal: 2025-11-06_17-15_new-tool_waypoint-sync.md

#### P0-3: Database Path Unification

**What We Need:**
Single consistent database path used by both MCP server and CLI.

**User Story:**
- As TARS Captain
- When registering a player via MCP
- I need that player to be accessible via CLI commands
- So all operations use consistent authentication and data
- Expected: Player registration via MCP immediately available to CLI operations

**Current Problem:**
```
MCP Database:  /var/spacetraders.db
CLI Database:  /bot/var/spacetraders.db
Result: Player exists in one, not found in the other
```

**Expected Behavior:**
```
Unified Database: /var/spacetraders.db (absolute path)
MCP writes player → CLI reads same player ✓
MCP writes ships → CLI reads same ships ✓
All operations share consistent state ✓
```

**Acceptance Criteria:**
1. Must use absolute path for database (not relative)
2. Must configure path via environment variable (SPACETRADERS_DB_PATH)
3. Must verify both MCP and CLI use same path
4. Must migrate existing data if databases diverged
5. Must document database path in configuration guide

**Success Metrics:**
- Player registered via MCP immediately queryable via CLI
- Ship synced via CLI immediately visible in MCP
- Zero "player not found" errors during operations
- Database queries return consistent results across MCP/CLI

**Note:** Detailed specification in separate feature proposal: 2025-11-06_bug-fix_database-path-unification.md

### Priority 1: Error Visibility (Should Have)

#### P1-1: Contract Negotiation Error Surfacing

**What We Need:**
Visibility into why contract negotiation fails, instead of silent 0/3 results.

**User Story:**
- As TARS Captain executing contract batch workflow
- When contract negotiation fails
- I need to see WHY it failed (API error, location issue, rate limit, etc.)
- So I can diagnose problems and adjust strategy
- Expected: Clear error message like "API Error 400: No faction at waypoint X1-HZ85-A1 offers contracts"

**Current Problem:**
```
Batch Workflow Results
Contracts negotiated: 0
Contracts accepted:   0
Contracts fulfilled:  0
Total profit:         0 credits

^ Zero explanation of why negotiations failed
```

**Expected Behavior:**
```
Batch Workflow Results
Contracts negotiated: 0
  - Attempt 1: API Error 400 (No faction at HQ)
  - Attempt 2: API Error 400 (No faction at HQ)
  - Attempt 3: API Error 400 (No faction at HQ)

Recommendation: Navigate to faction headquarters before negotiating contracts.
```

**Acceptance Criteria:**
1. Must display API error messages to user (status code + message)
2. Must show which attempts succeeded vs failed
3. Must provide troubleshooting recommendations for common errors
4. Must log detailed errors to mission-logs for debugging
5. Should suggest alternative locations if faction not available at current waypoint

**Success Metrics:**
- Contract failures show actionable error messages
- Admiral can diagnose contract issues without reading code
- TARS can detect "No faction available" and skip contract operations gracefully

#### P1-2: MCP Tool Existence Checking

**What We Need:**
Runtime validation that MCP tools referenced in operations actually exist.

**User Story:**
- As TARS deploying scout-coordinator
- When scout-coordinator tries to call waypoint_list tool
- I need immediate error if tool doesn't exist
- So I can report missing infrastructure instead of silently failing operations
- Expected: "Cannot deploy scout-coordinator: Required tool 'waypoint_sync' not implemented."

**Current Problem:**
```
Deploying scout-coordinator...
✓ Agent configured
✓ Tools loaded
✗ Operation failed silently (no error message)

^ TARS doesn't know waypoint_sync tool doesn't exist until deep into operation execution
```

**Expected Behavior:**
```
Deploying scout-coordinator...
✓ Agent configured
✗ Validation failed: Required tool 'waypoint_sync' not found in MCP server
  Available tools: waypoint_list (read-only)
  Missing tools: waypoint_sync

Cannot start market scouting operations. Contact Admiral to implement missing tools.
```

**Acceptance Criteria:**
1. Must validate tool existence before deploying specialist agents
2. Must check all tools listed in agent config against MCP server registry
3. Must provide list of missing tools with actionable error message
4. Must fail fast (before starting long-running operations)
5. Should suggest workarounds if available (e.g., "Use manual CLI: waypoint sync")

**Success Metrics:**
- Missing tool errors caught in <1 second (before operations start)
- Error messages list specific missing tools
- TARS can report infrastructure gaps to Admiral clearly

#### P1-3: Health Check Logging

**What We Need:**
Detailed logging of all infrastructure checks, operations, and errors to mission-logs.

**User Story:**
- As Admiral debugging failed AFK session
- When returning from AFK
- I need detailed logs showing what TARS attempted and why it failed
- So I can identify infrastructure gaps and fix them for next session
- Expected: mission-logs/2025-11-06_afk-session.log with timestamped operations and errors

**Required Log Entries:**

**Pre-Flight:**
```
[2025-11-06 14:00:00] AFK Session Started
[2025-11-06 14:00:01] Pre-flight validation: Database check... PASS
[2025-11-06 14:00:02] Pre-flight validation: MCP tools check... FAIL (waypoint_sync missing)
[2025-11-06 14:00:03] Pre-flight validation: Waypoint cache check... FAIL (0 waypoints in X1-HZ85)
[2025-11-06 14:00:04] Pre-flight validation: Fleet readiness... PASS (2 ships docked, fueled)
[2025-11-06 14:00:05] AFK Session BLOCKED: 2 critical validations failed
```

**Operations:**
```
[2025-11-06 14:00:10] Attempting contract batch workflow (3 iterations)
[2025-11-06 14:00:12] Contract negotiation attempt 1: API Error 400 (No faction at waypoint)
[2025-11-06 14:00:14] Contract negotiation attempt 2: API Error 400 (No faction at waypoint)
[2025-11-06 14:00:16] Contract negotiation attempt 3: API Error 400 (No faction at waypoint)
[2025-11-06 14:00:17] Contract batch workflow completed: 0/3 negotiated
```

**Acceptance Criteria:**
1. Must log all pre-flight validation results (PASS/FAIL with details)
2. Must log all operation attempts with timestamps
3. Must log all API errors with full error messages
4. Must log all infrastructure failures (missing tools, database errors)
5. Must write logs to mission-logs directory with session timestamp
6. Should include metrics (credits earned, operations completed, time elapsed)

**Success Metrics:**
- Admiral can reconstruct entire AFK session from logs
- Logs clearly show which validations failed and why
- Logs include actionable remediation steps
- Logs are human-readable without code knowledge

### Priority 2: Operational Fallbacks (Nice to Have)

#### P2-1: Fallback Operation Chains

**What We Need:**
When primary operations fail, TARS should attempt fallback operations instead of idling.

**User Story:**
- As TARS Captain during AFK session
- When contract operations fail (0 negotiations)
- I need to fall back to alternative revenue operations (market scouting, exploration)
- So fleet generates some credits even if primary strategy blocked
- Expected: TARS tries contracts → fails → tries scouting → fails → tries basic navigation → reports all blockers

**Fallback Chain:**

**Primary: Contract Fulfillment**
- If contracts succeed → execute contracts for 5-15K credits/hour
- If contracts fail → Fall back to Market Scouting

**Fallback 1: Market Scouting**
- If waypoint_sync available → sync waypoints, deploy scouts
- If waypoint_sync missing → Fall back to HQ Operations

**Fallback 2: HQ Operations**
- If HQ has marketplace → buy/sell operations at HQ only
- If HQ has shipyard → check ship prices, plan fleet expansion
- If HQ has neither → Fall back to Idle Monitoring

**Fallback 3: Idle Monitoring**
- Monitor fleet status every 5 minutes
- Log "waiting for infrastructure fixes" message
- Check if infrastructure issues resolved (tools added, database fixed)
- Retry operations if infrastructure restored

**Acceptance Criteria:**
1. Must attempt operations in priority order (contracts > scouting > HQ ops > idle)
2. Must log why each operation failed and which fallback chosen
3. Must not retry failed operations infinitely (max 3 attempts per operation type)
4. Must report final status: "Operating in fallback mode: HQ marketplace operations only"
5. Should periodically retry higher-priority operations if infrastructure changes

**Success Metrics:**
- TARS generates SOME credits even when primary strategy blocked
- Logs clearly show fallback chain execution
- Fleet utilization >0% even in degraded mode
- Admiral can see what limited operations were possible

#### P2-2: Autonomous Recovery System

**What We Need:**
TARS should detect and recover from transient failures automatically.

**User Story:**
- As TARS Captain during AFK session
- When temporary failures occur (network errors, API rate limits)
- I need to retry operations automatically with backoff
- So transient failures don't halt entire AFK session
- Expected: API rate limit → wait 60s → retry operation → success

**Recovery Strategies:**

**Network Errors (Transient):**
- Retry up to 3 times with exponential backoff (1s, 5s, 15s)
- If all retries fail → Fall back to next operation priority
- Log all retry attempts with reasons

**API Rate Limits (Transient):**
- Detect 429 status codes
- Wait 60 seconds before retry
- Reduce operation frequency if rate limits persist
- Log rate limit events for Admiral review

**Database Lock Errors (Transient):**
- Retry up to 5 times with 1s delay
- If persistent → Report database issue, enter idle mode
- Log database contention events

**Missing Waypoint Cache (Recoverable):**
- Detect empty cache during navigation
- Attempt waypoint_sync automatically
- If sync succeeds → Retry navigation
- If sync fails → Report infrastructure gap, fall back

**Acceptance Criteria:**
1. Must distinguish transient errors (retry) from permanent errors (fall back)
2. Must use exponential backoff for retries (not tight loops)
3. Must limit total retries (no infinite loops)
4. Must log all recovery attempts with outcomes
5. Must report unrecoverable errors clearly to Admiral

**Success Metrics:**
- Transient network errors don't halt AFK sessions
- API rate limits handled gracefully (wait and retry)
- Recovery attempts logged for Admiral visibility
- AFK sessions run for hours despite occasional transient failures

#### P2-3: Graceful Degradation Modes

**What We Need:**
Clear operational modes when infrastructure is incomplete.

**User Story:**
- As TARS Captain with incomplete infrastructure
- When critical tools missing but some operations possible
- I need to communicate operational mode clearly
- So Admiral knows what limited operations are running
- Expected: "Operating in DEGRADED mode: HQ-only operations, no navigation"

**Operational Modes:**

**FULL MODE (All Infrastructure Available):**
```
✓ Database accessible
✓ Waypoint cache populated
✓ All MCP tools available
✓ Fleet operational
Operations: Contracts, Scouting, Mining, Trading
Expected Revenue: 15-50K credits/hour
```

**DEGRADED MODE (Limited Infrastructure):**
```
✓ Database accessible
✗ Waypoint cache empty (navigation blocked)
✓ Most MCP tools available
✓ Fleet operational at HQ
Operations: HQ marketplace trading only
Expected Revenue: 2-5K credits/hour
```

**MINIMAL MODE (Critical Gaps):**
```
✓ Database accessible
✗ Waypoint cache empty
✗ Critical tools missing (waypoint_sync, contract_negotiate)
✓ Fleet readable (ship_list works)
Operations: Monitoring only, no active operations
Expected Revenue: 0 credits/hour
```

**OFFLINE MODE (Cannot Operate):**
```
✗ Database inaccessible
✗ Player authentication failed
✗ Fleet unreadable
Operations: None (cannot start AFK session)
Expected Revenue: 0 credits/hour
Action Required: Fix infrastructure before AFK
```

**Acceptance Criteria:**
1. Must detect current operational mode during pre-flight validation
2. Must communicate mode clearly in AFK start message
3. Must operate within constraints of current mode
4. Must log mode and reasons for degradation
5. Must report expected revenue range for current mode

**Success Metrics:**
- Admiral immediately understands what operations are possible
- TARS doesn't attempt operations beyond current mode capabilities
- Mode-appropriate operations execute successfully
- Logs show mode transitions and reasons

## Evidence

### Metrics Supporting This Proposal

**AFK Session Performance (This Session):**
```
Duration: 0.4 hours (24 minutes)
Credits Earned: $0
Target Revenue: $75k-125k over 8 hours
Actual Revenue: $0 (100% below target)
Fleet Utilization: 0% (target 60-80%)
Operations Success Rate: 0% (all blocked)
```

**Infrastructure Gap Impact:**
```
Missing waypoint_sync: Blocks scouting, mining, trading, navigation (4 operation types)
Silent contract failures: Blocks contract strategy (1 operation type)
Database split: Blocks all CLI-based operations
Total Operations Blocked: 100% (no viable operations)
```

**Pre-Flight Validation Impact Estimate:**
```
Without validation:
- 24 minutes wasted attempting impossible operations
- 0 credits earned despite 2 ships available
- 3 infrastructure bugs discovered through trial-and-error

With validation (estimated):
- <1 minute to detect all infrastructure gaps
- Clear error messages listing required fixes
- AFK session blocked immediately (Admiral notified)
- Time saved: 23 minutes
```

### Proven Strategy Reference

**From strategies.md, Phase 1 Requirements (lines 15-47):**
> "Build market visibility before scaling operations... Scout ships cost <100K but provide priceless market data... Contracts provide TWO revenue touchpoints."

**Analysis:**
- Phase 1 requires waypoint discovery (blocked without waypoint_sync)
- Phase 1 requires contract negotiation (blocked by silent failures)
- Phase 1 is PROVEN to generate 10-20K per contract
- Infrastructure gaps prevent execution of proven strategy

**Strategic Validation:**
The proposed solutions directly address the gap between "what the strategy requires" (waypoint discovery, working contracts, error visibility) and "what infrastructure provides" (incomplete tools, silent failures, no validation).

### Related Bug Reports

**Bug Report: 2025-11-06_16-45_missing-waypoint-sync-mcp-tool.md**
- Problem: waypoint_sync tool doesn't exist despite being critical for navigation
- Impact: ALL waypoint-dependent operations blocked
- Solution: P0-2 addresses this directly

**Bug Report: 2025-11-06_contract-negotiation-zero-success.md**
- Problem: 0/3 contracts negotiated with no error messages
- Impact: Contract strategy completely non-functional
- Solution: P1-1 surfaces errors, P2-1 provides fallbacks

**Bug Report: 2025-11-06_integration-failures-afk-mode.md**
- Problem: Multiple integration failures (database, missing tools, agent config)
- Impact: Complete AFK operational failure
- Solution: P0-1 catches these during pre-flight validation

**Strategic Assessment: 2025-11-06_strategic-assessment_afk-mode-capability-gap.md**
- Problem: "Infrastructure deficit creates operational deadlock"
- Evidence: 0% operations possible due to missing foundation
- Solution: All P0 proposals address foundational gaps

## Risks & Tradeoffs

### Risk 1: Pre-Flight Validation Delays AFK Start
**Concern:** Validation checks may take 30-60 seconds, delaying AFK session start.

**Acceptable because:**
- 30-60s validation saves 24+ minutes of silent failures
- Admiral gets immediate feedback on infrastructure readiness
- Failed AFK sessions waste hours, not seconds
- Validation is one-time cost, operations run for hours

**Mitigation:**
- Optimize validation queries (use indexes)
- Run validation checks in parallel where possible
- Cache validation results (re-validate only on infrastructure changes)
- Target <10s for typical validation (40-60 waypoints, 2 ships, 20 tools)

### Risk 2: Fallback Operations Generate Lower Revenue
**Concern:** Fallback operations may generate 2-5K credits/hour vs 15-50K for optimal operations.

**Acceptable because:**
- 2-5K credits/hour is better than $0/hour (current state)
- Fallback operations keep fleet active while infrastructure fixed
- Admiral can see what's possible vs what's blocked
- Graceful degradation better than hard failure

**Mitigation:**
- Clearly communicate expected revenue for current operational mode
- Log why fallbacks engaged (Admiral can prioritize fixes)
- Periodically retry higher-priority operations if infrastructure restored
- Document fallback performance metrics for Admiral review

### Risk 3: Error Logging May Generate Large Log Files
**Concern:** Detailed logging of all operations may create multi-MB log files over long AFK sessions.

**Acceptable because:**
- Debugging value outweighs disk space cost
- Logs are critical for diagnosing infrastructure issues
- Disk space is cheap, debugging time is expensive
- Logs enable self-improvement (TARS learns from failures)

**Mitigation:**
- Rotate logs daily (max 7 days retention)
- Compress old logs (gzip reduces size 80-90%)
- Log structured data (JSON) for efficient parsing
- Provide log level controls (DEBUG, INFO, ERROR)

### Risk 4: Database Path Unification May Break Existing Setups
**Concern:** Changing database path may invalidate existing player/ship/waypoint data.

**Acceptable because:**
- Current setup is broken (database split prevents operations)
- Migration path exists (copy data from MCP db to CLI db)
- One-time migration cost enables all future operations
- Alternative (manual database sync) requires intervention every session

**Mitigation:**
- Provide migration script to consolidate databases
- Backup existing databases before migration
- Validate data integrity after migration
- Document migration process clearly

### Risk 5: Waypoint Sync May Hit API Rate Limits
**Concern:** Syncing large systems (100+ waypoints) may trigger SpaceTraders rate limits.

**Acceptable because:**
- Waypoint sync is one-time operation per system
- Subsequent operations use cache (no repeated API calls)
- Rate limit impact is bounded (30-60s delay at worst)
- Alternative (manual waypoint discovery) is far slower

**Mitigation:**
- Display progress during multi-page syncs ("Fetching page 3/5...")
- Respect standard rate limiting (built into API client)
- Cache results to avoid repeated syncs
- Allow sync to complete over 30-60s if needed

## Success Metrics

### Immediate Success (Within 1 Hour of Implementation)

**Pre-Flight Validation (P0-1):**
1. **All infrastructure gaps caught:** Validation detects missing waypoint_sync tool
2. **Clear error messages:** Admiral receives actionable list of required fixes
3. **Fast validation:** Completes in <10 seconds for X1-HZ85 system
4. **AFK blocked when unsafe:** TARS refuses to start AFK until critical gaps fixed

**Waypoint Sync (P0-2):**
5. **Cache populated:** waypoint_list returns 40-60 waypoints for X1-HZ85
6. **Navigation enabled:** plan_route works without KeyError exceptions
7. **Markets discovered:** 6-8 MARKETPLACE waypoints identified
8. **Fast sync:** Completes in <30 seconds for typical systems

**Database Unification (P0-3):**
9. **Consistent authentication:** Player registered via MCP immediately accessible to CLI
10. **Zero auth failures:** No "player not found" errors during operations
11. **Shared state:** Ships synced via CLI visible in MCP ship_list

### Operational Success (Within First AFK Session)

**Error Visibility (P1):**
12. **Contract errors visible:** "API Error 400: No faction at waypoint" shown to Admiral
13. **Missing tools reported:** "Required tool 'waypoint_sync' not found" caught at pre-flight
14. **Logs comprehensive:** mission-logs contain full operation timeline with errors

**Operations Running:**
15. **Fleet utilization >60%:** Ships actively executing operations, not idle
16. **Revenue generation >0:** Some credits earned even if primary strategy blocked
17. **Operations completed:** At least 1 operation type successful (contracts OR scouting OR HQ ops)

### Strategic Success (Within 24 Hours)

**Infrastructure Robustness:**
18. **Zero silent failures:** All failures produce clear error messages
19. **Fast failure detection:** Infrastructure issues caught in <10s (pre-flight), not 24 minutes
20. **Self-documenting:** Logs enable Admiral to identify and fix infrastructure gaps

**Autonomous Operations:**
21. **AFK sessions viable:** TARS can operate for 1+ hours without intervention
22. **Fallback operations work:** When primary blocked, fallback generates some revenue
23. **Recovery from transient errors:** Network errors, rate limits don't halt sessions

**Financial Performance:**
24. **Revenue >0 credits/hour:** Even in degraded mode, some operations generate credits
25. **Revenue trajectory positive:** Each AFK session more successful as infrastructure improves
26. **Revenue target achievable:** Path clear to 15-50K credits/hour (Phase 1-2 strategies)

## Alternatives Considered

### Alternative 1: Manual Pre-Session Checklist
**Description:** Admiral manually validates infrastructure before starting AFK (check database, run waypoint_sync, verify tools exist).

**Rejected because:**
- Error-prone (Admiral may forget steps)
- Slow (15-30 minutes manual validation)
- Not scalable (different checklists per system, per strategy)
- Defeats purpose of autonomous operations
- TARS cannot self-heal if infrastructure degrades during session

### Alternative 2: Fail Fast Without Error Messages
**Description:** Let operations fail immediately but don't invest in error visibility.

**Rejected because:**
- Admiral wastes time debugging failures without logs
- Same failures repeat across sessions (no learning)
- Silent failures waste entire sessions (current state)
- Infrastructure gaps not discoverable without error messages

### Alternative 3: Build All Tools First, Then Validate
**Description:** Implement every possible MCP tool before attempting AFK operations.

**Rejected because:**
- High upfront cost (100+ hours to implement all tools)
- Many tools not needed for Phase 1 strategy
- Delays AFK viability by weeks/months
- Validation still needed even with all tools
- Over-engineering (YAGNI principle)

**Better approach:** Implement critical tools (waypoint_sync, contract_negotiate) based on strategy requirements, validate before each session.

### Alternative 4: Retry Operations Indefinitely
**Description:** When operations fail, retry forever until they succeed.

**Rejected because:**
- Wastes API calls on permanent failures (no faction available)
- Hides infrastructure issues (operations fail silently, retry silently)
- No fallback to alternative strategies
- Rate limit risk (too many retries)
- Admiral never learns about blockers

**Better approach:** Retry transient failures (network errors) with backoff, fall back on permanent failures (missing tools).

### Alternative 5: Operate Without Waypoint Cache
**Description:** Fetch waypoint data on-demand instead of caching.

**Rejected because:**
- High API call overhead (1 call per navigation attempt)
- Slow performance (network latency on every operation)
- Rate limit risk (100+ waypoint lookups per session)
- Doesn't enable operations like "find nearest marketplace"
- Worse than batch sync (30s one-time cost vs constant overhead)

## Implementation Estimates

### Priority 0: Critical Infrastructure (Must Have)

**P0-1: Pre-Flight Validation System**
- **Effort:** 4-6 hours
- **Breakdown:**
  - Design validation framework (1 hour)
  - Implement database check (1 hour)
  - Implement tool existence check (1 hour)
  - Implement waypoint cache check (1 hour)
  - Integrate with AFK start workflow (1 hour)
  - Testing and error message refinement (1 hour)
- **Complexity:** MEDIUM (new validation framework)
- **Blockers:** None (can implement immediately)
- **Value:** HIGH (catches all infrastructure gaps before wasting time)

**P0-2: Waypoint Synchronization Tool**
- **Effort:** 2-4 hours
- **Breakdown:**
  - MCP tool definition (30 minutes)
  - CLI command implementation (1 hour)
  - CLI-MCP integration (30 minutes)
  - Testing with X1-HZ85 system (1 hour)
  - Error handling refinement (1 hour)
- **Complexity:** LOW (backend command already exists)
- **Blockers:** Requires P0-3 (database unification) for authentication
- **Value:** CRITICAL (unblocks ALL navigation operations)

**P0-3: Database Path Unification**
- **Effort:** 2-3 hours
- **Breakdown:**
  - Environment variable configuration (1 hour)
  - Database migration script (1 hour)
  - Testing MCP/CLI consistency (1 hour)
- **Complexity:** MEDIUM (configuration changes)
- **Blockers:** None (can implement immediately)
- **Value:** CRITICAL (enables authentication across MCP/CLI)

**Total P0 Effort:** 8-13 hours
**P0 Dependencies:** P0-3 → P0-2 (database must be unified before waypoint_sync works)
**P0 Critical Path:** P0-3 (2-3h) + P0-2 (2-4h) + P0-1 (4-6h) = 8-13 hours
**P0 Deployment:** Can be deployed incrementally (P0-3 first, then P0-2, then P0-1)

### Priority 1: Error Visibility (Should Have)

**P1-1: Contract Negotiation Error Surfacing**
- **Effort:** 1-2 hours
- **Breakdown:**
  - Add error logging to batch workflow (30 minutes)
  - Surface errors to CLI output (30 minutes)
  - Add troubleshooting recommendations (30 minutes)
  - Testing with various error conditions (30 minutes)
- **Complexity:** LOW (add logging and error display)
- **Blockers:** None (can implement immediately)
- **Value:** MEDIUM (improves debugging, doesn't unblock operations)

**P1-2: MCP Tool Existence Checking**
- **Effort:** 2-3 hours
- **Breakdown:**
  - Build MCP tool registry (1 hour)
  - Add validation to agent deployment (1 hour)
  - Error message formatting (30 minutes)
  - Testing with missing tools (30 minutes)
- **Complexity:** MEDIUM (new validation system)
- **Blockers:** None (can implement immediately)
- **Value:** MEDIUM (fast failure detection)

**P1-3: Health Check Logging**
- **Effort:** 3-4 hours
- **Breakdown:**
  - Design log format and structure (1 hour)
  - Implement logging framework (1 hour)
  - Add log calls to operations (1 hour)
  - Log rotation and cleanup (1 hour)
- **Complexity:** MEDIUM (new logging infrastructure)
- **Blockers:** None (can implement immediately)
- **Value:** MEDIUM (enables debugging, doesn't prevent failures)

**Total P1 Effort:** 6-9 hours
**P1 Dependencies:** None (all independent)
**P1 Deployment:** Can deploy after P0 (low priority)

### Priority 2: Operational Fallbacks (Nice to Have)

**P2-1: Fallback Operation Chains**
- **Effort:** 4-6 hours
- **Breakdown:**
  - Design fallback decision tree (1 hour)
  - Implement fallback logic (2 hours)
  - Add fallback logging (1 hour)
  - Testing fallback scenarios (2 hours)
- **Complexity:** HIGH (complex control flow)
- **Blockers:** Requires P0-1, P0-2 (validation and waypoint sync)
- **Value:** MEDIUM (improves degraded mode, doesn't prevent failures)

**P2-2: Autonomous Recovery System**
- **Effort:** 3-5 hours
- **Breakdown:**
  - Identify transient vs permanent errors (1 hour)
  - Implement retry with backoff (2 hours)
  - Add recovery logging (1 hour)
  - Testing transient failure scenarios (1 hour)
- **Complexity:** MEDIUM (retry logic)
- **Blockers:** Requires P1-1 (error visibility)
- **Value:** MEDIUM (handles transient failures gracefully)

**P2-3: Graceful Degradation Modes**
- **Effort:** 2-3 hours
- **Breakdown:**
  - Define operational modes (1 hour)
  - Implement mode detection (1 hour)
  - Add mode communication to AFK start (1 hour)
- **Complexity:** LOW (mode detection and display)
- **Blockers:** Requires P0-1 (pre-flight validation)
- **Value:** LOW (improves communication, doesn't change operations)

**Total P2 Effort:** 9-14 hours
**P2 Dependencies:** P0-1 → P2-1, P2-3 | P1-1 → P2-2
**P2 Deployment:** Can defer until after P0 + P1 proven

### Total Implementation Estimate

**Critical Path (P0 only):** 8-13 hours
**Full Implementation (P0 + P1 + P2):** 23-36 hours

**Phased Deployment:**
- **Phase 1 (P0):** 8-13 hours → AFK mode becomes viable
- **Phase 2 (P1):** +6-9 hours → Error visibility improves debugging
- **Phase 3 (P2):** +9-14 hours → Autonomous recovery and fallbacks

**Recommendation:** Implement Phase 1 (P0) immediately to unblock AFK operations. Phase 2 and 3 can be deployed incrementally based on AFK session feedback.

## Recommendation

**IMPLEMENT PRIORITY 0 IMMEDIATELY (P0-1, P0-2, P0-3)**

**Justification:**

**1. Complete Operational Blockage:**
- Current state: 0% operations possible, $0 earned in 0.4-hour session
- P0 fixes unblock 100% of navigation-dependent operations
- Expected improvement: 0 credits/hour → 5-15K credits/hour (Phase 1 contracts)

**2. Proven Strategy Dependency:**
- Phase 1 strategy (strategies.md) is research-backed and proven
- Infrastructure gaps prevent execution of proven strategy
- P0 fixes directly enable Phase 1 execution

**3. High ROI:**
- Implementation: 8-13 hours
- Unblocks: Contracts, scouting, mining, trading (4 operation types)
- Enables: Autonomous AFK sessions generating 5-50K credits/hour
- Payback period: <1 hour of successful AFK operations

**4. Foundation for Future Work:**
- P0 creates infrastructure foundation
- P1 and P2 build on P0 (cannot implement without it)
- Future strategies depend on navigation (waypoint_sync required)

**5. Fast Failure Prevention:**
- Pre-flight validation saves 24+ minutes per failed session
- Clear error messages reduce Admiral debugging time
- Infrastructure gaps caught before wasting time

**Deployment Priority:**

**Week 1 (Critical Path):**
1. **P0-3:** Database path unification (2-3 hours) - Deploy first, enables authentication
2. **P0-2:** Waypoint sync tool (2-4 hours) - Deploy second, enables navigation
3. **P0-1:** Pre-flight validation (4-6 hours) - Deploy third, prevents future failures

**Week 2 (Error Visibility):**
4. **P1-1:** Contract error surfacing (1-2 hours) - Diagnose contract issues
5. **P1-2:** Tool existence checking (2-3 hours) - Fast failure on missing tools
6. **P1-3:** Health check logging (3-4 hours) - Comprehensive debugging

**Week 3+ (Fallbacks - Optional):**
7. **P2-1:** Fallback operation chains (4-6 hours) - Graceful degradation
8. **P2-2:** Autonomous recovery (3-5 hours) - Handle transient failures
9. **P2-3:** Degradation modes (2-3 hours) - Clear mode communication

**Success Criteria for Deployment:**

**P0 Deployment Successful When:**
- Pre-flight validation runs in <10 seconds
- Validation catches all 3 infrastructure gaps from this session
- waypoint_sync populates 40-60 waypoints for X1-HZ85
- Database authentication works consistently across MCP/CLI
- Navigation operations succeed (plan_route, navigate without errors)
- First AFK session generates >0 credits/hour

**Abort Criteria:**
- If P0 implementation exceeds 20 hours → Re-evaluate approach
- If waypoint_sync fails to populate cache → Investigate API issues
- If database unification breaks existing functionality → Rollback and redesign

**This proposal represents the minimum viable infrastructure needed to enable autonomous AFK operations. Without P0 fixes, AFK mode will continue to fail with 0% success rate and $0 revenue.**
