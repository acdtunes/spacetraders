"""
BDD step definitions for plan route query feature.

Tests the PlanRouteHandler query across all 16 scenarios using
black-box testing approach - only verifying observable behavior.
"""
import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from typing import Optional, Dict, Any
from unittest.mock import Mock

from application.navigation.queries.plan_route import (
    PlanRouteQuery,
    PlanRouteHandler
)
from domain.navigation.route import Route
from domain.shared.exceptions import ShipNotFoundError
from domain.shared.value_objects import Waypoint, Fuel, FlightMode
from domain.shared.ship import Ship

# Load all scenarios from the feature file
scenarios('../../features/application/plan_route_query.feature')


# ============================================================================
# Fixtures - Mock Infrastructure
# ============================================================================

@pytest.fixture
def context():
    """Shared test context dictionary"""
    return {}


class MockSystemGraphProvider:
    """Mock system graph provider for testing"""

    def __init__(self):
        self.graphs = {}
        self.query_calls = []

    def get_graph(self, system_symbol: str, force_refresh: bool = False):
        self.query_calls.append((system_symbol, force_refresh))
        graph = self.graphs.get(system_symbol, {"waypoints": {}})
        return type('GraphResult', (), {'graph': graph})()


class MockRoutingEngine:
    """Mock routing engine for testing"""

    def __init__(self):
        self.route_plan = None
        self.call_params = None

    def find_optimal_path(self, graph, start, goal, current_fuel, fuel_capacity, engine_speed, prefer_cruise=True):
        self.call_params = {
            'graph': graph,
            'start': start,
            'goal': goal,
            'current_fuel': current_fuel,
            'fuel_capacity': fuel_capacity,
            'engine_speed': engine_speed,
            'prefer_cruise': prefer_cruise
        }
        return self.route_plan


@pytest.fixture
def mock_graph_provider():
    """Mock graph provider"""
    return MockSystemGraphProvider()


@pytest.fixture
def mock_routing_engine():
    """Mock routing engine"""
    return MockRoutingEngine()


@pytest.fixture
def handler(mock_ship_repo, mock_routing_engine):
    """Create PlanRouteHandler with all dependencies"""
    return PlanRouteHandler(
        mock_ship_repo,
        mock_routing_engine
    )


# ============================================================================
# Helper Functions
# ============================================================================

def create_waypoint(
    symbol: str,
    x: float = 0.0,
    y: float = 0.0,
    system_symbol: str = "X1",
    has_fuel: bool = False
) -> Waypoint:
    """Helper to create test waypoint"""
    return Waypoint(
        symbol=symbol,
        x=x,
        y=y,
        system_symbol=system_symbol,
        waypoint_type="PLANET",
        has_fuel=has_fuel
    )


def create_ship(
    ship_symbol: str,
    player_id: int,
    waypoint_symbol: str,
    system_symbol: str,
    fuel_current: int,
    fuel_capacity: int,
    engine_speed: int
) -> Ship:
    """Helper to create test ship"""
    waypoint = create_waypoint(waypoint_symbol, 0.0, 0.0, system_symbol)
    fuel = Fuel(current=fuel_current, capacity=fuel_capacity)

    return Ship(
        ship_symbol=ship_symbol,
        player_id=player_id,
        current_location=waypoint,
        fuel=fuel,
        fuel_capacity=fuel_capacity,
        cargo_capacity=40,
        cargo_units=0,
        engine_speed=engine_speed,
        nav_status=Ship.IN_ORBIT
    )


# ============================================================================
# Background Steps
# ============================================================================

@given("the plan route query handler is initialized")
def handler_initialized(context):
    """Initialize handler context"""
    context['initialized'] = True


# ============================================================================
# Given Steps - Setup
# ============================================================================

@given(parsers.parse('a ship "{ship_symbol}" exists at waypoint "{waypoint}" in system "{system}"'))
def ship_exists(context, mock_ship_repo, ship_symbol, waypoint, system):
    """Create a ship at a waypoint"""
    # Store for later use
    context['ship_symbol'] = ship_symbol
    context['waypoint'] = waypoint
    context['system'] = system
    context['fuel_current'] = 100
    context['fuel_capacity'] = 200
    context['engine_speed'] = 30


