"""
BDD step definitions for Ship entity tests

Following black-box testing principles:
- NO access to private methods (methods starting with _)
- NO access to private attributes (attributes starting with _)
- Test only public API and observable behaviors
- Use context dictionary for sharing state between steps
"""
from pytest_bdd import scenario, given, when, then, parsers
import pytest

from domain.shared.ship import (
    Ship,
    InvalidNavStatusError,
    InsufficientFuelError,
    InvalidShipDataError,
)
from domain.shared.value_objects import Waypoint, Fuel, FlightMode


# ==============================================================================
# Background: Test Waypoints Setup (using fixture from conftest)
# ==============================================================================

@pytest.fixture(autouse=True)
def setup_waypoints(context, waypoints):
    """Automatically setup waypoints in context for all tests"""
    context["waypoints"] = waypoints


# ==============================================================================
# Helper Functions
# ==============================================================================

def create_default_ship(context, **kwargs):
    """Create a ship with default values, overriding with kwargs"""
    defaults = {
        "ship_symbol": "SHIP-1",
        "player_id": 1,
        "current_location": context["waypoints"]["X1-A1"],
        "fuel": Fuel(current=100, capacity=100),
        "fuel_capacity": 100,
        "cargo_capacity": 40,
        "cargo_units": 0,
        "engine_speed": 30,
        "nav_status": Ship.IN_ORBIT
    }
    defaults.update(kwargs)
    return Ship(**defaults)


# ==============================================================================
# Scenario: Create ship with valid data
# ==============================================================================

@scenario("../../features/domain/ship_entity.feature", "Create ship with valid data")
def test_create_ship_with_valid_data():
    pass


@when(parsers.parse('I create a ship with symbol "{symbol}", player {player_id:d}, at "{location}", fuel {fuel_current:d}/{fuel_capacity:d}, cargo {cargo_units:d}/{cargo_capacity:d}, speed {speed:d}, status "{status}"'))
def create_ship_full(context, symbol, player_id, location, fuel_current, fuel_capacity, cargo_units, cargo_capacity, speed, status):
    """Create a ship with full specification"""
    context["ship"] = Ship(
        ship_symbol=symbol,
        player_id=player_id,
        current_location=context["waypoints"][location],
        fuel=Fuel(current=fuel_current, capacity=fuel_capacity),
        fuel_capacity=fuel_capacity,
        cargo_capacity=cargo_capacity,
        cargo_units=cargo_units,
        engine_speed=speed,
        nav_status=status
    )


@when(parsers.parse('I create a ship with symbol "{symbol}", player {player_id:d}, at "{location}", fuel {fuel_current:d}/{fuel_capacity:d}, cargo {cargo_units:d}/{cargo_capacity:d}, speed {speed:d}'))
def create_ship_no_status(context, symbol, player_id, location, fuel_current, fuel_capacity, cargo_units, cargo_capacity, speed):
    """Create a ship without specifying status (uses default)"""
    context["ship"] = Ship(
        ship_symbol=symbol,
        player_id=player_id,
        current_location=context["waypoints"][location],
        fuel=Fuel(current=fuel_current, capacity=fuel_capacity),
        fuel_capacity=fuel_capacity,
        cargo_capacity=cargo_capacity,
        cargo_units=cargo_units,
        engine_speed=speed
    )


@then(parsers.parse('the ship should have symbol "{symbol}"'))
def check_ship_symbol(context, symbol):
    assert context["ship"].ship_symbol == symbol


@then(parsers.parse("the ship should have player_id {player_id:d}"))
def check_player_id(context, player_id):
    assert context["ship"].player_id == player_id


@then(parsers.parse('the ship should be at location "{location}"'))
def check_ship_location(context, location):
    assert context["ship"].current_location.symbol == location


@then(parsers.parse("the ship should have {amount:d} units of fuel"))
def check_fuel_amount(context, amount):
    assert context["ship"].fuel.current == amount


@then(parsers.parse("the ship fuel capacity should be {capacity:d}"))
def check_fuel_capacity(context, capacity):
    assert context["ship"].fuel_capacity == capacity


@then(parsers.parse("the ship cargo capacity should be {capacity:d}"))
def check_cargo_capacity(context, capacity):
    assert context["ship"].cargo_capacity == capacity


@then(parsers.parse("the ship cargo units should be {units:d}"))
def check_cargo_units(context, units):
    assert context["ship"].cargo_units == units


@then(parsers.parse("the ship engine speed should be {speed:d}"))
def check_engine_speed(context, speed):
    assert context["ship"].engine_speed == speed


@then("the ship should be in orbit")
def check_ship_in_orbit(context):
    assert context["ship"].is_in_orbit()


