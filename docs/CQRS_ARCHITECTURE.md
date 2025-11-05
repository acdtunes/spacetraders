# CQRS Architecture with pymediatr

**Pattern**: MediatR (Command/Query Responsibility Segregation)
**Library**: [pymediatr](https://github.com/megafon-it/pymediatr)

---

## Why pymediatr?

1. **Battle-tested pattern** - Ported from .NET MediatR (very mature)
2. **Pipeline behaviors** - Built-in middleware support
3. **Async support** - Native async/await
4. **Type-safe** - Full typing support
5. **Clean CQRS** - Explicit commands and queries
6. **Decoupled** - Handlers know nothing about each other

---

## Architecture Overview

```
┌─────────────────────────────────────────────────────┐
│                CLI / Daemon (Primary)                │
│    spacetraders player register --agent AGENT-1     │
└────────────────────┬────────────────────────────────┘
                     │
                     ▼
         ┌───────────────────────┐
         │    pymediatr.Mediator  │  ◄─── Request routing
         │   (Command/Query Bus)  │
         └───────┬───────────────┘
                 │ routes to
                 │
        ┌────────┴────────┐
        │                 │
        ▼                 ▼
  ┌──────────────┐  ┌──────────────┐
  │  Commands     │  │   Queries     │  ◄─── Application Layer
  │  (Writes)     │  │   (Reads)     │       (Use Cases)
  └──────┬────────┘  └──────┬────────┘
         │                  │
         │ handled by       │ handled by
         │                  │
         ▼                  ▼
  ┌──────────────┐  ┌──────────────┐
  │ CommandHandler│  │ QueryHandler  │
  │ (orchestrate) │  │ (fetch data)  │
  └──────┬────────┘  └──────┬────────┘
         │                  │
         │ uses             │ uses
         │                  │
         ▼                  ▼
  ┌────────────────────────────────┐
  │        Domain Layer             │  ◄─── Business Logic
  │  (Entities, VOs, Services)      │
  └────────────────────────────────┘
         │
         │ uses
         ▼
  ┌────────────────────────────────┐
  │     Infrastructure Layer        │  ◄─── Adapters
  │  (Repositories, API, OR-Tools)  │
  └────────────────────────────────┘
```

---

## Project Structure

```
bot-v2/
├── src/spacetraders/
│   │
│   ├── domain/                     # Pure business logic
│   │   ├── shared/
│   │   │   ├── player.py
│   │   │   ├── ship.py
│   │   │   ├── route.py
│   │   │   └── system_graph.py
│   │   │
│   │   └── navigation/
│   │       └── route_planner.py    # ORToolsPlanner domain service
│   │
│   ├── application/                # Use cases via CQRS
│   │   │
│   │   ├── commands/               # Write operations
│   │   │   ├── player/
│   │   │   │   ├── register_player.py
│   │   │   │   │   - RegisterPlayerCommand
│   │   │   │   │   - RegisterPlayerHandler(RequestHandler)
│   │   │   │   │
│   │   │   │   └── update_player_metadata.py
│   │   │   │       - UpdatePlayerMetadataCommand
│   │   │   │       - UpdatePlayerMetadataHandler
│   │   │   │
│   │   │   └── navigation/
│   │   │       ├── navigate_ship.py
│   │   │       │   - NavigateShipCommand
│   │   │       │   - NavigateShipHandler
│   │   │       │
│   │   │       └── plan_route.py
│   │   │           - PlanRouteCommand
│   │   │           - PlanRouteHandler
│   │   │
│   │   ├── queries/                # Read operations
│   │   │   ├── player/
│   │   │   │   └── get_player.py
│   │   │   │       - GetPlayerQuery
│   │   │   │       - GetPlayerHandler(RequestHandler)
│   │   │   │
│   │   │   └── navigation/
│   │   │       └── get_route.py
│   │   │           - GetRouteQuery
│   │   │           - GetRouteHandler
│   │   │
│   │   ├── behaviors/              # Pipeline behaviors (middleware)
│   │   │   ├── logging_behavior.py
│   │   │   ├── validation_behavior.py
│   │   │   └── transaction_behavior.py
│   │   │
│   │   └── mediator.py             # Mediator setup
│   │
│   ├── ports/                      # Interfaces
│   │   ├── inbound/                # Not needed with MediatR
│   │   └── outbound/               # Repository interfaces
│   │
│   ├── adapters/                   # Infrastructure
│   │   ├── primary/
│   │   │   ├── cli/
│   │   │   └── daemon/
│   │   │
│   │   └── secondary/
│   │       ├── persistence/
│   │       ├── api/
│   │       └── routing/
│   │
│   └── configuration/              # DI container
│       └── container.py
│
├── pyproject.toml
└── README.md
```

---

## Implementation Patterns

### 1. Command (Write Operation)

**`application/commands/player/register_player.py`:**

```python
from dataclasses import dataclass
from pymediatr import Request, RequestHandler
from datetime import datetime, timezone

from ....domain.shared.player import Player
from ....domain.shared.exceptions import DuplicateAgentSymbolError
from ....ports.outbound.repositories import IPlayerRepository

# Command DTO (Request)
@dataclass(frozen=True)
class RegisterPlayerCommand(Request):
    """
    Command to register a new player.

    Immutable data transfer object representing the user's intent.
    """
    agent_symbol: str
    token: str
    metadata: dict | None = None

# Command Handler
class RegisterPlayerHandler(RequestHandler[RegisterPlayerCommand, Player]):
    """
    Handles player registration.

    Orchestrates:
    1. Validation (uniqueness check)
    2. Domain entity creation
    3. Persistence via repository
    """

    def __init__(self, player_repository: IPlayerRepository):
        self._player_repo = player_repository

    async def handle(self, request: RegisterPlayerCommand) -> Player:
        """
        Execute player registration command.

        Args:
            request: RegisterPlayerCommand with agent/token

        Returns:
            Created Player entity

        Raises:
            DuplicateAgentSymbolError: If agent already exists
        """
        # 1. Validate uniqueness
        if self._player_repo.exists_by_agent_symbol(request.agent_symbol):
            raise DuplicateAgentSymbolError(
                f"Agent '{request.agent_symbol}' already registered"
            )

        # 2. Create domain entity
        player = Player(
            player_id=None,  # Assigned by repository
            agent_symbol=request.agent_symbol,
            token=request.token,
            created_at=datetime.now(timezone.utc),
            metadata=request.metadata or {}
        )

        # 3. Persist
        created_player = self._player_repo.create(player)

        return created_player
```

### 2. Query (Read Operation)

**`application/queries/player/get_player.py`:**

```python
from dataclasses import dataclass
from pymediatr import Request, RequestHandler

from ....domain.shared.player import Player
from ....domain.shared.exceptions import PlayerNotFoundError
from ....ports.outbound.repositories import IPlayerRepository

# Query DTO (Request)
@dataclass(frozen=True)
class GetPlayerQuery(Request):
    """Query to retrieve player by ID or agent symbol"""
    player_id: int | None = None
    agent_symbol: str | None = None

# Query Handler
class GetPlayerHandler(RequestHandler[GetPlayerQuery, Player]):
    """Handles player retrieval"""

    def __init__(self, player_repository: IPlayerRepository):
        self._player_repo = player_repository

    async def handle(self, request: GetPlayerQuery) -> Player:
        """
        Retrieve player from repository.

        Args:
            request: GetPlayerQuery with player_id or agent_symbol

        Returns:
            Player entity

        Raises:
            PlayerNotFoundError: If player not found
            ValueError: If neither player_id nor agent_symbol provided
        """
        if request.player_id:
            player = self._player_repo.find_by_id(request.player_id)
        elif request.agent_symbol:
            player = self._player_repo.find_by_agent_symbol(request.agent_symbol)
        else:
            raise ValueError("Must provide player_id or agent_symbol")

        if not player:
            raise PlayerNotFoundError("Player not found")

        return player
```

### 3. Complex Command (Navigation)

**`application/commands/navigation/navigate_ship.py`:**

```python
from dataclasses import dataclass
from pymediatr import Request, RequestHandler

from ....domain.shared.ship import Ship
from ....domain.shared.route import Route
from ....domain.shared.value_objects import Waypoint
from ....domain.shared.exceptions import NoRouteFoundError, InvalidShipStateError
from ....ports.outbound.repositories import IShipRepository

@dataclass(frozen=True)
class NavigateShipCommand(Request):
    """Command to navigate ship to destination"""
    ship_symbol: str
    destination: str
    player_id: int

@dataclass(frozen=True)
class NavigationResult:
    """Result of navigation operation"""
    success: bool
    ship: Ship
    route: Route
    message: str

class NavigateShipHandler(RequestHandler[NavigateShipCommand, NavigationResult]):
    """
    Handles ship navigation.

    Orchestrates:
    1. Load ship from repository
    2. Load system graph
    3. Plan route (domain: Ship.plan_route())
    4. Execute route (infrastructure: ShipController)
    5. Persist updated ship
    """

    def __init__(
        self,
        ship_repository: IShipRepository,
        graph_provider,  # ISystemGraphProvider
        ship_controller,  # ShipController
    ):
        self._ship_repo = ship_repository
        self._graph_provider = graph_provider
        self._controller = ship_controller

    async def handle(self, request: NavigateShipCommand) -> NavigationResult:
        """
        Execute ship navigation command.

        Pattern:
        1. Load ship (infrastructure)
        2. Plan route (domain logic)
        3. Execute route (infrastructure)
        4. Update ship state (domain)
        5. Persist (infrastructure)
        """
        # 1. Load ship
        ship = self._ship_repo.get_ship(request.ship_symbol)
        if not ship:
            raise ValueError(f"Ship {request.ship_symbol} not found")

        # 2. Already at destination?
        if ship.current_location.symbol == request.destination:
            return NavigationResult(
                success=True,
                ship=ship,
                route=None,
                message=f"Already at {request.destination}"
            )

        # 3. Load system graph
        system = self._extract_system(request.destination)
        graph_result = self._graph_provider.get_graph(system)

        # 4. Plan route (DOMAIN)
        destination = Waypoint(symbol=request.destination, x=0, y=0)
        route = ship.plan_route(destination, graph_result.graph)

        if not route:
            raise NoRouteFoundError(f"No route to {request.destination}")

        # 5. Validate (DOMAIN)
        if not ship.validate_route_feasibility(route):
            raise InvalidShipStateError("Route not feasible")

        # 6. Execute route (INFRASTRUCTURE)
        await self._execute_route(ship, route)

        # 7. Persist
        self._ship_repo.update_ship(ship)

        return NavigationResult(
            success=True,
            ship=ship,
            route=route,
            message=f"Navigated to {request.destination}"
        )

    async def _execute_route(self, ship: Ship, route: Route) -> None:
        """Execute route steps via infrastructure"""
        for step in route.steps:
            if step.is_navigate:
                # State machine: ensure in orbit
                ship.ensure_in_orbit(self._controller)

                # Navigate via API
                await self._controller.navigate_ship(
                    ship.symbol,
                    step.to_waypoint.symbol
                )

                # Update ship state (domain)
                ship.update_after_navigation(step)

            elif step.is_refuel:
                # State machine: ensure docked
                ship.ensure_docked(self._controller)

                # Refuel via API
                await self._controller.refuel_ship(ship.symbol)

                # Update ship state (domain)
                ship.update_after_refuel()

    def _extract_system(self, waypoint: str) -> str:
        """Extract system symbol from waypoint"""
        parts = waypoint.split("-")
        return f"{parts[0]}-{parts[1]}"
```

### 4. Pipeline Behavior (Middleware)

**`application/behaviors/logging_behavior.py`:**

```python
import logging
from pymediatr import PipelineBehavior

logger = logging.getLogger(__name__)

class LoggingBehavior(PipelineBehavior):
    """
    Log all requests passing through mediator.

    Logs:
    - Request type and data
    - Execution time
    - Success/failure
    - Errors
    """

    async def handle(self, request, next_handler):
        """
        Intercept request, log, and pass to next handler.

        Args:
            request: Command or Query
            next_handler: Next handler in pipeline

        Returns:
            Result from handler
        """
        request_name = type(request).__name__

        logger.info(f"Handling {request_name}")
        logger.debug(f"Request data: {request}")

        try:
            result = await next_handler()
            logger.info(f"Completed {request_name}")
            return result

        except Exception as e:
            logger.error(f"Failed {request_name}: {e}")
            raise
```

**`application/behaviors/validation_behavior.py`:**

```python
from pymediatr import PipelineBehavior

class ValidationBehavior(PipelineBehavior):
    """Validate requests before execution"""

    async def handle(self, request, next_handler):
        """
        Validate request if it has validate() method.

        Args:
            request: Command or Query
            next_handler: Next handler in pipeline

        Returns:
            Result from handler

        Raises:
            ValidationError: If validation fails
        """
        # Check if request has validate method
        if hasattr(request, 'validate'):
            request.validate()

        # Continue to next handler
        return await next_handler()
```

**`application/behaviors/transaction_behavior.py`:**

```python
from pymediatr import PipelineBehavior

class TransactionBehavior(PipelineBehavior):
    """
    Wrap commands in database transaction.

    Rollback on error, commit on success.
    """

    def __init__(self, database):
        self._db = database

    async def handle(self, request, next_handler):
        """
        Execute request within transaction.

        Only applies to commands (writes), not queries (reads).
        """
        # Check if this is a command (has 'Command' in name)
        is_command = 'Command' in type(request).__name__

        if not is_command:
            # Queries don't need transactions
            return await next_handler()

        # Execute in transaction
        with self._db.transaction() as conn:
            try:
                result = await next_handler()
                # Commit happens automatically on context exit
                return result
            except Exception:
                # Rollback happens automatically on exception
                raise
```

---

## Mediator Setup

**`application/mediator.py`:**

```python
from pymediatr import Mediator
from .behaviors.logging_behavior import LoggingBehavior
from .behaviors.validation_behavior import ValidationBehavior
from .behaviors.transaction_behavior import TransactionBehavior

# Command handlers
from .commands.player.register_player import RegisterPlayerCommand, RegisterPlayerHandler
from .commands.navigation.navigate_ship import NavigateShipCommand, NavigateShipHandler

# Query handlers
from .queries.player.get_player import GetPlayerQuery, GetPlayerHandler

def create_mediator(
    player_repo,
    ship_repo,
    graph_provider,
    ship_controller,
    database
) -> Mediator:
    """
    Create and configure mediator with all handlers and behaviors.

    Args:
        Dependencies injected from DI container

    Returns:
        Configured Mediator instance
    """
    mediator = Mediator()

    # Register pipeline behaviors (middleware)
    mediator.add_pipeline_behavior(LoggingBehavior())
    mediator.add_pipeline_behavior(ValidationBehavior())
    mediator.add_pipeline_behavior(TransactionBehavior(database))

    # Register command handlers
    mediator.register_handler(
        RegisterPlayerCommand,
        RegisterPlayerHandler(player_repo)
    )
    mediator.register_handler(
        NavigateShipCommand,
        NavigateShipHandler(ship_repo, graph_provider, ship_controller)
    )

    # Register query handlers
    mediator.register_handler(
        GetPlayerQuery,
        GetPlayerHandler(player_repo)
    )

    return mediator
```

---

## Usage Examples

### CLI Usage

**`adapters/primary/cli/player_cli.py`:**

```python
import asyncio
from ....configuration.container import get_mediator
from ....application.commands.player.register_player import RegisterPlayerCommand

async def register_player_command(args):
    """Handle player register CLI command"""
    mediator = get_mediator()

    command = RegisterPlayerCommand(
        agent_symbol=args.agent,
        token=args.token,
        metadata=json.loads(args.metadata) if args.metadata else None
    )

    try:
        player = await mediator.send(command)
        print(f"✅ Registered player {player.player_id}: {player.agent_symbol}")
        return 0
    except Exception as e:
        print(f"❌ Error: {e}")
        return 1

def main():
    """CLI entry point"""
    # Parse args...
    asyncio.run(register_player_command(args))
```

### Daemon Usage

**`adapters/primary/daemon/container_manager.py`:**

```python
async def _run_navigation_container(self, info: ContainerInfo):
    """Run navigation operation"""
    mediator = get_mediator()

    command = NavigateShipCommand(
        ship_symbol=info.config["ship_symbol"],
        destination=info.config["destination"],
        player_id=info.player_id
    )

    try:
        result = await mediator.send(command)
        info.logs.append(f"✅ Navigation complete: {result.message}")

    except Exception as e:
        info.logs.append(f"❌ Navigation failed: {e}")
        raise
```

### Testing

**`tests/bdd/steps/player/test_register_player_steps.py`:**

```python
from pytest_bdd import scenario, when, then
from pymediatr import Mediator
from spacetraders.application.commands.player.register_player import (
    RegisterPlayerCommand,
    RegisterPlayerHandler
)

@when('I register player "AGENT-1" with token "TOKEN-123"')
async def register_player(context, mock_player_repo):
    """Execute register player command"""
    mediator = Mediator()
    mediator.register_handler(
        RegisterPlayerCommand,
        RegisterPlayerHandler(mock_player_repo)
    )

    command = RegisterPlayerCommand(
        agent_symbol="AGENT-1",
        token="TOKEN-123"
    )

    context["player"] = await mediator.send(command)

@then("the player should have a player_id")
def check_player_id(context):
    assert context["player"].player_id is not None
```

---

## Benefits of pymediatr

1. **Decoupling** - Handlers don't know about each other
2. **Pipeline behaviors** - Middleware for cross-cutting concerns
3. **Type-safe** - Full typing with generics
4. **Async-first** - Native async/await support
5. **Testable** - Easy to mock and test handlers
6. **Scalable** - Add handlers without modifying existing code
7. **Battle-tested** - Proven pattern from .NET

---

## Dependencies

**`pyproject.toml`:**

```toml
[project]
dependencies = [
    "pymediatr>=0.1.1",
    "ortools>=9.7.0",
    "requests>=2.31.0",
    "python-dateutil>=2.8.0",
]
```

---

## Key Principles

1. **Commands** = Write operations (mutate state)
2. **Queries** = Read operations (fetch data)
3. **Handlers** = Thin orchestration (domain + infrastructure)
4. **Behaviors** = Cross-cutting concerns (logging, validation, transactions)
5. **Domain logic** = In domain entities and services (not handlers)
6. **Infrastructure** = Repositories, API clients, controllers

---

## Migration Path

### From Application Services

**Before (Application Service):**
```python
service = NavigationService(repo, controller, graph_provider)
result = service.navigate_to(ship_symbol="SHIP-1", destination="X1-B2")
```

**After (MediatR):**
```python
command = NavigateShipCommand(ship_symbol="SHIP-1", destination="X1-B2", player_id=1)
result = await mediator.send(command)
```

**Benefits:**
- More explicit (command object shows intent)
- Better testability (mock mediator or handler)
- Middleware support (logging, validation, transactions)
- Async-first (natural async patterns)

---

## References

- **pymediatr**: https://github.com/megafon-it/pymediatr
- **MediatR (.NET)**: https://github.com/jbogard/MediatR
- **CQRS Pattern**: https://martinfowler.com/bliki/CQRS.html
