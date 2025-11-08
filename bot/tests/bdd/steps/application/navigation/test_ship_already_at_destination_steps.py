"""Step definitions for ship already at destination navigation tests"""
import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from unittest.mock import Mock, patch

from application.navigation.commands.navigate_ship import (
    NavigateShipCommand,
    NavigateShipHandler
)
from domain.navigation.route import RouteStatus
from domain.shared.value_objects import Waypoint, Fuel
from domain.shared.ship import Ship

# Load scenarios from feature file
scenarios('../../../features/application/navigation/ship_already_at_destination.feature')


@pytest.fixture
def context():
    """Shared context for test state"""
    ctx = type('Context', (), {})()
    ctx.player_id = 1
    ctx.result = None
    ctx.exception = None
    return ctx


@pytest.fixture
def mock_ship_repository():
    """Mock ship repository"""
    repo = Mock()
    return repo


@pytest.fixture
def mock_routing_engine():
    """Mock routing engine"""
    engine = Mock()
    return engine


@given(parsers.parse('a player with agent "{agent_symbol}"'))
def player_with_agent(context, agent_symbol):
    """Register test player"""
    context.agent_symbol = agent_symbol
    context.player_id = 1


@given(parsers.parse('a ship "{ship_symbol}" at waypoint "{waypoint_symbol}"'))
def ship_at_waypoint(context, ship_symbol, waypoint_symbol, mock_ship_repository):
    """Create a ship at a specific waypoint"""
    context.ship_symbol = ship_symbol
    context.waypoint_symbol = waypoint_symbol

    # Extract system from waypoint
    parts = waypoint_symbol.split('-')
    system_symbol = f"{parts[0]}-{parts[1]}"
    context.system_symbol = system_symbol

    # Create ship entity
    current_location = Waypoint(
        symbol=waypoint_symbol,
        x=0.0,
        y=0.0,
        waypoint_type="PLANET",
        system_symbol=system_symbol,
        has_fuel=True
    )

    fuel = Fuel(current=100, capacity=400)

    ship = Ship(
        ship_symbol=ship_symbol,
        player_id=context.player_id,
        current_location=current_location,
        fuel=fuel,
        fuel_capacity=400,
        cargo_capacity=40,
        cargo_units=0,
        engine_speed=30,
        nav_status=Ship.IN_ORBIT
    )

    # Store ship in repository mock
    mock_ship_repository.find_by_symbol.return_value = ship
    context.ship = ship
    context.mock_ship_repository = mock_ship_repository


@given(parsers.parse('the ship has fuel {current:d}/{capacity:d}'))
def ship_has_fuel(context, current, capacity):
    """Update ship fuel levels"""
    # Ship is already created, update the fuel
    context.ship = Ship(
        ship_symbol=context.ship.ship_symbol,
        player_id=context.ship.player_id,
        current_location=context.ship.current_location,
        fuel=Fuel(current=current, capacity=capacity),
        fuel_capacity=capacity,
        cargo_capacity=context.ship.cargo_capacity,
        cargo_units=context.ship.cargo_units,
        engine_speed=context.ship.engine_speed,
        nav_status=context.ship.nav_status
    )
    context.mock_ship_repository.find_by_symbol.return_value = context.ship


@given(parsers.parse('system "{system_symbol}" has {count:d} waypoints'))
def system_waypoints(context, system_symbol, count, mock_routing_engine):
    """Create simple waypoint graph"""
    waypoints = {}

    # Create waypoints for testing
    waypoints['X1-TEST-A1'] = Waypoint(
        symbol='X1-TEST-A1',
        waypoint_type='PLANET',
        x=0,
        y=0,
        system_symbol=system_symbol,
        traits=('MARKETPLACE',),
        has_fuel=True
    )

    waypoints['X1-TEST-B2'] = Waypoint(
        symbol='X1-TEST-B2',
        waypoint_type='ASTEROID',
        x=10,
        y=10,
        system_symbol=system_symbol,
        traits=(),
        has_fuel=False
    )

    context.waypoints = waypoints
    context.mock_routing_engine = mock_routing_engine


@when(parsers.parse('I navigate ship "{ship_symbol}" to "{destination}"'))
def navigate_ship(context, ship_symbol, destination, mock_ship_repository, mock_routing_engine):
    """Execute navigation command"""
    import asyncio

    # Configure routing engine mock
    # When start == destination, routing engine returns empty steps
    if context.ship.current_location.symbol == destination:
        context.mock_routing_engine.find_optimal_path.return_value = {
            'steps': [],
            'total_fuel_cost': 0,
            'total_time': 0,
            'total_distance': 0.0
        }

    # Create handler with mocks
    handler = NavigateShipHandler(
        ship_repository=mock_ship_repository,
        routing_engine=mock_routing_engine
    )

    # Mock get_graph_provider_for_player and get_api_client_for_player
    with patch('configuration.container.get_api_client_for_player') as mock_get_api, \
         patch('configuration.container.get_graph_provider_for_player') as mock_get_graph:

        # Create mock graph provider
        mock_graph_provider = Mock()
        mock_graph_result = type('GraphResult', (), {
            'graph': {'waypoints': context.waypoints}
        })()
        mock_graph_provider.get_graph.return_value = mock_graph_result
        mock_get_graph.return_value = mock_graph_provider

        # Create mock API client
        mock_api_client = Mock()
        mock_get_api.return_value = mock_api_client
        context.mock_api_client = mock_api_client

        try:
            command = NavigateShipCommand(
                ship_symbol=ship_symbol,
                destination_symbol=destination,
                player_id=context.player_id
            )
            context.result = asyncio.run(handler.handle(command))
            context.exception = None
        except Exception as e:
            context.exception = e
            context.result = None


@then('the navigation should succeed immediately')
def navigation_succeeds(context):
    """Verify navigation succeeded without error"""
    if context.exception:
        pytest.fail(f"Navigation failed with error: {context.exception}")
    assert context.result is not None, "Expected route result but got None"


@then(parsers.parse('the route should have {count:d} segments'))
def route_has_segments(context, count):
    """Verify route segment count"""
    assert context.result is not None
    assert len(context.result.segments) == count, \
        f"Expected {count} segments but got {len(context.result.segments)}"


@then(parsers.parse('the route status should be "{status}"'))
def route_status(context, status):
    """Verify route status"""
    assert context.result is not None
    expected_status = RouteStatus[status]
    assert context.result.status == expected_status, \
        f"Expected status {expected_status} but got {context.result.status}"
