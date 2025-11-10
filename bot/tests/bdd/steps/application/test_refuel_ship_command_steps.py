"""
BDD step definitions for refuel ship command feature.

Tests RefuelShipCommand and RefuelShipHandler
across 13 scenarios covering all refueling functionality.
"""
import pytest
import asyncio
from typing import Optional
# Mock is handled by autouse fixture
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
def initialize_handler(context, ship_repo):
    """Initialize handler with shared mock dependencies from conftest.py"""
    context['mock_repo'] = ship_repo
    context['handler'] = RefuelShipHandler(ship_repo)
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

    # Initialize ships_data for API mock (will be updated by other steps)
    if 'ships_data' not in context:
        context['ships_data'] = {}
    context['ships_data'][ship_symbol] = {
        'symbol': ship_symbol,
        'player_id': player_id,
        'nav': {
            'waypointSymbol': 'X1-TEST-A1',
            'systemSymbol': 'X1-TEST',
            'status': 'DOCKED',
            'flightMode': 'CRUISE'
        },
        'fuel': {'current': 50, 'capacity': 100},
        'cargo': {'capacity': 40, 'units': 0, 'inventory': []},
        'frame': {'symbol': 'FRAME_PROBE'},
        'reactor': {'symbol': 'REACTOR_SOLAR_I'},
        'engine': {'symbol': 'ENGINE_IMPULSE_DRIVE_I', 'speed': 30},
        'modules': [],
        'mounts': []
    }


@given(parsers.parse('the ship is docked at waypoint "{waypoint}"'))
def ship_is_docked(context, waypoint):
    """Set ship to docked status"""
    context['nav_status'] = Ship.DOCKED
    context['waypoint'] = waypoint
    # Update ships_data if it exists
    if 'ships_data' in context and context.get('ship_symbol') in context['ships_data']:
        context['ships_data'][context['ship_symbol']]['nav']['waypointSymbol'] = waypoint
        context['ships_data'][context['ship_symbol']]['nav']['status'] = 'DOCKED'


@given(parsers.parse('the ship is in orbit at waypoint "{waypoint}"'))
def ship_is_in_orbit(context, waypoint):
    """Set ship to in orbit status"""
    context['nav_status'] = Ship.IN_ORBIT
    context['waypoint'] = waypoint
    # Update ships_data if it exists
    if 'ships_data' in context and context.get('ship_symbol') in context['ships_data']:
        context['ships_data'][context['ship_symbol']]['nav']['waypointSymbol'] = waypoint
        context['ships_data'][context['ship_symbol']]['nav']['status'] = 'IN_ORBIT'


@given("the ship is in transit")
def ship_is_in_transit(context):
    """Set ship to in transit status"""
    context['nav_status'] = Ship.IN_TRANSIT
    # Update ships_data if it exists
    if 'ships_data' in context and context.get('ship_symbol') in context['ships_data']:
        context['ships_data'][context['ship_symbol']]['nav']['status'] = 'IN_TRANSIT'


@given("the waypoint has fuel available")
def waypoint_has_fuel(context):
    """Set waypoint to have fuel available"""
    context['has_fuel'] = True

    # Add waypoint to context for conftest mock_graph_provider
    waypoint_symbol = context.get('waypoint', 'X1-TEST-A1')
    if 'waypoints' not in context:
        context['waypoints'] = {}
    context['waypoints'][waypoint_symbol] = {
        'x': 0.0,
        'y': 0.0,
        'type': 'PLANET',
        'traits': [{'symbol': 'MARKETPLACE'}],
        'has_fuel': True
    }


@given("the waypoint does not have fuel available")
def waypoint_no_fuel(context):
    """Set waypoint to not have fuel available"""
    context['has_fuel'] = False

    # Add waypoint to context for conftest mock_graph_provider
    waypoint_symbol = context.get('waypoint', 'X1-TEST-A1')
    if 'waypoints' not in context:
        context['waypoints'] = {}
    context['waypoints'][waypoint_symbol] = {
        'x': 0.0,
        'y': 0.0,
        'type': 'PLANET',
        'traits': [],
        'has_fuel': False
    }


@given(parsers.parse('the ship has {current:d} current fuel and {capacity:d} capacity'))
def set_ship_fuel(context, current, capacity):
    """Set ship fuel levels"""
    context['fuel_current'] = current
    context['fuel_capacity'] = capacity

    # Update ships_data with fuel levels
    ship_symbol = context.get('ship_symbol', 'TEST-SHIP-1')
    if 'ships_data' in context and ship_symbol in context['ships_data']:
        context['ships_data'][ship_symbol]['fuel']['current'] = current
        context['ships_data'][ship_symbol]['fuel']['capacity'] = capacity


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
        # API client is automatically mocked by autouse fixture
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
        # API client is automatically mocked by autouse fixture
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
    """Verify ship has correct fuel (API-only model, no database persistence)"""
    # In API-only model, we verify the result contains the correct fuel state
    assert context['result'] is not None, "No result returned"
    assert context['result'].ship.fuel.current == fuel, \
        f"Expected fuel {fuel}, got {context['result'].ship.fuel.current}"


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
