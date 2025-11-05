"""BDD step definitions for ShipRepository integration tests"""
from pytest_bdd import scenario, given, when, then, parsers
import pytest
from datetime import datetime

from spacetraders.domain.shared.player import Player
from spacetraders.domain.shared.ship import Ship
from spacetraders.domain.shared.value_objects import Waypoint, Fuel
from spacetraders.adapters.secondary.persistence.ship_repository import (
    ShipNotFoundError, DuplicateShipError
)


@pytest.fixture
def context():
    """Shared test context"""
    return {}


# Shared fixtures setup
@given("a fresh ship repository")
def fresh_repository(context, ship_repository):
    context["ship_repo"] = ship_repository


@given("a test player exists")
def setup_test_player(context, test_player):
    context["test_player"] = test_player


@given("a second test player exists")
def setup_second_player(context, player_repository):
    player = Player(
        player_id=None,
        agent_symbol="AGENT_2",
        token="token2",
        created_at=datetime(2025, 1, 1, 12, 0, 0),
        last_active=datetime(2025, 1, 1, 12, 0, 0)
    )
    context["player_2"] = player_repository.create(player)


@given(parsers.parse('a created ship "{ship_symbol}"'))
def create_test_ship(context, ship_symbol, ship_repository):
    waypoint = Waypoint(symbol="X1-A1", x=0.0, y=0.0, system_symbol="X1")
    fuel = Fuel(current=100, capacity=200)
    ship = Ship(
        ship_symbol=ship_symbol,
        player_id=context["test_player"].player_id,
        current_location=waypoint,
        fuel=fuel,
        fuel_capacity=200,
        cargo_capacity=50,
        cargo_units=0,
        engine_speed=30,
        nav_status="IN_ORBIT"
    )
    created = ship_repository.create(ship)
    context[f"ship_{ship_symbol}"] = created
    context["last_created"] = created


# When steps
@when(parsers.parse('I create a ship with symbol "{ship_symbol}"'))
def create_ship(context, ship_symbol, ship_repository):
    waypoint = Waypoint(symbol="X1-A1", x=0.0, y=0.0, system_symbol="X1")
    fuel = Fuel(current=100, capacity=200)
    ship = Ship(
        ship_symbol=ship_symbol,
        player_id=context["test_player"].player_id,
        current_location=waypoint,
        fuel=fuel,
        fuel_capacity=200,
        cargo_capacity=50,
        cargo_units=0,
        engine_speed=30,
        nav_status="IN_ORBIT"
    )
    context["last_created"] = ship_repository.create(ship)


@when(parsers.parse('I attempt to create another ship "{ship_symbol}" for the same player'))
def attempt_duplicate_ship(context, ship_symbol, ship_repository):
    context["error"] = None
    try:
        waypoint = Waypoint(symbol="X1-A1", x=0.0, y=0.0, system_symbol="X1")
        fuel = Fuel(current=100, capacity=200)
        ship = Ship(
            ship_symbol=ship_symbol,
            player_id=context["test_player"].player_id,
            current_location=waypoint,
            fuel=fuel,
            fuel_capacity=200,
            cargo_capacity=50,
            cargo_units=0,
            engine_speed=30,
            nav_status="IN_ORBIT"
        )
        ship_repository.create(ship)
    except Exception as e:
        context["error"] = e


@when(parsers.parse('I create ship "{ship_symbol}" for player {player_num:d}'))
def create_ship_for_player(context, ship_symbol, player_num, ship_repository):
    player_key = "test_player" if player_num == 1 else "player_2"
    waypoint = Waypoint(symbol="X1-A1", x=0.0, y=0.0, system_symbol="X1")
    fuel = Fuel(current=100, capacity=200)
    ship = Ship(
        ship_symbol=ship_symbol,
        player_id=context[player_key].player_id,
        current_location=waypoint,
        fuel=fuel,
        fuel_capacity=200,
        cargo_capacity=50,
        cargo_units=0,
        engine_speed=30,
        nav_status="IN_ORBIT"
    )
    created = ship_repository.create(ship)
    context[f"ship_{ship_symbol}_p{player_num}"] = created


@when(parsers.parse('I find the ship by symbol "{ship_symbol}"'))
def find_ship(context, ship_symbol, ship_repository):
    context["found"] = ship_repository.find_by_symbol(
        ship_symbol, context["test_player"].player_id
    )


@when(parsers.parse('I find ship by symbol "{ship_symbol}"'))
def find_ship_alt(context, ship_symbol, ship_repository):
    context["found"] = ship_repository.find_by_symbol(
        ship_symbol, context["test_player"].player_id
    )


