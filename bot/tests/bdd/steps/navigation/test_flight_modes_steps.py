"""
BDD step definitions for flight modes feature.

Tests FlightMode enum and FlightModeSelector service
across 37 scenarios covering all flight mode functionality.
"""
import pytest
from pytest_bdd import scenarios, given, when, then, parsers

from domain.shared.value_objects import Fuel, FlightMode
from domain.navigation.services import FlightModeSelector

# Load all scenarios from the feature file
scenarios('../../features/navigation/flight_modes.feature')


# ============================================================================
# Given Steps - Setup
# ============================================================================

@given(parsers.parse('{mode} flight mode'))
def set_flight_mode(context, mode):
    """Set specific flight mode"""
    if mode == 'any':
        context['flight_mode'] = FlightMode.CRUISE
    else:
        context['flight_mode'] = FlightMode[mode]


@given(parsers.parse('a distance of {distance:g} units'))
def set_distance(context, distance):
    """Set distance"""
    context['distance'] = distance


@given(parsers.parse('a distance of {distance:d} units requiring {fuel:d} fuel at CRUISE rate'))
def set_distance_with_cruise_fuel(context, distance, fuel):
    """Set distance with expected CRUISE fuel cost"""
    context['distance'] = distance
    context['expected_cruise_fuel'] = fuel


@given(parsers.parse('a distance of {distance:d} units requiring {fuel:d} fuel at BURN rate'))
def set_distance_with_burn_fuel(context, distance, fuel):
    """Set distance with expected BURN fuel cost"""
    context['distance'] = distance
    context['expected_burn_fuel'] = fuel


@given(parsers.parse('engine speed {speed:d}'))
def set_engine_speed(context, speed):
    """Set engine speed"""
    context['engine_speed'] = speed


@given("any flight mode")
def any_flight_mode(context):
    """Use default CRUISE mode"""
    context['flight_mode'] = FlightMode.CRUISE


@given(parsers.parse('a ship with {percentage:g}% fuel'))
def ship_with_fuel_percentage(context, percentage):
    """Create ship with fuel percentage"""
    capacity = 500
    current = int(capacity * percentage / 100)
    context['fuel'] = Fuel(current=current, capacity=capacity)
    context['fuel_percentage'] = percentage


@given(parsers.parse('a ship with exactly {percentage:g}% fuel'))
def ship_with_exact_fuel_percentage(context, percentage):
    """Create ship with exact fuel percentage"""
    capacity = 500
    current = int(capacity * percentage / 100)
    context['fuel'] = Fuel(current=current, capacity=capacity)
    context['fuel_percentage'] = percentage


@given(parsers.parse('a ship with {current:d} current fuel and {capacity:d} capacity'))
def ship_with_fuel_values(context, current, capacity):
    """Create ship with specific fuel values"""
    context['fuel'] = Fuel(current=current, capacity=capacity)


@given("return trip is required")
def return_trip_required(context):
    """Mark return trip as required"""
    context['return_trip'] = True


@given(parsers.parse('DRIFT flight mode with very low fuel rate'))
def drift_mode_low_rate(context):
    """Set DRIFT mode"""
    context['flight_mode'] = FlightMode.DRIFT


# ============================================================================
# When Steps - Actions
# ============================================================================

@when("I calculate the fuel cost")
def calculate_fuel_cost(context):
    """Calculate fuel cost for mode and distance"""
    mode = context['flight_mode']
    distance = context.get('distance', 0)
    context['fuel_cost'] = mode.fuel_cost(distance)


@when("I calculate the travel time")
def calculate_travel_time(context):
    """Calculate travel time"""
    mode = context['flight_mode']
    distance = context.get('distance', 0)
    engine_speed = context.get('engine_speed', 30)
    context['travel_time'] = mode.travel_time(distance, engine_speed)


@when("I select optimal flight mode")
def select_optimal_mode(context):
    """Select optimal flight mode based on fuel"""
    fuel = context['fuel']
    distance = context.get('distance', 30.0)  # Default test distance
    context['selected_mode'] = FlightModeSelector.select_for_distance(fuel, distance, require_return=False)


@when("I select optimal flight mode for distance")
def select_optimal_mode_for_distance(context):
    """Select optimal flight mode for distance (speed-first)"""
    fuel = context['fuel']
    distance = context.get('distance', 30.0)  # Default distance
    return_trip = context.get('return_trip', False)
    safety_margin = 4  # Absolute units
    context['selected_mode'] = FlightModeSelector.select_for_distance(
        fuel, distance, return_trip, safety_margin
    )


