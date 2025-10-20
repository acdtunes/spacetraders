# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository Overview

Autonomous bot automation system for SpaceTraders (HTTP API-based space trading game). The bot provides intelligent fleet management, autonomous mining/trading operations, smart routing, contract fulfillment, and market intelligence.

## Architecture

### Core Components

```
bot/
├── spacetraders_bot.py          # Main CLI entry point (all operations)
│
├── src/spacetraders_bot/core/    # Core library components
│   ├── api_client.py             # SpaceTraders API client with rate limiting
│   ├── ship_controller.py        # Ship state machine & operations
│   ├── smart_navigator.py        # Intelligent navigation with fuel optimization
│   ├── routing.py                # OR-Tools routing wrappers + graph building
│   ├── routing_config.py         # Hot-reloadable routing constants (YAML)
│   ├── routing_validator.py      # Route validation + pause mechanisms
│   ├── daemon_manager.py         # Background process management
│   ├── assignment_manager.py     # Ship allocation & conflict prevention
│   └── operation_controller.py   # Operation lifecycle & checkpointing
│
├── src/spacetraders_bot/operations/  # Modular operation handlers
│   ├── mining.py                 # Autonomous mining operations
│   ├── multileg_trader.py        # Trading & market scouting
│   ├── contracts.py              # Contract negotiation & fulfillment
│   ├── fleet.py                  # Fleet status & monitoring
│   ├── analysis.py               # Market analysis & utilities
│   ├── routing.py                # Route planning & graph operations
│   ├── daemon.py                 # Daemon lifecycle operations
│   ├── assignments.py            # Ship assignment operations
│   └── scout_coordination.py     # Multi-ship market coordination
│
└── tests/                        # BDD tests (pytest-bdd)
    ├── test_*_steps.py           # Step definitions
    ├── mock_api.py               # Mock SpaceTraders API
    └── conftest.py               # Test fixtures
```

### Design Philosophy

**Three-Layer Agent Architecture:**
1. **Human Captain** - Strategic decisions, approvals, interventions
2. **AI First Mate (Flag Captain)** - Tactical planning, agent coordination, reporting
3. **Specialist Agents** - Operational execution (mining, trading, contracts)

**🚨 CRITICAL AGENT RULES:**
- **NEVER use the CLI** (`spacetraders_bot.py`) - Always use MCP tools (`mcp__spacetraders-bot__*`)
- **NEVER call HTTP API directly** - Use `mcp__spacetraders-api__*` tools instead
- **Navigation:** Always use `bot_navigate` - includes SmartNavigator with automatic fuel management
- **Wait function:** ONLY Flag Captain may use `bot_wait_minutes` - specialists must NOT use it
- **Spawn specialists:** Use Task tool with appropriate `subagent_type` for complex operations

See `AGENT_ARCHITECTURE.md` for complete agent system design.

## Key Systems

### 1. Ship Controller - State Machine

**File:** `src/spacetraders_bot/core/ship_controller.py`

Ships have states: `DOCKED`, `IN_ORBIT`, `IN_TRANSIT`. All operations automatically handle state transitions:

```python
ship = ShipController(api, "SHIP-1")

# Automatically handles state transitions
ship.navigate("X1-HU87-B9")  # If DOCKED → orbit first, then navigate
ship.extract()               # If DOCKED → orbit first, then extract
ship.dock()                  # If IN_TRANSIT → wait for arrival, then dock
```

**Key Methods:**
- `navigate(waypoint, flight_mode=None)` - Auto-selects flight mode, handles IN_TRANSIT waits
- `orbit()`, `dock()` - State transitions with automatic waiting
- `extract()`, `sell_all()`, `refuel()` - Cargo/extraction/refueling
- `get_status()` - Full ship data (nav, fuel, cargo, location)

### 2. Smart Navigator - Intelligent Routing

**Files:** `src/spacetraders_bot/core/smart_navigator.py`, `src/spacetraders_bot/core/ortools_router.py`, `src/spacetraders_bot/core/routing.py`

