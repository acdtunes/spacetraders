# Bug Report: Scout Container Stuck at STARTING Status

**Date:** 2025-11-06 14:00
**Severity:** HIGH
**Status:** NEW
**Reporter:** Captain

## Summary
Scout container scout-tour-endurance-2-a0f051f8 deployed at minute 6 of AFK mode remains stuck in "STARTING" status for 18+ minutes. Container never transitions to RUNNING, ship never leaves headquarters, and container metadata is corrupted causing JSON parsing errors.

## Impact
- **Operations Affected:** Market intelligence gathering completely blocked, ENDURANCE-2 idle
- **Credits Lost:** Estimated 0 revenue from market tours, opportunity cost of 18+ minutes idle time
- **Duration:** 18+ minutes and ongoing
- **Workaround:** None applied - container unresponsive to inspection/logging commands

## Steps to Reproduce
1. Start daemon server
2. Deploy scout_markets operation via scout-coordinator
   - Ship: ENDURANCE-2
   - System: X1-HZ85
   - Markets: 8 waypoints (B2, C3, D4, etc.)
   - Iterations: 12
   - Container ID: scout-tour-endurance-2-a0f051f8
3. Observe container status remains "STARTING"
4. After 18+ minutes, container never transitions to "RUNNING"
5. Ship remains docked at X1-HZ85-A1 (headquarters)

## Expected Behavior
- Container should transition from "STARTING" to "RUNNING" within 1-2 minutes
- Ship should undock and begin touring markets (X1-HZ85-B2, C3, D4, etc.)
- Container should complete iterations and log progress
- daemon_inspect should return valid JSON with container metadata
- daemon_logs should show INFO/DEBUG logs of tour progress

## Actual Behavior
- Container status stuck at "STARTING" for 18+ minutes
- Ship remains DOCKED at X1-HZ85-A1 (starting waypoint)
- No market data collected
- No iterations completed
- Container metadata corrupted - JSON parsing failure
- No logs available (empty result set)

## Evidence

### Daemon Status
```
Command: daemon_inspect scout-tour-endurance-2-a0f051f8
Result: ❌ Command failed (exit code: 1)

Error Message:
❌ Error: Unterminated string starting at: line 1 column 7622 (char 7621)
```

**Analysis:** JSON parsing error at character 7621 suggests container configuration or metadata contains malformed JSON. This prevents inspection of container state including iteration count, restart count, and timestamps.

### Daemon Logs (ERROR Level)
```
Command: daemon_logs scout-tour-endurance-2-a0f051f8 --level ERROR
Result: ✅ Command executed successfully
Logs: (empty - no ERROR logs found)
```

### Daemon Logs (All Levels)
```
Command: daemon_logs scout-tour-endurance-2-a0f051f8
Result: ✅ Command executed successfully
Logs: (empty - no logs found)
```

**Analysis:** Empty log set indicates either:
1. Container never initialized logging
2. Container crashed before logging started
3. Database logging not persisting
4. Container process never actually started despite "STARTING" status

### Ship State
```
Ship: ENDURANCE-2
================================================================================
Location:       X1-HZ85-A1
System:         X1-HZ85
Status:         DOCKED

Fuel:           0/0 (0%)
Cargo:          0/0
Engine Speed:   9
```

**Analysis:** Ship remains at headquarters waypoint X1-HZ85-A1 in DOCKED status. Ship has never undocked or navigated to any market waypoints. Fuel 0/0 indicates this is a probe-type ship (no fuel required), so fuel is not blocking navigation.

### Error Message
```
Unterminated string starting at: line 1 column 7622 (char 7621)
```

## Root Cause Analysis

Based on the evidence, the root cause is a **container initialization failure with corrupted metadata**:

1. **JSON Corruption:** The daemon_inspect error "Unterminated string starting at: line 1 column 7622" indicates the container's configuration or metadata stored in the database contains malformed JSON. This suggests either:
   - Configuration serialization bug when container was created
   - Database corruption during write
   - Special characters in waypoint names/system data not properly escaped

2. **Container Never Started:** Empty logs combined with "STARTING" status for 18+ minutes indicates the container process never successfully initialized. The container is likely:
   - Stuck in a retry loop attempting to parse corrupted config
   - Deadlocked waiting for a resource
   - Failed to start but status never updated to "FAILED"

3. **Ship Never Received Commands:** ENDURANCE-2 remaining DOCKED at HQ proves the container never issued navigation commands, confirming the container process is non-functional.

**Critical Issue:** The "STARTING" status is misleading - the container has actually failed but the status was never updated to reflect this.

## Potential Fixes

### 1. Force Stop and Restart Container (IMMEDIATE)
**Rationale:** Container is non-functional and blocking operations. Stopping allows retry with fresh state.

**Steps:**
- Stop container scout-tour-endurance-2-a0f051f8
- Verify ship ENDURANCE-2 is available
- Redeploy scout_markets with same parameters
- Monitor for successful transition to RUNNING status

**Risk:** Low - container already non-functional
**Time:** 2-3 minutes

### 2. Fix JSON Serialization in Container Creation (CODE FIX)
**Rationale:** Prevent future corruption by fixing root cause in daemon creation logic.

**Areas to Investigate:**
- Container configuration serialization in daemon server
- Waypoint name escaping (special characters in X1-HZ85 waypoint names?)
- Database schema for container metadata storage
- JSON library used for serialization

**Risk:** Medium - requires code changes and testing
**Time:** 1-2 hours development + testing

### 3. Add Container Startup Timeout (DEFENSIVE)
**Rationale:** Prevent containers from staying in "STARTING" forever.

**Implementation:**
- Add 5-minute timeout for STARTING status
- Auto-transition to FAILED if timeout exceeded
- Log error message explaining why timeout occurred
- Allow automatic retry logic to kick in

**Risk:** Low - improves observability
**Time:** 30 minutes development

### 4. Add Database Validation (DEFENSIVE)
**Rationale:** Detect corrupted JSON before attempting to use it.

**Implementation:**
- Validate container config JSON after write to database
- Validate JSON before daemon_inspect attempts to parse
- Return clear error message identifying corrupted field
- Add database integrity checks to startup routine

**Risk:** Low - improves error handling
**Time:** 1 hour development

## Environment
- **Agent:** CHROMESAMURAI
- **System:** X1-HZ85
- **Ships Involved:** ENDURANCE-2
- **MCP Tools Used:** daemon_inspect, daemon_logs, ship_info
- **Container ID:** scout-tour-endurance-2-a0f051f8
- **Container Type:** scout_markets
- **Deployment Context:** AFK mode, minute 18/60, deployed at minute 6

## Recommendations

**IMMEDIATE ACTION (Next 5 Minutes):**
1. Stop container scout-tour-endurance-2-a0f051f8
2. Attempt to redeploy scout_markets with ENDURANCE-2
3. Monitor new container for successful RUNNING transition within 2 minutes
4. If second attempt fails, escalate to developer for JSON corruption investigation

**SHORT-TERM (Today):**
1. Implement container startup timeout (5 minutes)
2. Add validation for container config JSON before storage
3. Improve error messages to identify which field is corrupted

**LONG-TERM (This Week):**
1. Audit all container creation code for JSON serialization bugs
2. Add database integrity checks for container metadata
3. Implement health check endpoint for containers
4. Add automatic recovery for stuck containers

**TESTING:**
- Test scout_markets deployment with various system/waypoint configurations
- Test with waypoint names containing special characters
- Verify timeout mechanism triggers correctly
- Validate JSON schema enforcement
