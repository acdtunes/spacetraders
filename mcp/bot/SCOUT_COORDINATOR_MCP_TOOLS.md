# Scout Coordinator MCP Tools

This document describes the new MCP tools added for the scout-coordinator operation.

## Overview

Three new MCP tools have been added to the bot MCP server to support multi-ship coordinated market scouting operations:

1. `bot_scout_coordinator_start` - Start a coordinated scouting operation
2. `bot_scout_coordinator_stop` - Stop a coordinated scouting operation
3. `bot_scout_coordinator_status` - Check status of a coordinated scouting operation

## Tool Details

### bot_scout_coordinator_start

**Description:** Start multi-ship continuous market scouting operation as a background daemon. Coordinates multiple ships to efficiently scout markets in a system.

**Parameters:**
- `player_id` (integer, required) - Player ID from database
- `system` (string, required) - System symbol to scout (e.g., X1-HU87)
- `ships` (string, required) - Comma-separated ship symbols to use for scouting (e.g., 'SHIP-1,SHIP-2,SHIP-3')
- `algorithm` (string, optional) - Optimization algorithm for route planning (default: '2opt'). Options: 'greedy', '2opt'

**CLI Command Executed:**
```bash
python3 spacetraders_bot.py scout-coordinator start \
  --player-id {player_id} \
  --system {system} \
  --ships {ships} \
  [--algorithm {algorithm}]
```

**Example Usage:**
```javascript
bot_scout_coordinator_start({
  player_id: 6,
  system: "X1-HU87",
  ships: "SHIP-1,SHIP-2,SHIP-3",
  algorithm: "2opt"
})
```

### bot_scout_coordinator_stop

**Description:** Stop the multi-ship scouting coordinator and all associated scout ships for a system.

**Parameters:**
- `system` (string, required) - System symbol of the coordinator to stop (e.g., X1-HU87)

**CLI Command Executed:**
```bash
python3 spacetraders_bot.py scout-coordinator stop --system {system}
```

**Example Usage:**
```javascript
bot_scout_coordinator_stop({
  system: "X1-HU87"
})
```

### bot_scout_coordinator_status

**Description:** Show the status of the scout coordinator for a system, including all active scouts.

**Parameters:**
- `system` (string, required) - System symbol to check status for (e.g., X1-HU87)

**CLI Command Executed:**
```bash
python3 spacetraders_bot.py scout-coordinator status --system {system}
```

**Example Usage:**
```javascript
bot_scout_coordinator_status({
  system: "X1-HU87"
})
```

## Implementation Details

### Files Modified

1. **`mcp/bot/src/botToolDefinitions.ts`**
   - Added three new tool definitions to the `botToolDefinitions` array

2. **`mcp/bot/src/index.ts`**
   - Added three new case handlers in the `handleBotCliTool` method switch statement
   - Each handler constructs the appropriate CLI command and executes it via `runBotCommand`

### Build Process

The MCP server was rebuilt using:
```bash
cd mcp/bot && npm run build
```

The compiled JavaScript is located in `mcp/bot/build/`.

## Integration with Scout Coordinator

The scout-coordinator CLI command supports additional subcommands not exposed as MCP tools:
- `add-ship` - Add ship to ongoing operation
- `remove-ship` - Remove ship from ongoing operation

These were intentionally excluded from the MCP tools as they are less commonly needed and can be handled through stop/start operations if necessary.

## Testing

To verify the tools are working:

1. Check that the tools are registered:
   ```bash
   grep "bot_scout_coordinator" mcp/bot/build/botToolDefinitions.js
   ```

2. Verify the handlers are implemented:
   ```bash
   grep "bot_scout_coordinator" mcp/bot/build/index.js
   ```

3. Test the underlying CLI commands:
   ```bash
   python3 spacetraders_bot.py scout-coordinator start --help
   python3 spacetraders_bot.py scout-coordinator stop --help
   python3 spacetraders_bot.py scout-coordinator status --help
   ```

## Usage Notes

- The `start` operation runs as a background daemon that continuously scouts markets
- Multiple ships coordinate to efficiently cover all markets in the system
- The coordinator uses the specified algorithm (greedy or 2opt) to optimize routes
- All operations are keyed by system symbol, so only one coordinator can run per system at a time
- Stopping the coordinator will stop all associated scout ships