# ==============================================================================
# Scenario: Create ship with default nav status
# ==============================================================================

@scenario("../../features/domain/ship_entity.feature", "Create ship with default nav status")
def test_create_ship_with_default_nav_status():
    pass


# Reuses: create_ship_from_table, check_ship_in_orbit


# ==============================================================================
# Scenario: Create ship trims ship symbol whitespace
# ==============================================================================

@scenario("../../features/domain/ship_entity.feature", "Create ship trims ship symbol whitespace")
def test_create_ship_trims_whitespace():
    pass


@when(parsers.parse('I create a ship with ship_symbol "{symbol}"'))
def create_ship_with_symbol(context, symbol):
    context["ship"] = create_default_ship(context, ship_symbol=symbol)


# ==============================================================================
# Initialization Validation Scenarios
# ==============================================================================

@scenario("../../features/domain/ship_entity.feature", "Create ship with empty ship symbol raises error")
def test_empty_ship_symbol():
    pass


@scenario("../../features/domain/ship_entity.feature", "Create ship with whitespace only ship symbol raises error")
def test_whitespace_ship_symbol():
    pass


@scenario("../../features/domain/ship_entity.feature", "Create ship with zero player_id raises error")
def test_zero_player_id():
    pass


@scenario("../../features/domain/ship_entity.feature", "Create ship with negative player_id raises error")
def test_negative_player_id():
    pass


@scenario("../../features/domain/ship_entity.feature", "Create ship with zero fuel capacity succeeds")
def test_zero_fuel_capacity():
    pass


@scenario("../../features/domain/ship_entity.feature", "Create ship with negative fuel capacity raises error")
def test_negative_fuel_capacity():
    pass


@scenario("../../features/domain/ship_entity.feature", "Create ship with mismatched fuel capacity raises error")
def test_mismatched_fuel_capacity():
    pass


@scenario("../../features/domain/ship_entity.feature", "Create ship with negative cargo capacity raises error")
def test_negative_cargo_capacity():
    pass


@scenario("../../features/domain/ship_entity.feature", "Create ship with zero cargo capacity succeeds")
def test_zero_cargo_capacity():
    pass


@scenario("../../features/domain/ship_entity.feature", "Create ship with negative cargo units raises error")
def test_negative_cargo_units():
    pass


@scenario("../../features/domain/ship_entity.feature", "Create ship with cargo units exceeding capacity raises error")
def test_cargo_exceeds_capacity():
    pass


@scenario("../../features/domain/ship_entity.feature", "Create ship with zero engine speed raises error")
def test_zero_engine_speed():
    pass


@scenario("../../features/domain/ship_entity.feature", "Create ship with negative engine speed raises error")
def test_negative_engine_speed():
    pass


@scenario("../../features/domain/ship_entity.feature", "Create ship with invalid nav status raises error")
def test_invalid_nav_status():
    pass


# Shared steps for validation scenarios

@when("I attempt to create a ship with empty ship_symbol")
def attempt_create_ship_with_empty_symbol(context):
    try:
        context["ship"] = create_default_ship(context, ship_symbol="")
        context["error"] = None
    except Exception as e:
        context["error"] = e


@when(parsers.parse('I attempt to create a ship with ship_symbol "{symbol}"'))
def attempt_create_ship_with_symbol(context, symbol):
    try:
        context["ship"] = create_default_ship(context, ship_symbol=symbol)
        context["error"] = None
    except Exception as e:
        context["error"] = e


@when(parsers.parse("I attempt to create a ship with player_id {player_id:d}"))
def attempt_create_ship_with_player_id(context, player_id):
    try:
        context["ship"] = create_default_ship(context, player_id=player_id)
        context["error"] = None
    except Exception as e:
        context["error"] = e


@when(parsers.parse("I attempt to create a ship with fuel_capacity {capacity:d}"))
def attempt_create_ship_with_fuel_capacity(context, capacity):
    try:
        fuel = Fuel(current=0, capacity=capacity) if capacity > 0 else Fuel(current=0, capacity=0)
        context["ship"] = create_default_ship(context, fuel=fuel, fuel_capacity=capacity)
        context["error"] = None
    except Exception as e:
        context["error"] = e


@when(parsers.parse("I attempt to create a ship with fuel object capacity {fuel_cap:d} but fuel_capacity parameter {param_cap:d}"))
def attempt_create_ship_mismatched_fuel(context, fuel_cap, param_cap):
    try:
        fuel = Fuel(current=50, capacity=fuel_cap)
        context["ship"] = create_default_ship(context, fuel=fuel, fuel_capacity=param_cap)
        context["error"] = None
    except Exception as e:
        context["error"] = e


