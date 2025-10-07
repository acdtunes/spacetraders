# Ship Assignment System

Automated ship allocation and coordination to prevent conflicts and enable strategic reassignment.

## Overview

The ship assignment system manages which ships are assigned to which operations. This prevents:
- **Resource conflicts**: Two operators trying to use the same ship
- **Orphaned daemons**: Daemons running without tracking
- **Unclear fleet status**: Not knowing which ships are doing what

## Quick Start

### 1. Initialize Registry

```bash
# Create registry with all ships from API
python3 spacetraders_bot.py assignments init --token YOUR_TOKEN
```

### 2. Start an Operation with Assignment Tracking

```bash
# Start trading daemon
python3 spacetraders_bot.py daemon start trade \
  --daemon-id trader-ship1 \
  --token TOKEN \
  --ship CMDR_AC_2025-1 \
  --good SHIP_PARTS \
  --buy-from X1-HU87-D42 \
  --sell-to X1-HU87-A2 \
  --duration 4

# Register assignment
python3 spacetraders_bot.py assignments assign \
  --ship CMDR_AC_2025-1 \
  --operator trading_operator \
  --daemon-id trader-ship1 \
  --operation trade \
  --duration 4
```

### 3. Monitor Assignments

```bash
# List all assignments
python3 spacetraders_bot.py assignments list

# Check specific ship
python3 spacetraders_bot.py assignments status CMDR_AC_2025-1

# Sync with daemon status (detect crashed daemons)
python3 spacetraders_bot.py assignments sync
```

## Commands Reference

### `assignments list`

List all ship assignments

```bash
python3 spacetraders_bot.py assignments list [--include-stale]
```

**Output:**
```
====================================================================================================
SHIP ASSIGNMENTS
====================================================================================================
SHIP                 STATUS       OPERATOR                  DAEMON                    OPERATION
----------------------------------------------------------------------------------------------------
CMDR_AC_2025-1       ✅ active    trading_operator          trader-ship1              trade
CMDR_AC_2025-2       ✅ active    market_analyst            market-scout              scout-markets
CMDR_AC_2025-3       ✅ active    mining_operator           miner-ship3               mine
CMDR_AC_2025-6       ⚪ idle      none                      none                      none
====================================================================================================
```

### `assignments assign`

Assign ship to operation

```bash
python3 spacetraders_bot.py assignments assign \
  --ship SHIP_SYMBOL \
  --operator OPERATOR_NAME \
  --daemon-id DAEMON_ID \
  --operation OPERATION_TYPE \
  [--duration HOURS]
```

**Example:**
```bash
python3 spacetraders_bot.py assignments assign \
  --ship CMDR_AC_2025-1 \
  --operator trading_operator \
  --daemon-id trader-ship1 \
  --operation trade \
  --duration 4
```

### `assignments release`

Release ship from assignment

```bash
python3 spacetraders_bot.py assignments release SHIP_SYMBOL [--reason REASON]
```

**Example:**
```bash
python3 spacetraders_bot.py assignments release CMDR_AC_2025-1 --reason "operation_complete"
```

### `assignments available`

Check if ship is available

```bash
python3 spacetraders_bot.py assignments available SHIP_SYMBOL
```

**Output (available):**
```
✅ CMDR_AC_2025-6 is available
```

**Output (assigned):**
```
❌ CMDR_AC_2025-1 is currently assigned:
   Operator: trading_operator
   Daemon: trader-ship1
   Operation: trade
   Assigned at: 2025-10-05T10:00:00.000000
```

### `assignments find`

Find available ships

```bash
python3 spacetraders_bot.py assignments find [--cargo-min MIN] [--fuel-min MIN]
```

**Example:**
```bash
python3 spacetraders_bot.py assignments find --cargo-min 40
```

**Output:**
```
📡 Available ships (2):
  • CMDR_AC_2025-1
  • CMDR_AC_2025-6
```

### `assignments sync`

Sync registry with daemon status (detect crashed daemons)

```bash
python3 spacetraders_bot.py assignments sync
```

**Output:**
```
🔄 Synchronizing ship assignments with daemon status...

✅ Sync complete:
   Released (daemon stopped): 1 ships
   Still active: 3 ships

   Released ships:
     • CMDR_AC_2025-5
```

### `assignments reassign`

Reassign ships from one operation (strategic shift)

