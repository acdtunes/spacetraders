"""
Test probe/satellite ship navigation with 0 fuel capacity.

Verifies that ships with SATELLITE/PROBE role and 0 fuel capacity:
1. Pass health validation
2. Use CRUISE mode (not DRIFT)
3. Have 0 fuel cost for all navigation steps
4. Successfully route using OR-Tools probe fast-path
"""

import pytest
from spacetraders_bot.core.smart_navigator import SmartNavigator
from spacetraders_bot.core.ortools_router import ORToolsRouter
from spacetraders_bot.core.routing_config import RoutingConfig


def build_test_graph():
    """Build simple test graph with 3 waypoints."""
    return {
        "system": "X1-TEST",
        "waypoints": {
            "X1-TEST-A1": {"x": 0, "y": 0, "type": "PLANET", "traits": [], "has_fuel": False, "orbitals": []},
            "X1-TEST-B2": {"x": 100, "y": 0, "type": "MOON", "traits": [], "has_fuel": False, "orbitals": []},
            "X1-TEST-C3": {"x": 200, "y": 100, "type": "ASTEROID", "traits": [], "has_fuel": False, "orbitals": []},
        },
        "edges": [
            {"from": "X1-TEST-A1", "to": "X1-TEST-B2", "distance": 100, "type": "normal"},
            {"from": "X1-TEST-B2", "to": "X1-TEST-C3", "distance": 141.4, "type": "normal"},
            {"from": "X1-TEST-A1", "to": "X1-TEST-C3", "distance": 223.6, "type": "normal"},
        ],
    }


def build_probe_ship_data(symbol="PROBE-1", location="X1-TEST-A1"):
    """Build ship data for a probe/satellite with 0 fuel capacity."""
    return {
        "symbol": symbol,
        "registration": {"role": "SATELLITE"},  # SATELLITE or PROBE role
        "nav": {
            "waypointSymbol": location,
            "systemSymbol": "X1-TEST",
            "status": "IN_ORBIT",
        },
        "fuel": {
            "current": 0,
            "capacity": 0,  # 0 fuel capacity by design
        },
        "engine": {"speed": 10},
        "frame": {"integrity": 1.0},
        "cooldown": {"remainingSeconds": 0},
    }


def build_normal_ship_data_with_zero_fuel(symbol="SHIP-1", location="X1-TEST-A1"):
    """Build ship data for a normal ship (not probe) with 0 fuel capacity - should FAIL validation."""
    return {
        "symbol": symbol,
        "registration": {"role": "HAULER"},  # Normal role, not SATELLITE/PROBE
        "nav": {
            "waypointSymbol": location,
            "systemSymbol": "X1-TEST",
            "status": "IN_ORBIT",
        },
        "fuel": {
            "current": 0,
            "capacity": 0,  # 0 fuel capacity but wrong role
        },
        "engine": {"speed": 10},
        "frame": {"integrity": 1.0},
        "cooldown": {"remainingSeconds": 0},
    }


class MockAPIClient:
    """Mock API client for testing."""
    def __init__(self):
        self.ships = {}

    def get_ship(self, symbol):
        return self.ships.get(symbol)


def test_probe_ship_passes_health_validation():
    """Probe/satellite ships with 0 fuel capacity should pass health validation."""
    graph = build_test_graph()
    ship_data = build_probe_ship_data()
    api = MockAPIClient()

    navigator = SmartNavigator(api, "X1-TEST", graph=graph)

    is_healthy, reason = navigator._validate_ship_health(ship_data)

    assert is_healthy, f"Probe ship should pass health check, but failed: {reason}"
    assert reason == "Ship health OK"


def test_normal_ship_with_zero_fuel_fails_health_validation():
    """Normal ships (non-probe/satellite) with 0 fuel capacity should FAIL health validation."""
    graph = build_test_graph()
    ship_data = build_normal_ship_data_with_zero_fuel()
    api = MockAPIClient()

    navigator = SmartNavigator(api, "X1-TEST", graph=graph)

    is_healthy, reason = navigator._validate_ship_health(ship_data)

    assert not is_healthy, "Normal ship with 0 fuel capacity should fail health check"
    assert "no fuel capacity" in reason.lower()


