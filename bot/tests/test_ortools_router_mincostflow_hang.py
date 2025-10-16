#!/usr/bin/env python3
"""
Test for ORToolsRouter min-cost-flow solver hang bug.

HYPOTHESIS: The hang occurs in _solve_with_min_cost_flow() when the min cost flow
           solver takes exponential time on large graphs with many fuel states.

ROOT CAUSE THEORY:
  Line 484: flow.solve() has no timeout protection
  For 88 waypoints + fuel states (81 levels) = 7,128 nodes in flow graph
  Min cost flow solver might enter exponential search space

EXPECTED: Router should either:
  1. Complete routing in <10 seconds
  2. Time out gracefully with error message (not hang forever)

LOCATION: src/spacetraders_bot/core/ortools_router.py:484
"""

import time
import logging
from pathlib import Path

import pytest

from spacetraders_bot.core.ortools_router import ORToolsRouter
from spacetraders_bot.core.routing_config import RoutingConfig

# Enable debug logging to see where hang occurs
logging.basicConfig(level=logging.DEBUG)


def build_jb26_like_graph():
    """Build a graph like X1-JB26 with specific topology that might trigger hang."""
    waypoints = {}

    # Create 88 waypoints (same as X1-JB26)
    # Use realistic spatial distribution (not uniform grid)
    for i in range(88):
        letter_idx = i // 11
        number = (i % 11) + 1
        symbol = f"X1-TEST-{chr(65 + letter_idx)}{number}"

        # Clustered distribution with some isolated waypoints
        # This creates a mix of dense and sparse regions
        cluster_id = i // 20  # 4 clusters of ~20 waypoints each
        cluster_x = (cluster_id % 2) * 500
        cluster_y = (cluster_id // 2) * 500
        local_x = (i % 20) * 50
        local_y = ((i % 20) // 5) * 50

        waypoints[symbol] = {
            "type": "PLANET" if i % 15 == 0 else "ASTEROID",
            "x": cluster_x + local_x,
            "y": cluster_y + local_y,
            "traits": ["MARKETPLACE"] if i % 15 == 0 else [],
            "has_fuel": i % 15 == 0,  # 6 fuel stations
            "orbitals": [],
        }

    # Build dense edges (complete graph)
    edges = []
    symbols = list(waypoints.keys())

    print(f"Building complete graph with {len(symbols)} waypoints...")

    for i, frm in enumerate(symbols):
        wp_a = waypoints[frm]
        for target in symbols[i + 1:]:
            wp_b = waypoints[target]
            distance = ((wp_b["x"] - wp_a["x"])**2 + (wp_b["y"] - wp_a["y"])**2)**0.5
            edges.append({"from": frm, "to": target, "distance": round(distance, 2), "type": "synthetic"})
            edges.append({"from": target, "to": frm, "distance": round(distance, 2), "type": "synthetic"})

    print(f"Built graph: {len(waypoints)} waypoints, {len(edges)} edges")

    return {
        "system": "X1-TEST",
        "waypoints": waypoints,
        "edges": edges,
    }


@pytest.mark.timeout(30)  # pytest timeout as safety net
def test_ortools_router_mincostflow_completes_in_reasonable_time():
    """Test that min cost flow solver completes routing (doesn't hang)."""
    graph = build_jb26_like_graph()
    symbols = sorted(graph["waypoints"].keys())

    # Start at first waypoint, go to opposite corner (maximum distance)
    start = symbols[0]  # X1-TEST-A1
    goal = symbols[-1]  # X1-TEST-H8

    ship_data = {
        "symbol": "TEST-SHIP",
        "nav": {"waypointSymbol": start, "status": "IN_ORBIT"},
        "fuel": {"current": 399, "capacity": 400},
        "engine": {"speed": 10},
    }

    print(f"\nTest scenario:")
    print(f"  Graph: {len(symbols)} waypoints, {len(graph['edges'])} edges")
    print(f"  Route: {start} -> {goal}")
    print(f"  Fuel: {ship_data['fuel']['current']}/{ship_data['fuel']['capacity']}")

    router = ORToolsRouter(graph, ship_data, RoutingConfig())

    # Monitor execution to see where it hangs
    print("\n  Phase 1: Router initialization...")
    init_time = time.time()
    print(f"    ✓ Initialized in {time.time() - init_time:.3f}s")

    print("\n  Phase 2: Finding route...")
    route_start = time.time()

    route = router.find_optimal_route(start, goal, current_fuel=399, prefer_cruise=True)

    elapsed = time.time() - route_start

    # CRITICAL: Should complete in <10 seconds
    assert elapsed < 10.0, f"Routing took {elapsed:.2f}s (expected <10s) - min cost flow solver hanging!"

    # Verify route is valid
    assert route is not None, "Router should find a valid route"
    assert route["start"] == start
    assert route["goal"] == goal
    assert len(route["steps"]) > 0, "Route should have steps"

    print(f"\n  ✓ Route found in {elapsed:.2f}s")
    print(f"    Total time: {route['total_time']}s")
    print(f"    Final fuel: {route['final_fuel']}")
    print(f"    Steps: {len(route['steps'])}")


@pytest.mark.timeout(30)
def test_ortools_router_with_low_fuel_constraint():
    """Test routing with low fuel (triggers more refuel stops in min cost flow)."""
    graph = build_jb26_like_graph()
    symbols = sorted(graph["waypoints"].keys())

    start = symbols[0]
    goal = symbols[-1]

    # LOW FUEL: Forces min cost flow solver to find complex refueling paths
    ship_data = {
        "symbol": "TEST-SHIP",
        "nav": {"waypointSymbol": start, "status": "IN_ORBIT"},
        "fuel": {"current": 50, "capacity": 100},  # Low fuel capacity
        "engine": {"speed": 10},
    }

    print(f"\nLow fuel test:")
    print(f"  Fuel: {ship_data['fuel']['current']}/{ship_data['fuel']['capacity']}")

    router = ORToolsRouter(graph, ship_data, RoutingConfig())

    start_time = time.time()
    route = router.find_optimal_route(start, goal, current_fuel=50, prefer_cruise=True)
    elapsed = time.time() - start_time

    # Should still complete in reasonable time
    assert elapsed < 10.0, f"Low-fuel routing took {elapsed:.2f}s (hang detected!)"

    if route:
        print(f"  ✓ Route found in {elapsed:.2f}s with {len(route['steps'])} steps")
    else:
        print(f"  × No route found (may be legitimate if unreachable)")


def test_ortools_router_dijkstra_pathfinding_timing():
    """Isolate Dijkstra pathfinding to measure its performance separately."""
    graph = build_jb26_like_graph()
    symbols = sorted(graph["waypoints"].keys())

    ship_data = {
        "symbol": "TEST-SHIP",
        "nav": {"waypointSymbol": symbols[0], "status": "IN_ORBIT"},
        "fuel": {"current": 399, "capacity": 400},
        "engine": {"speed": 10},
    }

    router = ORToolsRouter(graph, ship_data, RoutingConfig())

    # Test Dijkstra pathfinding directly
    print("\nDijkstra pathfinding test:")
    start_time = time.time()
    path = router._find_waypoint_path(symbols[0], symbols[-1])
    elapsed = time.time() - start_time

    assert elapsed < 2.0, f"Dijkstra took {elapsed:.2f}s (should be <2s)"
    assert path is not None
    print(f"  ✓ Dijkstra found path in {elapsed:.3f}s ({len(path)} waypoints)")


if __name__ == "__main__":
    print("Testing ORToolsRouter min cost flow hang...")

    print("\n" + "=" * 70)
    print("TEST 1: Dijkstra Pathfinding Performance")
    print("=" * 70)
    test_ortools_router_dijkstra_pathfinding_timing()

    print("\n" + "=" * 70)
    print("TEST 2: Full Routing (High Fuel)")
    print("=" * 70)
    test_ortools_router_mincostflow_completes_in_reasonable_time()

    print("\n" + "=" * 70)
    print("TEST 3: Full Routing (Low Fuel - Complex Refueling)")
    print("=" * 70)
    test_ortools_router_with_low_fuel_constraint()

    print("\n✓ All tests passed!")
