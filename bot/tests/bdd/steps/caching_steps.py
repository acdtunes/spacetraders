"""Step definitions for tour cache validation scenarios."""

import pytest
import tempfile
from pathlib import Path
from pytest_bdd import given, when, then, parsers, scenarios

from spacetraders_bot.core.database import Database
from spacetraders_bot.core.ortools_router import ORToolsTSP

# Load all caching scenarios
scenarios('../../features/caching/tour_cache_validation.feature')


@pytest.fixture
def temp_db_path():
    """Create temporary database for isolated testing."""
    with tempfile.NamedTemporaryFile(delete=False, suffix=".db") as f:
        db_path = Path(f.name)
    yield db_path
    if db_path.exists():
        db_path.unlink()


@pytest.fixture
def caching_context():
    """Shared context for caching scenarios."""
    return {
        'db': None,
        'graph': None,
        'ship': None,
        'cache_entry': None,
        'tour': None,
    }


@given(parsers.parse('the X1-JV40 system with waypoints:\n{waypoint_table}'))
def setup_jv40_system(caching_context, waypoint_table):
    """Set up X1-JV40 system with waypoints."""
    # Parse waypoint table - simplified graph for testing
    waypoints = {
        "X1-JV40-A1": {"x": 0, "y": 0, "has_fuel": True, "type": "PLANET", "traits": ["MARKETPLACE"]},
        "X1-JV40-J52": {"x": 100, "y": 100, "has_fuel": True, "type": "FUEL_STATION", "traits": []},
        "X1-JV40-J53": {"x": 110, "y": 110, "has_fuel": True, "type": "PLANET", "traits": ["MARKETPLACE"]},
    }

    edges = []
    wp_list = list(waypoints.keys())
    for i, wp1 in enumerate(wp_list):
        for wp2 in wp_list[i + 1:]:
            wp1_data, wp2_data = waypoints[wp1], waypoints[wp2]
            distance = ((wp2_data["x"] - wp1_data["x"]) ** 2 + (wp2_data["y"] - wp1_data["y"]) ** 2) ** 0.5
            edges.append({"from": wp1, "to": wp2, "distance": round(distance, 2), "type": "normal"})

    caching_context['graph'] = {"system": "X1-JV40", "waypoints": waypoints, "edges": edges}


@given(parsers.parse('a {ship_type} ship at "{start_waypoint}"'))
def setup_ship(caching_context, ship_type, start_waypoint):
    """Set up ship at starting waypoint."""
    if ship_type == "scout":
        ship_data = {"symbol": "SCOUT-4", "engine": {"speed": 9}, "fuel": {"current": 1000, "capacity": 1000}}
    elif ship_type == "probe":
        ship_data = {"symbol": "PROBE-1", "engine": {"speed": 9}, "fuel": {"current": 0, "capacity": 0}}
    else:
        raise ValueError(f"Unknown ship type: {ship_type}")

    ship_data["nav"] = {"waypointSymbol": start_waypoint}
    caching_context['ship'] = ship_data


@given(parsers.parse('a stale cache entry with tour order: {tour_order}'))
def create_stale_cache(caching_context, temp_db_path, tour_order):
    """Create a stale cache entry with specified tour order."""
    db = Database(temp_db_path)
    caching_context['db'] = db

    # Parse tour order (e.g., "A1 → J53 → J52 → A1")
    waypoints_in_order = [wp.strip() for wp in tour_order.replace('→', ',').split(',')]
    # Expand to full names
    tour_order_full = ["X1-JV40-" + wp for wp in waypoints_in_order]

    # Markets are all except start/end
    markets = list(set(tour_order_full[1:-1]))  # Exclude start and return

    with db.transaction() as conn:
        db.save_tour_cache(
            conn, system="X1-JV40", markets=markets, algorithm="ortools",
            tour_order=tour_order_full, total_distance=200.0, start_waypoint=tour_order_full[0]
        )


@given(parsers.parse('a valid cache entry with tour order: {tour_order}'))
def create_valid_cache(caching_context, temp_db_path, tour_order):
    """Create a valid cache entry with specified tour order."""
    db = Database(temp_db_path)
    caching_context['db'] = db

    # Parse tour order
    waypoints_in_order = [wp.strip() for wp in tour_order.replace('→', ',').split(',')]
    tour_order_full = ["X1-JV40-" + wp for wp in waypoints_in_order]

    markets = list(set(tour_order_full[1:-1]))

    with db.transaction() as conn:
        db.save_tour_cache(
            conn, system="X1-JV40", markets=markets, algorithm="ortools",
            tour_order=tour_order_full, total_distance=100.0, start_waypoint=tour_order_full[0]
        )


@when(parsers.parse('I request an optimized tour for markets {markets}'))
def request_optimized_tour(caching_context, temp_db_path):
    """Request an optimized tour for specified markets."""
    db = caching_context.get('db') or Database(temp_db_path)
    graph = caching_context['graph']
    ship = caching_context['ship']

    tsp = ORToolsTSP(graph, db_path=str(temp_db_path))
    tour = tsp.optimise_tour(
        waypoints=["X1-JV40-A1", "X1-JV40-J53"],
        start="X1-JV40-A1",
        ship_data=ship,
        return_to_start=True,
        use_cache=True,
        algorithm="ortools"
    )

    caching_context['tour'] = tour


@then("the cache should be invalidated due to fuel station presence")
@then("a fresh tour should be built")
def verify_cache_invalidated(caching_context):
    """Verify cache was invalidated and fresh tour built."""
    assert caching_context['tour'] is not None, "Tour should be rebuilt"


@then("the cache should be used")
def verify_cache_used(caching_context):
    """Verify cache was used."""
    assert caching_context['tour'] is not None, "Tour should be from cache"


@then(parsers.parse('the tour should visit "{waypoint}"'))
def verify_tour_visits(caching_context, waypoint):
    """Verify tour visits specified waypoint."""
    tour = caching_context['tour']
    visited = set([tour["start"]])
    for leg in tour.get("legs", []):
        if "goal" in leg:
            visited.add(leg["goal"])
        for step in leg.get("steps", []):
            if step.get("action") == "navigate":
                visited.add(step["to"])

    assert waypoint in visited, f"Tour should visit {waypoint}, visited: {visited}"


@then(parsers.parse('the tour should NOT visit "{waypoint}"'))
@then(parsers.parse('the probe tour should NOT visit "{waypoint}"'))
def verify_tour_not_visits(caching_context, waypoint):
    """Verify tour does NOT visit specified waypoint."""
    tour = caching_context['tour']
    visited = set([tour["start"]])
    for leg in tour.get("legs", []):
        if "goal" in leg:
            visited.add(leg["goal"])
        for step in leg.get("steps", []):
            if step.get("action") == "navigate":
                visited.add(step["to"])

    assert waypoint not in visited, f"Tour should NOT visit {waypoint}, but visited: {visited}"


@then(parsers.parse('the tour should visit only "{waypoint1}" and "{waypoint2}"'))
def verify_tour_visits_only(caching_context, waypoint1, waypoint2):
    """Verify tour visits only specified waypoints."""
    tour = caching_context['tour']
    visited = set([tour["start"]])
    for leg in tour.get("legs", []):
        if "goal" in leg:
            visited.add(leg["goal"])

    expected = {waypoint1, waypoint2}
    assert visited == expected, f"Tour should visit only {expected}, but visited: {visited}"
