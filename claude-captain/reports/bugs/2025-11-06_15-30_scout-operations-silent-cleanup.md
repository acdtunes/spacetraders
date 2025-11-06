# Bug Report: Scout Operations Deployed Successfully But Silently Removed

**Date:** 2025-11-06 15:30
**Severity:** CRITICAL
**Status:** NEW
**Reporter:** Captain (via TARS)

## Summary
Scout operations for ENDURANCE-1 and ENDURANCE-2 were successfully deployed by scout-coordinator with confirmed container IDs, but both containers were completely removed from the daemon database within 6-12 minutes without leaving any logs, error messages, or state information. Operations that should run 35-45 minutes completed/failed in under 6 minutes with zero revenue generated.

## Impact
- **Operations Affected:** ALL scout market tour operations - complete failure
- **Credits Lost:** 18 minutes of zero revenue during AFK mode, estimated opportunity cost unknown
- **Duration:** 18 minutes active (0:00-0:18 of 60-minute AFK session)
- **Workaround:** NONE - containers are being silently cleaned up by unknown process
- **Blocking Status:** Scout operations are completely non-functional, potentially ALL daemon operations affected

## Steps to Reproduce
1. Start daemon server (verified running: pid 77210, socket exists)
2. Deploy scout_markets operation via scout-coordinator at minute 0:06:
   - Ship: ENDURANCE-2
   - System: X1-HZ85
   - Container ID assigned: `scout-tour-endurance-2-a0f051f8`
   - Status reported: STARTING (existing container from previous session)
3. Deploy scout_markets operation via scout-coordinator at minute 0:12:
   - Ship: ENDURANCE-1
   - Container ID assigned: `scout-tour-endurance-1-9b7931db`
   - Status reported: ACTIVE (newly created)
4. Verify both scouts ACTIVE, no conflicts reported
5. Expected runtime: 35-45 minutes
6. At minute 0:18, check daemon_list()
7. Observe: "No containers running"
8. Observe: Both ships docked at X1-HZ85-A1, IDLE
9. Observe: Zero logs available from either container
10. Observe: daemon_inspect returns "Container not found" for both

## Expected Behavior
- Containers should run for 35-45 minutes as configured
- Ships should navigate between market waypoints
- Containers should log progress (market visits, iterations)
- daemon_list() should show containers as RUNNING
- daemon_inspect() should return container metadata
- If containers fail, logs should capture error messages
- If containers complete, they should remain in database with completion status
- Operations should generate revenue from market intelligence

## Actual Behavior
- Containers deployed successfully with confirmed IDs
- Containers transitioned to ACTIVE status (scout-coordinator verified at 0:12)
- Within 6-12 minutes, containers completely removed from database
- No logs persisted to database (daemon_logs returns empty/success)
- No error messages captured
- daemon_inspect returns "Container not found"
- Ships moved from deployment locations to X1-HZ85-A1 (navigation occurred)
- Operations stopped silently
- Zero revenue generated
- Database cleanup occurred without user intervention or command

## Evidence

### Timeline of Events

**Minute 0:06 - Check-in #1:**
```
Scout-coordinator deployment attempt for ENDURANCE-2:
- Result: "Ship ENDURANCE-2 already assigned"
- Container ID: scout-tour-endurance-2-a0f051f8
- Status: STARTING
- Note: Container existed from previous session
```

**Minute 0:12 - Check-in #2:**
```
Scout-coordinator deployment for ENDURANCE-1:
- Result: SUCCESS
- Container ID: scout-tour-endurance-1-9b7931db
- Status: ACTIVE

Scout-coordinator status check for ENDURANCE-2:
- Container ID: scout-tour-endurance-2-a0f051f8
- Status: Confirmed ACTIVE
- No conflicts reported

Expected runtime: 35-45 minutes
```

**Minute 0:18 - Check-in #4 (CURRENT):**
```
daemon_list() result: "No containers running"

Both ships status:
- ENDURANCE-1: Docked X1-HZ85-A1, IDLE
- ENDURANCE-2: Docked X1-HZ85-A1, IDLE

Container inspection attempts: FAILED (not found)
```

