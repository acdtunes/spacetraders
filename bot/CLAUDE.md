# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## ⚠️ CRITICAL: Database Preservation ⚠️

**NEVER, EVER DELETE OR RECREATE THE DATABASE!**

**PRODUCTION DATABASE:** PostgreSQL at `localhost:5432/spacetraders` (configured via `DATABASE_URL` in `.env`)
**TEST DATABASE:** SQLite in-memory (`:memory:`) - ephemeral, created per test

**SQLite is ONLY used for tests!** All production operations use PostgreSQL.

The PostgreSQL database contains production data including:
- Registered players and agent tokens
- Ship assignments and navigation state
- Fleet operations history
- Container logs and daemon state

### Database Schema Changes

When adding new columns or tables:
1. **ALWAYS use ALTER TABLE to add columns** - NEVER drop and recreate tables
2. **Use DEFAULT values** for new columns to handle existing rows
3. **Test migrations on a copy first** - `pg_dump spacetraders > backup.sql` (PostgreSQL)
4. **Verify data preservation** after schema changes

Example safe migration:
```python
# GOOD - Adds column without losing data
cursor.execute("ALTER TABLE players ADD COLUMN credits INTEGER DEFAULT 0")

# BAD - DESTROYS ALL DATA
cursor.execute("DROP TABLE players")
cursor.execute("CREATE TABLE players (...)")
```

**If you accidentally delete data, restore from backup immediately!**

## ⚠️ CRITICAL: Daemon Server Restart Protocol ⚠️

**ALWAYS restart the daemon server after ANY code changes to routing, navigation, or scouting modules!**

The daemon server runs as a background process and loads code at startup. Code changes are NOT automatically picked up.

### When to Restart Daemon

Restart daemon IMMEDIATELY after:
- Using spacetraders-dev agent (it modifies code)
- Modifying files in `src/spacetraders/adapters/secondary/routing/`
- Modifying files in `src/spacetraders/application/navigation/`
- Modifying files in `src/spacetraders/application/scouting/`
- ANY changes to command handlers or VRP solver

### How to Restart Daemon

**Recommended: Use the scripts in `scripts/` folder:**

```bash
# Check daemon status
./scripts/daemon_status.sh

# Restart daemon (kills old, starts new)
./scripts/restart_daemon.sh

# Or manage manually:
./scripts/stop_daemon.sh   # Stop daemon
./scripts/start_daemon.sh  # Start daemon
```

**Manual restart (if scripts fail):**

```bash
# 1. Kill old daemon
pkill -9 -f daemon_server

# 2. Wait for cleanup
sleep 2

# 3. Start new daemon with updated code
./scripts/start_daemon.sh

# 4. Verify it started
./scripts/daemon_status.sh
```

### Verification Checklist

After restarting daemon, run `./scripts/daemon_status.sh` and verify:
- [ ] Status shows "✅ RUNNING" (not multiple instances)
- [ ] Logs show "Daemon server started on var/daemon.sock"
- [ ] Zombie assignments were released (if any shown in logs)
- [ ] No error messages in recent activity
- [ ] Test with actual command to confirm new code behavior

**DO NOT test in production without restarting daemon after code changes!**

## Project Overview

SpaceTraders V2 autonomous fleet management bot built with:
- **Hexagonal Architecture** (Ports & Adapters)
- **Domain-Driven Design** (DDD)
- **CQRS** (Command Query Responsibility Segregation) via custom pymediatr implementation
- **BDD Testing** with pytest-bdd
- **Python 3.12**

This is a clean rewrite focusing on architectural principles and testability.

## Development Commands

### Environment Setup

This project uses `uv` for fast, reliable dependency management.

**Option A: Using uv run (recommended, no activation needed):**
```bash
# Run commands directly with uv
uv run pytest tests/
uv run ./spacetraders --help
```

**Option B: Traditional activation:**
```bash
# Activate virtual environment
source .venv/bin/activate  # uv creates .venv by default
```

**First-time setup:**
```bash
# Install uv if not already installed
curl -LsSf https://astral.sh/uv/install.sh | sh

# Create virtual environment and install dependencies
uv sync
```

### Daemon Management Scripts

The `scripts/` folder contains helper scripts for managing the daemon server:

```bash
./scripts/daemon_status.sh       # Check daemon status and view recent logs
./scripts/start_daemon.sh        # Start the daemon server
./scripts/stop_daemon.sh         # Stop the daemon server
./scripts/restart_daemon.sh      # Restart daemon (kill + start + verify)
```

**Always use these scripts for daemon management!** They handle:
- Multiple instance detection and cleanup
- PostgreSQL connection from `.env`
- Socket file verification
- Process monitoring and logging

### Testing

Run all tests:
```bash
uv run pytest tests/
```

Run specific test directory:
```bash
uv run pytest tests/bdd/ -v
```

Run specific test file:
```bash
uv run pytest tests/bdd/steps/shared/test_player_steps.py -v
```

Run tests with coverage:
```bash
uv run pytest --cov=src/spacetraders --cov-report=html
```

### Running the Application

#### Player Management
```bash
# Register a new player
uv run ./spacetraders player register --agent AGENT-1 --token TOKEN-123
```

#### Navigation
```bash
# Navigate ship between waypoints
uv run ./spacetraders navigate --from X1-A1 --to X1-B2
```

#### Ship Purchasing
```bash
# List available ships at a shipyard
uv run ./spacetraders shipyard list --waypoint X1-GZ7-AB12 --agent YOUR_AGENT

# Purchase a single ship
uv run ./spacetraders shipyard purchase \
  --ship YOUR_AGENT-1 \
  --shipyard X1-GZ7-AB12 \
  --type SHIP_MINING_DRONE \
  --agent YOUR_AGENT

# Batch purchase multiple ships with budget constraint
uv run ./spacetraders shipyard batch \
  --ship YOUR_AGENT-1 \
  --shipyard X1-GZ7-AB12 \
  --type SHIP_MINING_DRONE \
  --quantity 5 \
  --max-budget 500000 \
  --agent YOUR_AGENT
```

View all CLI options:
```bash
uv run ./spacetraders --help
```

### Environment Variables

Required for API operations:
```bash
export SPACETRADERS_TOKEN="your-token-here"
```

## Architecture Overview

### Layer Structure

```
src/spacetraders/
├── domain/              # Pure business logic (no dependencies)
│   ├── shared/          # Shared kernel (Player, Ship, value objects)
│   └── navigation/      # Navigation domain logic
│
├── application/         # Use cases via CQRS
│   ├── player/
│   │   ├── commands/    # Write operations (RegisterPlayer, UpdatePlayer)
│   │   └── queries/     # Read operations (GetPlayer, ListPlayers)
│   ├── navigation/
│   │   ├── commands/    # NavigateShip, DockShip, OrbitShip, RefuelShip
│   │   └── queries/     # PlanRoute, GetShipLocation, GetSystemGraph
│   ├── shipyard/
│   │   ├── commands/    # PurchaseShip, BatchPurchaseShips
│   │   └── queries/     # GetShipyardListings
│   └── common/          # Pipeline behaviors (LoggingBehavior, ValidationBehavior)
│
├── ports/               # Interfaces
│   ├── outbound/        # Repository and infrastructure interfaces
│   ├── repositories.py  # Repository port definitions
│   └── routing_engine.py # Routing engine interface
│
├── adapters/            # Infrastructure implementations
│   ├── primary/         # Driving adapters
│   │   └── cli/         # CLI interface (main.py, player_cli.py, navigation_cli.py, shipyard_cli.py)
│   └── secondary/       # Driven adapters
│       ├── persistence/ # SQLite repositories (Database, PlayerRepository, ShipRepository)
│       ├── api/         # SpaceTraders API client with rate limiting
│       └── routing/     # OR-Tools routing engine (GraphBuilder, GraphProvider, ORToolsEngine)
│
└── configuration/       # Dependency injection
    ├── container.py     # Singleton DI container with mediator setup
    └── settings.py      # Application settings
```

### CQRS Pattern with pymediatr

This project uses a custom pymediatr implementation (src/pymediatr.py) for CQRS:

**Commands** (write operations):
- Immutable dataclasses inheriting from `Request[TResponse]`
- Handled by `RequestHandler[TRequest, TResponse]`
- Placed in `application/*/commands/`
- Example: `RegisterPlayerCommand` → `RegisterPlayerHandler`

**Queries** (read operations):
- Same structure as commands but in `application/*/queries/`
- Example: `GetPlayerQuery` → `GetPlayerHandler`