@given(parsers.parse('the ship has {fuel:d} fuel with capacity {capacity:d} and engine speed {speed:d}'))
def ship_has_fuel_and_speed(context, mock_ship_repo, fuel, capacity, speed):
    """Set ship fuel and engine specs"""
    ship = create_ship(
        ship_symbol=context['ship_symbol'],
        player_id=1,
        waypoint_symbol=context['waypoint'],
        system_symbol=context['system'],
        fuel_current=fuel,
        fuel_capacity=capacity,
        engine_speed=speed
    )
    mock_ship_repo.create(ship)
    context['fuel_current'] = fuel
    context['fuel_capacity'] = capacity
    context['engine_speed'] = speed


@given(parsers.parse('waypoint "{waypoint}" exists in system "{system}" at position ({x:f}, {y:f})'))
def waypoint_exists(context, mock_graph_provider, waypoint, system, x, y):
    """Create a waypoint in the graph"""
    if system not in mock_graph_provider.graphs:
        mock_graph_provider.graphs[system] = {"waypoints": {}}

    mock_graph_provider.graphs[system]["waypoints"][waypoint] = {
        "symbol": waypoint,
        "x": x,
        "y": y,
        "system_symbol": system,
        "type": "PLANET",
        "traits": [],
        "has_fuel": False,
        "orbitals": []
    }


@given(parsers.parse('waypoint "{waypoint}" exists in system "{system}" at position ({x:f}, {y:f}) with fuel available'))
def waypoint_exists_with_fuel(context, mock_graph_provider, waypoint, system, x, y):
    """Create a waypoint with fuel in the graph"""
    if system not in mock_graph_provider.graphs:
        mock_graph_provider.graphs[system] = {"waypoints": {}}

    mock_graph_provider.graphs[system]["waypoints"][waypoint] = {
        "symbol": waypoint,
        "x": x,
        "y": y,
        "system_symbol": system,
        "type": "PLANET",
        "traits": [],
        "has_fuel": True,
        "orbitals": []
    }


@given(parsers.parse('the routing engine returns a valid path from "{start}" to "{dest}"'))
def routing_engine_valid_path(context, mock_routing_engine, start, dest):
    """Setup routing engine to return a valid path"""
    mock_routing_engine.route_plan = {
        "steps": [
            {
                "action": "TRAVEL",
                "waypoint": start,
                "fuel_cost": 50,
                "time": 100,
                "mode": FlightMode.CRUISE,
                "distance": 141.42
            },
            {
                "action": "TRAVEL",
                "waypoint": dest,
                "fuel_cost": 0,
                "time": 0,
                "mode": FlightMode.CRUISE,
                "distance": 0
            }
        ],
        "total_fuel_cost": 50,
        "total_time": 100,
        "total_distance": 141.42
    }


@given(parsers.parse('no ship "{ship_symbol}" exists for player {player_id:d}'))
def no_ship_exists(mock_ship_repo, ship_symbol, player_id):
    """Ensure ship doesn't exist"""
    # Repository is empty by default
    pass


@given(parsers.parse('the routing engine returns no path from "{start}" to "{dest}"'))
def routing_engine_no_path(mock_routing_engine, start, dest):
    """Setup routing engine to return no path"""
    mock_routing_engine.route_plan = None


@given(parsers.parse('the routing engine returns a path with distance {distance:f} and fuel cost {fuel:d} and time {time:d}'))
def routing_engine_path_with_details(context, mock_routing_engine, distance, fuel, time):
    """Setup routing engine with specific path details"""
    start = context.get('waypoint', 'X1-START')
    dest = context.get('destination', 'X1-DEST')

    mock_routing_engine.route_plan = {
        "steps": [
            {
                "action": "TRAVEL",
                "waypoint": start,
                "fuel_cost": fuel,
                "time": time,
                "mode": FlightMode.CRUISE,
                "distance": distance
            },
            {
                "action": "TRAVEL",
                "waypoint": dest,
                "fuel_cost": 0,
                "time": 0,
                "mode": FlightMode.CRUISE,
                "distance": 0
            }
        ],
        "total_fuel_cost": fuel,
        "total_time": time,
        "total_distance": distance
    }