@when(parsers.parse("I attempt to create a ship with cargo_capacity {capacity:d}"))
def attempt_create_ship_with_cargo_capacity(context, capacity):
    try:
        context["ship"] = create_default_ship(context, cargo_capacity=capacity)
        context["error"] = None
    except Exception as e:
        context["error"] = e


@when(parsers.parse("I create a ship with cargo_capacity {capacity:d}"))
def create_ship_with_cargo_capacity(context, capacity):
    context["ship"] = create_default_ship(context, cargo_capacity=capacity, cargo_units=0)


@when(parsers.parse("I create a ship with fuel_capacity {capacity:d} and fuel {fuel_current:d}/{fuel_capacity:d}"))
def create_ship_with_fuel_capacity_and_fuel(context, capacity, fuel_current, fuel_capacity):
    fuel = Fuel(current=fuel_current, capacity=fuel_capacity)
    context["ship"] = create_default_ship(context, fuel=fuel, fuel_capacity=capacity)


@when(parsers.parse("I attempt to create a ship with cargo_units {units:d}"))
def attempt_create_ship_with_cargo_units(context, units):
    try:
        context["ship"] = create_default_ship(context, cargo_units=units)
        context["error"] = None
    except Exception as e:
        context["error"] = e


@when(parsers.parse("I attempt to create a ship with cargo_capacity {capacity:d} and cargo_units {units:d}"))
def attempt_create_ship_with_cargo(context, capacity, units):
    try:
        context["ship"] = create_default_ship(context, cargo_capacity=capacity, cargo_units=units)
        context["error"] = None
    except Exception as e:
        context["error"] = e


@when(parsers.parse("I attempt to create a ship with engine_speed {speed:d}"))
def attempt_create_ship_with_engine_speed(context, speed):
    try:
        context["ship"] = create_default_ship(context, engine_speed=speed)
        context["error"] = None
    except Exception as e:
        context["error"] = e


@when(parsers.parse('I attempt to create a ship with nav_status "{status}"'))
def attempt_create_ship_with_nav_status(context, status):
    try:
        context["ship"] = create_default_ship(context, nav_status=status)
        context["error"] = None
    except Exception as e:
        context["error"] = e


@then(parsers.parse("ship creation should fail with {error_type} matching \"{message}\""))
def check_creation_error(context, error_type, message):
    assert context["error"] is not None

    # Map error type names to classes
    error_classes = {
        "InvalidShipDataError": InvalidShipDataError,
        "ValueError": ValueError,
    }

    assert isinstance(context["error"], error_classes[error_type])
    assert message in str(context["error"])


# ==============================================================================
# Navigation State Machine Scenarios
# ==============================================================================

@scenario("../../features/domain/ship_entity.feature", "Depart from docked to in orbit")
def test_depart_from_docked():
    pass


@scenario("../../features/domain/ship_entity.feature", "Depart when already in orbit is noop")
def test_depart_when_in_orbit():
    pass


@scenario("../../features/domain/ship_entity.feature", "Depart when in transit raises error")
def test_depart_when_in_transit():
    pass


@scenario("../../features/domain/ship_entity.feature", "Dock from in orbit to docked")
def test_dock_from_orbit():
    pass


@scenario("../../features/domain/ship_entity.feature", "Dock when already docked is noop")
def test_dock_when_docked():
    pass


@scenario("../../features/domain/ship_entity.feature", "Dock when in transit raises error")
def test_dock_when_in_transit():
    pass


@scenario("../../features/domain/ship_entity.feature", "Start transit from in orbit to in transit")
def test_start_transit_from_orbit():
    pass


@scenario("../../features/domain/ship_entity.feature", "Start transit from docked transitions via orbit")
def test_start_transit_from_docked():
    pass


@scenario("../../features/domain/ship_entity.feature", "Start transit when already in transit raises error")
def test_start_transit_when_in_transit():
    pass


@scenario("../../features/domain/ship_entity.feature", "Arrive from in transit to in orbit")
def test_arrive_from_transit():
    pass


@scenario("../../features/domain/ship_entity.feature", "Arrive when docked raises error")
def test_arrive_when_docked():
    pass


@scenario("../../features/domain/ship_entity.feature", "Arrive when in orbit raises error")
def test_arrive_when_in_orbit():
    pass


@scenario("../../features/domain/ship_entity.feature", "Ensure in orbit from docked transitions to orbit")
def test_ensure_orbit_from_docked():
    pass


@scenario("../../features/domain/ship_entity.feature", "Ensure in orbit when already in orbit returns false")
def test_ensure_orbit_when_in_orbit():
    pass


@scenario("../../features/domain/ship_entity.feature", "Ensure in orbit when in transit raises error")
def test_ensure_orbit_when_in_transit():
    pass