**Pipeline Behaviors** (middleware):
- `LoggingBehavior`: Logs all requests and responses
- `ValidationBehavior`: Validates requests before execution
- Registered in `configuration/container.py`

**Mediator Usage**:
```python
from spacetraders.configuration.container import get_mediator
from spacetraders.application.player.commands.register_player import RegisterPlayerCommand

mediator = get_mediator()
command = RegisterPlayerCommand(agent_symbol="AGENT-1", token="TOKEN-123")
player = await mediator.send_async(command)
```

### Domain Layer Principles

**Entities** (domain/shared/):
- `Player`: Player aggregate with metadata and credits management
- `Ship`: Ship entity with navigation state machine (DOCKED → IN_ORBIT → IN_TRANSIT)

**Value Objects**:
- `Waypoint`: Immutable location (symbol, x, y)
- `Fuel`: Fuel state (current, capacity)
- `FlightMode`: Navigation mode (CRUISE, DRIFT, BURN, STEALTH)
- `Shipyard`: Shipyard data (symbol, ship_types, listings, transactions, modification_fee)
- `ShipListing`: Ship available for purchase (ship_type, name, description, purchase_price, frame, reactor, engine, modules, mounts)

**Invariants** enforced by domain:
- Ship navigation state machine transitions
- Fuel capacity limits
- Ship cargo capacity limits
- Valid flight modes and nav statuses
- Player credits cannot be negative (enforced via `spend_credits()`)
- Ship purchases validate credit availability before transaction

**Domain Services**:
- OR-Tools routing engine for optimal pathfinding
- System graph provider for navigation graph caching

### Dependency Injection

All dependencies are managed through `configuration/container.py`:
- Singleton instances for repositories, API client, mediator
- Use `get_mediator()` for CQRS operations
- Use `reset_container()` in tests for clean state

### Testing Strategy

**BDD Tests** (tests/bdd/):
- Gherkin feature files in `features/*/`
- Step definitions in `steps/*/`
- Use pytest-bdd for scenario execution

**Test Structure**:
```
tests/bdd/
├── features/
│   ├── domain/
│   │   ├── player.feature
│   │   └── shipyard/shipyard_value_objects.feature
│   ├── application/
│   │   ├── navigation/route_planning.feature, fuel_management.feature
│   │   └── shipyard/get_shipyard_listings.feature, purchase_ship.feature, batch_purchase_ships.feature
│   └── integration/
│       └── cli/test_shipyard_cli.py
└── steps/
    ├── domain/test_player_steps.py, test_shipyard_value_objects_steps.py
    ├── application/
    │   ├── navigation/test_route_planning_steps.py
    │   └── shipyard/test_shipyard_steps.py, test_purchase_ship_steps.py
    └── integration/cli/test_shipyard_cli.py
```

**Test Fixtures**:
- `context` fixture in conftest.py provides shared state
- Mock repositories for unit testing handlers
- Real database instances for integration tests

## Key Patterns and Conventions

### File Organization
- One command/query per file with its handler
- Handlers are co-located with their requests
- Example: `register_player.py` contains both `RegisterPlayerCommand` and `RegisterPlayerHandler`

### Naming Conventions
- Commands: `VerbNounCommand` (RegisterPlayerCommand, NavigateShipCommand)
- Queries: `VerbNounQuery` (GetPlayerQuery, PlanRouteQuery)
- Handlers: `{Request}Handler` (RegisterPlayerHandler, GetPlayerHandler)
- Repositories: `{Entity}Repository` (PlayerRepository, ShipRepository)

### Domain Exceptions
- All domain exceptions inherit from `DomainException` (domain/shared/exceptions.py)
- Ship exceptions: `InvalidNavStatusError`, `InsufficientFuelError`, `FuelCapacityExceededError`
- Player exceptions: `InsufficientCreditsError`
- Purchasing exceptions: `ShipNotAvailableError`, `ShipyardNotFoundError`

### Async/Await
- All handler methods are async: `async def handle(self, request) -> Response`
- Use `await mediator.send_async(request)` for dispatching
- CLI commands wrap async calls with `asyncio.run()`

### Database
- **Production:** PostgreSQL at `localhost:5432/spacetraders` (DATABASE_URL in `.env`)
- **Tests:** SQLite in-memory (`:memory:`) - automatically used when running tests
- Managed by `Database` class (adapters/secondary/persistence/database.py)
- Schema initialization on first run
- Repositories handle domain object mapping via SQLAlchemy ORM
- **NEVER use SQLite for production!** It's test-only.

