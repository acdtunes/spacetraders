"""
BDD step definitions for Orbit Ship Command feature.

Tests OrbitShipCommand and OrbitShipHandler application layer components
across 8 scenarios covering success cases, error handling, and state management.
"""
import pytest
import asyncio
from pytest_bdd import scenarios, given, when, then, parsers
from unittest.mock import patch

from application.navigation.commands.orbit_ship import (
    OrbitShipCommand,
    OrbitShipHandler
)
from domain.shared.ship import Ship, InvalidNavStatusError
from domain.shared.exceptions import ShipNotFoundError
from domain.shared.value_objects import Waypoint, Fuel

# Load all scenarios from the feature file
scenarios('../../features/application/orbit_ship_command.feature')


# ============================================================================
# Given Steps - Setup
# ============================================================================

@given("the orbit ship command system is initialized")
def orbit_system_initialized(context, mock_ship_repo, mock_api):
    """Initialize the orbit command system"""
    context['ship_repo'] = mock_ship_repo
    context['api'] = mock_api
    # Handler now only takes ship_repo - API client is retrieved via get_api_client_for_player
    context['handler'] = OrbitShipHandler(mock_ship_repo)
    context['initialized'] = True


@given(parsers.parse('a ship "{ship_symbol}" for player {player_id:d} with status "{status}"'))
def create_ship_with_status(context, ship_symbol, player_id, status):
    """Create a ship with specific status"""
    waypoint = Waypoint(
        symbol="X1-TEST-AB12",
        x=0.0,
        y=0.0,
        system_symbol="X1-TEST",
        waypoint_type="PLANET"
    )
    fuel = Fuel(current=100, capacity=100)

    ship = Ship(
        ship_symbol=ship_symbol,
        player_id=player_id,
        current_location=waypoint,
        fuel=fuel,
        fuel_capacity=100,
        cargo_capacity=40,
        cargo_units=0,
        engine_speed=30,
        nav_status=status
    )
    context['ship_repo'].create(ship)
    context['test_ship'] = ship


@given(parsers.parse('a ship "{ship_symbol}" for player {player_id:d} with the following properties:'), target_fixture='ship_with_properties')
def create_ship_with_properties(context, ship_symbol, player_id, datatable):
    """Create a ship with properties from table"""
    # Parse the table to get properties
    # datatable is a list of lists: [['property', 'value'], ['nav_status', 'DOCKED'], ...]
    props = {}
    for i, row in enumerate(datatable):
        if i == 0:  # Skip header row
            continue
        props[row[0]] = row[1]

    # Create waypoint
    location = props.get('location', 'X1-TEST-AB12')
    waypoint = Waypoint(
        symbol=location,
        x=0.0,
        y=0.0,
        system_symbol="X1-TEST",
        waypoint_type="PLANET"
    )

    # Create fuel
    fuel = Fuel(
        current=int(props.get('fuel_current', 100)),
        capacity=int(props.get('fuel_capacity', 100))
    )

    # Create ship
    ship = Ship(
        ship_symbol=ship_symbol,
        player_id=player_id,
        current_location=waypoint,
        fuel=fuel,
        fuel_capacity=int(props.get('fuel_capacity', 100)),
        cargo_capacity=int(props.get('cargo_capacity', 40)),
        cargo_units=int(props.get('cargo_units', 0)),
        engine_speed=int(props.get('engine_speed', 30)),
        nav_status=props.get('nav_status', 'DOCKED')
    )
    context['ship_repo'].create(ship)
    context['test_ship'] = ship
    context['original_properties'] = props


@given(parsers.parse('no ship exists with symbol "{ship_symbol}"'))
def no_ship_exists(context, ship_symbol):
    """Ensure no ship exists with given symbol"""
    # Ship repository is already empty, just note it
    context['nonexistent_ship'] = ship_symbol


# ============================================================================
# When Steps - Actions
# ============================================================================

@when(parsers.parse('I execute orbit command for ship "{ship_symbol}" and player {player_id:d}'))
def execute_orbit_command(context, ship_symbol, player_id):
    """Execute orbit command"""
    command = OrbitShipCommand(ship_symbol=ship_symbol, player_id=player_id)
    try:
        # Patch get_api_client_for_player at the container module level
        with patch('configuration.container.get_api_client_for_player', return_value=context['api']):
            result = asyncio.run(context['handler'].handle(command))
        context['result'] = result
        context['error'] = None
    except Exception as e:
        context['error'] = e
        context['result'] = None


@when(parsers.parse('I attempt to orbit ship "{ship_symbol}" for player {player_id:d}'))
def attempt_orbit_command(context, ship_symbol, player_id):
    """Attempt to orbit ship (may fail)"""
    execute_orbit_command(context, ship_symbol, player_id)


# ============================================================================
# Then Steps - Assertions
# ============================================================================

@then("the command should succeed")
def check_command_succeeded(context):
    """Verify command succeeded"""
    assert context['error'] is None, f"Expected success but got error: {context['error']}"
    assert context['result'] is not None, "Expected result but got None"


