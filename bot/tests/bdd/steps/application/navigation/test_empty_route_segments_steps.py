"""Step definitions for navigation empty route segments scenarios"""
import pytest
import asyncio
from pytest_bdd import scenarios, given, when, then, parsers
from unittest.mock import Mock, AsyncMock, MagicMock
from datetime import datetime, timezone

from application.navigation.commands.navigate_ship import (
    NavigateShipCommand,
    NavigateShipHandler
)
from domain.shared.ship import Ship
from domain.shared.value_objects import Waypoint, Fuel, FlightMode
from domain.navigation.route import Route, RouteSegment

# Load all scenarios from the feature file
scenarios('../../../features/application/navigation/empty_route_segments.feature')


@pytest.fixture
def context():
    """Provide shared context dictionary for test data"""
    return {}


@pytest.fixture
def mock_ship_repository():
    """Mock ship repository"""
    repo = Mock()

    # Mock sync_from_api to return the same ship
    def sync_from_api(ship_symbol, player_id, api_client, graph_provider):
        # Return the ship stored in the mock
        return repo._test_ship

    repo.sync_from_api = Mock(side_effect=sync_from_api)
    repo.update = Mock()

    return repo


@pytest.fixture
def mock_routing_engine():
    """Mock routing engine"""
    engine = Mock()
    return engine


@pytest.fixture
def mock_graph_provider():
    """Mock graph provider"""
    provider = Mock()
    return provider


@pytest.fixture
def mock_api_client():
    """Mock API client"""
    client = Mock()
    return client


@given(parsers.parse('a player with agent "{agent_symbol}"'))
def player_with_agent(context, agent_symbol):
    """Register test player"""
    context['agent_symbol'] = agent_symbol
    context['player_id'] = 1


@given(parsers.parse('a ship "{ship_symbol}" at waypoint "{waypoint_symbol}"'))
def ship_at_waypoint(context, ship_symbol, waypoint_symbol, mock_ship_repository):
    """Create a ship at a specific waypoint"""
    context['ship_symbol'] = ship_symbol
    context['waypoint_symbol'] = waypoint_symbol

    # Extract system from waypoint
    parts = waypoint_symbol.split('-')
    system_symbol = f"{parts[0]}-{parts[1]}"
    context['system_symbol'] = system_symbol

    # Create ship entity
    current_location = Waypoint(
        symbol=waypoint_symbol,
        x=0.0,
        y=0.0,
        waypoint_type="PLANET",
        system_symbol=system_symbol
    )

    fuel = Fuel(current=100, capacity=100)

    ship = Ship(
        ship_symbol=ship_symbol,
        player_id=context['player_id'],
        current_location=current_location,
        fuel=fuel,
        fuel_capacity=100,
        cargo_capacity=40,
        cargo_units=0,
        engine_speed=30,
        nav_status=Ship.IN_ORBIT
    )

    # Store ship in mock repository
    mock_ship_repository._test_ship = ship
    mock_ship_repository.find_by_symbol = Mock(return_value=ship)

    context['ship'] = ship


@given(parsers.parse('the waypoint cache for system "{system_symbol}" is empty'))
def empty_waypoint_cache(context, system_symbol, mock_graph_provider):
    """Mock empty waypoint cache"""
    # Graph provider returns empty waypoints dict
    graph_result = Mock()
    graph_result.graph = {'waypoints': {}}
    graph_result.source = 'database'
    graph_result.message = f'No waypoints cached for {system_symbol}'

    mock_graph_provider.get_graph = Mock(return_value=graph_result)
    context['mock_graph_provider'] = mock_graph_provider
    context['expected_error_contains'] = f'No waypoints found for system {system_symbol}'


@given(parsers.parse('the waypoint cache for system "{system_symbol}" has waypoints'))
def waypoint_cache_has_waypoints(context, system_symbol, mock_graph_provider):
    """Mock waypoint cache with waypoints"""
    # Create test waypoints
    waypoints = {
        f'{system_symbol}-A1': {
            'symbol': f'{system_symbol}-A1',
            'type': 'PLANET',
            'x': 0,
            'y': 0,
            'systemSymbol': system_symbol,
            'traits': ['MARKETPLACE'],
            'has_fuel': True
        },
        f'{system_symbol}-B1': {
            'symbol': f'{system_symbol}-B1',
            'type': 'MOON',
            'x': 10,
            'y': 10,
            'systemSymbol': system_symbol,
            'traits': [],
            'has_fuel': False
        }
    }

    graph_result = Mock()
    graph_result.graph = {'waypoints': waypoints}
    graph_result.source = 'database'

    mock_graph_provider.get_graph = Mock(return_value=graph_result)
    context['mock_graph_provider'] = mock_graph_provider


