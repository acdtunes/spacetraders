#!/usr/bin/env python3
"""
Step definitions for hop minimization BDD tests

Tests that the routing algorithm minimizes navigation hops when prefer_cruise=True
by using a sufficiently high LEG_COMPLEXITY_PENALTY.
"""
import sys
from pathlib import Path
import pytest
from pytest_bdd import scenarios, given, when, then, parsers
import tempfile

sys.path.insert(0, str(Path(__file__).parent.parent / "src"))
sys.path.insert(0, str(Path(__file__).parent))

import spacetraders_bot  # noqa: F401 - ensures compat aliases are registered
from bdd_table_utils import table_to_rows
from mock_api import MockAPIClient
from spacetraders_bot.core.routing import GraphBuilder, RouteOptimizer

# Load all scenarios from the feature file
scenarios('features/hop_minimization.feature')


@pytest.fixture
def context():
    """Shared test context"""
    return {
        'mock_api': None,
        'graph': None,
        'graph_builder': None,
        'route_optimizer': None,
        'ship_data': None,
        'route': None,
        'temp_dir': None
    }


@given("the SpaceTraders API is mocked", target_fixture="mock_api")
def mock_api(context):
    """Create mock API client"""
    context['mock_api'] = MockAPIClient()
    context['temp_dir'] = tempfile.mkdtemp()
    return context['mock_api']


@given(parsers.parse('waypoints exist:\n{table}'))
@given('waypoints exist:')
def create_waypoints_from_table(context, table=None, datatable=None):
    """Create waypoints from Gherkin table data."""
    rows = table_to_rows(table, datatable)

    if not rows:
        return

    headers = rows[0]
    data_rows = rows[1:]

    for cells in data_rows:
        row = dict(zip(headers, cells))

        symbol = row['symbol']
        wp_type = row.get('type', 'PLANET')
        x = int(float(row.get('x', 0)))
        y = int(float(row.get('y', 0)))
        traits_field = row.get('traits', '')
        traits = [t.strip() for t in traits_field.split(',') if t.strip()]

        context['mock_api'].add_waypoint(symbol, wp_type, x, y, traits)


@given(parsers.parse('a ship "{ship_symbol}" at "{waypoint}" with {current:d}/{capacity:d} fuel'))
def create_ship_for_routing(context, ship_symbol, waypoint, current, capacity):
    """Create ship data for routing"""
    context['mock_api'].set_ship_location(ship_symbol, waypoint, "IN_ORBIT")
    context['mock_api'].set_ship_fuel(ship_symbol, current, capacity)
    context['ship_data'] = context['mock_api'].get_ship(ship_symbol)


def _format_step(step):
    if step['action'] == 'navigate':
        return f"{step['from']}->{step['to']}"
    if step['action'] == 'refuel':
        return f"refuel@{step.get('waypoint', '?')}"
    return step.get('action', 'unknown')


def _format_steps(route):
    return [_format_step(step) for step in route.get('steps', [])]


@when(parsers.parse('I build a navigation graph for system "{system}"'))
def build_navigation_graph(context, system):
    """Build navigation graph from waypoints"""
    import os
    db_path = os.path.join(context['temp_dir'], 'test_hop_minimization.db')
    context['graph_builder'] = GraphBuilder(context['mock_api'], db_path=db_path)
    context['graph'] = context['graph_builder'].build_system_graph(system)


@when(parsers.parse('I plan a route from "{start}" to "{goal}" with prefer_cruise'))
def plan_route_prefer_cruise(context, start, goal):
    """Plan route using route optimizer with prefer_cruise=True"""
    context['route_optimizer'] = RouteOptimizer(context['graph'], context['ship_data'])
    current_fuel = context['ship_data']['fuel']['current']
    context['route'] = context['route_optimizer'].find_optimal_route(
        start, goal, current_fuel=current_fuel, prefer_cruise=True
    )


@then("the route should exist")
def route_should_exist(context):
    """Verify route was found"""
    assert context['route'] is not None, "Route should exist"


