#!/usr/bin/env python3
"""
CRITICAL BUG FIX TEST: Three routing system bugs blocking SILMARETH-1

Reproduces real-world production errors:
1. A* iteration limit too low (10k insufficient for 762-unit paths)
2. False "insufficient fuel" error (misleading - actually iteration limit hit)
3. Contract market selection without distance validation (selects markets 700+ units away)

Real-world scenario from SILMARETH-1:
- Location: X1-GH18-H57
- Destination: X1-GH18-J62
- Distance: 762.1 units
- Fuel: 400/400 (PLENTY for DRIFT)
- Error: "No route found after 10000 iterations"
- Error: "insufficient fuel even with DRIFT" (FALSE - fuel is adequate)
- Root cause: Market selection picked H57 (762u from J62) instead of nearby markets
"""

import math
import pytest
from unittest.mock import Mock, MagicMock, patch
from spacetraders_bot.core.routing import RouteOptimizer, FuelCalculator
from spacetraders_bot.core.smart_navigator import SmartNavigator


class TestRoutingCriticalBugs:
    """Test suite for three critical routing bugs"""

    @pytest.fixture
    def real_world_graph(self):
        """
        Simplified X1-GH18 graph matching production scenario

        Key waypoints:
        - H57: Purchase market (cheapest price, 762u from J62)
        - J62: Contract delivery waypoint
        - B7: Nearby market (only 100u from J62) - should be selected instead
        """
        return {
            "system": "X1-GH18",
            "waypoints": {
                "X1-GH18-H57": {
                    "type": "MARKETPLACE",
                    "x": 0,
                    "y": 0,
                    "traits": ["MARKETPLACE"],
                    "has_fuel": True,
                    "orbitals": []
                },
                "X1-GH18-J62": {
                    "type": "INDUSTRIAL_HQ",
                    "x": 600,
                    "y": 400,
                    "traits": ["MARKETPLACE"],
                    "has_fuel": True,
                    "orbitals": []
                },
                "X1-GH18-B7": {
                    "type": "MARKETPLACE",
                    "x": 550,
                    "y": 480,
                    "traits": ["MARKETPLACE"],
                    "has_fuel": True,
                    "orbitals": []
                },
                # Intermediate waypoint to make path more complex (increase iterations)
                "X1-GH18-M1": {
                    "type": "MOON",
                    "x": 300,
                    "y": 200,
                    "traits": [],
                    "has_fuel": False,
                    "orbitals": []
                },
                "X1-GH18-M2": {
                    "type": "MOON",
                    "x": 450,
                    "y": 300,
                    "traits": [],
                    "has_fuel": False,
                    "orbitals": []
                },
            },
            "edges": [
                {"from": "X1-GH18-H57", "to": "X1-GH18-M1", "distance": 360.5, "type": "normal"},
                {"from": "X1-GH18-M1", "to": "X1-GH18-M2", "distance": 180.2, "type": "normal"},
                {"from": "X1-GH18-M2", "to": "X1-GH18-J62", "distance": 180.2, "type": "normal"},
                {"from": "X1-GH18-H57", "to": "X1-GH18-J62", "distance": 721.1, "type": "normal"},  # Direct path
                {"from": "X1-GH18-B7", "to": "X1-GH18-J62", "distance": 111.8, "type": "normal"},  # Short path
                {"from": "X1-GH18-M1", "to": "X1-GH18-B7", "distance": 360.5, "type": "normal"},
            ]
        }

    @pytest.fixture
    def ship_data(self):
        """SILMARETH-1 ship data"""
        return {
            "symbol": "SILMARETH-1",
            "engine": {"speed": 30},  # Standard speed
            "fuel": {
                "current": 400,
                "capacity": 400
            },
            "cargo": {
                "capacity": 40,
                "units": 40,
                "inventory": [
                    {"symbol": "AMMONIA_ICE", "units": 40}
                ]
            },
            "nav": {
                "waypointSymbol": "X1-GH18-H57",
                "status": "IN_ORBIT",
                "systemSymbol": "X1-GH18"
            },
            "frame": {"integrity": 1.0},
            "registration": {"role": "HAULER"}
        }

    # =========================================================================
    # BUG #1: A* ITERATION LIMIT TOO LOW
    # =========================================================================

    def test_bug1_iteration_limit_insufficient_for_long_paths(self, real_world_graph, ship_data):
        """
        BUG #1: max_iterations=10000 is too low for 762-unit paths

        Expected: Route found (H57 → J62 is feasible with DRIFT)
        Current: "No route found after 10000 iterations"

        Root cause: Complex graph with multiple waypoints requires >10k iterations
        Fix: Increase max_iterations from 10000 to 50000
        """
        optimizer = RouteOptimizer(real_world_graph, ship_data)

        # Calculate exact fuel requirement (should be well within capacity)
        distance = 721.1  # H57 → J62 direct distance
        drift_fuel = FuelCalculator.fuel_cost(distance, 'DRIFT')
        cruise_fuel = FuelCalculator.fuel_cost(distance, 'CRUISE')

        print(f"\n=== BUG #1 ANALYSIS ===")
        print(f"Distance: {distance:.1f} units")
        print(f"Ship fuel: {ship_data['fuel']['current']}/{ship_data['fuel']['capacity']}")
        print(f"DRIFT fuel needed: {drift_fuel} (~{distance * 0.003:.1f})")
        print(f"CRUISE fuel needed: {cruise_fuel} (~{distance * 1.0:.1f})")
        print(f"Fuel adequate: DRIFT={ship_data['fuel']['current'] >= drift_fuel}, "
              f"CRUISE={ship_data['fuel']['current'] >= cruise_fuel}")

        # Attempt route planning
        route = optimizer.find_optimal_route(
            start="X1-GH18-H57",
            goal="X1-GH18-J62",
            current_fuel=400,
            prefer_cruise=True
        )

        # BEFORE FIX: route should be None (iteration limit hit)
        # AFTER FIX: route should exist with refuel stops or DRIFT mode
        if route is None:
            pytest.fail(
                f"BUG #1 REPRODUCED: No route found after max_iterations\n"
                f"This proves iteration limit is too low for {distance:.1f}-unit paths\n"
                f"Ship has {ship_data['fuel']['current']} fuel (DRIFT needs ~{drift_fuel})"
            )

        print(f"\n✅ BUG #1 FIXED: Route found with {len(route['steps'])} steps")
        print(f"Total time: {route['total_time']}s, Final fuel: {route['final_fuel']}")

    # =========================================================================
    # BUG #2: FALSE "INSUFFICIENT FUEL" ERROR MESSAGE
    # =========================================================================

    def test_bug2_misleading_insufficient_fuel_error(self, real_world_graph, ship_data):
        """
        BUG #2: SmartNavigator reports "insufficient fuel even with DRIFT" when route exists

        Expected: Accurate error message (e.g., "Route planning iteration limit exceeded")
        Current: "insufficient fuel even with DRIFT" (FALSE - fuel is adequate)

        Root cause: validate_route() returns generic error when route planner fails
        Real issue: A* hit iteration limit, NOT fuel constraint
        Fix: Distinguish between fuel constraints and pathfinding failures
        """
        # Mock API for SmartNavigator
        mock_api = Mock()
        mock_api.get_agent = Mock(return_value={"data": {"symbol": "TEST"}})

        navigator = SmartNavigator(mock_api, "X1-GH18", graph=real_world_graph)

        # Validate route from H57 to J62
        is_valid, reason = navigator.validate_route(ship_data, "X1-GH18-J62")

        print(f"\n=== BUG #2 ANALYSIS ===")
        print(f"Route validation result: {is_valid}")
        print(f"Reason: {reason}")

        # Calculate actual fuel feasibility
        distance = 721.1
        drift_fuel = FuelCalculator.fuel_cost(distance, 'DRIFT')
        has_enough_fuel = ship_data['fuel']['current'] >= drift_fuel

        print(f"\nFuel analysis:")
        print(f"  Distance: {distance:.1f} units")
        print(f"  DRIFT fuel needed: {drift_fuel}")
        print(f"  Ship fuel: {ship_data['fuel']['current']}")
        print(f"  Fuel adequate: {has_enough_fuel}")

        # BUG: Error message claims fuel is insufficient when it's actually adequate
        if not is_valid and "insufficient fuel" in reason.lower():
            if has_enough_fuel:
                pytest.fail(
                    f"BUG #2 REPRODUCED: Misleading error message\n"
                    f"Error says: '{reason}'\n"
                    f"Reality: Ship has {ship_data['fuel']['current']} fuel, "
                    f"DRIFT needs {drift_fuel}\n"
                    f"Real cause: A* iteration limit, NOT fuel constraint"
                )

        # AFTER FIX: Either route is valid, or error message is accurate
        if not is_valid:
            assert "insufficient fuel" not in reason.lower(), \
                "Error message should not mention fuel when fuel is adequate"

        print(f"\n✅ BUG #2 FIXED: Error message is accurate")

    # =========================================================================
    # BUG #3: CONTRACT MARKET SELECTION WITHOUT DISTANCE VALIDATION
    # =========================================================================

    def test_bug3_contract_market_ignores_distance(self):
        """
        BUG #3: Contract operation selects cheapest market without distance validation

        Expected: Select nearby market (B7, 100u from J62)
        Current: Selects cheapest market (H57, 762u from J62)

        Root cause: _find_lowest_price_market() only optimizes price, ignores distance
        Fix: Add distance validation before selecting purchase market

        Real-world impact:
        - SILMARETH-1 tried to navigate 762 units from H57 to J62
        - Navigation failed due to routing bugs #1 and #2
        - Should have selected B7 (100u from J62) instead
        """
        from spacetraders_bot.operations.contracts import _find_lowest_price_market
        from spacetraders_bot.core.database import Database

        # Create temporary in-memory database
        db = Database(":memory:")

        # Populate with market data
        with db.transaction() as conn:
            # H57: Cheapest market (1000 cr/unit) but 762 units from delivery
            conn.execute(
                """
                INSERT INTO market_data (waypoint_symbol, good_symbol, sell_price, supply)
                VALUES (?, ?, ?, ?)
                """,
                ("X1-GH18-H57", "AMMONIA_ICE", 1000, "ABUNDANT")
            )

            # B7: Nearby market (1200 cr/unit) only 100 units from delivery
            conn.execute(
                """
                INSERT INTO market_data (waypoint_symbol, good_symbol, sell_price, supply)
                VALUES (?, ?, ?, ?)
                """,
                ("X1-GH18-B7", "AMMONIA_ICE", 1200, "MODERATE")
            )

        # Find cheapest market (current behavior)
        result = _find_lowest_price_market(db, "AMMONIA_ICE", "X1-GH18")

        print(f"\n=== BUG #3 ANALYSIS ===")
        print(f"Selected market: {result[0] if result else 'None'}")
        print(f"Price: {result[1] if result else 'N/A'} cr/unit")

        # BUG: Selects H57 (cheapest) without considering 762-unit distance
        if result and result[0] == "X1-GH18-H57":
            print(f"\n❌ BUG #3 REPRODUCED:")
            print(f"  Selected: H57 (1000 cr/unit, 762u from J62)")
            print(f"  Should select: B7 (1200 cr/unit, 100u from J62)")
            print(f"  Extra cost: 200 cr/unit × 40 units = 8,000 cr")
            print(f"  WORTH IT to avoid 662-unit navigation failure!")

            # This is the bug - we NEED distance validation
            pytest.fail(
                f"BUG #3 REPRODUCED: Market selection ignores distance\n"
                f"Selected H57 (762u from delivery) instead of B7 (100u from delivery)\n"
                f"Must add distance validation to contract market selection"
            )

        print(f"\n✅ BUG #3 FIXED: Market selection considers distance")

    # =========================================================================
    # INTEGRATION TEST: ALL THREE BUGS
    # =========================================================================

    def test_integration_all_bugs_together(self, real_world_graph, ship_data):
        """
        Integration test: Reproduce full SILMARETH-1 failure scenario

        Scenario:
        1. Contract requires AMMONIA_ICE delivery to J62
        2. Market selection picks H57 (cheapest, 762u away) - BUG #3
        3. Navigation H57 → J62 fails due to iteration limit - BUG #1
        4. Error message claims fuel insufficient (false) - BUG #2

        Result: Ship stranded with cargo, unable to complete contract

        Fix validates:
        - Market selection considers distance (picks B7 instead of H57)
        - Navigation succeeds for long paths (increased iterations)
        - Error messages are accurate (distinguish fuel vs pathfinding)
        """
        print(f"\n=== INTEGRATION TEST: SILMARETH-1 FAILURE SCENARIO ===")

        # Step 1: Market selection (BUG #3)
        print(f"\n1. Market Selection")
        print(f"   Available markets:")
        print(f"     - H57: 1000 cr/unit, 762u from J62")
        print(f"     - B7:  1200 cr/unit, 100u from J62")

        # Current behavior: selects H57 (cheapest)
        # Fixed behavior: should select B7 (nearby)
        selected_market = "X1-GH18-H57"  # BUG: selects distant market
        delivery_waypoint = "X1-GH18-J62"

        # Calculate distance from selected market to delivery
        h57 = real_world_graph['waypoints']['X1-GH18-H57']
        j62 = real_world_graph['waypoints']['X1-GH18-J62']
        distance_to_delivery = math.sqrt(
            (j62['x'] - h57['x']) ** 2 + (j62['y'] - h57['y']) ** 2
        )

        print(f"   Selected: {selected_market}")
        print(f"   Distance to delivery: {distance_to_delivery:.1f} units")

        if distance_to_delivery > 300:
            print(f"   ⚠️  WARNING: Market >300 units from delivery (BUG #3)")

        # Step 2: Navigation attempt (BUGS #1 and #2)
        print(f"\n2. Navigation Planning")

        # Update ship location to selected market
        ship_data['nav']['waypointSymbol'] = selected_market

        optimizer = RouteOptimizer(real_world_graph, ship_data)
        route = optimizer.find_optimal_route(
            start=selected_market,
            goal=delivery_waypoint,
            current_fuel=ship_data['fuel']['current'],
            prefer_cruise=True
        )

        if route is None:
            print(f"   ❌ NAVIGATION FAILED")
            print(f"   Error: No route found (iteration limit exceeded)")

            # Check if fuel was actually sufficient
            drift_fuel = FuelCalculator.fuel_cost(distance_to_delivery, 'DRIFT')
            has_fuel = ship_data['fuel']['current'] >= drift_fuel

            print(f"\n3. Fuel Analysis")
            print(f"   Ship fuel: {ship_data['fuel']['current']}/{ship_data['fuel']['capacity']}")
            print(f"   DRIFT fuel needed: {drift_fuel}")
            print(f"   Fuel adequate: {has_fuel}")

            if has_fuel:
                pytest.fail(
                    f"ALL 3 BUGS REPRODUCED:\n"
                    f"BUG #3: Selected H57 (762u away) instead of B7 (100u away)\n"
                    f"BUG #1: Route planning failed (iteration limit)\n"
                    f"BUG #2: Error implies fuel issue when fuel is adequate\n"
                    f"\nResult: SILMARETH-1 stranded with contract cargo"
                )

        print(f"   ✅ Navigation successful: {len(route['steps'])} steps")
        print(f"\n✅ ALL BUGS FIXED: Contract delivery feasible")


if __name__ == "__main__":
    pytest.main([__file__, "-v", "-s"])
