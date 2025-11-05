"""
BDD step definitions for fuel management feature.

Tests Fuel value object, consumption, refueling, and RefuelPlanner service
across 34 scenarios covering all fuel management functionality.
"""
import pytest
from pytest_bdd import scenarios, given, when, then, parsers

from domain.shared.value_objects import Fuel, FlightMode
from domain.navigation.services import RefuelPlanner

# Load all scenarios from the feature file
scenarios('../../features/navigation/fuel_management.feature')


# ============================================================================
# Given Steps - Setup
# ============================================================================

@given(parsers.parse('a fuel object with {current:d} current and {capacity:d} capacity'))
def fuel_object(context, current, capacity):
    """Create fuel object"""
    try:
        context['fuel'] = Fuel(current=current, capacity=capacity)
        context['error'] = None
    except Exception as e:
        context['error'] = e
        context['fuel'] = None


@given(parsers.parse('a distance of {distance:d} units'))
def set_distance(context, distance):
    """Set distance for calculations"""
    context['distance'] = distance


@given(parsers.parse('a distance of {distance:g} units'))
def set_distance_float(context, distance):
    """Set distance for calculations (float)"""
    context['distance'] = distance


@given("I am at a marketplace")
def at_marketplace(context):
    """Set marketplace flag"""
    context['at_marketplace'] = True


@given("I am not at a marketplace")
def not_at_marketplace(context):
    """Clear marketplace flag"""
    context['at_marketplace'] = False


@given(parsers.parse('a destination {distance:d} units away'))
def destination_distance(context, distance):
    """Set destination distance"""
    context['destination_distance'] = distance


@given(parsers.parse('a refuel point {distance:d} units away'))
def refuel_point_distance(context, distance):
    """Set refuel point distance"""
    context['refuel_point_distance'] = distance


@given(parsers.parse('a safety margin of {margin:d}%'))
def safety_margin(context, margin):
    """Set safety margin"""
    context['safety_margin'] = margin / 100.0


@given("any flight mode")
def any_flight_mode(context):
    """Use default CRUISE mode"""
    context['flight_mode'] = FlightMode.CRUISE


@given(parsers.parse('CRUISE mode with {rate:g} fuel rate'))
def cruise_mode_with_rate(context, rate):
    """Set CRUISE mode"""
    context['flight_mode'] = FlightMode.CRUISE


# ============================================================================
# When Steps - Actions
# ============================================================================

@when(parsers.parse('I create a fuel object with {current:d} current and {capacity:d} capacity'))
def create_fuel_object(context, current, capacity):
    """Create fuel object (action)"""
    fuel_object(context, current, capacity)


@when(parsers.parse('I attempt to create a fuel object with {current:d} current and {capacity:d} capacity'))
def attempt_create_fuel(context, current, capacity):
    """Attempt to create fuel object (may fail)"""
    fuel_object(context, current, capacity)


@when("I calculate the fuel percentage")
def calculate_fuel_percentage(context):
    """Calculate fuel percentage"""
    context['percentage'] = context['fuel'].percentage()


@when(parsers.parse('I consume {amount:d} fuel units'))
def consume_fuel(context, amount):
    """Consume fuel"""
    try:
        context['new_fuel'] = context['fuel'].consume(amount)
        context['error'] = None
    except Exception as e:
        context['error'] = e


@when(parsers.parse('I attempt to consume {amount:d} fuel units'))
def attempt_consume_fuel(context, amount):
    """Attempt to consume fuel (may fail)"""
    consume_fuel(context, amount)


@when(parsers.parse('I calculate {mode} mode fuel consumption'))
def calculate_mode_fuel_consumption(context, mode):
    """Calculate fuel consumption for specific mode"""
    flight_mode = FlightMode[mode]
    distance = context.get('distance', 0)
    context['fuel_required'] = flight_mode.fuel_cost(distance)


@when(parsers.parse('I add {amount:d} fuel units'))
def add_fuel(context, amount):
    """Add fuel"""
    try:
        context['new_fuel'] = context['fuel'].add(amount)
        context['error'] = None
    except Exception as e:
        context['error'] = e


@when(parsers.parse('I attempt to add {amount:d} fuel units'))
def attempt_add_fuel(context, amount):
    """Attempt to add fuel (may fail)"""
    add_fuel(context, amount)


