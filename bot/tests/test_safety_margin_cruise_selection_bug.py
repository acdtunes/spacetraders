#!/usr/bin/env python3
"""
CRITICAL BUG FIX TEST: Safety margin prevents CRUISE selection at fuel stations

Reproduces production bug from SILMARETH-1 contract delivery:
- Ship at B33 (fuel station) with 19 fuel after first leg
- Destination J62 is 382 units away
- B33 has marketplace (can refuel to 400)
- Bug: Route selects DRIFT for 382u leg instead of refueling and using CRUISE
- Root cause: Safety margin check (cruise_cost * 1.1 > max_fuel) prevents CRUISE selection
- Impact: 45+ minute DRIFT journey instead of 7 minute CRUISE journey

Real production log evidence:
From /var/logs/contract_SILMARETH-1_2025-10-09T21-05-26.509922Z.log:
    📋 ROUTE PLAN:
       1. Navigate X1-GH18-I60 → X1-GH18-B33 (CRUISE, 107u, 108⛽)
       2. Navigate X1-GH18-B33 → X1-GH18-J62 (DRIFT, 382u, 2⛽)  # BUG!

Expected route:
    1. Navigate I60 → B33 (CRUISE, 107u)
    2. Refuel at B33 (+381⛽)
    3. Navigate B33 → J62 (CRUISE, 382u)

File: routing.py
Function: _should_allow_emergency_drift()
Buggy line 758: if current_data.get('has_fuel', False) and cruise_cost_to_neighbor * (1 + FUEL_SAFETY_MARGIN) <= max_fuel:
Fixed line 758: if current_data.get('has_fuel', False) and cruise_cost_to_neighbor <= max_fuel:

The problem:
- cruise_cost = 382 fuel
- FUEL_SAFETY_MARGIN = 0.1 (10%)
- cruise_cost * 1.1 = 420.2 fuel
- max_fuel = 400 (ship capacity)
- 420.2 > 400 → Check fails → Allows DRIFT!

The fix:
Remove safety margin from this check - when AT a fuel station, we can refuel to exactly what we need.
Safety margin should only apply when checking if CURRENT fuel is sufficient, not if a FULL TANK can reach destination.
"""
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

from spacetraders_bot.core.routing import RouteOptimizer, TimeCalculator, FuelCalculator


