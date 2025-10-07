# SpaceTraders MCP Server

Model Context Protocol (MCP) server that exposes SpaceTraders bot operations as tools for Claude and other MCP clients.

> **Update (Node.js Conversion)**
>
> The actively maintained MCP servers now live under `mcp/`:
> - `mcp/api/` exposes the public SpaceTraders API.
> - `mcp/bot/` (this document) exposes the SpaceTraders bot automation. Install
>   dependencies with `npm install`, build with `npm run build`, and point your
>   MCP client at `node /path/to/spacetradersV2/mcp/bot/build/index.js`.
>
> The server still relies on the Python bot code (`spacetraders_bot.py` and
> `bot/mcp_bridge.py`), so keep the Python dependencies from
> `bot/requirements.txt` installed as well.

## What is MCP?

The Model Context Protocol (MCP) is an open protocol that standardizes how applications provide context to LLMs. This MCP server allows Claude (and other MCP clients) to interact with your SpaceTraders bot through a standardized interface.

## Installation

### 1. Install dependencies

```bash
cd /Users/YOUR_USERNAME/Development/Personal/spacetradersV2/mcp/bot
npm install
npm run build
```

Also ensure the Python bot dependencies are installed:

```bash
cd /Users/YOUR_USERNAME/Development/Personal/spacetradersV2/bot
pip install -r requirements.txt
```

### 2. Configure Claude Desktop

Add the server to your Claude Desktop configuration file:

**macOS**: `~/Library/Application Support/Claude/claude_desktop_config.json`
**Windows**: `%APPDATA%\Claude\claude_desktop_config.json`

```json
{
  "mcpServers": {
    "spacetraders-bot": {
      "command": "node",
      "args": [
        "/Users/YOUR_USERNAME/Development/Personal/spacetradersV2/mcp/bot/build/index.js"
      ],
      "env": {
        "SPACETRADERS_TOKEN": "YOUR_AGENT_TOKEN_HERE"
      }
    }
  }
}
```

**Replace**:
- `/Users/YOUR_USERNAME/...` with your actual path to `build/index.js`
- `YOUR_AGENT_TOKEN_HERE` with your SpaceTraders agent token (optional, can pass via tool args)

### 3. Restart Claude Desktop

Restart Claude Desktop to load the new MCP server.

## Available Tools

### Fleet Management

#### `bot_fleet_status`
Check agent and ship status.

**Parameters**:
- `token` (required): Agent authentication token
- `ships` (optional): Comma-separated ship symbols

**Example**:
```json
{
  "token": "YOUR_TOKEN",
  "ships": "SHIP-1,SHIP-2"
}
```

#### `bot_fleet_monitor`
Monitor fleet continuously with periodic checks.

**Parameters**:
- `player_id` (required): Player ID
- `ships` (required): Ships to monitor
- `interval` (optional): Check interval in minutes (default: 5)
- `duration` (optional): Number of checks (default: 12)

---

### Mining Operations

#### `bot_run_mining`
Start autonomous mining operation.

**Parameters**:
- `player_id` (required): Player ID
- `ship` (required): Ship symbol
- `asteroid` (required): Asteroid waypoint
- `market` (required): Market waypoint
- `cycles` (optional): Number of cycles (default: 30)

**Example**:
```json
{
  "player_id": 1,
  "ship": "SHIP-3",
  "asteroid": "X1-HU87-B9",
  "market": "X1-HU87-B7",
  "cycles": 50
}
```

---

### Trading Operations

#### `bot_run_trading`
Execute trading route.

**Parameters**:
- `player_id` (required): Player ID
- `ship` (required): Ship symbol
- `good` (required): Trade good symbol
- `buy_from` (required): Buy waypoint
- `sell_to` (required): Sell waypoint
- `duration` (optional): Duration in hours (default: 1.0)
- `min_profit` (optional): Min profit per trip (default: 5000)

**Example**:
```json
{
  "player_id": 1,
  "ship": "SHIP-1",
  "good": "SHIP_PARTS",
  "buy_from": "X1-HU87-D42",
  "sell_to": "X1-HU87-A2",
  "duration": 2.0,
  "min_profit": 150000
}
```

#### `bot_trade_plan`
Run the multi-leg optimizer in analysis mode and return a proposed route without starting any daemons.

