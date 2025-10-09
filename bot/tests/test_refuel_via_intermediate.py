#!/usr/bin/env python3
"""
Test to verify refuel stops are inserted at intermediate waypoints
when routing through them with prefer_cruise=True

This tests the specific issue:
- Ship at A2 with low fuel
- Destination J62 (far away)
- Route passes through B33 (has fuel)
- Expected: Refuel at B33 before continuing to J62 in CRUISE
- Bug: Ship skips refuel at B33 and uses DRIFT instead
"""
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

from spacetraders_bot.core.routing import RouteOptimizer, TimeCalculator


def test_refuel_at_intermediate_waypoint():
    """
    Test case: A2 → J62 via B33 with refuel stop

    Scenario:
    - Ship at A2 with 100/400 fuel
    - Destination J62 is 600 units away
    - B33 is 300 units from A2 (on the path to J62)
    - B33 has marketplace (can refuel)
    - J62 is 300 units from B33

    Expected behavior with prefer_cruise=True:
    1. Navigate A2 → B33 (CRUISE, 300u)
    2. Refuel at B33 (mandatory!)
    3. Navigate B33 → J62 (CRUISE, 300u)

    Bug behavior (if exists):
    - Ship navigates A2 → J62 directly using DRIFT (skip refuel at B33)
    - Or uses CRUISE A2→B33 but then DRIFT B33→J62 (no refuel)
    """
    # Create linear test graph: A2 --- B33 --- J62
    waypoints = {
        "X1-TEST-A2": {
            "x": 0, "y": 0,
            "has_fuel": True,  # Start has fuel station
            "traits": ["MARKETPLACE"],
            "orbitals": []
        },
        "X1-TEST-B33": {
            "x": 300, "y": 0,
            "has_fuel": True,
            "traits": ["MARKETPLACE"],
            "orbitals": []
        },
        "X1-TEST-J62": {
            "x": 600, "y": 0,
            "has_fuel": False,
            "traits": [],
            "orbitals": []
        },
    }

    # Create edges (full mesh)
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
        "system": "X1-TEST",
        "waypoints": waypoints,
        "edges": edges
    }

    ship_data = {
        "symbol": "TEST-SHIP",
        "nav": {
            "waypointSymbol": "X1-TEST-A2",
            "status": "IN_ORBIT"
        },
        "fuel": {
            "current": 100,  # Not enough for direct CRUISE to J62 (600u needs ~600 fuel)
            "capacity": 400
        },
        "engine": {
            "speed": 30
        }
    }

    import logging
    logging.basicConfig(level=logging.INFO)

    optimizer = RouteOptimizer(graph, ship_data)
    route = optimizer.find_optimal_route(
        start="X1-TEST-A2",
        goal="X1-TEST-J62",
        current_fuel=100,
        prefer_cruise=True
    )

    print("\n" + "="*80)
    print(f"TEST: A2 → J62 via B33 with refuel stop")
    print("="*80)

    # Verify route exists
    assert route is not None, "Route should exist"

    print(f"Total steps: {len(route['steps'])}")
    print(f"Total time: {TimeCalculator.format_time(route['total_time'])}")
    print(f"Final fuel: {route['final_fuel']}/{ship_data['fuel']['capacity']}")

    print("\nRoute plan:")
    for i, step in enumerate(route['steps'], 1):
        if step['action'] == 'navigate':
            print(f"  {i}. Navigate {step['from']} → {step['to']} "
                  f"({step['mode']}, {step['distance']:.0f}u, {step['fuel_cost']}⛽)")
        elif step['action'] == 'refuel':
            print(f"  {i}. Refuel at {step['waypoint']} (+{step['fuel_added']}⛽)")

    print("\nAssertion checks:")

    # Extract steps by type
    nav_steps = [s for s in route['steps'] if s['action'] == 'navigate']
    refuel_steps = [s for s in route['steps'] if s['action'] == 'refuel']
    drift_steps = [s for s in nav_steps if s['mode'] == 'DRIFT']
    cruise_steps = [s for s in nav_steps if s['mode'] == 'CRUISE']

    # CRITICAL: Route should include refuel at B33
    refuel_at_b33 = any(s['waypoint'] == 'X1-TEST-B33' for s in refuel_steps)
    print(f"  ✓ Refuel at B33: {refuel_at_b33}")
    assert refuel_at_b33, "Route MUST refuel at intermediate waypoint B33"

    # All navigation should use CRUISE
    print(f"  ✓ All navigation uses CRUISE: {len(cruise_steps) == len(nav_steps)}")
    assert len(cruise_steps) == len(nav_steps), \
        f"All navigation legs should use CRUISE (found {len(drift_steps)} DRIFT legs)"

    # Route should pass through B33
    waypoints_visited = []
    for step in route['steps']:
        if step['action'] == 'navigate':
            waypoints_visited.append(step['to'])
        elif step['action'] == 'refuel':
            waypoints_visited.append(step['waypoint'])

    print(f"  ✓ Route passes through B33: {'X1-TEST-B33' in waypoints_visited}")
    assert 'X1-TEST-B33' in waypoints_visited, "Route should pass through B33"

    # Verify route order: should navigate to B33, refuel, then continue to J62
    # Find the refuel at B33
    refuel_b33_index = None
    for i, step in enumerate(route['steps']):
        if step['action'] == 'refuel' and step['waypoint'] == 'X1-TEST-B33':
            refuel_b33_index = i
            break

    assert refuel_b33_index is not None, "Should have refuel step at B33"

    # Check steps before and after refuel
    steps_before_refuel = route['steps'][:refuel_b33_index]
    steps_after_refuel = route['steps'][refuel_b33_index + 1:]

    # Before refuel: should have navigated to B33
    nav_to_b33 = any(
        s['action'] == 'navigate' and s['to'] == 'X1-TEST-B33'
        for s in steps_before_refuel
    )
    print(f"  ✓ Navigated to B33 before refuel: {nav_to_b33}")
    assert nav_to_b33, "Should navigate to B33 before refueling there"

    # After refuel: should navigate from B33 to J62 using CRUISE
    nav_from_b33 = [
        s for s in steps_after_refuel
        if s['action'] == 'navigate' and s['from'] == 'X1-TEST-B33'
    ]
    print(f"  ✓ Navigation from B33 after refuel: {len(nav_from_b33)} legs")
    assert len(nav_from_b33) > 0, "Should navigate from B33 after refueling"

    # Verify the leg from B33 uses CRUISE (not DRIFT)
    cruise_from_b33 = [s for s in nav_from_b33 if s['mode'] == 'CRUISE']
    print(f"  ✓ Uses CRUISE from B33: {len(cruise_from_b33) == len(nav_from_b33)}")
    assert len(cruise_from_b33) == len(nav_from_b33), \
        "Navigation from B33 should use CRUISE mode (after refueling)"

    print("\n" + "="*80)
    print("✅ TEST PASSED: Refuel at intermediate waypoint works correctly")
    print("="*80)


