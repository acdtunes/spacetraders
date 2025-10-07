# SpaceTraders Unified Bot - Quick Start Guide

## Installation

No installation needed! The bot uses only standard Python libraries:
- `requests`
- `python-dateutil`

```bash
pip install requests python-dateutil
```

## Basic Usage

The unified bot replaces all scattered scripts with a single command-line interface:

```bash
python3 spacetraders_bot.py <operation> [options]
```

### Available Operations

1. **mine** - Autonomous mining operation
2. **scout** - Market scouting and data collection
3. **contract** - Contract fulfillment
4. **analyze** - Market data analysis
5. **status** - Check agent and ship status *(NEW - replaces 15 shell scripts)*
6. **monitor** - Continuous fleet monitoring *(NEW - replaces 8 shell scripts)*
7. **negotiate** - Negotiate new contracts *(NEW - replaces shell scripts)*
8. **util** - Utility operations *(NEW - replaces utility shell scripts)*

## Operation Examples

### 1. Mining Operation

Automate resource extraction and selling:

```bash
python3 spacetraders_bot.py mine \
  --ship "CMDR_AC_2025-1" \
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

**Example Output:**
```
======================================================================
AUTONOMOUS MINING OPERATION
======================================================================

CYCLE 1/30
======================================================================

1. Navigating to asteroid X1-HU87-B9...
[12:34:56] 🚀 Navigating X1-HU87-B7 → X1-HU87-B9 (CRUISE)...
[12:34:56] ⛽ Fuel consumed: 5 (remaining: 75/80)
[12:34:56] ⏳ ETA: 2025-10-04T12:35:30
[12:35:32] ✅ Arrived at X1-HU87-B9

2. Mining until cargo full...
[12:35:33] ⛏️  Extracting resources...
[12:35:34] ✅ Extracted: IRON_ORE x3
[12:35:34] 📦 Cargo: 3/40
...

Revenue this cycle: 1,234 credits
Total revenue: 1,234 credits
```

### 2. Market Scouting

Scout marketplaces and collect trade data:

```bash
python3 spacetraders_bot.py scout-markets \
  --player-id 1 \
  --ship "CMDR_AC_2025-2" \
  --system "X1-HU87" \
  --markets 15
```

**What it does:**
1. Finds all marketplaces in system
2. Navigates to each marketplace
3. Docks and collects market data
4. Saves data to JSON file

**Output:**
```
======================================================================
MARKET SCOUTING OPERATION
======================================================================
System: X1-HU87
Markets to scout: 15
Ship: CMDR_AC_2025-2

1. Finding marketplaces...
Found 18 marketplaces

[1/15] Scouting X1-HU87-B7...
  ✅ Scouted: 24 goods

[2/15] Scouting X1-HU87-C12...
  ✅ Scouted: 18 goods
...

Data saved to: shared/data/scout_X1-HU87_2025-10-04T12-45-30.json
```

### 3. Contract Fulfillment

Automatically fulfill procurement contracts:

```bash
python3 spacetraders_bot.py contract \
  --ship "CMDR_AC_2025-1" \
  --contract-id "cmgbkhqjk19ekuo70grhn7rfy" \
  --buy-from "X1-HU87-B7"
```

**What it does:**
1. Gets contract details
2. Accepts contract (if not accepted)
3. Buys required resources from specified market
4. Navigates to delivery location
5. Delivers goods
6. Collects payment

**Output:**
```
======================================================================
CONTRACT FULFILLMENT OPERATION
======================================================================

1. Getting contract details...

Delivery Requirements:
  Resource: IRON_ORE
  Required: 55
  Fulfilled: 0
  Remaining: 55
  Destination: X1-JB59-G52

3. Buying resources from X1-HU87-B7...
[12:50:23] 💰 Buying 55 x IRON_ORE...
[12:50:25] ✅ Bought 55 x IRON_ORE @ 45 = 2,475 credits

4. Delivering to X1-JB59-G52...
[12:51:15] ✅ Delivered 55 units
🎉 Contract fulfilled! Payment: 9,993 credits
```

## Common Use Cases

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

# Terminal 3 - Ship 3
python3 spacetraders_bot.py mine \
  --player-id PLAYER_ID --ship SHIP-3 \
  --asteroid X1-HU87-D8 --market X1-HU87-B7 \
  --cycles 100
```

