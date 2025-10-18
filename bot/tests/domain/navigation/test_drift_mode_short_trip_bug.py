#!/usr/bin/env python3
"""
Test to reproduce DRIFT mode selection bug for short trips with adequate fuel

BUG DESCRIPTION:
STARHOPPER-8 mining drone is using DRIFT mode (350s travel time) instead of
CRUISE mode (~40s) for an 11.7-unit return trip from asteroid B14 to market B7,
even though it has 67/80 fuel (84%).

ROOT CAUSE HYPOTHESIS:
The OR-Tools min-cost flow solver is generating BOTH CRUISE and DRIFT arcs,
but for some reason DRIFT is being selected despite the 3,600-second penalty.

This suggests either:
1. CRUISE arcs aren't being generated at all (fuel constraint bug)
2. DRIFT arcs are being preferred due to tie-breaking logic
3. The penalty isn't being applied correctly in the min-cost flow graph
"""
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

from spacetraders_bot.core.routing import RouteOptimizer, TimeCalculator
import logging


def test_short_trip_should_use_cruise():
    """
    Test case: 11.7-unit trip with 67/80 fuel should ALWAYS use CRUISE

    Scenario:
    - Ship at B14 with 67/80 fuel (84% capacity)
    - Destination B7 is 11.7 units away
    - Fuel required for CRUISE: ~12 units
    - Fuel required for DRIFT: ~1 unit
    - Ship has MORE than enough fuel for CRUISE

    Expected behavior:
    - Route should use CRUISE mode (travel time ~40s)
    - NEVER use DRIFT (travel time ~350s)
    - The 3,600-second DRIFT penalty should make CRUISE always win
    """
    # Create test graph simulating X1-TX46 system
    waypoints = {
        "X1-TX46-B14": {
            "x": 0, "y": 0,
            "has_fuel": False,
            "traits": ["ASTEROID"],
            "orbitals": []
        },
        "X1-TX46-B7": {
            "x": 11.7, "y": 0,  # Exactly 11.7 units away
            "has_fuel": True,
            "traits": ["MARKETPLACE"],
            "orbitals": []
        },
    }

    # Create edges
    edges = [
        {
            "from": "X1-TX46-B14",
            "to": "X1-TX46-B7",
            "distance": 11.7,
            "type": "normal"
        },
        {
            "from": "X1-TX46-B7",
            "to": "X1-TX46-B14",
            "distance": 11.7,
            "type": "normal"
        }
    ]

    graph = {
        "system": "X1-TX46",
        "waypoints": waypoints,
        "edges": edges
    }

    # Create ship data matching STARHOPPER-8
    ship_data = {
        "symbol": "STARHOPPER-8",
        "nav": {
            "waypointSymbol": "X1-TX46-B14",
            "status": "IN_ORBIT"
        },
        "fuel": {
            "current": 67,
            "capacity": 80
        },
        "engine": {
            "speed": 9  # Mining drone speed
        }
    }

    # Plan route with prefer_cruise=True (default)
    logging.basicConfig(level=logging.DEBUG)

    optimizer = RouteOptimizer(graph, ship_data)
    route = optimizer.find_optimal_route(
        start="X1-TX46-B14",
        goal="X1-TX46-B7",
        current_fuel=67,
        prefer_cruise=True  # CRITICAL: Should enforce CRUISE
    )

    # Verify route exists
    assert route is not None, "Route should exist"

    # Verify route uses CRUISE mode
    nav_steps = [s for s in route['steps'] if s['action'] == 'navigate']
    drift_steps = [s for s in nav_steps if s['mode'] == 'DRIFT']
    cruise_steps = [s for s in nav_steps if s['mode'] == 'CRUISE']

    print("\n" + "="*80)
    print(f"TEST: Short trip (11.7u) with adequate fuel (67/80) should use CRUISE")
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
                  f"({step['mode']}, {step['distance']:.1f}u, {step['fuel_cost']}⛽, "
                  f"{TimeCalculator.format_time(step['time'])})")
        elif step['action'] == 'refuel':
            print(f"  {i}. Refuel at {step['waypoint']} (+{step['fuel_added']}⛽)")

    print("\nAssertion checks:")

    # CRITICAL: Should be ZERO DRIFT legs
    print(f"  ✓ DRIFT legs: {len(drift_steps)} (MUST be 0)")
    if len(drift_steps) > 0:
        print(f"\n❌ BUG DETECTED: Using DRIFT mode for short trip with adequate fuel!")
        print(f"   Ship has {ship_data['fuel']['current']}/{ship_data['fuel']['capacity']} fuel")
        print(f"   Trip is only {11.7} units")
        print(f"   CRUISE would need ~12 fuel (easily available)")
        print(f"   DRIFT time: ~350s vs CRUISE time: ~40s")
        print(f"   This is a 8.75x slowdown for no reason!")

    assert len(drift_steps) == 0, \
        f"Route MUST NOT use DRIFT for short trip with adequate fuel (found {len(drift_steps)} DRIFT legs)"

    # All navigation should use CRUISE
    print(f"  ✓ All navigation uses CRUISE: {len(cruise_steps) == len(nav_steps)}")
    assert len(cruise_steps) == len(nav_steps), \
        "All navigation legs must use CRUISE mode"

    # Route should be direct (1 leg for 11.7 units)
    print(f"  ✓ Navigation legs: {len(nav_steps)} (should be 1 for direct route)")
    assert len(nav_steps) == 1, \
        f"Route should be direct (1 leg) for short trip (found {len(nav_steps)} legs)"

    # Time should be reasonable (~40 seconds for CRUISE)
    # Allow up to 60 seconds for routing overhead
    max_acceptable_time = 60  # 1 minute
    print(f"  ✓ Total time: {route['total_time']}s (should be <{max_acceptable_time}s)")
    assert route['total_time'] < max_acceptable_time, \
        f"Route time too long: {route['total_time']}s (expected <{max_acceptable_time}s for CRUISE)"

    # Fuel consumption of navigation leg should match CRUISE (not DRIFT)
    # CRUISE: 11.7 units × 1.0 = ~12 fuel
    # DRIFT: 11.7 units × 0.003 = ~1 fuel
    # NOTE: Route may include refuel, so check navigation leg directly
    nav_fuel_cost = sum(s['fuel_cost'] for s in nav_steps)
    print(f"  ✓ Navigation fuel cost: {nav_fuel_cost} (should be ~12 for CRUISE)")
    assert nav_fuel_cost >= 10, \
        f"Fuel consumption too low ({nav_fuel_cost}) - suggests DRIFT mode was used"

    print("\n" + "="*80)
    print("✅ TEST PASSED: CRUISE mode correctly selected for short trip")
    print("="*80)


if __name__ == "__main__":
    try:
        test_short_trip_should_use_cruise()

        print("\n" + "="*80)
        print("🎉 ALL TESTS PASSED")
        print("="*80)
        print("\nSummary:")
        print("  ✓ CRUISE is always preferred when fuel is adequate")
        print("  ✓ DRIFT penalty (3,600s) is properly applied")
        print("  ✓ Short trips use CRUISE for fast travel")
        print("="*80 + "\n")

    except AssertionError as e:
        print(f"\n❌ TEST FAILED: {e}\n")
        sys.exit(1)
    except Exception as e:
        print(f"\n❌ UNEXPECTED ERROR: {e}\n")
        import traceback
        traceback.print_exc()
        sys.exit(1)