### Daemon Logs

**ENDURANCE-1 Container:**
```
Command: daemon_logs scout-tour-endurance-1-9b7931db --player-id 2 --limit 100
Result: ✅ Command executed successfully
Logs: (empty - no logs found)
```

**ENDURANCE-2 Container:**
```
Command: daemon_logs scout-tour-endurance-2-a0f051f8 --player-id 2 --limit 100
Result: ✅ Command executed successfully
Logs: (empty - no logs found)
```

**Analysis:** Empty log sets indicate either:
1. Containers never initialized logging before cleanup
2. Cleanup process removed log entries from database
3. Containers crashed immediately without writing any logs
4. Logging system not functional

### Daemon Status

**ENDURANCE-1 Container:**
```
Command: daemon_inspect scout-tour-endurance-1-9b7931db
Result: ❌ Command failed (exit code: 1)
Error: Container scout-tour-endurance-1-9b7931db not found
```

**ENDURANCE-2 Container:**
```
Command: daemon_inspect scout-tour-endurance-2-a0f051f8
Result: ❌ Command failed (exit code: 1)
Error: Container scout-tour-endurance-2-a0f051f8 not found
```

**Analysis:** Containers completely removed from daemon database. No metadata, status, iterations, restarts, or timestamps available. This is NOT a status update to "STOPPED" or "FAILED" - the containers were deleted entirely.

### Ship State (Cannot Verify - Ship Names Unknown)

**Issue:** TARS provided ship names "ENDURANCE-1" and "ENDURANCE-2" but MCP ship_info tool reports:
```
❌ Error: Ship 'ENDURANCE-1' not found for player 2
❌ Error: Ship 'ENDURANCE-2' not found for player 2
```

**Analysis:** Either:
1. Ship names are incorrect (missing agent prefix like ENDURANCE-1 vs AGENT-ENDURANCE-1)
2. Player ID 2 is incorrect for agent ENDURANCE
3. Ships were sold/deleted during operation
4. Database inconsistency between daemon containers and ship records

**Unable to verify ship navigation occurred.** TARS reported ships moved from deployment locations to X1-HZ85-A1, but this cannot be confirmed via MCP tools.

### Daemon Server Status

**Process Status:**
```
Running: YES
PID: 77210
Socket: /Users/andres.camacho/Development/Personal/spacetraders/bot/var/daemon.sock
```

**Daemon List Check:**
```
Command: daemon_list()
Result: ✅ Command executed successfully
Output: "No containers running"
```

**Analysis:** Daemon server is functional and responding to MCP tool commands. The issue is not server crash or connectivity - it's container lifecycle management.

### Error Message
```
No explicit error messages generated. All failures are SILENT.
```

## Root Cause Analysis

Based on the evidence, this is a **critical daemon container lifecycle bug** with the following characteristics:

### Primary Hypothesis: Automated Container Cleanup Process Gone Wrong

**Evidence:**
1. Containers were successfully created (scout-coordinator confirmed IDs)
2. Containers transitioned to ACTIVE status (verified at 0:12)
3. Containers disappeared from database entirely within 6-12 minutes
4. No logs persisted, no error messages, no metadata
5. This matches behavior of an automated cleanup process

**Possible Mechanisms:**
- **Cleanup Cron/Timer:** Daemon may have automated cleanup job that removes containers based on age, status, or other criteria
- **Watchdog Timeout:** Containers not sending heartbeats may be assumed dead and auto-cleaned
- **Database TTL:** Container records may have time-to-live expiration
- **Failed Status Auto-Cleanup:** Containers that fail quickly may be auto-removed to prevent database bloat

### Secondary Hypothesis: Container Initialization Failure + Aggressive Cleanup