Backed by Google OR-Tools (VRP/TSP solvers) with fuel/resource constraints and dual-mode edges. Provides fuel-aware pathfinding with automatic refuel stop insertion:

```python
navigator = SmartNavigator(api, "X1-HU87")

# Validates route feasibility
valid, reason = navigator.validate_route(ship.get_status(), "X1-HU87-B9")

# Executes route with auto-refueling
success = navigator.execute_route(ship, "X1-HU87-B9")
```

**Features:**
- **Google OR-Tools VRP/TSP engine** with fuel dimension and flight mode decisions
- **Automatic refuel stops** and fuel safety margin enforcement
- **Flight mode optimisation** (prefers CRUISE, falls back to DRIFT when capacity demands)
- **Graph caching** (`graphs/*.json`) via `GraphBuilder`
- **Tour optimisation** (ORToolsTSP replaces 2-opt/nearest neighbour heuristics)
- **Multi-ship partitioning** through `ORToolsFleetPartitioner`
- **Configurable constants** loaded from `config/routing_constants.yaml` (hot-reload supported)

### Routing Validation

- Manual validation command: `spacetraders_bot.py validate-routing --player-id X --ship SHIP --destination WAYPOINT`
- Uses `RoutingValidator` to compare predicted vs actual results
- Deviations >5% automatically pause routing (flag stored in `var/routing_pause.json`)
- Successful validations clear the pause flag

**Flight Modes:**
- `CRUISE`: ~1 fuel/unit (fast, use when fuel >75%)
- `DRIFT`: ~1 fuel/300 units (slow, fuel-efficient)

### 3. Daemon Manager - Background Operations

**File:** `src/spacetraders_bot/core/daemon_manager.py`

Manages long-running operations as background processes:

```bash
# Start daemon
python3 spacetraders_bot.py daemon start mine \
  --daemon-id miner-ship3 \
  --ship SHIP-3 \
  --asteroid X1-HU87-B9 \
  --market X1-HU87-B7 \
  --cycles 50

# Monitor
python3 spacetraders_bot.py daemon status miner-ship3
python3 spacetraders_bot.py daemon logs miner-ship3 --lines 50

# Stop
python3 spacetraders_bot.py daemon stop miner-ship3
```

**Daemon Types:**
- `mine` - Autonomous mining loops
- `trade` - Trading route execution
- `contract` - Contract fulfillment
- `scout-markets` - Market intelligence gathering

**Storage:**
- PIDs: `var/daemons/pids/{daemon_id}.json`
- Logs: `var/daemons/logs/{daemon_id}.log`

### 4. Assignment Manager - Ship Allocation

**File:** `src/spacetraders_bot/core/assignment_manager.py`

Prevents ship double-booking with centralized registry:

```bash
# List assignments
python3 spacetraders_bot.py assignments list

# Assign ship
python3 spacetraders_bot.py assignments assign \
  --ship SHIP-3 \
  --operator mining_operator \
  --daemon-id miner-ship3 \
  --operation mine

# Release ship
python3 spacetraders_bot.py assignments release SHIP-3

# Find available ships
python3 spacetraders_bot.py assignments find --cargo-min 40

# Sync with running daemons
python3 spacetraders_bot.py assignments sync
```

**Registry Location:** `var/data/sqlite/spacetraders.db`

### 5. API Client - Rate Limiting & Retry

**File:** `src/spacetraders_bot/core/api_client.py`

Handles SpaceTraders API with automatic retry and rate limiting:

- **Rate limit:** 2 requests/sec sustained, 10 burst/10s
- **Automatic retry:** Network errors, 429 rate limits, 5xx server errors
- **Exponential backoff:** 2s → 4s → 8s → 16s → 32s → 60s max
- **Request timeout:** 30s default

```python
api = APIClient(token="YOUR_TOKEN")
response = api.get("/my/ships")  # Auto-handles retries, rate limits
```

## Common Development Tasks

### Running Operations