@scenario("../../features/domain/ship_entity.feature", "Ensure docked from in orbit transitions to docked")
def test_ensure_docked_from_orbit():
    pass


@scenario("../../features/domain/ship_entity.feature", "Ensure docked when already docked returns false")
def test_ensure_docked_when_docked():
    pass


@scenario("../../features/domain/ship_entity.feature", "Ensure docked when in transit raises error")
def test_ensure_docked_when_in_transit():
    pass


# Shared Given steps for navigation

@given(parsers.parse('a docked ship at "{location}"'))
def docked_ship(context, location):
    context["ship"] = create_default_ship(
        context,
        current_location=context["waypoints"][location],
        nav_status=Ship.DOCKED
    )


@given(parsers.parse('a ship in orbit at "{location}"'))
def ship_in_orbit(context, location):
    context["ship"] = create_default_ship(
        context,
        current_location=context["waypoints"][location],
        nav_status=Ship.IN_ORBIT
    )


@given(parsers.parse('a ship in transit to "{location}"'))
def ship_in_transit(context, location):
    context["ship"] = create_default_ship(
        context,
        current_location=context["waypoints"][location],
        nav_status=Ship.IN_TRANSIT
    )


# When steps for navigation actions

@when("the ship departs")
def ship_departs(context):
    context["ship"].depart()


@when("I attempt to depart the ship")
def attempt_depart(context):
    try:
        context["ship"].depart()
        context["error"] = None
    except Exception as e:
        context["error"] = e


@when("the ship docks")
def ship_docks(context):
    context["ship"].dock()


@when("I attempt to dock the ship")
def attempt_dock(context):
    try:
        context["ship"].dock()
        context["error"] = None
    except Exception as e:
        context["error"] = e


@when(parsers.parse('the ship starts transit to "{destination}"'))
def start_transit(context, destination):
    context["ship"].start_transit(context["waypoints"][destination])


@when(parsers.parse('I attempt to start transit to "{destination}"'))
def attempt_start_transit(context, destination):
    try:
        context["ship"].start_transit(context["waypoints"][destination])
        context["error"] = None
    except Exception as e:
        context["error"] = e


@when("the ship arrives")
def ship_arrives(context):
    context["ship"].arrive()


@when("I attempt to arrive the ship")
def attempt_arrive(context):
    try:
        context["ship"].arrive()
        context["error"] = None
    except Exception as e:
        context["error"] = e


@when("I ensure the ship is in orbit")
def ensure_in_orbit(context):
    context["result"] = context["ship"].ensure_in_orbit()


@when("I attempt to ensure the ship is in orbit")
def attempt_ensure_in_orbit(context):
    try:
        context["result"] = context["ship"].ensure_in_orbit()
        context["error"] = None
    except Exception as e:
        context["error"] = e


@when("I ensure the ship is docked")
def ensure_docked(context):
    context["result"] = context["ship"].ensure_docked()


@when("I attempt to ensure the ship is docked")
def attempt_ensure_docked(context):
    try:
        context["result"] = context["ship"].ensure_docked()
        context["error"] = None
    except Exception as e:
        context["error"] = e


# Then steps for navigation assertions

@then("the ship should not be docked")
def check_not_docked(context):
    assert not context["ship"].is_docked()


@then("the ship should be docked")
def check_docked(context):
    assert context["ship"].is_docked()


@then("the ship should not be in orbit")
def check_not_in_orbit(context):
    assert not context["ship"].is_in_orbit()


@then("the ship should be in transit")
def check_in_transit(context):
    assert context["ship"].is_in_transit()


@then("the ship should not be in transit")
def check_not_in_transit(context):
    assert not context["ship"].is_in_transit()


@then("the result should be True")
def check_result_true(context):
    assert context["result"] is True


@then("the result should be False")
def check_result_false(context):
    assert context["result"] is False


@then(parsers.parse("the operation should fail with {error_type} matching \"{message}\""))
def check_operation_error(context, error_type, message):
    assert context["error"] is not None

    error_classes = {
        "InvalidNavStatusError": InvalidNavStatusError,
        "ValueError": ValueError,
        "InsufficientFuelError": InsufficientFuelError,
    }

    assert isinstance(context["error"], error_classes[error_type])
    assert message in str(context["error"])


# ==============================================================================
# Fuel Management Scenarios
# ==============================================================================

@scenario("../../features/domain/ship_entity.feature", "Consume fuel reduces fuel amount")
def test_consume_fuel():
    pass


@scenario("../../features/domain/ship_entity.feature", "Consume fuel with zero amount")
def test_consume_zero_fuel():
    pass


@scenario("../../features/domain/ship_entity.feature", "Consume fuel with negative amount raises error")
def test_consume_negative_fuel():
    pass


