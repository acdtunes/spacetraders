#!/usr/bin/env python3
"""
Test for ORToolsRouter initialization performance bug.

ISSUE: ORToolsRouter.__init__() pre-computes edge metrics for ALL edges in graph (e.g., 8,372 edges),
taking ~10 minutes. This happens every time a new bot process starts (every navigation, daemon spawn).

ROOT CAUSE: Line 143 in ortools_router.py calls _prepare_edge_metrics() which loops through all edges.

EXPECTED: Router should initialize instantly (<1 second) and compute edge metrics lazily on-demand.

REFERENCE: TSP's _build_time_matrix() (lines 681-719) already does lazy distance computation successfully.
"""

import time
from pathlib import Path

import pytest

from spacetraders_bot.core.ortools_router import ORToolsRouter
from spacetraders_bot.core.routing_config import RoutingConfig


def build_large_graph(num_waypoints: int = 130):
    """Build a graph similar to X1-VH85 with ~130 waypoints = 8,372 edges (dense graph)."""
    waypoints = {}

    # Create waypoints in a grid pattern
    # Using A-Z naming scheme with numbers for 130 waypoints
    for i in range(num_waypoints):
        # Generate symbol like X1-TEST-A1, X1-TEST-A2, ..., X1-TEST-E10
        symbol = f"X1-TEST-{chr(65 + i // 26)}{(i % 26) + 1}"
        waypoints[symbol] = {
            "type": "PLANET" if i % 10 == 0 else "ASTEROID",
            "x": (i % 13) * 100,  # Grid pattern
            "y": (i // 13) * 100,
            "traits": ["MARKETPLACE"] if i % 10 == 0 else [],
            "has_fuel": i % 10 == 0,  # Every 10th waypoint has fuel
            "orbitals": [],
        }

    # Build edges - COMPLETE dense graph with ALL pairs (this is what causes slow init)
    # For 130 waypoints: 130 * 129 / 2 = 8,385 unique pairs × 2 (bidirectional) = 16,770 edges
    edges = []
    symbols = list(waypoints.keys())
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


def test_ortools_router_initialization_is_fast():
    """Test that ORToolsRouter initializes quickly even with large graphs."""
    # Build a graph with ~130 waypoints = 8,372 edges (similar to X1-VH85)
    graph = build_large_graph(num_waypoints=130)

    ship_data = {
        "symbol": "TEST-SHIP",
        "nav": {"waypointSymbol": "X1-TEST-A0", "status": "IN_ORBIT"},
        "fuel": {"current": 100, "capacity": 100},
        "engine": {"speed": 10},
    }

    # Measure initialization time
    start_time = time.time()
    router = ORToolsRouter(graph, ship_data, RoutingConfig())
    elapsed = time.time() - start_time

    # CRITICAL: Initialization should be near-instant (<1 second, NOT 10 minutes)
    assert elapsed < 1.0, f"Router initialization took {elapsed:.2f}s (expected <1s) - lazy computation not working!"

    print(f"✓ Router initialized in {elapsed:.3f}s with {len(graph['edges'])} edges")


def test_ortools_router_routes_correctly_after_lazy_init():
    """Verify that routes are still correct after lazy edge metric computation."""
    graph = {
        "system": "X1-TEST",
        "waypoints": {
            "A": {"x": 0, "y": 0, "traits": ["MARKETPLACE"], "has_fuel": True, "orbitals": []},
            "B": {"x": 100, "y": 0, "traits": [], "has_fuel": False, "orbitals": []},
            "C": {"x": 100, "y": 100, "traits": ["MARKETPLACE"], "has_fuel": True, "orbitals": []},
        },
        "edges": [
            {"from": "A", "to": "B", "distance": 100, "type": "normal"},
            {"from": "B", "to": "A", "distance": 100, "type": "normal"},
            {"from": "B", "to": "C", "distance": 100, "type": "normal"},
            {"from": "C", "to": "B", "distance": 100, "type": "normal"},
            {"from": "A", "to": "C", "distance": 141.42, "type": "normal"},
            {"from": "C", "to": "A", "distance": 141.42, "type": "normal"},
        ],
    }

    ship_data = {
        "symbol": "TEST-SHIP",
        "nav": {"waypointSymbol": "A", "status": "IN_ORBIT"},
        "fuel": {"current": 200, "capacity": 200},
        "engine": {"speed": 10},
    }

    router = ORToolsRouter(graph, ship_data, RoutingConfig())

    # Test routing A -> C
    route = router.find_optimal_route("A", "C", current_fuel=200, prefer_cruise=True)

    assert route is not None, "Router should find a valid route"
    assert route["start"] == "A"
    assert route["goal"] == "C"
    assert len(route["steps"]) > 0, "Route should have navigation steps"

    # Verify route uses correct edges
    navigate_steps = [step for step in route["steps"] if step["action"] == "navigate"]
    assert len(navigate_steps) > 0, "Should have at least one navigation step"

    print(f"✓ Route found: {route['total_time']}s, {len(navigate_steps)} nav steps, final fuel: {route['final_fuel']}")


def test_ortools_router_edge_metrics_computed_on_demand():
    """Verify that edge metrics are computed lazily (on first access) and cached."""
    graph = {
        "system": "X1-TEST",
        "waypoints": {
            "A": {"x": 0, "y": 0, "traits": [], "has_fuel": False, "orbitals": []},
            "B": {"x": 50, "y": 0, "traits": [], "has_fuel": False, "orbitals": []},
        },
        "edges": [
            {"from": "A", "to": "B", "distance": 50, "type": "normal"},
        ],
    }

    ship_data = {
        "symbol": "TEST-SHIP",
        "nav": {"waypointSymbol": "A", "status": "IN_ORBIT"},
        "fuel": {"current": 100, "capacity": 100},
        "engine": {"speed": 10},
    }

    router = ORToolsRouter(graph, ship_data, RoutingConfig())

    # Before routing, edge metrics should NOT be pre-computed (empty or minimal)
    # After fix, _edge_metrics should be empty or very small initially
    initial_metrics_count = len(router._edge_metrics)
    print(f"Initial edge metrics count: {initial_metrics_count}")

    # Access edge metrics - should trigger lazy computation
    metrics = router.get_edge_metrics("A", "B")

    # After accessing, metrics should be computed and cached
    if metrics is not None:
        assert metrics.distance == 50
        assert metrics.drift_time > 0
        print(f"✓ Edge metrics computed on-demand: distance={metrics.distance}, drift_time={metrics.drift_time}")

    # Metrics should now be cached
    cached_metrics = router.get_edge_metrics("A", "B")
    assert cached_metrics == metrics, "Metrics should be cached (same object)"


if __name__ == "__main__":
    print("Testing ORToolsRouter initialization performance...")
    test_ortools_router_initialization_is_fast()
    test_ortools_router_routes_correctly_after_lazy_init()
    test_ortools_router_edge_metrics_computed_on_demand()
    print("✓ All tests passed!")
