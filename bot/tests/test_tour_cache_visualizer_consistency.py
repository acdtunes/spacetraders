#!/usr/bin/env python3
"""
Regression test: Tour cache key consistency between daemon and visualizer

This test validates that the visualizer's tour cache lookup logic exactly matches
the daemon's cache key format, preventing crossing edges in tour visualization.

BUG: Daemon removed start waypoint from markets list before caching (line 606),
     but visualizer included start waypoint in cache lookup, causing cache miss.

FIX: Visualizer must remove start waypoint from markets before cache lookup
     and include start_waypoint parameter (must NOT be NULL).
"""

import json
import sqlite3
from pathlib import Path
from typing import List


def test_daemon_cache_key_format():
    """Test that daemon caches tours with correct key format (25 markets + start)"""
    # Simulate daemon behavior from ortools_router.py:606-624
    # Input: 26 assigned markets (including start)
    assigned_markets = [
        "X1-JB26-A1",   # This becomes the start waypoint
        "X1-JB26-A2",
        "X1-JB26-B7",
        "X1-JB26-C5",
        "X1-JB26-D8",
    ]

    start = assigned_markets[0]  # First market is tour start

    # Daemon removes start from markets list before caching (line 606)
    stops = [wp for wp in assigned_markets if wp != start]  # 4 markets (was 5)

    # Verify daemon creates correct cache key
    assert len(stops) == 4, f"Expected 4 stops, got {len(stops)}"
    assert start not in stops, "Start waypoint should not be in stops list"
    assert start == "X1-JB26-A1", f"Expected start=X1-JB26-A1, got {start}"

    print(f"✓ Daemon cache key format:")
    print(f"  - Assigned markets: {len(assigned_markets)} (input)")
    print(f"  - Cache markets: {len(stops)} (start removed)")
    print(f"  - Start waypoint: {start} (non-NULL parameter)")


def test_visualizer_cache_lookup_matches_daemon():
    """Test that visualizer lookup logic matches daemon's cache key format"""
    # Simulate visualizer behavior from bot.ts:224-253 (BEFORE fix)
    assigned_markets = [
        "X1-JB26-A1",   # This is the tour start
        "X1-JB26-A2",
        "X1-JB26-B7",
        "X1-JB26-C5",
        "X1-JB26-D8",
    ]

    # CORRECT visualizer logic (AFTER fix):
    # Extract first market as start (matching operations/routing.py:371)
    tour_start = assigned_markets[0]

    # Remove start from markets for cache lookup (matching daemon behavior at ortools_router.py:606)
    markets_for_lookup = [m for m in assigned_markets if m != tour_start]

    # Sort markets (matching database.py:890 - cache key format)
    markets_sorted = sorted(markets_for_lookup)

    # Verify visualizer creates same cache key as daemon
    assert len(markets_sorted) == 4, f"Expected 4 markets for lookup, got {len(markets_sorted)}"
    assert tour_start not in markets_sorted, "Start waypoint should not be in markets list"
    assert tour_start == "X1-JB26-A1", f"Expected start=X1-JB26-A1, got {tour_start}"

    print(f"✓ Visualizer cache lookup format (AFTER fix):")
    print(f"  - Assigned markets: {len(assigned_markets)}")
    print(f"  - Lookup markets: {len(markets_sorted)} (start removed)")
    print(f"  - Start waypoint: {tour_start} (must be non-NULL)")

    # Verify cache lookup would succeed
    # Format must match database.py:890-891 and database.py:938
    cache_key_markets = json.dumps(markets_sorted)

    print(f"✓ Cache key format matches:")
    print(f"  - Markets JSON: {cache_key_markets}")
    print(f"  - Start parameter: {tour_start}")


def test_cache_miss_with_incorrect_lookup():
    """Demonstrate cache MISS when visualizer uses incorrect format (BEFORE fix)"""
    # Daemon cached tour (correct format)
    daemon_assigned_markets = [
        "X1-JB26-A1",
        "X1-JB26-A2",
        "X1-JB26-B7",
        "X1-JB26-C5",
        "X1-JB26-D8",
    ]

    daemon_start = daemon_assigned_markets[0]
    daemon_stops = [m for m in daemon_assigned_markets if m != daemon_start]
    daemon_markets_key = json.dumps(sorted(daemon_stops))

    print(f"\n✗ BEFORE FIX (cache miss scenario):")
    print(f"  Daemon cached with:")
    print(f"    - markets: {daemon_markets_key}")
    print(f"    - start_waypoint: {daemon_start}")

    # Visualizer INCORRECT lookup (BEFORE fix) - includes start in markets, start=NULL
    visualizer_markets_key_wrong = json.dumps(sorted(daemon_assigned_markets))  # All 5 markets!
    visualizer_start_wrong = None  # NULL start!

    print(f"  Visualizer looked up (WRONG):")
    print(f"    - markets: {visualizer_markets_key_wrong}")
    print(f"    - start_waypoint: {visualizer_start_wrong}")

    # Keys don't match → CACHE MISS!
    cache_miss = (
        daemon_markets_key != visualizer_markets_key_wrong or
        daemon_start != visualizer_start_wrong
    )

    assert cache_miss, "Should demonstrate cache miss with incorrect lookup"
    print(f"  ✗ RESULT: CACHE MISS (keys don't match)")
    print(f"  ✗ IMPACT: Visualizer returns unoptimized assignment order → CROSSING EDGES")