```bash
python3 spacetraders_bot.py assignments reassign \
  --ships SHIP1,SHIP2,SHIP3 \
  --from-operation OPERATION \
  [--no-stop] \
  [--timeout SECONDS]
```

**Example (stop all mining, switch to trading):**
```bash
python3 spacetraders_bot.py assignments reassign \
  --ships CMDR_AC_2025-3,CMDR_AC_2025-4,CMDR_AC_2025-5 \
  --from-operation mine \
  --timeout 15
```

### `assignments status`

Get detailed status for specific ship

```bash
python3 spacetraders_bot.py assignments status SHIP_SYMBOL
```

**Output:**
```
======================================================================
SHIP ASSIGNMENT STATUS: CMDR_AC_2025-1
======================================================================

Status: ✅ ACTIVE
Operator: trading_operator
Daemon: trader-ship1
Operation: trade
Assigned at: 2025-10-05T10:00:00.000000

Metadata:
  duration: 4

Daemon Status:
  Running: ✅ Yes
  PID: 12345
  Runtime: 3600s
  CPU: 2.5%
  Memory: 45.3 MB
======================================================================
```

### `assignments init`

Initialize registry from API (fetch all ships)

```bash
python3 spacetraders_bot.py assignments init --token YOUR_TOKEN
```

## Workflow Examples

### Example 1: Start Trading Operation

```bash
# 1. Check ship availability
python3 spacetraders_bot.py assignments available CMDR_AC_2025-1
# ✅ CMDR_AC_2025-1 is available

# 2. Start trading daemon
python3 spacetraders_bot.py daemon start trade \
  --daemon-id trader-ship1 \
  --token TOKEN \
  --ship CMDR_AC_2025-1 \
  --good SHIP_PARTS \
  --buy-from X1-HU87-D42 \
  --sell-to X1-HU87-A2 \
  --duration 4

# 3. Register assignment
python3 spacetraders_bot.py assignments assign \
  --ship CMDR_AC_2025-1 \
  --operator trading_operator \
  --daemon-id trader-ship1 \
  --operation trade \
  --duration 4

# 4. Verify assignment
python3 spacetraders_bot.py assignments status CMDR_AC_2025-1
```

### Example 2: Strategic Shift (Mining → Trading)

```bash
# 1. List current assignments
python3 spacetraders_bot.py assignments list
# Shows: Ships 3,4,5 mining

# 2. Reassign miners to idle (stops daemons)
python3 spacetraders_bot.py assignments reassign \
  --ships CMDR_AC_2025-3,CMDR_AC_2025-4,CMDR_AC_2025-5 \
  --from-operation mine

# 3. Find available ships
python3 spacetraders_bot.py assignments find
# Shows: Ships 3,4,5 now available

# 4. Start trading daemons for each
for ship in CMDR_AC_2025-3 CMDR_AC_2025-4 CMDR_AC_2025-5; do
  daemon_id="trader-${ship: -1}"

  # Start daemon
  python3 spacetraders_bot.py daemon start trade \
    --daemon-id $daemon_id \
    --token TOKEN \
    --ship $ship \
    --good IRON \
    --buy-from X1-HU87-B7 \
    --sell-to X1-HU87-A1 \
    --duration 6

  # Register assignment
  python3 spacetraders_bot.py assignments assign \
    --ship $ship \
    --operator trading_operator \
    --daemon-id $daemon_id \
    --operation trade
done

# 5. Verify all assignments
python3 spacetraders_bot.py assignments list
```

### Example 3: Periodic Monitoring (Sync with Daemons)

```bash
# Run every 5 minutes to detect crashed daemons
while true; do
  echo "=== $(date) ==="

  # Sync registry with daemon status
  python3 spacetraders_bot.py assignments sync

  # List all assignments
  python3 spacetraders_bot.py assignments list

  sleep 300  # 5 minutes
done
```

### Example 4: Clean Stop All Operations

```bash
# 1. Get all active ships
python3 spacetraders_bot.py assignments list | grep "✅ active"

# 2. Stop each operation gracefully
python3 spacetraders_bot.py daemon status  # List all daemons

# Stop each daemon
for daemon_id in trader-ship1 miner-ship3 miner-ship4; do
  python3 spacetraders_bot.py daemon stop $daemon_id
done

# 3. Sync to update registry
python3 spacetraders_bot.py assignments sync

# 4. Verify all idle
python3 spacetraders_bot.py assignments list
```

## Integration with Specialist Agents

### Ship Assignment Specialist Agent

Use this as part of your agent hierarchy:

