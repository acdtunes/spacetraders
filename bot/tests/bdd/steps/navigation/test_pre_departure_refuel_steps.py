"""
BDD step definitions for pre-departure refuel planning.

Tests the routing engine's ability to detect when a ship is at a fuel station
with insufficient fuel and plan a REFUEL action before departure.

This addresses the critical bug where ships fail navigation with "requires X more fuel"
error even when docked at a fuel station.
"""
import pytest
from pytest_bdd import scenarios, given, when, then, parsers

from domain.shared.value_objects import Waypoint, FlightMode
from adapters.secondary.routing.ortools_engine import ORToolsRoutingEngine

# Load all scenarios from the feature file
scenarios('../../features/navigation/pre_departure_refuel.feature')


# ============================================================================
# Given Steps - Setup
# ============================================================================

@given('the routing engine is initialized')
def initialize_routing_engine(context):
    """Initialize the routing engine with reduced timeouts for fast tests"""
    context['routing_engine'] = ORToolsRoutingEngine(tsp_timeout=1, vrp_timeout=1)
    context['waypoints'] = {}


@given(parsers.parse('a waypoint "{symbol}" at coordinates ({x:d}, {y:d}) with fuel station'))
def create_waypoint_with_fuel(context, symbol, x, y):
    """Create a waypoint with fuel station"""
    waypoint = Waypoint(
        symbol=symbol,
        x=float(x),
        y=float(y),
        system_symbol="X1-GZ7",
        waypoint_type="PLANET",
        traits=("MARKETPLACE",),
        has_fuel=True
    )
    context['waypoints'][symbol] = waypoint


@given(parsers.parse('a waypoint "{symbol}" at coordinates ({x:d}, {y:d}) without fuel'))
def create_waypoint_without_fuel(context, symbol, x, y):
    """Create a waypoint without fuel station"""
    waypoint = Waypoint(
        symbol=symbol,
        x=float(x),
        y=float(y),
        system_symbol="X1-GZ7",
        waypoint_type="PLANET",
        traits=(),
        has_fuel=False
    )
    context['waypoints'][symbol] = waypoint


@given(parsers.parse('a ship with {current_fuel:d} current fuel and {capacity:d} capacity at "{location}"'))
def setup_ship_fuel(context, current_fuel, capacity, location):
    """Set ship fuel parameters"""
    context['current_fuel'] = current_fuel
    context['fuel_capacity'] = capacity
    context['start_location'] = location


@given(parsers.parse("the ship's engine speed is {speed:d}"))
def setup_engine_speed(context, speed):
    """Set ship engine speed"""
    context['engine_speed'] = speed


# ============================================================================
# When Steps - Actions
# ============================================================================

@when(parsers.parse('I plan a route from "{start}" to "{end}"'))
def plan_route(context, start, end):
    """Plan a route using the routing engine"""
    routing_engine = context['routing_engine']
    waypoints = context['waypoints']
    current_fuel = context['current_fuel']
    fuel_capacity = context['fuel_capacity']
    engine_speed = context['engine_speed']

    # Call the routing engine's find_optimal_path method
    route_plan = routing_engine.find_optimal_path(
        graph=waypoints,
        start=start,
        goal=end,
        current_fuel=current_fuel,
        fuel_capacity=fuel_capacity,
        engine_speed=engine_speed,
        prefer_cruise=True
    )

    # Store the result
    context['route_plan'] = route_plan
    context['start'] = start
    context['end'] = end

    # Extract steps for easier assertions
    if route_plan:
        context['steps'] = route_plan.get('steps', [])
    else:
        context['steps'] = None


# ============================================================================
# Then Steps - Assertions
# ============================================================================

@then('the route should include a REFUEL action')
def check_route_has_refuel(context):
    """Verify route includes REFUEL action"""
    steps = context.get('steps')
    assert steps is not None, "Route planning failed - no path found"

    refuel_steps = [s for s in steps if s['action'] == 'REFUEL']
    assert len(refuel_steps) > 0, (
        f"Route does not include REFUEL action. Steps: {steps}"
    )