@given('the routing engine returns empty steps for the route')
def routing_engine_returns_empty_steps(context, mock_routing_engine):
    """Mock routing engine to return empty steps"""
    route_plan = {
        'steps': [],
        'total_distance': 0.0,
        'total_fuel': 0,
        'total_time': 0
    }

    mock_routing_engine.find_optimal_path = Mock(return_value=route_plan)
    context['mock_routing_engine'] = mock_routing_engine
    context['expected_error_contains'] = 'No route found'


@given('the routing engine returns only REFUEL actions with no TRAVEL steps')
def routing_engine_returns_only_refuel(context, mock_routing_engine):
    """Mock routing engine to return only REFUEL actions"""
    route_plan = {
        'steps': [
            {'action': 'REFUEL', 'waypoint': 'X1-TEST-A1'}
        ],
        'total_distance': 0.0,
        'total_fuel': 0,
        'total_time': 0
    }

    mock_routing_engine.find_optimal_path = Mock(return_value=route_plan)
    context['mock_routing_engine'] = mock_routing_engine
    context['expected_error_contains'] = 'Route plan has no TRAVEL steps'


@given(parsers.parse('the waypoint cache for system "{system_symbol}" is missing waypoint "{waypoint_symbol}"'))
def waypoint_cache_missing_waypoint(context, system_symbol, waypoint_symbol, mock_graph_provider):
    """Mock waypoint cache missing specific waypoint"""
    # Create waypoints but exclude the ship's location
    waypoints = {
        f'{system_symbol}-B1': {
            'symbol': f'{system_symbol}-B1',
            'type': 'MOON',
            'x': 10,
            'y': 10,
            'systemSymbol': system_symbol,
            'traits': [],
            'has_fuel': False
        }
    }

    graph_result = Mock()
    graph_result.graph = {'waypoints': waypoints}
    graph_result.source = 'database'

    mock_graph_provider.get_graph = Mock(return_value=graph_result)
    context['mock_graph_provider'] = mock_graph_provider
    context['expected_error_contains'] = f'Waypoint {waypoint_symbol} not found'


@given(parsers.parse('the waypoint cache for system "{system_symbol}" has all required waypoints'))
def waypoint_cache_has_all_waypoints(context, system_symbol, mock_graph_provider):
    """Mock complete waypoint cache"""
    # Create all required waypoints including ship location
    waypoint_symbol = context.get('waypoint_symbol', f'{system_symbol}-A1')
    destination_symbol = context.get('destination_symbol', f'{system_symbol}-B1')

    waypoints = {
        waypoint_symbol: {
            'symbol': waypoint_symbol,
            'type': 'PLANET',
            'x': 0,
            'y': 0,
            'systemSymbol': system_symbol,
            'traits': ['MARKETPLACE'],
            'has_fuel': True
        },
        destination_symbol: {
            'symbol': destination_symbol,
            'type': 'MOON',
            'x': 10,
            'y': 10,
            'systemSymbol': system_symbol,
            'traits': [],
            'has_fuel': False
        }
    }

    graph_result = Mock()
    graph_result.graph = {'waypoints': waypoints}
    graph_result.source = 'database'

    mock_graph_provider.get_graph = Mock(return_value=graph_result)
    context['mock_graph_provider'] = mock_graph_provider


@given('the routing engine returns valid TRAVEL steps')
def routing_engine_returns_valid_steps(context, mock_routing_engine):
    """Mock routing engine to return valid TRAVEL steps"""
    waypoint_symbol = context.get('waypoint_symbol', 'X1-TEST-A1')
    destination_symbol = context.get('destination_symbol', 'X1-TEST-B1')

    route_plan = {
        'steps': [
            {
                'action': 'TRAVEL',
                'waypoint': destination_symbol,
                'from': waypoint_symbol,
                'distance': 10.0,
                'fuel_cost': 10,
                'time': 60,
                'mode': FlightMode.CRUISE
            }
        ],
        'total_distance': 10.0,
        'total_fuel': 10,
        'total_time': 60
    }

    mock_routing_engine.find_optimal_path = Mock(return_value=route_plan)
    context['mock_routing_engine'] = mock_routing_engine


