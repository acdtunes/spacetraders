"""Step definitions for jettison cargo command tests"""
import pytest
import asyncio
from pytest_bdd import scenarios, given, when, then, parsers
from unittest.mock import Mock

from application.cargo.commands.jettison_cargo import (
    JettisonCargoCommand,
    JettisonCargoHandler
)
from domain.shared.ship import Ship
from domain.shared.value_objects import Waypoint, Fuel, Cargo, CargoItem

# Load scenarios
scenarios('../../../features/application/cargo/jettison_cargo.feature')


@pytest.fixture
def context():
    """Provide shared context dictionary for test data"""
    return {}


@given(parsers.parse('a player exists with ID {player_id:d}'))
def player_exists(context, player_id):
    """Set player ID in context"""
    context['player_id'] = player_id


@given(parsers.parse('the player has agent symbol "{agent_symbol}"'))
def player_has_agent(context, agent_symbol):
    """Set agent symbol in context"""
    context['agent_symbol'] = agent_symbol


@given(parsers.parse('a ship "{ship_symbol}" exists for player {player_id:d}'))
def ship_exists(context, ship_symbol, player_id):
    """Set ship symbol in context"""
    context['ship_symbol'] = ship_symbol
    context['player_id'] = player_id


@given(parsers.parse('the ship has {units:d} units of "{cargo_symbol}" in cargo'))
def ship_has_cargo(context, units, cargo_symbol):
    """Set ship cargo state in context"""
    if 'cargo_items' not in context:
        context['cargo_items'] = {}
    context['cargo_items'][cargo_symbol] = units


@given(parsers.parse('the ship is docked at waypoint "{waypoint}"'))
def ship_is_docked(context, waypoint):
    """Set ship status to docked"""
    context['ship_nav_status'] = Ship.DOCKED
    context['ship_waypoint'] = waypoint


@given(parsers.parse('the ship is in orbit at waypoint "{waypoint}"'))
def ship_is_in_orbit(context, waypoint):
    """Set ship status to in orbit"""
    context['ship_nav_status'] = Ship.IN_ORBIT
    context['ship_waypoint'] = waypoint


@when(parsers.parse('I jettison {units:d} units of "{cargo_symbol}" from ship "{ship_symbol}"'))
def jettison_cargo_from_ship(context, units, cargo_symbol, ship_symbol):
    """Execute jettison cargo command"""
    # Setup mock API client
    mock_api_client = Mock()
    mock_api_client.jettison_cargo = Mock(return_value={
        "data": {
            "cargo": {
                "capacity": 40,
                "units": 5,
                "inventory": []
            }
        }
    })

    # Create factory that returns the mock
    def api_client_factory(player_id):
        return mock_api_client

    # Create handler with mocked API client
    handler = JettisonCargoHandler(api_client_factory)

    # Execute command
    command = JettisonCargoCommand(
        ship_symbol=ship_symbol,
        player_id=context['player_id'],
        cargo_symbol=cargo_symbol,
        units=units
    )

    try:
        result = asyncio.run(handler.handle(command))
        context['jettison_result'] = result
        context['jettison_error'] = None
    except Exception as e:
        context['jettison_result'] = None
        context['jettison_error'] = e

    # Store mock for verification
    context['mock_api_client'] = mock_api_client


@then('the jettison command should succeed')
def verify_jettison_success(context):
    """Verify jettison command succeeded"""
    assert context['jettison_error'] is None, f"Jettison command failed with error: {context['jettison_error']}"


@then(parsers.parse('the API should receive jettison request for ship "{ship_symbol}" with cargo "{cargo_symbol}" and {units:d} units'))
def verify_api_jettison_call(context, ship_symbol, cargo_symbol, units):
    """Verify API client was called with correct parameters"""
    # Verify the mock was called correctly
    context['mock_api_client'].jettison_cargo.assert_called_once_with(
        ship_symbol=ship_symbol,
        cargo_symbol=cargo_symbol,
        units=units
    )
