# SpaceTraders V2 Implementation Plan

**Architecture**: Hexagonal Architecture (Ports & Adapters) + Domain-Driven Design (DDD) + CQRS
**Pattern**: Command/Query Handlers using pymediatr
**Approach**: Walking Skeleton → Domain-First → Infrastructure Last
**Testing**: BDD with pytest-bdd from day 1

---

## Why We're Starting Over

### Background: The Original Bot (`bot/`)

The original SpaceTraders bot (located in `/Users/andres.camacho/Development/Personal/spacetradersV2/bot/`) successfully implemented autonomous fleet management with DDD, achieving ~3,356 lines of BDD tests across Trading, Mining, and Contracts domains. However, it suffers from critical architectural issues:

**Problem #1: Infrastructure Doing Business Logic**
- `SmartNavigator` (856 lines) mixes route planning, validation, and execution
- Business logic (route planning, state machine) lives in `core/` infrastructure layer
- Violates Clean Architecture: domain depends on infrastructure

**Problem #2: Domain Models Are Dead Code**
- Rich domain `Ship` entity exists with methods like `can_navigate_to()`, `depart_to()`, `needs_refuel_for_journey()`
- **None of these methods are ever called** - operations bypass domain layer entirely
- Operations call `SmartNavigator` directly, making domain models useless decorations

**Problem #3: Massive Code Duplication**
- Fuel rates defined in 3 places: `domain/shared/value_objects.py` (DRIFT=0.003), `config/routing_constants.yaml` (DRIFT=0.003), `core/route_planner.py` (FuelCalculator)
- Distance calculations duplicated in 4+ locations
- Flight mode selection: Domain `FlightMode` vs YAML `flight_modes` vs legacy `select_flight_mode()`
- **Real Bug**: Mining profit calculator uses wrong DRIFT rate (0.001 instead of 0.003) due to duplication

**Problem #4: Application Services Add Redundant Layer**
- Application Services orchestrate domain + infrastructure
- But with CQRS, Command/Query Handlers already provide orchestration
- Services become thin wrappers around Handlers, adding no value
- Example: `TradingService.execute_route()` just calls domain methods + repository

**Problem #5: Testing Is Difficult**
- Can't test route planning without `SmartNavigator` (requires API client, database)
- Domain tests require infrastructure setup
- No clear boundary between layers

### The Navigation Refactoring (`domain-navigation-refactor` branch)

The `domain-navigation-refactor` branch (located in `/Users/andres.camacho/Development/Personal/spacetradersV2/.worktrees/domain-navigation-refactor/bot`) partially addressed these issues:

✅ **What It Fixed:**
- Moved route planning to domain: `Ship.plan_route()` (~1,820 lines of proven code)
- Created immutable domain `Route` value object with validation
- Domain `ORToolsPlanner` service treats OR-Tools as algorithm library
- `NavigationService` provides thin orchestration layer
- Eliminated duplication: domain `FlightMode` as single source of truth

❌ **What Remains:**
- Still uses Application Services (thin wrappers)
- Original bot's other domains (Trading, Mining, Contracts) still have architectural issues
- 856-line `SmartNavigator` still exists in codebase
- Migration only 50% complete (Phases 1-5 of 9)

### Bot V2: Clean Slate with Proven Patterns

Rather than continuing the incremental migration, **bot-v2** rebuilds from scratch using lessons learned:

✅ **What We're Fixing:**
1. **CQRS from Day 1**: Commands/Queries handled by Handlers (no redundant services layer)
2. **Domain-First**: Rich domain models actually used (not bypassed)
3. **Single Source of Truth**: No duplication - domain objects are the source
4. **Hexagonal Architecture**: True ports & adapters with dependency inversion
5. **Proven Navigation**: Copy ~1,820 lines of working domain navigation from refactor branch
6. **Incremental Build**: Walking Skeleton → Navigation → Contracts → Trading → Mining

✅ **What We're Keeping:**
- Daemon/container architecture (85% memory reduction vs independent processes)
- BDD testing strategy (100% pytest-bdd coverage)
- Domain models (Ship, Route, TradeRoute, MiningCycle, Contract aggregates)
- OR-Tools optimization for routing

**Goal**: Clean architecture with NO dead code, NO duplication, NO infrastructure doing business logic, and NO redundant layers.

---

## Table of Contents

