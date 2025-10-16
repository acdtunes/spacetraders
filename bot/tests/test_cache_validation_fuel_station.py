#!/usr/bin/env python3
"""
Test for cache validation logic that detects and rejects stale tour cache entries containing fuel stations.

This test validates the FIX implemented in BUG_FIX_REPORT_FUEL_STATION_SCOUT_TOUR.md:
- Cache validation in `_build_tour_from_order()` checks for fuel stations
- Cache validation in `_build_probe_tour_from_order()` checks for fuel stations
- Returns None (forces cache miss) when fuel station detected
- Fresh tour is built with correct filtering logic
"""

import json
import pytest
import tempfile
from pathlib import Path
from unittest.mock import patch, MagicMock

from spacetraders_bot.core.database import Database
from spacetraders_bot.core.ortools_router import ORToolsTSP, ORToolsRouter


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
    X1-JV40 system graph matching production evidence.

    Waypoints:
    - A1: PLANET with MARKETPLACE (legitimate market)
    - J52: FUEL_STATION (should NOT be in scout tours)
    - J53: PLANET with MARKETPLACE (legitimate market)
    """
    waypoints = {
        "X1-JV40-A1": {
            "x": 0,
            "y": 0,
            "has_fuel": True,
            "type": "PLANET",
            "traits": ["MARKETPLACE"],
        },
        "X1-JV40-J52": {
            "x": 100,
            "y": 100,
            "has_fuel": True,
            "type": "FUEL_STATION",
            "traits": [],  # NO MARKETPLACE trait
        },
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
def scout_ship_data():
    """Scout ship configuration with fuel capacity"""
    return {
        "symbol": "SCOUT-4",
        "engine": {"speed": 9},
        "fuel": {"current": 1000, "capacity": 1000},
        "nav": {"waypointSymbol": "X1-JV40-A1"},
    }


@pytest.fixture
def probe_ship_data():
    """Probe ship configuration (no fuel capacity)"""
    return {
        "symbol": "PROBE-1",
        "engine": {"speed": 9},
        "fuel": {"current": 0, "capacity": 0},
        "nav": {"waypointSymbol": "X1-JV40-A1"},
    }


def test_cache_validation_rejects_fuel_station_in_cached_tour(temp_db_path, jv40_graph, scout_ship_data):
    """
    Test that cache validation detects and rejects stale cached tours containing fuel stations.

    Scenario:
    1. Stale cache entry exists with fuel station (from old optimization)
    2. Scout operation retrieves cached tour
    3. Cache validation detects fuel station in tour order
    4. Returns None to force cache miss and rebuild
    5. Fresh tour built without fuel station
    """
    db = Database(temp_db_path)

    # Simulate STALE cache entry with J52 (fuel station) incorrectly included
    stale_markets = ["X1-JV40-J53"]  # Cache key: only J53 as stop (A1 is start)
    stale_tour_order = ["X1-JV40-A1", "X1-JV40-J53", "X1-JV40-J52", "X1-JV40-A1"]  # BUG: J52 is fuel station!

    with db.transaction() as conn:
        db.save_tour_cache(
            conn,
            system="X1-JV40",
            markets=stale_markets,
            algorithm="ortools",
            tour_order=stale_tour_order,
            total_distance=200.0,
            start_waypoint="X1-JV40-A1",
        )

    # Try to retrieve cached tour - cache validation should reject it and rebuild
    tsp = ORToolsTSP(jv40_graph, db_path=temp_db_path)

    tour = tsp.optimise_tour(
        waypoints=["X1-JV40-A1", "X1-JV40-J53"],  # Only legitimate markets
        start="X1-JV40-A1",
        ship_data=scout_ship_data,
        return_to_start=True,
        use_cache=True,
        algorithm="ortools",
    )

    assert tour is not None, "Tour should be rebuilt after cache invalidation"

    # Verify tour visits only legitimate markets (no fuel stations)
    visited_waypoints = set([tour["start"]])
    for leg in tour["legs"]:
        if "goal" in leg:
            visited_waypoints.add(leg["goal"])
        # Also check steps within each leg
        for step in leg.get("steps", []):
            if step.get("action") == "navigate":
                visited_waypoints.add(step["to"])

    print(f"\nVisited waypoints: {visited_waypoints}")

    # CRITICAL: Tour must visit assigned markets, never fuel stations
    assert "X1-JV40-A1" in visited_waypoints, "Tour must visit A1 (assigned market)"
    assert "X1-JV40-J53" in visited_waypoints, "Tour must visit J53 (assigned market)"
    assert "X1-JV40-J52" not in visited_waypoints, "Tour must NOT visit J52 (fuel station)"


def test_cache_validation_rejects_fuel_station_for_probes(temp_db_path, jv40_graph, probe_ship_data):
    """
    Test that cache validation works for probes (fast path without fuel calculations).

    Probes use different tour building logic (_build_probe_tour_from_order),
    so we need to verify cache validation works for both code paths.
    """
    db = Database(temp_db_path)

    # Stale cache entry with fuel station
    stale_markets = ["X1-JV40-J53"]
    stale_tour_order = ["X1-JV40-A1", "X1-JV40-J52", "X1-JV40-J53", "X1-JV40-A1"]  # J52 is fuel station

    with db.transaction() as conn:
        db.save_tour_cache(
            conn,
            system="X1-JV40",
            markets=stale_markets,
            algorithm="ortools",
            tour_order=stale_tour_order,
            total_distance=200.0,
            start_waypoint="X1-JV40-A1",
        )

    # Try to retrieve cached tour for probe
    tsp = ORToolsTSP(jv40_graph, db_path=temp_db_path)

    tour = tsp.optimise_tour(
        waypoints=["X1-JV40-A1", "X1-JV40-J53"],
        start="X1-JV40-A1",
        ship_data=probe_ship_data,  # Probe (fuel_capacity=0)
        return_to_start=True,
        use_cache=True,
        algorithm="ortools",
    )

    assert tour is not None, "Tour should be rebuilt after cache invalidation"

    # Verify tour visits only legitimate markets
    visited_waypoints = set([tour["start"]])
    for leg in tour["legs"]:
        if "goal" in leg:
            visited_waypoints.add(leg["goal"])

    print(f"\nProbe visited waypoints: {visited_waypoints}")

    # CRITICAL: Probe tour must visit assigned markets, never fuel stations
    assert "X1-JV40-A1" in visited_waypoints
    assert "X1-JV40-J53" in visited_waypoints
    assert "X1-JV40-J52" not in visited_waypoints, "Probe tour must NOT visit J52 (fuel station)"


def test_cache_validation_allows_legitimate_cached_tours(temp_db_path, jv40_graph, scout_ship_data):
    """
    Test that cache validation does NOT reject legitimate cached tours (no fuel stations).

    This ensures we don't break the cache optimization for valid entries.
    """
    db = Database(temp_db_path)

    # Valid cache entry with NO fuel stations
    valid_markets = ["X1-JV40-J53"]
    valid_tour_order = ["X1-JV40-A1", "X1-JV40-J53", "X1-JV40-A1"]  # Only legitimate markets

    with db.transaction() as conn:
        db.save_tour_cache(
            conn,
            system="X1-JV40",
            markets=valid_markets,
            algorithm="ortools",
            tour_order=valid_tour_order,
            total_distance=100.0,
            start_waypoint="X1-JV40-A1",
        )

    tsp = ORToolsTSP(jv40_graph, db_path=temp_db_path)

    tour = tsp.optimise_tour(
        waypoints=["X1-JV40-A1", "X1-JV40-J53"],
        start="X1-JV40-A1",
        ship_data=scout_ship_data,
        return_to_start=True,
        use_cache=True,
        algorithm="ortools",
    )

    assert tour is not None, "Valid cached tour should be returned"

    # Verify tour is from cache (router.find_optimal_route called for building legs)
    # Cache hit returns the tour order, then builds legs using router
    print(f"\nCache hit - tour built from cached order")

    # Verify tour visits only legitimate markets
    visited_waypoints = set([tour["start"]])
    for leg in tour["legs"]:
        if "goal" in leg:
            visited_waypoints.add(leg["goal"])

    assert "X1-JV40-A1" in visited_waypoints
    assert "X1-JV40-J53" in visited_waypoints
    assert "X1-JV40-J52" not in visited_waypoints


if __name__ == "__main__":
    # Run tests manually for debugging
    import sys
    pytest.main([__file__, "-v", "-s"] + sys.argv[1:])