@then(parsers.parse('the ship status should be "{expected_status}"'))
def check_ship_status(context, expected_status):
    """Verify ship status"""
    assert context['result'] is not None
    assert context['result'].nav_status == expected_status, \
        f"Expected status {expected_status}, got {context['result'].nav_status}"


@then(parsers.parse('the ship status should not be "{status}"'))
def check_ship_status_not(context, status):
    """Verify ship status is not the given status"""
    assert context['result'] is not None
    assert context['result'].nav_status != status, \
        f"Expected status to not be {status}, but it is"


@then(parsers.parse('the API orbit endpoint should be called with "{ship_symbol}"'))
def check_api_called(context, ship_symbol):
    """Verify API was called correctly"""
    assert context['api'].orbit_called, "API orbit was not called"
    assert context['api'].orbit_ship_symbol == ship_symbol, \
        f"Expected API call with {ship_symbol}, got {context['api'].orbit_ship_symbol}"


@then("the ship should be updated in the repository")
def check_ship_updated(context):
    """Verify ship state is correct (API-only model, no database persistence)"""
    # In API-only model, we verify the returned ship state is correct
    # The result should have the updated nav status
    assert context['result'] is not None, "No result returned"
    assert context['result'].nav_status == Ship.IN_ORBIT, \
        f"Expected IN_ORBIT in result, got {context['result'].nav_status}"


@then(parsers.parse('the command should fail with {error_type}'))
def check_command_failed(context, error_type):
    """Verify command failed with specific error"""
    assert context['error'] is not None, "Expected error but command succeeded"

    error_map = {
        'ShipNotFoundError': ShipNotFoundError,
        'InvalidNavStatusError': InvalidNavStatusError
    }

    expected_error_class = error_map.get(error_type)
    assert expected_error_class is not None, f"Unknown error type: {error_type}"
    assert isinstance(context['error'], expected_error_class), \
        f"Expected {error_type}, got {type(context['error']).__name__}"


@then(parsers.parse('the error message should contain "{text}"'))
def check_error_message_contains(context, text):
    """Verify error message contains text"""
    assert context['error'] is not None, "No error to check message"
    error_msg = str(context['error']).lower()
    assert text.lower() in error_msg, \
        f"Expected '{text}' in error message, got: {error_msg}"


@then(parsers.parse('the ship should have the following properties:'))
def check_ship_properties(context, datatable):
    """Verify ship has expected properties from table"""
    assert context['result'] is not None, "No result to check properties"

    for i, row in enumerate(datatable):
        if i == 0:  # Skip header row
            continue
        prop = row[0]
        expected = row[1]

        if prop == 'nav_status':
            actual = context['result'].nav_status
        elif prop == 'fuel_current':
            actual = str(context['result'].fuel.current)
        elif prop == 'fuel_capacity':
            actual = str(context['result'].fuel_capacity)
        elif prop == 'cargo_capacity':
            actual = str(context['result'].cargo_capacity)
        elif prop == 'cargo_units':
            actual = str(context['result'].cargo_units)
        elif prop == 'engine_speed':
            actual = str(context['result'].engine_speed)
        elif prop == 'location':
            actual = context['result'].current_location.symbol
        else:
            raise ValueError(f"Unknown property: {prop}")

        assert str(actual) == str(expected), \
            f"Property {prop}: expected {expected}, got {actual}"


@given(parsers.parse('a ship "{ship_symbol}" for player {player_id:d} in transit arriving in {seconds:d} seconds at "{location}"'))
def create_ship_in_transit_arriving_at(context, ship_symbol, player_id, seconds, location):
    """Create a ship that is in transit with a specific arrival time"""
    from datetime import datetime, timedelta, timezone

    waypoint = Waypoint(
        symbol=location,
        x=0.0,
        y=0.0,
        system_symbol="X1-TEST",
        waypoint_type="PLANET"
    )

    fuel = Fuel(current=100, capacity=100)

    ship = Ship(
        ship_symbol=ship_symbol,
        player_id=player_id,
        current_location=waypoint,
        fuel=fuel,
        fuel_capacity=100,
        cargo_capacity=40,
        cargo_units=0,
        engine_speed=30,
        nav_status=Ship.IN_TRANSIT
    )

    context['ship_repo'].create(ship)

    # Store mock_api and ensure handler exists
    if 'handler' not in context:
        from application.navigation.commands.orbit_ship import OrbitShipHandler
        context['handler'] = OrbitShipHandler(context['ship_repo'])

    # Configure mock API to transition ship from IN_TRANSIT to IN_ORBIT after arrival
    arrival_time = datetime.now(timezone.utc) + timedelta(seconds=seconds)
    context['api']._ship_state[ship_symbol] = {
        "nav_status": "IN_TRANSIT",
        "location": waypoint.symbol,
        "fuel_current": 100,
        "arrival_time": arrival_time
    }