@when(parsers.parse('I navigate ship "{ship_symbol}" to "{destination_symbol}"'))
def navigate_ship_to_destination(
    context,
    ship_symbol,
    destination_symbol,
    mock_ship_repository,
    mock_routing_engine,
    mock_api_client
):
    """Attempt to navigate ship to destination"""
    context['destination_symbol'] = destination_symbol

    # Patch the container to return our mocks
    from unittest.mock import patch

    graph_provider = context.get('mock_graph_provider')
    routing_engine = context.get('mock_routing_engine', mock_routing_engine)

    # Mock API client responses
    mock_api_client.get_ship = Mock(return_value={'data': {
        'symbol': ship_symbol,
        'nav': {'status': 'IN_ORBIT'},
        'fuel': {'current': 100, 'capacity': 100}
    }})
    mock_api_client.orbit_ship = Mock(return_value={'data': {'nav': {'status': 'IN_ORBIT'}}})
    mock_api_client.navigate_ship = Mock(return_value={
        'data': {
            'nav': {
                'status': 'IN_TRANSIT',
                'route': {
                    'arrival': '2024-01-01T12:00:00Z',
                    'destination': {'symbol': destination_symbol}
                }
            },
            'fuel': {'current': 90, 'capacity': 100}
        }
    })

    with patch('configuration.container.get_api_client_for_player', return_value=mock_api_client):
        with patch('configuration.container.get_graph_provider_for_player', return_value=graph_provider):
            handler = NavigateShipHandler(
                ship_repository=mock_ship_repository,
                routing_engine=routing_engine
            )

            command = NavigateShipCommand(
                ship_symbol=ship_symbol,
                destination_symbol=destination_symbol,
                player_id=context['player_id']
            )

            try:
                result = asyncio.run(handler.handle(command))
                context['navigation_result'] = result
                context['navigation_error'] = None
            except Exception as e:
                context['navigation_result'] = None
                context['navigation_error'] = e


@then(parsers.parse('the navigation should fail with message "{expected_message}"'))
def check_navigation_failure_message(context, expected_message):
    """Verify navigation failed with expected message"""
    error = context.get('navigation_error')
    assert error is not None, "Expected navigation to fail but it succeeded"

    error_message = str(error)
    assert expected_message in error_message, (
        f"Expected error message to contain '{expected_message}', "
        f"but got: {error_message}"
    )


@then('the error should suggest checking waypoint cache')
def check_error_suggests_waypoint_cache(context):
    """Verify error suggests checking waypoint cache"""
    error = context.get('navigation_error')
    assert error is not None, "Expected error to exist"

    error_message = str(error).lower()
    assert any(phrase in error_message for phrase in ['waypoint', 'cache', 'sync']), (
        f"Expected error to mention waypoint cache, but got: {error_message}"
    )


@then('the error should include the route steps')
def check_error_includes_route_steps(context):
    """Verify error includes route steps information"""
    error = context.get('navigation_error')
    assert error is not None, "Expected error to exist"

    error_message = str(error).lower()
    assert 'steps' in error_message, (
        f"Expected error to include route steps, but got: {error_message}"
    )


@then('the error should suggest syncing waypoints from API')
def check_error_suggests_syncing_from_api(context):
    """Verify error suggests syncing from API"""
    error = context.get('navigation_error')
    assert error is not None, "Expected error to exist"

    error_message = str(error).lower()
    assert any(phrase in error_message for phrase in ['sync', 'api', 'fetch']), (
        f"Expected error to suggest syncing from API, but got: {error_message}"
    )


@then('the navigation should complete successfully')
def check_navigation_success(context):
    """Verify navigation completed successfully"""
    error = context.get('navigation_error')
    result = context.get('navigation_result')

    assert error is None, f"Expected navigation to succeed but got error: {error}"
    assert result is not None, "Expected navigation result but got None"
    assert isinstance(result, Route), f"Expected Route object but got {type(result)}"


@then(parsers.parse('the route should have at least {min_segments:d} segment'))
def check_route_has_segments(context, min_segments):
    """Verify route has minimum number of segments"""
    result = context.get('navigation_result')
    assert result is not None, "Expected navigation result"

    segments = result.segments
    assert len(segments) >= min_segments, (
        f"Expected at least {min_segments} segment(s) but got {len(segments)}"
    )
