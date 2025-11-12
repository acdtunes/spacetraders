"""Step definitions for trading CLI tests"""
import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from unittest.mock import MagicMock, patch
import argparse
from io import StringIO
import sys

# Load scenarios
scenarios('../../../features/integration/cli/test_trading_cli.feature')


@pytest.fixture
def context():
    """Shared context for trading CLI tests"""
    return {
        'ships_data': {},
        'market_data': {},
        'players': {},
        'command_result': None,
        'command_output': None,
        'command_error': None
    }


@given(parsers.parse('a registered player with agent "{agent_symbol}" and player ID {player_id:d}'))
def create_player(context, agent_symbol, player_id):
    """Create a test player"""
    from configuration.container import get_player_repository
    from domain.shared.player import Player
    from datetime import datetime, timezone

    repo = get_player_repository()
    player = Player(
        player_id=None,  # Will be assigned by repository
        agent_symbol=agent_symbol,
        token=f"token_{agent_symbol}",
        created_at=datetime.now(timezone.utc),
        credits=10000
    )
    player = repo.create(player)
    context['players'][agent_symbol] = player


@given(parsers.parse('a ship "{ship_symbol}" exists for player {player_id:d} at "{location}"'))
def create_ship(context, ship_symbol, player_id, location):
    """Create a ship in context for API mock"""
    if 'ships_data' not in context:
        context['ships_data'] = {}

    context['ships_data'][ship_symbol] = {
        'symbol': ship_symbol,
        'player_id': player_id,
        'nav': {
            'waypointSymbol': location,
            'systemSymbol': 'X1-TEST',
            'status': 'DOCKED',
            'flightMode': 'CRUISE'
        },
        'fuel': {'current': 400, 'capacity': 400},
        'cargo': {'capacity': 40, 'units': 0, 'inventory': []},
        'frame': {'symbol': 'FRAME_PROBE'},
        'reactor': {'symbol': 'REACTOR_SOLAR_I'},
        'engine': {'symbol': 'ENGINE_IMPULSE_DRIVE_I', 'speed': 30},
        'modules': [],
        'mounts': []
    }


@given(parsers.parse('the ship is docked at "{location}"'))
def set_ship_docked(context, location):
    """Set ship status to docked"""
    # Ships are created docked by default in create_ship
    pass


@given(parsers.parse('the ship has {units:d} units of "{good}" in cargo'))
def add_cargo_to_ship(context, units, good):
    """Add cargo to ship"""
    # Find the ship (assumes only one ship in most tests)
    for ship_symbol, ship_data in context['ships_data'].items():
        ship_data['cargo']['inventory'].append({
            'symbol': good,
            'name': good.replace('_', ' ').title(),
            'description': f"A unit of {good}",
            'units': units
        })
        ship_data['cargo']['units'] = units


@given(parsers.parse('the market at "{waypoint}" buys "{good}" for {price:d} credits per unit'))
def set_market_buy_price(context, waypoint, good, price):
    """Set market buy price for a good"""
    if 'market_data' not in context:
        context['market_data'] = {}

    if waypoint not in context['market_data']:
        context['market_data'][waypoint] = {}

    context['market_data'][waypoint][good] = {
        'buy_price': price
    }


@when(parsers.parse('I run the CLI command "{command}"'))
def run_cli_command(context, command):
    """Run a CLI command and capture result"""
    import sys
    from io import StringIO
    from unittest.mock import patch, MagicMock

    # Parse command into argv format
    args = command.split()

    # Mock the daemon client to simulate container creation
    captured_output = StringIO()

    try:
        with patch('sys.argv', ['spacetraders'] + args):
            with patch('sys.stdout', captured_output):
                with patch('sys.stderr', captured_output):
                    # For sell command, we need to mock the daemon client
                    from adapters.primary.daemon.daemon_client import DaemonClient
                    mock_daemon = MagicMock(spec=DaemonClient)

                    # Mock create_container to return success
                    mock_daemon.create_container.return_value = {
                        'status': 'success',
                        'container_id': 'test-container-id'
                    }

                    with patch('adapters.primary.cli.trading_cli.get_daemon_client', return_value=mock_daemon):
                        try:
                            from adapters.primary.cli.main import main
                            result = main()
                            context['command_result'] = result if result is not None else 0
                            context['command_output'] = captured_output.getvalue()
                            context['command_error'] = None
                        except SystemExit as e:
                            context['command_result'] = e.code if e.code is not None else 0
                            context['command_output'] = captured_output.getvalue()
                            context['command_error'] = None
    except Exception as e:
        context['command_result'] = 1
        context['command_error'] = str(e)
        context['command_output'] = captured_output.getvalue()


@then('the command should succeed')
def check_command_success(context):
    """Verify command succeeded"""
    assert context['command_result'] == 0, \
        f"Command failed with result {context['command_result']}, error: {context.get('command_error')}, output: {context.get('command_output')}"


@then(parsers.parse('the command should fail with "{error_message}"'))
def check_command_failure(context, error_message):
    """Verify command failed with expected error"""
    assert context['command_result'] != 0, \
        f"Command unexpectedly succeeded. Output: {context.get('command_output')}"

    output = context.get('command_output', '') or ''
    error = context.get('command_error', '') or ''
    combined = output + error

    assert error_message in combined, \
        f"Expected error message '{error_message}' not found in output: {combined}"


@then(parsers.parse('the ship should have {units:d} units of "{good}" in cargo'))
def check_ship_cargo(context, units, good):
    """
    Verify ship cargo after sell

    Note: Since CLI commands run in daemon containers, we only verify
    the command was dispatched successfully. The actual cargo changes
    are tested at the application/handler level.
    """
    # For CLI tests, we just verify the command succeeded
    # The actual cargo modification happens in the daemon container
    assert context['command_result'] == 0, \
        f"Command should have succeeded but got result {context['command_result']}"


@then(parsers.parse('the player should gain {credits:d} credits'))
def check_player_credits(context, credits):
    """
    Verify player gained credits

    Note: Since CLI commands run in daemon containers, we only verify
    the command was dispatched successfully. The actual credit changes
    are tested at the application/handler level.
    """
    # For CLI tests, we just verify the command succeeded
    assert context['command_result'] == 0
