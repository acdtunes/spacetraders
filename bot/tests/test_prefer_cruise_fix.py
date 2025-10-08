#!/usr/bin/env python3
"""
Test to verify prefer_cruise=True optimization fix
Ensures DRIFT is avoided when CRUISE is viable via refueling
"""
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

from spacetraders_bot.core.routing import GraphBuilder, RouteOptimizer, FuelCalculator, TimeCalculator


def test_prefer_cruise_avoids_drift():
    """
    Test case: B7 → J55 route with 96 fuel

    Scenario:
    - Ship at B7 with 96/400 fuel
    - Destination J55 is 671 units away
    - B7 has marketplace (can refuel)
    - Several intermediate fuel stations available

    Expected behavior with prefer_cruise=True:
    - Refuel at B7 at start (if needed)
    - Use CRUISE mode for all legs
    - Insert refuel stops as needed
    - NEVER use DRIFT unless absolutely necessary
    - Minimize number of legs (2-4 hops max for 671u)

    Old behavior (buggy):
    - 6 hops including unnecessary DRIFT segments
    - Used DRIFT for short 24u leg (B35 → B29)
    - Total time ~9m 43s one-way

    New behavior (fixed):
    - 2-3 hops, all CRUISE
    - Refuel at start or strategically mid-route
    - Total time should be ~6-7 minutes one-way
    """
    # Create test graph simulating X1-JD30 system
    # Simplified version with key waypoints
    # Using realistic layout based on actual game data
    waypoints = {
        "X1-JD30-B7": {
            "x": 0, "y": 0,
            "has_fuel": True,
            "traits": ["MARKETPLACE"],
            "orbitals": []
        },
        "X1-JD30-I52": {
            "x": 350, "y": 0,
            "has_fuel": True,
            "traits": ["MARKETPLACE"],
            "orbitals": []
        },
        "X1-JD30-J55": {
            "x": 671, "y": 0,
            "has_fuel": True,
            "traits": ["MARKETPLACE"],
            "orbitals": []
        },
    }

    # Create edges (full mesh for simplicity)
    edges = []
    wp_list = list(waypoints.keys())
    for i, wp1 in enumerate(wp_list):
        for wp2 in wp_list[i+1:]:
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

    graph = {
        "system": "X1-JD30",
        "waypoints": waypoints,
        "edges": edges
    }

    # Create ship data
    ship_data = {
        "symbol": "TEST-SHIP",
        "nav": {
            "waypointSymbol": "X1-JD30-B7",
            "status": "IN_ORBIT"
        },
        "fuel": {
            "current": 96,
            "capacity": 400
        },
        "engine": {
            "speed": 30  # Standard speed
        }
    }

    # Plan route with prefer_cruise=True
    import logging
    logging.basicConfig(level=logging.DEBUG)

    optimizer = RouteOptimizer(graph, ship_data)
    route = optimizer.find_optimal_route(
        start="X1-JD30-B7",
        goal="X1-JD30-J55",
        current_fuel=96,
        prefer_cruise=True
    )

    # Verify route exists
    assert route is not None, "Route should exist"

    # Verify route uses CRUISE mode for navigation steps
    nav_steps = [s for s in route['steps'] if s['action'] == 'navigate']
    drift_steps = [s for s in nav_steps if s['mode'] == 'DRIFT']
    cruise_steps = [s for s in nav_steps if s['mode'] == 'CRUISE']

    print("\n" + "="*80)
    print(f"TEST: prefer_cruise=True B7 → J55 route optimization")
    print("="*80)
    print(f"Total steps: {len(route['steps'])}")
    print(f"Navigation legs: {len(nav_steps)}")
    print(f"CRUISE legs: {len(cruise_steps)}")
    print(f"DRIFT legs: {len(drift_steps)}")
    print(f"Total time: {TimeCalculator.format_time(route['total_time'])}")
    print(f"Final fuel: {route['final_fuel']}/{ship_data['fuel']['capacity']}")

    print("\nRoute plan:")
    for i, step in enumerate(route['steps'], 1):
        if step['action'] == 'navigate':
            print(f"  {i}. Navigate {step['from']} → {step['to']} "
                  f"({step['mode']}, {step['distance']:.0f}u, {step['fuel_cost']}⛽, "
                  f"{TimeCalculator.format_time(step['time'])})")
        elif step['action'] == 'refuel':
            print(f"  {i}. Refuel at {step['waypoint']} (+{step['fuel_added']}⛽)")

    print("\nAssertion checks:")

    # CRITICAL: No DRIFT legs should exist when prefer_cruise=True
    # Exception: Only if in true emergency (no fuel to reach any station via CRUISE)
    print(f"  ✓ DRIFT legs: {len(drift_steps)} (should be 0 or emergency only)")
    assert len(drift_steps) == 0, \
        f"Route should NOT use DRIFT when prefer_cruise=True (found {len(drift_steps)} DRIFT legs)"

    # Route should be reasonably direct (2-4 legs for 671 units)
    print(f"  ✓ Navigation legs: {len(nav_steps)} (should be ≤4 for direct routing)")
    assert len(nav_steps) <= 4, \
        f"Route should have ≤4 navigation legs (found {len(nav_steps)})"

    # All navigation should use CRUISE
    print(f"  ✓ All navigation uses CRUISE: {len(cruise_steps) == len(nav_steps)}")
    assert len(cruise_steps) == len(nav_steps), \
        "All navigation legs should use CRUISE mode"

    # Route should include refuel actions
    refuel_steps = [s for s in route['steps'] if s['action'] == 'refuel']
    print(f"  ✓ Refuel stops: {len(refuel_steps)} (proactive refueling strategy)")
    assert len(refuel_steps) >= 1, \
        "Route should include at least 1 refuel stop for long journey"

    # Time should be reasonable (not excessively long)
    # For 671 units at speed 30, CRUISE should take ~7-8 minutes + refuel time
    # Allow up to 15 minutes for refuel stops and route finding
    max_acceptable_time = 900  # 15 minutes
    print(f"  ✓ Total time: {route['total_time']}s (should be <{max_acceptable_time}s)")
    assert route['total_time'] < max_acceptable_time, \
        f"Route time too long: {route['total_time']}s (expected <{max_acceptable_time}s)"

    print("\n" + "="*80)
    print("✅ TEST PASSED: prefer_cruise optimization working correctly")
    print("="*80)


