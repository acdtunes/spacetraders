#!/usr/bin/env python3
"""
Test for OR-Tools min cost flow ORBITAL CLUSTER branching bug.

BUG DESCRIPTION:
The min cost flow solver produces branching solutions when multiple orbital waypoints
have zero-distance connections (E43 ↔ E45 ↔ E46). The solver sees multiple equally-
optimal paths through orbital clusters and activates multiple arcs from the same node.

ERROR MESSAGE FROM PRODUCTION:
[ERROR] ❌ [MCF-RECONSTRUCT] MULTIPLE ACTIVE ARCS DETECTED from node 341:
        arc 37153 → 746 and arc 37155 → 755
        Metadata: prev={'type': 'navigate', 'from': 'X1-JB26-E43', 'to': 'X1-JB26-E45', ...}
                 current={'type': 'navigate', 'from': 'X1-JB26-E43', 'to': 'X1-JB26-E46', ...}

[ERROR] ❌ [MCF-RECONSTRUCT] MULTIPLE ACTIVE ARCS DETECTED from node 746:
        arc 23584 → 737 and arc 62762 → 792
        Metadata: prev={'type': 'navigate', 'from': 'X1-JB26-E45', 'to': 'X1-JB26-E44', ...}
                 current={'type': 'sink', 'goal_fuel': 80}

ROOT CAUSE:
Zero-distance orbital paths (distance=0, cost=0) create multiple equally-optimal routes.
Even after sink connection fix (lines 674-685), the branching occurs because:

1. Multiple orbital waypoints have zero-distance connections
2. Solver sees multiple paths with identical cost (all 0)
3. Multiple arcs from same node are activated (branching)
4. Arc capacity=1 doesn't prevent this (constrains per-arc, not per-node)

FIX APPROACH:
Need deterministic tie-breaking when multiple equally-optimal paths exist.
See Option 4 in bug report (deterministic reconstruction filter).
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


def build_orbital_cluster_graph():
    """Build a graph with orbital cluster (planet + moons at same coordinates).

    This replicates the X1-JB26-E43/E45/E46 scenario where:
    - E43 is the parent planet
    - E44, E45, E46 are moons orbiting E43 (distance = 0 between all)
    - Multiple zero-distance paths create branching ambiguity

    The key is that all orbitals have IDENTICAL coordinates in database,
    creating distance=0 between them.
    """
    waypoints = {}

    # Orbital cluster at (0, 0) - Planet E43 + 3 moons
    # CRITICAL: All orbitals have SAME coordinates (distance=0 between all)
    waypoints["X1-JB26-E43"] = {
        "type": "PLANET",
        "x": 0,
        "y": 0,
        "has_fuel": True,
        "orbitals": ["X1-JB26-E44", "X1-JB26-E45", "X1-JB26-E46"]
    }
    waypoints["X1-JB26-E44"] = {
        "type": "MOON",
        "x": 0,  # Same coordinates as parent
        "y": 0,
        "has_fuel": False,
        "orbits": "X1-JB26-E43",
        "orbitals": []
    }
    waypoints["X1-JB26-E45"] = {
        "type": "MOON",
        "x": 0,  # Same coordinates as parent
        "y": 0,
        "has_fuel": False,
        "orbits": "X1-JB26-E43",
        "orbitals": []
    }
    waypoints["X1-JB26-E46"] = {
        "type": "MOON",
        "x": 0,  # Same coordinates as parent
        "y": 0,
        "has_fuel": False,
        "orbits": "X1-JB26-E43",
        "orbitals": []
    }

    # Additional waypoints for routing context
    waypoints["X1-JB26-A1"] = {"type": "JUMP_GATE", "x": -200, "y": 0, "has_fuel": True, "orbitals": []}
    waypoints["X1-JB26-G50"] = {"type": "ASTEROID", "x": 200, "y": 0, "has_fuel": False, "orbitals": []}

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
def test_orbital_cluster_branching_bug(caplog):
    """Test for branching bug when routing through orbital cluster (E43 ↔ E45 ↔ E46)."""
    caplog.set_level(logging.DEBUG)

    graph = build_orbital_cluster_graph()

    # Route through orbital cluster: A1 → E43 (planet) → E45 (moon)
    # The zero-distance E43 ↔ E45 edge creates branching opportunities
    start = "X1-JB26-A1"
    goal = "X1-JB26-E45"

    print(f"\n" + "=" * 80)
    print(f"TEST: Orbital Cluster Branching Bug")
    print(f"=" * 80)
    print(f"Route: {start} → {goal}")
    print(f"Expected: Direct route A1 → E43 → E45 (via zero-distance orbital hop)")
    print(f"Bug Symptom: Multiple active arcs at E43 (branching to E44, E45, E46)")
    print()

    ship_data = {
        "symbol": "TEST-SHIP",
        "nav": {"waypointSymbol": start, "status": "IN_ORBIT"},
        "fuel": {"current": 400, "capacity": 400},
        "engine": {"speed": 30},
    }

    router = ORToolsRouter(graph, ship_data, RoutingConfig())

    print("Calling find_optimal_route()...")
    route = router.find_optimal_route(start, goal, current_fuel=400, prefer_cruise=True)

    # Check for branching in logs
    log_messages = [record.message for record in caplog.records]
    branching_detected = any("MULTIPLE ACTIVE ARCS" in msg for msg in log_messages)

    if branching_detected:
        print("\n🔍 BRANCHING BUG DETECTED:")
        for msg in log_messages:
            if "MULTIPLE ACTIVE ARCS" in msg or "branching" in msg.lower():
                print(f"   {msg}")

        # This is the bug we're trying to fix
        # The fix should eliminate branching by deterministic tie-breaking
        pytest.fail(
            "Orbital cluster branching detected - multiple active arcs from same node. "
            "This indicates the min cost flow solver created multiple paths through "
            "zero-distance orbital connections."
        )

    if route is None:
        print("\n⚠️  Route failed (returned None)")
        pytest.fail("Router should find valid route through orbital cluster")

    print(f"\n✅ Route found: {len(route['steps'])} steps")
    print(f"   Total time: {route['total_time']}s")
    print(f"   Final fuel: {route['final_fuel']}")

    # Validate route structure
    navigate_steps = [s for s in route["steps"] if s["action"] == "navigate"]
    print(f"\n   Navigation legs: {len(navigate_steps)}")
    for step in navigate_steps:
        print(f"     - {step['from']} → {step['to']}: {step['mode']}, {step['distance']:.0f}u, {step['fuel_cost']} fuel")

    # Verify route reaches goal
    assert navigate_steps[-1]["to"] == goal, f"Route doesn't reach goal"

    # Verify no cycles
    visited = set()
    for step in navigate_steps:
        to = step["to"]
        assert to not in visited, f"Cycle detected: {to} visited twice!"
        visited.add(to)

    print("\n✅ Route validation passed - no branching, no cycles, reaches goal")


@pytest.mark.timeout(60)
def test_orbital_cluster_multiple_goals(caplog):
    """Test branching when multiple equally-optimal goals exist in orbital cluster."""
    caplog.set_level(logging.DEBUG)

    graph = build_orbital_cluster_graph()

    # Route to E43 (the planet) - solver could choose to arrive at E43, E44, E45, or E46
    # (all have distance=0 from each other, so equally optimal sink connections)
    start = "X1-JB26-A1"
    goal = "X1-JB26-E43"

    print(f"\n" + "=" * 80)
    print(f"TEST: Orbital Cluster - Multiple Sink Connection Branching")
    print(f"=" * 80)
    print(f"Route: {start} → {goal}")
    print(f"Expected: Direct route to E43")
    print(f"Bug Symptom: Solver connects multiple orbital waypoints to sink (all cost=0)")
    print()

    ship_data = {
        "symbol": "TEST-SHIP",
        "nav": {"waypointSymbol": start, "status": "IN_ORBIT"},
        "fuel": {"current": 400, "capacity": 400},
        "engine": {"speed": 30},
    }

    router = ORToolsRouter(graph, ship_data, RoutingConfig())

    print("Calling find_optimal_route()...")
    route = router.find_optimal_route(start, goal, current_fuel=400, prefer_cruise=True)

    # Check for branching in logs
    log_messages = [record.message for record in caplog.records]
    branching_detected = any("MULTIPLE ACTIVE ARCS" in msg for msg in log_messages)

    if branching_detected:
        print("\n🔍 BRANCHING BUG DETECTED:")
        for msg in log_messages:
            if "MULTIPLE ACTIVE ARCS" in msg:
                print(f"   {msg}")

        pytest.fail(
            "Multiple sink connection branching detected. "
            "Solver created multiple paths to different orbital waypoints."
        )

    if route is None:
        print("\n⚠️  Route failed (returned None)")
        pytest.fail("Router should find valid route to orbital planet")

    print(f"\n✅ Route found: {len(route['steps'])} steps")

    # Verify route reaches goal
    navigate_steps = [s for s in route["steps"] if s["action"] == "navigate"]
    assert navigate_steps[-1]["to"] == goal, f"Route doesn't reach goal"

    print("\n✅ Route validation passed - no branching")


@pytest.mark.timeout(60)
def test_orbital_cluster_complex_route(caplog):
    """Test routing through orbital cluster with intermediate waypoints."""
    caplog.set_level(logging.DEBUG)

    graph = build_orbital_cluster_graph()

    # Complex route: A1 → E43 → G50 (passes through orbital cluster)
    start = "X1-JB26-A1"
    goal = "X1-JB26-G50"

    print(f"\n" + "=" * 80)
    print(f"TEST: Complex Route Through Orbital Cluster")
    print(f"=" * 80)
    print(f"Route: {start} → {goal} (via E43 orbital cluster)")
    print(f"Expected: A1 → E43 → G50 (direct path)")
    print(f"Bug Symptom: Branching at E43 (multiple orbital hops before continuing)")
    print()

    ship_data = {
        "symbol": "TEST-SHIP",
        "nav": {"waypointSymbol": start, "status": "IN_ORBIT"},
        "fuel": {"current": 400, "capacity": 400},
        "engine": {"speed": 30},
    }

    router = ORToolsRouter(graph, ship_data, RoutingConfig())

    print("Calling find_optimal_route()...")
    route = router.find_optimal_route(start, goal, current_fuel=400, prefer_cruise=True)

    # Check for branching in logs
    log_messages = [record.message for record in caplog.records]
    branching_detected = any("MULTIPLE ACTIVE ARCS" in msg for msg in log_messages)

    if branching_detected:
        print("\n🔍 BRANCHING BUG DETECTED:")
        for msg in log_messages:
            if "MULTIPLE ACTIVE ARCS" in msg:
                print(f"   {msg}")

        pytest.fail("Orbital cluster branching detected during complex route")

    if route is None:
        print("\n⚠️  Route failed (returned None)")
        pytest.fail("Router should find valid route through orbital cluster")

    print(f"\n✅ Route found: {len(route['steps'])} steps")

    # Validate route
    navigate_steps = [s for s in route["steps"] if s["action"] == "navigate"]
    print(f"\n   Navigation legs: {len(navigate_steps)}")
    for step in navigate_steps:
        print(f"     - {step['from']} → {step['to']}: {step['mode']}, {step['distance']:.0f}u")

    # Should be 2 legs: A1 → (somewhere in E43 cluster) → G50
    # Or possibly direct: A1 → G50
    assert len(navigate_steps) >= 1, "Route should have at least 1 navigation leg"
    assert navigate_steps[-1]["to"] == goal, f"Route doesn't reach goal"

    # Verify no cycles
    visited = set()
    for step in navigate_steps:
        to = step["to"]
        assert to not in visited, f"Cycle detected: {to} visited twice!"
        visited.add(to)

    print("\n✅ Route validation passed - no branching, no cycles")


if __name__ == "__main__":
    print("\n" + "=" * 80)
    print("OR-TOOLS ORBITAL CLUSTER BRANCHING BUG TEST SUITE")
    print("=" * 80)

    try:
        test_orbital_cluster_branching_bug(None)
    except Exception as e:
        print(f"\n❌ Orbital cluster branching test FAILED: {e}")

    try:
        test_orbital_cluster_multiple_goals(None)
    except Exception as e:
        print(f"\n❌ Multiple goals test FAILED: {e}")

    try:
        test_orbital_cluster_complex_route(None)
    except Exception as e:
        print(f"\n❌ Complex route test FAILED: {e}")

    print("\n" + "=" * 80)
    print("TEST SUITE COMPLETE")
    print("=" * 80)