@scenario("../../features/domain/ship_entity.feature", "Consume fuel more than available raises error")
def test_consume_too_much_fuel():
    pass


@scenario("../../features/domain/ship_entity.feature", "Consume all fuel")
def test_consume_all_fuel():
    pass


@scenario("../../features/domain/ship_entity.feature", "Refuel increases fuel amount")
def test_refuel():
    pass


@scenario("../../features/domain/ship_entity.feature", "Refuel with zero amount")
def test_refuel_zero():
    pass


@scenario("../../features/domain/ship_entity.feature", "Refuel with negative amount raises error")
def test_refuel_negative():
    pass


@scenario("../../features/domain/ship_entity.feature", "Refuel caps at capacity")
def test_refuel_caps():
    pass


@scenario("../../features/domain/ship_entity.feature", "Refuel to full when partially filled")
def test_refuel_to_full_partial():
    pass


@scenario("../../features/domain/ship_entity.feature", "Refuel to full when already full")
def test_refuel_to_full_already_full():
    pass


# Given steps for fuel

@given(parsers.parse("a ship with {amount:d} units of fuel"))
def ship_with_fuel(context, amount):
    context["ship"] = create_default_ship(
        context,
        fuel=Fuel(current=amount, capacity=100)
    )


@given(parsers.parse("a ship with {amount:d} units of fuel and capacity {capacity:d}"))
def ship_with_fuel_and_capacity(context, amount, capacity):
    context["ship"] = create_default_ship(
        context,
        fuel=Fuel(current=amount, capacity=capacity),
        fuel_capacity=capacity
    )


# When steps for fuel operations

@when(parsers.parse("the ship consumes {amount:d} units of fuel"))
def consume_fuel(context, amount):
    context["ship"].consume_fuel(amount)


@when(parsers.parse("I attempt to consume {amount:d} units of fuel"))
def attempt_consume_fuel(context, amount):
    try:
        context["ship"].consume_fuel(amount)
        context["error"] = None
    except Exception as e:
        context["error"] = e


@when(parsers.parse("the ship refuels {amount:d} units"))
def refuel(context, amount):
    context["ship"].refuel(amount)


@when(parsers.parse("I attempt to refuel {amount:d} units"))
def attempt_refuel(context, amount):
    try:
        context["ship"].refuel(amount)
        context["error"] = None
    except Exception as e:
        context["error"] = e


@when("the ship refuels to full")
def refuel_to_full(context):
    context["fuel_added"] = context["ship"].refuel_to_full()


@then(parsers.parse("the fuel added should be {amount:d} units"))
def check_fuel_added(context, amount):
    assert context["fuel_added"] == amount


# ==============================================================================
# Navigation Calculation Scenarios
# ==============================================================================

@scenario("../../features/domain/ship_entity.feature", "Can navigate to nearby destination with enough fuel")
def test_can_navigate_nearby():
    pass


@scenario("../../features/domain/ship_entity.feature", "Can navigate to distant destination with enough fuel")
def test_can_navigate_distant():
    pass


@scenario("../../features/domain/ship_entity.feature", "Cannot navigate to destination without enough fuel")
def test_cannot_navigate_no_fuel():
    pass


@scenario("../../features/domain/ship_entity.feature", "Can navigate to same location")
def test_can_navigate_same_location():
    pass


@scenario("../../features/domain/ship_entity.feature", "Calculate fuel for trip with cruise mode")
def test_calculate_fuel_cruise():
    pass


@scenario("../../features/domain/ship_entity.feature", "Calculate fuel for trip with drift mode")
def test_calculate_fuel_drift():
    pass


@scenario("../../features/domain/ship_entity.feature", "Calculate fuel for trip with burn mode")
def test_calculate_fuel_burn():
    pass


@scenario("../../features/domain/ship_entity.feature", "Calculate fuel for trip with stealth mode")
def test_calculate_fuel_stealth():
    pass


@scenario("../../features/domain/ship_entity.feature", "Needs refuel for journey with low fuel")
def test_needs_refuel_low_fuel():
    pass


@scenario("../../features/domain/ship_entity.feature", "Needs refuel for journey with enough fuel")
def test_needs_refuel_enough_fuel():
    pass


@scenario("../../features/domain/ship_entity.feature", "Needs refuel for journey with custom safety margin")
def test_needs_refuel_custom_margin():
    pass


@scenario("../../features/domain/ship_entity.feature", "Calculate travel time with cruise mode")
def test_travel_time_cruise():
    pass


@scenario("../../features/domain/ship_entity.feature", "Calculate travel time with drift mode")
def test_travel_time_drift():
    pass


@scenario("../../features/domain/ship_entity.feature", "Calculate travel time with burn mode")
def test_travel_time_burn():
    pass


