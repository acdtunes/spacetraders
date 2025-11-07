# Bug Report: Scout Deployment - All Containers Controlling Same Ship

**Date:** 2025-11-07T10:05:00Z
**Severity:** HIGH
**Status:** NEW
**Reporter:** Captain

## Summary
Attempted to deploy 3 scout ships (ENDURANCE-2, ENDURANCE-3, ENDURANCE-4) for market intelligence. All 3 containers are controlling ENDURANCE-4 only, leaving ENDURANCE-2 and ENDURANCE-3 idle with no daemon control.

## Impact
- **Operations Affected:** Scout market intelligence gathering across X1-HZ85 system
- **Credits Lost:** Unknown (opportunity cost of 2 idle scouts)
- **Duration:** Ongoing since 2025-11-07 10:00 UTC (5+ minutes)
- **Workaround:** Manual container stop and redeployment required
- **Fleet Utilization:** 25% (1/4 ships working) instead of expected 75% (3/4 ships)
- **Market Coverage:** Only 1 market (X1-HZ85-C38) instead of 15 markets

## Steps to Reproduce
1. Deploy scout tour for ENDURANCE-2 with 12 markets via scout-coordinator
2. Deploy scout tour for ENDURANCE-3 with 2 markets via scout-coordinator
3. Deploy scout tour for ENDURANCE-4 with 1 market via scout-coordinator
4. Observe all 3 containers log "Starting scout tour: ENDURANCE-4 visiting 1 markets"
5. Verify ENDURANCE-2 and ENDURANCE-3 have no controlling daemon

## Expected Behavior
- Container `scout-tour-endurance-2-89ba486e` controls ENDURANCE-2, tours 12 markets
- Container `scout-tour-endurance-3-fcd6599d` controls ENDURANCE-3, tours 2 markets
- Container `scout-tour-endurance-4-812582de` controls ENDURANCE-4, tours 1 market
- Each ship has exactly 1 controlling daemon

## Actual Behavior
- Container `scout-tour-endurance-2-89ba486e` controls ENDURANCE-4, tours 1 market
- Container `scout-tour-endurance-3-fcd6599d` controls ENDURANCE-4, tours 1 market
- Container `scout-tour-endurance-4-812582de` controls ENDURANCE-4, tours 1 market
- ENDURANCE-2 and ENDURANCE-3 have NO controlling daemon
- All 3 containers execute identical operations on the same ship

## Evidence

### Container Status Summary

**Container: scout-tour-endurance-2-89ba486e**
- Status: STARTING
- Type: command
- Iterations: 0
- Restart Count: 0
- Started At: 2025-11-07T10:00:54.955140
- Ship Controlled: ENDURANCE-4 (INCORRECT - should be ENDURANCE-2)

**Container: scout-tour-endurance-3-fcd6599d**
- Status: STARTING
- Type: command
- Iterations: 0
- Restart Count: 0
- Started At: 2025-11-07T10:01:39.917626
- Ship Controlled: ENDURANCE-4 (INCORRECT - should be ENDURANCE-3)

**Container: scout-tour-endurance-4-812582de**
- Status: STARTING
- Type: command
- Iterations: 0
- Restart Count: 0
- Started At: 2025-11-07T10:02:56.341685
- Ship Controlled: ENDURANCE-4 (CORRECT)

### Daemon Logs (Identical Across All Containers)

**Container scout-tour-endurance-2-89ba486e logs:**
```
[2025-11-07T10:05:07.469594] [INFO] Starting scout tour: ENDURANCE-4 visiting 1 markets
[2025-11-07T10:05:08.461222] [INFO] Loaded graph for X1-HZ85 from database cache
[2025-11-07T10:05:08.465597] [INFO] Ship starting from X1-HZ85-C38
[2025-11-07T10:05:08.468014] [INFO] Visiting market 1/1: X1-HZ85-C38
[2025-11-07T10:05:09.355449] [INFO] Ship ENDURANCE-4 already at X1-HZ85-C38, skipping navigation
[2025-11-07T10:05:11.067844] [INFO] Ship ENDURANCE-4 already docked (idempotent)
[2025-11-07T10:05:11.913354] [INFO] ✅ Market X1-HZ85-C38: 9 goods updated
[2025-11-07T10:05:11.917525] [INFO] Tour complete: 1 markets, 9 goods, 1
```