def test_prefer_cruise_emergency_drift():
    """
    Test case: Emergency DRIFT when no fuel for CRUISE

    Scenario:
    - Ship at A1 with only 5 fuel (very low!)
    - Destination B1 is 100 units away
    - B1 has marketplace (can refuel)
    - Not enough fuel for CRUISE (needs ~100 fuel)
    - Just enough fuel for DRIFT (~1 fuel needed)

    Expected behavior with prefer_cruise=True:
    - Allow DRIFT to B1 (emergency fuel station reach)
    - This is the ONLY acceptable use of DRIFT
    """
    waypoints = {
        "X1-TEST-A1": {
            "x": 0, "y": 0,
            "has_fuel": False,
            "traits": [],
            "orbitals": []
        },
        "X1-TEST-B1": {
            "x": 100, "y": 0,
            "has_fuel": True,
            "traits": ["MARKETPLACE"],
            "orbitals": []
        },
    }

    edges = [{
        "from": "X1-TEST-A1",
        "to": "X1-TEST-B1",
        "distance": 100.0,
        "type": "normal"
    }]

    graph = {
        "system": "X1-TEST",
        "waypoints": waypoints,
        "edges": edges
    }

    ship_data = {
        "symbol": "TEST-SHIP",
        "nav": {
            "waypointSymbol": "X1-TEST-A1",
            "status": "IN_ORBIT"
        },
        "fuel": {
            "current": 5,  # Very low fuel!
            "capacity": 400
        },
        "engine": {
            "speed": 30
        }
    }

    optimizer = RouteOptimizer(graph, ship_data)
    route = optimizer.find_optimal_route(
        start="X1-TEST-A1",
        goal="X1-TEST-B1",
        current_fuel=5,
        prefer_cruise=True
    )

    print("\n" + "="*80)
    print(f"TEST: Emergency DRIFT to fuel station")
    print("="*80)

    # Route should exist
    assert route is not None, "Emergency route should exist"

    # Route should use DRIFT (emergency)
    nav_steps = [s for s in route['steps'] if s['action'] == 'navigate']
    drift_steps = [s for s in nav_steps if s['mode'] == 'DRIFT']

    print(f"Navigation legs: {len(nav_steps)}")
    print(f"DRIFT legs: {len(drift_steps)}")

    # In this emergency, DRIFT is acceptable
    print("  ✓ Emergency DRIFT to fuel station is acceptable")
    assert len(drift_steps) >= 1, "Should use DRIFT in emergency to reach fuel station"

    print("\n✅ TEST PASSED: Emergency DRIFT handling works correctly")
    print("="*80)


if __name__ == "__main__":
    print("\n" + "="*80)
    print("Running prefer_cruise optimization tests...")
    print("="*80)

    try:
        test_prefer_cruise_avoids_drift()
        test_prefer_cruise_emergency_drift()

        print("\n" + "="*80)
        print("🎉 ALL TESTS PASSED")
        print("="*80)
        print("\nSummary:")
        print("  ✓ DRIFT is avoided when CRUISE is viable via refueling")
        print("  ✓ Route uses minimal legs (direct routing)")
        print("  ✓ Proactive refueling strategy enabled")
        print("  ✓ Emergency DRIFT allowed when necessary")
        print("="*80 + "\n")

    except AssertionError as e:
        print(f"\n❌ TEST FAILED: {e}\n")
        sys.exit(1)
    except Exception as e:
        print(f"\n❌ UNEXPECTED ERROR: {e}\n")
        import traceback
        traceback.print_exc()
        sys.exit(1)