## Adding New Features

### Adding a New Command

1. Create command file in `application/{context}/commands/{name}.py`:
```python
from dataclasses import dataclass
from pymediatr import Request, RequestHandler

@dataclass(frozen=True)
class MyCommand(Request):
    param: str

class MyCommandHandler(RequestHandler[MyCommand, ResultType]):
    def __init__(self, repository):
        self._repo = repository

    async def handle(self, request: MyCommand) -> ResultType:
        # Implementation
        pass
```

2. Register handler in `configuration/container.py` in `get_mediator()`:
```python
_mediator.register_handler(
    MyCommand,
    lambda: MyCommandHandler(some_repo)
)
```

3. Add CLI command in appropriate CLI module (`adapters/primary/cli/`)

4. Write BDD test in `tests/bdd/features/{context}/` and step definitions

### Adding a New Query

Same process as commands, but place in `application/{context}/queries/`

### Adding a New Domain Entity

1. Create entity in `domain/shared/{entity}.py`
2. Add repository interface in `ports/outbound/`
3. Implement repository in `adapters/secondary/persistence/`
4. Add mapper for persistence in `adapters/secondary/persistence/mappers.py`
5. Register repository in `configuration/container.py`

## Important Notes

- **Domain logic belongs in domain layer**: Keep handlers thin, push logic into entities
- **Immutability**: All commands/queries are frozen dataclasses
- **Dependencies flow inward**: Domain has no dependencies; application depends on domain; adapters depend on both
- **Testing isolation**: Use `reset_container()` between tests
- **PYTHONPATH**: Always set `PYTHONPATH=src:$PYTHONPATH` when running pytest directly

## Documentation

Key architecture docs in `docs/`:
- `CQRS_ARCHITECTURE.md`: Detailed CQRS patterns and examples
- `IMPLEMENTATION_PLAN.md`: Full implementation roadmap
- `PHASE1_COMPLETED.md`: Walking skeleton completion details

## Account & Authentication

### SpaceTraders Account Token

The **account token** (different from agent tokens) is stored in `.env.account` and is used to register new agents via the SpaceTraders API.

**File location:** `.env.account` (**DO NOT COMMIT TO GIT**)

**Format:**
```bash
SPACETRADERS_ACCOUNT_TOKEN=eyJhbGci...
```

**To register a new agent:**
```bash
uv run python << 'EOF'
import requests
import json

# Load account token from .env.account
with open('.env.account', 'r') as f:
    for line in f:
        if line.startswith('SPACETRADERS_ACCOUNT_TOKEN'):
            ACCOUNT_TOKEN = line.split('=', 1)[1].strip()
            break

response = requests.post(
    'https://api.spacetraders.io/v2/register',
    headers={
        'Authorization': f'Bearer {ACCOUNT_TOKEN}',
        'Content-Type': 'application/json'
    },
    json={
        'symbol': 'YOUR_AGENT_NAME',  # Cyberpunk names work great!
        'faction': 'COSMIC'  # or VOID, GALACTIC, QUANTUM, etc.
    }
)

data = response.json()
if response.status_code == 201:
    agent_data = data['data']
    print(f"✅ Agent: {agent_data['agent']['symbol']}")
    print(f"✅ Credits: {agent_data['agent']['credits']}")
    print(f"✅ Token: {agent_data['token']}")
    print(f"✅ Ships: {agent_data['agent']['shipCount']}")
else:
    print(f"❌ Error: {data}")
EOF
```

### Token Storage Strategy

**Account Token:** `.env.account` (for registering new agents via API)
**Agent Tokens:** Stored in SQLite database at `var/spacetraders.db` in the `players` table
**No environment variables needed for agents:** The bot reads agent tokens directly from the database using player_id

**Query players:**
```bash
sqlite3 var/spacetraders.db "SELECT player_id, agent_symbol FROM players;"
```

**Register agent token in database after API registration:**
```bash
export SPACETRADERS_TOKEN=dummy  # Workaround for container init
uv run python -m spacetraders.adapters.primary.cli.main player register \
  --agent AGENT_SYMBOL \
  --token AGENT_TOKEN_FROM_API
```

### Important: .gitignore

Ensure `.env.account` is in `.gitignore` to avoid committing the account token:
```
.env*
*.db
var/
```