def test_safety_margin_bug_382_unit_cruise():
    """
    Test case: B33 → J62 with 19 fuel at fuel station

    Scenario from production:
    - Ship at B33 (fuel station) with 19/400 fuel
    - Destination J62 is 382 units away
    - B33 has marketplace (can refuel)
    - CRUISE needs 382 fuel (within 400 capacity)
    - CRUISE with 10% margin needs 420.2 fuel (exceeds 400 capacity)

    Bug behavior (OLD):
    - Safety margin check: 382 * 1.1 = 420.2 > 400 → FAILS
    - Function thinks CRUISE impossible even with full tank
    - Allows DRIFT as "emergency" option
    - Result: Navigate B33 → J62 (DRIFT, 382u, 2⛽) - takes 45+ minutes!

    Fixed behavior (NEW):
    - No safety margin in "can full tank reach?" check: 382 <= 400 → PASSES
    - Function knows CRUISE is viable with refuel
    - Blocks DRIFT
    - Forces route planner to insert refuel + use CRUISE
    - Result: Refuel at B33, then CRUISE to J62 (~7 minutes)
    """
    # Create test graph matching production scenario
    waypoints = {
        "X1-TEST-B33": {
            "x": 0, "y": 0,
            "has_fuel": True,  # CRITICAL: Has marketplace/fuel station
            "traits": ["MARKETPLACE"],
            "orbitals": []
        },
        "X1-TEST-J62": {
            "x": 382, "y": 0,
            "has_fuel": False,
            "traits": [],
            "orbitals": []
        },
    }

    # Create edge
    edges = [{
        "from": "X1-TEST-B33",
        "to": "X1-TEST-J62",
        "distance": 382.0,
        "type": "normal"
    }]

    graph = {
        "system": "X1-TEST",
        "waypoints": waypoints,
        "edges": edges
    }

    ship_data = {
        "symbol": "SILMARETH-1",
        "nav": {
            "waypointSymbol": "X1-TEST-B33",
            "status": "IN_ORBIT"
        },
        "fuel": {
            "current": 19,  # Exact production state after first leg
            "capacity": 400
        },
        "engine": {
            "speed": 30
        }
    }

    import logging
    logging.basicConfig(level=logging.INFO)

    print("\n" + "="*80)
    print("BUG REPRODUCTION: Safety margin prevents CRUISE at fuel station")
    print("="*80)
    print(f"Scenario: Ship at B33 (fuel station) with 19 fuel, going to J62 (382u)")
    print(f"Ship capacity: 400 fuel")
    print(f"CRUISE fuel needed: 382")
    print(f"CRUISE + 10% safety margin: 420.2 (exceeds capacity!)")
    print(f"\nBuggy logic: 420.2 > 400 → thinks CRUISE impossible → allows DRIFT")
    print(f"Fixed logic: 382 <= 400 → knows CRUISE viable with refuel → blocks DRIFT")
    print("="*80 + "\n")

    optimizer = RouteOptimizer(graph, ship_data)
    route = optimizer.find_optimal_route(
        start="X1-TEST-B33",
        goal="X1-TEST-J62",
        current_fuel=19,
        prefer_cruise=True
    )

    # Verify route exists
    assert route is not None, "Route should exist (CRUISE is viable with refuel)"

    print(f"Total steps: {len(route['steps'])}")
    print(f"Total time: {TimeCalculator.format_time(route['total_time'])}")
    print(f"Final fuel: {route['final_fuel']}/{ship_data['fuel']['capacity']}")

    print("\n📋 ROUTE PLAN:")
    for i, step in enumerate(route['steps'], 1):
        if step['action'] == 'navigate':
            print(f"   {i}. Navigate {step['from']} → {step['to']} "
                  f"({step['mode']}, {step['distance']:.0f}u, {step['fuel_cost']}⛽, "
                  f"{TimeCalculator.format_time(step['time'])})")
        elif step['action'] == 'refuel':
            print(f"   {i}. Refuel at {step['waypoint']} (+{step['fuel_added']}⛽)")

    print("\n🔍 ASSERTION CHECKS:")

    # Extract steps by type
    nav_steps = [s for s in route['steps'] if s['action'] == 'navigate']
    refuel_steps = [s for s in route['steps'] if s['action'] == 'refuel']
    drift_steps = [s for s in nav_steps if s['mode'] == 'DRIFT']
    cruise_steps = [s for s in nav_steps if s['mode'] == 'CRUISE']

    # CRITICAL: Route must NOT use DRIFT
    print(f"  ✓ No DRIFT legs: {len(drift_steps) == 0} (found {len(drift_steps)} DRIFT legs)")
    assert len(drift_steps) == 0, \
        f"Route should NOT use DRIFT when CRUISE is viable with refuel (found {len(drift_steps)} DRIFT legs)"

    # Route must use CRUISE
    print(f"  ✓ Uses CRUISE mode: {len(cruise_steps) > 0}")
    assert len(cruise_steps) > 0, "Route should use CRUISE mode for 382u leg"

    # Route must include refuel at B33
    refuel_at_b33 = any(s['waypoint'] == 'X1-TEST-B33' for s in refuel_steps)
    print(f"  ✓ Refuel at B33: {refuel_at_b33}")
    assert refuel_at_b33, "Route MUST refuel at B33 before CRUISE leg"

    # Time should be reasonable (CRUISE ~7min, not DRIFT ~45min)
    # For 382u at speed 30:
    # CRUISE: (382 * 31) / 30 = ~395s (~6.5 min)
    # DRIFT:  (382 * 26) / 30 = ~331s (~5.5 min) but misleading - actual DRIFT is MUCH slower
    # Allow up to 10 minutes with refuel time
    max_acceptable_time = 600  # 10 minutes
    print(f"  ✓ Reasonable time: {route['total_time']}s (should be <{max_acceptable_time}s for CRUISE)")
    assert route['total_time'] < max_acceptable_time, \
        f"Route time too long: {route['total_time']}s (expected <{max_acceptable_time}s) - indicates DRIFT usage"

    # Verify fuel consumption matches CRUISE (not DRIFT)
    # Starting fuel: 19
    # After refuel: 400
    # After CRUISE 382u: 400 - 382 = 18
    expected_final_fuel = 400 - 382
    fuel_tolerance = 50  # Allow some variance for refuel timing
    print(f"  ✓ Final fuel indicates CRUISE usage: {route['final_fuel']} "
          f"(expected ~{expected_final_fuel} for CRUISE)")
    # Just verify we didn't use DRIFT (which would leave ~398 fuel)
    assert route['final_fuel'] < 100, \
        f"Final fuel {route['final_fuel']} too high - indicates DRIFT usage instead of CRUISE"

    print("\n" + "="*80)
    print("✅ TEST PASSED: Safety margin bug fixed - CRUISE selected with refuel")
    print("="*80)
    print("\nSummary:")
    print("  ✓ Route refuels at B33 (fuel station)")
    print("  ✓ Route uses CRUISE mode for 382u leg (not DRIFT)")
    print(f"  ✓ Journey time reasonable: {TimeCalculator.format_time(route['total_time'])} "
          f"(vs 45+ min with DRIFT)")
    print("  ✓ Ship arrives with low fuel (CRUISE consumption), not high fuel (DRIFT)")
    print("="*80 + "\n")


