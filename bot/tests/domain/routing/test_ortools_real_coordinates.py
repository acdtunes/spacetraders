#!/usr/bin/env python3
"""
Test OR-Tools TSP performance on actual X1-VH85 waypoint coordinates.

This test investigates whether OR-Tools is producing optimal solutions or getting
stuck in local optima with crossing edges.
"""

import json
import math
from typing import Dict, List, Tuple

import pytest
from ortools.constraint_solver import pywrapcp, routing_enums_pb2

from src.spacetraders_bot.core.ortools_router import ORToolsTSP
from src.spacetraders_bot.core.routing_config import RoutingConfig


# Actual X1-VH85 coordinates from database
WAYPOINTS_X1_VH85 = {
    "X1-VH85-A1": {"x": 19.0, "y": 15.0},
    "X1-VH85-A2": {"x": 19.0, "y": 15.0},
    "X1-VH85-A3": {"x": 19.0, "y": 15.0},
    "X1-VH85-A4": {"x": 19.0, "y": 15.0},
    "X1-VH85-AC5D": {"x": -14.0, "y": 22.0},
    "X1-VH85-B6": {"x": 149.0, "y": 119.0},
    "X1-VH85-B7": {"x": 337.0, "y": 76.0},
    "X1-VH85-D48": {"x": 2.0, "y": 87.0},
    "X1-VH85-D49": {"x": 2.0, "y": 87.0},
    "X1-VH85-D50": {"x": 2.0, "y": 87.0},
    "X1-VH85-D51": {"x": 2.0, "y": 87.0},
    "X1-VH85-E53": {"x": 34.0, "y": -42.0},
    "X1-VH85-E54": {"x": 34.0, "y": -42.0},
    "X1-VH85-F55": {"x": 24.0, "y": 72.0},
    "X1-VH85-F56": {"x": 24.0, "y": 72.0},
    "X1-VH85-G58": {"x": -50.0, "y": -43.0},
    "X1-VH85-H59": {"x": -36.0, "y": 24.0},
    "X1-VH85-H60": {"x": -36.0, "y": 24.0},
    "X1-VH85-H61": {"x": -36.0, "y": 24.0},
    "X1-VH85-H62": {"x": -36.0, "y": 24.0},
}


# The cached tour from production that shows crossing edges
CACHED_TOUR_ORDER = [
    "X1-VH85-A1",
    "X1-VH85-AC5D",
    "X1-VH85-H59",
    "X1-VH85-H62",
    "X1-VH85-H61",
    "X1-VH85-H60",
    "X1-VH85-G58",
    "X1-VH85-E53",
    "X1-VH85-E54",
    "X1-VH85-B7",
    "X1-VH85-B6",
    "X1-VH85-F55",
    "X1-VH85-F56",
    "X1-VH85-D51",
    "X1-VH85-D50",
    "X1-VH85-D49",
    "X1-VH85-D48",
    "X1-VH85-A4",
    "X1-VH85-A3",
    "X1-VH85-A2",
    "X1-VH85-A1",
]


def distance(wp1: Dict, wp2: Dict) -> float:
    """Calculate Euclidean distance between two waypoints."""
    dx = wp2["x"] - wp1["x"]
    dy = wp2["y"] - wp1["y"]
    return math.hypot(dx, dy)


def calculate_tour_distance(order: List[str], waypoints: Dict) -> float:
    """Calculate total distance of a tour."""
    total = 0.0
    for i in range(len(order) - 1):
        wp1 = waypoints[order[i]]
        wp2 = waypoints[order[i + 1]]
        total += distance(wp1, wp2)
    return total


def segments_intersect(p1: Tuple[float, float], p2: Tuple[float, float],
                       p3: Tuple[float, float], p4: Tuple[float, float]) -> bool:
    """Check if line segment p1-p2 intersects with line segment p3-p4."""
    def ccw(a, b, c):
        return (c[1] - a[1]) * (b[0] - a[0]) > (b[1] - a[1]) * (c[0] - a[0])

    # Don't count shared endpoints as crossing
    if p1 == p3 or p1 == p4 or p2 == p3 or p2 == p4:
        return False

    return ccw(p1, p3, p4) != ccw(p2, p3, p4) and ccw(p1, p2, p3) != ccw(p1, p2, p4)


