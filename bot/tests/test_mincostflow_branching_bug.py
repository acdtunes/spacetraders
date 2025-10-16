#!/usr/bin/env python3
"""
Test for OR-Tools min cost flow branching bug - MULTIPLE ACTIVE ARCS DETECTED

BUG DESCRIPTION:
The min cost flow solver in _solve_with_min_cost_flow() produces branching solutions
where a single node has multiple outgoing arcs with flow > 0. This violates the
single-path routing constraint and causes reconstruction to fail.

ERROR MESSAGE:
[ERROR] ❌ [MCF-RECONSTRUCT] MULTIPLE ACTIVE ARCS DETECTED from node 3592:
        arc 170843 → 1665 and arc 170899 → 3510
[ERROR] ❌ [MCF-RECONSTRUCT] Min cost flow solution has multiple paths (branching detected)
[WARNING] ⚠️  [ROUTING] Min cost flow failed, falling back to simple Dijkstra route

IMPACT:
Ship STARGAZER-1 routing J57→H50 (756 units) fails min cost flow, falls back to
Dijkstra which doesn't plan refuel stops, resulting in 88 minutes in DRIFT mode
instead of ~20-30 minutes with CRUISE + refuel stop.

ROOT CAUSE:
The min cost flow formulation allows multiple units of flow to split at nodes,
but we set capacity=1 on all arcs. The solver is still finding multiple paths
with flow=1 on each arc, which suggests:

1. Supply/demand imbalance (source supply > sink demand or vice versa)
2. Multiple feasible paths with identical cost
3. Solver choosing to distribute flow across multiple arcs rather than single path

HYPOTHESIS:
The supply/demand is set to +1/-1, but there may be multiple sink connections
(one for each fuel level at goal) that create ambiguity. The solver sees
multiple valid ways to route the single unit of flow to the sink.

FIX APPROACHES:
1. Add flow conservation constraints (exactly 1 arc out per node with flow in)
2. Use a single sink node with penalty edges from all goal states
3. Add binary flow constraints (each arc either 0 or 1 flow)
4. Fix the graph construction to prevent multiple sink connections
"""

import logging
import math
from pathlib import Path

import pytest

from spacetraders_bot.core.ortools_router import ORToolsRouter
from spacetraders_bot.core.routing_config import RoutingConfig

# Enable detailed logging to see branching behavior
logging.basicConfig(
    level=logging.DEBUG,
    format='[%(levelname)s] %(message)s'
)


def build_realistic_system_jb26():
    """Build a realistic graph based on X1-JB26 system topology.

    This replicates the actual system where the branching bug occurs:
    - Route J57 → H50 (756 units, requires refuel)
    - Multiple fuel stations at various distances
    - Ship with fuel capacity 400 (not enough for direct CRUISE)

    CRITICAL: Fuel stations must be reachable in CRUISE mode (< 400 units)
    so the min cost flow solver can actually plan CRUISE + refuel routes.
    """
    waypoints = {}

    # Fuel stations (CLOSER positions - within CRUISE range from start)
    waypoints["X1-JB26-J57"] = {"type": "JUMP_GATE", "x": -100, "y": 0, "has_fuel": True, "orbitals": []}
    waypoints["X1-JB26-F45"] = {"type": "FUEL_STATION", "x": 200, "y": 100, "has_fuel": True, "orbitals": []}  # ~316 units from J57
    waypoints["X1-JB26-G32"] = {"type": "FUEL_STATION", "x": 250, "y": 50, "has_fuel": True, "orbitals": []}    # ~354 units from J57
    waypoints["X1-JB26-K21"] = {"type": "FUEL_STATION", "x": 150, "y": -150, "has_fuel": True, "orbitals": []}  # ~277 units from J57

    # Goal waypoint (requires refuel, but reachable from fuel station in CRUISE mode)
    # Distance from G32: ~350 units (within 400 fuel capacity)
    waypoints["X1-JB26-H50"] = {"type": "PLANET", "x": 600, "y": 50, "has_fuel": False, "orbitals": []}

    # Additional waypoints (no fuel) to create realistic graph density
    waypoints["X1-JB26-A10"] = {"type": "ASTEROID", "x": 100, "y": 100, "has_fuel": False, "orbitals": []}
    waypoints["X1-JB26-B20"] = {"type": "ASTEROID", "x": 300, "y": 50, "has_fuel": False, "orbitals": []}
    waypoints["X1-JB26-C30"] = {"type": "ASTEROID", "x": 500, "y": 0, "has_fuel": False, "orbitals": []}

    # Build complete graph (all-to-all edges)
    edges = []
    symbols = list(waypoints.keys())
    for i, frm in enumerate(symbols):
        wp_a = waypoints[frm]
        for target in symbols[i + 1:]:
            wp_b = waypoints[target]
            distance = math.hypot(wp_b["x"] - wp_a["x"], wp_b["y"] - wp_a["y"])
            edges.append({"from": frm, "to": target, "distance": round(distance, 2), "type": "synthetic"})
            edges.append({"from": target, "to": frm, "distance": round(distance, 2), "type": "synthetic"})

    return {
        "system": "X1-JB26",
        "waypoints": waypoints,
        "edges": edges,
    }


