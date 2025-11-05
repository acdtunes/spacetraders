"""
BDD step definitions for refuel ship command feature.

Tests RefuelShipCommand and RefuelShipHandler
across 13 scenarios covering all refueling functionality.
"""
import pytest
import asyncio
from typing import Optional
from unittest.mock import patch
from pytest_bdd import scenarios, given, when, then, parsers

from application.navigation.commands.refuel_ship import (
    RefuelShipCommand,
    RefuelShipHandler,
    RefuelShipResponse
)
from domain.shared.ship import Ship, InvalidNavStatusError
from domain.shared.exceptions import ShipNotFoundError
from domain.shared.value_objects import Waypoint, Fuel

# Load all scenarios from the feature file
scenarios('../../features/application/refuel_ship_command.feature')


# ============================================================================
# Mock implementations are now in conftest.py as shared fixtures
# ============================================================================


# ============================================================================
# Helper Functions
# ============================================================================

def create_test_ship(
    ship_symbol: str = "TEST-SHIP-1",
    player_id: int = 1,
    nav_status: str = Ship.DOCKED,
    fuel_current: int = 50,
    fuel_capacity: int = 100,
    has_fuel: bool = True,
    cargo_capacity: int = 40,
    cargo_units: int = 0,
    engine_speed: int = 30
) -> Ship:
    """Helper to create test ship"""
    waypoint = Waypoint(
        symbol="X1-TEST-AB12",
        x=0.0,
        y=0.0,
        system_symbol="X1-TEST",
        waypoint_type="PLANET",
        has_fuel=has_fuel
    )

    fuel = Fuel(current=fuel_current, capacity=fuel_capacity)

    return Ship(
        ship_symbol=ship_symbol,
        player_id=player_id,
        current_location=waypoint,
        fuel=fuel,
        fuel_capacity=fuel_capacity,
        cargo_capacity=cargo_capacity,
        cargo_units=cargo_units,
        engine_speed=engine_speed,
        nav_status=nav_status
    )


# ============================================================================
# Given Steps - Setup
# ============================================================================

@given("the refuel ship command handler is initialized")
def initialize_handler(context, mock_ship_repo, mock_api):
    """Initialize handler with shared mock dependencies from conftest.py"""
    context['mock_repo'] = mock_ship_repo
    context['mock_api'] = mock_api
    context['handler'] = RefuelShipHandler(mock_ship_repo)
    context['exception'] = None
    context['result'] = None


@given(parsers.parse('a ship "{ship_symbol}" owned by player {player_id:d}'))
def create_ship(context, ship_symbol, player_id):
    """Create a ship in the repository"""
    context['ship_symbol'] = ship_symbol
    context['player_id'] = player_id
    context['nav_status'] = Ship.DOCKED
    context['fuel_current'] = 50
    context['fuel_capacity'] = 100
    context['has_fuel'] = True
    context['cargo_capacity'] = 40
    context['cargo_units'] = 0
    context['engine_speed'] = 30


@given(parsers.parse('the ship is docked at waypoint "{waypoint}"'))
def ship_is_docked(context, waypoint):
    """Set ship to docked status"""
    context['nav_status'] = Ship.DOCKED
    context['waypoint'] = waypoint


@given(parsers.parse('the ship is in orbit at waypoint "{waypoint}"'))
def ship_is_in_orbit(context, waypoint):
    """Set ship to in orbit status"""
    context['nav_status'] = Ship.IN_ORBIT
    context['waypoint'] = waypoint


@given("the ship is in transit")
def ship_is_in_transit(context):
    """Set ship to in transit status"""
    context['nav_status'] = Ship.IN_TRANSIT


@given("the waypoint has fuel available")
def waypoint_has_fuel(context):
    """Set waypoint to have fuel available"""
    context['has_fuel'] = True


@given("the waypoint does not have fuel available")
def waypoint_no_fuel(context):
    """Set waypoint to not have fuel available"""
    context['has_fuel'] = False


@given(parsers.parse('the ship has {current:d} current fuel and {capacity:d} capacity'))
def set_ship_fuel(context, current, capacity):
    """Set ship fuel levels"""
    context['fuel_current'] = current
    context['fuel_capacity'] = capacity

    # Create the ship with all accumulated context
    ship = create_test_ship(
        ship_symbol=context.get('ship_symbol', 'TEST-SHIP-1'),
        player_id=context.get('player_id', 1),
        nav_status=context.get('nav_status', Ship.DOCKED),
        fuel_current=current,
        fuel_capacity=capacity,
        has_fuel=context.get('has_fuel', True),
        cargo_capacity=context.get('cargo_capacity', 40),
        cargo_units=context.get('cargo_units', 0),
        engine_speed=context.get('engine_speed', 30)
    )
    context['mock_repo'].create(ship)


@given(parsers.parse('the ship has cargo capacity {cargo_capacity:d} and cargo units {cargo_units:d}'))
def set_ship_cargo(context, cargo_capacity, cargo_units):
    """Set ship cargo properties"""
    context['cargo_capacity'] = cargo_capacity
    context['cargo_units'] = cargo_units


@given(parsers.parse('the ship has engine speed {engine_speed:d}'))
def set_ship_engine_speed(context, engine_speed):
    """Set ship engine speed"""
    context['engine_speed'] = engine_speed


@given(parsers.parse('the API will return refuel cost of {cost:d} credits'))
def set_api_refuel_cost(context, cost):
    """Configure API to return specific cost"""
    context['mock_api'].refuel_cost = cost