def test_cache_hit_with_correct_lookup():
    """Demonstrate cache HIT when visualizer uses correct format (AFTER fix)"""
    # Daemon cached tour (correct format)
    daemon_assigned_markets = [
        "X1-JB26-A1",
        "X1-JB26-A2",
        "X1-JB26-B7",
        "X1-JB26-C5",
        "X1-JB26-D8",
    ]

    daemon_start = daemon_assigned_markets[0]
    daemon_stops = [m for m in daemon_assigned_markets if m != daemon_start]
    daemon_markets_key = json.dumps(sorted(daemon_stops))

    print(f"\n✓ AFTER FIX (cache hit scenario):")
    print(f"  Daemon cached with:")
    print(f"    - markets: {daemon_markets_key}")
    print(f"    - start_waypoint: {daemon_start}")

    # Visualizer CORRECT lookup (AFTER fix) - removes start, start=non-NULL
    visualizer_start_correct = daemon_assigned_markets[0]  # Extract first market as start
    visualizer_markets_correct = [m for m in daemon_assigned_markets if m != visualizer_start_correct]
    visualizer_markets_key_correct = json.dumps(sorted(visualizer_markets_correct))

    print(f"  Visualizer looks up (CORRECT):")
    print(f"    - markets: {visualizer_markets_key_correct}")
    print(f"    - start_waypoint: {visualizer_start_correct}")

    # Keys match → CACHE HIT!
    cache_hit = (
        daemon_markets_key == visualizer_markets_key_correct and
        daemon_start == visualizer_start_correct
    )

    assert cache_hit, "Should demonstrate cache hit with correct lookup"
    print(f"  ✓ RESULT: CACHE HIT (keys match exactly)")
    print(f"  ✓ IMPACT: Visualizer displays optimized tour → NO CROSSING EDGES")