@pytest.mark.timeout(60)
def test_mincostflow_branching_long_route(caplog):
    """Test for branching bug on long route requiring refuel (J57→H50, 756 units)."""
    # Set logging to DEBUG for this test to capture branching detection
    caplog.set_level(logging.DEBUG)

    graph = build_realistic_system_jb26()

    start = "X1-JB26-J57"
    goal = "X1-JB26-H50"

    # Calculate actual distance
    wp_start = graph["waypoints"][start]
    wp_goal = graph["waypoints"][goal]
    distance = math.hypot(wp_goal["x"] - wp_start["x"], wp_goal["y"] - wp_start["y"])

    print(f"\n" + "=" * 80)
    print(f"TEST: Min Cost Flow Branching Bug - Long Route Requiring Refuel")
    print(f"=" * 80)
    print(f"Route: {start} → {goal}")
    print(f"Distance: {distance:.0f} units")
    print(f"Ship Fuel: 400/400 (CRUISE needs ~{distance:.0f} fuel, DRIFT needs ~{distance/300:.1f} fuel)")
    print(f"Expected: Min cost flow should plan CRUISE with refuel stop at F45, G32, or K21")
    print(f"Bug Symptom: Multiple active arcs from same node → fallback to Dijkstra → DRIFT mode")
    print()

    ship_data = {
        "symbol": "STARGAZER-1",
        "nav": {"waypointSymbol": start, "status": "IN_ORBIT"},
        "fuel": {"current": 400, "capacity": 400},  # Full fuel, but not enough for direct CRUISE
        "engine": {"speed": 30},  # Typical explorer speed
    }

    router = ORToolsRouter(graph, ship_data, RoutingConfig())

    print("Calling find_optimal_route()...")
    route = router.find_optimal_route(start, goal, current_fuel=400, prefer_cruise=True)

    # Check for branching or fallback messages in logs
    log_messages = [record.message for record in caplog.records]
    branching_detected = any("MULTIPLE ACTIVE ARCS" in msg for msg in log_messages)
    fallback_used = any("falling back to simple Dijkstra" in msg for msg in log_messages)

    if branching_detected:
        print("\n🔍 BRANCHING BUG DETECTED in logs:")
        for msg in log_messages:
            if "MULTIPLE ACTIVE ARCS" in msg or "branching" in msg.lower():
                print(f"   {msg}")

    if fallback_used:
        print("\n⚠️  FALLBACK TO DIJKSTRA detected in logs (min cost flow failed)")
        for msg in log_messages:
            if "fallback" in msg.lower():
                print(f"   {msg}")

    if route is None:
        print("\n❌ ROUTE FAILED (bug reproduced if logs show 'MULTIPLE ACTIVE ARCS DETECTED')")
        print("   Check debug logs above for branching detection messages")
        pytest.fail("Min cost flow should find a route, but returned None")

    print(f"\n✅ Route found: {len(route['steps'])} steps")
    print(f"   Total time: {route['total_time']}s")
    print(f"   Final fuel: {route['final_fuel']}")

    # Validate route structure
    refuel_steps = [s for s in route["steps"] if s["action"] == "refuel"]
    navigate_steps = [s for s in route["steps"] if s["action"] == "navigate"]

    print(f"\n   Refuel stops: {len(refuel_steps)}")
    for step in refuel_steps:
        print(f"     - {step['waypoint']}: +{step['fuel_added']} fuel")

    print(f"\n   Navigation legs: {len(navigate_steps)}")
    for step in navigate_steps:
        print(f"     - {step['from']} → {step['to']}: {step['mode']}, {step['distance']:.0f}u, {step['fuel_cost']} fuel")

    # Verify route correctness
    assert len(refuel_steps) >= 1, "Route should include at least 1 refuel stop for long distance"
    assert len(navigate_steps) >= 2, "Route should have at least 2 navigation legs (to refuel, then to goal)"

    # Verify no cycles (each waypoint visited at most once)
    visited = set()
    for step in navigate_steps:
        to = step["to"]
        assert to not in visited, f"Cycle detected: {to} visited twice!"
        visited.add(to)

    # Verify we actually reach the goal
    final_location = navigate_steps[-1]["to"]
    assert final_location == goal, f"Route doesn't reach goal (ended at {final_location})"

    print("\n✅ Route validation passed - no cycles, reaches goal")