@scenario("../../features/domain/ship_entity.feature", "Calculate travel time with stealth mode")
def test_travel_time_stealth():
    pass


@scenario("../../features/domain/ship_entity.feature", "Select optimal flight mode with high fuel")
def test_optimal_mode_high_fuel():
    pass


@scenario("../../features/domain/ship_entity.feature", "Select optimal flight mode with low fuel")
def test_optimal_mode_low_fuel():
    pass


@scenario("../../features/domain/ship_entity.feature", "Select optimal flight mode with medium fuel")
def test_optimal_mode_threshold():
    pass


# Given steps for navigation calculations

@given(parsers.parse('a ship at "{location}" with {amount:d} units of fuel'))
def ship_at_location_with_fuel(context, location, amount):
    context["ship"] = create_default_ship(
        context,
        current_location=context["waypoints"][location],
        fuel=Fuel(current=amount, capacity=100)
    )


@given(parsers.parse('a ship at "{location}" with {amount:d} units of fuel and capacity {capacity:d}'))
def ship_at_location_with_fuel_capacity(context, location, amount, capacity):
    context["ship"] = create_default_ship(
        context,
        current_location=context["waypoints"][location],
        fuel=Fuel(current=amount, capacity=capacity),
        fuel_capacity=capacity
    )


@given(parsers.parse('a ship at "{location}"'))
def ship_at_location(context, location):
    context["ship"] = create_default_ship(
        context,
        current_location=context["waypoints"][location]
    )


@given(parsers.parse('a ship at "{location}" with engine speed {speed:d}'))
def ship_at_location_with_speed(context, location, speed):
    context["ship"] = create_default_ship(
        context,
        current_location=context["waypoints"][location],
        engine_speed=speed
    )


# When steps for navigation calculations

@when(parsers.parse('I check if the ship can navigate to "{destination}"'))
def check_can_navigate(context, destination):
    context["result"] = context["ship"].can_navigate_to(context["waypoints"][destination])


@when(parsers.parse('I calculate fuel required to "{destination}" with {mode} mode'))
def calculate_fuel_required(context, destination, mode):
    flight_mode = getattr(FlightMode, mode)
    context["fuel_required"] = context["ship"].calculate_fuel_for_trip(
        context["waypoints"][destination],
        flight_mode
    )


@when(parsers.parse('I check if the ship needs refuel for journey to "{destination}"'))
def check_needs_refuel(context, destination):
    context["result"] = context["ship"].needs_refuel_for_journey(context["waypoints"][destination])


@when(parsers.parse('I check if the ship needs refuel for journey to "{destination}" with safety margin {margin:f}'))
def check_needs_refuel_with_margin(context, destination, margin):
    context["result"] = context["ship"].needs_refuel_for_journey(
        context["waypoints"][destination],
        safety_margin=margin
    )


@when(parsers.parse('I calculate travel time to "{destination}" with {mode} mode'))
def calculate_travel_time(context, destination, mode):
    flight_mode = getattr(FlightMode, mode)
    context["travel_time"] = context["ship"].calculate_travel_time(
        context["waypoints"][destination],
        flight_mode
    )


@when("I select optimal flight mode")
def select_optimal_mode(context):
    context["selected_mode"] = context["ship"].select_optimal_flight_mode()


# Then steps for navigation calculations

@then(parsers.parse("the fuel required should be {amount:d} units"))
def check_fuel_required(context, amount):
    assert context["fuel_required"] == amount


@then(parsers.parse("the travel time should be {seconds:d} seconds"))
def check_travel_time(context, seconds):
    assert context["travel_time"] == seconds


@then(parsers.parse("the selected mode should be {mode}"))
def check_selected_mode(context, mode):
    expected_mode = getattr(FlightMode, mode)
    assert context["selected_mode"] == expected_mode


# ==============================================================================
# Cargo Management Scenarios
# ==============================================================================

@scenario("../../features/domain/ship_entity.feature", "Has cargo space with empty cargo")
def test_has_cargo_space_empty():
    pass


@scenario("../../features/domain/ship_entity.feature", "Has cargo space with partial cargo")
def test_has_cargo_space_partial():
    pass


@scenario("../../features/domain/ship_entity.feature", "Has cargo space with full cargo")
def test_has_cargo_space_full():
    pass


@scenario("../../features/domain/ship_entity.feature", "Has cargo space with specific units")
def test_has_cargo_space_specific():
    pass


@scenario("../../features/domain/ship_entity.feature", "Has cargo space with specific units exceeding available")
def test_has_cargo_space_exceeding():
    pass


@scenario("../../features/domain/ship_entity.feature", "Available cargo space with empty cargo")
def test_available_cargo_empty():
    pass


