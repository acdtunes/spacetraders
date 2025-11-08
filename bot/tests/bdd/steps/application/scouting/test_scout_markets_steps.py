"""Step definitions for scout markets command tests"""
import asyncio
import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from unittest.mock import Mock, patch
from datetime import datetime, timezone

from application.scouting.commands.scout_markets import ScoutMarketsCommand
from configuration.container import (
    get_mediator,
    get_ship_repository,
    get_database,
    reset_container
)

# Load scenarios
scenarios('../../../features/application/scouting/scout_markets.feature')


@pytest.fixture
def context():
    """Shared context for scenario steps"""
    reset_container()
    return {
        'mediator': get_mediator(),
        'ship_repo': get_ship_repository(),
        'player_id': 1,
        'ships': [],
        'markets': []
    }


@pytest.fixture(autouse=True)
def cleanup():
    """Cleanup after each test"""
    yield
    reset_container()


# Background steps

@given(parsers.parse('a player with ID {player_id:d}'))
def create_player(context, player_id):
    """Create a player in the database"""
    context['player_id'] = player_id

    from configuration.container import get_database
    db = get_database()
    with db.transaction() as conn:
        conn.execute("""
            INSERT OR IGNORE INTO players (player_id, agent_symbol, token, created_at)
            VALUES (?, ?, ?, ?)
        """, (player_id, f'TEST_AGENT_{player_id}', 'test_token', '2025-01-01T00:00:00Z'))


@given(parsers.parse('ships in system "{system}":'))
def create_ships(context, system, datatable):
    """Setup ship data for mocking API responses"""
    headers = datatable[0]
    context['ships_data'] = []

    for row in datatable[1:]:
        row_dict = dict(zip(headers, row))
        ship_symbol = row_dict['ship_symbol']
        waypoint = row_dict['waypoint']
        status = row_dict['status']

        # Store ship data for API mocking
        context['ships_data'].append({
            'symbol': ship_symbol,
            'nav': {
                'waypointSymbol': waypoint,
                'systemSymbol': system,
                'status': status,
                'flightMode': 'CRUISE'
            },
            'fuel': {
                'current': 300,
                'capacity': 400
            },
            'cargo': {
                'capacity': 40,
                'units': 0,
                'inventory': []
            },
            'frame': {
                'symbol': 'FRAME_PROBE'
            },
            'reactor': {
                'symbol': 'REACTOR_SOLAR_I'
            },
            'engine': {
                'symbol': 'ENGINE_IMPULSE_DRIVE_I',
                'speed': 30
            },
            'modules': [],
            'mounts': []
        })
        context['ships'].append(ship_symbol)


# Command steps

@when(parsers.parse('I execute scout markets for system "{system}" with ships:'))
def set_ships_for_scout_markets(context, system, datatable):
    """Store ships for scout markets"""
    context['system'] = system
    context['scout_ships'] = [row[0] for row in datatable]