@given(parsers.parse('the routing engine returns a path via "{via}" with refuel action'))
def routing_engine_path_with_refuel(context, mock_routing_engine, via):
    """Setup routing engine with refuel stop"""
    start = context.get('waypoint', 'X1-START')
    dest = context.get('destination', 'X1-DEST')

    mock_routing_engine.route_plan = {
        "steps": [
            {
                "action": "TRAVEL",
                "waypoint": start,
                "fuel_cost": 30,
                "time": 50,
                "mode": FlightMode.CRUISE,
                "distance": 70.71
            },
            {
                "action": "REFUEL",
                "waypoint": via,
                "fuel_cost": 0,
                "time": 0
            },
            {
                "action": "TRAVEL",
                "waypoint": via,
                "fuel_cost": 30,
                "time": 50,
                "mode": FlightMode.CRUISE,
                "distance": 70.71
            },
            {
                "action": "TRAVEL",
                "waypoint": dest,
                "fuel_cost": 0,
                "time": 0,
                "mode": FlightMode.CRUISE,
                "distance": 0
            }
        ],
        "total_fuel_cost": 60,
        "total_time": 100,
        "total_distance": 141.42
    }


@given(parsers.parse('the routing engine returns a path via "{via}" without refuel'))
def routing_engine_path_without_refuel(context, mock_routing_engine, via):
    """Setup routing engine with multiple waypoints but no refuel"""
    start = context.get('waypoint', 'X1-START')
    dest = context.get('destination', 'X1-DEST')

    mock_routing_engine.route_plan = {
        "steps": [
            {
                "action": "TRAVEL",
                "waypoint": start,
                "fuel_cost": 30,
                "time": 50,
                "mode": FlightMode.CRUISE,
                "distance": 70.71
            },
            {
                "action": "TRAVEL",
                "waypoint": via,
                "fuel_cost": 30,
                "time": 50,
                "mode": FlightMode.CRUISE,
                "distance": 70.71
            },
            {
                "action": "TRAVEL",
                "waypoint": dest,
                "fuel_cost": 0,
                "time": 0,
                "mode": FlightMode.CRUISE,
                "distance": 0
            }
        ],
        "total_fuel_cost": 60,
        "total_time": 100,
        "total_distance": 141.42
    }


@given(parsers.parse('I create a plan route query for ship "{ship}" to "{dest}" for player {player:d}'))
def create_query(context, ship, dest, player):
    """Create a plan route query"""
    context['query'] = PlanRouteQuery(
        ship_symbol=ship,
        destination_symbol=dest,
        player_id=player
    )


# ============================================================================
# When Steps - Actions
# ============================================================================

@when(parsers.parse('I create a plan route query for ship "{ship}" to "{dest}" for player {player:d}'))
def create_query_when(context, ship, dest, player):
    """Create a plan route query"""
    context['query'] = PlanRouteQuery(
        ship_symbol=ship,
        destination_symbol=dest,
        player_id=player
    )
    context['destination'] = dest


@when(parsers.parse('I create a plan route query for ship "{ship}" to "{dest}" for player {player:d} with prefer_cruise {prefer:w}'))
def create_query_with_prefer(context, ship, dest, player, prefer):
    """Create a plan route query with prefer_cruise setting"""
    prefer_cruise = prefer.lower() == 'true'
    context['query'] = PlanRouteQuery(
        ship_symbol=ship,
        destination_symbol=dest,
        player_id=player,
        prefer_cruise=prefer_cruise
    )
    context['destination'] = dest


