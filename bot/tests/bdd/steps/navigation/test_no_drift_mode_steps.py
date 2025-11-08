"""Step definitions for no DRIFT mode tests"""
import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from typing import Dict
from domain.shared.value_objects import Waypoint, FlightMode
from adapters.secondary.routing.ortools_engine import ORToolsRoutingEngine

scenarios('../../features/navigation/no_drift_mode.feature')


@pytest.fixture
def context():
    """Test context"""
    return {}


@given("the routing engine is initialized")
def init_routing_engine(context):
    """Initialize routing engine"""
    context['routing_engine'] = ORToolsRoutingEngine()


@given('a waypoint graph with the following waypoints:', target_fixture='waypoint_graph')
def create_waypoint_graph(datatable, context):
    """Create waypoint graph from table"""
    graph: Dict[str, Waypoint] = {}

    # Skip header row - datatable is a list of lists
    for row in datatable[1:]:
        symbol = row[0]
        x = float(row[1])
        y = float(row[2])
        has_fuel = row[3].lower() == 'true'

        graph[symbol] = Waypoint(
            symbol=symbol,
            x=x,
            y=y,
            has_fuel=has_fuel
        )

    context['graph'] = graph
    return graph


@given(parsers.parse('a ship with fuel capacity {capacity:d} and current fuel {current:d}'))
def set_ship_fuel(capacity: int, current: int, context):
    """Set ship fuel parameters"""
    context['fuel_capacity'] = capacity
    context['current_fuel'] = current


@given(parsers.parse("the ship's engine speed is {speed:d}"))
def set_engine_speed(speed: int, context):
    """Set ship engine speed"""
    context['engine_speed'] = speed


@when(parsers.parse('I calculate a route from "{start}" to "{goal}"'))
def calculate_route(start: str, goal: str, context):
    """Calculate route using routing engine"""
    engine = context['routing_engine']

    # Call the actual routing engine
    result = engine.find_optimal_path(
        context['graph'],
        start,
        goal,
        context['current_fuel'],
        context['fuel_capacity'],
        context['engine_speed'],
        prefer_cruise=False  # Allow BURN mode
    )

    context['route_result'] = result


@then("the route should be found")
def route_should_be_found(context):
    """Verify route was found"""
    assert context['route_result'] is not None, "Route should be found but was None"


@then("the route should use only BURN or CRUISE modes")
def route_uses_only_burn_or_cruise(context):
    """Verify all travel steps use BURN or CRUISE, never DRIFT"""
    route = context['route_result']
    assert route is not None, "Route is None"

    travel_steps = [step for step in route['steps'] if step['action'] == 'TRAVEL']
    assert len(travel_steps) > 0, "Route should have at least one travel step"

    for i, step in enumerate(travel_steps):
        mode = step.get('mode')
        assert mode is not None, f"Step {i} has no mode"
        assert mode in (FlightMode.BURN, FlightMode.CRUISE), \
            f"Step {i} uses invalid mode {mode}, expected BURN or CRUISE only"


@then("the route should not use DRIFT mode")
def route_should_not_use_drift(context):
    """Verify no steps use DRIFT mode"""
    route = context['route_result']
    assert route is not None, "Route is None"

    for i, step in enumerate(route['steps']):
        if step['action'] == 'TRAVEL':
            mode = step.get('mode')
            assert mode != FlightMode.DRIFT, \
                f"Step {i} illegally uses DRIFT mode (waypoint: {step.get('waypoint')}, " \
                f"distance: {step.get('distance', 0):.1f})"


@then("the route should include refuel stops")
def route_includes_refuel_stops(context):
    """Verify route includes at least one refuel"""
    route = context['route_result']
    assert route is not None, "Route is None"

    refuel_steps = [step for step in route['steps'] if step['action'] == 'REFUEL']
    assert len(refuel_steps) > 0, "Route should include at least one refuel stop"


@then("the route should use only BURN mode")
def route_uses_only_burn(context):
    """Verify all travel steps use BURN mode"""
    route = context['route_result']
    assert route is not None, "Route is None"

    travel_steps = [step for step in route['steps'] if step['action'] == 'TRAVEL']
    assert len(travel_steps) > 0, "Route should have at least one travel step"

    for i, step in enumerate(travel_steps):
        mode = step.get('mode')
        assert mode == FlightMode.BURN, \
            f"Step {i} should use BURN mode, got {mode}"


@then("the route should use CRUISE mode for at least one segment")
def route_uses_cruise_for_at_least_one_segment(context):
    """Verify at least one travel step uses CRUISE mode"""
    route = context['route_result']
    assert route is not None, "Route is None"

    travel_steps = [step for step in route['steps'] if step['action'] == 'TRAVEL']
    assert len(travel_steps) > 0, "Route should have at least one travel step"

    cruise_steps = [step for step in travel_steps if step.get('mode') == FlightMode.CRUISE]
    assert len(cruise_steps) > 0, \
        f"Expected at least one CRUISE segment, but all segments used: " \
        f"{[step.get('mode') for step in travel_steps]}"
