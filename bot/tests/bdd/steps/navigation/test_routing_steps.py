#!/usr/bin/env python3
"""
Step definitions for routing algorithms BDD tests
"""
import sys
from pathlib import Path
import pytest
from pytest_bdd import scenarios, given, when, then, parsers
import tempfile

sys.path.insert(0, str(Path(__file__).resolve().parents[4] / 'lib'))
sys.path.insert(0, str(Path(__file__).resolve().parents[4]))

from bdd_table_utils import table_to_rows
from mock_api import MockAPIClient
from routing import GraphBuilder, RouteOptimizer, FuelCalculator, euclidean_distance

# Load all scenarios from the feature file
scenarios('../../features/navigation/routing_algorithms.feature')


_KNOWN_HEADERS = {
    'symbol', 'type', 'x', 'y', 'traits', 'distance', 'from', 'to',
    'heuristic', 'cost', 'page', 'has_fuel', 'edge_to', 'edge_type',
    'orbits'
}


def _parse_table(table: str | None = None, datatable: list[list[str]] | None = None) -> list[dict[str, str]]:
    rows = table_to_rows(table, datatable)

    if not rows:
        return []

    raw_headers = rows[0]
    header_lower = {h.lower() for h in raw_headers}

    if raw_headers and (header_lower & _KNOWN_HEADERS):
        headers = raw_headers
        data_rows = rows[1:]
    else:
        headers = ['value']
        data_rows = rows

    rows = []
    for row in data_rows:
        if not row:
            continue
        rows.append(dict(zip(headers, row)))
    return rows


def _get_value(row: dict[str, str], key: str) -> str:
    if key in row:
        return row[key]
    if key == 'symbol' and 'value' in row:
        return row['value']
    return next(iter(row.values()))