def count_crossing_edges(order: List[str], waypoints: Dict) -> int:
    """Count the number of crossing edges in a tour."""
    edges = []
    for i in range(len(order) - 1):
        wp1 = waypoints[order[i]]
        wp2 = waypoints[order[i + 1]]
        edges.append(((wp1["x"], wp1["y"]), (wp2["x"], wp2["y"])))

    crossings = 0
    for i in range(len(edges)):
        for j in range(i + 2, len(edges)):
            # Skip adjacent edges
            if abs(i - j) <= 1:
                continue
            if segments_intersect(edges[i][0], edges[i][1], edges[j][0], edges[j][1]):
                crossings += 1

    return crossings


def build_graph(waypoints: Dict, system: str = "X1-VH85") -> Dict:
    """Build a complete graph from waypoints."""
    graph = {
        "system": system,
        "waypoints": waypoints,
        "edges": []
    }

    symbols = list(waypoints.keys())
    for i, origin in enumerate(symbols):
        wp_a = waypoints[origin]
        for target in symbols[i + 1:]:
            wp_b = waypoints[target]
            dist = distance(wp_a, wp_b)
            graph["edges"].append({"from": origin, "to": target, "distance": dist})
            graph["edges"].append({"from": target, "to": origin, "distance": dist})

    return graph


def solve_tsp_with_timeout(
    waypoints: List[str],
    start: str,
    graph: Dict,
    timeout_ms: int,
    metaheuristic: str = "GUIDED_LOCAL_SEARCH",
) -> Tuple[List[str], float]:
    """Solve TSP with specific timeout and return tour order and objective value."""
    nodes = [start] + [wp for wp in waypoints if wp != start]
    nodes.append("__TOUR_END__")

    start_idx = 0
    end_idx = len(nodes) - 1

    manager = pywrapcp.RoutingIndexManager(len(nodes), 1, [start_idx], [end_idx])
    routing = pywrapcp.RoutingModel(manager)

    # Build distance matrix
    distance_matrix = [[0 for _ in range(len(nodes))] for _ in range(len(nodes))]
    for i, wp1 in enumerate(nodes):
        if wp1 == "__TOUR_END__":
            continue
        for j, wp2 in enumerate(nodes):
            if wp2 == "__TOUR_END__":
                distance_matrix[i][j] = 0
                continue
            if i == j:
                distance_matrix[i][j] = 0
            else:
                distance_matrix[i][j] = int(distance(graph["waypoints"][wp1], graph["waypoints"][wp2]))

    def distance_callback(from_index: int, to_index: int) -> int:
        from_node = manager.IndexToNode(from_index)
        to_node = manager.IndexToNode(to_index)
        return distance_matrix[from_node][to_node]

    transit_callback_index = routing.RegisterTransitCallback(distance_callback)
    routing.SetArcCostEvaluatorOfAllVehicles(transit_callback_index)

    # Configure solver
    search_params = pywrapcp.DefaultRoutingSearchParameters()
    search_params.first_solution_strategy = routing_enums_pb2.FirstSolutionStrategy.PATH_CHEAPEST_ARC
    search_params.local_search_metaheuristic = getattr(
        routing_enums_pb2.LocalSearchMetaheuristic,
        metaheuristic,
    )
    search_params.time_limit.FromMilliseconds(timeout_ms)
    search_params.log_search = True  # Enable logging to see what OR-Tools is doing

    solution = routing.SolveWithParameters(search_params)
    if not solution:
        return [], float("inf")

    # Extract tour order
    order = []
    index = routing.Start(0)
    while True:
        node = manager.IndexToNode(index)
        waypoint = nodes[node]
        if waypoint != "__TOUR_END__":
            order.append(waypoint)
        if routing.IsEnd(index):
            break
        index = solution.Value(routing.NextVar(index))

    # Add return to start
    order.append(start)

    objective = solution.ObjectiveValue()
    return order, objective