@then(parsers.parse('the route should use at most {max_legs:d} navigation legs'))
def route_should_use_at_most_legs(context, max_legs):
    """Verify route uses at most N navigation legs"""
    route = context['route']
    assert route is not None, "Route should exist"

    # Count navigation steps (not refuel steps)
    nav_steps = [s for s in route['steps'] if s['action'] == 'navigate']
    actual_legs = len(nav_steps)

    step_summaries = _format_steps(route)

    assert actual_legs <= max_legs, (
        f"Route should use at most {max_legs} navigation legs, but uses {actual_legs}. "
        f"Steps: {step_summaries}"
    )


@then(parsers.parse('the route should use exactly {exact_legs:d} navigation leg'))
@then(parsers.parse('the route should use exactly {exact_legs:d} navigation legs'))
def route_should_use_exactly_legs(context, exact_legs):
    """Verify route uses exactly N navigation legs"""
    route = context['route']
    assert route is not None, "Route should exist"

    nav_steps = [s for s in route['steps'] if s['action'] == 'navigate']
    actual_legs = len(nav_steps)

    step_summaries = _format_steps(route)

    assert actual_legs == exact_legs, (
        f"Route should use exactly {exact_legs} navigation legs, but uses {actual_legs}. "
        f"Steps: {step_summaries}"
    )


@then("the route should only use CRUISE mode")
def route_should_only_use_cruise(context):
    """Verify route only uses CRUISE mode (no DRIFT)"""
    route = context['route']
    assert route is not None, "Route should exist"

    nav_steps = [s for s in route['steps'] if s['action'] == 'navigate']
    drift_steps = [s for s in nav_steps if s['mode'] == 'DRIFT']

    assert len(drift_steps) == 0, (
        f"Route should only use CRUISE mode, but has {len(drift_steps)} DRIFT legs: "
        f"{[_format_step(s) for s in drift_steps]}"
    )


@then(parsers.parse('the route should include at most {max_refuels:d} refuel stop'))
@then(parsers.parse('the route should include at most {max_refuels:d} refuel stops'))
def route_should_include_at_most_refuels(context, max_refuels):
    """Verify route includes at most N refuel stops"""
    route = context['route']
    assert route is not None, "Route should exist"

    refuel_steps = [s for s in route['steps'] if s['action'] == 'refuel']
    actual_refuels = len(refuel_steps)

    assert actual_refuels <= max_refuels, (
        f"Route should include at most {max_refuels} refuel stops, but has {actual_refuels}: "
        f"{[_format_step(s) for s in refuel_steps]}"
    )


@then("the route should have no refuel stops")
def route_should_have_no_refuels(context):
    """Verify route has no refuel stops"""
    route = context['route']
    assert route is not None, "Route should exist"

    refuel_steps = [s for s in route['steps'] if s['action'] == 'refuel']

    assert len(refuel_steps) == 0, (
        f"Route should have no refuel stops, but has {len(refuel_steps)}: "
        f"{[_format_step(s) for s in refuel_steps]}"
    )


@then(parsers.parse('the route should include exactly {exact_refuels:d} refuel stop at start'))
def route_should_include_exact_refuel_at_start(context, exact_refuels):
    """Verify route includes exactly N refuel stops at the start"""
    route = context['route']
    assert route is not None, "Route should exist"

    refuel_steps = [s for s in route['steps'] if s['action'] == 'refuel']
    actual_refuels = len(refuel_steps)

    assert actual_refuels == exact_refuels, \
        f"Route should include exactly {exact_refuels} refuel stops, but has {actual_refuels}"

    # Verify refuel is at the start (step 0)
    if exact_refuels > 0:
        assert route['steps'][0]['action'] == 'refuel', \
            "First step should be refuel"


@then("the route should not visit intermediate asteroids")
def route_should_not_visit_intermediate_asteroids(context):
    """Verify route doesn't visit intermediate asteroids"""
    route = context['route']
    assert route is not None, "Route should exist"

    nav_steps = [s for s in route['steps'] if s['action'] == 'navigate']

    # Check that no waypoint contains 'B2', 'C3', 'E5', 'F6' (intermediate asteroids)
    intermediate_waypoints = ['B2', 'C3', 'E5', 'F6']

    for step in nav_steps:
        to_wp = step['to']
        for intermediate in intermediate_waypoints:
            assert intermediate not in to_wp, \
                f"Route should not visit intermediate asteroid, but visits {to_wp}"
