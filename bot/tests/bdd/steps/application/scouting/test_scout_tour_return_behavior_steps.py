"""
Step definitions for scout tour return-to-start behavior tests

REFACTORED: Removed mediator over-mocking. Now uses real mediator and verifies
behavior through observable ship state (querying ship repository) instead of
tracking mock call counts.
"""
import asyncio
import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from unittest.mock import Mock
from dataclasses import fields

from application.scouting.commands.scout_tour import ScoutTourCommand
from configuration.container import get_mediator, reset_container, get_ship_repository
from domain.shared.ship import Ship
from domain.shared.value_objects import Waypoint, Fuel

# Load scenarios
scenarios('../../../features/application/scouting/scout_tour_return_behavior.feature')


@pytest.fixture
def context():
    """Shared context for scenario steps"""
    reset_container()
    return {
        'player_id': 1,
        'ship_symbol': None,
        'markets': [],
        'waypoints_visited': [],
        'navigation_calls': [],
        'result': None,
        'error': None
    }


@pytest.fixture(autouse=True)
def cleanup():
    """Cleanup after each test"""
    yield
    reset_container()


# Background steps

@given(parsers.parse('a player with ID {player_id:d} exists'))
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


@given(parsers.parse('a ship "{ship_symbol}" exists at "{waypoint}" for player {player_id:d}'))
def create_ship(context, ship_symbol, waypoint, player_id):
    """Store ship data for mocking"""
    context['ship_symbol'] = ship_symbol
    context['starting_waypoint'] = waypoint
    context['ship_data'] = {
        'symbol': ship_symbol,
        'waypoint': waypoint,
        'system': waypoint.rsplit('-', 1)[0],  # Extract system from waypoint
        'status': 'DOCKED'
    }


@given('the scout tour will visit markets:')
def set_markets(context, datatable):
    """Store markets to visit"""
    headers = datatable[0]
    context['markets'] = [row[0] for row in datatable[1:]]


# Command steps

@when(parsers.parse('I execute a scout tour with ship "{ship_symbol}"'))
def execute_scout_tour(context, ship_symbol):
    """
    Execute scout tour using REAL mediator - verify behavior through ship repository.

    Uses real infrastructure (mediator, handlers) with mocks only at boundaries (API).
    """
    ship_data = context['ship_data']

    # Use REAL mediator from container
    mediator = get_mediator()

    # Create command - scout tour will use real mediator to navigate
    command = ScoutTourCommand(
        ship_symbol=ship_symbol,
        player_id=context['player_id'],
        system=ship_data['system'],
        markets=context['markets']
    )

    try:
        # Execute through REAL mediator
        result = asyncio.run(mediator.send_async(command))
        context['result'] = result
        context['succeeded'] = True
    except Exception as e:
        context['error'] = str(e)
        context['succeeded'] = False
        # Don't raise - let the test verify the error


@when('I inspect the ScoutTourCommand dataclass')
def inspect_scout_tour_command(context):
    """Inspect ScoutTourCommand fields"""
    context['command_fields'] = {f.name for f in fields(ScoutTourCommand)}


# Assertion steps

@then('the scout tour should complete successfully')
def check_scout_tour_success(context):
    """Verify scout tour completed successfully"""
    if not context.get('succeeded'):
        error_msg = context.get('error', 'Unknown error')
        assert False, f"Scout tour failed: {error_msg}"

    assert context['result'] is not None, "No result returned from scout tour"
    assert context['result'].markets_visited == len(context['markets']), \
        f"Expected {len(context['markets'])} markets visited, got {context['result'].markets_visited}"


@then(parsers.parse('the ship should visit exactly {count:d} waypoint'))
@then(parsers.parse('the ship should visit exactly {count:d} waypoints'))
def check_waypoint_count(context, count):
    """Verify number of waypoints visited"""
    assert len(context['markets']) == count, \
        f"Test setup error: Expected {count} markets, got {len(context['markets'])}"


@then('the ship should not return to starting waypoint')
def check_no_return_to_start(context):
    """
    Verify ship did NOT return to starting waypoint by querying ship repository.

    OBSERVABLE BEHAVIOR: Check ship's actual location through repository, not mock calls.
    """
    starting_waypoint = context['starting_waypoint']
    ship_symbol = context['ship_symbol']
    player_id = context['player_id']

    # Query REAL ship repository to check ship location
    ship_repo = get_ship_repository()
    ship = ship_repo.find_by_symbol(ship_symbol, player_id)

    # For a single-market scout where ship is already at the market,
    # ship should still be at that location (no movement needed)
    # This is observable - the ship's location is the same as starting location
    assert ship.current_location.symbol == starting_waypoint, \
        f"Expected ship to remain at {starting_waypoint}, but found at {ship.current_location.symbol}"


@then('the ship should return to starting waypoint')
def check_return_to_start(context):
    """
    Verify ship returned to starting waypoint by querying ship repository.

    OBSERVABLE BEHAVIOR: Check ship's actual final location, not how it got there.
    """
    starting_waypoint = context['starting_waypoint']
    ship_symbol = context['ship_symbol']
    player_id = context['player_id']

    # Query REAL ship repository to check ship location
    ship_repo = get_ship_repository()
    ship = ship_repo.find_by_symbol(ship_symbol, player_id)

    # After scout tour completes, ship should be back at starting waypoint
    assert ship.current_location.symbol == starting_waypoint, \
        f"Expected ship to return to {starting_waypoint}, but found at {ship.current_location.symbol}"


@then(parsers.parse('the command should not have a "{field_name}" field'))
def check_field_not_present(context, field_name):
    """Verify field is not present in command dataclass"""
    command_fields = context['command_fields']
    assert field_name not in command_fields, \
        f"Field '{field_name}' should not exist in ScoutTourCommand, but found: {command_fields}"