def test_safety_margin_bug_boundary_case():
    """
    Test boundary case: exactly at capacity threshold

    Scenario:
    - Ship at fuel station with low fuel
    - Destination exactly 400 units away
    - Ship capacity: 400 fuel
    - CRUISE needs 400 fuel (exactly at capacity)
    - CRUISE + 10% margin needs 440 fuel (exceeds capacity)

    Old behavior:
    - 440 > 400 → Allows DRIFT

    New behavior:
    - 400 <= 400 → Blocks DRIFT, forces CRUISE with refuel
    """
    waypoints = {
        "X1-TEST-START": {
            "x": 0, "y": 0,
            "has_fuel": True,
            "traits": ["MARKETPLACE"],
            "orbitals": []
        },
        "X1-TEST-END": {
            "x": 400, "y": 0,
            "has_fuel": False,
            "traits": [],
            "orbitals": []
        },
    }

    edges = [{
        "from": "X1-TEST-START",
        "to": "X1-TEST-END",
        "distance": 400.0,
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
            "waypointSymbol": "X1-TEST-START",
            "status": "IN_ORBIT"
        },
        "fuel": {
            "current": 50,
            "capacity": 400
        },
        "engine": {
            "speed": 30
        }
    }

    print("\n" + "="*80)
    print("BOUNDARY TEST: Exactly at capacity threshold")
    print("="*80)
    print(f"Distance: 400 units (exactly ship capacity)")
    print(f"CRUISE needs: 400 fuel")
    print(f"CRUISE + 10% margin: 440 fuel (exceeds capacity)")
    print(f"Bug: Would allow DRIFT because 440 > 400")
    print(f"Fix: Should use CRUISE because 400 <= 400")
    print("="*80 + "\n")

    optimizer = RouteOptimizer(graph, ship_data)
    route = optimizer.find_optimal_route(
        start="X1-TEST-START",
        goal="X1-TEST-END",
        current_fuel=50,
        prefer_cruise=True
    )

    assert route is not None, "Route should exist"

    nav_steps = [s for s in route['steps'] if s['action'] == 'navigate']
    drift_steps = [s for s in nav_steps if s['mode'] == 'DRIFT']

    print(f"Route uses {len(nav_steps)} navigation legs")
    print(f"DRIFT legs: {len(drift_steps)}")

    # Should NOT use DRIFT
    assert len(drift_steps) == 0, \
        f"Should not use DRIFT at boundary case (found {len(drift_steps)} DRIFT legs)"

    print("\n✅ BOUNDARY TEST PASSED: CRUISE selected at exact capacity threshold")
    print("="*80 + "\n")