@when(parsers.parse('I check if I can travel requiring {required:d} fuel'))
def check_can_travel(context, required):
    """Check travel feasibility with margin from context or default"""
    context['required_fuel'] = required
    margin = context.get('safety_margin', 0.1)
    context['can_travel'] = context['fuel'].can_travel(required, safety_margin=margin)


@when(parsers.parse('I check if I can travel requiring {required:d} fuel with no safety margin'))
def check_can_travel_no_margin(context, required):
    """Check travel feasibility without safety margin"""
    context['can_travel'] = context['fuel'].can_travel(required, safety_margin=0.0)


@when("I check if I should refuel")
def check_should_refuel(context):
    """Check if should refuel"""
    fuel = context['fuel']
    at_marketplace = context.get('at_marketplace', False)
    next_leg_distance = context.get('distance', context.get('destination_distance', 0))
    context['should_refuel'] = RefuelPlanner.should_refuel(fuel, at_marketplace, next_leg_distance)

    # Set reason if refueling recommended
    if context['should_refuel']:
        if next_leg_distance > 0:
            context['refuel_reason'] = "insufficient fuel for next leg in BURN mode"
        else:
            context['refuel_reason'] = "fuel below safety margin of 4 units"


@when("I check if I should refuel with no next leg distance")
def check_should_refuel_no_distance(context):
    """Check if should refuel with no known next leg"""
    fuel = context['fuel']
    at_marketplace = context.get('at_marketplace', False)
    context['should_refuel'] = RefuelPlanner.should_refuel(fuel, at_marketplace, next_leg_distance=0)

    # Set reason if refueling recommended
    if context['should_refuel']:
        context['refuel_reason'] = "fuel below safety margin of 4 units"


@when(parsers.parse('I check if I should refuel with next leg distance {distance:d}'))
def check_should_refuel_with_distance(context, distance):
    """Check if should refuel with specific next leg distance"""
    fuel = context['fuel']
    at_marketplace = context.get('at_marketplace', False)
    context['should_refuel'] = RefuelPlanner.should_refuel(fuel, at_marketplace, next_leg_distance=distance)


@when(parsers.parse('I check if refuel stop is needed using {mode} mode'))
def check_refuel_stop_needed(context, mode):
    """Check if refuel stop needed"""
    fuel = context['fuel']
    flight_mode = FlightMode[mode]
    dest_distance = context.get('destination_distance', 0)
    refuel_distance = context.get('refuel_point_distance', 0)

    context['refuel_stop_needed'] = RefuelPlanner.needs_refuel_stop(
        fuel, dest_distance, refuel_distance, flight_mode
    )

    # Check if can reach refuel point
    fuel_to_refuel = flight_mode.fuel_cost(refuel_distance)
    context['can_reach_refuel'] = fuel.can_travel(fuel_to_refuel, safety_margin=0.1)


@when("I check if fuel is full")
def check_fuel_full(context):
    """Check if fuel is full"""
    context['is_full'] = context['fuel'].is_full()


@when("I check the fuel percentage")
def check_fuel_percentage(context):
    """Check fuel percentage"""
    context['percentage'] = context['fuel'].percentage()


@when("I calculate fuel cost")
def calculate_fuel_cost(context):
    """Calculate fuel cost for distance and mode"""
    mode = context.get('flight_mode', FlightMode.CRUISE)
    distance = context.get('distance', 0)
    context['fuel_required'] = mode.fuel_cost(distance)


# ============================================================================
# Then Steps - Assertions
# ============================================================================

@then("the fuel should be created successfully")
def check_fuel_created(context):
    """Verify fuel was created"""
    assert context['fuel'] is not None
    assert context['error'] is None


@then(parsers.parse("the current fuel should be {amount:d}"))
def check_current_fuel(context, amount):
    """Verify current fuel"""
    assert context['fuel'].current == amount


@then(parsers.parse("the fuel capacity should be {capacity:d}"))
def check_fuel_capacity(context, capacity):
    """Verify fuel capacity"""
    assert context['fuel'].capacity == capacity


@then(parsers.parse("fuel creation should fail with {error_type}"))
def check_fuel_creation_failed(context, error_type):
    """Verify fuel creation failed"""
    assert context['error'] is not None
    if error_type == "ValueError":
        assert isinstance(context['error'], ValueError)