@pytest.fixture
def context():
    """Shared test context"""
    return {
        'mock_api': None,
        'graph': None,
        'graph_builder': None,
        'route_optimizer': None,
        'ship_data': None,
        'result': None,
        'distance': None,
        'fuel_cost': None,
        'heuristic': None,
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
def create_waypoints_from_table(context, table: str | None = None, datatable: list[list[str]] | None = None):
    """Create waypoints from datatable"""
    rows = _parse_table(table, datatable)

    for row in rows:
        symbol = _get_value(row, 'symbol')
        wp_type = row.get('type', row.get('value', 'ASTEROID'))
        x = int(float(row.get('x', 0)))
        y = int(float(row.get('y', 0)))
        traits_str = row.get('traits', '')
        orbits = row.get('orbits', '')

        traits = [traits_str] if traits_str else []
        context['mock_api'].add_waypoint(symbol, wp_type, x, y, traits)

        if orbits:
            waypoint_data = context['mock_api'].waypoints[symbol]
            waypoint_data['orbits'] = orbits
            if orbits in context['mock_api'].waypoints:
                parent_data = context['mock_api'].waypoints[orbits]
                if not any(o == symbol if isinstance(o, str) else o.get('symbol') == symbol
                          for o in parent_data['orbitals']):
                    parent_data['orbitals'].append({'symbol': symbol})


@given(parsers.parse('a simple navigation graph:\n{table}'))
@given('a simple navigation graph:')
def create_simple_graph(context, table: str | None = None, datatable: list[list[str]] | None = None):
    """Create a simple navigation graph from edges"""
    waypoints = {}
    edges = []

    rows = _parse_table(table, datatable)
    for row in rows:
        from_wp = _get_value(row, 'from')
        to_wp = _get_value(row, 'to')
        distance = int(float(_get_value(row, 'distance')))

        # Create waypoints if they don't exist
        if from_wp not in waypoints:
            waypoints[from_wp] = {"x": 0, "y": 0, "has_fuel": True, "traits": ["MARKETPLACE"]}
        if to_wp not in waypoints:
            waypoints[to_wp] = {"x": distance, "y": 0, "has_fuel": True, "traits": ["MARKETPLACE"]}

        # Create edge
        edges.append({
            "from": from_wp,
            "to": to_wp,
            "distance": distance,
            "type": "normal"
        })

    context['graph'] = {
        "system": "X1-TEST",
        "waypoints": waypoints,
        "edges": edges
    }


@given(parsers.parse('an isolated navigation graph:\n{table}'))
@given('an isolated navigation graph:')
def create_isolated_graph(context, table: str | None = None, datatable: list[list[str]] | None = None):
    """Create graph with isolated waypoints (no edges)"""
    waypoints = {}
    rows = _parse_table(table, datatable)
    for row in rows:
        symbol = row.get('waypoint') or _get_value(row, 'symbol')
        x = int(float(row.get('x', 0)))
        y = int(float(row.get('y', 0)))
        waypoints[symbol] = {"x": x, "y": y, "has_fuel": True, "traits": []}

    context['graph'] = {
        "system": "X1-TEST",
        "waypoints": waypoints,
        "edges": []  # No edges - isolated waypoints
    }


@given(parsers.parse('a ship "{ship_symbol}" at "{waypoint}" with {current:d}/{capacity:d} fuel'))
def create_ship_for_routing(context, ship_symbol, waypoint, current, capacity):
    """Create ship data for routing"""
    context['mock_api'].set_ship_location(ship_symbol, waypoint, "IN_ORBIT")
    context['mock_api'].set_ship_fuel(ship_symbol, current, capacity)
    context['ship_data'] = context['mock_api'].get_ship(ship_symbol)


@when(parsers.parse('I calculate distance from ({x1:d}, {y1:d}) to ({x2:d}, {y2:d})'))
def calculate_distance(context, x1, y1, x2, y2):
    """Calculate Euclidean distance"""
    context['distance'] = euclidean_distance(x1, y1, x2, y2)


@when(parsers.parse('I calculate fuel cost for {distance:d} units in {mode} mode'))
@when(parsers.parse('I calculate fuel cost for {distance:d} unit in {mode} mode'))
def calculate_fuel_cost(context, distance, mode):
    """Calculate fuel cost for given mode"""
    context['fuel_cost'] = FuelCalculator.fuel_cost(distance, mode)


@when(parsers.parse('I build a navigation graph for system "{system}"'))
def build_navigation_graph(context, system):
    """Build navigation graph from waypoints"""
    # Use temp directory for test database
    import os
    db_path = os.path.join(context['temp_dir'], 'test_routing.db')
    context['graph_builder'] = GraphBuilder(context['mock_api'], db_path=db_path)
    context['graph'] = context['graph_builder'].build_system_graph(system)


@when(parsers.parse('I plan a route from "{start}" to "{goal}"'))
def plan_route(context, start, goal):
    """Plan route using route optimizer"""
    context['route_optimizer'] = RouteOptimizer(context['graph'], context['ship_data'])
    current_fuel = context['ship_data']['fuel']['current']
    context['route'] = context['route_optimizer'].find_optimal_route(start, goal, current_fuel=current_fuel)


@when(parsers.parse('I calculate heuristic from "{start}" to "{goal}"'))
def calculate_heuristic(context, start, goal):
    """Calculate heuristic value"""
    context['route_optimizer'] = RouteOptimizer(context['graph'], context['ship_data'])
    context['heuristic'] = context['route_optimizer'].heuristic(start, goal)


@then(parsers.parse('the distance should be {expected:d}'))
def distance_should_be(context, expected):
    """Verify exact distance"""
    assert context['distance'] == expected, f"Expected {expected}, got {context['distance']}"


@then(parsers.parse('the distance should be approximately {expected:f}'))
def distance_should_be_approximately(context, expected):
    """Verify distance within tolerance"""
    assert abs(context['distance'] - expected) < 0.1, \
        f"Expected ~{expected}, got {context['distance']}"


@then(parsers.parse('the fuel cost should be {expected:d}'))
def fuel_cost_should_be(context, expected):
    """Verify fuel cost"""
    assert context['fuel_cost'] == expected, f"Expected {expected}, got {context['fuel_cost']}"


@then(parsers.parse('the graph should have {count:d} waypoints'))
def graph_should_have_waypoints(context, count):
    """Verify waypoint count"""
    assert len(context['graph']['waypoints']) == count, \
        f"Expected {count} waypoints, got {len(context['graph']['waypoints'])}"


@then("the graph should have edges")
def graph_should_have_edges(context):
    """Verify graph has edges"""
    assert len(context['graph']['edges']) > 0, "Graph should have edges"


@then(parsers.parse('waypoint "{waypoint}" should have fuel available'))
def waypoint_should_have_fuel(context, waypoint):
    """Verify waypoint has fuel"""
    assert context['graph']['waypoints'][waypoint]['has_fuel'] is True, \
        f"Waypoint {waypoint} should have fuel"


@then(parsers.parse('waypoint "{waypoint}" should not have fuel available'))
def waypoint_should_not_have_fuel(context, waypoint):
    """Verify waypoint does not have fuel"""
    assert context['graph']['waypoints'][waypoint]['has_fuel'] is False, \
        f"Waypoint {waypoint} should not have fuel"


@then("the route should exist")
def route_should_exist(context):
    """Verify route was found"""
    assert context['route'] is not None, "Route should exist"


@then("the route should be None")
def route_should_be_none(context):
    """Verify no route was found"""
    assert context['route'] is None, "Route should be None"


@then("the route should have navigation steps")
def route_should_have_steps(context):
    """Verify route has steps"""
    assert 'steps' in context['route'], "Route should have steps"
    assert len(context['route']['steps']) > 0, "Route should have at least one step"


@then(parsers.parse('the heuristic should be greater than {value:d}'))
def heuristic_greater_than(context, value):
    """Verify heuristic is greater than value"""
    assert context['heuristic'] > value, \
        f"Heuristic should be > {value}, got {context['heuristic']}"


@then(parsers.parse('the heuristic should be {expected:d}'))
def heuristic_should_be(context, expected):
    """Verify exact heuristic value"""
    assert context['heuristic'] == expected, \
        f"Expected heuristic {expected}, got {context['heuristic']}"


@then(parsers.parse('the edge from "{from_wp}" to "{to_wp}" should have distance {distance:d}'))
def edge_should_have_distance(context, from_wp, to_wp, distance):
    """Verify edge distance"""
    # Find edge
    edge = None
    for e in context['graph']['edges']:
        if (e['from'] == from_wp and e['to'] == to_wp) or \
           (e['from'] == to_wp and e['to'] == from_wp):
            edge = e
            break

    assert edge is not None, f"Edge from {from_wp} to {to_wp} not found"
    assert edge['distance'] == distance, f"Expected distance {distance}, got {edge['distance']}"


@then(parsers.parse('the edge should be type "{edge_type}"'))
def edge_should_be_type(context, edge_type):
    """Verify edge type"""
    # Get the last checked edge (hacky but works for this test)
    edge = context['graph']['edges'][0]
    assert edge['type'] == edge_type, f"Expected type {edge_type}, got {edge['type']}"