class TestORToolsRealCoordinates:
    """Test OR-Tools TSP performance on actual X1-VH85 coordinates."""

    def test_cached_tour_has_crossings(self):
        """Verify that the cached tour from production has crossing edges."""
        crossings = count_crossing_edges(CACHED_TOUR_ORDER, WAYPOINTS_X1_VH85)
        distance_cached = calculate_tour_distance(CACHED_TOUR_ORDER, WAYPOINTS_X1_VH85)

        print(f"\nCached tour analysis:")
        print(f"  Distance: {distance_cached:.2f} units")
        print(f"  Crossings: {crossings}")

        assert crossings > 0, f"Expected crossings but found {crossings}"

    def test_ortools_30s_timeout(self):
        """Test OR-Tools with current 30-second timeout."""
        graph = build_graph(WAYPOINTS_X1_VH85)
        stops = [wp for wp in WAYPOINTS_X1_VH85.keys() if wp != "X1-VH85-A1"]

        order, objective = solve_tsp_with_timeout(
            stops,
            "X1-VH85-A1",
            graph,
            timeout_ms=30000,
            metaheuristic="GUIDED_LOCAL_SEARCH",
        )

        assert order, "OR-Tools failed to find a solution"

        crossings = count_crossing_edges(order, WAYPOINTS_X1_VH85)
        distance_ortools = calculate_tour_distance(order, WAYPOINTS_X1_VH85)

        print(f"\nOR-Tools 30s solution:")
        print(f"  Distance: {distance_ortools:.2f} units")
        print(f"  Crossings: {crossings}")
        print(f"  Objective: {objective}")
        print(f"  Tour: {order}")

        # Test PASSES if we find crossings (confirms the bug)
        # Test FAILS if no crossings (OR-Tools working correctly)
        assert crossings > 0, f"Expected OR-Tools to produce crossings with 30s timeout, but found {crossings}"

    def test_ortools_5min_timeout(self):
        """Test OR-Tools with 5-minute timeout to see if more time helps."""
        graph = build_graph(WAYPOINTS_X1_VH85)
        stops = [wp for wp in WAYPOINTS_X1_VH85.keys() if wp != "X1-VH85-A1"]

        order, objective = solve_tsp_with_timeout(
            stops,
            "X1-VH85-A1",
            graph,
            timeout_ms=300000,  # 5 minutes
            metaheuristic="GUIDED_LOCAL_SEARCH",
        )

        assert order, "OR-Tools failed to find a solution"

        crossings = count_crossing_edges(order, WAYPOINTS_X1_VH85)
        distance_ortools = calculate_tour_distance(order, WAYPOINTS_X1_VH85)

        print(f"\nOR-Tools 5min solution:")
        print(f"  Distance: {distance_ortools:.2f} units")
        print(f"  Crossings: {crossings}")
        print(f"  Objective: {objective}")

        # With 5 minutes, OR-Tools should find a crossing-free solution
        if crossings == 0:
            print("  ✓ No crossings with 5min timeout - timeout is the issue")
        else:
            print(f"  ✗ Still has {crossings} crossings - timeout not the only issue")

    def test_ortools_simulated_annealing(self):
        """Test OR-Tools with SIMULATED_ANNEALING instead of GUIDED_LOCAL_SEARCH."""
        graph = build_graph(WAYPOINTS_X1_VH85)
        stops = [wp for wp in WAYPOINTS_X1_VH85.keys() if wp != "X1-VH85-A1"]

        order, objective = solve_tsp_with_timeout(
            stops,
            "X1-VH85-A1",
            graph,
            timeout_ms=30000,
            metaheuristic="SIMULATED_ANNEALING",
        )

        assert order, "OR-Tools failed to find a solution"

        crossings = count_crossing_edges(order, WAYPOINTS_X1_VH85)
        distance_ortools = calculate_tour_distance(order, WAYPOINTS_X1_VH85)

        print(f"\nOR-Tools SIMULATED_ANNEALING solution:")
        print(f"  Distance: {distance_ortools:.2f} units")
        print(f"  Crossings: {crossings}")
        print(f"  Objective: {objective}")

        if crossings == 0:
            print("  ✓ No crossings with SIMULATED_ANNEALING - metaheuristic matters")
        else:
            print(f"  ✗ Still has {crossings} crossings")

    def test_ortools_tabu_search(self):
        """Test OR-Tools with TABU_SEARCH metaheuristic."""
        graph = build_graph(WAYPOINTS_X1_VH85)
        stops = [wp for wp in WAYPOINTS_X1_VH85.keys() if wp != "X1-VH85-A1"]

        order, objective = solve_tsp_with_timeout(
            stops,
            "X1-VH85-A1",
            graph,
            timeout_ms=30000,
            metaheuristic="TABU_SEARCH",
        )

        assert order, "OR-Tools failed to find a solution"

        crossings = count_crossing_edges(order, WAYPOINTS_X1_VH85)
        distance_ortools = calculate_tour_distance(order, WAYPOINTS_X1_VH85)

        print(f"\nOR-Tools TABU_SEARCH solution:")
        print(f"  Distance: {distance_ortools:.2f} units")
        print(f"  Crossings: {crossings}")
        print(f"  Objective: {objective}")

        if crossings == 0:
            print("  ✓ No crossings with TABU_SEARCH")
        else:
            print(f"  ✗ Still has {crossings} crossings")

    def test_comparison_summary(self):
        """Run all configurations and compare results."""
        graph = build_graph(WAYPOINTS_X1_VH85)
        stops = [wp for wp in WAYPOINTS_X1_VH85.keys() if wp != "X1-VH85-A1"]

        configs = [
            ("GUIDED_LOCAL_SEARCH, 30s", "GUIDED_LOCAL_SEARCH", 30000),
            ("GUIDED_LOCAL_SEARCH, 5min", "GUIDED_LOCAL_SEARCH", 300000),
            ("SIMULATED_ANNEALING, 30s", "SIMULATED_ANNEALING", 30000),
            ("TABU_SEARCH, 30s", "TABU_SEARCH", 30000),
        ]

        results = []
        for name, metaheuristic, timeout in configs:
            order, objective = solve_tsp_with_timeout(
                stops,
                "X1-VH85-A1",
                graph,
                timeout,
                metaheuristic,
            )
            if order:
                crossings = count_crossing_edges(order, WAYPOINTS_X1_VH85)
                distance_total = calculate_tour_distance(order, WAYPOINTS_X1_VH85)
                results.append((name, distance_total, crossings, objective))

        print("\n" + "=" * 80)
        print("COMPARISON SUMMARY")
        print("=" * 80)
        print(f"{'Configuration':<35} {'Distance':>12} {'Crossings':>10} {'Objective':>12}")
        print("-" * 80)

        distance_cached = calculate_tour_distance(CACHED_TOUR_ORDER, WAYPOINTS_X1_VH85)
        crossings_cached = count_crossing_edges(CACHED_TOUR_ORDER, WAYPOINTS_X1_VH85)
        print(f"{'Cached tour (production)':<35} {distance_cached:>12.2f} {crossings_cached:>10} {'N/A':>12}")
        print("-" * 80)

        for name, dist, cross, obj in results:
            print(f"{name:<35} {dist:>12.2f} {cross:>10} {obj:>12.0f}")

        print("=" * 80)

        # Find best solution
        best = min(results, key=lambda x: (x[2], x[1]))  # Minimize crossings, then distance
        print(f"\nBEST SOLUTION: {best[0]}")
        print(f"  Distance: {best[1]:.2f} units")
        print(f"  Crossings: {best[2]}")

        if best[2] == 0:
            print("\n✓ Found a crossing-free solution!")
            print(f"  Recommendation: Use {best[0]} configuration")
        else:
            print(f"\n✗ All configurations produced crossings")
            print("  This suggests a deeper problem with the TSP setup")
