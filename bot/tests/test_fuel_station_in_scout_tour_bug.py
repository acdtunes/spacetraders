#!/usr/bin/env python3
"""
Test for CRITICAL bug: Fuel stations appearing in scout tours

BUG DESCRIPTION:
Scout coordinator filters fuel stations from market list when assigning scouts,
but fuel stations (J52) are appearing in actual scout tours despite being excluded
from the assignment.

EVIDENCE FROM PRODUCTION (System X1-JV40):
- Scout-4 assigned: [A1, J53] (2 markets - both PLANETS with MARKETPLACE)
- Scout-4 actual route: [J53, J52] (J52 is FUEL_STATION - should NOT be visited!)
- A1 is missing from tour, J52 (fuel station) included instead

ROOT CAUSE HYPOTHESIS:
1. Tour cache contains stale entry mapping [A1, J53] → [J53, J52] from old optimization
2. TSP optimizer is confusing A1 with J52 during route planning (coordinate proximity?)
3. Markets list is being corrupted somewhere between coordinator → scout daemon → TSP

EXPECTED BEHAVIOR:
- Scout tour should visit EXACTLY the assigned markets: [A1, J53]
- Fuel stations should NEVER appear in scout tours (only as refuel stops if needed)
- Tour should match coordinator assignment EXACTLY

CRITICAL CONSTRAINT:
- A1 is a PLANET with MARKETPLACE trait - legitimate market
- J52 is a FUEL_STATION - should NEVER be visited for market scouting
"""

import json
import pytest
import tempfile
from pathlib import Path
from unittest.mock import MagicMock, patch

from spacetraders_bot.core.database import Database
from spacetraders_bot.core.ortools_router import ORToolsTSP
from spacetraders_bot.core.routing import TourOptimizer


@pytest.fixture
def temp_db_path():
    """Create temporary database for isolated testing"""
    with tempfile.NamedTemporaryFile(delete=False, suffix=".db") as f:
        db_path = Path(f.name)

    yield db_path

    # Cleanup
    if db_path.exists():
        db_path.unlink()
    wal_file = Path(str(db_path) + "-wal")
    if wal_file.exists():
        wal_file.unlink()
    shm_file = Path(str(db_path) + "-shm")
    if shm_file.exists():
        shm_file.unlink()


@pytest.fixture
def jv40_graph():
    """
    Real-world X1-JV40 system graph matching production evidence.

    Waypoints:
    - A1: PLANET with MARKETPLACE (legitimate market)
    - J52: FUEL_STATION (should NOT be in scout tours)
    - J53: PLANET with MARKETPLACE (legitimate market)
    """
    waypoints = {
        # A1 - PLANET with MARKETPLACE (legitimate market)
        "X1-JV40-A1": {
            "x": 0,
            "y": 0,
            "has_fuel": True,
            "type": "PLANET",
            "traits": ["MARKETPLACE"],
        },
        # J52 - FUEL_STATION (should NOT be visited for market scouting)
        "X1-JV40-J52": {
            "x": 100,
            "y": 100,
            "has_fuel": True,
            "type": "FUEL_STATION",
            "traits": [],  # NO MARKETPLACE trait
        },
        # J53 - PLANET with MARKETPLACE (legitimate market)
        "X1-JV40-J53": {
            "x": 110,
            "y": 110,
            "has_fuel": True,
            "type": "PLANET",
            "traits": ["MARKETPLACE"],
        },
    }

    # Generate edges (fully connected)
    edges = []
    wp_list = list(waypoints.keys())
    for i, wp1 in enumerate(wp_list):
        for wp2 in wp_list[i + 1 :]:
            wp1_data = waypoints[wp1]
            wp2_data = waypoints[wp2]
            distance = ((wp2_data["x"] - wp1_data["x"]) ** 2 + (wp2_data["y"] - wp1_data["y"]) ** 2) ** 0.5
            edges.append({
                "from": wp1,
                "to": wp2,
                "distance": round(distance, 2),
                "type": "normal",
            })

    return {
        "system": "X1-JV40",
        "waypoints": waypoints,
        "edges": edges,
    }


@pytest.fixture
def scout4_ship_data():
    """Scout-4 ship configuration"""
    return {
        "symbol": "SCOUT-4",
        "engine": {"speed": 9},
        "fuel": {"current": 1000, "capacity": 1000},
        "nav": {"waypointSymbol": "X1-JV40-J53"},  # Starts at J53
    }


