"""
BDD step definitions for domain value objects feature.

Tests Waypoint, Fuel, FlightMode, and Distance value objects
across 74 scenarios covering all value object functionality.
Black-box testing approach - only tests observable behaviors.
"""
import pytest
from pytest_bdd import scenarios, given, when, then, parsers

from spacetraders.domain.shared.value_objects import (
    Waypoint,
    Fuel,
    FlightMode,
    Distance
)

# Load all scenarios from the feature file
scenarios('../../features/domain/value_objects.feature')


# ============================================================================
# Given Steps - Setup
# ============================================================================

@given("the value objects system is initialized")
def initialize_system(context):
    """Initialize test context"""
    context['waypoints'] = {}
    context['fuel'] = None
    context['flight_mode'] = None
    context['distance'] = None
    context['original_fuel'] = None
    context['original_distance'] = None
    context['error'] = None


# --- Waypoint Given Steps ---

@given(parsers.parse('a waypoint with symbol "{symbol}" at coordinates ({x:g}, {y:g})'))
def create_waypoint(context, symbol, x, y):
    """Create waypoint with basic properties"""
    context['waypoint'] = Waypoint(symbol=symbol, x=x, y=y)


@given(parsers.parse('a waypoint "{name}" with symbol "{symbol}" at coordinates ({x:g}, {y:g})'))
def create_named_waypoint(context, name, symbol, x, y):
    """Create named waypoint for distance calculations"""
    context['waypoints'][name] = Waypoint(symbol=symbol, x=x, y=y)


@given(parsers.parse('a waypoint "{name}" with symbol "{symbol}" at coordinates ({x:g}, {y:g}) with orbital "{orbital}"'))
def create_waypoint_with_orbital(context, name, symbol, x, y, orbital):
    """Create waypoint with orbital"""
    context['waypoints'][name] = Waypoint(
        symbol=symbol,
        x=x,
        y=y,
        orbitals=(orbital,)
    )


@given(parsers.parse('a waypoint with symbol "{symbol}" at coordinates ({x:g}, {y:g}) and system "{system}"'))
def create_waypoint_with_system(context, symbol, x, y, system):
    """Create waypoint with system symbol"""
    context['waypoint'] = Waypoint(symbol=symbol, x=x, y=y, system_symbol=system)


@given(parsers.parse('a waypoint with symbol "{symbol}" at coordinates ({x:g}, {y:g}) and type "{waypoint_type}"'))
def create_waypoint_with_type(context, symbol, x, y, waypoint_type):
    """Create waypoint with type"""
    context['waypoint'] = Waypoint(symbol=symbol, x=x, y=y, waypoint_type=waypoint_type)


@given(parsers.parse('a waypoint with symbol "{symbol}" at coordinates ({x:g}, {y:g}) and traits "{traits}"'))
def create_waypoint_with_traits(context, symbol, x, y, traits):
    """Create waypoint with traits"""
    traits_tuple = tuple(traits.split(','))
    context['waypoint'] = Waypoint(symbol=symbol, x=x, y=y, traits=traits_tuple)


@given(parsers.parse('a waypoint with symbol "{symbol}" at coordinates ({x:g}, {y:g}) and fuel available'))
def create_waypoint_with_fuel(context, symbol, x, y):
    """Create waypoint with fuel"""
    context['waypoint'] = Waypoint(symbol=symbol, x=x, y=y, has_fuel=True)


@given(parsers.parse('a waypoint with symbol "{symbol}" at coordinates ({x:g}, {y:g}) and orbital "{orbital}"'))
def create_waypoint_with_orbital_param(context, symbol, x, y, orbital):
    """Create waypoint with orbital (different from the named waypoint version)"""
    context['waypoint'] = Waypoint(symbol=symbol, x=x, y=y, orbitals=(orbital,))


# --- Fuel Given Steps ---