1. [Architecture Overview](#architecture-overview)
2. [CQRS with pymediatr](#cqrs-with-pymediatr)
3. [Project Structure](#project-structure)
4. [Phase 1: Walking Skeleton](#phase-1-walking-skeleton)
5. [Phase 2: Player Registration](#phase-2-player-registration)
6. [Phase 3: Navigation Domain](#phase-3-navigation-domain)
7. [Phase 4: Navigation Infrastructure](#phase-4-navigation-infrastructure)
8. [Phase 5: Daemon Infrastructure](#phase-5-daemon-infrastructure)
9. [Phase 6: Future Bounded Contexts](#phase-6-future-bounded-contexts)
10. [Testing Strategy](#testing-strategy)
11. [Key Principles](#key-principles)

---

## Architecture Overview

### Hexagonal Architecture (Ports & Adapters) + CQRS

```
┌─────────────────────────────────────────────────────────────┐
│                     Primary Adapters                         │
│              (Driving Side - User Interfaces)                │
│                                                              │
│     ┌──────────┐  ┌──────────┐  ┌──────────┐              │
│     │   CLI    │  │  Daemon  │  │   MCP    │              │
│     │  Adapter │  │  Server  │  │  Bridge  │              │
│     └─────┬────┘  └────┬─────┘  └────┬─────┘              │
│           │            │             │                       │
└───────────┼────────────┼─────────────┼───────────────────────┘
            │            │             │
            │            │             │ Send Commands/Queries
            ▼            ▼             ▼
    ┌───────────────────────────────────────────┐
    │           Mediator (pymediatr)            │
    │        Routes Requests to Handlers        │
    │                                           │
    │  - Pipeline Behaviors (middleware)       │
    │    • LoggingBehavior                     │
    │    • ValidationBehavior                  │
    │    • TransactionBehavior                 │
    └───────────────┬───────────────────────────┘
                    │
                    │ Dispatches to
                    ▼
    ┌───────────────────────────────────────────┐
    │      Command/Query Handlers (CQRS)        │
    │         Application Layer                 │
    │                                           │
    │  Commands (Write):                       │
    │  - RegisterPlayerHandler                 │
    │  - NavigateShipHandler                   │
    │  - AcceptContractHandler                 │
    │                                           │
    │  Queries (Read):                         │
    │  - GetPlayerHandler                      │
    │  - GetShipLocationHandler                │
    │  - ListContractsHandler                  │
    └───────────────┬───────────────────────────┘
                    │
                    │ Uses Domain + Repositories
                    ▼
    ┌───────────────────────────────────────────┐
    │         Domain Layer                      │
    │      (Pure Business Logic)                │
    │                                           │
    │  - Entities (Player, Ship, Route)        │
    │  - Value Objects (Fuel, Waypoint)        │
    │  - Domain Services (ORToolsPlanner)      │
    │  - Domain Events                         │
    └───────────────┬───────────────────────────┘
                    │
                    │ Defines Interfaces
                    ▼
    ┌───────────────────────────────────────────┐
    │         Outbound Ports (Interfaces)       │
    │                                           │
    │  - IPlayerRepository                     │
    │  - IShipRepository                       │
    │  - IGraphProvider                        │
    │  - ISpaceTradersAPI                      │
    └───────────────┬───────────────────────────┘
                    │
                    │ Implemented by
                    ▼
┌───────────────────┼───────────────────────────────────────┐
│                   │      Secondary Adapters                │
│                   │   (Driven Side - Infrastructure)       │
│                   │                                        │
│     ┌─────────────▼─────────┐  ┌──────────────────────┐  │
│     │   Persistence          │  │   External Services  │  │
│     │                        │  │                      │  │
│     │ - SQLite Repositories  │  │ - SpaceTraders API  │  │
│     │ - SystemGraph Provider │  │ - OR-Tools (domain) │  │
│     │ - Ship Controller      │  │                      │  │
│     └────────────────────────┘  └──────────────────────┘  │
│                                                            │
└────────────────────────────────────────────────────────────┘
```

### Key Principles

1. **Dependency Rule**: Domain → Application (Handlers) → Adapters (never reverse)
2. **CQRS Separation**: Commands (write) and Queries (read) handled separately
3. **Mediator Pattern**: pymediatr routes requests to handlers with pipeline behaviors
4. **Port Isolation**: Domain defines interfaces, infrastructure implements
5. **Testability**: Domain testable without infrastructure, handlers testable with mocks
6. **Flexibility**: Swap adapters without changing domain or handlers
7. **Multi-Tenancy**: All operations scoped by `player_id`

---

## CQRS with pymediatr

### What is CQRS?

**Command Query Responsibility Segregation** separates write operations (Commands) from read operations (Queries):

- **Commands**: Change system state (e.g., RegisterPlayer, NavigateShip, AcceptContract)
- **Queries**: Return data without side effects (e.g., GetPlayer, GetShipLocation, ListContracts)
- **Handlers**: Each Command/Query has one Handler that orchestrates domain + infrastructure

### Why pymediatr?

[pymediatr](https://github.com/megamarc/pymediatr) is a Python implementation of the MediatR pattern from .NET, providing:

1. **Request/Response Pattern**: Commands/Queries inherit from `Request[TResponse]`
2. **Handler Registration**: Handlers inherit from `RequestHandler[TRequest, TResponse]`
3. **Pipeline Behaviors**: Middleware for cross-cutting concerns (logging, validation, transactions)
4. **Dependency Injection**: Handlers receive dependencies via constructor

### Architecture Pattern

```python
# 1. Command/Query (immutable request)
@dataclass(frozen=True)
class NavigateShipCommand(Request[NavigationResult]):
    ship_symbol: str
    destination: str
    player_id: int

# 2. Handler (orchestrates domain + infrastructure)
class NavigateShipHandler(RequestHandler[NavigateShipCommand, NavigationResult]):
    def __init__(
        self,
        ship_repository: IShipRepository,
        graph_provider: IGraphProvider,
        ship_controller: IShipController
    ):
        self._ship_repo = ship_repository
        self._graph_provider = graph_provider
        self._controller = ship_controller

    async def handle(self, request: NavigateShipCommand) -> NavigationResult:
        # 1. Load ship from repository
        ship = self._ship_repo.get_ship(request.ship_symbol, request.player_id)

        # 2. Load system graph
        graph = self._graph_provider.get_graph(ship.current_location.system_symbol)

        # 3. Plan route (DOMAIN)
        destination = Waypoint(symbol=request.destination, ...)
        route = ship.plan_route(destination, graph)

        # 4. Execute route (INFRASTRUCTURE)
        for step in route.steps:
            if step.action == "navigate":
                ship.ensure_in_orbit()
                await self._controller.navigate(ship.symbol, step.to_waypoint.symbol)
                ship.arrive()
            elif step.action == "refuel":
                ship.ensure_docked()
                await self._controller.refuel(ship.symbol)
                ship.refuel(step.fuel_amount)

        # 5. Persist updated ship
        self._ship_repo.update_ship(ship)

        return NavigationResult(success=True, ship=ship, route=route)

# 3. Usage (CLI sends command via mediator)
mediator = Mediator()
command = NavigateShipCommand(
    ship_symbol="SHIP-1",
    destination="X1-A1-B2",
    player_id=1
)
result = await mediator.send_async(command)
```

### Pipeline Behaviors (Middleware)

Pipeline behaviors wrap handlers with cross-cutting concerns:

```python
# Logging behavior
class LoggingBehavior(PipelineBehavior):
    async def handle(self, request, next_handler):
        logger.info(f"Handling {type(request).__name__}")
        try:
            result = await next_handler()
            logger.info(f"Completed {type(request).__name__}")
            return result
        except Exception as e:
            logger.error(f"Failed {type(request).__name__}: {e}")
            raise

# Validation behavior
class ValidationBehavior(PipelineBehavior):
    async def handle(self, request, next_handler):
        # Validate command/query
        if hasattr(request, 'validate'):
            request.validate()
        return await next_handler()

# Transaction behavior
class TransactionBehavior(PipelineBehavior):
    def __init__(self, db_connection):
        self._db = db_connection

    async def handle(self, request, next_handler):
        # Only wrap Commands (writes), not Queries
        if isinstance(request, Command):
            self._db.begin_transaction()
            try:
                result = await next_handler()
                self._db.commit()
                return result
            except Exception:
                self._db.rollback()
                raise
        else:
            return await next_handler()
```

### Why No Application Services?

**Original bot had redundant layer:**
```python
# Application Service (thin wrapper)
class NavigationService:
    def navigate_to(self, ship_symbol: str, destination: str):
        # Just calls handler or domain methods
        # Adds no business value
        pass
```

**Bot-v2 eliminates redundancy:**
- **Handlers ARE the application layer** - they orchestrate domain + infrastructure
- **No thin wrappers** - handlers directly coordinate use cases
- **Pipeline behaviors** handle cross-cutting concerns (logging, validation, transactions)
- **Cleaner architecture** - fewer layers, clearer responsibilities

### Domain-Driven Design Concepts

**Entities**: Objects with identity and lifecycle (Player, Ship, Route)
**Value Objects**: Immutable objects defined by attributes (Fuel, Waypoint, FlightMode)
**Aggregates**: Cluster of entities/VOs with root entity (Route owns RouteSegments)
**Domain Services**: Stateless operations that don't belong to entities
**Repositories**: Abstraction for aggregate persistence
**Bounded Contexts**: Logical boundaries (Navigation, Contracts, Trading, Mining)

---

## Project Structure

```
bot-v2/
├── src/spacetraders/
│   ├── __init__.py
│   │
│   ├── domain/                     # Pure business logic (no dependencies)
│   │   ├── __init__.py
│   │   ├── shared/                 # Shared kernel
│   │   │   ├── __init__.py
│   │   │   ├── player.py           # Player entity
│   │   │   ├── ship.py             # Ship entity
│   │   │   ├── value_objects.py    # Waypoint, Fuel, Cargo, etc.
│   │   │   └── exceptions.py       # Domain exceptions
│   │   │
│   │   ├── navigation/             # Navigation bounded context
│   │   │   ├── __init__.py
│   │   │   ├── route.py            # Route aggregate
│   │   │   ├── value_objects.py    # RouteSegment, NavigationPlan
│   │   │   ├── services.py         # Domain services
│   │   │   └── exceptions.py
│   │   │
│   │   └── contracts/              # Future: Contracts bounded context
│   │
│   ├── application/                # CQRS Handlers (use case orchestration)
│   │   ├── __init__.py
│   │   ├── common/                 # Shared application logic
│   │   │   ├── __init__.py
│   │   │   ├── base.py             # Base Command/Query classes
│   │   │   └── behaviors.py        # Pipeline behaviors (logging, validation, etc.)
│   │   │
│   │   ├── player/                 # Player bounded context handlers
│   │   │   ├── __init__.py
│   │   │   ├── commands/           # Player write operations
│   │   │   │   ├── __init__.py
│   │   │   │   ├── register_player.py      # RegisterPlayerCommand + Handler
│   │   │   │   └── update_player_token.py  # UpdatePlayerTokenCommand + Handler
│   │   │   │
│   │   │   └── queries/            # Player read operations
│   │   │       ├── __init__.py
│   │   │       ├── get_player.py          # GetPlayerQuery + Handler
│   │   │       └── list_players.py        # ListPlayersQuery + Handler
│   │   │
│   │   └── navigation/             # Navigation bounded context handlers
│   │       ├── __init__.py
│   │       ├── commands/           # Navigation write operations
│   │       │   ├── __init__.py
│   │       │   ├── navigate_ship.py       # NavigateShipCommand + Handler
│   │       │   ├── dock_ship.py           # DockShipCommand + Handler
│   │       │   ├── orbit_ship.py          # OrbitShipCommand + Handler
│   │       │   └── refuel_ship.py         # RefuelShipCommand + Handler
│   │       │
│   │       └── queries/            # Navigation read operations
│   │           ├── __init__.py
│   │           ├── get_ship_location.py   # GetShipLocationQuery + Handler
│   │           ├── plan_route.py          # PlanRouteQuery + Handler
│   │           └── get_system_graph.py    # GetSystemGraphQuery + Handler
│   │
│   ├── ports/                      # Interfaces (domain defines, infrastructure implements)
│   │   ├── __init__.py
│   │   ├── repositories.py         # Repository interfaces
│   │   │                           # - IPlayerRepository
│   │   │                           # - IShipRepository
│   │   │                           # - IRouteRepository
│   │   │
│   │   ├── api_client.py           # SpaceTraders API interface
│   │   ├── ship_controller.py      # Ship control operations interface
│   │   └── graph_provider.py       # System graph provider interface
│   │
│   ├── adapters/                   # Infrastructure implementations
│   │   ├── __init__.py
│   │   │
│   │   ├── primary/                # Driving adapters
│   │   │   ├── __init__.py
│   │   │   │
│   │   │   ├── cli/                # Command-line interface
│   │   │   │   ├── __init__.py
│   │   │   │   ├── main.py         # Entry point
│   │   │   │   ├── player_cli.py
│   │   │   │   └── navigation_cli.py
│   │   │   │
│   │   │   ├── daemon/             # Container orchestration
│   │   │   │   ├── __init__.py
│   │   │   │   ├── daemon_server.py
│   │   │   │   ├── container_manager.py
│   │   │   │   ├── container_commands.py
│   │   │   │   ├── assignment_manager.py
│   │   │   │   └── daemon_client.py
│   │   │   │
│   │   │   └── mcp_bridge/         # MCP integration
│   │   │       ├── __init__.py
│   │   │       └── bridge.py
│   │   │
│   │   └── secondary/              # Driven adapters
│   │       ├── __init__.py
│   │       │
│   │       ├── persistence/        # Database implementations
│   │       │   ├── __init__.py
│   │       │   ├── database.py     # SQLite connection
│   │       │   ├── player_repository.py
│   │       │   ├── route_repository.py
│   │       │   ├── graph_repository.py
│   │       │   └── mappers.py      # Domain ↔ DB mappers
│   │       │
│   │       ├── api/                # SpaceTraders API client
│   │       │   ├── __init__.py
│   │       │   ├── client.py
│   │       │   └── rate_limiter.py
│   │       │
│   │       └── routing/            # OR-Tools implementations
│   │           ├── __init__.py
│   │           ├── ortools_engine.py
│   │           ├── ortools_router.py
│   │           ├── ortools_tsp.py
│   │           ├── graph_builder.py
│   │           └── graph_provider.py
│   │
│   └── configuration/              # Dependency injection
│       ├── __init__.py
│       ├── container.py            # DI container
│       └── settings.py             # Configuration
│
├── tests/
│   ├── __init__.py
│   ├── conftest.py                 # Global fixtures
│   │
│   └── bdd/
│       ├── __init__.py
│       ├── conftest.py
│       │
│       ├── features/               # Gherkin feature files
│       │   ├── shared/
│       │   │   ├── player.feature
│       │   │   └── ship.feature
│       │   │
│       │   └── navigation/
│       │       ├── route_planning.feature
│       │       ├── navigation_execution.feature
│       │       └── refuel_planning.feature
│       │
│       └── steps/                  # Step definitions
│           ├── __init__.py
│           ├── shared/
│           │   ├── conftest.py
│           │   ├── test_player_steps.py
│           │   └── test_ship_steps.py
│           │
│           └── navigation/
│               ├── conftest.py
│               ├── test_route_planning_steps.py
│               └── test_navigation_execution_steps.py
│
├── config/
│   └── routing.yaml                # Hot-reloadable routing config
│
├── var/                            # Runtime data (gitignored)
│   ├── spacetraders.db            # SQLite database
│   ├── daemon.sock                # Unix socket
│   └── logs/
│
├── docs/
│   ├── IMPLEMENTATION_PLAN.md     # This document
│   ├── ARCHITECTURE.md            # Detailed architecture docs
│   └── API.md                     # API documentation
│
├── pyproject.toml                  # Package definition
├── README.md
└── .gitignore
```

---

## Phase 1: Walking Skeleton

**Goal**: Prove the architecture works end-to-end with a minimal vertical slice.

### 1.1 Bootstrap Project

**Create project structure**:
```bash
mkdir -p bot-v2/{src/spacetraders,tests/bdd,config,var,docs}
cd bot-v2
```

**Create `pyproject.toml`**:
```toml
[build-system]
requires = ["setuptools>=61.0"]
build-backend = "setuptools.build_backend"

[project]
name = "spacetraders"
version = "2.0.0"
description = "SpaceTraders autonomous fleet management (V2)"
requires-python = ">=3.12"
dependencies = [
    "requests>=2.31.0",
    "ortools>=9.7.0",
    "pyyaml>=6.0",
    "python-dateutil>=2.8.0",
]

[project.optional-dependencies]
dev = [
    "pytest>=7.4.0",
    "pytest-bdd>=6.1.0",
    "pytest-cov>=4.1.0",
    "pytest-asyncio>=0.21.0",
]

[project.scripts]
spacetraders = "spacetraders.adapters.primary.cli.main:main"

[tool.pytest.ini_options]
testpaths = ["tests"]
python_files = "test_*.py"
python_classes = "Test*"
python_functions = "test_*"
```

**Setup virtual environment**:
```bash
python3.12 -m venv venv
source venv/bin/activate
pip install -e .[dev]
```

### 1.2 Create Stub Domain

**`src/spacetraders/domain/shared/value_objects.py`** (stub):
```python
from dataclasses import dataclass

@dataclass(frozen=True)
class Waypoint:
    """Stub waypoint value object"""
    symbol: str
    x: float = 0.0
    y: float = 0.0
```

**`src/spacetraders/domain/navigation/route.py`** (stub):
```python
from dataclasses import dataclass
from typing import List

@dataclass
class RouteSegment:
    """Stub route segment"""
    from_waypoint: str
    to_waypoint: str
    distance: float

class Route:
    """Stub route aggregate"""
    def __init__(self, route_id: str, segments: List[RouteSegment]):
        self.route_id = route_id
        self.segments = segments

    def total_distance(self) -> float:
        return sum(seg.distance for seg in self.segments)
```

### 1.3 Create Stub Query Handler

**Note**: For the walking skeleton, we use a simple stub handler to prove the architecture. In Phase 2+, we'll implement full CQRS with pymediatr.

**`src/spacetraders/application/navigation/queries/plan_route.py`** (stub):
```python
from dataclasses import dataclass
from typing import List
from ...domain.navigation.route import Route, RouteSegment

@dataclass(frozen=True)
class PlanRouteQuery:
    """Stub query - will become Request[Route] in Phase 2"""
    start: str
    destination: str

class PlanRouteHandler:
    """Stub handler - returns hardcoded route for walking skeleton"""

    def handle(self, query: PlanRouteQuery) -> Route:
        """Returns hardcoded route for testing architecture"""
        segment = RouteSegment(
            from_waypoint=query.start,
            to_waypoint=query.destination,
            distance=100.0
        )
        return Route(route_id="ROUTE-STUB-1", segments=[segment])
```

### 1.4 Create Minimal CLI Adapter

**`src/spacetraders/adapters/primary/cli/main.py`**:
```python
#!/usr/bin/env python3
import argparse
from spacetraders.application.navigation.queries.plan_route import (
    PlanRouteQuery,
    PlanRouteHandler
)

def main():
    parser = argparse.ArgumentParser(description="SpaceTraders V2")
    subparsers = parser.add_subparsers(dest="command")

    # Navigate command
    nav_parser = subparsers.add_parser("navigate")
    nav_parser.add_argument("--from", dest="start", required=True)
    nav_parser.add_argument("--to", dest="destination", required=True)

    args = parser.parse_args()

    if args.command == "navigate":
        # Walking skeleton: Direct handler invocation
        # Phase 2+: Use pymediatr Mediator
        handler = PlanRouteHandler()
        query = PlanRouteQuery(start=args.start, destination=args.destination)
        route = handler.handle(query)

        print(f"✅ Planned route {route.route_id}")
        print(f"   From: {route.segments[0].from_waypoint}")
        print(f"   To: {route.segments[0].to_waypoint}")
        print(f"   Distance: {route.total_distance():.1f} units")
    else:
        parser.print_help()

if __name__ == "__main__":
    main()
```

### 1.5 Test the Skeleton

**Manual test**:
```bash
spacetraders navigate --from X1-A1 --to X1-B2
```

**Expected output**:
```
✅ Planned route ROUTE-STUB-1
   From: X1-A1
   To: X1-B2
   Distance: 100.0 units
```

### 1.6 Write First BDD Test

**`tests/bdd/features/navigation/route_planning.feature`**:
```gherkin
Feature: Route Planning
  As a ship operator
  I want to plan navigation routes
  So that I can travel efficiently between waypoints

  Scenario: Plan simple route
    When I plan a route from "X1-A1" to "X1-B2"
    Then a route should be created
    And the route should have 1 segment
    And the total distance should be greater than 0
```

**`tests/bdd/steps/navigation/test_route_planning_steps.py`**:
```python
from pytest_bdd import scenario, when, then, parsers
from spacetraders.application.services.navigation_service import NavigationService

@scenario("../../features/navigation/route_planning.feature", "Plan simple route")
def test_plan_simple_route():
    pass

@when(parsers.parse('I plan a route from "{start}" to "{destination}"'))
def plan_route(context, start, destination):
    service = NavigationService()
    context["route"] = service.plan_route(start, destination)

@then("a route should be created")
def check_route_created(context):
    assert context["route"] is not None
    assert context["route"].route_id is not None

@then("the route should have 1 segment")
def check_segment_count(context):
    assert len(context["route"].segments) == 1

@then("the total distance should be greater than 0")
def check_distance(context):
    assert context["route"].total_distance() > 0
```

**`tests/conftest.py`**:
```python
import pytest

@pytest.fixture
def context():
    """Shared context for BDD steps"""
    return {}
```

**Run tests**:
```bash
pytest tests/bdd/ -v
```

**✅ Success Criteria**:
- CLI command runs and outputs route info
- BDD test passes
- Architecture proven end-to-end: CLI → Application → Domain
- Foundation for real implementation

---

## Phase 2: Player Registration

**Goal**: Implement multi-tenancy foundation with full domain-first approach.

### 2.1 Domain Layer

**`src/spacetraders/domain/shared/player.py`**:
```python
from dataclasses import dataclass
from datetime import datetime, timezone
from typing import Optional, Dict, Any

class Player:
    """
    Player entity - represents a SpaceTraders agent/account

    Invariants:
    - agent_symbol must be unique
    - token must be valid Bearer token format
    - last_active updated on any operation
    """

    def __init__(
        self,
        player_id: Optional[int],
        agent_symbol: str,
        token: str,
        created_at: datetime,
        last_active: Optional[datetime] = None,
        metadata: Optional[Dict[str, Any]] = None
    ):
        if not agent_symbol or not agent_symbol.strip():
            raise ValueError("agent_symbol cannot be empty")
        if not token or not token.strip():
            raise ValueError("token cannot be empty")

        self._player_id = player_id
        self._agent_symbol = agent_symbol.strip()
        self._token = token.strip()
        self._created_at = created_at
        self._last_active = last_active or created_at
        self._metadata = metadata or {}

    @property
    def player_id(self) -> Optional[int]:
        return self._player_id

    @property
    def agent_symbol(self) -> str:
        return self._agent_symbol

    @property
    def token(self) -> str:
        return self._token

    @property
    def created_at(self) -> datetime:
        return self._created_at

    @property
    def last_active(self) -> datetime:
        return self._last_active

    @property
    def metadata(self) -> Dict[str, Any]:
        return self._metadata.copy()

    def update_last_active(self) -> None:
        """Touch last active timestamp"""
        self._last_active = datetime.now(timezone.utc)

    def update_metadata(self, metadata: Dict[str, Any]) -> None:
        """Update metadata dict"""
        self._metadata.update(metadata)

    def is_active_within(self, hours: int) -> bool:
        """Check if player was active within N hours"""
        delta = datetime.now(timezone.utc) - self._last_active
        return delta.total_seconds() < (hours * 3600)

    def __repr__(self) -> str:
        return f"Player(id={self.player_id}, agent={self.agent_symbol})"
```

**`src/spacetraders/domain/shared/exceptions.py`**:
```python
class DomainException(Exception):
    """Base exception for all domain errors"""
    pass

class DuplicateAgentSymbolError(DomainException):
    """Raised when trying to register duplicate agent symbol"""
    pass

class PlayerNotFoundError(DomainException):
    """Raised when player not found"""
    pass
```

### 2.2 Repository Port

**`src/spacetraders/ports/outbound/repositories.py`**:
```python
from abc import ABC, abstractmethod
from typing import Optional, List
from ...domain.shared.player import Player

class IPlayerRepository(ABC):
    """Port for player persistence"""

    @abstractmethod
    def create(self, player: Player) -> Player:
        """Persist new player, returns player with assigned ID"""
        pass

    @abstractmethod
    def find_by_id(self, player_id: int) -> Optional[Player]:
        """Load player by ID"""
        pass

    @abstractmethod
    def find_by_agent_symbol(self, agent_symbol: str) -> Optional[Player]:
        """Load player by agent symbol"""
        pass

    @abstractmethod
    def list_all(self) -> List[Player]:
        """List all registered players"""
        pass

    @abstractmethod
    def update(self, player: Player) -> None:
        """Update existing player"""
        pass

    @abstractmethod
    def exists_by_agent_symbol(self, agent_symbol: str) -> bool:
        """Check if agent symbol already registered"""
        pass
```

### 2.3 Application Commands & Handlers (CQRS)

**`src/spacetraders/application/player/commands/register_player.py`**:
```python
from dataclasses import dataclass
from typing import Optional, Dict, Any
from datetime import datetime, timezone
from pymediatr import Request, RequestHandler

from ....domain.shared.player import Player
from ....domain.shared.exceptions import DuplicateAgentSymbolError
from ....ports.repositories import IPlayerRepository

@dataclass(frozen=True)
class RegisterPlayerCommand(Request[Player]):
    """Command to register new player"""
    agent_symbol: str
    token: str
    metadata: Optional[Dict[str, Any]] = None

class RegisterPlayerHandler(RequestHandler[RegisterPlayerCommand, Player]):
    """Handler for player registration"""

    def __init__(self, player_repository: IPlayerRepository):
        self._player_repo = player_repository

    async def handle(self, request: RegisterPlayerCommand) -> Player:
        """
        Register new player

        Raises:
            DuplicateAgentSymbolError: If agent symbol already registered
        """
        # 1. Check uniqueness (domain rule enforcement)
        if self._player_repo.exists_by_agent_symbol(request.agent_symbol):
            raise DuplicateAgentSymbolError(
                f"Agent symbol '{request.agent_symbol}' already registered"
            )

        # 2. Create domain entity
        player = Player(
            player_id=None,  # Will be assigned by repository
            agent_symbol=request.agent_symbol,
            token=request.token,
            created_at=datetime.now(timezone.utc),
            metadata=request.metadata
        )

        # 3. Persist via repository
        return self._player_repo.create(player)
```

**`src/spacetraders/application/player/commands/update_player.py`**:
```python
from dataclasses import dataclass
from typing import Dict, Any
from pymediatr import Request, RequestHandler

from ....domain.shared.player import Player
from ....domain.shared.exceptions import PlayerNotFoundError
from ....ports.repositories import IPlayerRepository

@dataclass(frozen=True)
class UpdatePlayerMetadataCommand(Request[Player]):
    """Command to update player metadata"""
    player_id: int
    metadata: Dict[str, Any]

class UpdatePlayerMetadataHandler(RequestHandler[UpdatePlayerMetadataCommand, Player]):
    """Handler for updating player metadata"""

    def __init__(self, player_repository: IPlayerRepository):
        self._player_repo = player_repository

    async def handle(self, request: UpdatePlayerMetadataCommand) -> Player:
        """Update player metadata"""
        # 1. Load player
        player = self._player_repo.find_by_id(request.player_id)
        if not player:
            raise PlayerNotFoundError(f"Player {request.player_id} not found")

        # 2. Update domain entity
        player.update_metadata(request.metadata)

        # 3. Persist
        self._player_repo.update(player)
        return player
```

### 2.4 Application Queries & Handlers

**`src/spacetraders/application/player/queries/get_player.py`**:
```python
from dataclasses import dataclass
from pymediatr import Request, RequestHandler

from ....domain.shared.player import Player
from ....domain.shared.exceptions import PlayerNotFoundError
from ....ports.repositories import IPlayerRepository

@dataclass(frozen=True)
class GetPlayerQuery(Request[Player]):
    """Query to get player by ID"""
    player_id: int

class GetPlayerHandler(RequestHandler[GetPlayerQuery, Player]):
    """Handler for getting player by ID"""

    def __init__(self, player_repository: IPlayerRepository):
        self._player_repo = player_repository

    async def handle(self, request: GetPlayerQuery) -> Player:
        """Get player by ID"""
        player = self._player_repo.find_by_id(request.player_id)
        if not player:
            raise PlayerNotFoundError(f"Player {request.player_id} not found")
        return player

@dataclass(frozen=True)
class GetPlayerByAgentQuery(Request[Player]):
    """Query to get player by agent symbol"""
    agent_symbol: str

class GetPlayerByAgentHandler(RequestHandler[GetPlayerByAgentQuery, Player]):
    """Handler for getting player by agent symbol"""

    def __init__(self, player_repository: IPlayerRepository):
        self._player_repo = player_repository

    async def handle(self, request: GetPlayerByAgentQuery) -> Player:
        """Get player by agent symbol"""
        player = self._player_repo.find_by_agent_symbol(request.agent_symbol)
        if not player:
            raise PlayerNotFoundError(f"Agent '{request.agent_symbol}' not found")
        return player
```

**`src/spacetraders/application/player/queries/list_players.py`**:
```python
from dataclasses import dataclass
from typing import List
from pymediatr import Request, RequestHandler

from ....domain.shared.player import Player
from ....ports.repositories import IPlayerRepository

@dataclass(frozen=True)
class ListPlayersQuery(Request[List[Player]]):
    """Query to list all players"""
    pass

class ListPlayersHandler(RequestHandler[ListPlayersQuery, List[Player]]):
    """Handler for listing all players"""

    def __init__(self, player_repository: IPlayerRepository):
        self._player_repo = player_repository

    async def handle(self, request: ListPlayersQuery) -> List[Player]:
        """List all registered players"""
        return self._player_repo.list_all()
```

### 2.5 BDD Tests (Domain-First)

**`tests/bdd/features/shared/player.feature`**:
```gherkin
Feature: Player Registration
  As a bot operator
  I want to register SpaceTraders agents
  So that I can manage multiple accounts

  Scenario: Register new player
    When I register player "AGENT-1" with token "TOKEN-123"
    Then the player should have a player_id
    And the player agent_symbol should be "AGENT-1"
    And the player token should be "TOKEN-123"
    And last_active should be set

  Scenario: Duplicate agent symbol rejected
    Given a player with agent_symbol "AGENT-1" exists
    When I attempt to register player "AGENT-1" with token "TOKEN-456"
    Then registration should fail with DuplicateAgentSymbolError

  Scenario: Empty agent symbol rejected
    When I attempt to register player "" with token "TOKEN-123"
    Then registration should fail with ValueError

  Scenario: Update player metadata
    Given a registered player with id 1
    When I update metadata with {"faction": "COSMIC"}
    Then the player metadata should contain "faction"

  Scenario: Touch last active timestamp
    Given a registered player with id 1
    When I touch the player's last_active
    Then last_active should be updated

  Scenario: List all players
    Given players "AGENT-1", "AGENT-2", "AGENT-3" are registered
    When I list all players
    Then I should see 3 players
```

**`tests/bdd/steps/shared/test_player_steps.py`**:
```python
from pytest_bdd import scenario, given, when, then, parsers
from datetime import datetime, timezone
import asyncio
from pymediatr import Mediator

from spacetraders.domain.shared.player import Player
from spacetraders.domain.shared.exceptions import DuplicateAgentSymbolError, PlayerNotFoundError
from spacetraders.application.player.commands.register_player import (
    RegisterPlayerCommand,
    RegisterPlayerHandler
)
from spacetraders.application.player.queries.get_player import (
    GetPlayerQuery,
    GetPlayerHandler
)

# Scenario: Register new player
@scenario("../../features/shared/player.feature", "Register new player")
def test_register_new_player():
    pass

@when(parsers.parse('I register player "{agent}" with token "{token}"'))
def register_player(context, agent, token, mock_player_repo, mediator):
    """
    Send RegisterPlayerCommand via mediator

    Note: In tests, we use sync wrapper around async handler
    """
    command = RegisterPlayerCommand(agent_symbol=agent, token=token)
    context["player"] = asyncio.run(mediator.send_async(command))

@then("the player should have a player_id")
def check_player_id(context):
    assert context["player"].player_id is not None

@then(parsers.parse('the player agent_symbol should be "{agent}"'))
def check_agent_symbol(context, agent):
    assert context["player"].agent_symbol == agent

@then(parsers.parse('the player token should be "{token}"'))
def check_token(context, token):
    assert context["player"].token == token

@then("last_active should be set")
def check_last_active(context):
    assert context["player"].last_active is not None

# Scenario: Duplicate agent symbol rejected
@scenario("../../features/shared/player.feature", "Duplicate agent symbol rejected")
def test_duplicate_agent_rejected():
    pass

@given(parsers.parse('a player with agent_symbol "{agent}" exists'))
def existing_player(context, agent, mock_player_repo):
    # Mock repository will have this agent
    context["existing_agent"] = agent

@when(parsers.parse('I attempt to register player "{agent}" with token "{token}"'))
def attempt_register_duplicate(context, agent, token, mock_player_repo):
    service = PlayerService(mock_player_repo)
    command = RegisterPlayerCommand(agent_symbol=agent, token=token)
    try:
        service.register_player(command)
        context["error"] = None
    except Exception as e:
        context["error"] = e

@then("registration should fail with DuplicateAgentSymbolError")
def check_duplicate_error(context):
    assert isinstance(context["error"], DuplicateAgentSymbolError)

# Additional scenarios...
```

**`tests/bdd/steps/shared/conftest.py`** (mock repository):
```python
import pytest
from typing import Optional, List, Dict
from spacetraders.domain.shared.player import Player
from spacetraders.ports.outbound.repositories import IPlayerRepository
from datetime import datetime, timezone

class MockPlayerRepository(IPlayerRepository):
    """In-memory player repository for testing"""

    def __init__(self):
        self._players: Dict[int, Player] = {}
        self._next_id = 1
        self._agents: Dict[str, int] = {}  # agent_symbol -> player_id

    def create(self, player: Player) -> Player:
        player_id = self._next_id
        self._next_id += 1

        created_player = Player(
            player_id=player_id,
            agent_symbol=player.agent_symbol,
            token=player.token,
            created_at=player.created_at,
            last_active=player.last_active,
            metadata=player.metadata
        )

        self._players[player_id] = created_player
        self._agents[player.agent_symbol] = player_id
        return created_player

    def find_by_id(self, player_id: int) -> Optional[Player]:
        return self._players.get(player_id)

    def find_by_agent_symbol(self, agent_symbol: str) -> Optional[Player]:
        player_id = self._agents.get(agent_symbol)
        return self._players.get(player_id) if player_id else None

    def list_all(self) -> List[Player]:
        return list(self._players.values())

    def update(self, player: Player) -> None:
        if player.player_id in self._players:
            self._players[player.player_id] = player

    def exists_by_agent_symbol(self, agent_symbol: str) -> bool:
        return agent_symbol in self._agents

@pytest.fixture
def mock_player_repo():
    """Provide mock player repository for tests"""
    return MockPlayerRepository()
```

### 2.6 Persistence Adapter (Real Implementation)

**`src/spacetraders/adapters/secondary/persistence/database.py`**:
```python
import sqlite3
import logging
from pathlib import Path
from contextlib import contextmanager
from typing import Optional

logger = logging.getLogger(__name__)

class Database:
    """SQLite database manager with WAL mode for concurrency"""

    def __init__(self, db_path: Optional[Path] = None):
        self.db_path = db_path or Path("var/spacetraders.db")
        self.db_path.parent.mkdir(parents=True, exist_ok=True)
        self._init_database()
        logger.info(f"Database initialized at {self.db_path}")

    def _get_connection(self) -> sqlite3.Connection:
        """Get database connection with optimized settings"""
        conn = sqlite3.connect(
            str(self.db_path),
            check_same_thread=False,
            timeout=30.0
        )
        conn.execute('PRAGMA journal_mode=WAL')
        conn.execute('PRAGMA foreign_keys=ON')
        conn.row_factory = sqlite3.Row
        return conn

    @contextmanager
    def connection(self):
        """Context manager for read-only connections"""
        conn = self._get_connection()
        try:
            yield conn
        finally:
            conn.close()

    @contextmanager
    def transaction(self):
        """Context manager for transactional writes"""
        conn = self._get_connection()
        try:
            yield conn
            conn.commit()
        except Exception:
            conn.rollback()
            raise
        finally:
            conn.close()

    def _init_database(self):
        """Initialize database schema"""
        with self._get_connection() as conn:
            cursor = conn.cursor()

            # Players table
            cursor.execute("""
                CREATE TABLE IF NOT EXISTS players (
                    player_id INTEGER PRIMARY KEY AUTOINCREMENT,
                    agent_symbol TEXT UNIQUE NOT NULL,
                    token TEXT NOT NULL,
                    created_at TIMESTAMP NOT NULL,
                    last_active TIMESTAMP,
                    metadata TEXT
                )
            """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_player_agent
                ON players(agent_symbol)
            """)

            conn.commit()
```

**`src/spacetraders/adapters/secondary/persistence/player_repository.py`**:
```python
import json
import logging
from typing import Optional, List
from datetime import datetime

from ....domain.shared.player import Player
from ....ports.outbound.repositories import IPlayerRepository
from .database import Database
from .mappers import PlayerMapper

logger = logging.getLogger(__name__)

class PlayerRepository(IPlayerRepository):
    """SQLite implementation of player repository"""

    def __init__(self, database: Database):
        self._db = database

    def create(self, player: Player) -> Player:
        """Persist new player"""
        with self._db.transaction() as conn:
            cursor = conn.cursor()

            cursor.execute("""
                INSERT INTO players (agent_symbol, token, created_at, last_active, metadata)
                VALUES (?, ?, ?, ?, ?)
            """, (
                player.agent_symbol,
                player.token,
                player.created_at.isoformat(),
                player.last_active.isoformat(),
                json.dumps(player.metadata)
            ))

            player_id = cursor.lastrowid
            logger.info(f"Created player {player_id}: {player.agent_symbol}")

            # Return player with assigned ID
            return Player(
                player_id=player_id,
                agent_symbol=player.agent_symbol,
                token=player.token,
                created_at=player.created_at,
                last_active=player.last_active,
                metadata=player.metadata
            )

    def find_by_id(self, player_id: int) -> Optional[Player]:
        """Load player by ID"""
        with self._db.connection() as conn:
            cursor = conn.cursor()
            cursor.execute("SELECT * FROM players WHERE player_id = ?", (player_id,))
            row = cursor.fetchone()
            return PlayerMapper.from_db_row(row) if row else None

    def find_by_agent_symbol(self, agent_symbol: str) -> Optional[Player]:
        """Load player by agent symbol"""
        with self._db.connection() as conn:
            cursor = conn.cursor()
            cursor.execute("SELECT * FROM players WHERE agent_symbol = ?", (agent_symbol,))
            row = cursor.fetchone()
            return PlayerMapper.from_db_row(row) if row else None

    def list_all(self) -> List[Player]:
        """List all players"""
        with self._db.connection() as conn:
            cursor = conn.cursor()
            cursor.execute("SELECT * FROM players ORDER BY created_at")
            rows = cursor.fetchall()
            return [PlayerMapper.from_db_row(row) for row in rows]

    def update(self, player: Player) -> None:
        """Update existing player"""
        with self._db.transaction() as conn:
            cursor = conn.cursor()
            cursor.execute("""
                UPDATE players
                SET last_active = ?, metadata = ?
                WHERE player_id = ?
            """, (
                player.last_active.isoformat(),
                json.dumps(player.metadata),
                player.player_id
            ))
            logger.debug(f"Updated player {player.player_id}")

    def exists_by_agent_symbol(self, agent_symbol: str) -> bool:
        """Check if agent symbol exists"""
        with self._db.connection() as conn:
            cursor = conn.cursor()
            cursor.execute(
                "SELECT 1 FROM players WHERE agent_symbol = ? LIMIT 1",
                (agent_symbol,)
            )
            return cursor.fetchone() is not None
```

**`src/spacetraders/adapters/secondary/persistence/mappers.py`**:
```python
import json
from datetime import datetime
from typing import Optional
from sqlite3 import Row

from ....domain.shared.player import Player

class PlayerMapper:
    """Map between database rows and Player entities"""

    @staticmethod
    def from_db_row(row: Row) -> Player:
        """Convert database row to Player entity"""
        return Player(
            player_id=row["player_id"],
            agent_symbol=row["agent_symbol"],
            token=row["token"],
            created_at=datetime.fromisoformat(row["created_at"]),
            last_active=datetime.fromisoformat(row["last_active"]) if row["last_active"] else None,
            metadata=json.loads(row["metadata"]) if row["metadata"] else {}
        )
```

### 2.7 CLI Adapter

**`src/spacetraders/adapters/primary/cli/player_cli.py`**:
```python
import argparse
import json
import asyncio
from typing import Any, Dict

from ....configuration.container import get_mediator
from ....application.player.commands.register_player import RegisterPlayerCommand
from ....application.player.queries.get_player import GetPlayerQuery, GetPlayerByAgentQuery
from ....application.player.queries.list_players import ListPlayersQuery
from ....domain.shared.exceptions import DuplicateAgentSymbolError, PlayerNotFoundError

def register_player_command(args: argparse.Namespace) -> int:
    """Handle player register command"""
    metadata: Dict[str, Any] = {}
    if args.metadata:
        metadata = json.loads(args.metadata)

    mediator = get_mediator()
    command = RegisterPlayerCommand(
        agent_symbol=args.agent_symbol,
        token=args.token,
        metadata=metadata
    )

    try:
        # Send command via mediator
        player = asyncio.run(mediator.send_async(command))
        print(f"✅ Registered player {player.player_id}: {player.agent_symbol}")
        return 0
    except DuplicateAgentSymbolError as e:
        print(f"❌ Error: {e}")
        return 1

def list_players_command(args: argparse.Namespace) -> int:
    """Handle player list command"""
    mediator = get_mediator()
    query = ListPlayersQuery()

    # Send query via mediator
    players = asyncio.run(mediator.send_async(query))

    if not players:
        print("No players registered")
        return 0

    print(f"Registered players ({len(players)}):")
    for player in players:
        active = "✓" if player.is_active_within(24) else "✗"
        print(f"  [{player.player_id}] {player.agent_symbol} {active}")

    return 0

def player_info_command(args: argparse.Namespace) -> int:
    """Handle player info command"""
    mediator = get_mediator()

    try:
        # Send appropriate query via mediator
        if args.player_id:
            query = GetPlayerQuery(player_id=args.player_id)
        else:
            query = GetPlayerByAgentQuery(agent_symbol=args.agent_symbol)

        player = asyncio.run(mediator.send_async(query))

        print(f"Player {player.player_id}:")
        print(f"  Agent: {player.agent_symbol}")
        print(f"  Created: {player.created_at.isoformat()}")
        print(f"  Last Active: {player.last_active.isoformat()}")
        if player.metadata:
            print(f"  Metadata: {json.dumps(player.metadata, indent=2)}")

        return 0
    except PlayerNotFoundError as e:
        print(f"❌ Error: {e}")
        return 1

def setup_player_commands(subparsers):
    """Setup player CLI commands"""
    player_parser = subparsers.add_parser("player", help="Player management")
    player_subparsers = player_parser.add_subparsers(dest="player_command")

    # Register command
    register_parser = player_subparsers.add_parser("register", help="Register new player")
    register_parser.add_argument("--agent", dest="agent_symbol", required=True)
    register_parser.add_argument("--token", required=True)
    register_parser.add_argument("--metadata", help="JSON metadata")
    register_parser.set_defaults(func=register_player_command)

    # List command
    list_parser = player_subparsers.add_parser("list", help="List all players")
    list_parser.set_defaults(func=list_players_command)

    # Info command
    info_parser = player_subparsers.add_parser("info", help="Get player info")
    info_parser.add_argument("--player-id", type=int)
    info_parser.add_argument("--agent", dest="agent_symbol")
    info_parser.set_defaults(func=player_info_command)
```

**Update `src/spacetraders/adapters/primary/cli/main.py`**:
```python
#!/usr/bin/env python3
import argparse
import sys
from .player_cli import setup_player_commands

def main():
    parser = argparse.ArgumentParser(description="SpaceTraders V2")
    subparsers = parser.add_subparsers(dest="command")

    # Setup subcommands
    setup_player_commands(subparsers)

    args = parser.parse_args()

    if hasattr(args, "func"):
        sys.exit(args.func(args))
    else:
        parser.print_help()
        sys.exit(1)

if __name__ == "__main__":
    main()
```

### 2.8 Dependency Injection & Mediator Setup

**`src/spacetraders/configuration/container.py`**:
```python
from pathlib import Path
from pymediatr import Mediator
from ..adapters.secondary.persistence.database import Database
from ..adapters.secondary.persistence.player_repository import PlayerRepository
from ..application.player.commands.register_player import (
    RegisterPlayerCommand,
    RegisterPlayerHandler
)
from ..application.player.queries.get_player import (
    GetPlayerQuery,
    GetPlayerHandler
)
from ..application.player.queries.list_players import (
    ListPlayersQuery,
    ListPlayersHandler
)
from ..application.common.behaviors import (
    LoggingBehavior,
    ValidationBehavior
)

# Singleton instances
_db = None
_player_repo = None
_mediator = None

def get_database() -> Database:
    """Get or create database instance"""
    global _db
    if _db is None:
        _db = Database(Path("var/spacetraders.db"))
    return _db

def get_player_repository() -> PlayerRepository:
    """Get or create player repository"""
    global _player_repo
    if _player_repo is None:
        _player_repo = PlayerRepository(get_database())
    return _player_repo

def get_mediator() -> Mediator:
    """Get or create configured mediator with all handlers registered"""
    global _mediator
    if _mediator is None:
        _mediator = Mediator()

        # Register pipeline behaviors (middleware)
        _mediator.register_behavior(LoggingBehavior())
        _mediator.register_behavior(ValidationBehavior())

        # Get dependencies
        player_repo = get_player_repository()

        # Register command handlers
        _mediator.register_handler(
            RegisterPlayerCommand,
            lambda: RegisterPlayerHandler(player_repo)
        )

        # Register query handlers
        _mediator.register_handler(
            GetPlayerQuery,
            lambda: GetPlayerHandler(player_repo)
        )
        _mediator.register_handler(
            ListPlayersQuery,
            lambda: ListPlayersHandler(player_repo)
        )

    return _mediator
```

### 2.9 Test Everything

**Run BDD tests**:
```bash
pytest tests/bdd/features/shared/player.feature -v
```

**Manual CLI tests**:
```bash
spacetraders player register --agent AGENT-1 --token TOKEN-123
spacetraders player list
spacetraders player info --agent AGENT-1
```

**✅ Success Criteria**:
- All BDD tests pass
- CLI commands work
- Player persisted to SQLite database
- Can query players
- Multi-tenancy foundation established

---

## Phase 3: Navigation Domain

**Goal**: Implement navigation domain logic with full fuel-aware routing.

### 3.1 Domain Value Objects

**`src/spacetraders/domain/shared/value_objects.py`** (complete implementation):
```python
from dataclasses import dataclass
from typing import Optional
from enum import Enum
import math

@dataclass(frozen=True)
class Waypoint:
    """Immutable waypoint value object"""
    symbol: str
    x: float
    y: float
    system_symbol: Optional[str] = None
    waypoint_type: Optional[str] = None
    traits: tuple = ()
    has_fuel: bool = False
    orbitals: tuple = ()

    def distance_to(self, other: 'Waypoint') -> float:
        """Calculate Euclidean distance to another waypoint"""
        return math.hypot(other.x - self.x, other.y - self.y)

    def is_orbital_of(self, other: 'Waypoint') -> bool:
        """Check if this waypoint orbits another"""
        return other.symbol in self.orbitals or self.symbol in other.orbitals

    def __repr__(self) -> str:
        return f"Waypoint({self.symbol})"

@dataclass(frozen=True)
class Fuel:
    """Immutable fuel value object"""
    current: int
    capacity: int

    def __post_init__(self):
        if self.current < 0:
            raise ValueError("current fuel cannot be negative")
        if self.capacity < 0:
            raise ValueError("fuel capacity cannot be negative")
        if self.current > self.capacity:
            raise ValueError("current fuel cannot exceed capacity")

    def percentage(self) -> float:
        """Fuel as percentage of capacity"""
        return (self.current / self.capacity * 100) if self.capacity > 0 else 0.0

    def consume(self, amount: int) -> 'Fuel':
        """Return new Fuel with amount consumed"""
        if amount < 0:
            raise ValueError("consume amount cannot be negative")
        new_current = max(0, self.current - amount)
        return Fuel(current=new_current, capacity=self.capacity)

    def add(self, amount: int) -> 'Fuel':
        """Return new Fuel with amount added"""
        if amount < 0:
            raise ValueError("add amount cannot be negative")
        new_current = min(self.capacity, self.current + amount)
        return Fuel(current=new_current, capacity=self.capacity)

    def can_travel(self, required: int, safety_margin: float = 0.1) -> bool:
        """Check if sufficient fuel for travel with safety margin"""
        required_with_margin = int(required * (1 + safety_margin))
        return self.current >= required_with_margin

    def is_full(self) -> bool:
        """Check if fuel at capacity"""
        return self.current == self.capacity

    def __repr__(self) -> str:
        return f"Fuel({self.current}/{self.capacity})"

class FlightMode(Enum):
    """Flight modes with time/fuel characteristics"""
    CRUISE = ("CRUISE", 31, 1.0)     # Fast, standard fuel
    DRIFT = ("DRIFT", 26, 0.003)     # Slow, minimal fuel
    BURN = ("BURN", 15, 2.0)         # Very fast, high fuel
    STEALTH = ("STEALTH", 50, 1.0)   # Very slow, stealthy

    def __init__(self, mode_name: str, time_multiplier: int, fuel_rate: float):
        self.mode_name = mode_name
        self.time_multiplier = time_multiplier
        self.fuel_rate = fuel_rate

    def fuel_cost(self, distance: float) -> int:
        """Calculate fuel cost for given distance"""
        if distance == 0:
            return 0
        return max(1, math.ceil(distance * self.fuel_rate))

    def travel_time(self, distance: float, engine_speed: int) -> int:
        """Calculate travel time in seconds"""
        if distance == 0:
            return 0
        return max(1, int((distance * self.time_multiplier) / max(1, engine_speed)))

    @staticmethod
    def select_optimal(fuel_percentage: float) -> 'FlightMode':
        """Select optimal mode based on fuel percentage"""
        return FlightMode.CRUISE if fuel_percentage >= 75.0 else FlightMode.DRIFT

@dataclass(frozen=True)
class Distance:
    """Immutable distance value object"""
    units: float

    def __post_init__(self):
        if self.units < 0:
            raise ValueError("distance cannot be negative")

    def with_margin(self, margin: float) -> 'Distance':
        """Return distance with safety margin applied"""
        return Distance(units=self.units * (1 + margin))

    def __repr__(self) -> str:
        return f"{self.units:.1f} units"
```

### 3.2 Navigation Domain Entities

**`src/spacetraders/domain/navigation/route.py`**:
```python
from dataclasses import dataclass
from typing import List, Optional
from enum import Enum

from ..shared.value_objects import Waypoint, FlightMode, Fuel

class RouteStatus(Enum):
    """Route execution status"""
    PLANNED = "PLANNED"
    EXECUTING = "EXECUTING"
    COMPLETED = "COMPLETED"
    FAILED = "FAILED"
    ABORTED = "ABORTED"

@dataclass(frozen=True)
class RouteSegment:
    """Immutable route segment value object"""
    from_waypoint: Waypoint
    to_waypoint: Waypoint
    distance: float
    fuel_required: int
    travel_time: int
    flight_mode: FlightMode
    requires_refuel: bool = False

    def __repr__(self) -> str:
        mode = self.flight_mode.mode_name
        refuel = " [REFUEL]" if self.requires_refuel else ""
        return f"{self.from_waypoint.symbol} → {self.to_waypoint.symbol} ({self.distance:.1f}u, {self.fuel_required}⛽, {mode}){refuel}"

class Route:
    """
    Route aggregate root - represents a complete navigation plan

    Invariants:
    - Segments form connected path (segment[i].to == segment[i+1].from)
    - Total fuel required does not exceed ship capacity
    - Route can only be executed from PLANNED status
    """

    def __init__(
        self,
        route_id: str,
        ship_symbol: str,
        player_id: int,
        segments: List[RouteSegment],
        ship_fuel_capacity: int
    ):
        if not segments:
            raise ValueError("Route must have at least one segment")

        self._route_id = route_id
        self._ship_symbol = ship_symbol
        self._player_id = player_id
        self._segments = segments
        self._ship_fuel_capacity = ship_fuel_capacity
        self._status = RouteStatus.PLANNED
        self._current_segment_index = 0

        self._validate()

    @property
    def route_id(self) -> str:
        return self._route_id

    @property
    def ship_symbol(self) -> str:
        return self._ship_symbol

    @property
    def player_id(self) -> int:
        return self._player_id

    @property
    def segments(self) -> List[RouteSegment]:
        return self._segments.copy()

    @property
    def status(self) -> RouteStatus:
        return self._status

    @property
    def current_segment_index(self) -> int:
        return self._current_segment_index

    def _validate(self) -> None:
        """Validate route invariants"""
        # Check segments form connected path
        for i in range(len(self._segments) - 1):
            current = self._segments[i]
            next_seg = self._segments[i + 1]
            if current.to_waypoint.symbol != next_seg.from_waypoint.symbol:
                raise ValueError(
                    f"Segments not connected: {current.to_waypoint.symbol} → {next_seg.from_waypoint.symbol}"
                )

        # Check fuel requirements don't exceed capacity
        max_fuel_needed = max(seg.fuel_required for seg in self._segments)
        if max_fuel_needed > self._ship_fuel_capacity:
            raise ValueError(
                f"Segment requires {max_fuel_needed} fuel but ship capacity is {self._ship_fuel_capacity}"
            )

    def start_execution(self) -> None:
        """Begin route execution"""
        if self._status != RouteStatus.PLANNED:
            raise ValueError(f"Cannot start route in status {self._status.value}")
        self._status = RouteStatus.EXECUTING

    def complete_segment(self) -> None:
        """Mark current segment as complete and advance"""
        if self._status != RouteStatus.EXECUTING:
            raise ValueError(f"Cannot complete segment when route status is {self._status.value}")

        self._current_segment_index += 1

        # Check if route complete
        if self._current_segment_index >= len(self._segments):
            self._status = RouteStatus.COMPLETED

    def fail_route(self, reason: str) -> None:
        """Mark route as failed"""
        self._status = RouteStatus.FAILED

    def abort_route(self, reason: str) -> None:
        """Abort route execution"""
        self._status = RouteStatus.ABORTED

    def total_distance(self) -> float:
        """Calculate total distance of route"""
        return sum(seg.distance for seg in self._segments)

    def total_fuel_required(self) -> int:
        """Calculate total fuel required (assuming refuels at stops)"""
        return sum(seg.fuel_required for seg in self._segments)

    def total_travel_time(self) -> int:
        """Calculate total travel time in seconds"""
        return sum(seg.travel_time for seg in self._segments)

    def current_segment(self) -> Optional[RouteSegment]:
        """Get current segment being executed"""
        if self._current_segment_index < len(self._segments):
            return self._segments[self._current_segment_index]
        return None

    def remaining_segments(self) -> List[RouteSegment]:
        """Get remaining segments to execute"""
        return self._segments[self._current_segment_index:]

    def __repr__(self) -> str:
        return (
            f"Route(id={self.route_id}, ship={self.ship_symbol}, "
            f"segments={len(self._segments)}, status={self._status.value})"
        )
```

**`src/spacetraders/domain/navigation/exceptions.py`**:
```python
from ..shared.exceptions import DomainException

class NavigationException(DomainException):
    """Base exception for navigation domain"""
    pass

class InsufficientFuelError(NavigationException):
    """Raised when ship doesn't have enough fuel"""
    pass

class InvalidRouteError(NavigationException):
    """Raised when route is invalid"""
    pass

class RouteExecutionError(NavigationException):
    """Raised when route execution fails"""
    pass

class NoRouteFoundError(NavigationException):
    """Raised when no valid route can be found"""
    pass
```

### 3.3 Domain Services

**`src/spacetraders/domain/navigation/services.py`**:
```python
from typing import Optional
from ..shared.value_objects import Fuel, FlightMode, Waypoint

class FlightModeSelector:
    """
    Domain service for selecting optimal flight mode

    Business rules:
    - CRUISE when fuel > 75% (fast travel)
    - DRIFT when fuel ≤ 75% (fuel conservation)
    """

    CRUISE_THRESHOLD = 75.0  # Percentage

    @staticmethod
    def select_for_fuel(fuel: Fuel) -> FlightMode:
        """Select flight mode based on current fuel"""
        return FlightMode.select_optimal(fuel.percentage())

    @staticmethod
    def select_for_distance(fuel: Fuel, distance: float, require_return: bool = False) -> FlightMode:
        """
        Select flight mode that allows travel with optional return

        Args:
            fuel: Current fuel state
            distance: Distance to travel
            require_return: If True, ensure enough fuel for return trip

        Returns:
            FlightMode that works for the journey
        """
        multiplier = 2 if require_return else 1

        # Try CRUISE first
        cruise_cost = FlightMode.CRUISE.fuel_cost(distance * multiplier)
        if fuel.can_travel(cruise_cost):
            return FlightMode.CRUISE

        # Fall back to DRIFT
        drift_cost = FlightMode.DRIFT.fuel_cost(distance * multiplier)
        if fuel.can_travel(drift_cost):
            return FlightMode.DRIFT

        # Not enough fuel for either mode
        return FlightMode.DRIFT  # Return cheapest, caller must handle refuel

class RefuelPlanner:
    """
    Domain service for planning refuel stops

    Business rules:
    - Refuel at marketplaces when fuel < 75%
    - Always maintain 10% safety margin
    - Prefer fewer refuel stops (efficiency)
    """

    REFUEL_THRESHOLD = 75.0  # Percentage
    SAFETY_MARGIN = 0.1      # 10%

    @staticmethod
    def should_refuel(fuel: Fuel, at_marketplace: bool) -> bool:
        """Determine if ship should refuel at current location"""
        if not at_marketplace:
            return False
        return fuel.percentage() < RefuelPlanner.REFUEL_THRESHOLD

    @staticmethod
    def needs_refuel_stop(
        fuel: Fuel,
        distance_to_destination: float,
        distance_to_refuel_point: float,
        mode: FlightMode
    ) -> bool:
        """
        Determine if refuel stop needed to reach destination

        Args:
            fuel: Current fuel
            distance_to_destination: Direct distance to destination
            distance_to_refuel_point: Distance to nearest refuel point
            mode: Intended flight mode

        Returns:
            True if refuel stop needed
        """
        fuel_to_destination = mode.fuel_cost(distance_to_destination)

        # Can we reach destination directly?
        if fuel.can_travel(fuel_to_destination, safety_margin=RefuelPlanner.SAFETY_MARGIN):
            return False

        # Can we reach refuel point?
        fuel_to_refuel = mode.fuel_cost(distance_to_refuel_point)
        return fuel.can_travel(fuel_to_refuel, safety_margin=RefuelPlanner.SAFETY_MARGIN)

class RouteValidator:
    """
    Domain service for validating route feasibility

    Business rules:
    - All segments must be connected
    - Fuel requirements must not exceed ship capacity
    - Route must have at least one segment
    """

    @staticmethod
    def validate_segments_connected(segments) -> bool:
        """Check if segments form connected path"""
        for i in range(len(segments) - 1):
            if segments[i].to_waypoint.symbol != segments[i + 1].from_waypoint.symbol:
                return False
        return True

    @staticmethod
    def validate_fuel_capacity(segments, ship_fuel_capacity: int) -> bool:
        """Check if any segment exceeds ship fuel capacity"""
        return all(seg.fuel_required <= ship_fuel_capacity for seg in segments)
```

### 3.4 BDD Tests for Navigation Domain

**`tests/bdd/features/navigation/route_planning.feature`**:
```gherkin
Feature: Route Planning
  As a ship operator
  I want to plan navigation routes
  So that I can travel efficiently between waypoints

  Scenario: Plan route with sufficient fuel
    Given a ship with 500 fuel capacity at waypoint "X1-A1"
    And waypoint "X1-B2" is 200 units away
    When I create a route to "X1-B2"
    Then the route should have 1 segment
    And the segment should use CRUISE mode
    And the segment should require 200 fuel
    And the route status should be PLANNED

  Scenario: Route segments must be connected
    Given waypoints "X1-A1", "X1-B2", "X1-C3"
    When I create a route with disconnected segments
    Then route creation should fail with InvalidRouteError

  Scenario: Flight mode selection based on fuel
    Given a ship with 80% fuel
    When I select flight mode
    Then CRUISE mode should be selected

  Scenario: Flight mode selection with low fuel
    Given a ship with 50% fuel
    When I select flight mode
    Then DRIFT mode should be selected

  Scenario: Refuel needed for long distance
    Given a ship with 100 fuel at "X1-A1"
    And destination "X1-Z9" is 500 units away
    And refuel point "X1-M5" is 80 units away
    When I plan the route
    Then the route should include a refuel stop at "X1-M5"

  Scenario: Start route execution
    Given a planned route
    When I start route execution
    Then the route status should be EXECUTING

  Scenario: Complete route segment
    Given an executing route with 3 segments
    When I complete the current segment
    Then the current segment index should be 1

  Scenario: Complete entire route
    Given an executing route with 1 segment
    When I complete the current segment
    Then the route status should be COMPLETED
```

*(Step definitions in `tests/bdd/steps/navigation/test_route_planning_steps.py`)*

---

## Phase 4: Navigation Infrastructure

**Goal**: Implement infrastructure adapters to support navigation domain.

### 4.1 SystemGraph & GraphBuilder

**Copy from old codebase**:
- `src/spacetraders/adapters/secondary/routing/graph_builder.py`
- `src/spacetraders/adapters/secondary/routing/graph_provider.py`

**Key responsibilities**:
- **GraphBuilder**: Fetch waypoints from SpaceTraders API, build graph structure
- **SystemGraphProvider**: Cache graphs in database, return from cache when available

**Port interface** (`src/spacetraders/ports/outbound/graph_provider.py`):
```python
from abc import ABC, abstractmethod
from typing import Dict, Optional
from dataclasses import dataclass

@dataclass
class GraphLoadResult:
    """Result of graph load operation"""
    graph: Dict  # {waypoints: {...}, edges: [...]}
    source: str  # "database" or "api"
    message: Optional[str] = None

class ISystemGraphProvider(ABC):
    """Port for system graph management"""

    @abstractmethod
    def get_graph(self, system_symbol: str, force_refresh: bool = False) -> GraphLoadResult:
        """
        Get navigation graph for system

        Args:
            system_symbol: System to get graph for
            force_refresh: Force fetch from API even if cached

        Returns:
            GraphLoadResult with graph data and source
        """
        pass

class IGraphBuilder(ABC):
    """Port for building graphs from API"""

    @abstractmethod
    def build_system_graph(self, system_symbol: str) -> Dict:
        """
        Fetch all waypoints for system and build graph

        Returns:
            Graph dict: {waypoints: {symbol: {...}}, edges: [{from, to, distance, type}]}
        """
        pass
```

### 4.2 OR-Tools Routing Engine

**Copy optimized implementation** (~1,000 lines from old codebase):
- `src/spacetraders/adapters/secondary/routing/ortools_router.py`
- `src/spacetraders/adapters/secondary/routing/ortools_tsp.py`
- `src/spacetraders/adapters/secondary/routing/ortools_engine.py` (wrapper)

**Port interface** (`src/spacetraders/ports/outbound/routing_engine.py`):
```python
from abc import ABC, abstractmethod
from typing import Dict, List, Optional
from ...domain.shared.value_objects import Waypoint

class IRoutingEngine(ABC):
    """Port for routing/pathfinding algorithms"""

    @abstractmethod
    def find_optimal_path(
        self,
        graph: Dict,
        start: str,
        goal: str,
        current_fuel: int,
        fuel_capacity: int,
        engine_speed: int,
        prefer_cruise: bool = True
    ) -> Optional[Dict]:
        """
        Find optimal path from start to goal with fuel constraints

        Returns:
            Route dict with steps: [{action, waypoint, fuel_cost, time, mode, ...}]
            or None if no path found
        """
        pass

    @abstractmethod
    def optimize_tour(
        self,
        graph: Dict,
        waypoints: List[str],
        start: str,
        return_to_start: bool,
        fuel_capacity: int,
        engine_speed: int
    ) -> Optional[Dict]:
        """
        Find optimal tour visiting all waypoints (TSP)

        Returns:
            Tour dict with ordered waypoints and legs
        """
        pass

    @abstractmethod
    def calculate_fuel_cost(
        self,
        distance: float,
        mode: str
    ) -> int:
        """Calculate fuel cost for distance with given mode"""
        pass

    @abstractmethod
    def calculate_travel_time(
        self,
        distance: float,
        mode: str,
        engine_speed: int
    ) -> int:
        """Calculate travel time in seconds"""
        pass
```

**Implementation details to preserve**:
- Path-first optimization (Dijkstra → Min-Cost Flow)
- Fuel granularity (10-unit buckets)
- Orbital hop penalty (+1 second)
- Branching resolution with priority tie-breaking
- Fallback to simple routing
- Probe fast path (0 fuel capacity)
- TSP caching with validation
- Orbital jitter for TSP

### 4.3 SpaceTraders API Client

**`src/spacetraders/ports/outbound/api_client.py`**:
```python
from abc import ABC, abstractmethod
from typing import Dict, Optional

class ISpaceTradersAPI(ABC):
    """Port for SpaceTraders game API"""

    @abstractmethod
    def get_agent(self) -> Dict:
        """Get agent info"""
        pass

    @abstractmethod
    def get_ship(self, ship_symbol: str) -> Dict:
        """Get ship details"""
        pass

    @abstractmethod
    def navigate_ship(self, ship_symbol: str, waypoint: str) -> Dict:
        """Navigate ship to waypoint"""
        pass

    @abstractmethod
    def dock_ship(self, ship_symbol: str) -> Dict:
        """Dock ship at current waypoint"""
        pass

    @abstractmethod
    def orbit_ship(self, ship_symbol: str) -> Dict:
        """Put ship in orbit"""
        pass

    @abstractmethod
    def refuel_ship(self, ship_symbol: str) -> Dict:
        """Refuel ship at current waypoint"""
        pass

    @abstractmethod
    def list_waypoints(
        self,
        system_symbol: str,
        page: int = 1,
        limit: int = 20
    ) -> Dict:
        """List waypoints in system"""
        pass
```

**`src/spacetraders/adapters/secondary/api/client.py`**:
```python
import requests
import logging
import time
from typing import Dict, Optional

from ....ports.outbound.api_client import ISpaceTradersAPI
from .rate_limiter import RateLimiter

logger = logging.getLogger(__name__)

class SpaceTradersAPIClient(ISpaceTradersAPI):
    """
    SpaceTraders HTTP API client with rate limiting

    Rate limit: 2 requests/second (token bucket)
    Automatic retry on 429 errors
    """

    BASE_URL = "https://api.spacetraders.io/v2"

    def __init__(self, token: str):
        self._token = token
        self._session = requests.Session()
        self._session.headers.update({
            "Authorization": f"Bearer {token}",
            "Content-Type": "application/json"
        })
        self._rate_limiter = RateLimiter(max_requests=2, time_window=1.0)

    def _request(self, method: str, endpoint: str, **kwargs) -> Dict:
        """Make rate-limited HTTP request with retry"""
        self._rate_limiter.acquire()

        url = f"{self.BASE_URL}{endpoint}"
        max_retries = 3

        for attempt in range(max_retries):
            try:
                response = self._session.request(method, url, **kwargs)

                if response.status_code == 429:
                    # Rate limited - exponential backoff
                    wait_time = (2 ** attempt) * 1.0
                    logger.warning(f"Rate limited, waiting {wait_time}s")
                    time.sleep(wait_time)
                    continue

                response.raise_for_status()
                return response.json()

            except requests.exceptions.RequestException as e:
                if attempt == max_retries - 1:
                    raise
                logger.warning(f"Request failed, retrying: {e}")
                time.sleep(1.0)

        raise RuntimeError(f"Request failed after {max_retries} attempts")

    def get_agent(self) -> Dict:
        return self._request("GET", "/my/agent")

    def get_ship(self, ship_symbol: str) -> Dict:
        return self._request("GET", f"/my/ships/{ship_symbol}")

    def navigate_ship(self, ship_symbol: str, waypoint: str) -> Dict:
        return self._request(
            "POST",
            f"/my/ships/{ship_symbol}/navigate",
            json={"waypointSymbol": waypoint}
        )

    def dock_ship(self, ship_symbol: str) -> Dict:
        return self._request("POST", f"/my/ships/{ship_symbol}/dock")

    def orbit_ship(self, ship_symbol: str) -> Dict:
        return self._request("POST", f"/my/ships/{ship_symbol}/orbit")

    def refuel_ship(self, ship_symbol: str) -> Dict:
        return self._request("POST", f"/my/ships/{ship_symbol}/refuel")

    def list_waypoints(
        self,
        system_symbol: str,
        page: int = 1,
        limit: int = 20
    ) -> Dict:
        return self._request(
            "GET",
            f"/systems/{system_symbol}/waypoints",
            params={"page": page, "limit": limit}
        )
```

**`src/spacetraders/adapters/secondary/api/rate_limiter.py`**:
```python
import time
import threading

class RateLimiter:
    """Token bucket rate limiter"""

    def __init__(self, max_requests: int, time_window: float):
        self.max_requests = max_requests
        self.time_window = time_window
        self.tokens = max_requests
        self.last_update = time.time()
        self.lock = threading.Lock()

    def acquire(self) -> None:
        """Acquire token, blocking if necessary"""
        with self.lock:
            now = time.time()
            elapsed = now - self.last_update

            # Replenish tokens
            self.tokens = min(
                self.max_requests,
                self.tokens + (elapsed / self.time_window) * self.max_requests
            )
            self.last_update = now

            # Wait for token if needed
            if self.tokens < 1:
                wait_time = (1 - self.tokens) * (self.time_window / self.max_requests)
                time.sleep(wait_time)
                self.tokens = 0
            else:
                self.tokens -= 1
```

### 4.4 Route Repository

**`src/spacetraders/adapters/secondary/persistence/route_repository.py`**:
```python
import json
import logging
from typing import Optional, List
from datetime import datetime

from ....domain.navigation.route import Route, RouteSegment, RouteStatus
from ....ports.outbound.repositories import IRouteRepository
from .database import Database
from .mappers import RouteMapper

logger = logging.getLogger(__name__)

class RouteRepository(IRouteRepository):
    """SQLite implementation of route repository"""

    def __init__(self, database: Database):
        self._db = database

    def save(self, route: Route) -> None:
        """Persist route to database"""
        with self._db.transaction() as conn:
            cursor = conn.cursor()

            route_data = RouteMapper.to_dict(route)

            cursor.execute("""
                INSERT OR REPLACE INTO routes (
                    route_id, player_id, ship_symbol, status,
                    current_segment_index, segments_data, created_at
                ) VALUES (?, ?, ?, ?, ?, ?, ?)
            """, (
                route.route_id,
                route.player_id,
                route.ship_symbol,
                route.status.value,
                route.get_current_segment_index(),
                json.dumps(route_data["segments"]),
                datetime.now().isoformat()
            ))

            logger.info(f"Saved route {route.route_id}")

    def find_by_id(self, route_id: str) -> Optional[Route]:
        """Load route by ID"""
        with self._db.connection() as conn:
            cursor = conn.cursor()
            cursor.execute("SELECT * FROM routes WHERE route_id = ?", (route_id,))
            row = cursor.fetchone()
            return RouteMapper.from_db_row(row) if row else None

    def find_by_ship(self, player_id: int, ship_symbol: str) -> List[Route]:
        """Find routes for ship"""
        with self._db.connection() as conn:
            cursor = conn.cursor()
            cursor.execute(
                "SELECT * FROM routes WHERE player_id = ? AND ship_symbol = ?",
                (player_id, ship_symbol)
            )
            rows = cursor.fetchall()
            return [RouteMapper.from_db_row(row) for row in rows]
```

**Update database schema** (`database.py`):
```sql
CREATE TABLE IF NOT EXISTS routes (
    route_id TEXT PRIMARY KEY,
    player_id INTEGER NOT NULL,
    ship_symbol TEXT NOT NULL,
    status TEXT NOT NULL,
    current_segment_index INTEGER DEFAULT 0,
    segments_data TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL,
    FOREIGN KEY (player_id) REFERENCES players(player_id)
);

CREATE INDEX IF NOT EXISTS idx_routes_player_ship
ON routes(player_id, ship_symbol);
```

### 4.5 Application Service

**`src/spacetraders/application/services/navigation_service.py`** (real implementation):
```python
import logging
from typing import Dict, Optional
import uuid

from ...domain.navigation.route import Route, RouteSegment, RouteStatus
from ...domain.navigation.exceptions import NoRouteFoundError, RouteExecutionError
from ...domain.shared.value_objects import Waypoint, Fuel, FlightMode
from ...ports.outbound.repositories import IRouteRepository
from ...ports.outbound.api_client import ISpaceTradersAPI
from ...ports.outbound.routing_engine import IRoutingEngine
from ...ports.outbound.graph_provider import ISystemGraphProvider

logger = logging.getLogger(__name__)

class NavigationService:
    """Application service for navigation operations"""

    def __init__(
        self,
        route_repository: IRouteRepository,
        api_client: ISpaceTradersAPI,
        routing_engine: IRoutingEngine,
        graph_provider: ISystemGraphProvider
    ):
        self._route_repo = route_repository
        self._api = api_client
        self._routing = routing_engine
        self._graphs = graph_provider

    def plan_route(
        self,
        player_id: int,
        ship_symbol: str,
        destination: str
    ) -> Route:
        """
        Plan route from ship's current location to destination

        Args:
            player_id: Player ID
            ship_symbol: Ship to navigate
            destination: Destination waypoint symbol

        Returns:
            Planned route

        Raises:
            NoRouteFoundError: If no valid route found
        """
        # Get ship state from API
        ship_data = self._api.get_ship(ship_symbol)
        ship_location = ship_data["data"]["nav"]["waypointSymbol"]
        ship_fuel = ship_data["data"]["fuel"]
        ship_engine = ship_data["data"]["engine"]["speed"]

        # Get system graph
        system = destination.split("-")[0] + "-" + destination.split("-")[1]
        graph_result = self._graphs.get_graph(system)

        # Find optimal path using routing engine
        path_result = self._routing.find_optimal_path(
            graph=graph_result.graph,
            start=ship_location,
            goal=destination,
            current_fuel=ship_fuel["current"],
            fuel_capacity=ship_fuel["capacity"],
            engine_speed=ship_engine,
            prefer_cruise=True
        )

        if not path_result:
            raise NoRouteFoundError(
                f"No route found from {ship_location} to {destination}"
            )

        # Convert routing result to domain Route
        segments = []
        for step in path_result["steps"]:
            if step["action"] == "navigate":
                from_wp = Waypoint(
                    symbol=step["from"],
                    x=graph_result.graph["waypoints"][step["from"]]["x"],
                    y=graph_result.graph["waypoints"][step["from"]]["y"]
                )
                to_wp = Waypoint(
                    symbol=step["to"],
                    x=graph_result.graph["waypoints"][step["to"]]["x"],
                    y=graph_result.graph["waypoints"][step["to"]]["y"]
                )

                segment = RouteSegment(
                    from_waypoint=from_wp,
                    to_waypoint=to_wp,
                    distance=step["distance"],
                    fuel_required=step["fuel_cost"],
                    travel_time=step["time"],
                    flight_mode=FlightMode[step["mode"]],
                    requires_refuel=step.get("action") == "refuel"
                )
                segments.append(segment)

        # Create route domain object
        route_id = f"ROUTE-{uuid.uuid4().hex[:8].upper()}"
        route = Route(
            route_id=route_id,
            ship_symbol=ship_symbol,
            player_id=player_id,
            segments=segments,
            ship_fuel_capacity=ship_fuel["capacity"]
        )

        # Persist route
        self._route_repo.save(route)

        logger.info(f"Planned route {route_id}: {ship_location} → {destination}")
        return route

    def execute_route(
        self,
        player_id: int,
        route_id: str
    ) -> Dict:
        """
        Execute planned route

        Args:
            player_id: Player ID
            route_id: Route to execute

        Returns:
            Execution result dict
        """
        # Load route
        route = self._route_repo.find_by_id(route_id)
        if not route:
            raise RouteExecutionError(f"Route {route_id} not found")

        if route.player_id != player_id:
            raise RouteExecutionError("Route belongs to different player")

        # Start execution
        route.start_execution()
        self._route_repo.save(route)

        # Execute segments
        for segment in route.remaining_segments():
            logger.info(f"Executing segment: {segment}")

            # Handle refuel if needed
            if segment.requires_refuel:
                self._api.dock_ship(route.ship_symbol)
                self._api.refuel_ship(route.ship_symbol)
                self._api.orbit_ship(route.ship_symbol)

            # Navigate
            nav_result = self._api.navigate_ship(
                route.ship_symbol,
                segment.to_waypoint.symbol
            )

            # Complete segment
            route.complete_segment()
            self._route_repo.save(route)

        logger.info(f"Route {route_id} completed")

        return {
            "route_id": route_id,
            "status": route.status.value,
            "ship_symbol": route.ship_symbol
        }
```

### 4.6 CLI Commands

**`src/spacetraders/adapters/primary/cli/navigation_cli.py`**:
```python
import argparse
from ....configuration.container import get_navigation_service

def plan_route_command(args: argparse.Namespace) -> int:
    """Handle plan route command"""
    service = get_navigation_service(args.player_id)

    try:
        route = service.plan_route(
            player_id=args.player_id,
            ship_symbol=args.ship,
            destination=args.to
        )

        print(f"✅ Planned route {route.route_id}")
        print(f"   Ship: {route.ship_symbol}")
        print(f"   Segments: {len(route.segments)}")
        print(f"   Distance: {route.total_distance():.1f} units")
        print(f"   Fuel: {route.total_fuel_required()}")
        print(f"   Time: {route.total_travel_time()}s")

        for i, seg in enumerate(route.segments, 1):
            print(f"   [{i}] {seg}")

        return 0
    except Exception as e:
        print(f"❌ Error: {e}")
        return 1

def execute_route_command(args: argparse.Namespace) -> int:
    """Handle execute route command"""
    service = get_navigation_service(args.player_id)

    try:
        result = service.execute_route(
            player_id=args.player_id,
            route_id=args.route_id
        )

        print(f"✅ Route executed successfully")
        print(f"   Route: {result['route_id']}")
        print(f"   Ship: {result['ship_symbol']}")
        print(f"   Status: {result['status']}")

        return 0
    except Exception as e:
        print(f"❌ Error: {e}")
        return 1

def setup_navigation_commands(subparsers):
    """Setup navigation CLI commands"""
    nav_parser = subparsers.add_parser("navigate", help="Navigation commands")
    nav_subparsers = nav_parser.add_subparsers(dest="nav_command")

    # Plan command
    plan_parser = nav_subparsers.add_parser("plan", help="Plan route")
    plan_parser.add_argument("--player-id", type=int, required=True)
    plan_parser.add_argument("--ship", required=True)
    plan_parser.add_argument("--to", required=True)
    plan_parser.set_defaults(func=plan_route_command)

    # Execute command
    exec_parser = nav_subparsers.add_parser("execute", help="Execute route")
    exec_parser.add_argument("--player-id", type=int, required=True)
    exec_parser.add_argument("--route-id", required=True)
    exec_parser.set_defaults(func=execute_route_command)
```

---

## Phase 5: Daemon Infrastructure

**Goal**: Add container orchestration to run long-running operations.

### 5.1 Daemon Server Architecture

**Copy from old codebase**:
- Unix socket server: `var/daemon.sock`
- JSON-RPC 2.0 protocol
- Asyncio event loop for all operations
- Container state machine: STARTING → RUNNING → STOPPED/FAILED
- Health monitoring (60s interval)
- Graceful shutdown (SIGTERM/SIGINT)

### 5.2 Container Manager

**`src/spacetraders/adapters/primary/daemon/container_manager.py`**:
```python
import asyncio
import logging
from dataclasses import dataclass
from enum import Enum
from typing import Optional, Dict, Any
from datetime import datetime

logger = logging.getLogger(__name__)

class ContainerStatus(Enum):
    """Container lifecycle status"""
    STARTING = "STARTING"
    RUNNING = "RUNNING"
    STOPPING = "STOPPING"
    STOPPED = "STOPPED"
    FAILED = "FAILED"

class RestartPolicy(Enum):
    """Container restart policy"""
    NO = "no"
    ON_FAILURE = "on-failure"
    ALWAYS = "always"
    UNLESS_STOPPED = "unless-stopped"

@dataclass
class ContainerInfo:
    """Container metadata"""
    container_id: str
    player_id: int
    container_type: str
    status: ContainerStatus
    restart_policy: RestartPolicy
    restart_count: int
    max_restarts: int
    config: Dict[str, Any]
    task: Optional[asyncio.Task]
    logs: list
    started_at: Optional[datetime]
    stopped_at: Optional[datetime]
    exit_code: Optional[int]
    exit_reason: Optional[str]

class ContainerManager:
    """Manage container lifecycle"""

    def __init__(self):
        self._containers: Dict[str, ContainerInfo] = {}
        self._lock = asyncio.Lock()

    async def create_container(
        self,
        container_id: str,
        player_id: int,
        container_type: str,
        config: Dict[str, Any],
        restart_policy: str = "no",
        max_restarts: int = 3
    ) -> ContainerInfo:
        """Create new container"""
        async with self._lock:
            if container_id in self._containers:
                raise ValueError(f"Container {container_id} already exists")

            info = ContainerInfo(
                container_id=container_id,
                player_id=player_id,
                container_type=container_type,
                status=ContainerStatus.STARTING,
                restart_policy=RestartPolicy(restart_policy),
                restart_count=0,
                max_restarts=max_restarts,
                config=config,
                task=None,
                logs=[],
                started_at=None,
                stopped_at=None,
                exit_code=None,
                exit_reason=None
            )

            self._containers[container_id] = info

            # Start container task
            info.task = asyncio.create_task(
                self._run_container(info)
            )

            logger.info(f"Created container {container_id}")
            return info

    async def _run_container(self, info: ContainerInfo):
        """Run container operation"""
        try:
            info.status = ContainerStatus.RUNNING
            info.started_at = datetime.now()

            # Execute operation based on container type
            if info.container_type == "navigation":
                await self._run_navigation_container(info)
            else:
                raise ValueError(f"Unknown container type: {info.container_type}")

            info.status = ContainerStatus.STOPPED
            info.exit_code = 0

        except Exception as e:
            logger.error(f"Container {info.container_id} failed: {e}")
            info.status = ContainerStatus.FAILED
            info.exit_code = 1
            info.exit_reason = str(e)

            # Handle restart policy
            await self._handle_restart(info)

        finally:
            info.stopped_at = datetime.now()

    async def _run_navigation_container(self, info: ContainerInfo):
        """Run navigation operation"""
        from ....configuration.container import get_navigation_service

        service = get_navigation_service(info.player_id)

        # Execute route
        result = service.execute_route(
            player_id=info.player_id,
            route_id=info.config["route_id"]
        )

        info.logs.append(f"Route executed: {result}")

    async def _handle_restart(self, info: ContainerInfo):
        """Handle container restart based on policy"""
        if info.restart_policy == RestartPolicy.NO:
            return

        if info.restart_policy == RestartPolicy.ON_FAILURE and info.exit_code == 0:
            return

        if info.restart_count >= info.max_restarts:
            logger.warning(f"Container {info.container_id} exceeded max restarts")
            return

        # Exponential backoff
        wait_time = min(60, 2 ** info.restart_count)
        logger.info(f"Restarting container {info.container_id} in {wait_time}s")
        await asyncio.sleep(wait_time)

        info.restart_count += 1
        info.status = ContainerStatus.STARTING
        info.task = asyncio.create_task(self._run_container(info))

    async def stop_container(self, container_id: str) -> None:
        """Stop container"""
        async with self._lock:
            info = self._containers.get(container_id)
            if not info:
                raise ValueError(f"Container {container_id} not found")

            if info.task and not info.task.done():
                info.status = ContainerStatus.STOPPING
                info.task.cancel()
                try:
                    await info.task
                except asyncio.CancelledError:
                    pass

            info.status = ContainerStatus.STOPPED
            logger.info(f"Stopped container {container_id}")

    def get_container(self, container_id: str) -> Optional[ContainerInfo]:
        """Get container info"""
        return self._containers.get(container_id)

    def list_containers(self) -> list:
        """List all containers"""
        return list(self._containers.values())
```

### 5.3 Ship Assignment Manager

**`src/spacetraders/adapters/primary/daemon/assignment_manager.py`**:
```python
import logging
from typing import Optional
from datetime import datetime

from ....adapters.secondary.persistence.database import Database

logger = logging.getLogger(__name__)

class ShipAssignmentManager:
    """
    Manage ship assignments to prevent double-booking

    Ensures ships are only assigned to one operation at a time
    """

    def __init__(self, database: Database):
        self._db = database

    def assign(
        self,
        player_id: int,
        ship_symbol: str,
        daemon_id: str,
        operation: str
    ) -> bool:
        """
        Assign ship to operation

        Returns:
            True if assignment successful, False if already assigned
        """
        with self._db.transaction() as conn:
            cursor = conn.cursor()

            # Check availability
            cursor.execute("""
                SELECT status, daemon_id FROM ship_assignments
                WHERE ship_symbol = ? AND player_id = ?
            """, (ship_symbol, player_id))

            row = cursor.fetchone()
            if row and row["status"] == "active":
                logger.warning(
                    f"Ship {ship_symbol} already assigned to {row['daemon_id']}"
                )
                return False

            # Assign
            cursor.execute("""
                INSERT OR REPLACE INTO ship_assignments (
                    ship_symbol, player_id, daemon_id, operation,
                    status, assigned_at
                ) VALUES (?, ?, ?, ?, 'active', ?)
            """, (
                ship_symbol,
                player_id,
                daemon_id,
                operation,
                datetime.now().isoformat()
            ))

            logger.info(f"Assigned {ship_symbol} to {daemon_id}")
            return True

    def release(
        self,
        player_id: int,
        ship_symbol: str,
        reason: str = "completed"
    ) -> None:
        """Release ship assignment"""
        with self._db.transaction() as conn:
            cursor = conn.cursor()
            cursor.execute("""
                UPDATE ship_assignments
                SET status = 'idle',
                    released_at = ?,
                    release_reason = ?
                WHERE ship_symbol = ? AND player_id = ?
            """, (
                datetime.now().isoformat(),
                reason,
                ship_symbol,
                player_id
            ))
            logger.info(f"Released {ship_symbol}")

    def check_available(
        self,
        player_id: int,
        ship_symbol: str
    ) -> bool:
        """Check if ship is available"""
        with self._db.connection() as conn:
            cursor = conn.cursor()
            cursor.execute("""
                SELECT status FROM ship_assignments
                WHERE ship_symbol = ? AND player_id = ?
            """, (ship_symbol, player_id))

            row = cursor.fetchone()
            return not row or row["status"] != "active"
```

**Update database schema**:
```sql
CREATE TABLE IF NOT EXISTS ship_assignments (
    ship_symbol TEXT NOT NULL,
    player_id INTEGER NOT NULL,
    daemon_id TEXT,
    operation TEXT,
    status TEXT DEFAULT 'idle',
    assigned_at TIMESTAMP,
    released_at TIMESTAMP,
    release_reason TEXT,
    PRIMARY KEY (ship_symbol, player_id),
    FOREIGN KEY (player_id) REFERENCES players(player_id)
);

CREATE TABLE IF NOT EXISTS containers (
    container_id TEXT NOT NULL,
    player_id INTEGER NOT NULL,
    container_type TEXT,
    status TEXT,
    restart_policy TEXT,
    restart_count INTEGER DEFAULT 0,
    config TEXT,
    started_at TIMESTAMP,
    stopped_at TIMESTAMP,
    exit_code INTEGER,
    exit_reason TEXT,
    PRIMARY KEY (container_id, player_id),
    FOREIGN KEY (player_id) REFERENCES players(player_id)
);

CREATE TABLE IF NOT EXISTS container_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    container_id TEXT,
    player_id INTEGER,
    timestamp TIMESTAMP,
    message TEXT
);
```

### 5.4 Daemon Server

**`src/spacetraders/adapters/primary/daemon/daemon_server.py`**:
```python
import asyncio
import json
import logging
import signal
import socket
from pathlib import Path
from typing import Dict, Any

from .container_manager import ContainerManager
from .assignment_manager import ShipAssignmentManager
from ....configuration.container import get_database

logger = logging.getLogger(__name__)

class DaemonServer:
    """
    Daemon server for long-running operations

    - Unix socket at var/daemon.sock
    - JSON-RPC 2.0 protocol
    - Asyncio event loop
    - Graceful shutdown
    """

    SOCKET_PATH = Path("var/daemon.sock")

    def __init__(self):
        self._container_mgr = ContainerManager()
        self._assignment_mgr = ShipAssignmentManager(get_database())
        self._server: Optional[asyncio.Server] = None
        self._running = False

    async def start(self):
        """Start daemon server"""
        # Cleanup old socket
        if self.SOCKET_PATH.exists():
            self.SOCKET_PATH.unlink()

        self.SOCKET_PATH.parent.mkdir(parents=True, exist_ok=True)

        # Create Unix socket server
        self._server = await asyncio.start_unix_server(
            self._handle_connection,
            path=str(self.SOCKET_PATH)
        )

        # Set permissions (owner read/write)
        self.SOCKET_PATH.chmod(0o660)

        self._running = True
        logger.info(f"Daemon server started on {self.SOCKET_PATH}")

        # Register signal handlers
        for sig in (signal.SIGTERM, signal.SIGINT):
            asyncio.get_event_loop().add_signal_handler(
                sig,
                lambda: asyncio.create_task(self.stop())
            )

        # Start health monitor
        asyncio.create_task(self._health_monitor())

        async with self._server:
            await self._server.serve_forever()

    async def stop(self):
        """Stop daemon server gracefully"""
        if not self._running:
            return

        logger.info("Shutting down daemon server...")
        self._running = False

        # Stop all containers
        for container in self._container_mgr.list_containers():
            await self._container_mgr.stop_container(container.container_id)

        # Close server
        if self._server:
            self._server.close()
            await self._server.wait_closed()

        # Cleanup socket
        if self.SOCKET_PATH.exists():
            self.SOCKET_PATH.unlink()

        logger.info("Daemon server stopped")

    async def _handle_connection(
        self,
        reader: asyncio.StreamReader,
        writer: asyncio.StreamWriter
    ):
        """Handle client connection"""
        try:
            data = await reader.read(65536)
            request = json.loads(data.decode())

            # Process JSON-RPC request
            response = await self._process_request(request)

            # Send response
            writer.write(json.dumps(response).encode())
            await writer.drain()

        except Exception as e:
            logger.error(f"Error handling connection: {e}")
            error_response = {
                "jsonrpc": "2.0",
                "error": {"code": -32603, "message": str(e)},
                "id": None
            }
            writer.write(json.dumps(error_response).encode())
            await writer.drain()

        finally:
            writer.close()
            await writer.wait_closed()

    async def _process_request(self, request: Dict) -> Dict:
        """Process JSON-RPC request"""
        method = request.get("method")
        params = request.get("params", {})
        request_id = request.get("id")

        try:
            if method == "container.create":
                result = await self._create_container(params)
            elif method == "container.stop":
                result = await self._stop_container(params)
            elif method == "container.inspect":
                result = self._inspect_container(params)
            elif method == "container.list":
                result = self._list_containers(params)
            else:
                raise ValueError(f"Unknown method: {method}")

            return {
                "jsonrpc": "2.0",
                "result": result,
                "id": request_id
            }

        except Exception as e:
            return {
                "jsonrpc": "2.0",
                "error": {"code": -32603, "message": str(e)},
                "id": request_id
            }

    async def _create_container(self, params: Dict) -> Dict:
        """Create container"""
        container_id = params["container_id"]
        player_id = params["player_id"]
        container_type = params["container_type"]
        config = params.get("config", {})

        # Check ship assignment
        ship_symbol = config.get("ship_symbol")
        if ship_symbol:
            if not self._assignment_mgr.assign(
                player_id, ship_symbol, container_id, container_type
            ):
                raise ValueError(f"Ship {ship_symbol} already assigned")

        # Create container
        info = await self._container_mgr.create_container(
            container_id=container_id,
            player_id=player_id,
            container_type=container_type,
            config=config,
            restart_policy=params.get("restart_policy", "no"),
            max_restarts=params.get("max_restarts", 3)
        )

        return {
            "container_id": info.container_id,
            "status": info.status.value
        }

    async def _stop_container(self, params: Dict) -> Dict:
        """Stop container"""
        container_id = params["container_id"]
        await self._container_mgr.stop_container(container_id)

        # Release ship assignment
        info = self._container_mgr.get_container(container_id)
        if info and info.config.get("ship_symbol"):
            self._assignment_mgr.release(
                info.player_id,
                info.config["ship_symbol"],
                reason="stopped"
            )

        return {"container_id": container_id, "status": "stopped"}

    def _inspect_container(self, params: Dict) -> Dict:
        """Inspect container"""
        container_id = params["container_id"]
        info = self._container_mgr.get_container(container_id)

        if not info:
            raise ValueError(f"Container {container_id} not found")

        return {
            "container_id": info.container_id,
            "player_id": info.player_id,
            "type": info.container_type,
            "status": info.status.value,
            "restart_count": info.restart_count,
            "started_at": info.started_at.isoformat() if info.started_at else None,
            "exit_code": info.exit_code
        }

    def _list_containers(self, params: Dict) -> Dict:
        """List containers"""
        containers = []
        for info in self._container_mgr.list_containers():
            containers.append({
                "container_id": info.container_id,
                "player_id": info.player_id,
                "type": info.container_type,
                "status": info.status.value
            })

        return {"containers": containers}

    async def _health_monitor(self):
        """Monitor container health"""
        while self._running:
            await asyncio.sleep(60)

            # Check all containers
            for info in self._container_mgr.list_containers():
                if info.task and info.task.done():
                    # Container finished, check restart policy
                    logger.info(f"Container {info.container_id} finished")

def main():
    """Entry point for daemon server"""
    logging.basicConfig(
        level=logging.INFO,
        format='%(asctime)s - %(name)s - %(levelname)s - %(message)s'
    )

    server = DaemonServer()

    try:
        asyncio.run(server.start())
    except KeyboardInterrupt:
        logger.info("Interrupted by user")
```

### 5.5 Daemon CLI Commands

**`src/spacetraders/adapters/primary/cli/daemon_cli.py`**:
```python
import argparse
import json
import socket
from pathlib import Path

SOCKET_PATH = Path("var/daemon.sock")

def daemon_server_command(args: argparse.Namespace) -> int:
    """Start daemon server"""
    from ...adapters.primary.daemon.daemon_server import main as daemon_main
    daemon_main()
    return 0

def daemon_start_command(args: argparse.Namespace) -> int:
    """Start daemon container"""
    request = {
        "jsonrpc": "2.0",
        "method": "container.create",
        "params": {
            "container_id": args.daemon_id,
            "player_id": args.player_id,
            "container_type": args.type,
            "config": {
                "ship_symbol": args.ship,
                "route_id": args.route_id
            },
            "restart_policy": args.restart_policy
        },
        "id": 1
    }

    response = _send_request(request)
    if "result" in response:
        print(f"✅ Started container {response['result']['container_id']}")
        return 0
    else:
        print(f"❌ Error: {response['error']}")
        return 1

def daemon_stop_command(args: argparse.Namespace) -> int:
    """Stop daemon container"""
    request = {
        "jsonrpc": "2.0",
        "method": "container.stop",
        "params": {"container_id": args.daemon_id},
        "id": 1
    }

    response = _send_request(request)
    if "result" in response:
        print(f"✅ Stopped container {args.daemon_id}")
        return 0
    else:
        print(f"❌ Error: {response['error']}")
        return 1

def daemon_list_command(args: argparse.Namespace) -> int:
    """List daemon containers"""
    request = {
        "jsonrpc": "2.0",
        "method": "container.list",
        "params": {},
        "id": 1
    }

    response = _send_request(request)
    if "result" in response:
        containers = response["result"]["containers"]
        if not containers:
            print("No containers running")
            return 0

        print(f"Containers ({len(containers)}):")
        for c in containers:
            print(f"  [{c['container_id']}] {c['type']} - {c['status']}")
        return 0
    else:
        print(f"❌ Error: {response['error']}")
        return 1

def _send_request(request: dict) -> dict:
    """Send JSON-RPC request to daemon"""
    sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    sock.connect(str(SOCKET_PATH))

    sock.sendall(json.dumps(request).encode())
    response_data = sock.recv(65536)

    sock.close()
    return json.loads(response_data.decode())

def setup_daemon_commands(subparsers):
    """Setup daemon CLI commands"""
    daemon_parser = subparsers.add_parser("daemon", help="Daemon operations")
    daemon_subparsers = daemon_parser.add_subparsers(dest="daemon_command")

    # Server command
    server_parser = daemon_subparsers.add_parser("server", help="Start daemon server")
    server_parser.set_defaults(func=daemon_server_command)

    # Start command
    start_parser = daemon_subparsers.add_parser("start", help="Start container")
    start_parser.add_argument("--daemon-id", required=True)
    start_parser.add_argument("--player-id", type=int, required=True)
    start_parser.add_argument("--type", required=True, choices=["navigation"])
    start_parser.add_argument("--ship", required=True)
    start_parser.add_argument("--route-id", required=True)
    start_parser.add_argument("--restart-policy", default="no")
    start_parser.set_defaults(func=daemon_start_command)

    # Stop command
    stop_parser = daemon_subparsers.add_parser("stop", help="Stop container")
    stop_parser.add_argument("--daemon-id", required=True)
    stop_parser.set_defaults(func=daemon_stop_command)

    # List command
    list_parser = daemon_subparsers.add_parser("list", help="List containers")
    list_parser.set_defaults(func=daemon_list_command)
```

---

## Phase 6: Future Bounded Contexts

### 6.1 Contracts Domain

Follow same pattern as Navigation:
1. **Domain layer**: Contract aggregate, Deliverable VO, domain services
2. **Ports**: Repository, API client interfaces
3. **Application**: ContractService orchestration
4. **Adapters**: Persistence, CLI, daemon support

Reuse navigation for delivery routes.

### 6.2 Trading Domain

Complex domain with:
- TradeRoute aggregate (buy/sell sequence)
- Circuit breaker (price validation)
- Market data repository
- Route optimization with OR-Tools

### 6.3 Mining Domain

- MiningCycle aggregate
- Extraction site value objects
- Yield estimation services
- Cooldown management

### 6.4 Scouting Domain

- Market intelligence gathering
- Fleet partitioning (OR-Tools VRP)
- Data freshness tracking

---

## Testing Strategy

### Domain Tests (No Mocks)

**Pure business logic testing**:
```python
def test_fuel_consumption():
    fuel = Fuel(current=100, capacity=100)
    new_fuel = fuel.consume(50)
    assert new_fuel.current == 50
    assert fuel.current == 100  # Immutable

def test_route_validates_segments_connected():
    # Disconnected segments
    seg1 = RouteSegment(from="A", to="B", ...)
    seg2 = RouteSegment(from="C", to="D", ...)  # Not connected!

    with pytest.raises(InvalidRouteError):
        Route(route_id="R1", segments=[seg1, seg2], ...)
```

### Application Tests (Mocked Ports)

**Test orchestration logic**:
```python
def test_navigation_service_plans_route(mock_routing_engine, mock_api_client):
    service = NavigationService(
        route_repository=mock_route_repo,
        api_client=mock_api_client,
        routing_engine=mock_routing_engine,
        graph_provider=mock_graph_provider
    )

    route = service.plan_route(player_id=1, ship="SHIP-1", destination="X1-B2")

    assert route.route_id is not None
    assert mock_routing_engine.find_optimal_path.called
```

### Integration Tests (Real Adapters)

**End-to-end with real infrastructure**:
```python
def test_end_to_end_navigation(real_database, real_api_client):
    # Uses real SQLite database
    # Uses real SpaceTraders API (or sandbox)

    service = create_navigation_service()  # Real wiring
    route = service.plan_route(...)
    result = service.execute_route(...)

    assert result["status"] == "completed"
```

---

## Key Principles

### Dependency Rule

```
Domain (no dependencies)
   ↓
Application (depends on Domain)
   ↓
Adapters (depend on Application + Domain)
```

**NEVER**: Domain depends on Application or Adapters
**NEVER**: Application depends on Adapters

### Port Isolation

- Domain defines interfaces (`IPlayerRepository`, `IRoutingEngine`)
- Infrastructure implements interfaces
- Domain/Application never import from infrastructure

### Immutable Value Objects

All VOs are `@dataclass(frozen=True)`:
```python
@dataclass(frozen=True)
class Fuel:
    current: int
    capacity: int

    def consume(self, amount: int) -> 'Fuel':
        return Fuel(current=self.current - amount, capacity=self.capacity)
```

### Entity Encapsulation

Modify entities via methods only:
```python
# WRONG
player._last_active = datetime.now()

# RIGHT
player.update_last_active()
```

### Multi-Tenancy

All operations scoped by `player_id`:
```python
service.plan_route(player_id=1, ship="SHIP-1", ...)
```

### Test-First Development

1. Write failing BDD test (Gherkin)
2. Implement domain logic
3. Implement application service
4. Implement adapters
5. Verify test passes

---

## Summary

This plan follows the **Walking Skeleton → Domain-First → Infrastructure Last** approach:

1. **Phase 1**: Prove architecture with minimal vertical slice
2. **Phases 2-3**: Build domain-first (Player, Navigation)
3. **Phase 4**: Add infrastructure adapters (OR-Tools, API, DB)
4. **Phase 5**: Add daemon orchestration (wraps existing services)
5. **Phase 6**: Replicate pattern for new bounded contexts

**Key Success Metrics**:
- All tests pass (BDD with pytest-bdd)
- CLI commands work end-to-end
- Daemon containers run autonomously
- OR-Tools routing performs optimally
- Ship assignments prevent double-booking
- Multi-tenancy supports multiple players
- Architecture proven flexible and maintainable