@given("the API will not return cost information")
def api_no_cost(context):
    """Configure API to not return cost - set cost to None"""
    # Store original cost and set to indicate no cost
    context['_original_cost'] = context['mock_api'].refuel_cost
    context['mock_api'].refuel_cost = None


# ============================================================================
# When Steps - Actions
# ============================================================================

@when(parsers.parse('I execute refuel command for ship "{ship_symbol}" and player {player_id:d}'))
def execute_refuel_command(context, ship_symbol, player_id):
    """Execute refuel command"""
    command = RefuelShipCommand(ship_symbol=ship_symbol, player_id=player_id)
    try:
        with patch('configuration.container.get_api_client_for_player', return_value=context['mock_api']):
            context['result'] = asyncio.run(context['handler'].handle(command))
        context['exception'] = None
    except Exception as e:
        context['exception'] = e
        context['result'] = None


@when(parsers.parse('I execute refuel command for ship "{ship_symbol}" and player {player_id:d} with {units:d} units'))
def execute_refuel_command_with_units(context, ship_symbol, player_id, units):
    """Execute refuel command with specific units"""
    command = RefuelShipCommand(ship_symbol=ship_symbol, player_id=player_id, units=units)
    try:
        with patch('configuration.container.get_api_client_for_player', return_value=context['mock_api']):
            context['result'] = asyncio.run(context['handler'].handle(command))
        context['exception'] = None
    except Exception as e:
        context['exception'] = e
        context['result'] = None


# ============================================================================
# Then Steps - Assertions
# ============================================================================

@then("the refuel should succeed")
def check_refuel_success(context):
    """Verify refuel succeeded"""
    assert context['exception'] is None, f"Expected success but got: {context['exception']}"
    assert context['result'] is not None
    assert isinstance(context['result'], RefuelShipResponse)


@then(parsers.parse('the ship should have {fuel:d} current fuel'))
def check_ship_fuel(context, fuel):
    """Verify ship current fuel"""
    assert context['result'].ship.fuel.current == fuel


@then(parsers.parse('{fuel_added:d} units of fuel should be added'))
def check_fuel_added(context, fuel_added):
    """Verify fuel added"""
    assert context['result'].fuel_added == fuel_added


@then(parsers.parse('the cost should be {cost:d} credits'))
def check_cost(context, cost):
    """Verify refuel cost"""
    assert context['result'].cost == cost


@then(parsers.parse('the API refuel should be called for ship "{ship_symbol}"'))
def check_api_refuel_called(context, ship_symbol):
    """Verify API refuel was called"""
    assert context['mock_api'].refuel_called
    assert context['mock_api'].refuel_ship_symbol == ship_symbol


@then("the command should fail with ShipNotFoundError")
def check_ship_not_found_error(context):
    """Verify ShipNotFoundError was raised"""
    assert context['exception'] is not None
    assert isinstance(context['exception'], ShipNotFoundError)


@then("the command should fail with InvalidNavStatusError")
def check_invalid_nav_status_error(context):
    """Verify InvalidNavStatusError was raised"""
    assert context['exception'] is not None
    assert isinstance(context['exception'], InvalidNavStatusError)


@then("the command should fail with ValueError")
def check_value_error(context):
    """Verify ValueError was raised"""
    assert context['exception'] is not None
    assert isinstance(context['exception'], ValueError)


@then(parsers.parse('the error message should contain "{text}"'))
def check_error_message_contains(context, text):
    """Verify error message contains text"""
    assert context['exception'] is not None
    error_msg = str(context['exception']).lower()
    assert text.lower() in error_msg, f"Expected '{text}' in error message: {error_msg}"


@then("the ship should be docked")
def check_ship_docked(context):
    """Verify ship is docked"""
    assert context['result'].ship.nav_status == Ship.DOCKED


@then("fuel should be added")
def check_fuel_was_added(context):
    """Verify some fuel was added"""
    assert context['result'].fuel_added >= 0


@then(parsers.parse('the ship in the repository should have {fuel:d} current fuel'))
def check_repo_ship_fuel(context, fuel):
    """Verify ship in repository has correct fuel"""
    ship = context['mock_repo'].find_by_symbol(
        context.get('ship_symbol', 'TEST-SHIP-1'),
        context.get('player_id', 1)
    )
    assert ship is not None
    assert ship.fuel.current == fuel


@then("the cost should be None")
def check_cost_none(context):
    """Verify cost is None"""
    assert context['result'].cost is None


@then(parsers.parse('the ship symbol should be "{symbol}"'))
def check_ship_symbol(context, symbol):
    """Verify ship symbol"""
    assert context['result'].ship.ship_symbol == symbol


@then(parsers.parse('the ship player id should be {player_id:d}'))
def check_ship_player_id(context, player_id):
    """Verify ship player id"""
    assert context['result'].ship.player_id == player_id


@then(parsers.parse('the ship cargo capacity should be {capacity:d}'))
def check_ship_cargo_capacity(context, capacity):
    """Verify ship cargo capacity"""
    assert context['result'].ship.cargo_capacity == capacity


@then(parsers.parse('the ship cargo units should be {units:d}'))
def check_ship_cargo_units(context, units):
    """Verify ship cargo units"""
    assert context['result'].ship.cargo_units == units


@then(parsers.parse('the ship engine speed should be {speed:d}'))
def check_ship_engine_speed(context, speed):
    """Verify ship engine speed"""
    assert context['result'].ship.engine_speed == speed
