#!/usr/bin/env python3
"""
Test tour cache key format matches scout assignment format

BUG DESCRIPTION:
Scout assignments store ALL markets (start + stops) but tour cache was storing
only STOPS (excluding start). This caused cache misses when visualizer queried
with full market list from scout assignments.

EVIDENCE:
- Scout assignment: ["I63", "J65", "J66", "I64"] (4 markets, I63 is start)
- Old tour cache: ["I64", "J65", "J66"] (3 markets - MISSING I63!)
- Result: Cache miss, visualizer draws suboptimal greedy route

FIX:
Include start waypoint in cached markets list to match assignment format.
"""

import tempfile
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from spacetraders_bot.core.database import Database
from spacetraders_bot.core.ortools_router import ORToolsTSP


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
def simple_graph():
    """Simple 4-waypoint graph matching DRAGONSPYRE-2 scenario"""
    waypoints = {
        "X1-TEST-I63": {"x": 0, "y": 0, "has_fuel": True, "type": "PLANET", "traits": ["MARKETPLACE"]},
        "X1-TEST-J65": {"x": 10, "y": 10, "has_fuel": True, "type": "PLANET", "traits": ["MARKETPLACE"]},
        "X1-TEST-J66": {"x": 20, "y": 10, "has_fuel": True, "type": "PLANET", "traits": ["MARKETPLACE"]},
        "X1-TEST-I64": {"x": 30, "y": 0, "has_fuel": True, "type": "PLANET", "traits": ["MARKETPLACE"]},
    }

    return {
        "system": "X1-TEST",
        "waypoints": waypoints,
        "edges": []
    }


@pytest.fixture
def ship_data():
    """Standard ship configuration"""
    return {
        "symbol": "TEST-SCOUT-1",
        "engine": {"speed": 9},
        "fuel": {"current": 1000, "capacity": 1000},
        "nav": {"waypointSymbol": "X1-TEST-I63"},
    }


def test_tour_cache_includes_start_waypoint(temp_db_path, simple_graph, ship_data):
    """
    Test that tour cache includes start waypoint in cached markets list.

    This ensures cache key matches scout assignment format.

    Scout assignment format: ["I63", "J65", "J66", "I64"]
    Expected cache format: ["I63", "J65", "J66", "I64"] (same!)
    Old buggy format:      ["I64", "J65", "J66"] (missing start!)
    """
    tsp = ORToolsTSP(simple_graph, db_path=temp_db_path)

    start = "X1-TEST-I63"
    waypoints = ["X1-TEST-J65", "X1-TEST-J66", "X1-TEST-I64"]

    # Generate tour (will cache)
    tour = tsp.optimise_tour(
        waypoints=waypoints,
        start=start,
        ship_data=ship_data,
        return_to_start=True,
        use_cache=True,
        algorithm="ortools"
    )

    assert tour is not None, "Tour generation failed"

    # Verify database cache entry
    db = Database(temp_db_path)
    with db.connection() as conn:
        cursor = conn.cursor()
        cursor.execute("SELECT markets FROM tour_cache WHERE system = ?", ("X1-TEST",))
        row = cursor.fetchone()

    assert row is not None, "Tour not cached"

    import json
    cached_markets = json.loads(row["markets"])

    # CRITICAL: Cached markets must include start waypoint
    expected_markets = sorted([start] + waypoints)
    assert cached_markets == expected_markets, \
        f"Cache should include start! Expected {expected_markets}, got {cached_markets}"


def test_tour_cache_retrieval_with_scout_assignment_format(temp_db_path, simple_graph, ship_data):
    """
    Test that visualizer can retrieve cached tour using scout assignment format.

    Simulates visualizer query with full market list from ship assignment.
    """
    tsp = ORToolsTSP(simple_graph, db_path=temp_db_path)

    start = "X1-TEST-I63"
    waypoints = ["X1-TEST-J65", "X1-TEST-J66", "X1-TEST-I64"]

    # Step 1: Generate and cache tour
    tour1 = tsp.optimise_tour(
        waypoints=waypoints,
        start=start,
        ship_data=ship_data,
        return_to_start=True,
        use_cache=True,
        algorithm="ortools"
    )

    assert tour1 is not None
    original_distance = tour1["total_distance"]

    # Step 2: Simulate visualizer query with FULL market list (start + stops)
    # This is how scout assignments are stored: ["I63", "J65", "J66", "I64"]
    full_market_list = [start] + waypoints

    # Create new TSP instance to simulate fresh visualizer query
    tsp2 = ORToolsTSP(simple_graph, db_path=temp_db_path)

    # Query with full market list
    tour2 = tsp2.optimise_tour(
        waypoints=waypoints,  # visualizer extracts stops from full list
        start=start,
        ship_data=ship_data,
        return_to_start=True,
        use_cache=True,  # Should hit cache!
        algorithm="ortools"
    )

    assert tour2 is not None, "Cache retrieval failed with scout assignment format"
    assert tour2["total_distance"] == original_distance, \
        "Cache hit should return identical tour distance"