@when("I list all ships for the player")
def list_ships(context, ship_repository):
    context["ships"] = ship_repository.find_all_by_player(context["test_player"].player_id)


@when(parsers.parse('I move the ship to "{location}" and consume fuel to {fuel:d}'))
def move_ship(context, location, fuel, ship_repository):
    ship = context["last_created"]
    new_location = Waypoint(symbol=location, x=10.0, y=10.0, system_symbol="X1")
    new_fuel = Fuel(current=fuel, capacity=200)

    # Recreate ship with new location and fuel using constructor
    updated_ship = Ship(
        ship_symbol=ship.ship_symbol,
        player_id=ship.player_id,
        current_location=new_location,
        fuel=new_fuel,
        fuel_capacity=ship.fuel_capacity,
        cargo_capacity=ship.cargo_capacity,
        cargo_units=ship.cargo_units,
        engine_speed=ship.engine_speed,
        nav_status=ship.nav_status
    )
    ship_repository.update(updated_ship)
    context["last_created"] = updated_ship


@when(parsers.parse('I update the ship cargo to {cargo:d} units'))
def update_cargo(context, cargo, ship_repository):
    ship = context["last_created"]

    # Recreate ship with new cargo using constructor
    updated_ship = Ship(
        ship_symbol=ship.ship_symbol,
        player_id=ship.player_id,
        current_location=ship.current_location,
        fuel=ship.fuel,
        fuel_capacity=ship.fuel_capacity,
        cargo_capacity=ship.cargo_capacity,
        cargo_units=cargo,
        engine_speed=ship.engine_speed,
        nav_status=ship.nav_status
    )
    ship_repository.update(updated_ship)
    context["last_created"] = updated_ship


@when(parsers.parse('I change the ship nav_status to "{status}"'))
def change_nav_status(context, status, ship_repository):
    ship = context["last_created"]

    # Recreate ship with new nav_status using constructor
    updated_ship = Ship(
        ship_symbol=ship.ship_symbol,
        player_id=ship.player_id,
        current_location=ship.current_location,
        fuel=ship.fuel,
        fuel_capacity=ship.fuel_capacity,
        cargo_capacity=ship.cargo_capacity,
        cargo_units=ship.cargo_units,
        engine_speed=ship.engine_speed,
        nav_status=status
    )
    ship_repository.update(updated_ship)
    context["last_created"] = updated_ship


@when("I attempt to update a nonexistent ship")
def update_nonexistent(context, ship_repository):
    context["error"] = None
    try:
        waypoint = Waypoint(symbol="X1-A1", x=0.0, y=0.0, system_symbol="X1")
        fuel = Fuel(current=100, capacity=200)
        ship = Ship(
            ship_symbol="FAKE",
            player_id=999,
            current_location=waypoint,
            fuel=fuel,
            fuel_capacity=200,
            cargo_capacity=50,
            cargo_units=0,
            engine_speed=30,
            nav_status="IN_ORBIT"
        )
        ship_repository.update(ship)
    except Exception as e:
        context["error"] = e


@when(parsers.parse('I delete the ship "{ship_symbol}"'))
def delete_ship(context, ship_symbol, ship_repository):
    ship_repository.delete(ship_symbol, context["test_player"].player_id)


@when(parsers.parse('I attempt to delete ship "{ship_symbol}"'))
def attempt_delete_ship(context, ship_symbol, ship_repository):
    context["error"] = None
    try:
        ship_repository.delete(ship_symbol, context["test_player"].player_id)
    except Exception as e:
        context["error"] = e


@when(parsers.parse('I create ship "{symbol}" with status "{status}"'))
def create_ship_with_status(context, symbol, status, ship_repository):
    waypoint = Waypoint(symbol="X1-A1", x=0.0, y=0.0, system_symbol="X1")
    fuel = Fuel(current=100, capacity=200)
    ship = Ship(
        ship_symbol=symbol,
        player_id=context["test_player"].player_id,
        current_location=waypoint,
        fuel=fuel,
        fuel_capacity=200,
        cargo_capacity=50,
        cargo_units=0,
        engine_speed=30,
        nav_status=status
    )
    context[f"ship_{symbol}"] = ship_repository.create(ship)


@when("I create a ship with zero fuel")
def create_zero_fuel_ship(context, ship_repository):
    waypoint = Waypoint(symbol="X1-A1", x=0.0, y=0.0, system_symbol="X1")
    fuel = Fuel(current=0, capacity=200)
    ship = Ship(
        ship_symbol="EMPTY-FUEL",
        player_id=context["test_player"].player_id,
        current_location=waypoint,
        fuel=fuel,
        fuel_capacity=200,
        cargo_capacity=50,
        cargo_units=0,
        engine_speed=30,
        nav_status="IN_ORBIT"
    )
    context["last_created"] = ship_repository.create(ship)