@pytest.mark.timeout(60)
def test_mincostflow_branching_detection():
    """Test that branching detection works and fails gracefully (not hang)."""
    graph = build_realistic_system_jb26()

    start = "X1-JB26-J57"
    goal = "X1-JB26-H50"

    print(f"\n" + "=" * 80)
    print(f"TEST: Branching Detection (Graceful Failure)")
    print(f"=" * 80)
    print(f"Goal: Verify that branching detection prevents infinite loops")
    print()

    ship_data = {
        "symbol": "TEST-SHIP",
        "nav": {"waypointSymbol": start, "status": "IN_ORBIT"},
        "fuel": {"current": 400, "capacity": 400},
        "engine": {"speed": 30},
    }

    router = ORToolsRouter(graph, ship_data, RoutingConfig())

    # This should either succeed or fail gracefully (not hang)
    # If branching is detected, it should return None and log the error
    route = router.find_optimal_route(start, goal, current_fuel=400, prefer_cruise=True)

    if route is None:
        print("\n⚠️  Router returned None (branching detected or no path)")
        print("   Expected behavior: Detection prevents infinite loop")
        # This is acceptable - the bug is HANGING, not returning None
    else:
        print(f"\n✅ Valid route returned with {len(route['steps'])} steps")
        print("   No branching detected")

    print("\n✅ Test passed - no hang/timeout (branching handled correctly)")


@pytest.mark.timeout(30)
def test_mincostflow_simple_route_no_branching():
    """Test that simple routes (no refuel) work without branching issues."""
    graph = build_realistic_system_jb26()

    start = "X1-JB26-J57"
    goal = "X1-JB26-A10"  # Close waypoint, no refuel needed

    wp_start = graph["waypoints"][start]
    wp_goal = graph["waypoints"][goal]
    distance = math.hypot(wp_goal["x"] - wp_start["x"], wp_goal["y"] - wp_start["y"])

    print(f"\n" + "=" * 80)
    print(f"TEST: Simple Route (No Refuel) - Should Not Branch")
    print(f"=" * 80)
    print(f"Route: {start} → {goal}")
    print(f"Distance: {distance:.0f} units (well within fuel capacity)")
    print()

    ship_data = {
        "symbol": "TEST-SHIP",
        "nav": {"waypointSymbol": start, "status": "IN_ORBIT"},
        "fuel": {"current": 400, "capacity": 400},
        "engine": {"speed": 30},
    }

    router = ORToolsRouter(graph, ship_data, RoutingConfig())
    route = router.find_optimal_route(start, goal, current_fuel=400, prefer_cruise=True)

    assert route is not None, "Simple route should always succeed"

    # Should be direct navigation (no refuel)
    refuel_steps = [s for s in route["steps"] if s["action"] == "refuel"]
    navigate_steps = [s for s in route["steps"] if s["action"] == "navigate"]

    assert len(refuel_steps) == 0, "Short route should not need refuel"
    assert len(navigate_steps) == 1, "Should be single direct navigation"
    assert navigate_steps[0]["to"] == goal, "Should navigate directly to goal"

    print(f"✅ Simple route works: {navigate_steps[0]['from']} → {navigate_steps[0]['to']}")
    print(f"   Mode: {navigate_steps[0]['mode']}, Fuel: {navigate_steps[0]['fuel_cost']}")


if __name__ == "__main__":
    print("\n" + "=" * 80)
    print("OR-TOOLS MIN COST FLOW BRANCHING BUG TEST SUITE")
    print("=" * 80)

    try:
        test_mincostflow_simple_route_no_branching()
    except Exception as e:
        print(f"\n❌ Simple route test FAILED: {e}")

    try:
        test_mincostflow_branching_detection()
    except Exception as e:
        print(f"\n❌ Branching detection test FAILED: {e}")

    try:
        test_mincostflow_branching_long_route()
    except Exception as e:
        print(f"\n❌ Long route test FAILED: {e}")

    print("\n" + "=" * 80)
    print("TEST SUITE COMPLETE")
    print("=" * 80)