def test_probe_routing_uses_cruise_mode():
    """Probe routing should use CRUISE mode (not DRIFT) with 0 fuel cost."""
    graph = build_test_graph()
    ship_data = build_probe_ship_data(location="X1-TEST-A1")

    router = ORToolsRouter(graph, ship_data, RoutingConfig())
    route = router.find_optimal_route("X1-TEST-A1", "X1-TEST-C3", current_fuel=0)

    assert route is not None, "Probe routing should succeed"
    assert route["final_fuel"] == 0, "Probe should end with 0 fuel"

    # Check all navigation steps
    nav_steps = [step for step in route["steps"] if step["action"] == "navigate"]
    assert len(nav_steps) > 0, "Route should have navigation steps"

    for step in nav_steps:
        assert step["fuel_cost"] == 0, f"Probe navigation should cost 0 fuel, but step cost {step['fuel_cost']}"
        assert step["mode"] == "CRUISE", f"Probe should use CRUISE mode, not {step['mode']}"


def test_probe_routing_works_for_multi_hop():
    """Probe routing should work for multi-hop journeys."""
    graph = build_test_graph()
    ship_data = build_probe_ship_data(location="X1-TEST-A1")

    router = ORToolsRouter(graph, ship_data, RoutingConfig())

    # Route through multiple waypoints
    route1 = router.find_optimal_route("X1-TEST-A1", "X1-TEST-B2", current_fuel=0)
    assert route1 is not None
    assert route1["final_fuel"] == 0

    route2 = router.find_optimal_route("X1-TEST-B2", "X1-TEST-C3", current_fuel=0)
    assert route2 is not None
    assert route2["final_fuel"] == 0

    # All steps should have 0 fuel cost
    all_steps = route1["steps"] + route2["steps"]
    nav_steps = [step for step in all_steps if step["action"] == "navigate"]
    assert all(step["fuel_cost"] == 0 for step in nav_steps)


def test_probe_ship_role_variations():
    """Test both SATELLITE and PROBE roles are recognized."""
    graph = build_test_graph()
    api = MockAPIClient()
    navigator = SmartNavigator(api, "X1-TEST", graph=graph)

    # Test SATELLITE role
    satellite_data = build_probe_ship_data()
    satellite_data["registration"]["role"] = "SATELLITE"
    is_healthy, _ = navigator._validate_ship_health(satellite_data)
    assert is_healthy, "SATELLITE role should pass health check"

    # Test PROBE role
    probe_data = build_probe_ship_data()
    probe_data["registration"]["role"] = "PROBE"
    is_healthy, _ = navigator._validate_ship_health(probe_data)
    assert is_healthy, "PROBE role should pass health check"

    # Test lowercase (should normalize)
    lowercase_data = build_probe_ship_data()
    lowercase_data["registration"]["role"] = "satellite"
    is_healthy, _ = navigator._validate_ship_health(lowercase_data)
    assert is_healthy, "Lowercase 'satellite' should pass (normalized to uppercase)"


def test_smart_navigator_validates_probe_route():
    """SmartNavigator.validate_route() should work for probe ships."""
    graph = build_test_graph()
    ship_data = build_probe_ship_data(location="X1-TEST-A1")
    api = MockAPIClient()

    navigator = SmartNavigator(api, "X1-TEST", graph=graph)

    valid, reason = navigator.validate_route(ship_data, "X1-TEST-C3")

    assert valid, f"Probe route should be valid, but failed: {reason}"


def test_smart_navigator_plan_route_for_probe():
    """SmartNavigator.plan_route() should work for probe ships."""
    graph = build_test_graph()
    ship_data = build_probe_ship_data(location="X1-TEST-A1")
    api = MockAPIClient()

    navigator = SmartNavigator(api, "X1-TEST", graph=graph)

    route = navigator.plan_route(ship_data, "X1-TEST-C3", prefer_cruise=True)

    assert route is not None, "Probe route planning should succeed"
    assert route["final_fuel"] == 0

    nav_steps = [step for step in route["steps"] if step["action"] == "navigate"]
    assert all(step["fuel_cost"] == 0 for step in nav_steps), "All probe navigation should cost 0 fuel"
    assert all(step["mode"] == "CRUISE" for step in nav_steps), "Probes should use CRUISE mode"


if __name__ == "__main__":
    pytest.main([__file__, "-v"])