**Responsibilities:**
1. Maintain ship registry
2. Grant/deny ship requests from operators
3. Handle reassignments from Captain/First Mate
4. Sync with daemon status periodically
5. Prevent double-booking

**Agent Usage:**

```python
# In specialist agent code
from assignment_manager import AssignmentManager

manager = AssignmentManager()

# Request ship
if manager.is_available("CMDR_AC_2025-1"):
    # Assign ship
    manager.assign(
        ship="CMDR_AC_2025-1",
        operator="trading_operator",
        daemon_id="trader-ship1",
        operation="trade",
        metadata={"route": "SHIP_PARTS D42->A2"}
    )
else:
    # Ship not available
    assignment = manager.get_assignment("CMDR_AC_2025-1")
    print(f"Ship assigned to {assignment['assigned_to']}")

# Release ship when done
manager.release("CMDR_AC_2025-1", reason="operation_complete")

# Periodic sync (every 5 min)
manager.sync_with_daemons()
```

## Registry File Format

**Location:** `agents/cmdr_ac_2025/ship_assignments.json`

```json
{
  "CMDR_AC_2025-1": {
    "assigned_to": "trading_operator",
    "daemon_id": "trader-ship1",
    "operation": "trade",
    "status": "active",
    "assigned_at": "2025-10-05T10:00:00.000000",
    "metadata": {
      "duration": 4,
      "frame": "FRAME_FRIGATE",
      "cargo_capacity": 40,
      "fuel_capacity": 400
    }
  },
  "CMDR_AC_2025-2": {
    "assigned_to": "market_analyst",
    "daemon_id": "market-scout",
    "operation": "scout-markets",
    "status": "active",
    "assigned_at": "2025-10-05T09:30:00.000000",
    "metadata": {
      "frame": "FRAME_PROBE",
      "cargo_capacity": 0,
      "fuel_capacity": 100
    }
  },
  "CMDR_AC_2025-6": {
    "assigned_to": null,
    "daemon_id": null,
    "operation": null,
    "status": "idle",
    "released_at": "2025-10-05T12:00:00.000000",
    "release_reason": "operation_complete",
    "metadata": {
      "frame": "FRAME_FRIGATE",
      "cargo_capacity": 40,
      "fuel_capacity": 400
    }
  }
}
```

## Status Values

- **`active`**: Ship assigned and daemon running
- **`idle`**: Ship available for assignment
- **`stale`**: Assignment exists but daemon stopped (needs cleanup)

## Best Practices

1. **Always sync before assigning**: Run `assignments sync` to detect stale assignments
2. **Check availability first**: Use `assignments available` before starting daemons
3. **Register immediately**: Assign ship right after starting daemon
4. **Release when done**: Always release ships when operations complete
5. **Periodic sync**: Run `assignments sync` every 5-10 minutes to detect crashes
6. **Use reassign for strategy shifts**: Clean way to move ships between operations
7. **Initialize on startup**: Run `assignments init` when setting up fleet

## Troubleshooting

### Ship shows "assigned" but daemon not running

```bash
# Sync to detect and fix
python3 spacetraders_bot.py assignments sync

# Manually release if needed
python3 spacetraders_bot.py assignments release SHIP_SYMBOL --reason "stale_daemon"
```

### Can't assign ship (already assigned)

```bash
# Check current assignment
python3 spacetraders_bot.py assignments status SHIP_SYMBOL

# Stop daemon and release
python3 spacetraders_bot.py daemon stop DAEMON_ID
python3 spacetraders_bot.py assignments release SHIP_SYMBOL
```

### Registry out of sync with daemons

```bash
# Full sync
python3 spacetraders_bot.py assignments sync

# Verify
python3 spacetraders_bot.py assignments list
python3 spacetraders_bot.py daemon status
```

### Need to reset registry

```bash
# Backup current registry
cp agents/cmdr_ac_2025/ship_assignments.json agents/cmdr_ac_2025/ship_assignments.json.backup

# Reinitialize
python3 spacetraders_bot.py assignments init --token YOUR_TOKEN

# Stop all daemons
python3 spacetraders_bot.py daemon status
# Stop each one manually

# Sync
python3 spacetraders_bot.py assignments sync
```

## See Also

- **Daemon Management**: `python3 spacetraders_bot.py daemon --help`
- **Fleet Status**: `python3 spacetraders_bot.py status --help`
- **CLAUDE.md**: General bot usage and game mechanics
