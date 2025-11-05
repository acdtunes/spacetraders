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
    """Create ships in the database"""
    from domain.shared.ship import Ship
    from domain.shared.value_objects import Waypoint, Fuel

    headers = datatable[0]
    for row in datatable[1:]:
        row_dict = dict(zip(headers, row))
        ship_symbol = row_dict['ship_symbol']
        waypoint = row_dict['waypoint']
        status = row_dict['status']

        # Use provided system symbol
        system_symbol = system

        # Create ship entity
        ship = Ship(
            ship_symbol=ship_symbol,
            player_id=context['player_id'],
            current_location=Waypoint(
                symbol=waypoint,
                waypoint_type='PLANET',
                x=0,
                y=0,
                system_symbol=system_symbol,
                traits=tuple(),
                has_fuel=True
            ),
            fuel=Fuel(current=300, capacity=400),
            fuel_capacity=400,
            cargo_capacity=40,
            cargo_units=0,
            engine_speed=30,
            nav_status=status
        )

        context['ship_repo'].create(ship)
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

    # Mock graph provider
    mock_graph = Mock()
    from tests.fixtures.graph_fixtures import get_mock_graph_for_system
    mock_graph.get_graph.return_value = get_mock_graph_for_system(context['system'])

    # Mock daemon client to track container creation
    mock_daemon = Mock()
    created_containers = []

    def mock_create_container(config):
        container_id = f"scout-tour-{config['config']['params']['ship_symbol']}-mock"
        created_containers.append({
            'container_id': container_id,
            'ship': config['config']['params']['ship_symbol'],
            'markets': config['config']['params']['markets']
        })
        return {'container_id': container_id}

    mock_daemon.create_container.side_effect = mock_create_container

    # Patch at container level
    with patch('configuration.container.get_graph_provider_for_player') as mock_graph_fn, \
         patch('configuration.container.get_daemon_client') as mock_daemon_fn:

        mock_graph_fn.return_value = mock_graph
        mock_daemon_fn.return_value = mock_daemon

        # Create and execute command
        command = ScoutMarketsCommand(
            ship_symbols=context['scout_ships'],
            player_id=context['player_id'],
            system=context['system'],
            markets=context['markets'],
            iterations=1,
            return_to_start=False
        )

        try:
            result = asyncio.run(context['mediator'].send_async(command))
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