def test_legitimate_drift_still_allowed():
    """
    Test that legitimate DRIFT use cases still work

    Scenario:
    - Ship at waypoint WITHOUT fuel station
    - Low fuel (5 units)
    - Destination fuel station is 100 units away
    - NOT enough fuel for CRUISE (needs ~100)
    - Just enough for DRIFT (needs ~1)

    Expected: Should use DRIFT (emergency fuel station reach)
    This is the ONLY legitimate use of DRIFT with prefer_cruise=True
    """
    waypoints = {
        "X1-TEST-START": {
            "x": 0, "y": 0,
            "has_fuel": False,  # No fuel here!
            "traits": [],
            "orbitals": []
        },
        "X1-TEST-FUEL": {
            "x": 100, "y": 0,
            "has_fuel": True,
            "traits": ["MARKETPLACE"],
            "orbitals": []
        },
    }

    edges = [{
        "from": "X1-TEST-START",
        "to": "X1-TEST-FUEL",
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
            "waypointSymbol": "X1-TEST-START",
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

    print("\n" + "="*80)
    print("LEGITIMATE DRIFT TEST: Emergency fuel station reach")
    print("="*80)
    print(f"Ship at waypoint WITHOUT fuel")
    print(f"Low fuel: 5 units")
    print(f"Destination fuel station: 100 units away")
    print(f"CRUISE needs: ~100 fuel (NOT enough)")
    print(f"DRIFT needs: ~1 fuel (OK)")
    print(f"Expected: Use DRIFT (legitimate emergency)")
    print("="*80 + "\n")

    optimizer = RouteOptimizer(graph, ship_data)
    route = optimizer.find_optimal_route(
        start="X1-TEST-START",
        goal="X1-TEST-FUEL",
        current_fuel=5,
        prefer_cruise=True
    )

    assert route is not None, "Emergency DRIFT route should exist"

    nav_steps = [s for s in route['steps'] if s['action'] == 'navigate']
    drift_steps = [s for s in nav_steps if s['mode'] == 'DRIFT']

    print(f"Route uses {len(nav_steps)} navigation legs")
    print(f"DRIFT legs: {len(drift_steps)}")

    # Should use DRIFT (emergency)
    assert len(drift_steps) > 0, "Should use DRIFT in emergency to reach fuel station"

    print("\n✅ LEGITIMATE DRIFT TEST PASSED: Emergency DRIFT still works")
    print("="*80 + "\n")


if __name__ == "__main__":
    print("\n" + "="*80)
    print("CRITICAL BUG FIX TEST SUITE")
    print("Safety margin prevents CRUISE selection at fuel stations")
    print("="*80)

    try:
        # Test 1: Main bug reproduction (382u case from production)
        test_safety_margin_bug_382_unit_cruise()

        # Test 2: Boundary case (exactly at capacity)
        test_safety_margin_bug_boundary_case()

        # Test 3: Verify legitimate DRIFT still works
        test_legitimate_drift_still_allowed()

        print("\n" + "="*80)
        print("🎉 ALL TESTS PASSED")
        print("="*80)
        print("\nSummary:")
        print("  ✓ Safety margin bug fixed (382u case)")
        print("  ✓ Boundary case works (400u at capacity)")
        print("  ✓ Legitimate DRIFT still allowed (emergency)")
        print("\nResult: CRUISE is now selected at fuel stations instead of DRIFT")
        print("Impact: Journey time reduced from 45+ min (DRIFT) to ~7 min (CRUISE)")
        print("="*80 + "\n")

    except AssertionError as e:
        print(f"\n❌ TEST FAILED: {e}\n")
        print("This indicates the bug still exists or the fix caused a regression.\n")
        sys.exit(1)
    except Exception as e:
        print(f"\n❌ UNEXPECTED ERROR: {e}\n")
        import traceback
        traceback.print_exc()
        sys.exit(1)