```bash
# Mining operation (foreground)
python3 spacetraders_bot.py mine \
  --player-id PLAYER_ID --ship SHIP-1 \
  --asteroid X1-HU87-B9 --market X1-HU87-B7 \
  --cycles 30

# Market scouting with optimized tour
python3 spacetraders_bot.py scout-markets \
  --player-id PLAYER_ID --ship SHIP-2 \
  --system X1-HU87 --algorithm 2opt --return-to-start

# Contract fulfillment
python3 spacetraders_bot.py contract \
  --player-id PLAYER_ID --ship SHIP-1 --contract-id ID

# Fleet status
python3 spacetraders_bot.py status --player-id PLAYER_ID

# Fleet monitoring (periodic checks)
python3 spacetraders_bot.py monitor \
  --player-id PLAYER_ID --ships SHIP-1,SHIP-2 \
  --interval 5 --duration 12
```

### Testing

**Framework:** pytest-bdd (Behavior-Driven Development with Gherkin)

**Status:** ✅ 100% BDD Migration Complete (as of 2025-10-19)

All 117 tests have been migrated to BDD format using Gherkin scenarios:
- ✅ 94 domain tests → `tests/bdd/features/` (trading, routing, scouting, etc.)
- ✅ 23 unit tests → `tests/bdd/features/unit/` (CLI, core, operations)
- ❌ Legacy subprocess bridge → **DELETED**
- ❌ Legacy `tests/domain/` → **DELETED**
- ❌ Legacy `tests/unit/` → **DELETED**

```bash
# Install dependencies
pip install -r tests/requirements.txt

# Run all BDD tests
pytest tests/ -v

# Run specific domain
pytest tests/bdd/features/trading/ -v

# Run specific feature
pytest tests/bdd/features/routing/fuel_aware_routing.feature -v

# Run with coverage
pytest tests/ --cov=src --cov-report=html
open htmlcov/index.html

# Run specific scenario
pytest tests/ -k "Skip failed segment when independent segments remain" -v

# Run by marker
pytest -m unit tests/         # Unit-level tests
pytest -m domain tests/       # Domain-level tests
pytest -m regression tests/   # Regression tests only
```

**Test Structure:**
- `tests/bdd/features/` - Gherkin feature files organized by domain
- `tests/bdd/steps/` - Step definitions (Given/When/Then)
- `tests/bdd/steps/fixtures/` - Mock fixtures (API, database, etc.)
- `tests/conftest.py` - pytest-bdd configuration

**Philosophy:** Every test can and should be BDD - from unit tests to complex integration tests.

**Coverage Goals:** 80%+ (current: ~85% for core components)

See `TESTING_GUIDE.md` for complete BDD testing patterns and examples.

### Building System Graphs

Smart navigation requires system graphs for pathfinding:

```bash
# Build graph for a system
python3 spacetraders_bot.py graph build \
  --player-id PLAYER_ID --system X1-HU87 \
  --output graphs/X1-HU87_graph.json

# Plan route between waypoints
python3 spacetraders_bot.py route plan \
  --player-id PLAYER_ID --ship SHIP-1 \
  --start X1-HU87-A1 --end X1-HU87-B9 \
  --graph graphs/X1-HU87_graph.json

# Plan multi-stop tour
python3 spacetraders_bot.py tour plan \
  --player-id PLAYER_ID --ship SHIP-1 \
  --waypoints X1-HU87-A1,X1-HU87-B7,X1-HU87-C5 \
  --algorithm 2opt \
  --graph graphs/X1-HU87_graph.json
```

### Background Execution

For long-running operations, use daemons instead of nohup:

```bash
# Old way (manual process management)
nohup python3 spacetraders_bot.py mine ... > logs/output.log 2>&1 &
echo $! > mining.pid

# New way (managed daemons)
python3 spacetraders_bot.py daemon start mine \
  --daemon-id miner-ship3 --ship SHIP-3 \
  --asteroid X1-HU87-B9 --market X1-HU87-B7 \
  --cycles 50

# Monitor
python3 spacetraders_bot.py daemon status
python3 spacetraders_bot.py daemon logs miner-ship3
```

