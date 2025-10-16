#!/usr/bin/env python3
"""
Test for ORToolsRouter hang bug during route calculation.

ISSUE: Router hangs at 97.8% CPU when calling find_optimal_route() on large graphs (88+ waypoints).
       Process never completes and produces no error logs.

ROOT CAUSE: _find_waypoint_path() Dijkstra algorithm has O(E³) complexity due to inefficient edge lookup.
            Line 231-235 searches through ALL edges for EVERY neighbor access during pathfinding.

            For X1-JB26 (88 waypoints, 7,656 edges):
            - Dijkstra explores ~7,656 waypoint-neighbor pairs
            - Each pair triggers linear search through 7,656 edges
            - Total: 7,656 × 7,656 = 58,614,336 linear searches = hang

EXPECTED: Router should compute routes in <5 seconds even for large graphs (88 waypoints).

LOCATION: src/spacetraders_bot/core/ortools_router.py:231-235, 312-363
"""

import time
from pathlib import Path

import pytest

from spacetraders_bot.core.ortools_router import ORToolsRouter
from spacetraders_bot.core.routing_config import RoutingConfig


def build_large_dense_graph(num_waypoints: int = 88):
    """Build a dense graph like X1-JB26 with ~88 waypoints and ~7,656 edges."""
    waypoints = {}

    # Create waypoints in a grid pattern (similar to real system)
    for i in range(num_waypoints):
        # Generate symbol like X1-TEST-A1, X1-TEST-A2, ..., X1-TEST-H11
        letter_idx = i // 11
        number = (i % 11) + 1
        symbol = f"X1-TEST-{chr(65 + letter_idx)}{number}"

        waypoints[symbol] = {
            "type": "PLANET" if i % 10 == 0 else "ASTEROID",
            "x": (i % 11) * 100,  # Grid pattern
            "y": (i // 11) * 100,
            "traits": ["MARKETPLACE"] if i % 10 == 0 else [],
            "has_fuel": i % 10 == 0,  # Every 10th waypoint has fuel
            "orbitals": [],
        }

    # Build COMPLETE dense graph with ALL pairs (this triggers the hang)
    # For 88 waypoints: 88 × 87 / 2 = 3,828 unique pairs × 2 (bidirectional) = 7,656 edges
    edges = []
    symbols = list(waypoints.keys())

    print(f"Building dense graph with {num_waypoints} waypoints...")

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


def test_ortools_router_does_not_hang_on_large_graph():
    """Test that router completes routing in reasonable time (no hang) for 88-waypoint graph."""
    # Build graph similar to X1-JB26 (88 waypoints, 7,656 edges)
    graph = build_large_dense_graph(num_waypoints=88)

    # Start at first waypoint (like X1-JB26-A2)
    symbols = sorted(graph["waypoints"].keys())
    start = symbols[0]  # X1-TEST-A1
    goal = symbols[-1]  # X1-TEST-H11 (opposite corner)

    ship_data = {
        "symbol": "TEST-SHIP",
        "nav": {"waypointSymbol": start, "status": "IN_ORBIT"},
        "fuel": {"current": 399, "capacity": 400},
        "engine": {"speed": 10},
    }

    router = ORToolsRouter(graph, ship_data, RoutingConfig())

    print(f"Testing route: {start} -> {goal}")
    print(f"Ship fuel: {ship_data['fuel']['current']}/{ship_data['fuel']['capacity']}")

    # Measure routing time - should complete in <5 seconds (NOT hang forever)
    start_time = time.time()
    route = router.find_optimal_route(start, goal, current_fuel=399, prefer_cruise=True)
    elapsed = time.time() - start_time

    # CRITICAL: Route calculation should complete quickly, not hang
    # Before fix: hangs forever (97.8% CPU, no progress)
    # After fix: completes in <5 seconds
    assert elapsed < 10.0, f"Routing took {elapsed:.2f}s (expected <10s) - likely hanging due to O(E³) complexity!"

    # Verify route is valid
    assert route is not None, "Router should find a valid route"
    assert route["start"] == start
    assert route["goal"] == goal
    assert len(route["steps"]) > 0, "Route should have navigation steps"

    print(f"✓ Route computed in {elapsed:.2f}s (total time: {route['total_time']}s, final fuel: {route['final_fuel']})")
    print(f"  Steps: {len(route['steps'])}")


def test_ortools_router_edge_lookup_performance():
    """Test that edge metric computation doesn't search linearly through all edges."""
    # Build small graph to verify edge lookup is efficient
    graph = {
        "system": "X1-TEST",
        "waypoints": {
            "A": {"x": 0, "y": 0, "traits": [], "has_fuel": False, "orbitals": []},
            "B": {"x": 50, "y": 0, "traits": [], "has_fuel": False, "orbitals": []},
            "C": {"x": 100, "y": 0, "traits": [], "has_fuel": False, "orbitals": []},
        },
        "edges": [
            {"from": "A", "to": "B", "distance": 50, "type": "normal"},
            {"from": "B", "to": "A", "distance": 50, "type": "normal"},
            {"from": "B", "to": "C", "distance": 50, "type": "normal"},
            {"from": "C", "to": "B", "distance": 50, "type": "normal"},
            {"from": "A", "to": "C", "distance": 100, "type": "normal"},
            {"from": "C", "to": "A", "distance": 100, "type": "normal"},
        ],
    }

    ship_data = {
        "symbol": "TEST-SHIP",
        "nav": {"waypointSymbol": "A", "status": "IN_ORBIT"},
        "fuel": {"current": 200, "capacity": 200},
        "engine": {"speed": 10},
    }

    router = ORToolsRouter(graph, ship_data, RoutingConfig())

    # Access edge metrics multiple times - should be fast (cached)
    start_time = time.time()
    for _ in range(1000):
        metrics = router.get_edge_metrics("A", "B")
        assert metrics is not None
    elapsed = time.time() - start_time

    # Should complete 1000 lookups in <0.1s (if properly cached)
    assert elapsed < 0.1, f"1000 edge lookups took {elapsed:.3f}s - caching not working!"
    print(f"✓ 1000 edge lookups in {elapsed:.3f}s (caching works)")


def test_dijkstra_pathfinding_performance():
    """Test that Dijkstra pathfinding (_find_waypoint_path) is efficient on large graphs."""
    # Build medium-sized graph (40 waypoints, ~1,560 edges)
    graph = build_large_dense_graph(num_waypoints=40)
    symbols = sorted(graph["waypoints"].keys())

    ship_data = {
        "symbol": "TEST-SHIP",
        "nav": {"waypointSymbol": symbols[0], "status": "IN_ORBIT"},
        "fuel": {"current": 200, "capacity": 200},
        "engine": {"speed": 10},
    }

    router = ORToolsRouter(graph, ship_data, RoutingConfig())

    # Call _find_waypoint_path directly (this is where the hang occurs)
    start_time = time.time()
    path = router._find_waypoint_path(symbols[0], symbols[-1])
    elapsed = time.time() - start_time

    # Should complete quickly even with 40 waypoints and 1,560 edges
    assert elapsed < 2.0, f"Dijkstra took {elapsed:.2f}s (expected <2s) - O(E³) complexity detected!"
    assert path is not None, "Should find a valid path"
    assert len(path) >= 2, "Path should have at least start and goal"

    print(f"✓ Dijkstra found path in {elapsed:.3f}s ({len(path)} waypoints)")


if __name__ == "__main__":
    print("Testing ORToolsRouter hang bug...")
    print("\n1. Testing Dijkstra pathfinding performance...")
    test_dijkstra_pathfinding_performance()

    print("\n2. Testing edge lookup performance...")
    test_ortools_router_edge_lookup_performance()

    print("\n3. Testing full route calculation (88 waypoints)...")
    test_ortools_router_does_not_hang_on_large_graph()

    print("\n✓ All tests passed!")