@scenario("../../features/domain/ship_entity.feature", "Available cargo space with partial cargo")
def test_available_cargo_partial():
    pass


@scenario("../../features/domain/ship_entity.feature", "Available cargo space with full cargo")
def test_available_cargo_full():
    pass


@scenario("../../features/domain/ship_entity.feature", "Is cargo empty when empty")
def test_is_cargo_empty_when_empty():
    pass


@scenario("../../features/domain/ship_entity.feature", "Is cargo empty when not empty")
def test_is_cargo_empty_when_not():
    pass


@scenario("../../features/domain/ship_entity.feature", "Is cargo full when full")
def test_is_cargo_full_when_full():
    pass


@scenario("../../features/domain/ship_entity.feature", "Is cargo full when not full")
def test_is_cargo_full_when_not():
    pass


# Given steps for cargo

@given(parsers.parse("a ship with cargo capacity {capacity:d} and cargo units {units:d}"))
def ship_with_cargo(context, capacity, units):
    context["ship"] = create_default_ship(
        context,
        cargo_capacity=capacity,
        cargo_units=units
    )


# When steps for cargo operations

@when("I check if the ship has cargo space")
def check_has_cargo_space(context):
    context["result"] = context["ship"].has_cargo_space()


@when(parsers.parse("I check if the ship has cargo space for {units:d} units"))
def check_has_cargo_space_for_units(context, units):
    context["result"] = context["ship"].has_cargo_space(units)


@when("I check available cargo space")
def check_available_cargo_space(context):
    context["available_space"] = context["ship"].available_cargo_space()


@when("I check if cargo is empty")
def check_is_cargo_empty(context):
    context["result"] = context["ship"].is_cargo_empty()


@when("I check if cargo is full")
def check_is_cargo_full(context):
    context["result"] = context["ship"].is_cargo_full()


# Then steps for cargo

@then(parsers.parse("the available space should be {units:d} units"))
def check_available_space(context, units):
    assert context["available_space"] == units


# ==============================================================================
# State Query Scenarios
# ==============================================================================

@scenario("../../features/domain/ship_entity.feature", "Is docked when docked")
def test_is_docked_when_docked():
    pass


@scenario("../../features/domain/ship_entity.feature", "Is docked when in orbit")
def test_is_docked_when_in_orbit():
    pass


@scenario("../../features/domain/ship_entity.feature", "Is docked when in transit")
def test_is_docked_when_in_transit():
    pass


@scenario("../../features/domain/ship_entity.feature", "Is in orbit when in orbit")
def test_is_in_orbit_when_in_orbit():
    pass


@scenario("../../features/domain/ship_entity.feature", "Is in orbit when docked")
def test_is_in_orbit_when_docked():
    pass


@scenario("../../features/domain/ship_entity.feature", "Is in orbit when in transit")
def test_is_in_orbit_when_in_transit():
    pass


@scenario("../../features/domain/ship_entity.feature", "Is in transit when in transit")
def test_is_in_transit_when_in_transit():
    pass


@scenario("../../features/domain/ship_entity.feature", "Is in transit when docked")
def test_is_in_transit_when_docked():
    pass


@scenario("../../features/domain/ship_entity.feature", "Is in transit when in orbit")
def test_is_in_transit_when_in_orbit():
    pass


@scenario("../../features/domain/ship_entity.feature", "Is at location when at location")
def test_is_at_location_when_at():
    pass


@scenario("../../features/domain/ship_entity.feature", "Is at location when not at location")
def test_is_at_location_when_not():
    pass


# When steps for state queries

@when("I check if the ship is docked")
def check_is_docked(context):
    context["result"] = context["ship"].is_docked()


@when("I check if the ship is in orbit")
def check_is_in_orbit(context):
    context["result"] = context["ship"].is_in_orbit()


@when("I check if the ship is in transit")
def check_is_in_transit(context):
    context["result"] = context["ship"].is_in_transit()


@when(parsers.parse('I check if the ship is at location "{location}"'))
def check_is_at_location(context, location):
    context["result"] = context["ship"].is_at_location(context["waypoints"][location])


# ==============================================================================
# Equality and Hashing Scenarios
# ==============================================================================

@scenario("../../features/domain/ship_entity.feature", "Ships with same symbol and player are equal")
def test_equal_ships():
    pass


@scenario("../../features/domain/ship_entity.feature", "Ships with different symbols are not equal")
def test_different_symbols():
    pass


@scenario("../../features/domain/ship_entity.feature", "Ships with different players are not equal")
def test_different_players():
    pass


@scenario("../../features/domain/ship_entity.feature", "Ship not equal to string")
def test_not_equal_string():
    pass