### Logging Control

```bash
# Default: INFO level (normal operations)
python3 spacetraders_bot.py mine --token T --ship S --asteroid A --market M

# WARNING level (minimal output, production)
python3 spacetraders_bot.py mine ... --log-level WARNING

# ERROR level (critical issues only)
python3 spacetraders_bot.py mine ... --log-level ERROR
```

**Log files:** All operations create detailed logs in `logs/` directory.

## Important Implementation Details

### Ship State Machine Transitions

**Critical:** Always check ship state before operations:

```python
# BAD: Assumes ship is in correct state
ship.extract()  # Fails if ship is DOCKED

# GOOD: Let ship controller handle state
ship.extract()  # Automatically orbits if DOCKED, then extracts
```

**State Transition Rules:**
- `DOCKED` → can refuel, sell cargo, buy cargo, repair
- `IN_ORBIT` → can extract, scan, navigate
- `IN_TRANSIT` → must wait for arrival before other actions

`ShipController` automatically handles all transitions.

### Fuel Management

**Critical fuel constraints:**

1. **DRIFT mode needs minimum 1 fuel** (0 fuel = permanently stranded)
2. **Always calculate round-trip fuel** before navigating
3. **SmartNavigator automatically inserts refuel stops** when needed
4. **Flight mode selection:**
   - Fuel >75% → CRUISE (fast)
   - Fuel <75% → DRIFT (efficient)
   - Emergency (<25%) → DRIFT to nearest market

**Fuel calculation:**
```python
distance = 150  # units
cruise_fuel = distance * 1.0  # ~150 fuel
drift_fuel = distance * 0.003  # ~0.5 fuel

# SmartNavigator handles this automatically
navigator.validate_route(ship.get_status(), destination)
```

### Zero-Distance Navigation

**Critical discovery:** Waypoints sharing orbital parent have **0 distance** (instant, no fuel):

- Planet ↔ Moon (orbiting same parent)
- Check `orbitals` field (planet) or `orbits` field (moon)
- Example: X1-HU87-A1 (planet) ↔ X1-HU87-A2 (moon) = 0 fuel

Useful for emergency refueling access.

### API Rate Limiting

SpaceTraders API enforces **2 requests/sec sustained, 10 burst/10s**.

`APIClient` handles this automatically with:
- Token bucket rate limiter
- Exponential backoff on 429 errors
- Automatic retry on network/server errors

**No manual sleep needed** - client handles all rate limiting.

### Daemon Process Management

Daemons run as background Python processes:

- **PID tracking:** JSON files store process metadata
- **Log streaming:** Real-time logs via `daemon logs`
- **Graceful shutdown:** SIGTERM allows cleanup
- **Stale detection:** `daemon status` checks if process still alive

**Cleanup stale daemons:**
```bash
python3 spacetraders_bot.py daemon cleanup
```

### Ship Assignment Conflicts

**Problem:** Multiple operations using same ship simultaneously.

**Solution:** Always use assignment manager:

```python
# 1. Check availability
assignments available SHIP-3

# 2. Assign before starting daemon
assignments assign --ship SHIP-3 --operator mining_op --daemon-id miner-3 --operation mine

# 3. Start daemon
daemon start mine --daemon-id miner-3 --ship SHIP-3 ...

# 4. Release when done
assignments release SHIP-3
```

**Auto-sync:** `assignments sync` reconciles registry with running daemons.

## Working with Operations

### Adding New Operations

1. **Create operation handler** in `operations/new_operation.py`:

```python
def new_operation(args, api, logger):
    """Operation handler"""
    logger.info("Starting new operation")
    # Implementation
    return True  # or False on failure
```

2. **Export in** `operations/__init__.py`:

```python
from .new_operation import new_operation

__all__ = [..., 'new_operation']
```

3. **Add CLI argument parser** in `spacetraders_bot.py`:

```python
# Add subparser
new_parser = subparsers.add_parser('newop', help='New operation')
new_parser.add_argument('--token', required=True)
# ... other arguments
```

