# Navigation Architecture Reference

**Source**: `/spacetradersV2/.worktrees/domain-navigation-refactor/bot`
**Status**: Phases 1-5 Complete (1,954 lines of domain/application/infrastructure code)

This document captures the **actual navigation architecture** from the domain-navigation-refactor worktree to guide bot-v2 implementation.

---

## Key Architectural Decisions

### 1. Route is Immutable Value Object (NOT Aggregate Root)

```python
@dataclass(frozen=True)
class Route:
    """
    Complete navigation route from origin to destination (immutable VO).

    Contains:
    - start: Waypoint (ship's current location)
    - destination: Waypoint (target)
    - steps: List[RouteStep] (navigate + refuel steps)
    - total_fuel_cost: int
    - total_time: int (seconds)
    - final_fuel: int

    Domain Rules:
    - Steps must form connected path
    - Route must start at ship's location
    - Route must end at destination
    - Total fuel cost validated
    """
```

**Why Value Object?**
- Routes are **planned and executed**, not tracked over time
- No identity needed (routes are ephemeral)
- Immutability ensures route can't be corrupted mid-execution
- Easy to test, serialize, and reason about

### 2. RouteStep (not RouteSegment)

```python
@dataclass(frozen=True)
class RouteStep:
    """
    Single step in a route (immutable VO).

    Can be either:
    - Navigate: Move from one waypoint to another
    - Refuel: Refuel at current waypoint

    Fields:
    - action: "navigate" or "refuel"
    - from_waypoint: Optional[Waypoint]
    - to_waypoint: Optional[Waypoint]
    - flight_mode: Optional[FlightMode]
    - fuel_cost: int
    - travel_time: int (seconds)
    - distance: float
    """
```

