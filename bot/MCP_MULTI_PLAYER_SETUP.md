# MCP Server Multi-Player Setup Guide

This guide shows how to configure the SpaceTraders MCP server for **multiple players** using the shared database system.

> **Update:** Use the Node.js/TypeScript MCP bot server in `mcp/bot/` for all new
> setups (`npm install`, `npm run build`, then `node .../mcp/bot/build/index.js`).
> The legacy Python steps below are retained only for historical reference.

## Prerequisites

- Python 3.10 or higher
- Claude Desktop installed
- SpaceTraders API token(s)

## Step 1: Install Dependencies

```bash
cd /Users/andres.camacho/Development/Personal/spacetradersV2/bot

# Install required packages
pip install -r requirements.txt
```

This installs:
- `requests` - HTTP client for SpaceTraders API
- `python-dateutil` - Date/time utilities
- `mcp` - Model Context Protocol SDK
- `psutil` - Process management

## Step 2: Configure Claude Desktop

### Find Your Config File

**macOS**:
```bash
# Open config directory
open ~/Library/Application\ Support/Claude/
# Edit: claude_desktop_config.json
```

**Windows**:
```
%APPDATA%\Claude\claude_desktop_config.json
```

### Add MCP Server Configuration

Edit `claude_desktop_config.json` and add the SpaceTraders bot server:

```json
{
  "mcpServers": {
    "spacetraders-bot": {
      "command": "node",
      "args": [
        "/Users/andres.camacho/Development/Personal/spacetradersV2/mcp/bot/build/index.js"
      ]
    }
  }
}
```

**Important**:
- Replace the path with your actual absolute path to `build/index.js`
- Do **NOT** include tokens or player_id in the config (multi-player support)

## Step 3: Restart Claude Desktop

1. Quit Claude Desktop completely
2. Relaunch Claude Desktop
3. Check for MCP server in the bottom-right corner (🔌 icon)

## Step 4: Register Players

Each player must be registered once in the database. Use Claude to do this:

### Register First Player

In Claude Desktop:
```
Register me as a SpaceTraders player:
- Agent Symbol: CMDR_AC_2025
- Token: eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9...
```

Claude will use `bot_register_player` and return:
```
✅ Player registered successfully!

Player ID: 1
Agent Symbol: CMDR_AC_2025
Token stored: eyJhbGciOi...VDs1FynSq9

Use player_id=1 for all operations.
```

### Register Additional Players

```
Register another player:
- Agent Symbol: EXPLORER_BOT
- Token: eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9...
```

Returns:
```
✅ Player registered successfully!

Player ID: 2
Agent Symbol: EXPLORER_BOT
Token stored: eyJhbGciOi...Abc123XyZ

Use player_id=2 for all operations.
```

## Step 5: List Players

To see all registered players:

```
Show me all registered SpaceTraders players
```

Claude will use `bot_list_players` and show:
```
Registered Players:

• Player ID 1: CMDR_AC_2025
  Registered: 2025-10-05 14:30:00
  Last Active: 2025-10-05 16:45:00

• Player ID 2: EXPLORER_BOT
  Registered: 2025-10-05 15:00:00
  Last Active: 2025-10-05 16:30:00
```

## Step 6: Use Player-Specific Operations

Now Claude can execute operations for any player:

### Check Status for Player 1

```
Check fleet status for player 1
```

### Start Mining for Player 2

```
Start mining operation for player 2:
- Ship: SHIP-3
- Asteroid: X1-HU87-B9
- Market: X1-HU87-B7
- Cycles: 50
```

### Multi-Player Scenario

```
Player 1 wants to trade IRON_ORE from X1-HU87-D42 to X1-HU87-A2 using SHIP-1
Player 2 wants to mine at X1-HU87-B9 with SHIP-3

Start both operations as daemons
```

Claude will:
1. Identify player_id from context (1 and 2)
2. Start daemon for player 1's trading operation
3. Start daemon for player 2's mining operation
4. Report both daemon IDs

## How It Works

### Player Identification Flow

1. **User says who they are:**
   ```
   I'm CMDR_AC_2025, check my fleet status
   ```

2. **Claude looks up player:**
   - Uses `bot_get_player_info` with agent_symbol
   - Gets player_id (e.g., 1)

3. **Claude executes operation:**
   - Uses `bot_fleet_status` with player_id=1
   - Returns player 1's fleet status

### Token Management

- **Tokens stored in database**: Each player's token is stored when they register
- **Automatic retrieval**: Operations automatically use the correct token for each player_id
- **No manual token passing**: Users never need to provide tokens after registration

### Data Isolation

- **Per-Player Data**: Ships, daemons, assignments are isolated by player_id
- **Shared Data**: System graphs and market data are shared across all players
- **No Conflicts**: Player 1's SHIP-1 is completely separate from Player 2's SHIP-1