4. **Add dispatcher entry** in `spacetraders_bot.py`:

```python
elif args.operation == 'newop':
    from operations import new_operation
    success = new_operation(args, api, logger)
```

### Using Ship Controller in Operations

```python
from lib.api_client import APIClient
from lib.ship_controller import ShipController

api = APIClient(token=args.token)
ship = ShipController(api, args.ship)

# High-level operations handle all details
ship.navigate("X1-HU87-B9")  # Auto-selects flight mode, handles state
ship.orbit()                 # Waits if IN_TRANSIT
ship.extract()               # Auto-orbits if DOCKED
ship.sell_all()             # Sells all cargo
ship.refuel()               # Auto-docks if needed
```

### Using Smart Navigator

```python
from lib.smart_navigator import SmartNavigator

navigator = SmartNavigator(api, system="X1-HU87")

# Validate before executing
status = ship.get_status()
valid, reason = navigator.validate_route(status, destination)
if not valid:
    logger.error(f"Route invalid: {reason}")
    return False

# Execute with automatic refuel stops
success = navigator.execute_route(ship, destination)
```

## Environment Setup

### Dependencies

```bash
pip install requests python-dateutil

# For testing
pip install pytest pytest-bdd pytest-cov
```

### Authentication

- **Agent Token:** JWT string from SpaceTraders registration
- **Pass via CLI:** `--token YOUR_TOKEN`
- **Or environment variable:** `SPACETRADERS_TOKEN`

### Directory Structure

Bot expects these directories (auto-created):
- `logs/` - Operation logs
- `graphs/` - Cached system graphs
- `var/daemons/pids/` - Daemon PID files
- `var/daemons/logs/` - Daemon logs
- `agents/cmdr_ac_2025/` - Agent-specific data

## Game Mechanics Quick Reference

**⚠️ IMPORTANT:** Operating this bot requires understanding SpaceTraders game mechanics. See `GAME_GUIDE.md` for complete operational strategies.

### Critical Fuel Rules

**NEVER violate these:**

1. **DRIFT mode needs minimum 1 fuel** - 0 fuel = ship permanently lost
2. **Always calculate round-trip fuel:**
   ```
   Required = (Distance × 2 × 1.1)  # 10% safety buffer
   ```
3. **Flight mode consumption:**
   - `CRUISE`: ~1 fuel/unit (fast, use when fuel >75%)
   - `DRIFT`: ~1 fuel/300 units (slow, fuel-efficient)

4. **Flight mode selection:**
   - Fuel >75% → Use CRUISE
   - Fuel <75% → Use DRIFT (or CRUISE for short <100 unit trips)
   - Fuel <25% → Emergency: DRIFT to nearest market

**SmartNavigator handles fuel calculations automatically** - always use it for navigation.

### Ship States

Ships exist in three states with specific capability constraints:

| State | Can Do | Cannot Do |
|-------|--------|-----------|
| `DOCKED` | Refuel, buy/sell cargo, repair | Extract, navigate directly |
| `IN_ORBIT` | Extract, navigate, scan | Buy/sell, refuel |
| `IN_TRANSIT` | Nothing (wait for arrival) | Everything else |

**ShipController automatically handles state transitions** - just call the method you need.

### Market Types

**Always verify market type before navigating:**

| Type | Sells To Ships | Buys From Ships | Use For |
|------|----------------|-----------------|---------|
| `EXCHANGE` | Raw materials (ores, crystals) | Raw materials | Mining operations |
| `HQ` | Equipment, food, medicine | Finished goods | Contracts (finished) |
| `INDUSTRIAL` | Refined metals, components | Ores, raw materials | Contracts (raw) |
| Fuel Station | Fuel only | Nothing | Refueling only |
| `SHIPYARD` | Ships, modules | Varies | Fleet expansion |

### Mining Operations

**Key Parameters:**
- **Cooldown:** 80 seconds between extractions
- **Yield:** 2-7 units per extraction (RNG)
- **Success Rate:** 10-15% for targeted ores
- **Best Practice:** Mine only if <200 fuel from market