@when("I create a ship with full cargo")
def create_full_cargo_ship(context, ship_repository):
    waypoint = Waypoint(symbol="X1-A1", x=0.0, y=0.0, system_symbol="X1")
    fuel = Fuel(current=100, capacity=200)
    ship = Ship(
        ship_symbol="FULL-CARGO",
        player_id=context["test_player"].player_id,
        current_location=waypoint,
        fuel=fuel,
        fuel_capacity=200,
        cargo_capacity=50,
        cargo_units=50,
        engine_speed=30,
        nav_status="IN_ORBIT"
    )
    context["last_created"] = ship_repository.create(ship)


@when(parsers.parse('I create ship "{symbol}" at "{location}"'))
def create_ship_at_location(context, symbol, location, ship_repository):
    # Map location to coordinates
    coords = {"X1-A1": (0.0, 0.0), "X1-A2": (10.0, 10.0), "X1-A3": (20.0, 20.0)}
    x, y = coords.get(location, (0.0, 0.0))
    waypoint = Waypoint(symbol=location, x=x, y=y, system_symbol="X1")
    fuel = Fuel(current=100, capacity=200)
    ship = Ship(
        ship_symbol=symbol,
        player_id=context["test_player"].player_id,
        current_location=waypoint,
        fuel=fuel,
        fuel_capacity=200,
        cargo_capacity=50,
        cargo_units=0,
        engine_speed=30,
        nav_status="IN_ORBIT"
    )
    context[f"ship_{symbol}"] = ship_repository.create(ship)


@when(parsers.parse('I move the ship to "{location}"'))
def move_ship_simple(context, location, ship_repository):
    ship = context["last_created"]
    coords = {"X1-A2": (10.0, 10.0), "X1-A3": (20.0, 20.0)}
    x, y = coords.get(location, (0.0, 0.0))
    new_location = Waypoint(symbol=location, x=x, y=y, system_symbol="X1")

    # Recreate ship with new location using constructor
    updated_ship = Ship(
        ship_symbol=ship.ship_symbol,
        player_id=ship.player_id,
        current_location=new_location,
        fuel=ship.fuel,
        fuel_capacity=ship.fuel_capacity,
        cargo_capacity=ship.cargo_capacity,
        cargo_units=ship.cargo_units,
        engine_speed=ship.engine_speed,
        nav_status=ship.nav_status
    )
    ship_repository.update(updated_ship)
    context["last_created"] = updated_ship


@when(parsers.parse('I create ship "{symbol}" with speed {speed:d}'))
def create_ship_with_speed(context, symbol, speed, ship_repository):
    waypoint = Waypoint(symbol="X1-A1", x=0.0, y=0.0, system_symbol="X1")
    fuel = Fuel(current=100, capacity=200)
    ship = Ship(
        ship_symbol=symbol,
        player_id=context["test_player"].player_id,
        current_location=waypoint,
        fuel=fuel,
        fuel_capacity=200,
        cargo_capacity=50,
        cargo_units=0,
        engine_speed=speed,
        nav_status="IN_ORBIT"
    )
    context[f"ship_{symbol}"] = ship_repository.create(ship)


# Then steps
@then("the ship should be persisted")
def check_persisted(context):
    assert context["last_created"] is not None


@then(parsers.parse('the ship should have symbol "{symbol}"'))
def check_symbol(context, symbol):
    ship = context.get("found") or context.get("last_created")
    assert ship.ship_symbol == symbol


@then(parsers.parse("the ship fuel should be {fuel:d}"))
def check_fuel(context, fuel):
    ship = context.get("found") or context.get("last_created")
    assert ship.fuel.current == fuel


@then("creation should fail with DuplicateShipError")
def check_duplicate_error(context):
    assert isinstance(context["error"], DuplicateShipError)


@then("both ships should exist independently")
def check_both_exist(context, ship_repository):
    ship1 = ship_repository.find_by_symbol("SHIP-1", context["test_player"].player_id)
    ship2 = ship_repository.find_by_symbol("SHIP-1", context["player_2"].player_id)
    assert ship1 is not None
    assert ship2 is not None


@then("the ship should be found")
def check_found(context):
    assert context["found"] is not None


@then("the ship should not be found")
def check_not_found(context):
    assert context["found"] is None


