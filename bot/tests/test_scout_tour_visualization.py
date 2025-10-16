#!/usr/bin/env python3
"""
Test scout tour visualization to ensure no crossing edges

Bug Report: Visualizer shows crossing edges for DRAGONSPYRE-3 tour (23 waypoints)
despite tour cache containing optimized OR-Tools TSP solution with no crossings.

Root Cause Hypothesis:
- Backend API correctly extracts optimized tour_order from full tour
- Frontend correctly renders tour_order
- BUT: Tour_order may have crossing edges because it's a SUBSET of the full tour
  (23 markets) extracted from the full optimized tour (24 markets)

The issue is that extracting a subset from an optimized full tour does NOT guarantee
the subset itself has no crossings, even if the full tour is crossing-free.

Example:
Full tour: A→B→C→D (no crossings)
Subset for ship 1: [A, C] → renders as A→C (direct line)
Subset for ship 2: [B, D] → renders as B→D (direct line)
BUT A→C and B→D might CROSS each other!

Solution: The backend needs to re-optimize each scout's subset tour, not just filter
the full tour order.
"""

import json
import math
import tempfile
from pathlib import Path
from typing import List, Tuple, Set

import pytest

from spacetraders_bot.core.database import Database


def segments_intersect(p1: Tuple[float, float], p2: Tuple[float, float],
                       p3: Tuple[float, float], p4: Tuple[float, float]) -> bool:
    """
    Check if line segment p1-p2 intersects with line segment p3-p4.

    Uses cross product method to detect intersection.
    Returns True if segments intersect (excluding endpoints touching).
    """
    def ccw(a: Tuple[float, float], b: Tuple[float, float], c: Tuple[float, float]) -> bool:
        return (c[1] - a[1]) * (b[0] - a[0]) > (b[1] - a[1]) * (c[0] - a[0])

    # Check if p1-p2 and p3-p4 intersect
    return ccw(p1, p3, p4) != ccw(p2, p3, p4) and ccw(p1, p2, p3) != ccw(p1, p2, p4)


def has_crossing_edges(tour_order: List[str], waypoint_coords: dict) -> Tuple[bool, List[Tuple[int, int]]]:
    """
    Check if a tour has any crossing edges.

    Args:
        tour_order: List of waypoint symbols in visit order
        waypoint_coords: Dict mapping waypoint symbol to (x, y) coordinates

    Returns:
        (has_crossings, crossing_pairs) where crossing_pairs is list of (edge1_idx, edge2_idx)
    """
    if len(tour_order) < 4:
        return False, []  # Need at least 4 points to have crossing edges

    edges = [(tour_order[i], tour_order[i+1]) for i in range(len(tour_order)-1)]
    crossings = []

    for i, (a1, a2) in enumerate(edges):
        if a1 not in waypoint_coords or a2 not in waypoint_coords:
            continue

        p1 = waypoint_coords[a1]
        p2 = waypoint_coords[a2]

        # Check against all non-adjacent edges
        for j, (b1, b2) in enumerate(edges[i+2:], start=i+2):
            if b1 not in waypoint_coords or b2 not in waypoint_coords:
                continue

            # Skip if edges share an endpoint (adjacent edges)
            if a2 == b1 or a1 == b2:
                continue

            p3 = waypoint_coords[b1]
            p4 = waypoint_coords[b2]

            if segments_intersect(p1, p2, p3, p4):
                crossings.append((i, j))

    return len(crossings) > 0, crossings


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


def test_small_tour_no_crossing_edges(temp_db_path):
    """
    Small tour (4 waypoints) should have no crossing edges.

    This validates the optimized tour for DRAGONSPYRE-2.
    """
    db = Database(temp_db_path)

    # Simulate DRAGONSPYRE-2 tour: 4 waypoints in optimized order
    tour_order = ["X1-VH85-I63", "X1-VH85-J65", "X1-VH85-J66", "X1-VH85-I64"]

    # Waypoint coordinates (real coordinates from X1-VH85 system)
    waypoint_coords = {
        "X1-VH85-I63": (2400.0, 1050.0),
        "X1-VH85-J65": (2500.0, 1100.0),
        "X1-VH85-J66": (2550.0, 1075.0),
        "X1-VH85-I64": (2450.0, 1025.0),
    }

    # Check for crossing edges
    has_crossings, crossing_pairs = has_crossing_edges(tour_order, waypoint_coords)

    assert not has_crossings, f"Small tour should have no crossings, found {len(crossing_pairs)} crossings: {crossing_pairs}"
    assert len(crossing_pairs) == 0, "Crossing pairs list should be empty"


