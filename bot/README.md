# SpaceTraders Unified Bot

A comprehensive automation system for the SpaceTraders API game. This bot provides autonomous mining, market intelligence, contract fulfillment, fleet management, and more.

## Quick Start

```bash
# Install in editable mode
python3 -m venv .venv
source .venv/bin/activate
pip install -e .[dev]

# Run any operation
spacetraders-bot <operation> [options]
```

## Available Operations

### Core Automation
1. **`mine`** - Autonomous mining with smart flight mode selection
2. **`scout`** - Market intelligence gathering and data collection
3. **`contract`** - Automated contract fulfillment (buy/mine/deliver)
4. **`analyze`** - Market analysis and trade route optimization

### Fleet Management
5. **`status`** - Check agent and ship status
6. **`monitor`** - Continuous fleet monitoring
7. **`negotiate`** - Negotiate new contracts
8. **`util`** - Utilities: find fuel, calculate distance

## Architecture

```
bot/
├── src/spacetraders_bot/         # Installable package
│   ├── core/                     # API client, navigation, database, routing
│   ├── operations/               # CLI command handlers
│   ├── cli/                      # Console entry point
│   └── integrations/             # Bridges (e.g., MCP server helper)
│
├── config/agents/                # Versioned agent configuration (JSON)
├── docs/agents/                  # Reference prompts and templates
├── var/                          # Runtime data (logs, state, sqlite)
└── pyproject.toml                # Packaging + entry points
```

## Key Features

### Intelligent Ship Operations
- ✅ **Smart Navigation** - Auto-handles IN_TRANSIT, fuel checks, flight mode
- ✅ **Auto-Refueling** - Refuels when low, optimizes for round trips
- ✅ **Flight Mode Selection** - CRUISE when fuel >75%, DRIFT for efficiency
- ✅ **Comprehensive Cargo** - Buy, sell, jettison with proper error handling

### Resilience & Reliability
- ✅ **Rate Limiting** - API-compliant (2 req/sec)
- ✅ **Automatic Retry** - Network errors, rate limits, server errors
- ✅ **Exponential Backoff** - Smart retry delays (2s → 4s → 8s → 16s → 32s → 60s max)
- ✅ **Error Recovery** - Graceful handling of timeouts, connection errors, API failures
- ✅ **Request Timeouts** - 30s timeout prevents hanging operations

### Observability
- ✅ **Structured Logging** - Python logging with INFO/WARNING/ERROR levels
- ✅ **Dual Logging** - Console and detailed file logs
- ✅ **Comprehensive Tracing** - Full visibility into operations

### Flexibility
- ✅ **CLI Arguments** - All tokens, ships, waypoints via args
- ✅ **Configurable** - Intervals, durations, cycles all customizable
- ✅ **Reusable Library** - Import and use in custom scripts

## Usage Examples

### Mining Operation
```bash
spacetraders-bot mine \
  --ship "SHIP-1" \
  --asteroid "X1-HU87-B9" \
  --market "X1-HU87-B7" \
  --cycles 30
```

**What it does:**
1. Navigates to asteroid
2. Mines until cargo full
3. Navigates to market
4. Sells all cargo
5. Refuels
6. Repeats for specified cycles

### Market Scouting
```bash
spacetraders-bot scout-markets \
  --player-id PLAYER_ID \
  --ship "SHIP-2" \
  --system "X1-HU87" \
  --markets 20
```

**Output:** JSON file with market data for analysis

### Contract Fulfillment
```bash
# 1. Negotiate new contract
spacetraders-bot negotiate --player-id PLAYER_ID --ship SHIP-1

# 2. Fulfill contract
spacetraders-bot contract \
  --player-id PLAYER_ID \
  --ship SHIP-1 \
  --contract-id CONTRACT_ID \
  --buy-from MARKET_WAYPOINT
```

### Fleet Status
```bash
# Check all ships
spacetraders-bot status --player-id PLAYER_ID

# Check specific ships
spacetraders-bot status --player-id PLAYER_ID --ships SHIP-1,SHIP-2
```

### Fleet Monitoring
```bash
# Monitor for 1 hour (12 checks at 5-min intervals)
python3 spacetraders_bot.py monitor \
  --player-id PLAYER_ID \
  --ships SHIP-1,SHIP-2,SHIP-3 \
  --interval 5 \
  --duration 12
```

## Common Workflows

### Autonomous Mining Fleet
Run multiple miners in parallel:

```bash
# Terminal 1 - Ship 1
python3 spacetraders_bot.py mine \
  --player-id PLAYER_ID --ship SHIP-1 \
  --asteroid X1-HU87-B9 --market X1-HU87-B7 \
  --cycles 100

# Terminal 2 - Ship 2
python3 spacetraders_bot.py mine \
  --player-id PLAYER_ID --ship SHIP-2 \
  --asteroid X1-HU87-C14 --market X1-HU87-B7 \
  --cycles 100
```

### Market Intelligence Pipeline
1. **Scout markets:**
```bash
python3 spacetraders_bot.py scout-markets \
  --player-id PLAYER_ID --ship SHIP-2 \
  --system X1-HU87 --markets 20
```

2. **Review data manually** and determine the best trade route

### Contract Automation
1. **Negotiate contract:**
```bash
python3 spacetraders_bot.py negotiate --player-id PLAYER_ID --ship SHIP-1
```

2. **Fulfill contract:**
```bash
python3 spacetraders_bot.py contract \
  --player-id PLAYER_ID --ship SHIP-1 \
  --contract-id CONTRACT_ID \
  --buy-from MARKET_WAYPOINT
```

## Background Execution

```bash
# Run in background
nohup python3 spacetraders_bot.py mine \
  --player-id PLAYER_ID --ship SHIP-1 \
  --asteroid X1-HU87-B9 --market X1-HU87-B7 \
  --cycles 100 > /dev/null 2>&1 &

# Save PID
echo $! > mining.pid

# Monitor log
tail -f logs/mining_SHIP-1_*.log

# Stop
kill $(cat mining.pid)
```

## Troubleshooting

### Rate Limiting
The bot automatically handles rate limits with exponential backoff. If you see warnings about rate limits, this is normal - the bot will retry automatically.

### Navigation Failures
If navigation fails, check:
1. Ship has enough fuel (bot auto-refuels, but check starting fuel)
2. Waypoint symbol is correct
3. Ship is not already in transit

### Import Errors
Ensure you're in the bot directory:
```bash
cd /path/to/spacetradersV2/bot
python3 spacetraders_bot.py <operation> <options>
```

## Getting Your Token

1. Register at https://spacetraders.io
2. Your token is in the API response
3. Or get it from environment: `echo $SPACETRADERS_TOKEN`

## Help

```bash
# General help
python3 spacetraders_bot.py --help

# Operation-specific help
python3 spacetraders_bot.py mine --help
python3 spacetraders_bot.py scout-markets --help
python3 spacetraders_bot.py contract --help
python3 spacetraders_bot.py status --help
python3 spacetraders_bot.py monitor --help
python3 spacetraders_bot.py negotiate --help
python3 spacetraders_bot.py util --help
```

---

**Happy Trading, Commander! o7** 🚀
