"""
BDD step definitions for domain services feature.

Tests FlightModeSelector, RefuelPlanner, and RouteValidator services
across 37 scenarios covering all domain service functionality.

Black-box testing approach - tests business logic and rules only.
"""
import pytest
from pytest_bdd import scenarios, given, when, then, parsers

from domain.navigation.services import (
    FlightModeSelector,
    RefuelPlanner,
    RouteValidator
)
from domain.shared.value_objects import Fuel, FlightMode, Waypoint
from domain.navigation.route import RouteSegment

# Load all scenarios from the feature file
scenarios('../../features/domain/domain_services.feature')


# ============================================================================
# Given Steps - Setup
# ============================================================================

@given("the domain services are initialized")
def domain_services_initialized(context):
    """Initialize context for domain services"""
    context['segments'] = []
    context['waypoints'] = {}


@given(parsers.parse("a ship with fuel at {percentage:d} percent"))
def ship_with_fuel_percentage(context, percentage):
    """Create ship with fuel percentage"""
    capacity = 100
    current = int(capacity * percentage / 100)
    context['fuel'] = Fuel(current=current, capacity=capacity)


@given(parsers.parse("a ship with fuel at exactly {percentage:d} percent"))
def ship_with_exact_fuel_percentage(context, percentage):
    """Create ship with exact fuel percentage"""
    capacity = 100
    current = int(capacity * percentage / 100)
    context['fuel'] = Fuel(current=current, capacity=capacity)


@given(parsers.parse("a ship with {current:d} current fuel and {capacity:d} capacity"))
def ship_with_fuel_values(context, current, capacity):
    """Create ship with specific fuel values"""
    context['fuel'] = Fuel(current=current, capacity=capacity)


@given(parsers.parse("a distance of {distance:d} units"))
def set_distance(context, distance):
    """Set distance"""
    context['distance'] = float(distance)


@given("return trip is required")
def return_trip_required(context):
    """Mark return trip as required"""
    context['return_trip'] = True


@given("the ship is at a marketplace")
def ship_at_marketplace(context):
    """Mark ship at marketplace"""
    context['at_marketplace'] = True


@given("the ship is not at a marketplace")
def ship_not_at_marketplace(context):
    """Mark ship not at marketplace"""
    context['at_marketplace'] = False


@given(parsers.parse("a distance to destination of {distance:d} units"))
def distance_to_destination(context, distance):
    """Set distance to destination"""
    context['distance_to_destination'] = float(distance)


@given(parsers.parse("a distance to refuel point of {distance:d} units"))
def distance_to_refuel_point(context, distance):
    """Set distance to refuel point"""
    context['distance_to_refuel_point'] = float(distance)


@given(parsers.parse("flight mode is {mode}"))
def set_flight_mode(context, mode):
    """Set flight mode"""
    context['flight_mode'] = FlightMode[mode]


@given(parsers.parse("waypoint {name} at coordinates {x:d}, {y:d}"))
def create_waypoint(context, name, x, y):
    """Create waypoint at coordinates"""
    waypoint = Waypoint(
        symbol=f"X1-{name}1",
        x=float(x),
        y=float(y),
        system_symbol="X1"
    )
    context['waypoints'][name] = waypoint


@given(parsers.parse("a route segment from {from_wp} to {to_wp} with {distance:d} distance"))
def create_route_segment_with_distance(context, from_wp, to_wp, distance):
    """Create route segment with distance"""
    from_waypoint = context['waypoints'][from_wp]
    to_waypoint = context['waypoints'][to_wp]

    segment = RouteSegment(
        from_waypoint=from_waypoint,
        to_waypoint=to_waypoint,
        distance=float(distance),
        fuel_required=50,  # Default fuel
        travel_time=100,
        flight_mode=FlightMode.CRUISE
    )
    context['segments'].append(segment)


@given(parsers.parse("a route segment from {from_wp} to {to_wp} requiring {fuel:d} fuel"))
def create_route_segment_with_fuel(context, from_wp, to_wp, fuel):
    """Create route segment with specific fuel requirement"""
    from_waypoint = context['waypoints'][from_wp]
    to_waypoint = context['waypoints'][to_wp]

    segment = RouteSegment(
        from_waypoint=from_waypoint,
        to_waypoint=to_waypoint,
        distance=100.0,
        fuel_required=fuel,
        travel_time=100,
        flight_mode=FlightMode.CRUISE
    )
    context['segments'].append(segment)


@given("no route segments")
def no_route_segments(context):
    """Clear route segments"""
    context['segments'] = []


@given(parsers.parse("ship fuel capacity is {capacity:d}"))
def set_ship_fuel_capacity(context, capacity):
    """Set ship fuel capacity"""
    context['ship_fuel_capacity'] = capacity


# ============================================================================
# When Steps - Actions
# ============================================================================

@when("I select flight mode based on fuel")
def select_mode_by_fuel(context):
    """Select flight mode based on current fuel"""
    fuel = context['fuel']
    distance = context.get('distance', 30)  # Default test distance
    context['selected_mode'] = FlightModeSelector.select_for_distance(fuel, distance, require_return=False)