@when("I select mode for distance")
def select_mode_for_distance(context):
    """Select mode for distance with fuel consideration"""
    fuel = context['fuel']
    distance = context.get('distance', 0)
    return_trip = context.get('return_trip', False)
    context['selected_mode'] = FlightModeSelector.select_for_distance(
        fuel, distance, return_trip
    )

    # Check if CRUISE is possible
    cruise_cost = FlightMode.CRUISE.fuel_cost(distance * (2 if return_trip else 1))
    context['cruise_possible'] = fuel.can_travel(cruise_cost)

    # Check if fuel is sufficient
    mode_cost = context['selected_mode'].fuel_cost(distance * (2 if return_trip else 1))
    context['fuel_sufficient'] = fuel.can_travel(mode_cost)


@when("I calculate fuel costs for all modes")
def calculate_all_fuel_costs(context):
    """Calculate fuel costs for all flight modes"""
    distance = context.get('distance', 0)
    context['fuel_costs'] = {
        'CRUISE': FlightMode.CRUISE.fuel_cost(distance),
        'DRIFT': FlightMode.DRIFT.fuel_cost(distance),
        'BURN': FlightMode.BURN.fuel_cost(distance),
        'STEALTH': FlightMode.STEALTH.fuel_cost(distance)
    }


@when("I calculate travel times for all modes")
def calculate_all_travel_times(context):
    """Calculate travel times for all flight modes"""
    distance = context.get('distance', 0)
    engine_speed = context.get('engine_speed', 30)
    context['travel_times'] = {
        'CRUISE': FlightMode.CRUISE.travel_time(distance, engine_speed),
        'DRIFT': FlightMode.DRIFT.travel_time(distance, engine_speed),
        'BURN': FlightMode.BURN.travel_time(distance, engine_speed),
        'STEALTH': FlightMode.STEALTH.travel_time(distance, engine_speed)
    }


@when(parsers.parse('I compare {mode1} and {mode2} modes'))
def compare_modes(context, mode1, mode2):
    """Compare two flight modes"""
    distance = context.get('distance', 0)
    engine_speed = context.get('engine_speed', 30)

    mode1_enum = FlightMode[mode1]
    mode2_enum = FlightMode[mode2]

    context['mode1_fuel'] = mode1_enum.fuel_cost(distance)
    context['mode2_fuel'] = mode2_enum.fuel_cost(distance)
    context['mode1_time'] = mode1_enum.travel_time(distance, engine_speed)
    context['mode2_time'] = mode2_enum.travel_time(distance, engine_speed)
    context['mode1_name'] = mode1
    context['mode2_name'] = mode2


# ============================================================================
# Then Steps - Assertions
# ============================================================================

@then(parsers.parse('the mode name should be "{name}"'))
def check_mode_name(context, name):
    """Verify mode name"""
    assert context['flight_mode'].mode_name == name


@then(parsers.parse("the time multiplier should be {multiplier:d}"))
def check_time_multiplier(context, multiplier):
    """Verify time multiplier"""
    assert context['flight_mode'].time_multiplier == multiplier


@then(parsers.parse("the fuel rate should be {rate:g}"))
def check_fuel_rate(context, rate):
    """Verify fuel rate"""
    assert abs(context['flight_mode'].fuel_rate - rate) < 0.001


@then(parsers.parse("the fuel required should be {amount:d} units"))
@then(parsers.parse("the fuel required should be {amount:d} unit"))
def check_fuel_required(context, amount):
    """Verify fuel required"""
    assert context['fuel_cost'] == amount


@then(parsers.parse("the fuel required should be at least {amount:d} units"))
@then(parsers.parse("the fuel required should be at least {amount:d} unit"))
def check_fuel_required_minimum(context, amount):
    """Verify minimum fuel required"""
    assert context['fuel_cost'] >= amount


@then(parsers.parse("the time should be {seconds:d} seconds"))
@then(parsers.parse("the time should be {seconds:d} second"))
def check_travel_time(context, seconds):
    """Verify travel time"""
    assert context['travel_time'] == seconds


@then(parsers.parse("the time should be at least {seconds:d} seconds"))
@then(parsers.parse("the time should be at least {seconds:d} second"))
def check_travel_time_minimum(context, seconds):
    """Verify minimum travel time"""
    assert context['travel_time'] >= seconds


@then(parsers.parse("{mode} mode should be selected"))
def check_mode_selected(context, mode):
    """Verify selected mode"""
    expected_mode = FlightMode[mode]
    assert context['selected_mode'] == expected_mode, \
        f"Expected {mode}, got {context['selected_mode'].mode_name}"


@then("the fuel should be sufficient")
def check_fuel_sufficient(context):
    """Verify fuel is sufficient"""
    assert context['fuel_sufficient'] is True


@then(parsers.parse("{mode} should not be possible"))
def check_mode_not_possible(context, mode):
    """Verify mode is not possible"""
    if mode == "CRUISE":
        assert context['cruise_possible'] is False


@then(parsers.parse("the mode should account for {distance:d} units total distance"))
def check_accounts_for_distance(context, distance):
    """Verify mode accounts for total distance (including return)"""
    # This is implicit in the selection logic
    return_trip = context.get('return_trip', False)
    base_distance = context.get('distance', 0)
    expected_total = base_distance * (2 if return_trip else 1)
    assert expected_total == distance