@then('the REFUEL action should be the first step')
def check_refuel_is_first(context):
    """Verify REFUEL is the first action"""
    steps = context.get('steps')
    assert steps is not None, "Route planning failed - no path found"
    assert len(steps) > 0, "Route has no steps"

    first_step = steps[0]
    assert first_step['action'] == 'REFUEL', (
        f"First step is not REFUEL. First step: {first_step}, All steps: {steps}"
    )


@then(parsers.parse('the REFUEL should be at waypoint "{waypoint}"'))
def check_refuel_waypoint(context, waypoint):
    """Verify REFUEL is at correct waypoint"""
    steps = context.get('steps')
    assert steps is not None, "Route planning failed - no path found"

    refuel_steps = [s for s in steps if s['action'] == 'REFUEL']
    assert len(refuel_steps) > 0, "Route does not include REFUEL action"

    refuel_waypoint = refuel_steps[0]['waypoint']
    assert refuel_waypoint == waypoint, (
        f"REFUEL at {refuel_waypoint}, expected {waypoint}"
    )


@then(parsers.parse('the route should then include a TRAVEL action to "{destination}"'))
def check_travel_to_destination(context, destination):
    """Verify route includes TRAVEL to destination"""
    steps = context.get('steps')
    assert steps is not None, "Route planning failed - no path found"

    travel_steps = [s for s in steps if s['action'] == 'TRAVEL' and s['waypoint'] == destination]
    assert len(travel_steps) > 0, (
        f"Route does not include TRAVEL to {destination}. Steps: {steps}"
    )


@then('the route should not include a REFUEL action')
def check_route_has_no_refuel(context):
    """Verify route does not include REFUEL action"""
    steps = context.get('steps')
    assert steps is not None, "Route planning failed - no path found"

    refuel_steps = [s for s in steps if s['action'] == 'REFUEL']
    assert len(refuel_steps) == 0, (
        f"Route includes unexpected REFUEL action. Steps: {steps}"
    )


@then(parsers.parse('the route should include a TRAVEL action to "{destination}"'))
def check_travel_action(context, destination):
    """Verify route includes TRAVEL to destination"""
    steps = context.get('steps')
    assert steps is not None, "Route planning failed - no path found"

    travel_steps = [s for s in steps if s['action'] == 'TRAVEL' and s['waypoint'] == destination]
    assert len(travel_steps) > 0, (
        f"Route does not include TRAVEL to {destination}. Steps: {steps}"
    )


@then('the REFUEL should not be the first step')
def check_refuel_not_first(context):
    """Verify REFUEL is not the first action"""
    steps = context.get('steps')
    assert steps is not None, "Route planning failed - no path found"
    assert len(steps) > 0, "Route has no steps"

    first_step = steps[0]
    assert first_step['action'] != 'REFUEL', (
        f"First step should not be REFUEL. Steps: {steps}"
    )


@then(parsers.parse('the route should have TRAVEL to "{waypoint}" before REFUEL'))
def check_travel_before_refuel(context, waypoint):
    """Verify TRAVEL to waypoint happens before REFUEL"""
    steps = context.get('steps')
    assert steps is not None, "Route planning failed - no path found"

    # Find indices of TRAVEL to waypoint and REFUEL
    travel_index = None
    refuel_index = None

    for i, step in enumerate(steps):
        if step['action'] == 'TRAVEL' and step['waypoint'] == waypoint:
            travel_index = i
        if step['action'] == 'REFUEL':
            refuel_index = i

    assert travel_index is not None, f"No TRAVEL to {waypoint} found"
    assert refuel_index is not None, "No REFUEL found"
    assert travel_index < refuel_index, (
        f"TRAVEL to {waypoint} should come before REFUEL. Steps: {steps}"
    )


@then(parsers.parse('the reason should be "{reason}"'))
def check_refuel_reason(context, reason):
    """Verify refuel reason (for documentation purposes)"""
    # This step is mainly for documentation in the feature file
    # The routing engine includes REFUEL based on fuel constraints
    steps = context.get('steps')
    assert steps is not None, "Route planning failed - no path found"

    refuel_steps = [s for s in steps if s['action'] == 'REFUEL']
    assert len(refuel_steps) > 0, f"Expected refuel due to: {reason}"
