"""BDD step definitions for VRP market distribution tests"""
from pytest_bdd import scenarios, given, when, then, parsers
from dataclasses import dataclass
from typing import Dict, List, Optional
from domain.shared.value_objects import Waypoint
from adapters.secondary.routing.ortools_engine import ORToolsRoutingEngine


scenarios('../../features/routing/vrp_market_distribution.feature')


@dataclass
class VRPTestContext:
    """Context for VRP test scenarios"""
    graph: Dict[str, Waypoint]
    ship_locations: Dict[str, str]
    fuel_capacity: int
    engine_speed: int
    markets: List[str]
    assignments: Optional[Dict[str, List[str]]]
    exception: Optional[Exception]
    distance_matrix: Optional[List[List[int]]] = None


@given("a navigation graph with waypoints:")
def navigation_graph_with_waypoints(datatable, context):
    """Create navigation graph from data table"""
    graph = {}
    # Skip header row - datatable is list of lists
    for row in datatable[1:]:
        symbol = row[0]
        x = int(row[1])
        y = int(row[2])
        waypoint_type = row[3]
        has_fuel = row[4].lower() == 'true'

        graph[symbol] = Waypoint(
            symbol=symbol,
            x=x,
            y=y,
            waypoint_type=waypoint_type,
            has_fuel=has_fuel
        )

    context['vrp'] = VRPTestContext(
        graph=graph,
        ship_locations={},
        fuel_capacity=400,
        engine_speed=30,
        markets=[],
        assignments=None,
        exception=None
    )


@given("a navigation graph with additional market waypoints:")
def add_market_waypoints_to_graph(datatable, context):
    """Add more waypoints to existing graph"""
    # Skip header row - datatable is list of lists
    for row in datatable[1:]:
        symbol = row[0]
        x = int(row[1])
        y = int(row[2])
        waypoint_type = row[3]
        has_fuel = row[4].lower() == 'true'

        context['vrp'].graph[symbol] = Waypoint(
            symbol=symbol,
            x=x,
            y=y,
            waypoint_type=waypoint_type,
            has_fuel=has_fuel
        )


@given(parsers.parse('ship "{ship}" at waypoint "{waypoint}" with fuel {current}/{capacity} and engine speed {speed}'))
def ship_at_waypoint(ship, waypoint, current, capacity, speed, context):
    """Register ship location and specs"""
    context['vrp'].ship_locations[ship] = waypoint
    context['vrp'].fuel_capacity = int(capacity)
    context['vrp'].engine_speed = int(speed)


@when("I optimize fleet tour for markets:")
def optimize_fleet_tour_from_table(datatable, context):
    """Optimize VRP with markets from data table"""
    # Data table has no header row - each row is a single market symbol
    markets = [row[0] for row in datatable if row]
    context['vrp'].markets = markets

    engine = ORToolsRoutingEngine()
    try:
        assignments = engine.optimize_fleet_tour(
            graph=context['vrp'].graph,
            markets=markets,
            ship_locations=context['vrp'].ship_locations,
            fuel_capacity=context['vrp'].fuel_capacity,
            engine_speed=context['vrp'].engine_speed
        )
        context['vrp'].assignments = assignments
        context['vrp'].exception = None
    except Exception as e:
        context['vrp'].assignments = None
        context['vrp'].exception = e


@when(parsers.parse("I optimize fleet tour for {count:d} markets"))
def optimize_fleet_tour_for_count(count, context):
    """Optimize VRP with all markets in graph"""
    # Use all waypoints in graph as markets
    markets = list(context['vrp'].graph.keys())[:count]
    context['vrp'].markets = markets

    engine = ORToolsRoutingEngine()
    try:
        assignments = engine.optimize_fleet_tour(
            graph=context['vrp'].graph,
            markets=markets,
            ship_locations=context['vrp'].ship_locations,
            fuel_capacity=context['vrp'].fuel_capacity,
            engine_speed=context['vrp'].engine_speed
        )
        context['vrp'].assignments = assignments
        context['vrp'].exception = None
    except Exception as e:
        context['vrp'].assignments = None
        context['vrp'].exception = e


@then(parsers.parse("all {count:d} markets should be assigned to ships"))
def all_markets_assigned(count, context):
    """Verify all markets were assigned"""
    if context['vrp'].exception:
        raise AssertionError(f"VRP optimization failed with exception: {context['vrp'].exception}")

    assert context['vrp'].assignments is not None, "No assignments returned from VRP"

    # Count total assigned markets
    assigned_markets = set()
    for ship, markets in context['vrp'].assignments.items():
        assigned_markets.update(markets)

    total_markets = set(context['vrp'].markets)
    dropped_markets = total_markets - assigned_markets

    assert len(dropped_markets) == 0, (
        f"VRP dropped {len(dropped_markets)} markets: {dropped_markets}. "
        f"Expected all {count} markets to be assigned."
    )

    assert len(assigned_markets) == count, (
        f"Expected {count} markets assigned, got {len(assigned_markets)}"
    )


@then("each ship should have at least 1 market assigned")
def each_ship_has_markets(context):
    """Verify each ship got at least one market"""
    if context['vrp'].exception:
        raise AssertionError(f"VRP optimization failed with exception: {context['vrp'].exception}")

    assert context['vrp'].assignments is not None, "No assignments returned from VRP"

    empty_ships = [ship for ship, markets in context['vrp'].assignments.items() if len(markets) == 0]

    # It's OK if some ships are empty if there are fewer markets than ships
    # But with equal or more markets than ships, all ships should be used
    if len(context['vrp'].markets) >= len(context['vrp'].ship_locations):
        assert len(empty_ships) == 0, (
            f"Ships without assignments: {empty_ships}. "
            f"With {len(context['vrp'].markets)} markets and {len(context['vrp'].ship_locations)} ships, "
            f"all ships should be utilized."
        )