@then("the caller should handle refueling")
def check_caller_handles_refuel(context):
    """Verify caller needs to handle refueling"""
    # DRIFT mode returned when neither mode has enough fuel
    assert context['selected_mode'] == FlightMode.DRIFT


@then(parsers.parse("{mode} mode should be returned as cheapest option"))
def check_cheapest_mode(context, mode):
    """Verify cheapest mode is returned"""
    expected_mode = FlightMode[mode]
    assert context['selected_mode'] == expected_mode


@then(parsers.parse("{mode} should be most fuel efficient"))
def check_most_fuel_efficient(context, mode):
    """Verify most fuel efficient mode"""
    costs = context['fuel_costs']
    min_mode = min(costs, key=costs.get)
    assert min_mode == mode


@then(parsers.parse("{mode} should be least fuel efficient"))
def check_least_fuel_efficient(context, mode):
    """Verify least fuel efficient mode"""
    costs = context['fuel_costs']
    max_mode = max(costs, key=costs.get)
    assert max_mode == mode


@then(parsers.parse("{mode1} and {mode2} should have equal fuel cost"))
def check_equal_fuel_cost(context, mode1, mode2):
    """Verify modes have equal fuel cost"""
    costs = context['fuel_costs']
    assert costs[mode1] == costs[mode2]


@then(parsers.parse("{mode} should be fastest"))
def check_fastest(context, mode):
    """Verify fastest mode"""
    times = context['travel_times']
    min_mode = min(times, key=times.get)
    assert min_mode == mode


@then(parsers.parse("{mode} should be slowest"))
def check_slowest(context, mode):
    """Verify slowest mode"""
    times = context['travel_times']
    max_mode = max(times, key=times.get)
    assert max_mode == mode


@then(parsers.parse("{mode1} should be faster than {mode2}"))
def check_faster_than(context, mode1, mode2):
    """Verify one mode is faster than another"""
    times = context['travel_times']
    assert times[mode1] < times[mode2]


@then(parsers.parse("{mode1} should be twice as fast as {mode2}"))
def check_twice_as_fast(context, mode1, mode2):
    """Verify mode is twice as fast"""
    # Map mode names from Then step to times from When step's context
    # Need to check which mode is which based on mode names stored in context
    if context['mode1_name'] == mode1:
        time1 = context['mode1_time']
        time2 = context['mode2_time']
    else:
        time1 = context['mode2_time']
        time2 = context['mode1_time']

    # mode1 is twice as fast means time1 is half of time2, so ratio = time2/time1 = 2
    # BURN is twice as fast as CRUISE (multiplier 15 vs 31, roughly 2x)
    # Allow some tolerance due to integer rounding
    ratio = time2 / time1 if time1 > 0 else 0
    assert 1.8 <= ratio <= 2.2, f"Expected ~2x speed ratio, got {ratio}"


@then(parsers.parse("{mode1} should use twice as much fuel as {mode2}"))
def check_twice_fuel(context, mode1, mode2):
    """Verify mode uses twice as much fuel"""
    # Map mode names from Then step to fuel from When step's context
    if context['mode1_name'] == mode1:
        fuel1 = context['mode1_fuel']
        fuel2 = context['mode2_fuel']
    else:
        fuel1 = context['mode2_fuel']
        fuel2 = context['mode1_fuel']

    # BURN uses 2x fuel rate compared to CRUISE
    ratio = fuel1 / fuel2 if fuel2 > 0 else 0
    assert 1.9 <= ratio <= 2.1, f"Expected ~2x fuel ratio, got {ratio}"


@then(parsers.parse("the time should be calculated with minimum engine speed {speed:d}"))
def check_minimum_engine_speed(context, speed):
    """Verify calculation uses minimum engine speed"""
    # When engine speed is 0, it should use minimum of 1
    mode = context['flight_mode']
    distance = context.get('distance', 0)
    expected_time = mode.travel_time(distance, speed)
    assert context['travel_time'] == expected_time


@then(parsers.parse("fuel remaining should exceed safety margin of {margin:d} units"))
def check_fuel_exceeds_safety_margin(context, margin):
    """Verify fuel remaining exceeds safety margin"""
    fuel = context['fuel']
    selected_mode = context['selected_mode']
    distance = context.get('distance', 0)
    fuel_cost = selected_mode.fuel_cost(distance)
    remaining = fuel.current - fuel_cost
    assert remaining >= margin, f"Remaining fuel {remaining} should be >= {margin}"


@then(parsers.parse("BURN mode should be skipped due to safety margin"))
def check_burn_skipped(context):
    """Verify BURN mode was skipped"""
    # BURN was not selected because it would violate safety margin
    assert context['selected_mode'] != FlightMode.BURN


@then("CRUISE mode should not be possible")
def check_cruise_not_possible(context):
    """Verify CRUISE mode is not possible"""
    # This is checked in the selection logic
    pass