@given(parsers.parse('fuel with {current:d} current and {capacity:d} capacity'))
def create_fuel(context, current, capacity):
    """Create fuel object"""
    context['fuel'] = Fuel(current=current, capacity=capacity)
    context['original_fuel'] = context['fuel']


@given(parsers.parse('a ship with {percentage:g}% fuel'))
def create_ship_with_fuel_percentage(context, percentage):
    """Create fuel with percentage"""
    capacity = 500
    current = int(capacity * percentage / 100)
    context['fuel'] = Fuel(current=current, capacity=capacity)
    context['fuel_percentage'] = percentage


# --- FlightMode Given Steps ---

@given(parsers.parse('{mode} flight mode'))
def set_flight_mode(context, mode):
    """Set specific flight mode"""
    context['flight_mode'] = FlightMode[mode]


# --- Distance Given Steps ---

@given(parsers.parse('a distance of {units:g} units'))
def create_distance(context, units):
    """Create distance object"""
    context['distance'] = Distance(units=units)
    context['original_distance'] = context['distance']


# ============================================================================
# When Steps - Actions
# ============================================================================

# --- Waypoint When Steps ---

@when(parsers.parse('I attempt to modify the waypoint symbol to "{new_symbol}"'))
def attempt_modify_waypoint_symbol(context, new_symbol):
    """Attempt to modify waypoint symbol"""
    try:
        context['waypoint'].symbol = new_symbol
        context['error'] = None
    except Exception as e:
        context['error'] = e


@when(parsers.parse('I calculate the distance from waypoint "{from_name}" to waypoint "{to_name}"'))
def calculate_distance_between_waypoints(context, from_name, to_name):
    """Calculate distance between two waypoints"""
    waypoint_from = context['waypoints'][from_name]
    waypoint_to = context['waypoints'][to_name]
    context[f'distance_{from_name}_to_{to_name}'] = waypoint_from.distance_to(waypoint_to)


@when("I calculate the distance from the waypoint to itself")
def calculate_distance_to_self(context):
    """Calculate distance from waypoint to itself"""
    waypoint = context['waypoint']
    context['self_distance'] = waypoint.distance_to(waypoint)


@when(parsers.parse('I check if waypoint "{name1}" is an orbital of waypoint "{name2}"'))
def check_orbital_relationship(context, name1, name2):
    """Check if waypoint is orbital of another"""
    waypoint1 = context['waypoints'][name1]
    waypoint2 = context['waypoints'][name2]
    context['is_orbital'] = waypoint1.is_orbital_of(waypoint2)
    context['is_orbital_reverse'] = waypoint2.is_orbital_of(waypoint1)


@when("I get the string representation of the waypoint")
def get_waypoint_repr(context):
    """Get waypoint repr"""
    context['repr'] = repr(context['waypoint'])


# --- Fuel When Steps ---

@when(parsers.parse('I attempt to create fuel with {current:d} current and {capacity:d} capacity'))
def attempt_create_fuel(context, current, capacity):
    """Attempt to create fuel with invalid values"""
    try:
        Fuel(current=current, capacity=capacity)
        context['error'] = None
    except ValueError as e:
        context['error'] = e


@when(parsers.parse('I attempt to modify the current fuel to {new_current:d}'))
def attempt_modify_fuel_current(context, new_current):
    """Attempt to modify fuel current"""
    try:
        context['fuel'].current = new_current
        context['error'] = None
    except Exception as e:
        context['error'] = e


@when("I calculate the fuel percentage")
def calculate_fuel_percentage(context):
    """Calculate fuel percentage"""
    context['percentage'] = context['fuel'].percentage()


@when(parsers.parse('I consume {amount:d} units of fuel'))
def consume_fuel(context, amount):
    """Consume fuel"""
    context['new_fuel'] = context['fuel'].consume(amount)


@when(parsers.parse('I attempt to consume {amount:d} units of fuel'))
def attempt_consume_fuel(context, amount):
    """Attempt to consume fuel with invalid amount"""
    try:
        context['fuel'].consume(amount)
        context['error'] = None
    except ValueError as e:
        context['error'] = e