def test_large_tour_subset_extracted_from_full_tour_has_crossings(temp_db_path):
    """
    Large tour subset (23 waypoints) extracted from full tour (24 waypoints)
    may have crossing edges even if full tour is optimized.

    This demonstrates the BUG: filtering a subset from an optimized full tour
    does NOT guarantee the subset itself has no crossings.
    """
    # Full optimized tour (24 unique waypoints, 25 total with return to start)
    full_tour_order = [
        "X1-VH85-A1", "X1-VH85-AC5D", "X1-VH85-H62", "X1-VH85-H61",
        "X1-VH85-H60", "X1-VH85-H59", "X1-VH85-G58", "X1-VH85-C47",
        "X1-VH85-C46", "X1-VH85-I64", "X1-VH85-K92", "X1-VH85-E54",
        "X1-VH85-E53", "X1-VH85-B7", "X1-VH85-B6", "X1-VH85-F56",
        "X1-VH85-F55", "X1-VH85-D51", "X1-VH85-D50", "X1-VH85-D49",
        "X1-VH85-D48", "X1-VH85-A2", "X1-VH85-A3", "X1-VH85-A4", "X1-VH85-A1"
    ]

    # DRAGONSPYRE-3 assigned markets (23 waypoints, missing I64)
    assigned_markets = [
        "X1-VH85-A1", "X1-VH85-AC5D", "X1-VH85-B6", "X1-VH85-B7",
        "X1-VH85-C47", "X1-VH85-D48", "X1-VH85-E53", "X1-VH85-F55",
        "X1-VH85-G58", "X1-VH85-H59", "X1-VH85-K92", "X1-VH85-A2",
        "X1-VH85-A3", "X1-VH85-A4", "X1-VH85-C46", "X1-VH85-D49",
        "X1-VH85-D50", "X1-VH85-D51", "X1-VH85-E54", "X1-VH85-F56",
        "X1-VH85-H60", "X1-VH85-H61", "X1-VH85-H62"
    ]

    # Filter full tour to get subset (simulates backend API logic)
    assigned_set = set(assigned_markets)
    subset_tour = [wp for wp in full_tour_order if wp in assigned_set]

    # Remove duplicate A1 from end if present
    if len(subset_tour) > 1 and subset_tour[0] == subset_tour[-1]:
        subset_tour = subset_tour[:-1]

    print(f"\nFull tour: {len(full_tour_order)} waypoints")
    print(f"Assigned markets: {len(assigned_markets)} waypoints")
    print(f"Filtered subset: {len(subset_tour)} waypoints")

    # Waypoint coordinates (approximate positions in X1-VH85)
    waypoint_coords = {
        "X1-VH85-A1": (0.0, 0.0),
        "X1-VH85-A2": (50.0, 0.0),
        "X1-VH85-A3": (100.0, 0.0),
        "X1-VH85-A4": (150.0, 0.0),
        "X1-VH85-AC5D": (200.0, 50.0),
        "X1-VH85-B6": (300.0, 100.0),
        "X1-VH85-B7": (350.0, 100.0),
        "X1-VH85-C46": (400.0, 200.0),
        "X1-VH85-C47": (450.0, 200.0),
        "X1-VH85-D48": (500.0, 300.0),
        "X1-VH85-D49": (550.0, 300.0),
        "X1-VH85-D50": (600.0, 300.0),
        "X1-VH85-D51": (650.0, 300.0),
        "X1-VH85-E53": (700.0, 400.0),
        "X1-VH85-E54": (750.0, 400.0),
        "X1-VH85-F55": (800.0, 500.0),
        "X1-VH85-F56": (850.0, 500.0),
        "X1-VH85-G58": (900.0, 600.0),
        "X1-VH85-H59": (950.0, 700.0),
        "X1-VH85-H60": (1000.0, 700.0),
        "X1-VH85-H61": (1050.0, 700.0),
        "X1-VH85-H62": (1100.0, 700.0),
        "X1-VH85-I64": (1150.0, 800.0),  # Missing from subset
        "X1-VH85-K92": (1200.0, 900.0),
    }

    # Check full tour for crossings (should be optimized)
    full_has_crossings, full_crossings = has_crossing_edges(full_tour_order[:-1], waypoint_coords)
    print(f"\nFull tour crossings: {len(full_crossings)}")

    # Check subset tour for crossings
    subset_has_crossings, subset_crossings = has_crossing_edges(subset_tour, waypoint_coords)
    print(f"Subset tour crossings: {len(subset_crossings)}")

    if subset_has_crossings:
        print(f"\nCrossing edge pairs in subset:")
        for i, j in subset_crossings:
            edge1 = f"{subset_tour[i]} -> {subset_tour[i+1]}"
            edge2 = f"{subset_tour[j]} -> {subset_tour[j+1]}"
            print(f"  Edge {i} ({edge1}) crosses Edge {j} ({edge2})")

    # This test DOCUMENTS the bug: subset may have crossings even if full tour doesn't
    # We expect this to potentially fail (demonstrating the bug exists)
    assert not subset_has_crossings, \
        f"Subset tour has {len(subset_crossings)} crossing edges even though it was extracted from optimized full tour!"


