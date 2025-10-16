import tempfile
from pathlib import Path

from spacetraders_bot.core.routing import TourOptimizer


def build_graph(system: str = "X1-TOUR"):
    return {
        "system": system,
        "waypoints": {
            f"{system}-A": {"x": 0, "y": 0, "has_fuel": True, "traits": ["MARKETPLACE"]},
            f"{system}-B": {"x": 100, "y": 0, "has_fuel": True, "traits": ["MARKETPLACE"]},
            f"{system}-C": {"x": 200, "y": 0, "has_fuel": True, "traits": ["MARKETPLACE"]},
        },
        "edges": [
            {"from": f"{system}-A", "to": f"{system}-B", "distance": 100.0},
            {"from": f"{system}-B", "to": f"{system}-A", "distance": 100.0},
            {"from": f"{system}-B", "to": f"{system}-C", "distance": 100.0},
            {"from": f"{system}-C", "to": f"{system}-B", "distance": 100.0},
            {"from": f"{system}-A", "to": f"{system}-C", "distance": 200.0},
            {"from": f"{system}-C", "to": f"{system}-A", "distance": 200.0},
        ],
    }


def build_ship(start: str, fuel: int = 400, capacity: int = 400):
    return {
        "symbol": "TEST-TOUR-1",
        "nav": {
            "waypointSymbol": start,
            "status": "IN_ORBIT",
        },
        "fuel": {
            "current": fuel,
            "capacity": capacity,
        },
        "frame": {"integrity": 100},
        "registration": {"role": "EXPLORER"},
        "engine": {"speed": 30},
        "cooldown": {"remainingSeconds": 0},
    }


def regression_plan_tour_with_cache(tmp_path):
    graph = build_graph("X1-TOUR")
    ship = build_ship("X1-TOUR-A")
    db_path = tmp_path / "tour_cache.sqlite"

    optimizer = TourOptimizer(graph, ship, db_path=db_path)
    tour = optimizer.plan_tour(
        start="X1-TOUR-A",
        stops=["X1-TOUR-B", "X1-TOUR-C"],
        current_fuel=300,
        return_to_start=True,
        use_cache=True,
    )

    assert tour is not None
    assert tour["return_to_start"] is True

    # Second call should hit cache and still return a tour
    cached_tour = optimizer.plan_tour(
        start="X1-TOUR-A",
        stops=["X1-TOUR-B", "X1-TOUR-C"],
        current_fuel=300,
        return_to_start=True,
        use_cache=True,
    )

    assert cached_tour is not None
    assert cached_tour["return_to_start"] is True


def regression_build_tour_from_invalid_order(tmp_path):
    graph = build_graph("X1-TOUR-INVALID")
    ship = build_ship("X1-TOUR-INVALID-A")
    optimizer = TourOptimizer(graph, ship, db_path=tmp_path / "tour_cache.sqlite")

    # Tour order with fewer than two points should fail validation
    assert optimizer._build_tour_from_order(["X1-TOUR-INVALID-A"], 300, False) is None