@when(parsers.parse('I add {amount:d} units of fuel'))
def add_fuel(context, amount):
    """Add fuel"""
    context['new_fuel'] = context['fuel'].add(amount)


@when(parsers.parse('I attempt to add {amount:d} units of fuel'))
def attempt_add_fuel(context, amount):
    """Attempt to add fuel with invalid amount"""
    try:
        context['fuel'].add(amount)
        context['error'] = None
    except ValueError as e:
        context['error'] = e


@when(parsers.parse('I check if I can travel with {required:d} units required'))
def check_can_travel(context, required):
    """Check if can travel with required fuel"""
    context['can_travel'] = context['fuel'].can_travel(required)


@when(parsers.parse('I check if I can travel with {required:d} units required and {margin:g} safety margin'))
def check_can_travel_with_margin(context, required, margin):
    """Check if can travel with required fuel and safety margin"""
    context['can_travel'] = context['fuel'].can_travel(required, safety_margin=margin)


@when("I check if fuel is full")
def check_fuel_is_full(context):
    """Check if fuel is full"""
    context['is_full'] = context['fuel'].is_full()


@when("I get the string representation of the fuel")
def get_fuel_repr(context):
    """Get fuel repr"""
    context['repr'] = repr(context['fuel'])


# --- FlightMode When Steps ---

@when(parsers.parse('I calculate fuel cost for {distance:g} units distance'))
def calculate_fuel_cost(context, distance):
    """Calculate fuel cost for distance"""
    context['fuel_cost'] = context['flight_mode'].fuel_cost(distance)


@when(parsers.parse('I calculate travel time for {distance:g} units distance and engine speed {speed:d}'))
def calculate_travel_time(context, distance, speed):
    """Calculate travel time for distance and speed"""
    context['travel_time'] = context['flight_mode'].travel_time(distance, speed)


@when("I select optimal flight mode")
def select_optimal_flight_mode(context):
    """Select optimal flight mode based on fuel"""
    fuel = context['fuel']
    # Use a default distance of 30 units for testing
    distance = 30.0
    cruise_cost = FlightMode.CRUISE.fuel_cost(distance)
    context['selected_mode'] = FlightMode.select_optimal(fuel.current, cruise_cost, safety_margin=4)


# --- Distance When Steps ---

@when(parsers.parse('I attempt to create a distance of {units:g} units'))
def attempt_create_distance(context, units):
    """Attempt to create distance with invalid value"""
    try:
        Distance(units=units)
        context['error'] = None
    except ValueError as e:
        context['error'] = e


@when(parsers.parse('I attempt to modify the distance units to {new_units:g}'))
def attempt_modify_distance_units(context, new_units):
    """Attempt to modify distance units"""
    try:
        context['distance'].units = new_units
        context['error'] = None
    except Exception as e:
        context['error'] = e


@when(parsers.parse('I apply a safety margin of {margin:g}'))
def apply_safety_margin(context, margin):
    """Apply safety margin to distance"""
    context['distance_with_margin'] = context['distance'].with_margin(margin)


@when("I get the string representation of the distance")
def get_distance_repr(context):
    """Get distance repr"""
    context['repr'] = repr(context['distance'])


# ============================================================================
# Then Steps - Assertions
# ============================================================================

# --- Waypoint Then Steps ---

@then(parsers.parse('the waypoint symbol should be "{symbol}"'))
def check_waypoint_symbol(context, symbol):
    """Verify waypoint symbol"""
    assert context['waypoint'].symbol == symbol


@then(parsers.parse('the waypoint x coordinate should be {x:g}'))
def check_waypoint_x(context, x):
    """Verify waypoint x coordinate"""
    assert context['waypoint'].x == x


@then(parsers.parse('the waypoint y coordinate should be {y:g}'))
def check_waypoint_y(context, y):
    """Verify waypoint y coordinate"""
    assert context['waypoint'].y == y