def test_fuel_station_excluded_from_markets_list(jv40_graph):
    """
    Test that coordinator correctly filters fuel stations from market list.

    This validates the FIRST step: coordinator should only extract MARKETPLACE waypoints.
    """
    # Simulate coordinator extracting markets from graph
    markets = TourOptimizer.get_markets_from_graph(jv40_graph)

    print(f"\nExtracted markets: {markets}")

    # CRITICAL: Only A1 and J53 should be in markets list
    assert "X1-JV40-A1" in markets, "A1 (PLANET+MARKETPLACE) should be in markets"
    assert "X1-JV40-J53" in markets, "J53 (PLANET+MARKETPLACE) should be in markets"
    assert "X1-JV40-J52" not in markets, "J52 (FUEL_STATION) should NOT be in markets"

    # Verify exactly 2 markets
    assert len(markets) == 2, f"Should find exactly 2 markets, found {len(markets)}: {markets}"


def test_tour_visits_assigned_markets_only(temp_db_path, jv40_graph, scout4_ship_data):
    """
    Test that TSP optimizer visits ONLY assigned markets (no fuel stations).

    This is the CRITICAL test for the bug: when assigned [A1, J53],
    tour should visit EXACTLY those waypoints, never J52.
    """
    tsp = ORToolsTSP(jv40_graph, db_path=temp_db_path)

    # Coordinator assignment for Scout-4
    assigned_markets = ["X1-JV40-A1", "X1-JV40-J53"]
    start = assigned_markets[0]  # A1 (partition centroid)
    waypoints = assigned_markets[1:]  # [J53]

    # Generate tour
    tour = tsp.optimise_tour(
        waypoints=waypoints,
        start=start,
        ship_data=scout4_ship_data,
        return_to_start=True,
        use_cache=False,  # Disable cache to ensure fresh optimization
        algorithm="ortools",
    )

    assert tour is not None, "Tour generation failed"

    # Extract all waypoints visited
    visited_waypoints = set()
    visited_waypoints.add(start)

    for leg in tour["legs"]:
        if "goal" in leg:
            visited_waypoints.add(leg["goal"])

    print(f"\nAssigned markets: {assigned_markets}")
    print(f"Tour visits: {visited_waypoints}")

    # CRITICAL CHECKS
    assert "X1-JV40-A1" in visited_waypoints, "A1 should be in tour (assigned market)"
    assert "X1-JV40-J53" in visited_waypoints, "J53 should be in tour (assigned market)"
    assert "X1-JV40-J52" not in visited_waypoints, "J52 (FUEL_STATION) should NOT be in tour"

    # Verify exactly 2 waypoints visited
    expected_visits = set(assigned_markets)
    assert visited_waypoints == expected_visits, \
        f"Tour should visit exactly assigned markets {expected_visits}, visited: {visited_waypoints}"


def test_tour_cache_never_stores_fuel_stations(temp_db_path, jv40_graph, scout4_ship_data):
    """
    Test that tour cache entries never contain fuel stations.

    This validates cache integrity: cached markets list should only contain
    waypoints with MARKETPLACE trait.
    """
    tsp = ORToolsTSP(jv40_graph, db_path=temp_db_path)

    assigned_markets = ["X1-JV40-A1", "X1-JV40-J53"]
    start = assigned_markets[0]
    waypoints = assigned_markets[1:]

    # Generate and cache tour
    tour = tsp.optimise_tour(
        waypoints=waypoints,
        start=start,
        ship_data=scout4_ship_data,
        return_to_start=True,
        use_cache=True,  # Enable caching
        algorithm="ortools",
    )

    assert tour is not None

    # Check database cache entry
    db = Database(temp_db_path)
    with db.connection() as conn:
        cursor = conn.cursor()
        cursor.execute("SELECT markets FROM tour_cache WHERE system = ?", ("X1-JV40",))
        row = cursor.fetchone()

    assert row is not None, "Tour not cached"

    cached_markets = json.loads(row["markets"])
    print(f"\nCached markets: {cached_markets}")

    # CRITICAL: Cached markets should NEVER contain fuel stations
    for market in cached_markets:
        waypoint_type = jv40_graph["waypoints"][market]["type"]
        traits = jv40_graph["waypoints"][market]["traits"]

        assert waypoint_type != "FUEL_STATION", \
            f"Cached market {market} is FUEL_STATION (should never be cached)"
        assert "MARKETPLACE" in traits, \
            f"Cached market {market} missing MARKETPLACE trait"