**Evidence:**
1. ENDURANCE-2 container stuck in STARTING status (previous bug report #2025-11-06_14-00)
2. ENDURANCE-1 deployed fresh but also disappeared
3. Empty logs suggest containers never successfully initialized
4. Ships may have navigated (TARS report) but containers didn't track it

**Possible Mechanism:**
- Container process starts
- Fails during initialization (config parsing, API auth, etc.)
- Exits with error code
- Daemon cleanup process sees failed container
- Removes container AND all logs to keep database clean
- No error surfaced to user

### Tertiary Hypothesis: Player ID / Agent Symbol Mismatch

**Evidence:**
1. ship_info cannot find ENDURANCE-1 or ENDURANCE-2 for player_id 2
2. Previous bug reports reference agent CHROMESAMURAI with player_id 2
3. Scout-coordinator deployed containers but ship lookups fail
4. Containers may have failed auth checks due to player mismatch

**Possible Mechanism:**
- Scout-coordinator deployed containers with incorrect player_id
- Containers attempted API calls with wrong credentials
- API rejected requests, containers failed
- Cleanup process removed failed containers

### Supporting Evidence from Previous Bug Reports

**Bug #2025-11-06_14-00 (scout-container-stuck-starting.md):**
- ENDURANCE-2 container stuck in STARTING for 18+ minutes
- JSON corruption error at character 7621
- Container never transitioned to RUNNING
- Same container ID: `scout-tour-endurance-2-a0f051f8`

**Correlation:** The ENDURANCE-2 container that was stuck in STARTING at check-in #1 (0:06) is the SAME container from the previous bug report. It was never successfully started, remained corrupted, and was eventually cleaned up.

**Bug #2025-11-06 (contract-negotiation-zero-success.md):**
- Contract operations failing silently (0/3 success)
- No error messages displayed
- Operations complete without exceptions but with zero results
- Similar pattern of silent failures

**Correlation:** Both contract operations and scout operations exhibit silent failure patterns, suggesting a systemic issue with error handling or logging configuration.

### Confidence Assessment

**HIGH CONFIDENCE (80%):** Automated cleanup process is removing failed containers without preserving logs or error state.

**MEDIUM CONFIDENCE (60%):** Container initialization failures are root cause of cleanup triggers.

**LOW CONFIDENCE (30%):** Player ID mismatch is causing auth failures (ship_info failures suggest data inconsistency but scout-coordinator wouldn't deploy if ships weren't found).

## Potential Fixes

### Fix 1: Disable or Throttle Container Cleanup Process (IMMEDIATE)
**Rationale:** Preserve evidence of failures to enable debugging. Cleanup process is destroying critical diagnostic data.

**Implementation:**
- Locate cleanup cron/timer in daemon server code
- Disable automatic cleanup OR increase TTL to hours instead of minutes
- Add configuration flag to control cleanup behavior
- Preserve failed containers and logs for at least 1 hour

**Risk:** LOW - Temporarily increased database size
**Time:** 30 minutes to locate and disable cleanup code
**Blocks Operations:** NO - Can be done while testing other fixes

### Fix 2: Add Container Failure State Preservation (DEFENSIVE)
**Rationale:** Failed containers should be marked FAILED, not deleted. Logs should be preserved.

**Implementation:**
- When container fails, update status to FAILED
- Preserve container metadata (iterations, restarts, timestamps)
- Keep last 100 log entries even for failed containers
- Add failure_reason field to container record
- Only cleanup containers older than 24 hours OR on explicit user command

**Risk:** LOW - Improves observability
**Time:** 1-2 hours development
**Blocks Operations:** NO - Defensive improvement

### Fix 3: Verify Player ID / Ship Lookups (DIAGNOSTIC)
**Rationale:** Resolve ship_info failures to confirm ship navigation occurred and player_id is correct.

**Implementation:**
- Query database directly for agent ENDURANCE
- Get correct player_id for ENDURANCE
- Verify ship symbols (may be ENDURANCE-ENDURANCE-1, etc.)
- Retry ship_info with correct player_id and ship symbols
- Update scout-coordinator if player_id mapping is wrong

**Risk:** LOW - Diagnostic only
**Time:** 15 minutes investigation
**Blocks Operations:** MAYBE - If player_id is wrong, ALL operations fail

### Fix 4: Add Container Heartbeat/Health Checks (DEFENSIVE)
**Rationale:** Know when containers are alive vs. dead before cleanup.

**Implementation:**
- Containers send heartbeat every 30 seconds
- Daemon tracks last_heartbeat timestamp
- Cleanup only removes containers with no heartbeat for 10+ minutes
- Log warning when container stops sending heartbeats
- Surface heartbeat failures to user

**Risk:** LOW - Adds monitoring capability
**Time:** 2-3 hours development
**Blocks Operations:** NO - Future improvement

### Fix 5: Test Scout Operations with Debug Logging (IMMEDIATE)
**Rationale:** Capture container output before cleanup occurs.

**Implementation:**
- Deploy single scout operation with container ID
- Immediately tail container logs in real-time
- Watch daemon_inspect in loop to track status transitions
- Capture exact moment of failure or cleanup
- Document complete lifecycle

**Risk:** NONE - Diagnostic test
**Time:** 10 minutes setup, 45 minutes observation
**Blocks Operations:** YES - Requires active monitoring

## Recommended Investigation Steps

### CRITICAL - Immediate Actions (Next 10 Minutes):

1. **Verify Player ID and Ship Names:**
   ```bash
   # Query database for agent ENDURANCE
   sqlite3 /path/to/database.db "SELECT player_id, agent_symbol FROM players WHERE agent_symbol LIKE '%ENDURANCE%';"

   # List all ships for found player_id
   sqlite3 /path/to/database.db "SELECT symbol FROM ships WHERE player_id = <PLAYER_ID>;"
   ```

2. **Check Daemon Cleanup Configuration:**
   - Search daemon server code for cleanup timers, cron jobs, or TTL settings
   - Look for AUTO DELETE, CLEANUP, or EXPIRE logic in container management
   - Document current cleanup behavior

3. **Test Single Scout Deployment with Monitoring:**
   - Deploy one scout operation
   - Monitor in real-time with 30-second polling:
     ```bash
     while true; do
       echo "=== $(date) ==="
       daemon_list
       daemon_inspect <CONTAINER_ID>
       ship_info <SHIP_SYMBOL> --player-id <PLAYER_ID>
       sleep 30
     done
     ```
   - Capture exact moment containers disappear

### HIGH PRIORITY - Diagnostic Actions (Next 30 Minutes):

4. **Examine Database Schema:**
   - Check if containers table has TTL column
   - Check for triggers on container state changes
   - Look for cascade deletes tied to ship or player records

5. **Review Daemon Server Logs (Not Container Logs):**
   - Check daemon server process logs (not container operation logs)
   - Look for cleanup events, deletion logs, or error handling
   - May be in separate log file from container logs

6. **Test Contract Operations:**
   - Verify if contract-coordinator operations exhibit same cleanup behavior
   - If contracts also cleaned up silently, confirms systemic issue
   - If contracts persist, issue is scout-specific

### MEDIUM PRIORITY - Code Review (Next 60 Minutes):

7. **Audit Container Lifecycle Code:**
   - Review container creation, status transitions, and cleanup logic
   - Search for automatic removal triggers
   - Verify error handling preserves logs and state

8. **Check Scout-Coordinator Logic:**
   - Verify scout-coordinator properly sets player_id
   - Confirm ship lookups succeed before deployment
   - Test if scout-coordinator handles deployment failures gracefully

## Temporary Workarounds

### Workaround 1: Manual Scout Operations (NOT VIABLE)
**Issue:** Containers are being auto-removed regardless of how they're deployed.
**Verdict:** NO WORKAROUND - Scout operations are completely blocked.

### Workaround 2: Use Alternative Revenue Operations
While investigating scout failures:
- **Market Trading:** Manual buy/sell operations (if working)
- **Surveying:** Mining/surveying operations (if working)
- **Contracts:** Contract fulfillment (BLOCKED - separate bug #2025-11-06)

**Verdict:** LIMITED - Most automated operations may be affected.

### Workaround 3: Increase Monitoring Frequency
- Poll daemon_list() every 60 seconds instead of 6 minutes
- Catch container failures faster
- Manually redeploy when containers disappear
- Requires active monitoring (defeats AFK mode)

**Verdict:** DEFEATS PURPOSE - AFK mode requires unattended operation.

## Environment
- **Agent:** ENDURANCE (assumed - verification needed)
- **Player ID:** 2 (assumed - verification needed)
- **System:** X1-HZ85
- **Ships Involved:**
  - ENDURANCE-1 (40-cargo hauler)
  - ENDURANCE-2 (solar probe, 0 fuel capacity)
- **Container IDs:**
  - scout-tour-endurance-1-9b7931db (deployed 0:12, removed by 0:18)
  - scout-tour-endurance-2-a0f051f8 (existed 0:06, removed by 0:18)
- **Daemon Server:**
  - Status: RUNNING
  - PID: 77210
  - Socket: /Users/andres.camacho/Development/Personal/spacetraders/bot/var/daemon.sock
- **MCP Tools Used:**
  - daemon_list
  - daemon_inspect (failed - containers not found)
  - daemon_logs (succeeded but empty)
  - ship_info (failed - ships not found for player_id 2)
- **Operation Context:** AFK mode, minute 18/60
- **Expected Runtime:** 35-45 minutes per scout operation
- **Actual Runtime:** <6 minutes before cleanup
- **Revenue Generated:** 0 credits

## Related Bug Reports
1. **2025-11-06_14-00_scout-container-stuck-starting.md** - ENDURANCE-2 container stuck in STARTING with JSON corruption (SAME CONTAINER)
2. **2025-11-06_contract-negotiation-zero-success.md** - Contract operations failing silently with zero results (SIMILAR PATTERN)
3. **2025-11-06_integration-failures-afk-mode.md** - (referenced but not read - may contain additional context)

## Critical Questions for Admiral

When Admiral returns (42 minutes remaining), TARS needs answers:

1. **What is the correct player_id for agent ENDURANCE?**
   - MCP tools expect player_id 2 but ship lookups fail
   - Is ENDURANCE a different player than CHROMESAMURAI?

2. **What are the actual ship symbols for ENDURANCE fleet?**
   - TARS reports ENDURANCE-1 and ENDURANCE-2
   - MCP cannot find these ships for player_id 2
   - Are they prefixed? (e.g., ENDURANCE-ENDURANCE-1?)

3. **Is there automated container cleanup configured?**
   - Containers disappearing in 6-12 minutes
   - No logs or error state preserved
   - Is this intentional behavior or bug?

4. **Are ANY daemon operations working?**
   - Scouts: FAILED (this bug)
   - Contracts: FAILED (bug #2025-11-06)
   - Other operations: UNKNOWN

5. **Should TARS halt all AFK operations until fixes deployed?**
   - Zero revenue in 18 minutes
   - 42 minutes remaining in AFK session
   - Risk of wasted time vs. potential for partial recovery

## Verdict: Operations Status

**SCOUT OPERATIONS: COMPLETELY BLOCKED**
- Severity: CRITICAL
- All scout deployments fail within minutes
- Silent cleanup prevents debugging
- No workaround available

**CONTRACT OPERATIONS: COMPLETELY BLOCKED**
- Severity: HIGH (separate bug report)
- 0% negotiation success rate
- Silent failures

**OVERALL DAEMON VIABILITY: UNKNOWN**
- At least 2 operation types confirmed broken
- Systemic issues with error handling and cleanup
- Recommend HALT all AFK operations until fixes confirmed

**RECOMMENDED ACTION:**
1. IMMEDIATE: Verify player_id and ship names
2. IMMEDIATE: Disable container cleanup process
3. IMMEDIATE: Test single scout operation with real-time monitoring
4. Within 30 minutes: Determine if ANY operations are viable
5. Report back to TARS with viability assessment

**TIME CRITICAL:** Admiral returns in 42 minutes. If no operations are viable, AFK mode should be aborted to prevent wasted time.