@when("I attempt to modify the query ship symbol")
def modify_query(context):
    """Attempt to modify the query"""
    import asyncio

    try:
        context['query'].ship_symbol = "MODIFIED"
        context['exception'] = None
    except Exception as e:
        context['exception'] = e


@when(parsers.parse('I plan a route for ship "{ship}" to "{dest}" as player {player:d}'))
def plan_route(context, handler, mock_graph_provider, ship, dest, player):
    """Execute plan route query"""
    import asyncio
    from unittest.mock import patch

    query = PlanRouteQuery(
        ship_symbol=ship,
        destination_symbol=dest,
        player_id=player
    )

    context['destination'] = dest

    try:
        with patch('configuration.container.get_graph_provider_for_player', return_value=mock_graph_provider):
            result = asyncio.run(handler.handle(query))
        context['result'] = result
        context['exception'] = None
    except Exception as e:
        context['result'] = None
        context['exception'] = e


@when(parsers.parse('I plan a route for ship "{ship}" to "{dest}" as player {player:d} with prefer_cruise {prefer:w}'))
def plan_route_with_prefer(context, handler, mock_graph_provider, ship, dest, player, prefer):
    """Execute plan route query with prefer_cruise setting"""
    import asyncio
    from unittest.mock import patch

    prefer_cruise = prefer.lower() == 'true'

    query = PlanRouteQuery(
        ship_symbol=ship,
        destination_symbol=dest,
        player_id=player,
        prefer_cruise=prefer_cruise
    )

    context['destination'] = dest

    try:
        with patch('configuration.container.get_graph_provider_for_player', return_value=mock_graph_provider):
            result = asyncio.run(handler.handle(query))
        context['result'] = result
        context['exception'] = None
    except Exception as e:
        context['result'] = None
        context['exception'] = e


@when(parsers.parse('I attempt to plan a route for ship "{ship}" to "{dest}" as player {player:d}'))
def attempt_plan_route(context, handler, mock_graph_provider, ship, dest, player):
    """Attempt to plan route (expecting failure)"""
    plan_route(context, handler, mock_graph_provider, ship, dest, player)


# ============================================================================
# Then Steps - Assertions
# ============================================================================

@then(parsers.parse('the query ship symbol should be "{ship}"'))
def query_ship_symbol(context, ship):
    """Verify query ship symbol"""
    assert context['query'].ship_symbol == ship


@then(parsers.parse('the query destination should be "{dest}"'))
def query_destination(context, dest):
    """Verify query destination"""
    assert context['query'].destination_symbol == dest


@then(parsers.parse('the query player ID should be {player:d}'))
def query_player_id(context, player):
    """Verify query player ID"""
    assert context['query'].player_id == player


@then(parsers.parse('the query prefer_cruise should be {value:w}'))
def query_prefer_cruise(context, value):
    """Verify query prefer_cruise value"""
    expected = value.lower() == 'true'
    assert context['query'].prefer_cruise == expected


@then("the modification should fail with AttributeError")
def modification_fails(context):
    """Verify modification failed"""
    exception = context.get('exception')
    assert exception is not None, "No exception was raised"
    assert isinstance(exception, AttributeError), \
        f"Expected AttributeError, got {type(exception).__name__}"


@then("a route should be returned")
def route_returned(context):
    """Verify route was returned"""
    result = context.get('result')
    assert result is not None, "No route was returned"
    assert isinstance(result, Route), f"Expected Route, got {type(result)}"


@then(parsers.parse('the route ship symbol should be "{ship}"'))
def route_ship_symbol(context, ship):
    """Verify route ship symbol"""
    result = context.get('result')
    assert result is not None, "No route was returned"
    assert result.ship_symbol == ship


@then(parsers.parse('the route player ID should be {player:d}'))
def route_player_id(context, player):
    """Verify route player ID"""
    result = context.get('result')
    assert result is not None, "No route was returned"
    assert result.player_id == player


@then(parsers.parse('the route should have {count:d} segment'))
@then(parsers.parse('the route should have {count:d} segments'))
def route_segment_count(context, count):
    """Verify route segment count"""
    result = context.get('result')
    assert result is not None, "No route was returned"
    assert len(result.segments) == count, \
        f"Expected {count} segments, got {len(result.segments)}"