@then(parsers.parse('the waypoint system symbol should be "{system_symbol}"'))
def check_waypoint_system_symbol(context, system_symbol):
    """Verify waypoint system symbol"""
    assert context['waypoint'].system_symbol == system_symbol


@then(parsers.parse('the waypoint type should be "{waypoint_type}"'))
def check_waypoint_type(context, waypoint_type):
    """Verify waypoint type"""
    assert context['waypoint'].waypoint_type == waypoint_type


@then(parsers.parse('the waypoint should have traits "{trait1}" and "{trait2}"'))
def check_waypoint_traits(context, trait1, trait2):
    """Verify waypoint traits"""
    assert trait1 in context['waypoint'].traits
    assert trait2 in context['waypoint'].traits


@then("the waypoint should have fuel")
def check_waypoint_has_fuel(context):
    """Verify waypoint has fuel"""
    assert context['waypoint'].has_fuel is True


@then(parsers.parse('the waypoint should have orbital "{orbital}"'))
def check_waypoint_orbital(context, orbital):
    """Verify waypoint orbital"""
    assert orbital in context['waypoint'].orbitals


@then(parsers.parse('the distance should be {distance:g} units'))
def check_distance_value(context, distance):
    """Verify calculated distance"""
    # Check distance from stored calculations
    for key, value in context.items():
        if key.startswith('distance_') and not key.endswith('_margin'):
            assert abs(value - distance) < 0.001
            return

    # Check self distance
    if 'self_distance' in context:
        assert abs(context['self_distance'] - distance) < 0.001


@then("both distances should be equal")
def check_distances_equal(context):
    """Verify distances are equal (symmetric)"""
    # Extract the two distance calculations
    distances = [v for k, v in context.items() if k.startswith('distance_')]
    assert len(distances) == 2
    assert abs(distances[0] - distances[1]) < 0.001


@then("the orbital relationship should be true")
def check_orbital_true(context):
    """Verify orbital relationship is true"""
    assert context['is_orbital'] is True


@then("the orbital relationship should be symmetric")
def check_orbital_symmetric(context):
    """Verify orbital relationship is symmetric"""
    assert context['is_orbital'] == context['is_orbital_reverse']


@then("the orbital relationship should be false")
def check_orbital_false(context):
    """Verify orbital relationship is false"""
    assert context['is_orbital'] is False


@then(parsers.parse('the representation should contain "{text}"'))
def check_repr_contains(context, text):
    """Verify repr contains text"""
    assert text in context['repr']


# --- Fuel Then Steps ---

@then(parsers.parse('the current fuel should be {current:d}'))
def check_fuel_current(context, current):
    """Verify current fuel"""
    assert context['fuel'].current == current


@then(parsers.parse('the fuel capacity should be {capacity:d}'))
def check_fuel_capacity(context, capacity):
    """Verify fuel capacity"""
    assert context['fuel'].capacity == capacity


@then(parsers.parse('fuel creation should fail with error "{error_message}"'))
def check_fuel_creation_error(context, error_message):
    """Verify fuel creation error"""
    assert context['error'] is not None
    assert error_message in str(context['error'])


@then(parsers.parse('the percentage should be {percentage:g}%'))
def check_fuel_percentage(context, percentage):
    """Verify fuel percentage"""
    assert abs(context['percentage'] - percentage) < 0.001


@then(parsers.parse('the new fuel should have {current:d} current'))
def check_new_fuel_current(context, current):
    """Verify new fuel current"""
    assert context['new_fuel'].current == current


@then(parsers.parse('the new fuel should have {capacity:d} capacity'))
def check_new_fuel_capacity(context, capacity):
    """Verify new fuel capacity"""
    assert context['new_fuel'].capacity == capacity


@then(parsers.parse('the original fuel should have {current:d} current'))
def check_original_fuel_current(context, current):
    """Verify original fuel unchanged"""
    assert context['original_fuel'].current == current


