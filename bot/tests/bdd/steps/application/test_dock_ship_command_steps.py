"""BDD steps for Dock Ship Command"""
from pytest_bdd import scenario, given, when, then, parsers
import asyncio
import pytest
from unittest.mock import patch

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
def initialize_handler(context, mock_ship_repo, mock_api):
    """Initialize the DockShipHandler with mock dependencies"""
    # Store mock API in context so it can be used by the patched get_api_client_for_player
    context["mock_api"] = mock_api
    context["mock_ship_repo"] = mock_ship_repo
    # Handler now only takes ship_repo - API client is retrieved via get_api_client_for_player
    context["handler"] = DockShipHandler(mock_ship_repo)


@given(parsers.parse('a ship "{ship_symbol}" for player {player_id:d} in orbit at "{location}"'))
def create_ship_in_orbit(context, ship_symbol, player_id, location, mock_ship_repo):
    """Create a ship in orbit at a specific location"""
    waypoint = Waypoint(
        symbol=location,
        x=0.0,
        y=0.0,
        system_symbol=location.split('-')[0] + '-' + location.split('-')[1],
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
        nav_status=Ship.IN_ORBIT
    )

    mock_ship_repo.create(ship)
    context["initial_nav_status"] = Ship.IN_ORBIT


@when(parsers.parse('I execute dock ship command for "{ship_symbol}" and player {player_id:d}'))
def execute_dock_command(context, ship_symbol, player_id):
    """Execute the dock ship command"""
    handler = context["handler"]
    command = DockShipCommand(ship_symbol=ship_symbol, player_id=player_id)
    # Patch get_api_client_for_player at the container module level
    with patch('spacetraders.configuration.container.get_api_client_for_player', return_value=context["mock_api"]):
        context["result"] = asyncio.run(handler.handle(command))
    context["ship_symbol"] = ship_symbol
    context["error"] = None


@then("the ship should be docked")
def check_ship_docked(context):
    """Verify the ship is in docked status"""
    assert context["result"].nav_status == Ship.DOCKED


@then(parsers.parse('the API dock method should be called with "{ship_symbol}"'))
def check_api_dock_called(context, ship_symbol):
    """Verify the API dock method was called"""
    mock_api = context["mock_api"]
    assert mock_api.dock_called
    assert mock_api.dock_ship_symbol == ship_symbol


@then(parsers.parse('the ship should be persisted with nav status "{status}"'))
def check_ship_persisted(context, status, mock_ship_repo):
    """Verify the ship was updated in the repository"""
    ship_symbol = context["ship_symbol"]
    # Get the ship from the repository - need to determine player_id from context
    # The result should have the player_id
    player_id = context["result"].player_id
    updated_ship = mock_ship_repo.find_by_symbol(ship_symbol, player_id)
    assert updated_ship is not None
    assert updated_ship.nav_status == status


# ==============================================================================
# Scenario: Dock ship that is already docked
# ==============================================================================
@scenario("../../features/application/dock_ship_command.feature", "Dock ship that is already docked")
def test_dock_ship_already_docked():
    pass


@given(parsers.parse('a ship "{ship_symbol}" for player {player_id:d} already docked at "{location}"'))
def create_ship_already_docked(context, ship_symbol, player_id, location, mock_ship_repo):
    """Create a ship that is already docked"""
    waypoint = Waypoint(
        symbol=location,
        x=0.0,
        y=0.0,
        system_symbol=location.split('-')[0] + '-' + location.split('-')[1],
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
        nav_status=Ship.DOCKED
    )

    mock_ship_repo.create(ship)
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
    # No action needed - ship doesn't exist in empty repository
    context["ship_symbol"] = ship_symbol
    context["player_id"] = player_id


@when(parsers.parse('I attempt to dock ship "{ship_symbol}" for player {player_id:d}'))
def attempt_dock_command(context, ship_symbol, player_id):
    """Attempt to execute dock command and capture any errors"""
    handler = context["handler"]
    command = DockShipCommand(ship_symbol=ship_symbol, player_id=player_id)
    try:
        # Patch get_api_client_for_player at the container module level
        with patch('spacetraders.configuration.container.get_api_client_for_player', return_value=context["mock_api"]):
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
# Scenario: Cannot dock ship that is in transit
# ==============================================================================
@scenario("../../features/application/dock_ship_command.feature", "Cannot dock ship that is in transit")
def test_cannot_dock_ship_in_transit():
    pass


@given(parsers.parse('a ship "{ship_symbol}" for player {player_id:d} in transit to "{destination}"'))
def create_ship_in_transit(context, ship_symbol, player_id, destination, mock_ship_repo):
    """Create a ship that is in transit"""
    waypoint = Waypoint(
        symbol=destination,
        x=0.0,
        y=0.0,
        system_symbol=destination.split('-')[0] + '-' + destination.split('-')[1],
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

    mock_ship_repo.create(ship)


@then("the command should fail with InvalidNavStatusError")
def check_invalid_nav_status_error(context):
    """Verify InvalidNavStatusError was raised"""
    assert isinstance(context["error"], InvalidNavStatusError)


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
def create_ship_with_properties(context, ship_symbol, player_id, location, fuel_current, fuel_capacity, mock_ship_repo):
    """Create a ship with specific properties"""
    waypoint = Waypoint(
        symbol=location,
        x=0.0,
        y=0.0,
        system_symbol=location.split('-')[0] + '-' + location.split('-')[1],
        waypoint_type="PLANET"
    )

    fuel = Fuel(current=fuel_current, capacity=fuel_capacity)

    ship = Ship(
        ship_symbol=ship_symbol,
        player_id=player_id,
        current_location=waypoint,
        fuel=fuel,
        fuel_capacity=fuel_capacity,
        cargo_capacity=40,
        cargo_units=0,
        engine_speed=30,
        nav_status=Ship.IN_ORBIT
    )

    mock_ship_repo.create(ship)
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
