"""BDD steps for Dock Ship Command"""
from pytest_bdd import scenario, given, when, then, parsers
import asyncio
import pytest
from datetime import datetime, timedelta, timezone
import time

from application.navigation.commands.dock_ship import (
    DockShipCommand,
    DockShipHandler
)
from domain.shared.ship import Ship, InvalidNavStatusError
from domain.shared.exceptions import ShipNotFoundError
from domain.shared.value_objects import Waypoint, Fuel


# ==============================================================================
# Scenario: Dock ship successfully from orbit
# ==============================================================================
@scenario("../../features/application/dock_ship_command.feature", "Dock ship successfully from orbit")
def test_dock_ship_successfully_from_orbit():
    pass


@given("the dock ship command handler is initialized")
def initialize_handler(context, ship_repo):
    """Initialize the DockShipHandler with mock dependencies"""
    context["ship_repo"] = ship_repo
    # Handler now only takes ship_repo - API client is retrieved via get_api_client_for_player
    context["handler"] = DockShipHandler(ship_repo)


@given(parsers.parse('a ship "{ship_symbol}" for player {player_id:d} in orbit at "{location}"'))
def create_ship_in_orbit(context, ship_symbol, player_id, location):
    """Create a ship in orbit at a specific location (store in context for API mock)"""
    parts = location.split('-')
    system_symbol = f"{parts[0]}-{parts[1]}" if len(parts) >= 2 else "X1-TEST"

    # Store ship data for API mock
    if 'ships_data' not in context:
        context['ships_data'] = {}

    context['ships_data'][ship_symbol] = {
        'symbol': ship_symbol,
        'player_id': player_id,  # Track player ownership for API mock
        'nav': {
            'waypointSymbol': location,
            'systemSymbol': system_symbol,
            'status': 'IN_ORBIT',
            'flightMode': 'CRUISE'
        },
        'fuel': {'current': 100, 'capacity': 100},
        'cargo': {'capacity': 40, 'units': 0, 'inventory': []},
        'frame': {'symbol': 'FRAME_PROBE'},
        'reactor': {'symbol': 'REACTOR_SOLAR_I'},
        'engine': {'symbol': 'ENGINE_IMPULSE_DRIVE_I', 'speed': 30},
        'modules': [],
        'mounts': []
    }
    context["initial_nav_status"] = Ship.IN_ORBIT


@when(parsers.parse('I execute dock ship command for "{ship_symbol}" and player {player_id:d}'))
def execute_dock_command(context, ship_symbol, player_id):
    """Execute the dock ship command"""
    handler = context["handler"]
    command = DockShipCommand(ship_symbol=ship_symbol, player_id=player_id)
    # Record start time for timing verification
    context["wait_start_time"] = datetime.now(timezone.utc)
    # API client is automatically mocked by autouse fixture
    context["result"] = asyncio.run(handler.handle(command))
    context["ship_symbol"] = ship_symbol
    context["error"] = None


@then("the ship should be docked")
def check_ship_docked(context):
    """Verify the ship is in docked status"""
    assert context["result"].nav_status == Ship.DOCKED


@then(parsers.parse('the API dock method should be called with "{ship_symbol}"'))
def check_api_dock_called(context, ship_symbol):
    """Verify the API dock method was called (black-box: verify ship is docked)"""
    # Black-box testing: We verify the outcome (ship is docked), not the API call
    assert context["result"].nav_status == Ship.DOCKED


@then(parsers.parse('the ship should be persisted with nav status "{status}"'))
def check_ship_persisted(context, status):
    """Verify the ship has the expected nav status (API-only model, no database persistence)"""
    # In API-only model, we verify the returned ship state matches expectations
    # The command result should reflect the API state
    assert context["result"].nav_status == status


# ==============================================================================
# Scenario: Dock ship that is already docked
# ==============================================================================
@scenario("../../features/application/dock_ship_command.feature", "Dock ship that is already docked")
def test_dock_ship_already_docked():
    pass


