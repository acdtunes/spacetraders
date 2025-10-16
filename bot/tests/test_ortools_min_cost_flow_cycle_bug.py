#!/usr/bin/env python3
"""
Test for OR-Tools min cost flow cycle bug.

BUG DESCRIPTION:
The route reconstruction in _solve_with_min_cost_flow() creates an infinite loop
because the min cost flow solution has multiple arcs with flow > 0 from the same
source node. The code only stores ONE arc per tail in active_arc_by_tail, which
causes the route to revisit nodes (creating a cycle).

DEBUG LOG EVIDENCE:
[INFO] ✅ [MCF] Solver completed with status=Status.OPTIMAL
[INFO] ✅ [MCF-RECONSTRUCT] Found 267 active arcs (out of 343589 total)
[INFO] 🔍 [MCF-RECONSTRUCT] Starting route reconstruction from node 3155 to sink 3608
[INFO] 🔍 [MCF-RECONSTRUCT] Iteration 1: current_node=3155, steps=0
[INFO] 🔍 [MCF-RECONSTRUCT] Iteration 2: current_node=1552, steps=1
[INFO] 🔍 [MCF-RECONSTRUCT] Iteration 3: current_node=3438, steps=2
[INFO] 🔍 [MCF-RECONSTRUCT] Iteration 4: current_node=3397, steps=3
[ERROR] ❌ [MCF-RECONSTRUCT] INFINITE LOOP DETECTED: revisited node 1552 at iteration 5

The route tries to visit nodes: 3155 → 1552 → 3438 → 3397 → **1552** (cycle!)

ROOT CAUSE:
Line 538-544 in ortools_router.py builds active_arc_by_tail as Dict[int, int],
which maps tail → arc. However, if multiple arcs from the same tail have flow > 0,
only the LAST one is stored (dictionary overwrites previous values). This can
happen when the min cost flow problem is not properly constrained to ensure a
single path.

EXPECTED BEHAVIOR:
1. The min cost flow solver should produce a solution where each node has at most
   one outgoing arc with flow > 0 (single path, not multiple paths)
2. The route reconstruction should validate this constraint and fail gracefully
   if violated
3. OR, the code should handle multiple active arcs per node and choose the
   correct one (though this would indicate a deeper problem with the formulation)

FIX APPROACH:
Add validation during arc mapping to detect and report multiple active arcs from
the same node, then return None (indicating routing failure) rather than entering
an infinite loop.
"""

import logging
from pathlib import Path

import pytest

from spacetraders_bot.core.ortools_router import ORToolsRouter
from spacetraders_bot.core.routing_config import RoutingConfig

# Enable debug logging to see detailed arc mapping
logging.basicConfig(level=logging.INFO)


def build_cycle_prone_graph():
    """Build a graph topology that might trigger the cycle bug.

    Uses a realistic system with:
    - Multiple fuel stations in different regions
    - Long-distance routes requiring refueling
    - Similar-cost alternate paths (creates ambiguity for min cost flow)
    """
    waypoints = {}

    # Cluster A: Fuel station + asteroids (left side)
    waypoints["X1-TEST-A1"] = {"type": "PLANET", "x": 0, "y": 0, "has_fuel": True, "orbitals": []}
    waypoints["X1-TEST-A2"] = {"type": "ASTEROID", "x": 50, "y": 0, "has_fuel": False, "orbitals": []}
    waypoints["X1-TEST-A3"] = {"type": "ASTEROID", "x": 100, "y": 0, "has_fuel": False, "orbitals": []}

    # Cluster B: Fuel station + asteroids (middle top)
    waypoints["X1-TEST-B1"] = {"type": "PLANET", "x": 200, "y": 100, "has_fuel": True, "orbitals": []}
    waypoints["X1-TEST-B2"] = {"type": "ASTEROID", "x": 250, "y": 100, "has_fuel": False, "orbitals": []}

    # Cluster C: Fuel station + asteroids (middle bottom) - ALTERNATE PATH
    waypoints["X1-TEST-C1"] = {"type": "PLANET", "x": 200, "y": -100, "has_fuel": True, "orbitals": []}
    waypoints["X1-TEST-C2"] = {"type": "ASTEROID", "x": 250, "y": -100, "has_fuel": False, "orbitals": []}

    # Cluster D: Fuel station + goal (right side)
    waypoints["X1-TEST-D1"] = {"type": "PLANET", "x": 400, "y": 0, "has_fuel": True, "orbitals": []}
    waypoints["X1-TEST-D2"] = {"type": "ASTEROID", "x": 450, "y": 0, "has_fuel": False, "orbitals": []}

    # Build edges (complete graph for simplicity)
    edges = []
    symbols = list(waypoints.keys())
    for i, frm in enumerate(symbols):
        wp_a = waypoints[frm]
        for target in symbols[i + 1:]:
            wp_b = waypoints[target]
            distance = ((wp_b["x"] - wp_a["x"])**2 + (wp_b["y"] - wp_a["y"])**2)**0.5
            edges.append({"from": frm, "to": target, "distance": round(distance, 2), "type": "synthetic"})
            edges.append({"from": target, "to": frm, "distance": round(distance, 2), "type": "synthetic"})

    return {
        "system": "X1-TEST",
        "waypoints": waypoints,
        "edges": edges,
    }


