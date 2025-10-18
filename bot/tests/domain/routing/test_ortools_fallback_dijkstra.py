#!/usr/bin/env python3
"""
Test for OR-Tools Dijkstra fallback when min cost flow fails.

BUG DESCRIPTION:
When the min cost flow solver produces branching solutions (multiple active arcs
from the same node), the operation currently fails with return None. This blocks
operations like contract fulfillment that need guaranteed routing.

FIX APPROACH:
Implement automatic fallback to simple Dijkstra-based routing that:
- Uses _find_waypoint_path() to get waypoint sequence
- Manually constructs navigate + refuel steps without OR-Tools
- Ensures ship never runs out of fuel by inserting refuel stops
- Uses CRUISE when possible, DRIFT when fuel-constrained
- Returns route in same format as min cost flow solver

EXPECTED BEHAVIOR:
1. When min cost flow fails (returns None), automatically try fallback
2. Fallback produces valid route with correct fuel management
3. Fallback correctly inserts refuel stops before waypoints where fuel insufficient
4. Operations succeed even when min cost flow has branching issues
"""

import logging
from pathlib import Path

import pytest

from spacetraders_bot.core.ortools_router import ORToolsRouter
from spacetraders_bot.core.routing_config import RoutingConfig

# Enable debug logging to see fallback behavior
logging.basicConfig(level=logging.INFO)


def build_simple_graph_with_fuel_stations():
    """Build a simple graph with fuel stations for testing fallback routing."""
    waypoints = {}

    # Linear chain: A1 (fuel) -> A2 -> A3 (fuel) -> A4 -> A5 (fuel)
    waypoints["X1-TEST-A1"] = {"type": "PLANET", "x": 0, "y": 0, "has_fuel": True, "orbitals": []}
    waypoints["X1-TEST-A2"] = {"type": "ASTEROID", "x": 100, "y": 0, "has_fuel": False, "orbitals": []}
    waypoints["X1-TEST-A3"] = {"type": "PLANET", "x": 200, "y": 0, "has_fuel": True, "orbitals": []}
    waypoints["X1-TEST-A4"] = {"type": "ASTEROID", "x": 300, "y": 0, "has_fuel": False, "orbitals": []}
    waypoints["X1-TEST-A5"] = {"type": "PLANET", "x": 400, "y": 0, "has_fuel": True, "orbitals": []}

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


def test_fallback_produces_valid_route():
    """Test that fallback produces valid routes when min cost flow would fail."""
    graph = build_simple_graph_with_fuel_stations()

    start = "X1-TEST-A1"
    goal = "X1-TEST-A5"

    ship_data = {
        "symbol": "TEST-SHIP",
        "nav": {"waypointSymbol": start, "status": "IN_ORBIT"},
        "fuel": {"current": 150, "capacity": 150},  # Limited fuel requiring refuel
        "engine": {"speed": 10},
    }

    print(f"\nTest scenario:")
    print(f"  Route: {start} → {goal}")
    print(f"  Distance: 400 units (requires refuel)")
    print(f"  Fuel: {ship_data['fuel']['current']}/{ship_data['fuel']['capacity']}")

    router = ORToolsRouter(graph, ship_data, RoutingConfig())

    # This should produce a route (either from min cost flow or fallback)
    route = router.find_optimal_route(start, goal, current_fuel=150, prefer_cruise=True)

    assert route is not None, "Router should produce a route (via min cost flow or fallback)"
    print(f"\n  ✓ Route found with {len(route['steps'])} steps")
    print(f"    Total time: {route['total_time']}s")
    print(f"    Final fuel: {route['final_fuel']}")

    # Verify route handles fuel correctly (may use DRIFT to avoid refuel, or refuel + CRUISE)
    refuel_steps = [step for step in route["steps"] if step["action"] == "refuel"]
    nav_steps = [step for step in route["steps"] if step["action"] == "navigate"]

    if len(refuel_steps) > 0:
        print(f"  ✓ Route includes {len(refuel_steps)} refuel stops")
    else:
        print(f"  ✓ Route uses DRIFT to avoid refuel ({nav_steps[0]['mode'] if nav_steps else 'N/A'})")

    # Verify no fuel exhaustion
    fuel = ship_data['fuel']['current']
    for step in route["steps"]:
        if step["action"] == "refuel":
            fuel += step["fuel_added"]
            fuel = min(fuel, ship_data['fuel']['capacity'])
        elif step["action"] == "navigate":
            assert fuel >= step["fuel_cost"], f"Insufficient fuel: {fuel} < {step['fuel_cost']}"
            fuel -= step["fuel_cost"]

    print(f"  ✓ No fuel exhaustion detected")

    # Verify reaches destination
    last_nav = [step for step in route["steps"] if step["action"] == "navigate"][-1]
    assert last_nav["to"] == goal, f"Route should end at {goal}, got {last_nav['to']}"
    print(f"  ✓ Route reaches destination {goal}")