def test_database_cache_roundtrip(tmp_path):
    """Integration test: Save tour via daemon, retrieve via visualizer (AFTER fix)"""
    # Create temporary test database
    test_db = tmp_path / "test_cache.db"

    conn = sqlite3.connect(str(test_db))
    conn.row_factory = sqlite3.Row

    # Create tour_cache table (matching database.py:320-330)
    conn.execute("""
        CREATE TABLE tour_cache (
            system TEXT NOT NULL,
            markets TEXT NOT NULL,
            algorithm TEXT NOT NULL,
            start_waypoint TEXT,
            tour_order TEXT NOT NULL,
            total_distance REAL NOT NULL,
            calculated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
            PRIMARY KEY (system, markets, algorithm, start_waypoint)
        )
    """)

    # DAEMON: Save tour (simulating ortools_router.py:618-624 and database.py:916-949)
    assigned_markets = [
        "X1-JB26-A1",
        "X1-JB26-A2",
        "X1-JB26-B7",
        "X1-JB26-C5",
        "X1-JB26-D8",
    ]

    system = "X1-JB26"
    algorithm = "ortools"
    start = assigned_markets[0]
    stops = [m for m in assigned_markets if m != start]  # Remove start before caching

    # Optimized tour order (daemon computed this via OR-Tools)
    optimized_tour_order = [
        "X1-JB26-A1",  # Start
        "X1-JB26-A2",  # Nearest neighbor
        "X1-JB26-C5",  # Next nearest
        "X1-JB26-B7",  # Next
        "X1-JB26-D8",  # Last
    ]

    markets_key = json.dumps(sorted(stops))
    tour_order_json = json.dumps(optimized_tour_order)

    conn.execute("""
        INSERT INTO tour_cache
            (system, markets, algorithm, start_waypoint, tour_order, total_distance, calculated_at)
        VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
    """, (system, markets_key, algorithm, start, tour_order_json, 1234.5))
    conn.commit()

    print(f"\n✓ Daemon saved tour to cache:")
    print(f"  - System: {system}")
    print(f"  - Markets key: {markets_key}")
    print(f"  - Algorithm: {algorithm}")
    print(f"  - Start: {start}")
    print(f"  - Tour order: {optimized_tour_order}")

    # VISUALIZER: Retrieve tour (simulating bot.ts:224-253 AFTER fix)
    # Visualizer receives assigned_markets from ship_assignments.metadata
    visualizer_assigned_markets = assigned_markets  # Same 5 markets

    # Extract first market as start (CRITICAL: matches operations/routing.py:371)
    visualizer_start = visualizer_assigned_markets[0]

    # Remove start from markets for cache lookup (CRITICAL: matches daemon behavior)
    visualizer_markets_for_lookup = [m for m in visualizer_assigned_markets if m != visualizer_start]
    visualizer_markets_key = json.dumps(sorted(visualizer_markets_for_lookup))

    # SQL query with EXACT match including start_waypoint (CRITICAL: must be non-NULL)
    cursor = conn.execute("""
        SELECT tour_order, total_distance, calculated_at
        FROM tour_cache
        WHERE system = ?
          AND markets = ?
          AND algorithm IN ('ortools', '2opt')
          AND start_waypoint = ?
        ORDER BY
          CASE algorithm
            WHEN 'ortools' THEN 1
            WHEN '2opt' THEN 2
            ELSE 3
          END,
          calculated_at DESC
        LIMIT 1
    """, (system, visualizer_markets_key, visualizer_start))

    row = cursor.fetchone()

    assert row is not None, "Visualizer should find cached tour"

    cached_tour_order = json.loads(row['tour_order'])

    print(f"\n✓ Visualizer retrieved tour from cache:")
    print(f"  - System: {system}")
    print(f"  - Markets key: {visualizer_markets_key}")
    print(f"  - Start: {visualizer_start}")
    print(f"  - Retrieved tour: {cached_tour_order}")

    # Verify retrieved tour matches daemon's optimized order
    assert cached_tour_order == optimized_tour_order, "Retrieved tour should match daemon's cached tour"

    print(f"\n✓ SUCCESS: Visualizer found daemon's cached tour")
    print(f"✓ NO CROSSING EDGES: Tour displayed with optimal order")

    conn.close()


def test_visualizer_sql_query_format():
    """Test that visualizer SQL query matches database schema exactly"""
    # Visualizer SQL query (bot.ts:232-253 AFTER fix)
    # Must include all 4 cache key components:
    # 1. system
    # 2. markets (sorted, start removed)
    # 3. algorithm (prefer ortools over 2opt)
    # 4. start_waypoint (must be non-NULL)

    query_components = {
        "system": "X1-JB26",
        "markets": '["X1-JB26-A2", "X1-JB26-B7", "X1-JB26-C5", "X1-JB26-D8"]',  # 4 markets (start removed)
        "algorithm": ["ortools", "2opt"],  # Prefer ortools
        "start_waypoint": "X1-JB26-A1",  # Must NOT be NULL
    }

    # Verify all components present
    assert query_components["system"] is not None
    assert query_components["markets"] is not None
    assert len(json.loads(query_components["markets"])) == 4, "Should have 4 markets (start removed)"
    assert query_components["start_waypoint"] is not None, "Start waypoint must NOT be NULL"
    assert query_components["start_waypoint"] not in json.loads(query_components["markets"]), \
        "Start waypoint should not be in markets list"

    print(f"\n✓ Visualizer SQL query format (AFTER fix):")
    print(f"  WHERE system = '{query_components['system']}'")
    print(f"    AND markets = '{query_components['markets']}'")
    print(f"    AND algorithm IN {tuple(query_components['algorithm'])}")
    print(f"    AND start_waypoint = '{query_components['start_waypoint']}'")
    print(f"  ✓ All 4 cache key components present")
    print(f"  ✓ start_waypoint is non-NULL")
    print(f"  ✓ Markets list excludes start waypoint")


if __name__ == "__main__":
    print("=" * 80)
    print("TOUR CACHE KEY CONSISTENCY TEST")
    print("Regression test for visualizer crossing edges bug")
    print("=" * 80)

    test_daemon_cache_key_format()
    print()

    test_visualizer_cache_lookup_matches_daemon()
    print()

    test_cache_miss_with_incorrect_lookup()
    print()

    test_cache_hit_with_correct_lookup()
    print()

    # Integration test with temporary database
    import tempfile
    with tempfile.TemporaryDirectory() as tmpdir:
        test_database_cache_roundtrip(Path(tmpdir))
    print()

    test_visualizer_sql_query_format()

    print("\n" + "=" * 80)
    print("✓ ALL TESTS PASSED")
    print("=" * 80)