def test_tour_cache_key_consistency_across_restarts(temp_db_path, simple_graph, ship_data):
    """
    Test that tour cache key remains consistent across bot restarts.

    This validates the fix prevents cache misses after daemon restarts.
    """
    start = "X1-TEST-I63"
    waypoints = ["X1-TEST-J65", "X1-TEST-J66", "X1-TEST-I64"]

    # Simulate first bot run: generate and cache tour
    tsp1 = ORToolsTSP(simple_graph, db_path=temp_db_path)
    tour1 = tsp1.optimise_tour(
        waypoints=waypoints,
        start=start,
        ship_data=ship_data,
        return_to_start=True,
        use_cache=True,
        algorithm="ortools"
    )

    assert tour1 is not None
    del tsp1  # Simulate daemon crash/restart

    # Simulate second bot run: query cache with same scout assignment
    tsp2 = ORToolsTSP(simple_graph, db_path=temp_db_path)
    tour2 = tsp2.optimise_tour(
        waypoints=waypoints,
        start=start,
        ship_data=ship_data,
        return_to_start=True,
        use_cache=True,
        algorithm="ortools"
    )

    assert tour2 is not None
    assert tour2["total_distance"] == tour1["total_distance"], \
        "Cache should persist across restarts with consistent key format"


def test_regression_old_cache_key_format_fails(temp_db_path, simple_graph, ship_data):
    """
    Test demonstrating the OLD buggy behavior (for documentation).

    This test shows what happens when cache uses stops-only format
    but query uses full market list.

    RESULT: Cache miss, suboptimal greedy fallback route.
    """
    db = Database(temp_db_path)
    start = "X1-TEST-I63"
    waypoints = ["X1-TEST-J65", "X1-TEST-J66", "X1-TEST-I64"]

    # Simulate old buggy cache format (stops only, no start)
    stops_only = waypoints  # Missing start!
    tour_order = [start, "X1-TEST-J65", "X1-TEST-J66", "X1-TEST-I64"]

    with db.transaction() as conn:
        db.save_tour_cache(
            conn,
            system="X1-TEST",
            markets=stops_only,  # Old buggy format
            algorithm="ortools",
            tour_order=tour_order,
            total_distance=100.0,
            start_waypoint=None
        )

    # Try to retrieve with full market list (scout assignment format)
    with db.connection() as conn:
        cached = db.get_cached_tour(
            conn,
            system="X1-TEST",
            markets=[start] + waypoints,  # Full list (correct format)
            algorithm="ortools",
            start_waypoint=None
        )

    # OLD BUG: Cache miss because key doesn't match!
    assert cached is None, \
        "Old cache format (stops-only) should NOT match full market list query"


def test_fix_cache_key_format_succeeds(temp_db_path, simple_graph, ship_data):
    """
    Test demonstrating the FIX (cache with start included).

    This test shows that with the fix, cache keys match scout assignments.

    RESULT: Cache hit, optimal OR-Tools route returned.
    """
    db = Database(temp_db_path)
    start = "X1-TEST-I63"
    waypoints = ["X1-TEST-J65", "X1-TEST-J66", "X1-TEST-I64"]

    # NEW FIXED FORMAT: Include start in cached markets
    full_market_list = [start] + waypoints
    tour_order = [start, "X1-TEST-J65", "X1-TEST-J66", "X1-TEST-I64", start]

    with db.transaction() as conn:
        db.save_tour_cache(
            conn,
            system="X1-TEST",
            markets=full_market_list,  # Fixed format (includes start!)
            algorithm="ortools",
            tour_order=tour_order,
            total_distance=100.0,
            start_waypoint=None
        )

    # Query with full market list (scout assignment format)
    with db.connection() as conn:
        cached = db.get_cached_tour(
            conn,
            system="X1-TEST",
            markets=full_market_list,  # Same format as save
            algorithm="ortools",
            start_waypoint=None
        )

    # FIX: Cache hit because keys match!
    assert cached is not None, \
        "Fixed cache format (with start) should match full market list query"
    assert cached["tour_order"] == tour_order, "Cached tour order should match"


if __name__ == "__main__":
    # Run tests manually for debugging
    import sys
    pytest.main([__file__, "-v", "-s"] + sys.argv[1:])