### Market Intelligence Pipeline

1. **Scout markets:**
```bash
python3 spacetraders_bot.py scout-markets \
  --player-id PLAYER_ID \
  --player-id PLAYER_ID --ship SHIP-2 \
  --system X1-HU87 --markets 20
```

```bash
  --data-file shared/data/scout_X1-HU87_*.json
```

3. **Execute best trade route manually** (or build trading operation)

### Contract Automation

1. **List contracts** (using old script or API):
```bash
curl -H "Authorization: Bearer TOKEN" \
  https://api.spacetraders.io/v2/my/contracts
```

2. **Fulfill contract:**
```bash
python3 spacetraders_bot.py contract \
  --player-id PLAYER_ID --ship SHIP-1 \
  --contract-id CONTRACT_ID \
  --buy-from MARKET_WAYPOINT
```

## Advanced Usage

### Using as a Library

You can import and use the bot components in your own scripts:

```python
from lib import APIClient, ShipController

# Initialize
api = APIClient(token="YOUR_TOKEN")
ship = ShipController(api, ship_symbol="SHIP-1")

# High-level operations
ship.navigate("X1-HU87-B9")
ship.orbit()
extraction = ship.extract()
ship.navigate("X1-HU87-B7")
ship.dock()
revenue = ship.sell_all()

print(f"Revenue: {revenue} credits")
```

### Custom Operations

Create custom operation scripts:

```python
#!/usr/bin/env python3
from lib import APIClient, ShipController

def custom_mining(token, ship_symbol, targets):
    """Mine from multiple asteroids"""
    api = APIClient(token)
    ship = ShipController(api, ship_symbol)

    for asteroid, market in targets:
        ship.navigate(asteroid)
        ship.orbit()

        # Mine 20 units
        for _ in range(20):
            ship.extract()

        ship.navigate(market)
        ship.dock()
        ship.sell_all()
        ship.refuel()

# Run
custom_mining(
    token="TOKEN",
    ship_symbol="SHIP-1",
    targets=[
        ("X1-HU87-B9", "X1-HU87-B7"),
        ("X1-HU87-C14", "X1-HU87-B7"),
        ("X1-HU87-D8", "X1-HU87-B7"),
    ]
)
```

## Logging

All operations create log files in `logs/`:

```
logs/
├── mining_SHIP-1_2025-10-04T12-34-56.log
├── scout_SHIP-2_2025-10-04T13-45-23.log
└── contract_SHIP-1_2025-10-04T14-56-12.log
```

## Troubleshooting

### Rate Limiting

The bot automatically handles rate limits. If you see:
```
Rate limit hit, waiting 10s (attempt 1/5)
```

This is normal - the bot will retry automatically.

### Navigation Failures

If navigation fails:
```
❌ Navigation failed
```

Check:
1. Ship has enough fuel (bot auto-refuels, but check starting fuel)
2. Waypoint symbol is correct
3. Ship is not already in transit

### Import Errors

If you get `ModuleNotFoundError`:
```bash
# Ensure you're in the bot directory
cd /path/to/spacetradersV2/bot

# Run the bot
python3 spacetraders_bot.py <operation> <options>
```

## Getting Your Token

1. Register at https://spacetraders.io
2. Your token is in the API response
3. Or get it from environment: `echo $SPACETRADERS_TOKEN`

## Next Steps

1. **Try mining operation** - Start earning credits
2. **Scout your system** - Gather market intelligence
4. **Fulfill contracts** - Complete missions for reputation

### 5. Status Checking (NEW!)

Check agent and ship status - **replaces 15 shell scripts** like `check_status.sh`:

```bash
# Check all ships
python3 spacetraders_bot.py status --player-id PLAYER_ID

# Check specific ships
python3 spacetraders_bot.py status \
  --player-id PLAYER_ID \
  --ships "SHIP-1,SHIP-2,SHIP-3"
```

**Output:**
```
======================================================================
SPACETRADERS STATUS - 2025-10-04T14:23:45
======================================================================

💰 AGENT INFO:
  Callsign: CMDR_AC_2025
  Credits: 284,190
  HQ: X1-HU87-A1

🚀 FLEET STATUS:

  SHIP-1:
    Location: X1-HU87-B7
    Status: DOCKED
    Flight Mode: CRUISE
    Fuel: 400/400
    Cargo: 0/40

  SHIP-2:
    Location: X1-HU87-C12
    Status: IN_ORBIT
    Flight Mode: DRIFT
    Fuel: 45/100
    Cargo: 15/0
```

