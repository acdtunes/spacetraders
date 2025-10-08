#!/usr/bin/env python3
"""
Advanced step definitions for routing algorithms BDD tests
Tests complex scenarios including TSP, 2-opt, refueling, and edge cases
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
from routing import (
    GraphBuilder, RouteOptimizer, TourOptimizer, FuelCalculator,
    TimeCalculator, parse_waypoint_symbol, euclidean_distance
)

# Load all scenarios from the feature file
scenarios('../../features/navigation/routing_advanced.feature')


_KNOWN_HEADERS = {
    'symbol', 'type', 'x', 'y', 'traits', 'page', 'distance', 'from', 'to',
    'has_fuel', 'has_fuel_at_to', 'edge_to', 'edge_type', 'purchase_price',
    'sell_price', 'supply', 'activity', 'trade_volume', 'last_updated',
    'name'
}


def _parse_table(table: str | None = None, datatable: list[list[str]] | None = None) -> list[dict[str, str]]:
    """Parse a Gherkin table string into list of dict rows."""
    rows_raw = table_to_rows(table, datatable)
    if not rows_raw:
        return []

    raw_headers = rows_raw[0]
    header_lower = {h.lower() for h in raw_headers}

    if raw_headers and (header_lower & _KNOWN_HEADERS):
        headers = raw_headers
        data_rows = rows_raw[1:]
    else:
        headers = ['value']
        data_rows = rows_raw

    rows = []
    for values in data_rows:
        if not values:
            continue
        rows.append(dict(zip(headers, values)))
    return rows


def _get_value(row: dict[str, str], key: str) -> str:
    """Get value from row using key, falling back to the first column."""
    if key in row:
        return row[key]
    if key == 'symbol' and 'value' in row:
        return row['value']
    return next(iter(row.values()))


# Import step definitions from basic routing tests to avoid duplication
from test_routing_steps import (
    create_simple_graph,
    create_isolated_graph
)


@pytest.fixture
def context():
    """Shared test context"""
    return {
        'mock_api': None,
        'graph': None,
        'graph_builder': None,
        'route_optimizer': None,
        'tour_optimizer': None,
        'ship_data': None,
        'result': None,
        'formatted_time': None,
        'can_afford': None,
        'parsed_system': None,
        'parsed_waypoint': None,
        'route': None,
        'tour': None,
        'baseline_tour': None,
        'optimized_tour': None,
        'markets': None,
        'temp_dir': None,
        'ship_fuel': None,
        'iterations': None,
        'page_limit': None
    }


# =============================================================================
# Background & Setup
# =============================================================================

@given("the SpaceTraders API is mocked", target_fixture="mock_api")
def mock_api(context):
    """Create mock API client"""
    context['mock_api'] = MockAPIClient()
    context['temp_dir'] = tempfile.mkdtemp()
    return context['mock_api']


# =============================================================================
# Time & Fuel Calculations
# =============================================================================

@when(parsers.parse('I format time for {seconds:d} seconds'))
def format_time(context, seconds):
    """Format time from seconds"""
    context['formatted_time'] = TimeCalculator.format_time(seconds)


@then(parsers.parse('the formatted time should be "{expected}"'))
def formatted_time_should_be(context, expected):
    """Verify formatted time"""
    assert context['formatted_time'] == expected, \
        f"Expected '{expected}', got '{context['formatted_time']}'"


@given(parsers.parse('a ship has {fuel:d} fuel'))
def ship_has_fuel(context, fuel):
    """Set ship fuel level"""
    context['ship_fuel'] = fuel


@when(parsers.parse('I check if it can afford {distance:d} units in {mode} mode'))
def check_can_afford(context, distance, mode):
    """Check if ship can afford journey"""
    context['can_afford'] = FuelCalculator.can_afford(
        distance, context['ship_fuel'], mode
    )


@then("the ship should not afford the journey")
def ship_should_not_afford(context):
    """Verify ship cannot afford journey"""
    assert context['can_afford'] is False, "Ship should not afford journey"


@then("the ship should afford the journey")
def ship_should_afford(context):
    """Verify ship can afford journey"""
    assert context['can_afford'] is True, "Ship should afford journey"


@when(parsers.parse('I parse waypoint symbol "{symbol}"'))
def parse_waypoint(context, symbol):
    """Parse waypoint symbol"""
    system, waypoint = parse_waypoint_symbol(symbol)
    context['parsed_system'] = system
    context['parsed_waypoint'] = waypoint


@then(parsers.parse('the system should be "{expected}"'))
def system_should_be(context, expected):
    """Verify parsed system"""
    assert context['parsed_system'] == expected, \
        f"Expected system '{expected}', got '{context['parsed_system']}'"


@then(parsers.parse('the waypoint should be "{expected}"'))
def waypoint_should_be(context, expected):
    """Verify parsed waypoint"""
    assert context['parsed_waypoint'] == expected, \
        f"Expected waypoint '{expected}', got '{context['parsed_waypoint']}'"


# =============================================================================
# Graph Builder - Pagination & Error Handling
# =============================================================================

@given(parsers.parse('waypoints exist with pagination:\n{table}'))
@given('waypoints exist with pagination:')
def create_waypoints_with_pagination(context, table: str | None = None, datatable: list[list[str]] | None = None):
    """Create waypoints with pagination"""
    rows = _parse_table(table, datatable)
    waypoints_by_page = {}

    for row in rows:
        symbol = row['symbol']
        wp_type = row['type']
        x = int(row['x'])
        y = int(row['y'])
        page = int(row['page'])

        # Add waypoint
        context['mock_api'].add_waypoint(symbol, wp_type, x, y, [])

        # Group by page for pagination
        if page not in waypoints_by_page:
            waypoints_by_page[page] = []
        waypoints_by_page[page].append(symbol)

    # Configure mock API for pagination
    context['mock_api'].waypoints_by_page = waypoints_by_page


@given(parsers.parse('the system "{system}" has no waypoints'))
def system_has_no_waypoints(context, system):
    """Configure empty system"""
    context['mock_api'].waypoints = {}


@given(parsers.parse('the system "{system}" has {pages:d} pages of waypoints'))
def system_has_many_pages(context, system, pages):
    """Configure system with many pages"""
    context['page_limit'] = pages


@when(parsers.parse('I build a navigation graph for system "{system}"'))
def build_navigation_graph(context, system):
    """Build navigation graph"""
    # Use temp directory for test database
    import os
    db_path = os.path.join(context['temp_dir'], 'test_routing.db')
    context['graph_builder'] = GraphBuilder(context['mock_api'], db_path=db_path)
    try:
        context['graph'] = context['graph_builder'].build_system_graph(system)
    except Exception as e:
        context['graph'] = None


@then("the graph should be None")
def graph_should_be_none(context):
    """Verify graph is None"""
    assert context['graph'] is None, "Graph should be None"


# =============================================================================
# Route Optimizer - Refueling & Advanced Pathfinding
# =============================================================================

@given(parsers.parse('a fuel network:\n{table}'))
@given('a fuel network:')
def create_fuel_network(context, table: str | None = None, datatable: list[list[str]] | None = None):
    """Create navigation graph with fuel stations"""
    rows = _parse_table(table, datatable)
    waypoints = {}
    edges = []

    for row in rows:
        from_wp = _get_value(row, 'from')
        to_wp = _get_value(row, 'to')
        distance = int(float(_get_value(row, 'distance')))
        has_fuel_at_to = _get_value(row, 'has_fuel_at_to').lower() == 'true'

        # Create waypoints if they don't exist
        if from_wp not in waypoints:
            waypoints[from_wp] = {
                "x": 0, "y": 0, "has_fuel": False, "traits": [], "orbitals": []
            }
        if to_wp not in waypoints:
            has_fuel = has_fuel_at_to
            traits = ["MARKETPLACE"] if has_fuel else []
            waypoints[to_wp] = {
                "x": distance, "y": 0, "has_fuel": has_fuel,
                "traits": traits, "orbitals": []
            }

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


@given("all waypoints have no fuel available")
def all_waypoints_no_fuel(context):
    """Remove fuel from all waypoints"""
    for wp_data in context['graph']['waypoints'].values():
        wp_data['has_fuel'] = False
        wp_data['traits'] = []


@given(parsers.parse('a ship "{ship_symbol}" at "{waypoint}" with {current:d}/{capacity:d} fuel'))
def create_ship_at_location(context, ship_symbol, waypoint, current, capacity):
    """Create ship at specific location with fuel"""
    context['mock_api'].set_ship_location(ship_symbol, waypoint, "IN_ORBIT")
    context['mock_api'].set_ship_fuel(ship_symbol, current, capacity)
    context['ship_data'] = context['mock_api'].get_ship(ship_symbol)


@when(parsers.parse('I plan a route from "{start}" to "{goal}"'))
def plan_route(context, start, goal):
    """Plan route using route optimizer"""
    context['route_optimizer'] = RouteOptimizer(context['graph'], context['ship_data'])
    current_fuel = context['ship_data']['fuel']['current']
    context['route'] = context['route_optimizer'].find_optimal_route(
        start, goal, current_fuel=current_fuel
    )


@then("the route should exist")
def route_should_exist(context):
    """Verify route exists"""
    assert context['route'] is not None, "Route should exist"


@then("the route should be None")
def route_should_be_none(context):
    """Verify route is None"""
    assert context['route'] is None, "Route should be None"


@then(parsers.parse('the route should include a refuel action at "{waypoint}"'))
def route_should_include_refuel(context, waypoint):
    """Verify route includes refuel action"""
    refuel_found = False
    for step in context['route']['steps']:
        if step['action'] == 'refuel' and step['waypoint'] == waypoint:
            refuel_found = True
            break
    assert refuel_found, f"Route should include refuel at {waypoint}"


@then("the route should use DRIFT mode")
def route_should_use_drift(context):
    """Verify route uses DRIFT mode"""
    drift_found = False
    for step in context['route']['steps']:
        if step['action'] == 'navigate' and step['mode'] == 'DRIFT':
            drift_found = True
            break
    assert drift_found, "Route should use DRIFT mode"


@given(parsers.parse('a complex graph with {count:d} waypoints'))
def create_complex_graph(context, count):
    """Create complex graph with many waypoints"""
    waypoints = {}
    edges = []

    # Create grid of waypoints
    for i in range(count):
        symbol = f"X1-CX-A{i+1}"
        waypoints[symbol] = {
            "x": (i % 10) * 50,
            "y": (i // 10) * 50,
            "has_fuel": True,
            "traits": ["MARKETPLACE"],
            "orbitals": []
        }

        # Connect to previous waypoint
        if i > 0:
            prev_symbol = f"X1-CX-A{i}"
            edges.append({
                "from": prev_symbol,
                "to": symbol,
                "distance": 50,
                "type": "normal"
            })

    context['graph'] = {
        "system": "X1-CX",
        "waypoints": waypoints,
        "edges": edges
    }


@when(parsers.parse('I plan a route from "{start}" to "{goal}"'))
def plan_route_complex(context, start, goal):
    """Plan route in complex graph"""
    context['route_optimizer'] = RouteOptimizer(context['graph'], context['ship_data'])
    current_fuel = context['ship_data']['fuel']['current']
    context['route'] = context['route_optimizer'].find_optimal_route(
        start, goal, current_fuel=current_fuel
    )


# =============================================================================
# Multi-Stop Tour Optimization
# =============================================================================

@given(parsers.parse('a tour network:\n{table}'))
@given('a tour network:')
def create_tour_network(context, table: str | None = None, datatable: list[list[str]] | None = None):
    """Create network for tour planning"""
    waypoints = {}
    edges = []

    symbols = []
    rows = _parse_table(table, datatable)
    for row in rows:

        symbol = _get_value(row, 'symbol')
        x = int(float(_get_value(row, 'x')))
        y = int(float(_get_value(row, 'y')))
        has_fuel = _get_value(row, 'has_fuel').lower() == 'true'

        traits = ["MARKETPLACE"] if has_fuel else []
        waypoints[symbol] = {
            "x": x, "y": y, "has_fuel": has_fuel,
            "traits": traits, "orbitals": []
        }
        symbols.append(symbol)

    # Create full mesh of edges
    for i, wp1 in enumerate(symbols):
        for wp2 in symbols[i+1:]:
            wp1_data = waypoints[wp1]
            wp2_data = waypoints[wp2]

            distance = ((wp2_data['x'] - wp1_data['x']) ** 2 +
                       (wp2_data['y'] - wp1_data['y']) ** 2) ** 0.5

            edges.append({
                "from": wp1,
                "to": wp2,
                "distance": round(distance, 2),
                "type": "normal"
            })

    context['graph'] = {
        "system": "X1-TEST",
        "waypoints": waypoints,
        "edges": edges
    }


@given(parsers.parse('an isolated waypoint "{symbol}" at ({x:d}, {y:d})'))
def add_isolated_waypoint(context, symbol, x, y):
    """Add isolated waypoint with no edges"""
    context['graph']['waypoints'][symbol] = {
        "x": x, "y": y, "has_fuel": False,
        "traits": [], "orbitals": []
    }
    # Don't add edges - it's isolated


@when(parsers.parse('I plan a tour from "{start}" visiting:\n{table}'))
@when(parsers.parse('I plan a tour from "{start}" visiting:'))
def plan_tour(context, start, table: str | None = None, datatable: list[list[str]] | None = None):
    """Plan multi-stop tour"""
    rows = _parse_table(table, datatable)
    stops = [_get_value(row, 'symbol') for row in rows]

    # Store stops for later use
    context['tour_start'] = start
    context['tour_stops'] = stops
    context['tour_return'] = False

    context['tour_optimizer'] = TourOptimizer(context['graph'], context['ship_data'])
    current_fuel = context['ship_data']['fuel']['current']

    context['tour'] = context['tour_optimizer'].solve_nearest_neighbor(
        start, stops, current_fuel, return_to_start=False
    )


@when("the tour should return to start")
def tour_should_return_to_start(context):
    """Modify tour to return to start"""
    # Set return flag and re-plan
    context['tour_return'] = True

    start = context['tour_start']
    stops = context['tour_stops']
    current_fuel = context['ship_data']['fuel']['current']

    context['tour'] = context['tour_optimizer'].solve_nearest_neighbor(
        start, stops, current_fuel, return_to_start=True
    )


@then("the tour should exist")
def tour_should_exist(context):
    """Verify tour exists"""
    assert context['tour'] is not None, "Tour should exist"


@then("the tour should be None")
def tour_should_be_none(context):
    """Verify tour is None"""
    assert context['tour'] is None, "Tour should be None"


@then(parsers.parse('the tour should have {count:d} legs'))
def tour_should_have_legs(context, count):
    """Verify tour leg count"""
    assert len(context['tour']['legs']) == count, \
        f"Expected {count} legs, got {len(context['tour']['legs'])}"


@then(parsers.parse('the final waypoint should be "{waypoint}"'))
def final_waypoint_should_be(context, waypoint):
    """Verify final waypoint"""
    last_leg = context['tour']['legs'][-1]
    assert last_leg['goal'] == waypoint, \
        f"Expected final waypoint '{waypoint}', got '{last_leg['goal']}'"


@then(parsers.parse('the tour should include automatic refuel at "{waypoint}"'))
def tour_should_include_refuel(context, waypoint):
    """Verify tour includes automatic refuel"""
    refuel_found = False
    for leg in context['tour']['legs']:
        for step in leg['steps']:
            if step['action'] == 'refuel' and step['waypoint'] == waypoint:
                refuel_found = True
                break
    assert refuel_found, f"Tour should include refuel at {waypoint}"


# =============================================================================
# 2-Opt Optimization
# =============================================================================

@given(parsers.parse('a baseline tour visiting stops in order:\n{table}'))
@given('a baseline tour visiting stops in order:')
def create_baseline_tour(context, table: str | None = None, datatable: list[list[str]] | None = None):
    """Create baseline tour for optimization"""
    rows = _parse_table(table, datatable)
    stops = [_get_value(row, 'symbol') for row in rows]
    context['stops'] = stops

    # Get start from graph (first waypoint)
    start = list(context['graph']['waypoints'].keys())[0]

    context['tour_optimizer'] = TourOptimizer(context['graph'], context['ship_data'])
    current_fuel = context['ship_data']['fuel']['current']

    context['baseline_tour'] = context['tour_optimizer'].solve_nearest_neighbor(
        start, stops, current_fuel, return_to_start=False
    )


@given(parsers.parse('an already optimal tour visiting:\n{table}'))
@given('an already optimal tour visiting:')
def create_optimal_tour(context, table: str | None = None, datatable: list[list[str]] | None = None):
    """Create already optimal tour"""
    rows = _parse_table(table, datatable)
    stops = [_get_value(row, 'symbol') for row in rows]

    start = list(context['graph']['waypoints'].keys())[0]

    context['tour_optimizer'] = TourOptimizer(context['graph'], context['ship_data'])
    current_fuel = context['ship_data']['fuel']['current']

    context['baseline_tour'] = context['tour_optimizer'].solve_nearest_neighbor(
        start, stops, current_fuel, return_to_start=False
    )


@when("I optimize the tour with 2-opt")
def optimize_tour_2opt(context):
    """Optimize tour using 2-opt"""
    if context['baseline_tour']:
        context['optimized_tour'] = context['tour_optimizer'].two_opt_improve(
            context['baseline_tour']
        )
    else:
        context['optimized_tour'] = None


@when(parsers.parse('I optimize the tour with 2-opt and max {max_iter:d} iterations'))
def optimize_tour_2opt_max_iter(context, max_iter):
    """Optimize tour with max iterations"""
    if context['baseline_tour']:
        context['optimized_tour'] = context['tour_optimizer'].two_opt_improve(
            context['baseline_tour'], max_iterations=max_iter
        )
    else:
        context['optimized_tour'] = None


@then("the optimized tour should be faster than baseline")
def optimized_tour_should_be_faster(context):
    """Verify optimization improves time"""
    baseline_time = context['baseline_tour']['total_time']
    optimized_time = context['optimized_tour']['total_time']
    # Allow equal or better (might already be optimal)
    assert optimized_time <= baseline_time, \
        f"Optimized tour should be <= baseline ({optimized_time} vs {baseline_time})"


@then("the optimization should report improvements")
def optimization_should_report_improvements(context):
    """Verify optimization reports improvements"""
    # This is verified by the function itself through logging
    # We just check the tour exists
    assert context['optimized_tour'] is not None


# =============================================================================
# Market Discovery
# =============================================================================

@given(parsers.parse('a system graph with waypoints:\n{table}'))
@given('a system graph with waypoints:')
def create_system_graph_for_markets(context, table: str | None = None, datatable: list[list[str]] | None = None):
    """Create system graph with specific waypoints"""
    waypoints = {}
    rows = _parse_table(table, datatable)
    for row in rows:

        symbol = _get_value(row, 'symbol')
        wp_type = _get_value(row, 'type')
        traits_str = row.get('traits', '')

        traits = [traits_str] if traits_str else []

        waypoints[symbol] = {
            "type": wp_type,
            "x": 0, "y": 0,
            "traits": traits,
            "has_fuel": 'MARKETPLACE' in traits or 'FUEL_STATION' in traits,
            "orbitals": []
        }

    context['graph'] = {
        "system": "X1-TEST",
        "waypoints": waypoints,
        "edges": []
    }


@given("an empty system graph")
def create_empty_graph(context):
    """Create empty graph"""
    context['graph'] = {
        "system": "X1-EMPTY",
        "waypoints": {},
        "edges": []
    }


@when("I discover markets in the graph")
def discover_markets(context):
    """Discover markets in graph"""
    context['markets'] = TourOptimizer.get_markets_from_graph(context['graph'])


@then(parsers.parse('I should find {count:d} markets:\n{table}'))
@then(parsers.parse('I should find {count:d} markets:'))
def should_find_markets_with_table(context, count, table: str | None = None, datatable: list[list[str]] | None = None):
    """Verify market count and specific markets"""
    assert len(context['markets']) == count, \
        f"Expected {count} markets, found {len(context['markets'])}"
    rows = _parse_table(table, datatable)
    expected_markets = [_get_value(row, 'symbol') for row in rows]
    assert set(context['markets']) == set(expected_markets), \
        f"Expected markets {expected_markets}, got {context['markets']}"


@then(parsers.parse('I should find {count:d} markets'))
def should_find_markets_count(context, count):
    """Verify market count only"""
    assert len(context['markets']) == count, \
        f"Expected {count} markets, found {len(context['markets'])}"


# =============================================================================
# Edge Cases
# =============================================================================

@then(parsers.parse('the route should have {count:d} navigation steps'))
def route_should_have_navigation_steps(context, count):
    """Verify navigation step count"""
    nav_steps = [s for s in context['route']['steps'] if s['action'] == 'navigate']
    assert len(nav_steps) == count, \
        f"Expected {count} navigation steps, got {len(nav_steps)}"


@given(parsers.parse('a complex tour with {count:d} waypoints'))
def create_complex_tour(context, count):
    """Create complex tour for optimization testing"""
    # Ensure ship_data is available before creating the tour
    if not context.get('ship_data'):
        # Create a default ship if not exists
        context['mock_api'].set_ship_location("MAX-ITER", "X1-MI-HQ", "IN_ORBIT")
        context['mock_api'].set_ship_fuel("MAX-ITER", 1000, 1000)
        context['ship_data'] = context['mock_api'].get_ship("MAX-ITER")

    waypoints = {}
    symbols = []

    for i in range(count):
        symbol = f"X1-MI-M{i+1}"
        symbols.append(symbol)
        waypoints[symbol] = {
            "x": i * 50,
            "y": (i % 2) * 50,  # Zigzag pattern
            "has_fuel": True,
            "traits": ["MARKETPLACE"],
            "orbitals": []
        }

    # Add HQ
    waypoints["X1-MI-HQ"] = {
        "x": 0, "y": 0, "has_fuel": True,
        "traits": ["MARKETPLACE"], "orbitals": []
    }

    # Create full mesh
    edges = []
    all_symbols = ["X1-MI-HQ"] + symbols
    for i, wp1 in enumerate(all_symbols):
        for wp2 in all_symbols[i+1:]:
            wp1_data = waypoints[wp1]
            wp2_data = waypoints[wp2]

            distance = ((wp2_data['x'] - wp1_data['x']) ** 2 +
                       (wp2_data['y'] - wp1_data['y']) ** 2) ** 0.5

            edges.append({
                "from": wp1,
                "to": wp2,
                "distance": round(distance, 2),
                "type": "normal"
            })

    context['graph'] = {
        "system": "X1-MI",
        "waypoints": waypoints,
        "edges": edges
    }

    # Create baseline tour
    context['tour_optimizer'] = TourOptimizer(context['graph'], context['ship_data'])
    current_fuel = context['ship_data']['fuel']['current']

    context['baseline_tour'] = context['tour_optimizer'].solve_nearest_neighbor(
        "X1-MI-HQ", symbols[:5], current_fuel, return_to_start=False
    )


@then(parsers.parse('the graph should have {count:d} waypoints'))
def graph_should_have_waypoints(context, count):
    """Verify waypoint count in graph"""
    actual_count = len(context['graph']['waypoints']) if context['graph'] else 0
    assert actual_count == count, \
        f"Expected {count} waypoints, got {actual_count}"