@then("no markets should be dropped")
def no_markets_dropped(context):
    """Verify no markets were dropped by VRP solver"""
    if context['vrp'].exception:
        raise AssertionError(f"VRP optimization failed with exception: {context['vrp'].exception}")

    assert context['vrp'].assignments is not None, "No assignments returned from VRP"

    # Collect all assigned markets
    assigned_markets = set()
    for ship, markets in context['vrp'].assignments.items():
        assigned_markets.update(markets)

    # Check for dropped markets
    total_markets = set(context['vrp'].markets)
    dropped_markets = total_markets - assigned_markets

    assert len(dropped_markets) == 0, (
        f"VRP dropped {len(dropped_markets)} markets: {dropped_markets}. "
        f"This should not happen with proper disjunction penalty."
    )


@then("load should be balanced across ships")
def load_balanced_across_ships(context):
    """Verify markets are distributed somewhat evenly"""
    if context['vrp'].exception:
        raise AssertionError(f"VRP optimization failed with exception: {context['vrp'].exception}")

    assert context['vrp'].assignments is not None, "No assignments returned from VRP"

    # Calculate load distribution
    loads = [len(markets) for markets in context['vrp'].assignments.values()]

    if len(loads) == 0:
        return  # No ships, nothing to balance

    min_load = min(loads)
    max_load = max(loads)

    # Allow some imbalance, but not extreme (max should be at most 2x min + 1)
    # For 24 markets and 5 ships: ideal is ~4-5 each
    # Allow range like [3, 6] or [4, 7]
    assert max_load <= min_load * 2 + 2, (
        f"Load imbalance too high: min={min_load}, max={max_load}. "
        f"Loads: {sorted(loads)}"
    )


@when("I build the VRP distance matrix for markets:")
def build_vrp_distance_matrix(datatable, context):
    """Build VRP distance matrix and capture it for inspection"""
    markets = [row[0] for row in datatable if row]
    context['vrp'].markets = markets

    engine = ORToolsRoutingEngine()
    try:
        # Build distance matrix directly
        nodes = list(context['vrp'].graph.keys())
        distance_matrix = engine._build_distance_matrix_for_vrp(
            nodes=nodes,
            graph=context['vrp'].graph,
            fuel_capacity=context['vrp'].fuel_capacity,
            engine_speed=context['vrp'].engine_speed
        )
        context['vrp'].distance_matrix = distance_matrix
        context['vrp'].exception = None
    except Exception as e:
        context['vrp'].distance_matrix = None
        context['vrp'].exception = e


@then(parsers.parse('the distance from "{origin}" to "{destination}" should reflect pathfinding with refueling'))
def distance_reflects_pathfinding_with_refueling(origin, destination, context):
    """Verify distance matrix entry uses actual pathfinding for routes requiring refueling"""
    if context['vrp'].exception:
        raise AssertionError(f"Distance matrix build failed: {context['vrp'].exception}")

    assert context['vrp'].distance_matrix is not None, "No distance matrix was built"

    # Get node indices
    nodes = list(context['vrp'].graph.keys())
    origin_idx = nodes.index(origin)
    dest_idx = nodes.index(destination)

    matrix_distance = context['vrp'].distance_matrix[origin_idx][dest_idx]

    # Test actual pathfinding
    engine = ORToolsRoutingEngine()
    route = engine.find_optimal_path(
        graph=context['vrp'].graph,
        start=origin,
        goal=destination,
        current_fuel=context['vrp'].fuel_capacity,
        fuel_capacity=context['vrp'].fuel_capacity,
        engine_speed=context['vrp'].engine_speed,
        prefer_cruise=True
    )

    if route:
        # Path exists - matrix should reflect actual pathfinding time (with refueling)
        expected_time = route['total_time']
        assert matrix_distance == expected_time, (
            f"Distance matrix for {origin}->{destination} should use pathfinding time. "
            f"Expected {expected_time} (with refueling), got {matrix_distance} (straight-line)"
        )
    else:
        # No path exists - matrix should mark as unreachable
        assert matrix_distance == 1_000_000, (
            f"Distance matrix for {origin}->{destination} should mark unreachable routes as 1,000,000. "
            f"Got {matrix_distance} instead (straight-line distance was used)"
        )


@then(parsers.parse('the distance from "{origin}" to "{destination}" should reflect direct pathfinding'))
def distance_reflects_direct_pathfinding(origin, destination, context):
    """Verify distance matrix entry uses actual pathfinding for direct routes"""
    if context['vrp'].exception:
        raise AssertionError(f"Distance matrix build failed: {context['vrp'].exception}")

    assert context['vrp'].distance_matrix is not None, "No distance matrix was built"

    # Get node indices
    nodes = list(context['vrp'].graph.keys())
    origin_idx = nodes.index(origin)
    dest_idx = nodes.index(destination)

    matrix_distance = context['vrp'].distance_matrix[origin_idx][dest_idx]

    # Test actual pathfinding
    engine = ORToolsRoutingEngine()
    route = engine.find_optimal_path(
        graph=context['vrp'].graph,
        start=origin,
        goal=destination,
        current_fuel=context['vrp'].fuel_capacity,
        fuel_capacity=context['vrp'].fuel_capacity,
        engine_speed=context['vrp'].engine_speed,
        prefer_cruise=True
    )

    assert route is not None, f"Expected direct route from {origin} to {destination} to exist"

    expected_time = route['total_time']
    assert matrix_distance == expected_time, (
        f"Distance matrix for {origin}->{destination} should use pathfinding time. "
        f"Expected {expected_time} (direct path), got {matrix_distance}"
    )