### 6. Fleet Monitoring (NEW!)

Monitor fleet continuously - **replaces 8 shell scripts** like `monitor_loop.sh`, `fleet_monitor.sh`:

```bash
python3 spacetraders_bot.py monitor \
  --player-id PLAYER_ID \
  --ships "SHIP-1,SHIP-2,SHIP-3" \
  --interval 5 \
  --duration 12
```

**What it does:**
- Checks status every 5 minutes
- Runs for 12 checks (1 hour total)
- Tracks profit over time
- Shows ship locations, fuel, cargo
- Displays ETA for in-transit ships

**Output:**
```
======================================================================
FLEET MONITORING
======================================================================
Starting Credits: 284,190
Monitoring Interval: 5 minutes
Ships: SHIP-1,SHIP-2,SHIP-3
Duration: 12 checks

======================================================================
CHECK #1 - 14:23:45
======================================================================

💰 Credits: 284,190 (+0)

🚀 SHIP-1:
   DOCKED at X1-HU87-B7
   Fuel: 400/400 | Cargo: 0/40

🚀 SHIP-2:
   IN_TRANSIT at X1-HU87-C12
   Fuel: 45/100 | Cargo: 15/0
   ETA: 2025-10-04T14:28:30
...
```

### 7. Contract Negotiation (NEW!)

Negotiate new contracts - **replaces** `negotiate_contract.sh`:

```bash
python3 spacetraders_bot.py negotiate \
  --player-id PLAYER_ID \
  --ship "SHIP-1"
```

**Output:**
```
======================================================================
NEGOTIATE CONTRACT
======================================================================
Negotiating contract with ship SHIP-1...

✅ Contract negotiated successfully!

Contract ID: cmgbkhqjk19ekuo70grhn7rfy
Type: PROCUREMENT
Faction: COSMIC

Payment:
  On Accept: 2,657
  On Fulfill: 9,993

Delivery Requirements:
  - 55 x IRON_ORE
    Destination: X1-JB59-G52

Deadline to Accept: 2025-10-05T01:01:15.291Z
Deadline to Fulfill: 2025-10-11T01:01:15.291Z
```

### 8. Utility Operations (NEW!)

Utility operations - **replaces** `find_nearest_fuel.sh`, distance calculation scripts:

**Find nearest fuel station:**
```bash
python3 spacetraders_bot.py util \
  --player-id PLAYER_ID \
  --type find-fuel \
  --ship "SHIP-1"
```

**Output:**
```
======================================================================
FIND NEAREST FUEL STATION
======================================================================
Current location: X1-HU87-A2 (-123, 456)

Nearest fuel stations:
----------------------------------------------------------------------
1. X1-HU87-A1            (PLANET              ) - 0 units
2. X1-HU87-B7            (ASTEROID            ) - 45 units
3. X1-HU87-C12           (MOON                ) - 78 units
```

**Calculate distance between waypoints:**
```bash
python3 spacetraders_bot.py util \
  --player-id PLAYER_ID \
  --type distance \
  --waypoint1 "X1-HU87-A2" \
  --waypoint2 "X1-HU87-B9"
```

**Output:**
```
======================================================================
CALCULATE DISTANCE
======================================================================
X1-HU87-A2: (-123, 456)
X1-HU87-B9: (234, -567)

Distance: 1123.4 units
Fuel needed (CRUISE): ~1235 units
Fuel needed (DRIFT): ~5 units
```

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

## Migration from Old Scripts

### Python Scripts
See `CONSOLIDATION_GUIDE.md` for detailed migration from old Python scripts.

### Shell Scripts
See `SHELL_CONSOLIDATION.md` for detailed migration from shell scripts.

**Quick Reference:**
- `check_status.sh` → `python3 spacetraders_bot.py status --token T`
- `monitor_loop.sh` → `python3 spacetraders_bot.py monitor --token T --ships S1,S2 --interval 5`
- `negotiate_contract.sh` → `python3 spacetraders_bot.py negotiate --token T --ship S`
- `find_nearest_fuel.sh` → `python3 spacetraders_bot.py util --type find-fuel --token T --ship S`

---

**Happy Trading, Commander! o7** 🚀