## Example Workflows

### Onboarding New Player

```
I'm a new SpaceTraders player:
- Agent: NEW_EXPLORER
- Token: eyJhbGciOi...xyz123

Register me and check my fleet status
```

Claude will:
1. Register player → Get player_id (e.g., 3)
2. Check status using player_id=3
3. Show fleet information

### Multi-Player Fleet Management

```
Show me the status of all players and their active operations
```

Claude will:
1. List all players
2. For each player, check daemon status
3. Show comprehensive multi-player overview

### Cross-Player Market Intelligence

```
Player 1 scouted markets in X1-HU87.
Player 2 wants to use that data to find best trade routes.
Help player 2 set up a trading operation.
```

Claude will:
1. Load shared market data (from player 1's scouting)
2. Evaluate profitable routes for player 2
3. Start trading daemon with player 2's player_id

## Available Tools

### Player Management
- `bot_list_players` - List all registered players
- `bot_register_player` - Register new player
- `bot_get_player_info` - Get player details by ID or agent symbol

### Fleet Operations
- `bot_fleet_status` - Check fleet status (requires player_id)
- `bot_fleet_monitor` - Monitor fleet continuously (requires player_id)

### Mining/Trading/Contracts
- `bot_run_mining` - Mining operations (requires player_id)
- `bot_run_trading` - Trading operations (requires player_id)
- `bot_trade_plan` - Analyze markets and propose a multi-leg route without executing it
- `bot_purchase_ship` - Purchase ships from a shipyard with a designated hauler
- `bot_multileg_trade` - Run the multi-leg trading optimizer and summarize the profit
- `bot_negotiate_contract` - Negotiate contracts (requires player_id)
- `bot_fulfill_contract` - Fulfill contracts (requires player_id)

### Daemon Management
- `bot_daemon_start` - Start background operations
- `bot_daemon_stop` - Stop daemons
- `bot_daemon_status` - Check daemon status
- `bot_daemon_logs` - View daemon logs
- `bot_daemon_cleanup` - Sweep the registry for stopped daemons and purge stale records

### Ship Assignments
- `bot_assignments_list` - List ship assignments
- `bot_assignments_assign` - Assign ship to operation
- `bot_assignments_release` - Release ship
- `bot_assignments_find` - Find available ships
- `bot_assignments_available` - Check if a ship is idle and show who holds it if not
- `bot_assignments_status` - Render the full assignment card with operator, daemon, timestamps, metrics
- `bot_assignments_reassign` - Bulk release ships from an operation, optionally stopping daemons first
- `bot_assignments_init` - Bootstrap the registry by pulling the fleet and seeding idle entries

### Navigation & Intelligence
- `bot_build_graph` - Build system graphs
- `bot_plan_route` - Plan routes with fuel awareness
- `bot_scout_markets` - Scout market prices
- `bot_find_mining_opportunities` - Find best mining spots

## Troubleshooting

### Player Not Found

```
❌ Player not found
```

**Solution**: Register the player first
```
Register player AGENT_NAME with token TOKEN
```

### Wrong Player_ID

```
I'm CMDR_AC_2025 but operations are running for player 2
```

**Solution**: Claude should look up player_id by agent_symbol
```
What's my player_id? I'm CMDR_AC_2025
```

### Server Not Showing Up

1. **Check JSON syntax**:
   ```bash
   python3 -m json.tool ~/Library/Application\ Support/Claude/claude_desktop_config.json
   ```

2. **Check path to build/index.js**:
   ```bash
   ls -l /Users/andres.camacho/Development/Personal/spacetradersV2/mcp/bot/build/index.js
   ```

3. **Check Claude logs** (macOS):
   ```bash
   tail -f ~/Library/Logs/Claude/mcp*.log
   ```

## Security Notes

🔒 **Token Security**
- Tokens stored in plaintext in `data/spacetraders.db`
- Protect database file permissions: `chmod 600 data/spacetraders.db`
- Never commit database to git
- Don't share player_id with untrusted users (they can access your operations)

🔒 **Multi-Player Access**
- Anyone with access to the MCP server can execute operations for any player
- Suitable for: Team environments, trusted multi-user setups
- Not suitable for: Public/untrusted environments

## Database Location

```
/Users/andres.camacho/Development/Personal/spacetradersV2/bot/data/spacetraders.db
```

**Backup regularly:**
```bash
cp data/spacetraders.db data/bot_backup_$(date +%Y%m%d).db
```

## Next Steps

1. ✅ Register all players
2. ✅ Check `MULTI_PLAYER_QUICKSTART.md` for database usage examples
3. ✅ Review `MCP_SERVER_README.md` for complete tool documentation
4. ✅ Read `GAME_GUIDE.md` for operational strategies

---

**Ready for multi-player SpaceTraders automation! o7** 🚀
