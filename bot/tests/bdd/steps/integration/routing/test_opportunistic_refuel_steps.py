"""Step definitions for opportunistic refueling routing engine tests."""
import pytest
from pytest_bdd import given, when, then, parsers, scenarios
from domain.shared.value_objects import Waypoint, FlightMode
from adapters.secondary.routing.ortools_engine import ORToolsRoutingEngine

scenarios('../../../features/integration/routing/opportunistic_refuel.feature')


@pytest.fixture
def context():
    """Test context for sharing state between steps."""
    return {}


@given(parsers.parse('a system "{system_symbol}" with waypoints'))
def system_with_waypoints(context, system_symbol, datatable):
    """Create waypoints from table."""
    headers = datatable[0]
    waypoints = []

    for row in datatable[1:]:
        row_dict = dict(zip(headers, row))
        wp = Waypoint(
            symbol=row_dict['symbol'],
            waypoint_type=row_dict['type'],
            x=int(row_dict['x']),
            y=int(row_dict['y']),
            has_fuel=row_dict['has_fuel'].lower() == 'true'
        )
        waypoints.append(wp)

    context['waypoints'] = waypoints
    context['system_symbol'] = system_symbol


@given(parsers.parse('a ship with {capacity:d} fuel capacity'))
def ship_fuel_capacity(context, capacity):
    """Set ship fuel capacity."""
    context['fuel_capacity'] = capacity


@given(parsers.parse('the ship has {current:d} current fuel'))
def ship_current_fuel(context, current):
    """Set ship current fuel."""
    context['current_fuel'] = current


@given(parsers.parse('the ship is at waypoint "{waypoint_symbol}" with fuel'))
def ship_at_waypoint_with_fuel(context, waypoint_symbol):
    """Set ship starting location."""
    context['start'] = waypoint_symbol


@when(parsers.parse('I plan a route from "{start}" to "{goal}"'))
def plan_route(context, start, goal):
    """Plan a route using the routing engine."""
    engine = ORToolsRoutingEngine()

    # Create graph from waypoints stored in context
    graph = {wp.symbol: wp for wp in context['waypoints']}

    # Default engine speed for testing
    engine_speed = 30

    route = engine.find_optimal_path(
        graph=graph,
        start=start,
        goal=goal,
        current_fuel=context['current_fuel'],
        fuel_capacity=context['fuel_capacity'],
        engine_speed=engine_speed,
        prefer_cruise=True
    )

    context['route'] = route


@when(parsers.parse('I plan a route from "{start}" to "{goal}" via "{intermediate}"'))
def plan_route_via(context, start, goal, intermediate):
    """Plan a route with intermediate waypoint."""
    engine = ORToolsRoutingEngine()

    # Create graph from waypoints stored in context
    graph = {wp.symbol: wp for wp in context['waypoints']}

    # Default engine speed for testing
    engine_speed = 30

    # Note: The routing engine will automatically route through intermediate
    # waypoints if needed based on fuel constraints
    route = engine.find_optimal_path(
        graph=graph,
        start=start,
        goal=goal,
        current_fuel=context['current_fuel'],
        fuel_capacity=context['fuel_capacity'],
        engine_speed=engine_speed,
        prefer_cruise=True
    )

    context['route'] = route


@then(parsers.parse('the route should include a REFUEL action at "{waypoint_symbol}"'))
def route_includes_refuel(context, waypoint_symbol):
    """Verify route includes refuel action at waypoint."""
    route = context['route']

    # Check if any step has a refuel action at the waypoint
    has_refuel = False
    for step in route['steps']:
        if step['action'] == 'REFUEL' and step['waypoint'] == waypoint_symbol:
            has_refuel = True
            break

    assert has_refuel, f"Route should include REFUEL action at {waypoint_symbol}, but it doesn't. Steps: {route['steps']}"


@then(parsers.parse('the route should NOT include a REFUEL action at "{waypoint_symbol}"'))
def route_does_not_include_refuel(context, waypoint_symbol):
    """Verify route does NOT include refuel action at waypoint."""
    route = context['route']

    # Check if any step has a refuel action at the waypoint
    has_refuel = False
    for step in route['steps']:
        if step['action'] == 'REFUEL' and step['waypoint'] == waypoint_symbol:
            has_refuel = True
            break

    assert not has_refuel, f"Route should NOT include REFUEL action at {waypoint_symbol}, but it does. Steps: {route['steps']}"


@then('the ship should have sufficient fuel for the journey')
def ship_has_sufficient_fuel(context):
    """Verify ship has enough fuel for the entire journey."""
    route = context['route']

    # Simulate the journey and track fuel
    current_fuel = context['current_fuel']
    fuel_capacity = context['fuel_capacity']

    for step in route['steps']:
        # If refueling, fill tank
        if step['action'] == 'REFUEL':
            current_fuel = fuel_capacity

        # Deduct fuel for travel
        if step['action'] == 'TRAVEL':
            current_fuel -= step['fuel_cost']

            # Verify we never go negative
            assert current_fuel >= 0, f"Ship ran out of fuel during step: {step}"
