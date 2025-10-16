#!/usr/bin/env python3
"""
Test case for intermediate refuel station bug

BUG REPORT:
- Issue: RouteOptimizer fails to find intermediate refuel stations when direct CRUISE is impossible
- Evidence: SILMARETH-1 route B32→C43 (428 units) used DRIFT instead of finding intermediate
            station for CRUISE route
- Ship Constraint: 400 fuel capacity, needs 471 fuel for direct CRUISE to C43
- Expected: Algorithm should find route like B32→[fuel station X]→C43 using CRUISE for both legs
- Actual: Algorithm chooses DRIFT mode (50 minute travel time) instead
"""

import sys
from pathlib import Path
sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'src'))

from spacetraders_bot.core.routing import RouteOptimizer, FuelCalculator, TimeCalculator


def regression_should_find_intermediate_refuel_station_instead_of_drift():
    """
    Test that RouteOptimizer finds intermediate refuel stations for CRUISE routes
    instead of falling back to DRIFT mode when direct CRUISE is impossible.

    Scenario:
    - Ship at B32 with 58 fuel (400 capacity)
    - Destination: C43 (428 units away)
    - Direct CRUISE requires: 428 fuel (ship only has 58, capacity 400 - IMPOSSIBLE)
    - Refuel at B32 gives 400 fuel (still not enough for direct CRUISE to C43: needs 471)
    - Intermediate station X at 200 units from B32, 228 units from C43

    Expected route (with prefer_cruise=True):
    1. Refuel at B32 (58 → 400 fuel)
    2. Navigate B32 → X (CRUISE, 200 units, ~200 fuel cost)
    3. Refuel at X (200 → 400 fuel)
    4. Navigate X → C43 (CRUISE, 228 units, ~228 fuel cost)

    Total time: ~15-20 minutes (two CRUISE legs + two refuel stops)

    Bug behavior (actual):
    - Returns DRIFT route (50 minutes) instead of finding intermediate station
    """

    # Create test graph
    # B32 is starting point (has fuel)
    # C43 is destination (428 units away, no fuel)
    # X is intermediate fuel station (200 units from B32, 228 units from C43)
    graph = {
        "system": "X1-GH18",
        "waypoints": {
            "X1-GH18-B32": {
                "type": "ASTEROID",
                "x": 0,
                "y": 0,
                "traits": ["MARKETPLACE"],
                "has_fuel": True,
                "orbitals": []
            },
            "X1-GH18-X": {
                "type": "PLANET",
                "x": 200,
                "y": 0,
                "traits": ["MARKETPLACE"],
                "has_fuel": True,
                "orbitals": []
            },
            "X1-GH18-C43": {
                "type": "ASTEROID",
                "x": 428,
                "y": 0,
                "traits": [],
                "has_fuel": False,
                "orbitals": []
            }
        },
        "edges": [
            {"from": "X1-GH18-B32", "to": "X1-GH18-X", "distance": 200, "type": "normal"},
            {"from": "X1-GH18-B32", "to": "X1-GH18-C43", "distance": 428, "type": "normal"},
            {"from": "X1-GH18-X", "to": "X1-GH18-C43", "distance": 228, "type": "normal"},
        ]
    }

    # Ship data matching SILMARETH-1
    ship_data = {
        "symbol": "SILMARETH-1",
        "nav": {
            "waypointSymbol": "X1-GH18-B32",
            "status": "IN_ORBIT"
        },
        "fuel": {
            "current": 58,
            "capacity": 400
        },
        "engine": {
            "speed": 9  # Standard mining drone speed
        }
    }

    # Create route optimizer
    optimizer = RouteOptimizer(graph, ship_data)

    # Plan route with prefer_cruise=True (should NEVER use DRIFT)
    route = optimizer.find_optimal_route(
        start="X1-GH18-B32",
        goal="X1-GH18-C43",
        current_fuel=58,
        prefer_cruise=True
    )

    # Verify route was found
    assert route is not None, "Route should be found"

    # Verify route uses ONLY CRUISE mode (no DRIFT)
    for step in route['steps']:
        if step['action'] == 'navigate':
            assert step['mode'] == 'CRUISE', \
                f"Route should use CRUISE only, but found {step['mode']} for {step['from']} → {step['to']}"

    # Verify route includes intermediate refuel station
    waypoints_visited = set()
    for step in route['steps']:
        if step['action'] == 'navigate':
            waypoints_visited.add(step['from'])
            waypoints_visited.add(step['to'])

    assert "X1-GH18-X" in waypoints_visited, \
        "Route should visit intermediate fuel station X1-GH18-X"

    # Verify route includes refuel actions
    refuel_actions = [s for s in route['steps'] if s['action'] == 'refuel']
    assert len(refuel_actions) >= 2, \
        f"Route should include at least 2 refuel actions, found {len(refuel_actions)}"

    # Verify total time is reasonable (should be much less than 50 minutes / 3000 seconds)
    # Two CRUISE legs (200u + 228u) at speed 9 = ~(200*31/9) + (228*31/9) = ~1480 seconds
    # Plus refuel time and penalties = ~1500-2000 seconds total
    assert route['total_time'] < 3000, \
        f"Route time should be < 3000s (CRUISE route), but got {route['total_time']}s"

    print(f"✓ Route found using CRUISE with intermediate refuel station")
    print(f"  Total time: {TimeCalculator.format_time(route['total_time'])}")
    print(f"  Final fuel: {route['final_fuel']}")
    print(f"  Steps: {len(route['steps'])}")
    print(f"  Refuel stops: {len(refuel_actions)}")