**Key difference from RouteSegment**:
- Includes **refuel actions** (not just navigation)
- More granular (one action per step)
- Validates action-specific invariants (navigate needs mode, refuel doesn't)

### 3. SystemGraph is Domain Object

```python
@dataclass(frozen=True)
class SystemGraph:
    """
    Navigation graph for a star system (immutable VO).

    Contains:
    - system: str
    - waypoints: Dict[str, WaypointNode]
    - edges: List[GraphEdge]

    Domain Rules:
    - All waypoints belong to same system
    - Edges reference existing waypoints
    - Graph is bidirectional
    """

@dataclass(frozen=True)
class WaypointNode:
    """
    Node in navigation graph (immutable VO).

    Contains:
    - symbol: str
    - waypoint_type: str
    - x, y: int coordinates
    - has_marketplace: bool
    - has_fuel: bool
    - traits: List[str]
    """

@dataclass(frozen=True)
class GraphEdge:
    """
    Edge connecting two waypoints (immutable VO).

    Contains:
    - from_waypoint: str
    - to_waypoint: str
    - distance: float
    - edge_type: "normal" or "orbital"
    """
```

**Why Domain Object?**
- **Graph is a domain concept** (not infrastructure detail)
- Business rules about navigation depend on graph structure
- Domain services (ORToolsPlanner) need graph to plan routes
- Type-safe domain objects (not dicts)

### 4. ORToolsPlanner is Domain Service

```python
class ORToolsPlanner:
    """
    Domain service for route planning using OR-Tools.

    Business logic: Given ship's fuel/position and destination,
    calculate optimal route considering fuel constraints.

    Algorithm:
    1. Build state space: (waypoint, fuel_level) nodes
    2. Add navigation edges: consume fuel, time cost
    3. Add refuel edges: restore fuel at stations
    4. Solve min-cost flow
    5. Reconstruct Route from solution

    Optimizations:
    - Fuel granularity: 10-fuel increments (10x state reduction)
    - Path-first routing: Dijkstra approximate path
    - Prefer CRUISE over DRIFT (penalty on DRIFT)
    """

    def find_optimal_route(
        self,
        start: Waypoint,
        destination: Waypoint,
        current_fuel: int,
        fuel_capacity: int,
        system_graph: SystemGraph,
        engine_speed: int = 30,
    ) -> Optional[Route]:
        """Returns domain Route object"""
```

**Why Domain Service?**
- **Route planning is pure business logic**
- No infrastructure dependencies (uses only domain objects)
- OR-Tools treated as algorithm library (like `math` module)
- Testable without API or database

**Key Insight**: "Domain service because route planning is pure business logic... OR-Tools is treated as a domain dependency (algorithm library, like math)"

### 5. Ship.plan_route() - Rich Domain Entity

```python
class Ship:
    """Ship entity with navigation capabilities"""

    def plan_route(
        self,
        destination: Waypoint,
        system_graph: SystemGraph,
        planner: Optional[ORToolsPlanner] = None,
    ) -> Optional[Route]:
        """
        Plan optimal route to destination.

        This is the PRIMARY navigation method that replaces SmartNavigator.

        Delegates to ORToolsPlanner domain service.
        """
        # Validate state
        if self._nav.is_in_transit():
            raise InvalidShipStateError("Cannot plan route while in transit")

        # Use injected planner or create default
        if planner is None:
            from .route_planner import ORToolsPlanner
            planner = ORToolsPlanner()

        # Delegate to domain service
        return planner.find_optimal_route(
            start=self.current_location,
            destination=destination,
            current_fuel=self.fuel.current,
            fuel_capacity=self.fuel.capacity,
            system_graph=system_graph,
            engine_speed=self.engine.speed,
        )

    def validate_route_feasibility(self, route: Route) -> bool:
        """Validate if ship can execute route"""
        return (
            route.total_fuel_cost <= self.fuel.capacity
            and route.start == self.current_location
            and self._nav.can_navigate()
        )

    def ensure_in_orbit(self, controller):
        """State machine: transition to IN_ORBIT"""
        if self._nav.is_docked():
            controller.orbit_ship(self.symbol)
        elif self._nav.is_in_transit():
            raise InvalidShipStateError("Cannot orbit while in transit")

    def ensure_docked(self, controller):
        """State machine: transition to DOCKED"""
        if self._nav.is_in_orbit():
            controller.dock_ship(self.symbol)
        elif self._nav.is_in_transit():
            raise InvalidShipStateError("Cannot dock while in transit")
```

**Why Rich Entity?**
- Ship entity **no longer dead code** (was just data holder)
- Domain logic belongs to domain objects
- Ship orchestrates its own navigation
- State machine logic in domain (not infrastructure)

### 6. NavigationService - Thin Orchestration

```python
class NavigationService:
    """
    Application service for navigation orchestration.

    This is a THIN layer that:
    - Loads Ship from repository
    - Delegates planning to domain (Ship.plan_route())
    - Delegates execution to infrastructure (ShipController)
    - Persists updated ship state

    Business logic lives in domain objects.
    """

    def __init__(
        self,
        ship_repository: ShipRepository,
        ship_controller: ShipController,
        graph_provider,  # SystemGraphProvider
    ):
        self._ship_repo = ship_repository
        self._ship_controller = ship_controller
        self._graph_provider = graph_provider

    def navigate_to(
        self,
        ship_symbol: str,
        destination_symbol: str
    ) -> NavigationResult:
        """
        Navigate ship to destination.

        Pattern:
        1. Load ship from repository
        2. Load system graph from provider
        3. Plan route (domain: Ship.plan_route())
        4. Validate feasibility (domain: Ship.validate_route_feasibility())
        5. Execute route (infrastructure: ShipController)
        6. Update ship state
        7. Persist ship
        """
        # 1. Load ship
        ship = self._ship_repo.get_ship(ship_symbol)
        if ship is None:
            raise ShipNotFoundError(f"Ship {ship_symbol} not found")

        # 2. Already there?
        if ship.current_location.symbol == destination_symbol:
            return NavigationResult.already_there(ship, destination_symbol)

        # 3. Load graph
        destination = Waypoint(symbol=destination_symbol, x=0, y=0)
        system = destination.system_symbol
        graph = self._graph_provider.get_graph(system)

        # 4. Plan route (DOMAIN)
        route = ship.plan_route(destination, graph.as_domain_graph())
        if route is None:
            raise NoRouteFoundError(f"No route found to {destination_symbol}")

        # 5. Validate (DOMAIN)
        if not ship.validate_route_feasibility(route):
            raise InvalidRouteError("Route not feasible for ship")

        # 6. Execute route (INFRASTRUCTURE)
        self._execute_route_steps(ship, route)

        # 7. Persist
        self._ship_repo.update_ship(ship)

        return NavigationResult.completed(ship, route)

    def _execute_route_steps(self, ship: Ship, route: Route):
        """Execute route steps via infrastructure"""
        for step in route.steps:
            if step.is_navigate:
                # State machine: ensure in orbit
                ship.ensure_in_orbit(self._ship_controller)

                # Navigate via API
                self._ship_controller.navigate_ship(
                    ship.symbol,
                    step.to_waypoint.symbol
                )

                # Update ship state
                ship.update_after_navigation(step)

            elif step.is_refuel:
                # State machine: ensure docked
                ship.ensure_docked(self._ship_controller)

                # Refuel via API
                self._ship_controller.refuel_ship(ship.symbol)

                # Update ship state
                ship.update_after_refuel()
```

**Why Thin Service?**
- Application layer orchestrates, doesn't contain logic
- Domain objects make decisions (Ship.plan_route, Ship.validate_route_feasibility)
- Service just coordinates: load → domain → infrastructure → persist
- Testable with mocked repositories and controller

### 7. SystemGraphProvider - Infrastructure Caching

```python
class SystemGraphProvider:
    """
    Infrastructure adapter for system graph management.

    Three-tier caching:
    1. Memory cache (instant, _graph_cache dict)
    2. Database cache (~10ms, 7-day TTL)
    3. API fetch (~2-5 seconds, full pagination)

    Returns GraphLoadResult with source tracking.
    """

    def __init__(self, api_client, database):
        self._api = api_client
        self._db = database
        self._graph_cache: Dict[str, SystemGraph] = {}  # Memory cache

    def get_graph(self, system_symbol: str) -> GraphLoadResult:
        """
        Get system graph with three-tier caching.

        Priority:
        1. Check memory cache (instant)
        2. Check database cache (~10ms)
        3. Build from API (~2-5 seconds)
        4. Cache at all levels
        """
        # Hot cache (memory)
        if system_symbol in self._graph_cache:
            return GraphLoadResult(
                graph=self._graph_cache[system_symbol],
                source="memory_cache",
                message="Loaded from memory cache"
            )

        # Warm cache (database, 7-day TTL)
        db_graph = self._load_from_database(system_symbol)
        if db_graph and not self._is_stale(db_graph):
            self._graph_cache[system_symbol] = db_graph
            return GraphLoadResult(
                graph=db_graph,
                source="database_cache",
                message="Loaded from database (warm cache)"
            )

        # Cold cache (API)
        api_graph = self._build_from_api(system_symbol)
        self._save_to_database(system_symbol, api_graph)
        self._graph_cache[system_symbol] = api_graph
        return GraphLoadResult(
            graph=api_graph,
            source="api",
            message="Built from API (cold cache)"
        )

    def _build_from_api(self, system_symbol: str) -> SystemGraph:
        """
        Build graph from SpaceTraders API.

        Process:
        1. Paginate through all waypoints
        2. Build O(n²) edges between all waypoints
        3. Mark orbital edges (0 distance)
        4. Create domain SystemGraph object
        """
        waypoints = self._fetch_all_waypoints(system_symbol)
        nodes = {}
        edges = []

        for wp in waypoints:
            node = WaypointNode(
                symbol=wp["symbol"],
                waypoint_type=wp["type"],
                x=wp["x"],
                y=wp["y"],
                has_marketplace="MARKETPLACE" in [t["symbol"] for t in wp.get("traits", [])],
                has_fuel="MARKETPLACE" in [t["symbol"] for t in wp.get("traits", [])] or wp.get("type") == "FUEL_STATION",
                traits=[t["symbol"] for t in wp.get("traits", [])]
            )
            nodes[wp["symbol"]] = node

        # Build edges (O(n²))
        for from_wp in nodes.values():
            for to_wp in nodes.values():
                if from_wp.symbol == to_wp.symbol:
                    continue

                distance = from_wp.distance_to(to_wp)
                is_orbital = (to_wp.symbol in from_wp.orbitals or
                             from_wp.symbol in to_wp.orbitals)

                edge = GraphEdge(
                    from_waypoint=from_wp.symbol,
                    to_waypoint=to_wp.symbol,
                    distance=0.0 if is_orbital else distance,
                    edge_type="orbital" if is_orbital else "normal"
                )
                edges.append(edge)

        return SystemGraph(
            system=system_symbol,
            waypoints=nodes,
            edges=edges
        )
```

**Why Infrastructure?**
- API calls (external system)
- Database persistence (infrastructure concern)
- Caching strategy (performance optimization)
- Domain just needs graph, doesn't care about source

---

## Architecture Diagram

```
┌─────────────────────────────────────────────────┐
│              CLI / Daemon (Primary)              │
│    spacetraders navigate --ship SHIP-1 --to X1  │
└───────────────────┬─────────────────────────────┘
                    │
                    ▼
        ┌───────────────────────────┐
        │   NavigationService        │  ◄─── Application Layer
        │   (Thin Orchestration)     │       (Use Case)
        └──────────┬────────────────┘
                   │
          ┌────────┴────────┐
          │                 │
          ▼                 ▼
  ┌──────────────┐   ┌─────────────────┐
  │ ShipRepository│   │ SystemGraphProvider│  ◄─── Infrastructure
  │ (Load Ship)   │   │ (3-tier caching)   │       (Adapters)
  └──────┬────────┘   └────────┬───────────┘
         │                     │
         │                     │ provides
         │                     │
         ▼                     ▼
  ┌──────────────────────────────────────┐
  │            Ship Entity                │  ◄─── Domain Layer
  │  plan_route(dest, graph, planner)    │       (Business Logic)
  │  validate_route_feasibility(route)   │
  │  ensure_in_orbit()                   │
  │  ensure_docked()                     │
  └──────────┬───────────────────────────┘
             │ uses
             │
             ▼
  ┌──────────────────────────┐
  │   ORToolsPlanner          │  ◄─── Domain Service
  │   (Pure Business Logic)   │       (Stateless)
  │                           │
  │ find_optimal_route(...)   │
  │  → builds state space     │
  │  → solves min-cost flow   │
  │  → returns Route VO       │
  └──────────┬────────────────┘
             │ uses
             │
             ▼
  ┌──────────────────────────┐
  │     SystemGraph          │  ◄─── Domain Value Objects
  │  (Navigation Network)     │       (Immutable)
  │                           │
  │  waypoints: Dict[WaypointNode]│
  │  edges: List[GraphEdge]   │
  └───────────────────────────┘
             │
             │ returns
             ▼
  ┌──────────────────────────┐
  │        Route             │  ◄─── Domain Value Object
  │  (Immutable Path)         │       (Immutable)
  │                           │
  │  start: Waypoint          │
  │  destination: Waypoint    │
  │  steps: List[RouteStep]   │
  │  total_fuel_cost: int     │
  │  total_time: int          │
  └───────────────────────────┘
             │
             │ executed by
             ▼
  ┌──────────────────────────┐
  │    ShipController        │  ◄─── Infrastructure
  │  (API Execution)          │       (External Calls)
  │                           │
  │  navigate_ship()          │
  │  dock_ship()              │
  │  orbit_ship()             │
  │  refuel_ship()            │
  └───────────────────────────┘
```

---

## Key Takeaways for bot-v2

### 1. Domain-First Design

- **Route** and **RouteStep** are immutable value objects
- **SystemGraph**, **WaypointNode**, **GraphEdge** are domain objects
- **ORToolsPlanner** is domain service (uses only domain objects)
- **Ship** is rich entity with navigation methods

### 2. OR-Tools is Domain Dependency

> "OR-Tools is treated as a domain dependency (algorithm library, like math)"

- Not infrastructure adapter
- Domain service uses OR-Tools directly
- No ports/interfaces for OR-Tools
- Algorithm library, not external system

### 3. Ship Orchestrates Planning

- **Ship.plan_route()** is primary method
- Ship delegates to ORToolsPlanner
- Dependency injection (planner optional parameter)
- Ship validates feasibility

### 4. Application Layer is Thin

- NavigationService orchestrates
- Loads ship, loads graph
- Calls Ship.plan_route() (domain)
- Executes via ShipController (infrastructure)
- Persists ship state

### 5. SystemGraphProvider is Infrastructure

- Three-tier caching (memory → DB → API)
- API pagination (~2-5 seconds)
- O(n²) edge generation
- 7-day cache TTL

### 6. Immutability Throughout

- All domain VOs are `@dataclass(frozen=True)`
- Routes can't be corrupted
- Easy to test and serialize
- No unexpected mutations

### 7. Clean Separation

- **Domain**: Route, SystemGraph, ORToolsPlanner, Ship.plan_route()
- **Application**: NavigationService (orchestration)
- **Infrastructure**: SystemGraphProvider, ShipController, ShipRepository

---

## Implementation Order for bot-v2

### Phase 1: Domain Value Objects
1. `Waypoint` (shared kernel)
2. `Fuel`, `FlightMode` (shared kernel)
3. `WaypointNode`, `GraphEdge`, `SystemGraph`
4. `RouteStep`
5. `Route`

### Phase 2: Domain Services
6. `RoutingConfig`
7. `ORToolsPlanner` (copy ~380 lines from worktree)

### Phase 3: Ship Entity
8. Add `Ship.plan_route()`
9. Add `Ship.validate_route_feasibility()`
10. Add `Ship.ensure_in_orbit()`, `Ship.ensure_docked()`

### Phase 4: Application Service
11. `NavigationService` (thin orchestration)
12. `NavigationResult` value object

### Phase 5: Infrastructure
13. `SystemGraphProvider` (three-tier caching)
14. `ShipController` (API execution)
15. `ShipRepository` (load/save ships)

### Phase 6: Testing
16. BDD tests for Route/RouteStep
17. BDD tests for SystemGraph
18. BDD tests for ORToolsPlanner
19. BDD tests for Ship.plan_route()
20. BDD tests for NavigationService

---

## Files to Copy from Worktree

**Domain Layer** (~970 lines):
- `domain/shared/route.py` (230 lines)
- `domain/shared/system_graph.py` (310 lines)
- `domain/shared/route_planner.py` (380 lines)
- `domain/shared/ship.py` (+170 lines navigation methods)

**Application Layer** (~370 lines):
- `application/services/navigation_service.py` (370 lines)

**Infrastructure Layer** (~480 lines):
- `infrastructure/navigation/system_graph_provider.py` (480 lines)

**Total**: ~1,820 lines of proven, tested code

---

## Testing Strategy

### Domain Tests (No Mocks)

```gherkin
Feature: Route Planning

  Scenario: Plan route with sufficient fuel
    Given a ship with 500 fuel at "X1-A1"
    And destination "X1-B2" is 200 units away
    When I plan a route to "X1-B2"
    Then the route should have 1 navigate step
    And the route should require 200 fuel
    And the route should use CRUISE mode

  Scenario: Plan route requiring refuel
    Given a ship with 100 fuel at "X1-A1"
    And destination "X1-Z9" is 500 units away
    And waypoint "X1-M5" has fuel and is 80 units from start
    When I plan a route to "X1-Z9"
    Then the route should have 3 steps
    And step 1 should be navigate to "X1-M5"
    And step 2 should be refuel at "X1-M5"
    And step 3 should be navigate to "X1-Z9"
```

### Application Tests (Mocked Infrastructure)

```gherkin
Feature: Navigation Service

  Scenario: Navigate ship successfully
    Given a mocked ship repository with ship "SHIP-1"
    And a mocked graph provider with system "X1-ABC"
    When I navigate ship "SHIP-1" to "X1-ABC-Z9"
    Then Ship.plan_route should be called
    Then ShipController.navigate_ship should be called
    Then the ship should be persisted
```

---

## References

- **Source Worktree**: `/spacetradersV2/.worktrees/domain-navigation-refactor/bot`
- **Status Document**: `NAVIGATION_REFACTORING_STATUS.md`
- **Total New Code**: 1,954 lines (phases 1-5 complete)
- **Pending**: 31 files to migrate, legacy deletion, comprehensive tests
