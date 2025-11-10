"""Step definitions for scout tour wait optimization tests"""
import asyncio
import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from unittest.mock import Mock, AsyncMock, patch

from application.scouting.commands.scout_tour import ScoutTourCommand
from configuration.container import get_mediator, reset_container
from domain.shared.ship import Ship
from domain.shared.value_objects import Waypoint, Fuel

# Load scenarios
scenarios('../../../features/application/scouting/scout_tour_wait_optimization.feature')


@pytest.fixture
def context():
    """Shared context for scenario steps"""
    reset_container()
    return {
        'player_id': 1,
        'ship_symbol': None,
        'markets': [],
        'wait_time': None,
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


# Command step

@when(parsers.parse('I execute a scout tour iteration with ship "{ship_symbol}"'))
def execute_scout_tour(context, ship_symbol):
    """Execute scout tour and verify wait behavior"""
    # Create mock ship repository
    ship_repo = Mock()

    ship_data = context['ship_data']
    test_ship = Ship(
        ship_symbol=ship_symbol,
        player_id=context['player_id'],
        current_location=Waypoint(
            symbol=ship_data['waypoint'],
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
        nav_status=ship_data['status']
    )

    ship_repo.find_by_symbol.return_value = test_ship

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

    # Create mock mediator for NavigateShipCommand and DockShipCommand
    mock_mediator_for_nav = Mock()

    async def mock_send_async(command):
        """Mock send_async to avoid actual navigation"""
        return None

    mock_mediator_for_nav.send_async = AsyncMock(side_effect=mock_send_async)

    # Create test handler
    from application.scouting.commands.scout_tour import ScoutTourHandler
    handler = ScoutTourHandler(ship_repo, mock_market_repo)

    # Create command
    command = ScoutTourCommand(
        ship_symbol=ship_symbol,
        player_id=context['player_id'],
        system=ship_data['system'],
        markets=context['markets']
    )

    # Track asyncio.sleep calls to verify wait behavior
    mock_sleep = AsyncMock()

    # Patch dependencies including asyncio.sleep
    with patch('configuration.container.get_mediator') as mock_get_mediator, \
         patch('configuration.container.get_api_client_for_player') as mock_get_api, \
         patch('asyncio.sleep', mock_sleep):

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

        # Check if sleep was called and with what duration
        if mock_sleep.called:
            # Get the duration from the first call
            sleep_duration = mock_sleep.call_args[0][0]
            context['wait_time'] = sleep_duration
        else:
            context['wait_time'] = 0


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


@then('the tour should wait 60 seconds before next iteration')
def check_tour_waited(context):
    """Verify tour waited 60 seconds"""
    assert context['wait_time'] == 60, \
        f"Expected 60 second wait for stationary scout, but wait was {context['wait_time']} seconds"


@then('the tour should not wait before next iteration')
def check_tour_did_not_wait(context):
    """Verify tour did not wait"""
    assert context['wait_time'] == 0, \
        f"Expected 0 second wait for touring scout, but wait was {context['wait_time']} seconds"