**Container scout-tour-endurance-3-fcd6599d logs:**
```
[2025-11-07T10:05:07.470464] [INFO] Starting scout tour: ENDURANCE-4 visiting 1 markets
[2025-11-07T10:05:08.462251] [INFO] Loaded graph for X1-HZ85 from database cache
[2025-11-07T10:05:08.466340] [INFO] Ship starting from X1-HZ85-C38
[2025-11-07T10:05:08.468897] [INFO] Visiting market 1/1: X1-HZ85-C38
[2025-11-07T10:05:09.359004] [INFO] Ship ENDURANCE-4 already at X1-HZ85-C38, skipping navigation
[2025-11-07T10:05:11.069202] [INFO] Ship ENDURANCE-4 already docked (idempotent)
[2025-11-07T10:05:11.914941] [INFO] ✅ Market X1-HZ85-C38: 9 goods updated
[2025-11-07T10:05:11.918603] [INFO] Tour complete: 1 markets, 9 goods, 1
```

**Container scout-tour-endurance-4-812582de logs:**
```
[2025-11-07T10:05:07.471415] [INFO] Starting scout tour: ENDURANCE-4 visiting 1 markets
[2025-11-07T10:05:08.463126] [INFO] Loaded graph for X1-HZ85 from database cache
[2025-11-07T10:05:08.467238] [INFO] Ship starting from X1-HZ85-C38
[2025-11-07T10:05:08.469823] [INFO] Visiting market 1/1: X1-HZ85-C38
[2025-11-07T10:05:09.360383] [INFO] Ship ENDURANCE-4 already at X1-HZ85-C38, skipping navigation
[2025-11-07T10:05:11.070204] [INFO] Ship ENDURANCE-4 already docked (idempotent)
[2025-11-07T10:05:11.916495] [INFO] ✅ Market X1-HZ85-C38: 9 goods updated
[2025-11-07T10:05:11.920747] [INFO] Tour complete: 1 markets, 9 goods, 1
```

**Key Observation:** All 3 containers show IDENTICAL log messages with ENDURANCE-4 and "1 markets", despite being named for different ships.

### Ship State

**ENDURANCE-2:**
```
Location:       X1-HZ85-E42
Status:         IN_TRANSIT
Fuel:           0/0 (0%)
Cargo:          0/0
Controlling Daemon: NONE (IDLE)
```

**ENDURANCE-3:**
```
Location:       X1-HZ85-J58
Status:         IN_TRANSIT
Fuel:           0/0 (0%)
Cargo:          0/0
Controlling Daemon: NONE (IDLE)
```

**ENDURANCE-4:**
```
Location:       X1-HZ85-C38
Status:         DOCKED
Fuel:           0/0 (0%)
Cargo:          0/0
Controlling Daemon: 3 CONTAINERS (scout-tour-endurance-2-89ba486e, scout-tour-endurance-3-fcd6599d, scout-tour-endurance-4-812582de)
```

### Error Message
No ERROR level logs found in any container. The bug is silent - containers execute successfully but on the wrong ship.

## Root Cause Analysis

This bug indicates a **critical flaw in the scout-coordinator's container creation logic**. The most likely root causes:

1. **Container Configuration Reuse:** The scout-coordinator may be reusing the same command configuration object for all 3 deployments without properly updating the ship parameter. This would cause all containers to receive the last ship's configuration (ENDURANCE-4).

2. **Late Binding Issue:** The ship symbol may be captured by reference rather than by value when creating the container command. When the loop completes, all references point to the final ship (ENDURANCE-4).