@then(parsers.parse('the ship repository should have been queried for "{ship}"'))
def repository_queried(context, mock_ship_repo, ship):
    """Verify ship repository was queried"""
    # BLACK-BOX TESTING: We verify the repository was queried by checking
    # that the returned route contains the ship data. If the ship wasn't
    # found in the repository, the query would have failed with ShipNotFoundError.
    # Testing internal state (mock_ship_repo._ships) would be white-box testing.
    route = context.get('result')
    assert route is not None, "Route should have been created (repository was queried)"
    assert route.ship_symbol == ship, f"Route should be for ship {ship}"


@then(parsers.parse('the graph provider should have been queried for system "{system}"'))
def graph_provider_queried(context, mock_graph_provider, system):
    """Verify graph provider was queried"""
    assert len(mock_graph_provider.query_calls) > 0, \
        "Graph provider should have been queried"
    assert any(call[0] == system for call in mock_graph_provider.query_calls), \
        f"Graph provider should have been queried for system {system}"


@then("the query should fail with ShipNotFoundError")
def query_fails_ship_not_found(context):
    """Verify ShipNotFoundError was raised"""
    exception = context.get('exception')
    assert exception is not None, "No exception was raised"
    assert isinstance(exception, ShipNotFoundError), \
        f"Expected ShipNotFoundError, got {type(exception).__name__}"


@then(parsers.parse('the error message should contain "{text}"'))
def error_contains(context, text):
    """Verify error message contains text"""
    exception = context.get('exception')
    assert exception is not None, "No exception was raised"
    assert text in str(exception), \
        f"Expected '{text}' in error message, got: {str(exception)}"


@then("the query should fail with ValueError")
def query_fails_value_error(context):
    """Verify ValueError was raised"""
    exception = context.get('exception')
    assert exception is not None, "No exception was raised"
    assert isinstance(exception, ValueError), \
        f"Expected ValueError, got {type(exception).__name__}"


@then(parsers.parse('the routing engine should have been called with start "{start}"'))
def routing_engine_start(context, mock_routing_engine, start):
    """Verify routing engine start parameter"""
    assert mock_routing_engine.call_params is not None, \
        "Routing engine was not called"
    assert mock_routing_engine.call_params['start'] == start


@then(parsers.parse('the routing engine should have been called with goal "{goal}"'))
def routing_engine_goal(context, mock_routing_engine, goal):
    """Verify routing engine goal parameter"""
    assert mock_routing_engine.call_params is not None, \
        "Routing engine was not called"
    assert mock_routing_engine.call_params['goal'] == goal


@then(parsers.parse('the routing engine should have been called with current_fuel {fuel:d}'))
def routing_engine_current_fuel(context, mock_routing_engine, fuel):
    """Verify routing engine current_fuel parameter"""
    assert mock_routing_engine.call_params is not None, \
        "Routing engine was not called"
    assert mock_routing_engine.call_params['current_fuel'] == fuel


@then(parsers.parse('the routing engine should have been called with fuel_capacity {capacity:d}'))
def routing_engine_fuel_capacity(context, mock_routing_engine, capacity):
    """Verify routing engine fuel_capacity parameter"""
    assert mock_routing_engine.call_params is not None, \
        "Routing engine was not called"
    assert mock_routing_engine.call_params['fuel_capacity'] == capacity


@then(parsers.parse('the routing engine should have been called with engine_speed {speed:d}'))
def routing_engine_engine_speed(context, mock_routing_engine, speed):
    """Verify routing engine engine_speed parameter"""
    assert mock_routing_engine.call_params is not None, \
        "Routing engine was not called"
    assert mock_routing_engine.call_params['engine_speed'] == speed