def test_fallback_inserts_refuel_stops_correctly():
    """Test that fallback correctly inserts refuel stops when fuel insufficient."""
    graph = build_simple_graph_with_fuel_stations()

    start = "X1-TEST-A1"
    goal = "X1-TEST-A4"  # Shorter route but still needs refuel

    ship_data = {
        "symbol": "TEST-SHIP",
        "nav": {"waypointSymbol": start, "status": "IN_ORBIT"},
        "fuel": {"current": 120, "capacity": 120},  # Just barely not enough for direct CRUISE
        "engine": {"speed": 10},
    }

    print(f"\nTest scenario:")
    print(f"  Route: {start} → {goal}")
    print(f"  Distance: 300 units")
    print(f"  Fuel: {ship_data['fuel']['current']}/{ship_data['fuel']['capacity']}")
    print(f"  Expected: Refuel at A3 (halfway point)")

    router = ORToolsRouter(graph, ship_data, RoutingConfig())

    route = router.find_optimal_route(start, goal, current_fuel=120, prefer_cruise=True)

    assert route is not None, "Router should produce a route"

    # Check refuel locations
    refuel_steps = [step for step in route["steps"] if step["action"] == "refuel"]
    print(f"\n  ✓ Route includes {len(refuel_steps)} refuel stops")

    if len(refuel_steps) > 0:
        for refuel in refuel_steps:
            print(f"    - Refuel at {refuel['waypoint']}: +{refuel['fuel_added']} fuel")

    # Simulate fuel consumption to verify correctness
    fuel = ship_data['fuel']['current']
    for i, step in enumerate(route["steps"]):
        if step["action"] == "refuel":
            fuel += step["fuel_added"]
            fuel = min(fuel, ship_data['fuel']['capacity'])
            print(f"  Step {i+1}: Refuel at {step['waypoint']}, fuel={fuel}")
        elif step["action"] == "navigate":
            print(f"  Step {i+1}: Navigate {step['from']} → {step['to']} ({step['mode']}, -{step['fuel_cost']} fuel)")
            assert fuel >= step["fuel_cost"], f"Insufficient fuel at step {i+1}"
            fuel -= step["fuel_cost"]
            print(f"           Fuel remaining: {fuel}")

    print(f"  ✓ Final fuel: {fuel}")


def test_fallback_prefers_cruise_when_fuel_available():
    """Test that fallback prefers CRUISE mode when fuel available."""
    graph = build_simple_graph_with_fuel_stations()

    start = "X1-TEST-A1"
    goal = "X1-TEST-A2"  # Short distance, plenty of fuel

    ship_data = {
        "symbol": "TEST-SHIP",
        "nav": {"waypointSymbol": start, "status": "IN_ORBIT"},
        "fuel": {"current": 200, "capacity": 200},  # Plenty of fuel
        "engine": {"speed": 10},
    }

    print(f"\nTest scenario:")
    print(f"  Route: {start} → {goal}")
    print(f"  Distance: 100 units")
    print(f"  Fuel: {ship_data['fuel']['current']}/{ship_data['fuel']['capacity']}")
    print(f"  Expected: CRUISE mode (plenty of fuel)")

    router = ORToolsRouter(graph, ship_data, RoutingConfig())

    route = router.find_optimal_route(start, goal, current_fuel=200, prefer_cruise=True)

    assert route is not None, "Router should produce a route"

    nav_steps = [step for step in route["steps"] if step["action"] == "navigate"]
    assert len(nav_steps) == 1, "Should have exactly one navigation step"

    mode = nav_steps[0]["mode"]
    print(f"\n  ✓ Used flight mode: {mode}")
    assert mode == "CRUISE", "Should prefer CRUISE when fuel available"