3. **Container Naming vs. Ship Assignment Mismatch:** The container naming is correct (includes ship number), but the actual ScoutTourCommand parameters are incorrect. This suggests the bug is in how parameters are passed to the daemon container, not in the container naming logic.

4. **Database/State Management Issue:** Less likely, but possible that ship assignments are being overwritten in a shared state object before container creation completes.

**Evidence Supporting Root Cause #1:**
- All 3 containers show "1 markets" (ENDURANCE-4's tour length)
- All 3 containers target X1-HZ85-C38 (ENDURANCE-4's starting location)
- Container names are correct (scout-tour-endurance-{2,3,4})
- Log messages show correct container ID but wrong ship

## Potential Fixes

### Fix 1: Ensure Deep Copy of Command Parameters (RECOMMENDED)
```python
# In scout-coordinator's deployment loop:
for ship_symbol, market_tour in deployments.items():
    # Create a NEW command config for each ship (not reuse)
    command_config = {
        "ship_symbol": ship_symbol,  # Explicitly set per iteration
        "markets": market_tour.copy(),  # Deep copy the markets list
        "iterations": -1
    }
    container_id = daemon_client.create_container(
        container_type="command",
        command="ScoutTourCommand",
        config=command_config  # Use the newly created config
    )
```

**Rationale:** Ensures each container gets its own independent configuration object with the correct ship symbol and markets.

### Fix 2: Add Ship Assignment Validation
```python
# After container creation, verify ship assignment:
container_info = daemon_client.inspect_container(container_id)
actual_ship = extract_ship_from_logs(container_info)
if actual_ship != expected_ship:
    raise ValueError(f"Container {container_id} assigned to {actual_ship}, expected {expected_ship}")
```

**Rationale:** Fail fast if ship assignment is incorrect, preventing silent failures.

### Fix 3: Add Integration Test for Multi-Ship Deployment
```gherkin
Feature: Scout Coordinator Multi-Ship Deployment
  Scenario: Deploy 3 scouts with different tours
    Given 3 scout ships: ENDURANCE-2, ENDURANCE-3, ENDURANCE-4
    When scout-coordinator deploys all 3 ships
    Then container scout-tour-endurance-2 controls ENDURANCE-2
    And container scout-tour-endurance-3 controls ENDURANCE-3
    And container scout-tour-endurance-4 controls ENDURANCE-4
    And ENDURANCE-2 visits 12 markets
    And ENDURANCE-3 visits 2 markets
    And ENDURANCE-4 visits 1 market
```

**Rationale:** Prevents regression by testing the exact scenario that failed.

## Environment
- Agent: ENDURANCE
- System: X1-HZ85
- Ships Involved: ENDURANCE-2, ENDURANCE-3, ENDURANCE-4
- MCP Tools Used: daemon_inspect, daemon_logs, ship_info
- Container IDs:
  - scout-tour-endurance-2-89ba486e
  - scout-tour-endurance-3-fcd6599d
  - scout-tour-endurance-4-812582de

## Recommended Actions

1. **Immediate:** Stop all 3 containers to prevent wasted operations
2. **Investigation:** Review scout-coordinator's container creation code for configuration reuse
3. **Fix:** Implement Fix 1 (deep copy of command parameters)
4. **Testing:** Add integration test (Fix 3) to prevent regression
5. **Validation:** Redeploy scouts and verify each container controls correct ship

## Additional Context

This is a **HIGH severity** bug because:
- It silently fails (no errors logged)
- It wastes 66% of scout fleet capacity
- It prevents market intelligence gathering across the system
- It's not immediately obvious from container names that assignments are wrong
- Multiple containers operating on the same ship could cause race conditions or API rate limit issues

The bug demonstrates the importance of:
- Integration testing for multi-ship operations
- Parameter validation at container creation time
- Logging ship assignments explicitly in container startup
- Using immutable configuration objects to prevent accidental sharing