@then(parsers.parse('the routing engine should have been called with prefer_cruise {prefer:w}'))
def routing_engine_prefer_cruise(context, mock_routing_engine, prefer):
    """Verify routing engine prefer_cruise parameter"""
    expected = prefer.lower() == 'true'
    assert mock_routing_engine.call_params is not None, \
        "Routing engine was not called"
    assert mock_routing_engine.call_params['prefer_cruise'] == expected


@then(parsers.parse('the routing engine should have received a graph with waypoint "{waypoint}"'))
def routing_engine_graph_waypoint(context, mock_routing_engine, waypoint):
    """Verify routing engine received graph with waypoint"""
    assert mock_routing_engine.call_params is not None, \
        "Routing engine was not called"
    graph = mock_routing_engine.call_params['graph']
    assert waypoint in graph, \
        f"Waypoint {waypoint} not in graph"


@then(parsers.parse('the graph waypoint "{waypoint}" should have symbol "{symbol}"'))
def graph_waypoint_symbol(context, mock_routing_engine, waypoint, symbol):
    """Verify graph waypoint has correct symbol"""
    assert mock_routing_engine.call_params is not None, \
        "Routing engine was not called"
    graph = mock_routing_engine.call_params['graph']
    assert waypoint in graph, \
        f"Waypoint {waypoint} not in graph"
    assert graph[waypoint].symbol == symbol


@then(parsers.parse('segment {num:d} should have from_waypoint "{waypoint}"'))
def segment_from_waypoint(context, num, waypoint):
    """Verify segment from_waypoint"""
    result = context.get('result')
    assert result is not None, "No route was returned"
    segment = result.segments[num - 1]
    assert segment.from_waypoint.symbol == waypoint


@then(parsers.parse('segment {num:d} should have to_waypoint "{waypoint}"'))
def segment_to_waypoint(context, num, waypoint):
    """Verify segment to_waypoint"""
    result = context.get('result')
    assert result is not None, "No route was returned"
    segment = result.segments[num - 1]
    assert segment.to_waypoint.symbol == waypoint


@then(parsers.parse('segment {num:d} should have distance {distance:f}'))
def segment_distance(context, num, distance):
    """Verify segment distance"""
    result = context.get('result')
    assert result is not None, "No route was returned"
    segment = result.segments[num - 1]
    assert segment.distance == distance


@then(parsers.parse('segment {num:d} should have fuel_required {fuel:d}'))
def segment_fuel_required(context, num, fuel):
    """Verify segment fuel_required"""
    result = context.get('result')
    assert result is not None, "No route was returned"
    segment = result.segments[num - 1]
    assert segment.fuel_required == fuel


@then(parsers.parse('segment {num:d} should have travel_time {time:d}'))
def segment_travel_time(context, num, time):
    """Verify segment travel_time"""
    result = context.get('result')
    assert result is not None, "No route was returned"
    segment = result.segments[num - 1]
    assert segment.travel_time == time


@then(parsers.parse('segment {num:d} should have flight_mode {mode}'))
def segment_flight_mode(context, num, mode):
    """Verify segment flight_mode"""
    result = context.get('result')
    assert result is not None, "No route was returned"
    segment = result.segments[num - 1]
    expected_mode = FlightMode.CRUISE if mode == "CRUISE" else FlightMode.DRIFT
    assert segment.flight_mode == expected_mode


@then(parsers.parse('segment {num:d} should require refuel'))
def segment_requires_refuel(context, num):
    """Verify segment requires refuel"""
    result = context.get('result')
    assert result is not None, "No route was returned"
    segment = result.segments[num - 1]
    assert segment.requires_refuel is True


@then(parsers.parse('segment {num:d} should not require refuel'))
def segment_not_require_refuel(context, num):
    """Verify segment does not require refuel"""
    result = context.get('result')
    assert result is not None, "No route was returned"
    segment = result.segments[num - 1]
    assert segment.requires_refuel is False


@then(parsers.parse('the route ID should be "{route_id}"'))
def route_id_check(context, route_id):
    """Verify route ID"""
    result = context.get('result')
    assert result is not None, "No route was returned"
    assert result.route_id == route_id