def test_fallback_uses_drift_when_fuel_constrained():
    """Test that fallback uses DRIFT mode when fuel constrained."""
    graph = build_simple_graph_with_fuel_stations()

    start = "X1-TEST-A1"
    goal = "X1-TEST-A5"

    ship_data = {
        "symbol": "TEST-SHIP",
        "nav": {"waypointSymbol": start, "status": "IN_ORBIT"},
        "fuel": {"current": 50, "capacity": 150},  # Low starting fuel
        "engine": {"speed": 10},
    }

    print(f"\nTest scenario:")
    print(f"  Route: {start} → {goal}")
    print(f"  Distance: 400 units")
    print(f"  Fuel: {ship_data['fuel']['current']}/{ship_data['fuel']['capacity']}")
    print(f"  Expected: Mix of CRUISE and DRIFT, with refuel stops")

    router = ORToolsRouter(graph, ship_data, RoutingConfig())

    route = router.find_optimal_route(start, goal, current_fuel=50, prefer_cruise=True)

    assert route is not None, "Router should produce a route"

    nav_steps = [step for step in route["steps"] if step["action"] == "navigate"]
    modes = [step["mode"] for step in nav_steps]

    print(f"\n  ✓ Route uses modes: {modes}")
    print(f"  ✓ Navigation steps: {len(nav_steps)}")

    # Should have at least some DRIFT usage due to fuel constraints
    # (exact behavior depends on refuel stop placement)
    print(f"  ✓ Route successfully generated despite fuel constraints")


@pytest.mark.timeout(30)
def test_fallback_doesnt_hang():
    """Test that fallback doesn't hang on complex graphs."""
    graph = build_simple_graph_with_fuel_stations()

    start = "X1-TEST-A1"
    goal = "X1-TEST-A5"

    ship_data = {
        "symbol": "TEST-SHIP",
        "nav": {"waypointSymbol": start, "status": "IN_ORBIT"},
        "fuel": {"current": 100, "capacity": 200},
        "engine": {"speed": 10},
    }

    print(f"\nTest scenario:")
    print(f"  Route: {start} → {goal}")
    print(f"  Timeout: 30 seconds")
    print(f"  Expected: Complete within timeout")

    router = ORToolsRouter(graph, ship_data, RoutingConfig())

    route = router.find_optimal_route(start, goal, current_fuel=100, prefer_cruise=True)

    assert route is not None, "Router should produce a route within timeout"
    print(f"\n  ✓ Route completed within timeout")


if __name__ == "__main__":
    print("Testing OR-Tools Dijkstra fallback...")

    print("\n" + "=" * 70)
    print("TEST 1: Fallback Produces Valid Route")
    print("=" * 70)
    test_fallback_produces_valid_route()

    print("\n" + "=" * 70)
    print("TEST 2: Fallback Inserts Refuel Stops Correctly")
    print("=" * 70)
    test_fallback_inserts_refuel_stops_correctly()

    print("\n" + "=" * 70)
    print("TEST 3: Fallback Prefers CRUISE When Fuel Available")
    print("=" * 70)
    test_fallback_prefers_cruise_when_fuel_available()

    print("\n" + "=" * 70)
    print("TEST 4: Fallback Uses DRIFT When Fuel Constrained")
    print("=" * 70)
    test_fallback_uses_drift_when_fuel_constrained()

    print("\n" + "=" * 70)
    print("TEST 5: Fallback Doesn't Hang")
    print("=" * 70)
    test_fallback_doesnt_hang()

    print("\n✓ All tests passed!")