**Parameters**:
- `player_id` (required): Player ID
- `ship` (required): Ship symbol
- `system` (optional): System symbol to analyze (defaults to ship's current system)
- `max_stops` (optional): Maximum number of stops to consider (default: 4)

#### `bot_purchase_ship`
Purchase ships from a shipyard using an existing hauler.

**Parameters**:
- `player_id` (required): Player ID
- `ship` (required): Ship symbol that will perform the purchase
- `shipyard` (required): Shipyard waypoint symbol
- `ship_type` (required): Ship type to buy
- `quantity` (optional): Number of ships to buy (default: 1)
- `max_budget` (required): Maximum credits to spend across all purchases

#### `bot_multileg_trade`
Run the multi-leg trading optimizer to plan the route, execute each leg, and summarize the profit.

**Parameters**:
- `player_id` (required): Player ID
- `ship` (required): Ship symbol
- `system` (required): System symbol
- `max_stops` (optional): Maximum stops to consider (default: 4)

---

### Market Intelligence

#### `bot_scout_markets`
Scout all markets in a system with optimized routing.

**Parameters**:
- `player_id` (required): Player ID
- `ship` (required): Ship symbol (use probe for 0 fuel)
- `system` (required): System symbol
- `algorithm` (optional): "greedy" or "2opt" (default: greedy)
- `return_to_start` (optional): Return to start (default: false)

#### `bot_negotiate_contract`
Negotiate a new contract.

**Parameters**:
- `player_id` (required): Player ID
- `ship` (required): Ship symbol

#### `bot_fulfill_contract`
Fulfill an accepted contract.

**Parameters**:
- `player_id` (required): Player ID
- `ship` (required): Ship symbol
- `contract_id` (required): Contract ID
- `buy_from` (optional): Waypoint to buy from

---

### Navigation & Routing

#### `bot_build_graph`
Build navigation graph for a system.

**Parameters**:
- `player_id` (required): Player ID
- `system` (required): System symbol

#### `bot_plan_route`
Plan optimal route between waypoints.

**Parameters**:
- `player_id` (required): Player ID
- `ship` (required): Ship symbol
- `system` (required): System symbol
- `start` (required): Starting waypoint
- `goal` (required): Destination waypoint

---

### Daemon Management

#### `bot_daemon_start`
Start operation as background daemon.

**Parameters**:
- `player_id` (required): Player ID
- `operation` (required): Operation name (mine, trade, etc.)
- `daemon_id` (optional): Daemon identifier
- `args` (required): Array of operation arguments

**Example**:
```json
{
  "player_id": 1,
  "operation": "mine",
  "daemon_id": "miner-ship3",
  "args": [
    "--player-id", "1",
    "--ship", "SHIP-3",
    "--asteroid", "X1-HU87-B9",
    "--market", "X1-HU87-B7",
    "--cycles", "100"
  ]
}
```

#### `bot_daemon_stop`
Stop a running daemon.

**Parameters**:
- `player_id` (required): Player ID
- `daemon_id` (required): Daemon ID

#### `bot_daemon_status`
Get daemon status.

**Parameters**:
- `player_id` (required): Player ID
- `daemon_id` (optional): Specific daemon (omit for all)

#### `bot_daemon_logs`
Get daemon logs.

**Parameters**:
- `player_id` (required): Player ID
- `daemon_id` (required): Daemon ID
- `lines` (optional): Number of lines (default: 20)

#### `bot_daemon_cleanup`
Scan for daemons that have stopped and clean up their registry records.

**Parameters**:
- `player_id` (required): Player ID

---

### Ship Assignment Management

#### `bot_assignments_list`
List all ship assignments.

**Parameters**:
- `player_id` (required): Player ID
- `include_stale` (optional): Include stale assignments

#### `bot_assignments_assign`
Assign ship to operation.

**Parameters**:
- `player_id` (required): Player ID
- `ship` (required): Ship symbol
- `operator` (required): Operator name
- `daemon_id` (required): Associated daemon ID
- `operation` (required): Operation type
- `duration` (optional): Expected duration

#### `bot_assignments_release`
Release ship from assignment.

**Parameters**:
- `player_id` (required): Player ID
- `ship` (required): Ship symbol
- `reason` (optional): Release reason

#### `bot_assignments_find`
Find available ships.

**Parameters**:
- `player_id` (required): Player ID
- `cargo_min` (optional): Min cargo capacity
- `fuel_min` (optional): Min fuel capacity

#### `bot_assignments_sync`
Sync registry with daemons.

**Parameters**:
- `player_id` (required): Player ID

#### `bot_assignments_available`
Check whether a ship is idle and surface the operator/daemon that is holding it if busy.

**Parameters**:
- `player_id` (required): Player ID
- `ship` (required): Ship symbol

#### `bot_assignments_status`
Render the full assignment card for a ship (status badge, operator, daemon link, timestamps, live metrics).

**Parameters**:
- `player_id` (required): Player ID
- `ship` (required): Ship symbol

#### `bot_assignments_reassign`
Bulk release ships from an operation, stopping daemons unless `no_stop` is set, then mark them idle.

**Parameters**:
- `player_id` (required): Player ID
- `ships` (required): Comma-separated ship symbols
- `from_operation` (required): Operation label
- `no_stop` (optional): Skip stopping daemons
- `timeout` (optional): Shutdown timeout in seconds

#### `bot_assignments_init`
Bootstrap the assignment registry by pulling the fleet from the API and seeding idle records.

**Parameters**:
- `player_id` (required): Player ID

---

### Utilities

#### `bot_find_fuel`
Find nearest fuel station.

**Parameters**:
- `player_id` (required): Player ID
- `ship` (required): Ship symbol

#### `bot_calculate_distance`
Calculate distance between waypoints.

**Parameters**:
- `player_id` (required): Player ID
- `waypoint1` (required): First waypoint
- `waypoint2` (required): Second waypoint

#### `bot_wait_minutes`
Sleep for a given number of minutes so the Flag Captain can pause between status checks.

**Parameters**:
- `minutes` (required): Minutes to wait (capped at 60)
- `reason` (optional): Note describing why the pause is needed

---

## Usage Examples

### Example 1: Check Fleet Status

In Claude Desktop with MCP server configured:

```
User: Check my SpaceTraders fleet status

Claude: [Uses bot_fleet_status tool]
```

### Example 2: Start Mining Operation

```
User: Start mining operation with SHIP-3 at asteroid X1-HU87-B9, selling at X1-HU87-B7

Claude: [Uses bot_run_mining tool with appropriate parameters]
```

### Example 3: Scout Markets and Find Best Trade Route

```
User: Scout all markets in X1-HU87 system using my probe ship, then analyze the data to find the best trade route

Claude:
1. [Uses bot_scout_markets to gather data]
2. [Reports findings]
```

### Example 4: Background Mining with Daemon

```
User: Start background mining daemon for SHIP-3 that runs for 100 cycles

Claude: [Uses bot_daemon_start with mine operation]
```

Later:

```
User: Check status of the mining daemon

Claude: [Uses bot_daemon_status]
```

---

## Workflow Examples

### Autonomous Fleet Management

```
User: I want to maximize profits for the next 4 hours. Check fleet status, scout markets, and set up optimal operations.

Claude will:
1. Use bot_fleet_status to check fleet
2. Use bot_scout_markets to gather intel
4. Use bot_assignments_find to find available ships
5. Start trading/mining daemons with optimal configurations
6. Monitor progress with bot_daemon_status
```

### Contract Fulfillment

```
User: Negotiate a new contract and fulfill it if profitable

Claude will:
1. Use bot_negotiate_contract
2. Analyze contract terms (ROI, profit, feasibility)
3. If good, use bot_fulfill_contract
4. Report completion and earnings
```

---

## Troubleshooting

### Server Not Appearing in Claude Desktop

1. Check `claude_desktop_config.json` syntax (valid JSON)
2. Verify absolute path to `build/index.js`
3. Ensure Python 3.10+ installed
4. Restart Claude Desktop completely
5. Check Claude Desktop logs: `~/Library/Logs/Claude/` (macOS)

### Tool Execution Failures

1. Verify `spacetraders_bot.py` works from command line
2. Check token validity
3. Ensure ship symbols are correct
4. Review daemon logs: `operations/daemons/logs/`

### Permission Errors

Ensure the `node` executable is available on your `PATH` and that your shell
user has permission to run it. The compiled `build/index.js` file does not need
explicit execute permissions since it is launched with `node`.

---

## Development

### Testing the MCP Server

```bash
npm install
npm run build
node build/index.js
```

### Adding New Tools

1. Add or update the tool definition in `src/botToolDefinitions.ts`
2. Implement the corresponding handler in `src/index.ts`
3. Run `npm run build` and test with Claude Desktop

---

## Security Notes

- **Never commit tokens**: Keep tokens in environment variables or config files (gitignored)
- **Local only**: MCP server runs locally, no network exposure
- **Token access**: Anyone with access to your Claude Desktop can use your SpaceTraders token via MCP tools

---

## Links

- **MCP Documentation**: https://modelcontextprotocol.io
- **SpaceTraders API**: https://docs.spacetraders.io
- **Bot Documentation**: See `README.md`, `CLAUDE.md`, `GAME_GUIDE.md`

---

**Happy Trading, Commander! o7** 🚀