@when('markets:')
def set_markets_and_execute(context, datatable):
    """Store markets and execute scout markets command"""
    context['markets'] = [row[0] for row in datatable]

    # Mock ship repository to return ships
    from domain.shared.ship import Ship
    from domain.shared.value_objects import Waypoint, Fuel

    mock_ship_repo = Mock()

    def mock_find_by_symbol(ship_symbol, player_id):
        ship_data = next((s for s in context['ships_data'] if s['symbol'] == ship_symbol), None)
        if not ship_data:
            return None

        # Convert API data to Ship entity
        waypoint_symbol = ship_data['nav']['waypointSymbol']
        system_symbol = ship_data['nav']['systemSymbol']

        return Ship(
            ship_symbol=ship_symbol,
            player_id=player_id,
            current_location=Waypoint(
                symbol=waypoint_symbol,
                waypoint_type='PLANET',
                x=0,
                y=0,
                system_symbol=system_symbol,
                traits=tuple(),
                has_fuel=True
            ),
            fuel=Fuel(
                current=ship_data['fuel']['current'],
                capacity=ship_data['fuel']['capacity']
            ),
            fuel_capacity=ship_data['fuel']['capacity'],
            cargo_capacity=ship_data['cargo']['capacity'],
            cargo_units=ship_data['cargo']['units'],
            engine_speed=ship_data['engine']['speed'],
            nav_status=ship_data['nav']['status']
        )

    mock_ship_repo.find_by_symbol.side_effect = mock_find_by_symbol

    # Mock graph provider
    mock_graph = Mock()
    # Import with absolute path to avoid module not found error
    import sys
    import os
    # Add tests directory to path temporarily
    tests_dir = os.path.join(os.path.dirname(__file__), '../../../../')
    sys.path.insert(0, tests_dir)
    try:
        from fixtures.graph_fixtures import get_mock_graph_for_system
        mock_graph.get_graph.return_value = get_mock_graph_for_system(context['system'])
    finally:
        sys.path.pop(0)

    # Mock routing engine for multi-ship VRP optimization
    mock_routing_engine = Mock()

    def mock_optimize_fleet_tour(graph, markets, ship_locations, fuel_capacity, engine_speed):
        """Mock VRP partitioning - distribute markets evenly across ships"""
        ships = list(ship_locations.keys())
        assignments = {ship: [] for ship in ships}

        # Round-robin distribution
        for i, market in enumerate(markets):
            ship_idx = i % len(ships)
            assignments[ships[ship_idx]].append(market)

        return assignments

    mock_routing_engine.optimize_fleet_tour.side_effect = mock_optimize_fleet_tour

    # Mock daemon client to track container creation
    mock_daemon = Mock()
    created_containers = []

    # Mock list_containers to return empty list (no existing containers)
    def mock_list_containers(player_id=None):
        return {'containers': []}

    mock_daemon.list_containers.side_effect = mock_list_containers

    def mock_create_container(config):
        container_id = f"scout-tour-{config['config']['params']['ship_symbol']}-mock"
        created_containers.append({
            'container_id': container_id,
            'ship': config['config']['params']['ship_symbol'],
            'markets': config['config']['params']['markets']
        })
        return {'container_id': container_id}

    mock_daemon.create_container.side_effect = mock_create_container

    # Create test mediator with mocked ship repository
    from pymediatr import Mediator
    from application.scouting.commands.scout_markets import ScoutMarketsCommand, ScoutMarketsHandler
    from application.common.behaviors import LoggingBehavior, ValidationBehavior

    test_mediator = Mediator()
    test_mediator.register_behavior(LoggingBehavior())
    test_mediator.register_behavior(ValidationBehavior())
    test_mediator.register_handler(
        ScoutMarketsCommand,
        lambda: ScoutMarketsHandler(mock_ship_repo)
    )

    # Patch at container level
    with patch('configuration.container.get_graph_provider_for_player') as mock_graph_fn, \
         patch('configuration.container.get_daemon_client') as mock_daemon_fn, \
         patch('configuration.container.get_routing_engine') as mock_routing_fn:

        mock_graph_fn.return_value = mock_graph
        mock_daemon_fn.return_value = mock_daemon
        mock_routing_fn.return_value = mock_routing_engine

        # Create and execute command
        command = ScoutMarketsCommand(
            ship_symbols=context['scout_ships'],
            player_id=context['player_id'],
            system=context['system'],
            markets=context['markets'],
            iterations=1
        )

        try:
            result = asyncio.run(test_mediator.send_async(command))
            context['scout_result'] = result
            context['created_containers'] = created_containers
            context['scout_succeeded'] = True
        except Exception as e:
            context['scout_error'] = str(e)
            context['scout_succeeded'] = False


# Assertion steps

@then('scout markets should complete successfully')
def check_scout_success(context):
    """Verify scout markets completed successfully"""
    if not context.get('scout_succeeded'):
        error_msg = context.get('scout_error', 'Unknown error')
        assert False, f"Scout markets failed: {error_msg}"


@then(parsers.parse('{count:d} ship assignments should be returned'))
def check_assignment_count(context, count):
    """Verify number of ship assignments"""
    result = context['scout_result']
    assert len(result.assignments) == count, \
        f"Expected {count} ship assignments but got {len(result.assignments)}"


@then('each market should be assigned to exactly one ship')
def check_no_market_duplicates(context):
    """Verify markets are not duplicated across ships"""
    result = context['scout_result']

    all_assigned_markets = []
    for ship, markets in result.assignments.items():
        all_assigned_markets.extend(markets)

    # Check for duplicates
    unique_markets = set(all_assigned_markets)
    assert len(all_assigned_markets) == len(unique_markets), \
        f"Markets assigned multiple times: {len(all_assigned_markets)} total, {len(unique_markets)} unique"

    # Check all markets were assigned
    expected_markets = set(context['markets'])
    assert unique_markets == expected_markets, \
        f"Market mismatch. Expected: {expected_markets}, Got: {unique_markets}"


@then('each ship should have at least one market assigned')
def check_all_ships_have_markets(context):
    """Verify every ship got assigned markets"""
    result = context['scout_result']

    for ship in context['scout_ships']:
        assigned_markets = result.assignments.get(ship, [])
        assert len(assigned_markets) > 0, \
            f"Ship {ship} has no markets assigned"