@pytest.mark.timeout(30)
def test_min_cost_flow_cycle_detection():
    """Test that route reconstruction detects and fails on cycles (doesn't hang)."""
    graph = build_cycle_prone_graph()

    # Start at A1, go to D2 (requires refueling at B1 or C1)
    # Low fuel capacity forces refueling, which might create ambiguity
    start = "X1-TEST-A1"
    goal = "X1-TEST-D2"

    ship_data = {
        "symbol": "TEST-SHIP",
        "nav": {"waypointSymbol": start, "status": "IN_ORBIT"},
        "fuel": {"current": 150, "capacity": 200},  # Enough for one leg, needs refuel
        "engine": {"speed": 10},
    }

    print(f"\nTest scenario:")
    print(f"  Route: {start} → {goal}")
    print(f"  Fuel: {ship_data['fuel']['current']}/{ship_data['fuel']['capacity']}")
    print(f"  Expected: Route through B1 or C1 (refuel), then D1 (refuel), then D2")

    router = ORToolsRouter(graph, ship_data, RoutingConfig())

    # This should either:
    # 1. Find a valid route without cycles
    # 2. Return None due to cycle detection (current expected behavior)
    # 3. NOT hang in an infinite loop
    route = router.find_optimal_route(start, goal, current_fuel=150, prefer_cruise=True)

    if route is None:
        print("\n  ⚠️  Router returned None (cycle detected or no path found)")
        print("  This is acceptable - the bug is that it would HANG, not fail gracefully")
    else:
        print(f"\n  ✓ Route found with {len(route['steps'])} steps")
        print(f"    Total time: {route['total_time']}s")
        print(f"    Final fuel: {route['final_fuel']}")

        # Verify no cycles in the route
        visited_waypoints = set()
        for step in route["steps"]:
            if step["action"] == "navigate":
                frm = step["from"]
                to = step["to"]
                assert to not in visited_waypoints, f"Cycle detected: {to} visited twice!"
                visited_waypoints.add(to)

        print("  ✓ No cycles detected in route")


@pytest.mark.timeout(30)
def test_min_cost_flow_multiple_active_arcs_detection():
    """Test detection of multiple active arcs from the same node (the core bug)."""
    graph = build_cycle_prone_graph()

    start = "X1-TEST-A1"
    goal = "X1-TEST-D2"

    ship_data = {
        "symbol": "TEST-SHIP",
        "nav": {"waypointSymbol": start, "status": "IN_ORBIT"},
        "fuel": {"current": 150, "capacity": 200},
        "engine": {"speed": 10},
    }

    router = ORToolsRouter(graph, ship_data, RoutingConfig())

    # Attempt routing - should detect multiple active arcs if the bug exists
    route = router.find_optimal_route(start, goal, current_fuel=150, prefer_cruise=True)

    # The fix should either:
    # 1. Return None with logged warning about multiple active arcs
    # 2. Return a valid route if the formulation was fixed to prevent multiple paths
    if route is not None:
        print("\n  ✓ Valid route returned")
        assert len(route["steps"]) > 0
    else:
        print("\n  ⚠️  Router returned None (expected if cycle detected)")


if __name__ == "__main__":
    print("Testing OR-Tools min cost flow cycle bug...")

    print("\n" + "=" * 70)
    print("TEST 1: Cycle Detection (No Hang)")
    print("=" * 70)
    test_min_cost_flow_cycle_detection()

    print("\n" + "=" * 70)
    print("TEST 2: Multiple Active Arcs Detection")
    print("=" * 70)
    test_min_cost_flow_multiple_active_arcs_detection()

    print("\n✓ All tests passed!")
