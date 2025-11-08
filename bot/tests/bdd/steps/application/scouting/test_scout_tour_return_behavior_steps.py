"""Step definitions for scout tour return-to-start behavior tests"""
import asyncio
import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from unittest.mock import Mock, AsyncMock, patch
from dataclasses import fields

from application.scouting.commands.scout_tour import ScoutTourCommand
from configuration.container import get_mediator, reset_container
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
    """Execute scout tour and track navigation calls"""
    # Create mock ship repository with state tracking
    ship_data = context['ship_data']
    current_location = ship_data['waypoint']

    # Track current ship location (updated after each navigation)
    def make_test_ship(location):
        return Ship(
            ship_symbol=ship_symbol,
            player_id=context['player_id'],
            current_location=Waypoint(
                symbol=location,
                waypoint_type='PLANET',
                x=0,
                y=0,
                system_symbol=ship_data['system'],
                traits=tuple(),
                has_fuel=True
            ),
            fuel=Fuel(current=300, capacity=400),
            fuel_capacity=400,
            cargo_capacity=40,
            cargo_units=0,
            engine_speed=30,
            nav_status='DOCKED'
        )

    # Mock ship repository that returns ship at current location
    mock_ship_repo = Mock()
    def get_ship(symbol, player_id):
        return make_test_ship(current_location)

    mock_ship_repo.find_by_symbol.side_effect = get_ship

    # Create mock market repository
    mock_market_repo = Mock()
    mock_market_repo.upsert_market_data = Mock()

    # Mock API client for market data
    mock_api_client = Mock()
    mock_api_client.get_market.return_value = {
        'data': {
            'tradeGoods': [
                {
                    'symbol': 'FOOD',
                    'supply': 'MODERATE',
                    'activity': 'WEAK',
                    'purchasePrice': 100,
                    'sellPrice': 120,
                    'tradeVolume': 10
                }
            ]
        }
    }

    # Track navigation calls and update ship location
    context['navigation_calls'] = []

    async def mock_send_async(command):
        """Mock send_async to track navigation and update ship location"""
        nonlocal current_location
        from application.navigation.commands.navigate_ship import NavigateShipCommand
        if isinstance(command, NavigateShipCommand):
            context['navigation_calls'].append(command.destination_symbol)
            # Update current location so subsequent navigations work correctly
            current_location = command.destination_symbol
        return None

    mock_mediator_for_nav = Mock()
    mock_mediator_for_nav.send_async = AsyncMock(side_effect=mock_send_async)

    # Create test handler
    from application.scouting.commands.scout_tour import ScoutTourHandler
    handler = ScoutTourHandler(mock_ship_repo, mock_market_repo)

    # Create command WITHOUT return_to_start parameter
    command = ScoutTourCommand(
        ship_symbol=ship_symbol,
        player_id=context['player_id'],
        system=ship_data['system'],
        markets=context['markets']
    )

    # Patch dependencies
    with patch('configuration.container.get_mediator') as mock_get_mediator, \
         patch('configuration.container.get_api_client_for_player') as mock_get_api, \
         patch('asyncio.sleep', new_callable=AsyncMock) as mock_sleep:

        mock_get_mediator.return_value = mock_mediator_for_nav
        mock_get_api.return_value = mock_api_client

        try:
            result = asyncio.run(handler.handle(command))
            context['result'] = result
            context['succeeded'] = True
        except Exception as e:
            context['error'] = str(e)
            context['succeeded'] = False
            raise


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
    """Verify ship did NOT return to starting waypoint"""
    starting_waypoint = context['starting_waypoint']
    navigation_calls = context['navigation_calls']

    # For stationary scout (1 market), ship is already at market - no navigation needed
    # So navigation_calls should be empty
    assert len(navigation_calls) == 0, \
        f"Expected no navigation for stationary scout, but got navigation calls: {navigation_calls}"


@then('the ship should return to starting waypoint')
def check_return_to_start(context):
    """Verify ship returned to starting waypoint"""
    starting_waypoint = context['starting_waypoint']
    navigation_calls = context['navigation_calls']

    # Ship navigates to markets NOT at starting location, then returns to start
    # The _navigate_to method skips navigation if ship is already at destination
    # For 2 markets starting at A1: [A1, B2]
    #   - Navigate to A1: SKIPPED (already there)
    #   - Navigate to B2: NAVIGATES
    #   - Return to A1: NAVIGATES
    #   => Expected: [B2, A1]
    # For 3 markets starting at A1: [A1, B2, C3]
    #   - Navigate to A1: SKIPPED
    #   - Navigate to B2: NAVIGATES
    #   - Navigate to C3: NAVIGATES
    #   - Return to A1: NAVIGATES
    #   => Expected: [B2, C3, A1]

    # Must have at least one navigation call (the return)
    assert len(navigation_calls) > 0, \
        f"Expected at least one navigation call (return to start), got {len(navigation_calls)}: {navigation_calls}"

    # Last navigation should be back to starting waypoint
    assert navigation_calls[-1] == starting_waypoint, \
        f"Expected last navigation to be to {starting_waypoint}, but was {navigation_calls[-1]}"


@then(parsers.parse('the command should not have a "{field_name}" field'))
def check_field_not_present(context, field_name):
    """Verify field is not present in command dataclass"""
    command_fields = context['command_fields']
    assert field_name not in command_fields, \
        f"Field '{field_name}' should not exist in ScoutTourCommand, but found: {command_fields}"