**Asteroid Selection:**
- ✅ SEEK: `COMMON_METAL_DEPOSITS`, `PRECIOUS_METAL_DEPOSITS`, `MINERAL_DEPOSITS`
- ❌ AVOID: `STRIPPED` (depleted), `RADIOACTIVE`, `EXPLOSIVE_GASES`

**Decision Rule:** Compare mining time vs. buying cost
```python
mining_time_hours = (units_needed / avg_yield) * 80 / 3600
opportunity_cost = mining_time_hours * credits_per_hour_from_trading
if market_purchase_cost < opportunity_cost:
    # BUY instead of mining
```

### Contract Evaluation

**Accept contract if ALL true:**
- ✅ Net profit >5,000 credits
- ✅ ROI >5%
- ✅ Resource available (can mine or buy)
- ✅ Delivery within fuel range

**Calculate profit:**
```python
total_payment = on_accepted + on_fulfilled
trips = ceil(units_required / cargo_capacity)  # Usually 40
cost = (fuel_cost + purchase_or_mining_cost) * trips
net_profit = total_payment - cost
roi = (net_profit / cost) * 100
```

### Zero-Distance Navigation

**Critical optimization:** Waypoints sharing orbital parent have **0 distance** (instant, no fuel).

Example: Planet `X1-HU87-A1` ↔ Moon `X1-HU87-A2` = 0 fuel

Check planet's `orbitals` field or moon's `orbits` field.

### Common Decision Points

**When to use CRUISE vs DRIFT:**
```python
if ship.fuel > ship.fuel_capacity * 0.75:
    use CRUISE  # Fast travel
elif distance < 100 and ship.fuel > distance * 2:
    use CRUISE  # Short trip, acceptable fuel
else:
    use DRIFT  # Fuel efficiency priority
```

**When to mine vs buy:**
```python
if resource_market_price * units < mining_opportunity_cost:
    BUY  # Cheaper than mining
else:
    MINE  # More economical
```

**When to accept contract:**
```python
if net_profit > 5000 and roi > 5 and resource_available and within_fuel_range:
    ACCEPT
else:
    REJECT  # Not worth it
```

### Complete Game Guide

For detailed strategies, see `GAME_GUIDE.md`:
- **Fuel Management** - Emergency procedures, calculations, optimization
- **Mining Best Practices** - Asteroid selection, yield optimization
- **Marketplace Mechanics** - Market types, trading strategies, price ranges
- **Contract Operations** - Negotiation, evaluation formulas, fulfillment
- **Common Mistakes** - The 15 Commandments, emergency procedures
- **Quick Reference Tables** - Fuel planning, flight modes, market types

## MCP Server Integration

The bot includes MCP (Model Context Protocol) servers that expose all operations as tools for Claude and other MCP clients.

**Files:**
- `mcp/bot/src/index.ts` - Bot operations MCP server
- `mcp/api/src/index.ts` - SpaceTraders API MCP server

**Key Features:**
- Exposes all bot operations as MCP tools
- Allows Claude to execute operations directly
- Supports daemon management and monitoring
- Enables autonomous fleet management workflows
- Provides direct SpaceTraders API access for queries

**CRITICAL RULES FOR AGENTS:**
1. **NEVER use the CLI directly** - Always use `mcp__spacetraders-bot__*` tools
2. **NEVER call HTTP API directly** - Use `mcp__spacetraders-api__*` or `mcp__spacetraders-bot__*` tools
3. **Navigation:** Always use `mcp__spacetraders-bot__bot_navigate` - it uses SmartNavigator with automatic fuel management
4. **Wait function:** ONLY Flag Captain may use `bot_wait_minutes` - specialists must NOT use it
5. **Specialists:** Use Task tool to spawn specialists for complex operations

**Navigation Tool:**
```python
# Navigate ship with automatic fuel management and refuel stops
bot_navigate(player_id=6, ship="VEILSTORM-1", destination="X1-NF92-H59")
```