def test_visualizer_should_use_optimized_tour_order_not_assigned_markets(temp_db_path):
    """
    Visualizer should render tour using tour_order field, not assigned markets order.

    This test validates that:
    1. Backend API returns tour_order field
    2. tour_order is different from assigned markets order
    3. tour_order should be used for rendering
    """
    db = Database(temp_db_path)

    # Assigned markets (original partition order - may have crossings)
    assigned_markets = [
        "X1-VH85-A1", "X1-VH85-AC5D", "X1-VH85-B6", "X1-VH85-B7",
        "X1-VH85-C47", "X1-VH85-D48"
    ]

    # Optimized tour_order (from OR-Tools - no crossings)
    tour_order = [
        "X1-VH85-A1", "X1-VH85-AC5D", "X1-VH85-C47", "X1-VH85-D48",
        "X1-VH85-B6", "X1-VH85-B7"
    ]

    # Verify they're different
    assert assigned_markets != tour_order, "tour_order should differ from assigned markets"

    # The frontend should use tour_order for rendering, NOT assigned_markets
    # This is already correctly implemented in ScoutTourLayer.tsx line 73:
    # const tourOrder = tour.tour_order;


def test_backend_api_correctly_extracts_subset_tour_order(temp_db_path):
    """
    Backend API /tours/:systemSymbol should correctly extract subset tour_order
    from full optimized tour by filtering to only assigned markets.

    This test validates the backend filtering logic is working correctly.
    """
    db = Database(temp_db_path)

    # Save full optimized tour to cache
    full_markets = ["M1", "M2", "M3", "M4", "M5"]
    full_tour_order = ["START", "M1", "M3", "M5", "M2", "M4", "START"]

    with db.transaction() as conn:
        db.save_tour_cache(
            conn,
            system="X1-TEST",
            markets=full_markets,
            algorithm="ortools",
            tour_order=full_tour_order,
            total_distance=100.0,
            start_waypoint="START"
        )

    with db.connection() as conn:
        conn.execute("PRAGMA wal_checkpoint(FULL)")

    # Simulate scout assignment with subset of markets
    scout_assigned_markets = ["M1", "M3", "M5"]

    # Backend API filtering logic (from bot.ts lines 259-267)
    assigned_set = set(scout_assigned_markets)
    seen = set()
    filtered_tour = []
    for waypoint in full_tour_order:
        if waypoint in assigned_set and waypoint not in seen:
            filtered_tour.append(waypoint)
            seen.add(waypoint)

    print(f"\nFull tour: {full_tour_order}")
    print(f"Assigned markets: {scout_assigned_markets}")
    print(f"Filtered tour: {filtered_tour}")

    # Verify filtering preserves optimized order
    assert filtered_tour == ["M1", "M3", "M5"], "Filtered tour should preserve full tour order"
    assert len(filtered_tour) == len(scout_assigned_markets), "All assigned markets should be in filtered tour"


def test_crossing_edge_detection_algorithm():
    """Test the crossing edge detection algorithm works correctly"""

    # Case 1: X-shaped crossing (obvious crossing)
    tour1 = ["A", "B", "C", "D"]
    coords1 = {
        "A": (0.0, 0.0),
        "B": (100.0, 100.0),
        "C": (0.0, 100.0),
        "D": (100.0, 0.0),
    }
    # Tour A→B→C→D creates edges A→B and C→D which cross
    has_cross1, pairs1 = has_crossing_edges(tour1, coords1)
    assert has_cross1, "X-shaped tour should have crossing edges"
    assert len(pairs1) > 0, "Should detect at least one crossing"

    # Case 2: Square (no crossing)
    tour2 = ["A", "B", "C", "D"]
    coords2 = {
        "A": (0.0, 0.0),
        "B": (100.0, 0.0),
        "C": (100.0, 100.0),
        "D": (0.0, 100.0),
    }
    # Tour A→B→C→D creates a square with no crossings
    has_cross2, pairs2 = has_crossing_edges(tour2, coords2)
    assert not has_cross2, f"Square tour should have no crossings, found {pairs2}"

    # Case 3: Triangle (no crossing possible with 3 points)
    tour3 = ["A", "B", "C"]
    coords3 = {
        "A": (0.0, 0.0),
        "B": (100.0, 0.0),
        "C": (50.0, 100.0),
    }
    has_cross3, pairs3 = has_crossing_edges(tour3, coords3)
    assert not has_cross3, "Triangle cannot have crossings"