@scenario("../../features/domain/ship_entity.feature", "Ship not equal to integer")
def test_not_equal_integer():
    pass


@scenario("../../features/domain/ship_entity.feature", "Ship not equal to None")
def test_not_equal_none():
    pass


@scenario("../../features/domain/ship_entity.feature", "Equal ships have same hash")
def test_equal_hash():
    pass


@scenario("../../features/domain/ship_entity.feature", "Ships can be used in set")
def test_ships_in_set():
    pass


# Given steps for equality

@given(parsers.parse('two ships with symbol "{symbol}" and player_id {player_id:d} with different attributes'))
def two_ships_same_identity(context, symbol, player_id):
    context["ship1"] = create_default_ship(
        context,
        ship_symbol=symbol,
        player_id=player_id,
        fuel=Fuel(current=100, capacity=100),
        cargo_units=0
    )
    context["ship2"] = create_default_ship(
        context,
        ship_symbol=symbol,
        player_id=player_id,
        fuel=Fuel(current=50, capacity=100),
        cargo_capacity=60,
        cargo_units=10,
        engine_speed=40
    )


@given(parsers.parse('a ship with symbol "{symbol}" and player_id {player_id:d}'))
def ship_with_identity(context, symbol, player_id):
    if "ship1" not in context:
        context["ship1"] = create_default_ship(context, ship_symbol=symbol, player_id=player_id)
    else:
        context["ship2"] = create_default_ship(context, ship_symbol=symbol, player_id=player_id)


@given(parsers.parse('a ship with symbol "{symbol}"'))
def ship_with_symbol_for_comparison(context, symbol):
    context["ship"] = create_default_ship(context, ship_symbol=symbol)


@given(parsers.parse('three ships: "{symbol1}" player {player1:d}, "{symbol2}" player {player2:d}, "{symbol3}" player {player3:d}'))
def three_ships(context, symbol1, player1, symbol2, player2, symbol3, player3):
    context["ship1"] = create_default_ship(context, ship_symbol=symbol1, player_id=player1)
    context["ship2"] = create_default_ship(context, ship_symbol=symbol2, player_id=player2)
    context["ship3"] = create_default_ship(context, ship_symbol=symbol3, player_id=player3)


# When steps for equality

@when("I compare the ships for equality")
def compare_ships(context):
    context["equal"] = (context["ship1"] == context["ship2"])


@when(parsers.parse('I compare the ship to string "{value}"'))
def compare_to_string(context, value):
    context["equal"] = (context["ship"] == value)


@when(parsers.parse("I compare the ship to integer {value:d}"))
def compare_to_integer(context, value):
    context["equal"] = (context["ship"] == value)


@when("I compare the ship to None")
def compare_to_none(context):
    context["equal"] = (context["ship"] == None)


@when("I compute the hash of both ships")
def compute_hashes(context):
    context["hash1"] = hash(context["ship1"])
    context["hash2"] = hash(context["ship2"])


@when("I add the ships to a set")
def add_to_set(context):
    context["ship_set"] = {context["ship1"], context["ship2"], context["ship3"]}


# Then steps for equality

@then("the ships should be equal")
def check_ships_equal(context):
    assert context["equal"] is True


@then("the ships should not be equal")
def check_ships_not_equal(context):
    assert context["equal"] is False


@then("they should not be equal")
def check_not_equal(context):
    assert context["equal"] is False


@then("the hashes should be equal")
def check_hashes_equal(context):
    assert context["hash1"] == context["hash2"]


@then(parsers.parse("the set should contain {count:d} ships"))
def check_set_size(context, count):
    assert len(context["ship_set"]) == count


# ==============================================================================
# Repr Scenarios
# ==============================================================================

@scenario("../../features/domain/ship_entity.feature", "Repr contains ship info")
def test_repr_contains_info():
    pass


# Given steps for repr

@given(parsers.parse('a ship with symbol "{symbol}" at "{location}" with status "{status}" and fuel "{fuel_info}"'))
def ship_for_repr(context, symbol, location, status, fuel_info):
    context["ship"] = create_default_ship(
        context,
        ship_symbol=symbol,
        current_location=context["waypoints"][location],
        nav_status=status,
        fuel=Fuel(current=100, capacity=100)
    )


# When steps for repr

@when("I get the repr of the ship")
def get_repr(context):
    context["repr"] = repr(context["ship"])


# Then steps for repr

@then(parsers.parse('the repr should contain "{text}"'))
def check_repr_contains(context, text):
    assert text in context["repr"]


@then("the repr should contain fuel information")
def check_repr_contains_fuel(context):
    # Check for fuel information in repr (either "100/100" or "Fuel")
    assert "100/100" in context["repr"] or "Fuel" in context["repr"]