**Setup:**
```bash
# Install MCP SDK
pip install mcp

# Configure in Claude Desktop config
# See MCP_SERVER_README.md for details
```

**Usage in Claude Desktop:**
Once configured, Claude can directly:
- Check fleet status
- Navigate ships with automatic fuel management
- Start/stop mining and trading operations
- Scout markets and analyze routes
- Manage daemons and ship assignments
- Negotiate and fulfill contracts
- Write narrative mission logs to Captain's Log

See `MCP_SERVER_README.md` for complete setup and tool documentation.

### Captain's Log - Narrative Mission Logging

**File:** `operations/captain_logging.py`

All specialist agents MUST log operations using `bot_captain_log_entry` with narrative prose format:

**REQUIRED PARAMETERS:**
- `narrative` - First-person story-like description explaining what was done and WHY
- `insights` (for OPERATION_COMPLETED) - Strategic lessons learned
- `recommendations` (for OPERATION_COMPLETED) - Forward-looking suggestions

**Format Requirements:**
1. **Use first-person voice** - "I deployed SHIP-6 to asteroid B46..."
2. **Explain strategic reasoning** - "I chose DRIFT mode because fuel costs would destroy profit in CRUISE"
3. **Include emotional tone** - Express pride, concern, determination as appropriate
4. **Provide context** - Explain situational factors that influenced decisions
5. **Quantify results** - Include hard numbers for performance metrics
6. **Look forward** - Offer actionable recommendations for optimization

**Example:**
```python
bot_captain_log_entry(
  agent="IRONKEEP",
  entry_type="OPERATION_COMPLETED",
  operator="Mining Operator",
  ship="IRONKEEP-6",
  narrative="""I've been running IRONKEEP-6 on the B46→B7 mining route for 4 hours.
The asteroid yielded primarily GOLD_ORE as expected, but I had to adapt when yields
dropped to 3 units/extraction. Rather than abandon position, I extended cycle times
to avoid depletion. Generated 6,100 credits net—consistent and reliable.""",
  insights="""B46 yields 3.2 units avg (vs expected 4), but B7 price compensated
(1,620 cr/unit vs 1,500 forecast). DRIFT mode essential—CRUISE would have destroyed
profitability. Ship speed (9) is the bottleneck; faster ships could triple profit/hr.""",
  recommendations="""Consider faster mining ship upgrade for this route. Monitor B7
market—if GOLD_ORE drops below 1,400 cr/unit, switch to B22 for COPPER_ORE. B46 can
sustain 8-12 more hours before depletion risk."""
)
```

**File Locking:**
The logging system implements automatic file locking with exponential backoff retry (0.1s → 1.6s) to safely handle concurrent writes from multiple specialists.

**Log Structure:**
- Main log: `agents/{agent}/docs/captain-log.md`
- Session archives: `agents/{agent}/logs/sessions/{session_id}.json`
- Executive reports: `agents/{agent}/logs/executive_reports/`

**MCP Tools:**
- `bot_captain_log_init` - Initialize new captain's log
- `bot_captain_log_session_start` - Start mission session
- `bot_captain_log_entry` - Write narrative log entry
- `bot_captain_log_session_end` - End session with summary
- `bot_captain_log_search` - Search logs by tag/timeframe
- `bot_captain_log_report` - Generate executive summary

## Documentation

- `README.md` - User-facing guide with operation examples
- `AGENT_ARCHITECTURE.md` - Multi-agent system design
- `TESTING_GUIDE.md` - BDD testing with pytest-bdd
- `GAME_GUIDE.md` - SpaceTraders game mechanics and strategies
- `QUICK_START.md` - Quick reference for all operations
- `LOGGING_AND_ERROR_HANDLING.md` - Logging system details
- `MCP_SERVER_README.md` - MCP server setup and tool reference

## Support

- **SpaceTraders API Docs:** https://docs.spacetraders.io
- **SpaceTraders Website:** https://spacetraders.io
- **Discord:** https://discord.gg/UpEfRRjjsCT
