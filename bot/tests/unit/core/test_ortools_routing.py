import math
from pathlib import Path

import pytest

from spacetraders_bot.core.ortools_router import ORToolsRouter
from spacetraders_bot.core.routing_config import RoutingConfig, RoutingConfigError
from spacetraders_bot.core.routing_validator import RoutingValidator
from spacetraders_bot.core.routing_pause import is_paused, resume as resume_pause


def build_graph(edges):
    waypoints = {}
    for frm, to, distance, fuel_info in edges:
        for symbol in (frm, to):
            has_fuel = fuel_info.get(symbol, False) if isinstance(fuel_info, dict) else False
            waypoints.setdefault(symbol, {
                "type": "TEST",
                "x": 0,
                "y": 0,
                "traits": ["MARKETPLACE"] if has_fuel else [],
                "has_fuel": has_fuel,
                "orbitals": [],
            })
    graph_edges = []
    for frm, to, distance, _ in edges:
        graph_edges.append({"from": frm, "to": to, "distance": distance, "type": "normal"})
    return {"system": "X1-TEST", "waypoints": waypoints, "edges": graph_edges}


def regression_ortools_router_probe_fast_path():
    graph = {
        "system": "X1-TEST",
        "waypoints": {
            "A": {"x": 0, "y": 0, "traits": [], "has_fuel": False, "orbitals": []},
            "B": {"x": 10, "y": 0, "traits": [], "has_fuel": False, "orbitals": []},
        },
        "edges": [{"from": "A", "to": "B", "distance": 10, "type": "normal"}],
    }
    ship_data = {
        "symbol": "PROBE",
        "nav": {"waypointSymbol": "A", "status": "IN_ORBIT"},
        "fuel": {"current": 0, "capacity": 0},
        "engine": {"speed": 20},
    }

    router = ORToolsRouter(graph, ship_data, RoutingConfig())
    route = router.find_optimal_route("A", "B", 0)

    assert route is not None
    assert route["total_time"] >= 0
    assert all(step["fuel_cost"] == 0 for step in route["steps"] if step["action"] == "navigate")


def regression_ortools_router_selects_drift_when_cruise_impossible():
    edges = [
        ("A", "B", 500, {"A": True, "B": False}),
    ]
    graph = build_graph(edges)

    ship_data = {
        "symbol": "MINER-1",
        "nav": {"waypointSymbol": "A", "status": "IN_ORBIT"},
        "fuel": {"current": 50, "capacity": 50},
        "engine": {"speed": 10},
    }

    router = ORToolsRouter(graph, ship_data, RoutingConfig())
    route = router.find_optimal_route("A", "B", 50, prefer_cruise=True)

    assert route is not None
    navigate_steps = [step for step in route["steps"] if step["action"] == "navigate"]
    assert navigate_steps
    assert navigate_steps[0]["mode"] == "DRIFT"


def regression_routing_config_validation(tmp_path: Path):
    bad_config = tmp_path / "bad.yaml"
    bad_config.write_text("fuel_safety_margin: -1\n")

    with pytest.raises(RoutingConfigError):
        RoutingConfig(bad_config)