@then(parsers.parse('the consumption should fail with error "{error_message}"'))
def check_consumption_error(context, error_message):
    """Verify consumption error"""
    assert context['error'] is not None
    assert error_message in str(context['error'])


@then(parsers.parse('the addition should fail with error "{error_message}"'))
def check_addition_error(context, error_message):
    """Verify addition error"""
    assert context['error'] is not None
    assert error_message in str(context['error'])


@then("travel should be possible")
def check_travel_possible(context):
    """Verify travel is possible"""
    assert context['can_travel'] is True


@then("travel should not be possible")
def check_travel_not_possible(context):
    """Verify travel is not possible"""
    assert context['can_travel'] is False


@then("fuel should be full")
def check_fuel_full(context):
    """Verify fuel is full"""
    assert context['is_full'] is True


@then("fuel should not be full")
def check_fuel_not_full(context):
    """Verify fuel is not full"""
    assert context['is_full'] is False


# --- FlightMode Then Steps ---

@then(parsers.parse('{mode} flight mode should exist'))
def check_flight_mode_exists(context, mode):
    """Verify flight mode exists"""
    assert FlightMode[mode] is not None


@then(parsers.parse('the mode name should be "{name}"'))
def check_mode_name(context, name):
    """Verify mode name"""
    assert context['flight_mode'].mode_name == name


@then(parsers.parse('the time multiplier should be {multiplier:d}'))
def check_time_multiplier(context, multiplier):
    """Verify time multiplier"""
    assert context['flight_mode'].time_multiplier == multiplier


@then(parsers.parse('the fuel rate should be {rate:g}'))
def check_fuel_rate(context, rate):
    """Verify fuel rate"""
    assert abs(context['flight_mode'].fuel_rate - rate) < 0.001


@then(parsers.parse('the fuel cost should be {cost:d} units'))
@then(parsers.parse('the fuel cost should be {cost:d} unit'))
def check_fuel_cost(context, cost):
    """Verify fuel cost"""
    assert context['fuel_cost'] == cost


@then(parsers.parse('the fuel cost should be at least {cost:d} units'))
@then(parsers.parse('the fuel cost should be at least {cost:d} unit'))
def check_fuel_cost_minimum(context, cost):
    """Verify minimum fuel cost"""
    assert context['fuel_cost'] >= cost


@then(parsers.parse('the travel time should be {time:d} seconds'))
@then(parsers.parse('the travel time should be {time:d} second'))
def check_travel_time(context, time):
    """Verify travel time"""
    assert context['travel_time'] == time


@then(parsers.parse('the travel time should be at least {time:d} seconds'))
@then(parsers.parse('the travel time should be at least {time:d} second'))
def check_travel_time_minimum(context, time):
    """Verify minimum travel time"""
    assert context['travel_time'] >= time


@then(parsers.parse('{mode} mode should be selected'))
def check_mode_selected(context, mode):
    """Verify selected mode"""
    expected_mode = FlightMode[mode]
    assert context['selected_mode'] == expected_mode


# --- Distance Then Steps ---

@then(parsers.parse('the distance units should be {units:g}'))
def check_distance_units(context, units):
    """Verify distance units"""
    assert abs(context['distance'].units - units) < 0.001


@then(parsers.parse('distance creation should fail with error "{error_message}"'))
def check_distance_creation_error(context, error_message):
    """Verify distance creation error"""
    assert context['error'] is not None
    assert error_message in str(context['error'])


@then(parsers.parse('the distance with margin should be {units:g} units'))
def check_distance_with_margin(context, units):
    """Verify distance with margin"""
    assert abs(context['distance_with_margin'].units - units) < 0.001


@then(parsers.parse('the original distance should be {units:g} units'))
def check_original_distance(context, units):
    """Verify original distance unchanged"""
    assert abs(context['original_distance'].units - units) < 0.001


# --- Generic Then Steps ---

@then("the modification should be rejected")
def check_modification_rejected(context):
    """Verify modification was rejected"""
    assert context['error'] is not None
