import math
import tempfile
from pathlib import Path

import pytest

from spacetraders_bot.core.routing import RouteOptimizer


def build_graph():
    return {
        "system": "X1-UNIT",
        "waypoints": {
            "X1-UNIT-A": {"x": 0, "y": 0, "has_fuel": True, "traits": ["MARKETPLACE"]},
            "X1-UNIT-B": {"x": 100, "y": 0, "has_fuel": False, "traits": []},
            "X1-UNIT-C": {"x": 200, "y": 0, "has_fuel": True, "traits": ["MARKETPLACE"]},
        },
        "edges": [
            {"from": "X1-UNIT-A", "to": "X1-UNIT-B", "distance": 100.0},
            {"from": "X1-UNIT-B", "to": "X1-UNIT-A", "distance": 100.0},
            {"from": "X1-UNIT-B", "to": "X1-UNIT-C", "distance": 100.0},
            {"from": "X1-UNIT-C", "to": "X1-UNIT-B", "distance": 100.0},
        ],
    }


def build_ship(location: str, fuel: int, capacity: int = 120, role: str = 'EXPLORER'):
    return {
        "symbol": "TEST-ROUTE-1",
        "nav": {
            "waypointSymbol": location,
            "status": "IN_ORBIT",
        },
        "fuel": {
            "current": fuel,
            "capacity": capacity,
        },
        "frame": {"integrity": 100},
        "registration": {"role": role},
        "engine": {"speed": 30},
        "cooldown": {"remainingSeconds": 0},
    }


def regression_find_optimal_route_cruise():
    graph = build_graph()
    ship = build_ship("X1-UNIT-A", fuel=120)

    optimizer = RouteOptimizer(graph, ship)
    route = optimizer.find_optimal_route("X1-UNIT-A", "X1-UNIT-C", current_fuel=120, prefer_cruise=True)

    assert route is not None
    navigate_steps = [step for step in route["steps"] if step["action"] == "navigate"]
    assert [step["to"] for step in navigate_steps] == ["X1-UNIT-B", "X1-UNIT-C"]
    assert navigate_steps[0]["mode"] == "CRUISE"


def regression_emergency_drift_to_fuel_station():
    graph = build_graph()
    ship = build_ship("X1-UNIT-B", fuel=2)

    optimizer = RouteOptimizer(graph, ship)
    route = optimizer.find_optimal_route("X1-UNIT-B", "X1-UNIT-C", current_fuel=2, prefer_cruise=True)

    assert route is not None
    navigate_steps = [step for step in route["steps"] if step["action"] == "navigate"]
    assert navigate_steps[0]["mode"] == "DRIFT"


def regression_prefer_drift_when_allowed():
    graph = build_graph()
    ship = build_ship("X1-UNIT-A", fuel=5)

    optimizer = RouteOptimizer(graph, ship)
    route = optimizer.find_optimal_route("X1-UNIT-A", "X1-UNIT-C", current_fuel=5, prefer_cruise=False)

    assert route is not None
    assert any(step["mode"] == "DRIFT" for step in route["steps"] if step["action"] == "navigate")