def regression_direct_drift_when_no_intermediate_stations_exist():
    """
    Test that DRIFT is correctly used when no intermediate fuel stations exist
    and direct CRUISE is impossible.

    This is the VALID case where DRIFT should be used.
    """

    # Graph with no intermediate stations
    graph = {
        "system": "X1-TEST",
        "waypoints": {
            "X1-TEST-A": {
                "type": "ASTEROID",
                "x": 0,
                "y": 0,
                "traits": ["MARKETPLACE"],
                "has_fuel": True,
                "orbitals": []
            },
            "X1-TEST-B": {
                "type": "ASTEROID",
                "x": 500,
                "y": 0,
                "traits": [],
                "has_fuel": False,
                "orbitals": []
            }
        },
        "edges": [
            {"from": "X1-TEST-A", "to": "X1-TEST-B", "distance": 500, "type": "normal"},
        ]
    }

    ship_data = {
        "symbol": "TEST-SHIP",
        "nav": {"waypointSymbol": "X1-TEST-A", "status": "IN_ORBIT"},
        "fuel": {"current": 50, "capacity": 400},
        "engine": {"speed": 9}
    }

    optimizer = RouteOptimizer(graph, ship_data)

    # With prefer_cruise=True, should still find a route (using DRIFT as emergency)
    route = optimizer.find_optimal_route(
        start="X1-TEST-A",
        goal="X1-TEST-B",
        current_fuel=50,
        prefer_cruise=True
    )

    assert route is not None, "Route should be found even if DRIFT is only option"

    # In this case, DRIFT is the ONLY option, so it's acceptable
    drift_found = False
    for step in route['steps']:
        if step['action'] == 'navigate' and step['mode'] == 'DRIFT':
            drift_found = True
            break

    assert drift_found, "Route should use DRIFT when it's the only option"
    print(f"✓ DRIFT correctly used when no intermediate stations available")


if __name__ == '__main__':
    print("Testing intermediate refuel station bug...")
    print()

    print("Test 1: Should find intermediate station instead of DRIFT")
    try:
        test_should_find_intermediate_refuel_station_instead_of_drift()
    except AssertionError as e:
        print(f"  ✗ FAILED: {e}")
        print()
        print("BUG CONFIRMED: RouteOptimizer is not finding intermediate refuel stations")

    print()
    print("Test 2: Should use DRIFT when no intermediate stations exist")
    try:
        test_direct_drift_when_no_intermediate_stations_exist()
    except AssertionError as e:
        print(f"  ✗ FAILED: {e}")