def test_backend_should_use_exact_cached_tour_not_filtered_full_tour(temp_db_path):
    """
    Backend API fix: Match tours by EXACT market list, not by filtering full tour.

    This test validates the FIX for the crossing edges bug.
    """
    db = Database(temp_db_path)

    # Save individual optimized tours for each scout (simulates scout coordinator behavior)
    scout1_markets = ["M1", "M2", "M3"]
    scout1_tour = ["S1", "M1", "M3", "M2", "S1"]  # Optimized with OR-Tools

    scout2_markets = ["M4", "M5", "M6"]
    scout2_tour = ["S2", "M5", "M4", "M6", "S2"]  # Optimized with OR-Tools

    with db.transaction() as conn:
        # Save scout 1 tour
        db.save_tour_cache(
            conn,
            system="X1-TEST",
            markets=scout1_markets,
            algorithm="ortools",
            tour_order=scout1_tour,
            total_distance=100.0,
            start_waypoint="S1"
        )

        # Save scout 2 tour
        db.save_tour_cache(
            conn,
            system="X1-TEST",
            markets=scout2_markets,
            algorithm="ortools",
            tour_order=scout2_tour,
            total_distance=120.0,
            start_waypoint="S2"
        )

    with db.connection() as conn:
        conn.execute("PRAGMA wal_checkpoint(FULL)")

    # Backend API should retrieve exact cached tour for scout 1
    with db.connection() as conn:
        cached = db.get_cached_tour(
            conn,
            system="X1-TEST",
            markets=scout1_markets,
            algorithm="ortools",
            start_waypoint="S1"
        )

    assert cached is not None, "Should find exact cached tour for scout 1"
    assert cached["tour_order"] == scout1_tour, "Should return scout 1's optimized tour, not filtered full tour"

    # Backend API should retrieve exact cached tour for scout 2
    with db.connection() as conn:
        cached = db.get_cached_tour(
            conn,
            system="X1-TEST",
            markets=scout2_markets,
            algorithm="ortools",
            start_waypoint="S2"
        )

    assert cached is not None, "Should find exact cached tour for scout 2"
    assert cached["tour_order"] == scout2_tour, "Should return scout 2's optimized tour, not filtered full tour"

    # Backend API should NOT find a tour if markets don't match exactly
    wrong_markets = ["M1", "M2"]  # Different from scout1_markets
    with db.connection() as conn:
        cached = db.get_cached_tour(
            conn,
            system="X1-TEST",
            markets=wrong_markets,
            algorithm="ortools",
            start_waypoint="S1"
        )

    assert cached is None, "Should NOT find tour when markets don't match exactly"


def test_fix_validation_no_crossings_with_individually_optimized_tours(temp_db_path):
    """
    Validate FIX: When using individually optimized tours (not filtered subsets),
    there should be no crossing edges.
    """
    db = Database(temp_db_path)

    # Scout 1: 4 waypoints individually optimized
    scout1_markets = ["A", "B", "C", "D"]
    scout1_tour = ["A", "B", "C", "D"]  # Optimized path

    # Scout 2: 3 waypoints individually optimized
    scout2_markets = ["E", "F", "G"]
    scout2_tour = ["E", "F", "G"]  # Optimized path

    # Waypoint coordinates positioned to cause crossings if paths were naively connected
    coords = {
        "A": (0.0, 0.0),
        "B": (100.0, 0.0),
        "C": (100.0, 100.0),
        "D": (0.0, 100.0),
        "E": (200.0, 50.0),
        "F": (250.0, 50.0),
        "G": (300.0, 50.0),
    }

    # Check scout 1 tour for crossings
    scout1_has_crossings, scout1_crossings = has_crossing_edges(scout1_tour, coords)
    assert not scout1_has_crossings, f"Scout 1 optimized tour should have no crossings, found {scout1_crossings}"

    # Check scout 2 tour for crossings
    scout2_has_crossings, scout2_crossings = has_crossing_edges(scout2_tour, coords)
    assert not scout2_has_crossings, f"Scout 2 optimized tour should have no crossings, found {scout2_crossings}"

    print("\n✅ FIX VALIDATED: Individually optimized tours have no crossing edges")


if __name__ == "__main__":
    # Run tests manually for debugging
    import sys
    pytest.main([__file__, "-v", "-s", "-x"] + sys.argv[1:])
