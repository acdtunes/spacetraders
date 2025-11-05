"""
Pytest fixtures for navigation BDD tests.

Provides mock repositories, services, and shared context for testing
navigation domain logic without external dependencies.

Also provides real database fixtures for integration tests.
"""
import pytest
import asyncio
from typing import Dict, List, Optional
from dataclasses import dataclass
from pytest_bdd import given, parsers
from datetime import datetime

from spacetraders.domain.shared.value_objects import Waypoint, Fuel, FlightMode
from spacetraders.domain.navigation.route import Route, RouteSegment, RouteStatus
from spacetraders.configuration.container import (
    get_database,
    get_player_repository,
    get_ship_repository,
    reset_container
)
from spacetraders.domain.shared.player import Player
from spacetraders.domain.shared.ship import Ship


# Mock Repositories

class MockShipRepository:
    """In-memory ship repository for testing"""

    def __init__(self):
        self.ships: Dict[str, dict] = {}

    def add(self, ship_symbol: str, fuel: Fuel, location: Waypoint, engine_speed: int = 30):
        """Add a ship to the repository"""
        self.ships[ship_symbol] = {
            'symbol': ship_symbol,
            'fuel': fuel,
            'location': location,
            'engine_speed': engine_speed,
            'player_id': 1
        }

    def get(self, ship_symbol: str) -> Optional[dict]:
        """Get a ship by symbol"""
        return self.ships.get(ship_symbol)

    def update_fuel(self, ship_symbol: str, fuel: Fuel):
        """Update ship fuel"""
        if ship_symbol in self.ships:
            self.ships[ship_symbol]['fuel'] = fuel

    def update_location(self, ship_symbol: str, location: Waypoint):
        """Update ship location"""
        if ship_symbol in self.ships:
            self.ships[ship_symbol]['location'] = location


class MockRouteRepository:
    """In-memory route repository for testing"""

    def __init__(self):
        self.routes: Dict[str, Route] = {}
        self._next_id = 1

    def save(self, route: Route) -> str:
        """Save a route"""
        self.routes[route.route_id] = route
        return route.route_id

    def get(self, route_id: str) -> Optional[Route]:
        """Get a route by ID"""
        return self.routes.get(route_id)

    def generate_id(self) -> str:
        """Generate a new route ID"""
        route_id = f"route-{self._next_id}"
        self._next_id += 1
        return route_id


class MockGraphProvider:
    """Provides test navigation graphs"""

    def __init__(self):
        self.waypoints: Dict[str, Waypoint] = {}
        self._create_default_waypoints()

    def _create_default_waypoints(self):
        """Create a default set of test waypoints"""
        # System X1 waypoints
        self.waypoints["X1-A1"] = Waypoint("X1-A1", 0, 0, "X1", "PLANET", has_fuel=True)
        self.waypoints["X1-B2"] = Waypoint("X1-B2", 200, 0, "X1", "PLANET", has_fuel=True)
        self.waypoints["X1-C3"] = Waypoint("X1-C3", 400, 0, "X1", "PLANET", has_fuel=True)
        self.waypoints["X1-M5"] = Waypoint("X1-M5", 80, 0, "X1", "PLANET", has_fuel=True)
        self.waypoints["X1-Z9"] = Waypoint("X1-Z9", 500, 0, "X1", "PLANET", has_fuel=True)

        # Orbital waypoints
        self.waypoints["X1-A1-ORBITAL"] = Waypoint(
            "X1-A1-ORBITAL", 0, 0, "X1", "ORBITAL_STATION",
            has_fuel=False, orbitals=("X1-A1",)
        )

    def get_waypoint(self, symbol: str) -> Optional[Waypoint]:
        """Get waypoint by symbol"""
        return self.waypoints.get(symbol)

    def add_waypoint(self, waypoint: Waypoint):
        """Add a waypoint to the graph"""
        self.waypoints[waypoint.symbol] = waypoint

    def get_distance(self, from_symbol: str, to_symbol: str) -> float:
        """Get distance between two waypoints"""
        from_wp = self.waypoints.get(from_symbol)
        to_wp = self.waypoints.get(to_symbol)
        if from_wp and to_wp:
            # Special case: orbital hop costs 0 distance but 1 fuel
            if from_wp.is_orbital_of(to_wp):
                return 0.0
            return from_wp.distance_to(to_wp)
        return 0.0


class MockRoutingEngine:
    """Simple pathfinding for tests - no OR-Tools needed"""

    def __init__(self, graph_provider: MockGraphProvider):
        self.graph = graph_provider

    def find_path(self, from_symbol: str, to_symbol: str, via: Optional[List[str]] = None) -> List[str]:
        """Find path between waypoints"""
        if via:
            return [from_symbol] + via + [to_symbol]
        return [from_symbol, to_symbol]

    def find_nearest_refuel(self, from_symbol: str) -> Optional[str]:
        """Find nearest refuel point"""
        from_wp = self.graph.get_waypoint(from_symbol)
        if not from_wp:
            return None

        # Find closest waypoint with fuel
        closest = None
        min_dist = float('inf')
        for symbol, wp in self.graph.waypoints.items():
            if wp.has_fuel and symbol != from_symbol:
                dist = from_wp.distance_to(wp)
                if dist < min_dist:
                    min_dist = dist
                    closest = symbol
        return closest