@when("I select flight mode for distance")
def select_mode_for_distance(context):
    """Select flight mode for distance"""
    fuel = context['fuel']
    distance = context.get('distance', 0.0)
    context['selected_mode'] = FlightModeSelector.select_for_distance(
        fuel, distance, require_return=False
    )


@when("I select flight mode for distance with return")
def select_mode_for_distance_with_return(context):
    """Select flight mode for distance with return trip"""
    fuel = context['fuel']
    distance = context.get('distance', 0.0)
    require_return = context.get('return_trip', False)
    context['selected_mode'] = FlightModeSelector.select_for_distance(
        fuel, distance, require_return=require_return
    )


@when("I check if ship should refuel")
def check_should_refuel(context):
    """Check if ship should refuel"""
    fuel = context['fuel']
    at_marketplace = context.get('at_marketplace', False)
    next_leg_distance = context.get('distance', 0)
    context['should_refuel'] = RefuelPlanner.should_refuel(fuel, at_marketplace, next_leg_distance)


@when("I check if refuel stop is needed")
def check_refuel_stop_needed(context):
    """Check if refuel stop is needed"""
    fuel = context['fuel']
    distance_to_destination = context.get('distance_to_destination', 0.0)
    distance_to_refuel = context.get('distance_to_refuel_point', 0.0)
    mode = context.get('flight_mode', FlightMode.CRUISE)

    context['needs_refuel_stop'] = RefuelPlanner.needs_refuel_stop(
        fuel=fuel,
        distance_to_destination=distance_to_destination,
        distance_to_refuel_point=distance_to_refuel,
        mode=mode
    )


@when("I validate segments are connected")
def validate_segments_connected(context):
    """Validate that route segments are connected"""
    segments = context.get('segments', [])
    context['segments_valid'] = RouteValidator.validate_segments_connected(segments)


@when("I validate fuel capacity")
def validate_fuel_capacity(context):
    """Validate fuel capacity for segments"""
    segments = context.get('segments', [])
    capacity = context.get('ship_fuel_capacity', 100)
    context['fuel_capacity_valid'] = RouteValidator.validate_fuel_capacity(
        segments, capacity
    )


@when("I check the FlightModeSelector safety margin")
def check_flight_mode_selector_safety_margin(context):
    """Get FlightModeSelector safety margin"""
    context['safety_margin'] = FlightModeSelector.SAFETY_MARGIN


@when("I check the RefuelPlanner safety margin")
def check_refuel_planner_safety_margin(context):
    """Get RefuelPlanner safety margin"""
    context['safety_margin'] = RefuelPlanner.SAFETY_MARGIN


# ============================================================================
# Then Steps - Assertions
# ============================================================================

@then(parsers.parse("{mode} mode should be selected"))
def check_mode_selected(context, mode):
    """Verify selected mode"""
    expected_mode = FlightMode[mode]
    assert context['selected_mode'] == expected_mode, \
        f"Expected {mode}, got {context['selected_mode'].mode_name}"


@then("refuel should be recommended")
def check_refuel_recommended(context):
    """Verify refuel is recommended"""
    assert context['should_refuel'] is True


@then("refuel should not be recommended")
def check_refuel_not_recommended(context):
    """Verify refuel is not recommended"""
    assert context['should_refuel'] is False


@then("refuel stop should be needed")
def check_refuel_stop_needed(context):
    """Verify refuel stop is needed"""
    assert context['needs_refuel_stop'] is True


@then("refuel stop should not be needed")
def check_refuel_stop_not_needed(context):
    """Verify refuel stop is not needed"""
    assert context['needs_refuel_stop'] is False


@then("refuel stop should not be needed due to safety margin")
def check_refuel_stop_not_needed_safety(context):
    """Verify refuel stop is not needed due to safety margin"""
    assert context['needs_refuel_stop'] is False


@then("refuel stop should not be needed because cannot reach refuel point")
def check_refuel_stop_cannot_reach(context):
    """Verify refuel stop not needed because can't reach refuel point"""
    assert context['needs_refuel_stop'] is False


@then("segments should be valid")
def check_segments_valid(context):
    """Verify segments are valid"""
    assert context['segments_valid'] is True


@then("segments should not be valid")
def check_segments_not_valid(context):
    """Verify segments are not valid"""
    assert context['segments_valid'] is False


@then("fuel capacity should be sufficient")
def check_fuel_capacity_sufficient(context):
    """Verify fuel capacity is sufficient"""
    assert context['fuel_capacity_valid'] is True


@then("fuel capacity should not be sufficient")
def check_fuel_capacity_insufficient(context):
    """Verify fuel capacity is not sufficient"""
    assert context['fuel_capacity_valid'] is False


@then(parsers.parse("the safety margin should be {value:d} units"))
@then(parsers.parse("the safety margin should be {value:d}"))
def check_safety_margin_value(context, value):
    """Verify safety margin value"""
    assert context['safety_margin'] == value, \
        f"Expected safety margin {value}, got {context['safety_margin']}"