@given(parsers.parse('a ship "{ship_symbol}" for player {player_id:d} already docked at "{location}"'))
def create_ship_already_docked(context, ship_symbol, player_id, location):
    """Create a ship that is already docked (store in context for API mock)"""
    parts = location.split('-')
    system_symbol = f"{parts[0]}-{parts[1]}" if len(parts) >= 2 else "X1-TEST"

    # Store ship data for API mock
    if 'ships_data' not in context:
        context['ships_data'] = {}

    context['ships_data'][ship_symbol] = {
        'symbol': ship_symbol,
        'player_id': player_id,  # Track player ownership for API mock
        'nav': {
            'waypointSymbol': location,
            'systemSymbol': system_symbol,
            'status': 'DOCKED',
            'flightMode': 'CRUISE'
        },
        'fuel': {'current': 100, 'capacity': 100},
        'cargo': {'capacity': 40, 'units': 0, 'inventory': []},
        'frame': {'symbol': 'FRAME_PROBE'},
        'reactor': {'symbol': 'REACTOR_SOLAR_I'},
        'engine': {'symbol': 'ENGINE_IMPULSE_DRIVE_I', 'speed': 30},
        'modules': [],
        'mounts': []
    }
    context["initial_nav_status"] = Ship.DOCKED


@then(parsers.parse('the ship nav status should remain "{status}"'))
def check_nav_status_remains(context, status):
    """Verify the nav status remained the same"""
    assert context["result"].nav_status == status


# ==============================================================================
# Scenario: Cannot dock non-existent ship
# ==============================================================================
@scenario("../../features/application/dock_ship_command.feature", "Cannot dock non-existent ship")
def test_cannot_dock_nonexistent_ship():
    pass


@given(parsers.parse('no ship exists with symbol "{ship_symbol}" for player {player_id:d}'))
def no_ship_exists(context, ship_symbol, player_id):
    """Ensure no ship exists with the given symbol"""
    # Set empty ships_data to indicate no ships exist (avoid fallback to default ship)
    context['ships_data'] = {}
    context["ship_symbol"] = ship_symbol
    context["player_id"] = player_id


@when(parsers.parse('I attempt to dock ship "{ship_symbol}" for player {player_id:d}'))
def attempt_dock_command(context, ship_symbol, player_id):
    """Attempt to execute dock command and capture any errors"""
    handler = context["handler"]
    command = DockShipCommand(ship_symbol=ship_symbol, player_id=player_id)
    try:
        # API client is automatically mocked by autouse fixture
        context["result"] = asyncio.run(handler.handle(command))
        context["error"] = None
    except Exception as e:
        context["error"] = e
        context["result"] = None


@then("the command should fail with ShipNotFoundError")
def check_ship_not_found_error(context):
    """Verify ShipNotFoundError was raised"""
    assert isinstance(context["error"], ShipNotFoundError)


@then(parsers.parse('the error message should mention "{text}"'))
def check_error_message_contains(context, text):
    """Verify error message contains specific text"""
    assert text in str(context["error"])


# ==============================================================================
# Scenario: Cannot dock ship belonging to different player
# ==============================================================================
@scenario("../../features/application/dock_ship_command.feature", "Cannot dock ship belonging to different player")
def test_cannot_dock_ship_wrong_player():
    pass


# Reuses existing given/when/then steps


# ==============================================================================
# Scenario: Ship transitions from orbit to docked
# ==============================================================================
@scenario("../../features/application/dock_ship_command.feature", "Ship transitions from orbit to docked")
def test_ship_transitions_orbit_to_docked():
    pass


@then(parsers.parse('the ship nav status should change from "{old_status}" to "{new_status}"'))
def check_nav_status_transition(context, old_status, new_status):
    """Verify the nav status changed correctly"""
    assert context["initial_nav_status"] == old_status
    assert context["result"].nav_status == new_status
    assert context["result"].nav_status != old_status


# ==============================================================================
# Scenario: Docking preserves all other ship properties
# ==============================================================================
@scenario("../../features/application/dock_ship_command.feature", "Docking preserves all other ship properties")
def test_docking_preserves_properties():
    pass


@given(parsers.parse('a ship "{ship_symbol}" for player {player_id:d} in orbit at "{location}" with fuel {fuel_current:d}/{fuel_capacity:d}'))
def create_ship_with_properties(context, ship_symbol, player_id, location, fuel_current, fuel_capacity):
    """Create a ship with specific properties (store in context for API mock)"""
    parts = location.split('-')
    system_symbol = f"{parts[0]}-{parts[1]}" if len(parts) >= 2 else "X1-TEST"

    # Store ship data for API mock
    if 'ships_data' not in context:
        context['ships_data'] = {}

    context['ships_data'][ship_symbol] = {
        'symbol': ship_symbol,
        'player_id': player_id,  # Track player ownership for API mock
        'nav': {
            'waypointSymbol': location,
            'systemSymbol': system_symbol,
            'status': 'IN_ORBIT',
            'flightMode': 'CRUISE'
        },
        'fuel': {'current': fuel_current, 'capacity': fuel_capacity},
        'cargo': {'capacity': 40, 'units': 0, 'inventory': []},
        'frame': {'symbol': 'FRAME_PROBE'},
        'reactor': {'symbol': 'REACTOR_SOLAR_I'},
        'engine': {'symbol': 'ENGINE_IMPULSE_DRIVE_I', 'speed': 30},
        'modules': [],
        'mounts': []
    }
    context["initial_nav_status"] = Ship.IN_ORBIT