class MockAPIClient:
    """Mock API client that records calls"""

    def __init__(self):
        self.calls: List[dict] = []
        self.refuel_called = False

    def navigate(self, ship_symbol: str, waypoint_symbol: str, mode: str):
        """Record navigation call"""
        self.calls.append({
            'action': 'navigate',
            'ship': ship_symbol,
            'waypoint': waypoint_symbol,
            'mode': mode
        })
        return {'success': True}

    def refuel(self, ship_symbol: str):
        """Record refuel call"""
        self.refuel_called = True
        self.calls.append({
            'action': 'refuel',
            'ship': ship_symbol
        })
        return {'success': True, 'fuel_added': 100}

    def get_calls(self, action: Optional[str] = None) -> List[dict]:
        """Get recorded calls, optionally filtered by action"""
        if action:
            return [c for c in self.calls if c['action'] == action]
        return self.calls


# Pytest Fixtures

@pytest.fixture
def context():
    """Shared test context dictionary"""
    return {}


@pytest.fixture
def mock_ship_repository():
    """Mock ship repository"""
    return MockShipRepository()


@pytest.fixture
def mock_route_repository():
    """Mock route repository"""
    return MockRouteRepository()


@pytest.fixture
def mock_graph_provider():
    """Mock graph provider with test waypoints"""
    return MockGraphProvider()


@pytest.fixture
def mock_routing_engine(mock_graph_provider):
    """Mock routing engine"""
    return MockRoutingEngine(mock_graph_provider)


@pytest.fixture
def mock_api_client():
    """Mock API client"""
    return MockAPIClient()


# Background step implementations

@given("the navigation system is initialized")
def navigation_system_initialized(context):
    """Initialize navigation system context"""
    context['initialized'] = True


@given("the fuel management system is initialized")
def fuel_system_initialized(context):
    """Initialize fuel management system context"""
    context['initialized'] = True


@given("the flight mode system is initialized")
def flight_mode_system_initialized(context):
    """Initialize flight mode system context"""
    context['initialized'] = True
    context['default_engine_speed'] = 30


@given("a ship with engine speed 30")
def ship_with_engine_speed(context):
    """Set default engine speed"""
    context['engine_speed'] = 30


# ============================================================================
# REAL DATABASE FIXTURES (for integration tests)
# ============================================================================

# Note: Database cleanup is now handled in root conftest.py
# This ensures all tests (not just navigation tests) get clean database state


@pytest.fixture
def event_loop():
    """Create event loop for async tests"""
    loop = asyncio.get_event_loop_policy().new_event_loop()
    yield loop
    loop.close()


# ============================================================================
# SHARED STEP DEFINITIONS (for integration tests)
# ============================================================================

@given(parsers.parse('a player exists with ID {player_id:d} and token "{token}"'))
def create_test_player(context, player_id, token):
    """Create a test player in the database"""
    player_repo = get_player_repository()

    # Create player
    player = Player(
        player_id=None,  # Will be assigned by repository
        agent_symbol=f"TEST-AGENT-{player_id}",
        token=token,
        created_at=datetime.now(),
        last_active=datetime.now(),
        metadata={}
    )

    created_player = player_repo.create(player)
    context['player_id'] = created_player.player_id
    context['player'] = created_player

    # If specific ID requested, update it
    if player_id != created_player.player_id:
        db = get_database()
        with db.transaction() as conn:
            cursor = conn.cursor()
            cursor.execute("""
                UPDATE players SET player_id = ? WHERE player_id = ?
            """, (player_id, created_player.player_id))
        context['player_id'] = player_id


@given(parsers.parse('a ship "{ship_symbol}" exists for player {player_id:d} at "{location}"'))
@given(parsers.parse('a ship "{ship_symbol}" exists for player {player_id:d}'))
def create_test_ship(context, ship_symbol, player_id, location="X1-TEST-A1"):
    """Create a test ship in the database"""
    ship_repo = get_ship_repository()

    # Create waypoint for location
    waypoint = Waypoint(
        symbol=location,
        waypoint_type="PLANET",
        x=0,
        y=0,
        system_symbol="X1-TEST",
        traits=(),
        has_fuel=True
    )

    # Create ship
    ship = Ship(
        ship_symbol=ship_symbol,
        player_id=player_id,
        current_location=waypoint,
        fuel=Fuel(current=400, capacity=400),
        fuel_capacity=400,
        cargo_capacity=40,
        cargo_units=0,
        engine_speed=30,
        nav_status=Ship.DOCKED
    )

    # Save to database
    ship_repo.create(ship)

    context['ship_symbol'] = ship_symbol
    context['ship'] = ship
    context['player_id'] = player_id


@given('the SpaceTraders API is available')
def api_available(context):
    """Mark API as available for tests"""
    context['api_available'] = True