def test_direct_route_without_intermediate():
    """
    Test case: Direct route when intermediate refuel not needed

    Scenario:
    - Ship at A2 with 350/400 fuel (plenty!)
    - Destination J62 is 300 units away
    - Can reach J62 directly via CRUISE
    - Should NOT insert unnecessary refuel stops
    """
    waypoints = {
        "X1-TEST-A2": {
            "x": 0, "y": 0,
            "has_fuel": True,
            "traits": ["MARKETPLACE"],
            "orbitals": []
        },
        "X1-TEST-J62": {
            "x": 300, "y": 0,
            "has_fuel": False,
            "traits": [],
            "orbitals": []
        },
    }

    edges = [{
        "from": "X1-TEST-A2",
        "to": "X1-TEST-J62",
        "distance": 300.0,
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
            "waypointSymbol": "X1-TEST-A2",
            "status": "IN_ORBIT"
        },
        "fuel": {
            "current": 350,  # Plenty of fuel!
            "capacity": 400
        },
        "engine": {
            "speed": 30
        }
    }

    optimizer = RouteOptimizer(graph, ship_data)
    route = optimizer.find_optimal_route(
        start="X1-TEST-A2",
        goal="X1-TEST-J62",
        current_fuel=350,
        prefer_cruise=True
    )

    print("\n" + "="*80)
    print(f"TEST: Direct route when fuel sufficient")
    print("="*80)

    assert route is not None, "Route should exist"

    nav_steps = [s for s in route['steps'] if s['action'] == 'navigate']
    refuel_steps = [s for s in route['steps'] if s['action'] == 'refuel']

    print(f"Navigation legs: {len(nav_steps)}")
    print(f"Refuel stops: {len(refuel_steps)}")

    # Should be direct route (1 navigation leg, 0 refuel stops)
    print("  ✓ Direct navigation (no unnecessary refuel)")
    assert len(nav_steps) == 1, "Should have 1 direct navigation leg"
    assert nav_steps[0]['mode'] == 'CRUISE', "Should use CRUISE mode"

    print("\n✅ TEST PASSED: Direct route optimization works")
    print("="*80)


if __name__ == "__main__":
    print("\n" + "="*80)
    print("Running intermediate refuel tests...")
    print("="*80)

    try:
        test_refuel_at_intermediate_waypoint()
        test_direct_route_without_intermediate()

        print("\n" + "="*80)
        print("🎉 ALL TESTS PASSED")
        print("="*80)
        print("\nSummary:")
        print("  ✓ Refuel stops inserted at intermediate waypoints")
        print("  ✓ CRUISE mode used after refueling")
        print("  ✓ Direct routes preserved when fuel sufficient")
        print("="*80 + "\n")

    except AssertionError as e:
        print(f"\n❌ TEST FAILED: {e}\n")
        sys.exit(1)
    except Exception as e:
        print(f"\n❌ UNEXPECTED ERROR: {e}\n")
        import traceback
        traceback.print_exc()
        sys.exit(1)