def regression_routing_validator_detects_deviation(monkeypatch, tmp_path):
    config_path = tmp_path / "routing.yaml"
    config_path.write_text(
        """
flight_modes:
  CRUISE:
    time_multiplier: 31
    fuel_rate: 1.0
  DRIFT:
    time_multiplier: 26
    fuel_rate: 0.003
validation:
  max_deviation_percent: 5.0
  pause_on_failure: true
"""
    )
    config = RoutingConfig(config_path)

    class StubAPI:
        def __init__(self):
            self.state = {
                "symbol": "TEST",
                "nav": {
                    "waypointSymbol": "A",
                    "systemSymbol": "SYS",
                    "status": "IN_ORBIT",
                },
                "fuel": {"current": 40, "capacity": 100},
            }

        def get_ship(self, ship_symbol: str):
            return self.state

    class StubNavigator:
        def __init__(self, api, system_symbol, graph=None):
            self.api = api

        def plan_route(self, ship_data, destination, prefer_cruise=True):
            return {
                "total_time": 100,
                "steps": [
                    {"action": "navigate", "from": "A", "to": destination, "fuel_cost": 10, "distance": 50, "mode": "CRUISE", "time": 100},
                ],
            }

        def execute_route(self, ship_controller, destination, prefer_cruise=True):
            ship_controller.api.state["fuel"]["current"] = 15
            return True

    class StubShipController:
        def __init__(self, api, ship_symbol):
            self.api = api

        def get_status(self):
            return self.api.get_ship("TEST")

        def refuel(self, *args, **kwargs):
            return True

    api = StubAPI()

    monkeypatch.setattr("spacetraders_bot.core.routing_validator.SmartNavigator", StubNavigator)
    monkeypatch.setattr("spacetraders_bot.core.routing_validator.ShipController", StubShipController)

    current_time = {"value": 1_000.0}

    def fake_time():
        value = current_time["value"]
        current_time["value"] += 10.0
        return value

    monkeypatch.setattr("spacetraders_bot.core.routing_validator.time.time", fake_time)

    resume_pause()
    validator = RoutingValidator(api, "SYS", config=config)
    result = validator.validate_route("TEST", "B", prefer_cruise=True, execute=True)

    assert result is not None
    assert not result.passed
    assert result.time_deviation_pct > validator.max_deviation_pct
    assert is_paused()
    resume_pause()


def regression_routing_validator_resumes_on_success(monkeypatch, tmp_path):
    config_path = tmp_path / "routing.yaml"
    config_path.write_text(
        """
flight_modes:
  CRUISE:
    time_multiplier: 31
    fuel_rate: 1.0
  DRIFT:
    time_multiplier: 26
    fuel_rate: 0.003
validation:
  max_deviation_percent: 5.0
  pause_on_failure: true
"""
    )
    config = RoutingConfig(config_path)

    class StubAPI:
        def __init__(self):
            self.state = {
                "symbol": "TEST",
                "nav": {
                    "waypointSymbol": "A",
                    "systemSymbol": "SYS",
                    "status": "IN_ORBIT",
                },
                "fuel": {"current": 100, "capacity": 100},
            }

        def get_ship(self, ship_symbol: str):
            return self.state

    class StubNavigator:
        def __init__(self, api, system_symbol, graph=None):
            self.api = api

        def plan_route(self, ship_data, destination, prefer_cruise=True):
            return {
                "total_time": 50,
                "steps": [
                    {"action": "navigate", "from": "A", "to": destination, "fuel_cost": 10, "distance": 20, "mode": "CRUISE", "time": 50},
                ],
            }

        def execute_route(self, ship_controller, destination, prefer_cruise=True):
            ship_controller.api.state["fuel"]["current"] -= 10
            return True

    class StubShipController:
        def __init__(self, api, ship_symbol):
            self.api = api

        def get_status(self):
            return self.api.get_ship("TEST")

        def refuel(self, *args, **kwargs):
            return True

    api = StubAPI()

    monkeypatch.setattr("spacetraders_bot.core.routing_validator.SmartNavigator", StubNavigator)
    monkeypatch.setattr("spacetraders_bot.core.routing_validator.ShipController", StubShipController)

    # deterministic time increments
    current_time = {"value": 1_000.0}

    def fake_time():
        value = current_time["value"]
        current_time["value"] += 50.0
        return value

    monkeypatch.setattr("spacetraders_bot.core.routing_validator.time.time", fake_time)

    resume_pause()
    validator = RoutingValidator(api, "SYS", config=config)
    result = validator.validate_route("TEST", "B", prefer_cruise=True, execute=True)

    assert result is not None
    assert result.passed
    assert not is_paused()