def test_stale_cache_with_fuel_station_causes_bug(temp_db_path, jv40_graph, scout4_ship_data):
    """
    Test demonstrating ROOT CAUSE: Stale cache with fuel station causes bug.

    This simulates the production bug scenario:
    1. Old tour optimization cached [J53, J52] (somehow J52 got included)
    2. Coordinator assigns [A1, J53] to Scout-4
    3. Tour cache lookup with [A1, J53] finds stale [J53, J52] entry
    4. Result: Scout visits J52 (fuel station) instead of A1 (assigned market)

    This test DEMONSTRATES THE BUG by manually creating stale cache entry.
    """
    db = Database(temp_db_path)

    # Simulate STALE cache entry with J52 (fuel station) incorrectly included
    # This represents old optimization that somehow included fuel station
    stale_markets = ["X1-JV40-J53", "X1-JV40-J52"]  # BUG: J52 is fuel station!
    stale_tour_order = ["X1-JV40-J53", "X1-JV40-J52", "X1-JV40-J53"]

    with db.transaction() as conn:
        db.save_tour_cache(
            conn,
            system="X1-JV40",
            markets=stale_markets,  # Stale entry with fuel station
            algorithm="ortools",
            tour_order=stale_tour_order,
            total_distance=100.0,
            start_waypoint=None,
        )

    # Coordinator assigns [A1, J53] to Scout-4
    assigned_markets = ["X1-JV40-A1", "X1-JV40-J53"]

    # Scout-4 queries cache with assigned markets
    tsp = ORToolsTSP(jv40_graph, db_path=temp_db_path)
    start = assigned_markets[0]
    waypoints = assigned_markets[1:]

    tour = tsp.optimise_tour(
        waypoints=waypoints,
        start=start,
        ship_data=scout4_ship_data,
        return_to_start=True,
        use_cache=True,  # Will use stale cache if keys match
        algorithm="ortools",
    )

    assert tour is not None

    # Extract visited waypoints
    visited = {start}
    for leg in tour["legs"]:
        if "goal" in leg:
            visited.add(leg["goal"])

    print(f"\nAssigned markets: {assigned_markets}")
    print(f"Cached markets (stale): {stale_markets}")
    print(f"Tour visits: {visited}")

    # BUG REPRODUCTION: If cache lookup is too permissive, tour might visit J52
    # Expected behavior: Cache miss (because A1 ≠ J52), fresh optimization
    # Buggy behavior: Cache hit (somehow), visits J52 instead of A1

    # CRITICAL: Tour must visit assigned markets, never fuel stations
    assert "X1-JV40-A1" in visited, "Tour must visit A1 (assigned market)"
    assert "X1-JV40-J53" in visited, "Tour must visit J53 (assigned market)"
    assert "X1-JV40-J52" not in visited, "Tour must NOT visit J52 (fuel station)"


def test_coordinator_market_list_matches_tour_input(jv40_graph):
    """
    Test that market list passed to tour optimizer matches coordinator extraction.

    This validates the data flow:
    1. Coordinator extracts markets from graph (filters fuel stations)
    2. Coordinator passes markets to tour optimizer
    3. Tour optimizer receives ONLY marketplace waypoints

    This test ensures fuel stations are filtered BEFORE tour optimization.
    """
    # Step 1: Coordinator extracts markets
    coordinator_markets = TourOptimizer.get_markets_from_graph(jv40_graph)

    # Step 2: Validate extracted markets
    assert "X1-JV40-A1" in coordinator_markets
    assert "X1-JV40-J53" in coordinator_markets
    assert "X1-JV40-J52" not in coordinator_markets

    # Step 3: Simulate passing to tour optimizer
    # In production, this is: optimizer.plan_tour(start, coordinator_markets, ...)
    tour_optimizer_input = coordinator_markets

    # Validate tour optimizer receives clean input
    for market in tour_optimizer_input:
        waypoint_type = jv40_graph["waypoints"][market]["type"]
        traits = jv40_graph["waypoints"][market]["traits"]

        assert waypoint_type != "FUEL_STATION", \
            f"Tour optimizer input contains fuel station: {market}"
        assert "MARKETPLACE" in traits, \
            f"Tour optimizer input contains non-marketplace waypoint: {market}"


if __name__ == "__main__":
    # Run tests manually for debugging
    import sys
    pytest.main([__file__, "-v", "-s"] + sys.argv[1:])