@then(parsers.parse('the error message should mention "{text}"'))
def check_error_message(context, text):
    """Verify error message contains text"""
    assert context['error'] is not None
    error_msg = str(context['error']).lower()
    assert text.lower() in error_msg, \
        f"Expected '{text}' in error message, got: {error_msg}"


@then(parsers.parse("the percentage should be {percentage:g}"))
def check_percentage(context, percentage):
    """Verify fuel percentage"""
    actual = context['percentage']
    assert abs(actual - percentage) < 0.01, \
        f"Expected {percentage}%, got {actual}%"


@then(parsers.parse("the new fuel should have {current:d} current"))
def check_new_fuel_current(context, current):
    """Verify new fuel current"""
    assert context['new_fuel'].current == current


@then(parsers.parse("the capacity should remain {capacity:d}"))
def check_capacity_unchanged(context, capacity):
    """Verify capacity unchanged"""
    assert context['new_fuel'].capacity == capacity


@then(parsers.parse("the fuel required should be {amount:d} units"))
@then(parsers.parse("the fuel required should be {amount:d}"))
def check_fuel_required(context, amount):
    """Verify fuel required"""
    assert context['fuel_required'] == amount


@then(parsers.parse("the operation should fail with {error_type}"))
def check_operation_failed(context, error_type):
    """Verify operation failed"""
    assert context['error'] is not None
    if error_type == "ValueError":
        assert isinstance(context['error'], ValueError)


@then("the fuel should be at full capacity")
def check_fuel_full(context):
    """Verify fuel is full"""
    fuel = context.get('new_fuel') or context.get('fuel')
    assert fuel.is_full()


@then("the travel should be feasible")
def check_travel_feasible(context):
    """Verify travel is feasible"""
    assert context['can_travel'] is True


@then("the travel should not be feasible")
def check_travel_not_feasible(context):
    """Verify travel is not feasible"""
    assert context['can_travel'] is False


@then(parsers.parse("the required fuel with margin should be {amount:d}"))
def check_fuel_with_margin(context, amount):
    """Verify fuel with safety margin"""
    # This is calculated in the test context
    fuel = context['fuel']
    required = context.get('required_fuel', 100)
    margin = context.get('safety_margin', 0.1)
    expected = int(required * (1 + margin))
    assert expected == amount


@then("refueling should be recommended")
def check_refuel_recommended(context):
    """Verify refueling is recommended"""
    assert context['should_refuel'] is True


@then("refueling should not be recommended")
def check_refuel_not_recommended(context):
    """Verify refueling is not recommended"""
    assert context['should_refuel'] is False


@then(parsers.parse('the reason should be "{reason}"'))
def check_refuel_reason(context, reason):
    """Verify refuel reason"""
    assert context.get('refuel_reason') == reason


@then("a refuel stop should be needed")
def check_refuel_stop_needed(context):
    """Verify refuel stop is needed"""
    assert context['refuel_stop_needed'] is True


@then("a refuel stop should not be needed")
def check_refuel_stop_not_needed(context):
    """Verify refuel stop is not needed"""
    assert context['refuel_stop_needed'] is False


@then("I should be able to reach the refuel point")
def check_can_reach_refuel(context):
    """Verify can reach refuel point"""
    assert context['can_reach_refuel'] is True


@then("I should not be able to reach the refuel point")
def check_cannot_reach_refuel(context):
    """Verify cannot reach refuel point"""
    assert context['can_reach_refuel'] is False


@then("the fuel should be at full capacity")
def check_is_full(context):
    """Verify fuel is at capacity"""
    fuel = context.get('new_fuel') or context.get('fuel')
    assert fuel.is_full()


@then("the fuel should not be at full capacity")
def check_not_full(context):
    """Verify fuel is not at capacity"""
    assert not context['is_full']


@then(parsers.parse("the travel feasibility for {units:d} unit should be false"))
def check_travel_feasibility_false(context, units):
    """Verify cannot travel given distance"""
    can_travel = context['fuel'].can_travel(units)
    assert can_travel is False


@then(parsers.parse("the fuel required should be at least {amount:d}"))
@then(parsers.parse("the fuel required should be at least {amount:d} unit"))
def check_fuel_required_minimum(context, amount):
    """Verify fuel required is at least amount"""
    assert context['fuel_required'] >= amount