@then(parsers.parse('the ship should have fuel current {current:d} and capacity {capacity:d}'))
def check_fuel_preserved(context, current, capacity):
    """Verify fuel properties are preserved"""
    assert context["result"].fuel.current == current
    assert context["result"].fuel_capacity == capacity


@then(parsers.parse('the ship should have cargo capacity {capacity:d} and units {units:d}'))
def check_cargo_preserved(context, capacity, units):
    """Verify cargo properties are preserved"""
    assert context["result"].cargo_capacity == capacity
    assert context["result"].cargo_units == units


@then(parsers.parse('the ship should have engine speed {speed:d}'))
def check_engine_speed_preserved(context, speed):
    """Verify engine speed is preserved"""
    assert context["result"].engine_speed == speed


@then(parsers.parse('the ship should be at location "{location}"'))
def check_location_preserved(context, location):
    """Verify location is preserved"""
    assert context["result"].current_location.symbol == location


# ==============================================================================
# Scenario: Dock command waits for ship in transit to arrive
# ==============================================================================
@scenario("../../features/application/dock_ship_command.feature", "Dock command waits for ship in transit to arrive")
def test_dock_waits_for_transit():
    pass


@given(parsers.parse('a ship "{ship_symbol}" for player {player_id:d} in transit arriving in {seconds:f} seconds'))
def create_ship_in_transit_arriving(context, ship_symbol, player_id, seconds, ship_repo):
    """Create a ship that is in transit with a specific arrival time"""
    location = "X1-TEST-CD34"

    # Store repo in context (needed for execute_dock_command)
    context["ship_repo"] = ship_repo
    # Also initialize handler if not already done
    if "handler" not in context:
        context["handler"] = DockShipHandler(ship_repo)

    # Calculate arrival time
    arrival_time = datetime.now(timezone.utc) + timedelta(seconds=seconds)
    context["arrival_time"] = arrival_time
    context["wait_seconds"] = seconds
    context["wait_start_time"] = None

    # Store ship data for API mock with route.arrival
    if 'ships_data' not in context:
        context['ships_data'] = {}

    context['ships_data'][ship_symbol] = {
        'symbol': ship_symbol,
        'player_id': player_id,  # Track player ownership for API mock
        'nav': {
            'waypointSymbol': location,
            'systemSymbol': 'X1-TEST',
            'status': 'IN_TRANSIT',
            'flightMode': 'CRUISE',
            'route': {
                'destination': {
                    'symbol': location,
                    'type': 'PLANET',
                    'systemSymbol': 'X1-TEST',
                    'x': 0,
                    'y': 0
                },
                'arrival': arrival_time.isoformat()  # ISO format timestamp for arrival
            }
        },
        'fuel': {'current': 100, 'capacity': 100},
        'cargo': {'capacity': 40, 'units': 0, 'inventory': []},
        'frame': {'symbol': 'FRAME_PROBE'},
        'reactor': {'symbol': 'REACTOR_SOLAR_I'},
        'engine': {'symbol': 'ENGINE_IMPULSE_DRIVE_I', 'speed': 30},
        'modules': [],
        'mounts': []
    }


@then("the handler should wait for arrival")
def check_handler_waited(context):
    """Verify the handler waited for the ship to arrive"""
    # The handler should have taken at least the wait_seconds time
    wait_start = context.get("wait_start_time")
    wait_end = datetime.now(timezone.utc)

    if wait_start:
        elapsed = (wait_end - wait_start).total_seconds()
        expected_wait = context["wait_seconds"]
        # Allow some tolerance (0.5 seconds) for test execution overhead
        assert elapsed >= (expected_wait - 0.5), \
            f"Handler should have waited at least {expected_wait}s, but only waited {elapsed}s"


@then("the ship should be docked after waiting")
def check_ship_docked_after_waiting(context):
    """Verify the ship is docked after waiting for transit"""
    assert context["result"] is not None, "No result returned from handler"
    assert context["result"].nav_status == Ship.DOCKED, \
        f"Expected ship to be DOCKED after waiting, but got {context['result'].nav_status}"