@then(parsers.parse('the ship location should be "{location}"'))
def check_location(context, location):
    ship = context.get("found") or context.get("last_created")
    assert ship.current_location.symbol == location


@then("the ship waypoint should have full details from graph")
def check_waypoint_details(context):
    ship = context["found"]
    assert ship.current_location.waypoint_type == "PLANET"
    assert ship.current_location.system_symbol == "X1"


@then(parsers.parse("I should see {count:d} ship"))
def check_ship_count_singular(context, count):
    assert len(context["ships"]) == count


@then(parsers.parse("I should see {count:d} ships"))
def check_ship_count(context, count):
    assert len(context["ships"]) == count


@then("ships should be in alphabetical order")
def check_alphabetical(context):
    symbols = [s.ship_symbol for s in context["ships"]]
    assert symbols == sorted(symbols)


@then(parsers.parse("the ship cargo should be {cargo:d}"))
def check_cargo(context, cargo):
    ship = context.get("found") or context.get("last_created")
    assert ship.cargo_units == cargo


@then(parsers.parse('the ship nav_status should be "{status}"'))
def check_nav_status(context, status):
    ship = context.get("found") or context.get("last_created")
    assert ship.nav_status == status


@then("update should fail with ShipNotFoundError")
def check_update_error(context):
    assert isinstance(context["error"], ShipNotFoundError)


@then("deletion should fail with ShipNotFoundError")
def check_deletion_error(context):
    assert isinstance(context["error"], ShipNotFoundError)


@then(parsers.parse("all {count:d} ships should have their respective statuses"))
def check_all_statuses(context, count, ship_repository):
    ships = ship_repository.find_all_by_player(context["test_player"].player_id)
    statuses = {s.nav_status for s in ships}
    assert len(statuses) == count


@then("the ship cargo should equal capacity")
def check_cargo_equals_capacity(context):
    ship = context["last_created"]
    assert ship.cargo_units == ship.cargo_capacity


@then("the ship should be at full cargo")
def check_full_cargo(context):
    ship = context["last_created"]
    assert ship.is_cargo_full()


@then("each ship should be at its designated location")
def check_designated_locations(context, ship_repository):
    ship1 = context["ship_SHIP-1"]
    ship2 = context["ship_SHIP-2"]
    ship3 = context["ship_SHIP-3"]
    
    # Re-fetch to verify persistence
    found1 = ship_repository.find_by_symbol(ship1.ship_symbol, ship1.player_id)
    found2 = ship_repository.find_by_symbol(ship2.ship_symbol, ship2.player_id)
    found3 = ship_repository.find_by_symbol(ship3.ship_symbol, ship3.player_id)
    
    assert found1.current_location.symbol == "X1-A1"
    assert found2.current_location.symbol == "X1-A2"
    assert found3.current_location.symbol == "X1-A3"


@then("each ship should have its designated speed")
def check_designated_speeds(context, ship_repository):
    slow = context["ship_SHIP-SLOW"]
    medium = context["ship_SHIP-MEDIUM"]
    fast = context["ship_SHIP-FAST"]
    
    # Re-fetch to verify
    found_slow = ship_repository.find_by_symbol(slow.ship_symbol, slow.player_id)
    found_medium = ship_repository.find_by_symbol(medium.ship_symbol, medium.player_id)
    found_fast = ship_repository.find_by_symbol(fast.ship_symbol, fast.player_id)
    
    assert found_slow.engine_speed == 10
    assert found_medium.engine_speed == 30
    assert found_fast.engine_speed == 50


# Generate scenario decorators
scenarios = [
    "Create new ship",
    "Create duplicate ship fails",
    "Same ship symbol for different players",
    "Find ship by symbol when exists",
    "Find ship by symbol when not exists",
    "Find ship reconstructs waypoint from graph",
    "Find all ships by player when empty",
    "Find all ships by player with single ship",
    "Find all ships by player with multiple ships",
    "Ships are returned ordered by symbol",
    "Update ship location and fuel",
    "Update ship cargo",
    "Update ship nav status",
    "Update nonexistent ship fails",
    "Delete ship",
    "Delete nonexistent ship fails",
    "Ships with different nav statuses",
    "Ship with zero fuel",
    "Ship with full cargo",
    "Ships at different waypoint locations",
    "Update ship location multiple times",
    "Ships with different engine speeds",
]

for scenario_name in scenarios:
    func_name = f"test_{scenario_name.lower().replace(' ', '_')}"
    scenario_func = scenario(
        "../../../features/integration/persistence/ship_repository.feature",
        scenario_name
    )
    globals()[func_name] = scenario_func(lambda: None)
