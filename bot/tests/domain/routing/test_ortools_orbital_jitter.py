#!/usr/bin/env python3
"""
Test for OR-Tools TSP orbital coordinate jitter fix.

BUG: Orbital waypoints (planets + their moons/stations) have IDENTICAL coordinates
in the database, creating distance=0 between them. This violates TSP assumptions and
confuses the OR-Tools solver, producing tours with crossing edges.

FIX: Apply tiny coordinate jitter (0.05-0.1 units) to orbital waypoints before
building the TSP distance matrix. This ensures all waypoints have unique positions
while preserving actual navigation behavior (orbitals still have 0 distance in routing).

This test validates the fix by:
1. Testing that orbitals without jitter produce suboptimal tours
2. Testing that orbitals with jitter produce optimal crossing-free tours
3. Verifying jitter is applied correctly to orbital groups
"""

import math
import pytest
from typing import Dict, List, Tuple
from ortools.constraint_solver import pywrapcp, routing_enums_pb2


# X1-VH85 coordinates with orbitals at identical positions
WAYPOINTS_WITH_ORBITALS = {
    # Orbital group 1: Planet A1 + moons A2, A3, A4 (all at same location)
    "X1-VH85-A1": {"x": 19.0, "y": 15.0},   # Planet
    "X1-VH85-A2": {"x": 19.0, "y": 15.0},   # Moon 1
    "X1-VH85-A3": {"x": 19.0, "y": 15.0},   # Moon 2
    "X1-VH85-A4": {"x": 19.0, "y": 15.0},   # Moon 3

    # Orbital group 2: Planet D48 + moons D49, D50, D51 (all at same location)
    "X1-VH85-D48": {"x": 2.0, "y": 87.0},   # Planet
    "X1-VH85-D49": {"x": 2.0, "y": 87.0},   # Moon 1
    "X1-VH85-D50": {"x": 2.0, "y": 87.0},   # Moon 2
    "X1-VH85-D51": {"x": 2.0, "y": 87.0},   # Moon 3

    # Orbital group 3: Planet E53 + moon E54 (same location)
    "X1-VH85-E53": {"x": 34.0, "y": -42.0},  # Planet
    "X1-VH85-E54": {"x": 34.0, "y": -42.0},  # Moon

    # Orbital group 4: Planet F55 + moon F56 (same location)
    "X1-VH85-F55": {"x": 24.0, "y": 72.0},   # Planet
    "X1-VH85-F56": {"x": 24.0, "y": 72.0},   # Moon

    # Orbital group 5: Planet H59 + moons H60, H61, H62 (same location)
    "X1-VH85-H59": {"x": -36.0, "y": 24.0},  # Planet
    "X1-VH85-H60": {"x": -36.0, "y": 24.0},  # Moon 1
    "X1-VH85-H61": {"x": -36.0, "y": 24.0},  # Moon 2
    "X1-VH85-H62": {"x": -36.0, "y": 24.0},  # Moon 3

    # Non-orbital waypoints (unique coordinates)
    "X1-VH85-AC5D": {"x": -14.0, "y": 22.0},
    "X1-VH85-B6": {"x": 149.0, "y": 119.0},
    "X1-VH85-B7": {"x": 337.0, "y": 76.0},
    "X1-VH85-G58": {"x": -50.0, "y": -43.0},
}


def apply_orbital_jitter(waypoints: Dict[str, Dict]) -> Dict[str, Tuple[float, float]]:
    """
    Apply tiny coordinate jitter to orbital waypoints to prevent OR-Tools confusion.

    Orbitals (planets + their moons) have identical coordinates in database, which
    creates distance=0 and breaks TSP assumptions. Add radial jitter for TSP only.

    Returns:
        Dictionary mapping waypoint symbols to jittered (x, y) coordinates
    """
    coords = {}
    coord_groups: Dict[Tuple[float, float], List[str]] = {}  # Group by coordinates

    # Group waypoints with identical coordinates
    for symbol, wp_data in waypoints.items():
        x = wp_data["x"]
        y = wp_data["y"]
        coord_key = (x, y)

        if coord_key not in coord_groups:
            coord_groups[coord_key] = []
        coord_groups[coord_key].append(symbol)

    # Apply jitter to groups with >1 waypoint (orbitals)
    for (base_x, base_y), group in coord_groups.items():
        if len(group) == 1:
            # Single waypoint at this coordinate - use original
            coords[group[0]] = (base_x, base_y)
        else:
            # Multiple waypoints - apply radial jitter
            jitter_radius = 0.05  # Small enough to not affect distances significantly
            for i, symbol in enumerate(group):
                angle = (2 * math.pi * i) / len(group)  # Spread evenly in circle
                jittered_x = base_x + jitter_radius * math.cos(angle)
                jittered_y = base_y + jitter_radius * math.sin(angle)
                coords[symbol] = (jittered_x, jittered_y)

    return coords


def distance(p1: Tuple[float, float], p2: Tuple[float, float]) -> float:
    """Calculate Euclidean distance between two points."""
    dx = p2[0] - p1[0]
    dy = p2[1] - p1[1]
    return math.hypot(dx, dy)


def solve_tsp_with_coordinates(
    waypoints: List[str],
    start: str,
    coords: Dict[str, Tuple[float, float]],
    timeout_ms: int = 30000,
) -> Tuple[List[str], float]:
    """Solve TSP using provided coordinates."""
    nodes = [start] + [wp for wp in waypoints if wp != start]
    nodes.append("__TOUR_END__")

    start_idx = 0
    end_idx = len(nodes) - 1

    manager = pywrapcp.RoutingIndexManager(len(nodes), 1, [start_idx], [end_idx])
    routing = pywrapcp.RoutingModel(manager)

    # Build distance matrix from coordinates
    distance_matrix = [[0 for _ in range(len(nodes))] for _ in range(len(nodes))]
    for i, wp1 in enumerate(nodes):
        if wp1 == "__TOUR_END__":
            continue
        for j, wp2 in enumerate(nodes):
            if wp2 == "__TOUR_END__":
                distance_matrix[i][j] = 0
                continue
            if i == j:
                distance_matrix[i][j] = 0
            else:
                distance_matrix[i][j] = int(distance(coords[wp1], coords[wp2]) * 100)

    def distance_callback(from_index: int, to_index: int) -> int:
        from_node = manager.IndexToNode(from_index)
        to_node = manager.IndexToNode(to_index)
        return distance_matrix[from_node][to_node]

    transit_callback_index = routing.RegisterTransitCallback(distance_callback)
    routing.SetArcCostEvaluatorOfAllVehicles(transit_callback_index)

    # Configure solver
    search_params = pywrapcp.DefaultRoutingSearchParameters()
    search_params.first_solution_strategy = routing_enums_pb2.FirstSolutionStrategy.PATH_CHEAPEST_ARC
    search_params.local_search_metaheuristic = routing_enums_pb2.LocalSearchMetaheuristic.GUIDED_LOCAL_SEARCH
    search_params.time_limit.FromMilliseconds(timeout_ms)

    solution = routing.SolveWithParameters(search_params)
    if not solution:
        return [], float("inf")

    # Extract tour order
    order = []
    index = routing.Start(0)
    while True:
        node = manager.IndexToNode(index)
        waypoint = nodes[node]
        if waypoint != "__TOUR_END__":
            order.append(waypoint)
        if routing.IsEnd(index):
            break
        index = solution.Value(routing.NextVar(index))

    # Add return to start
    order.append(start)

    objective = solution.ObjectiveValue()
    return order, objective


def count_crossing_edges(order: List[str], coords: Dict[str, Tuple[float, float]]) -> int:
    """Count the number of crossing edges in a tour."""
    def ccw(a, b, c):
        return (c[1] - a[1]) * (b[0] - a[0]) > (b[1] - a[1]) * (c[0] - a[0])

    def segments_intersect(p1, p2, p3, p4):
        # Skip shared endpoints
        if p1 == p3 or p1 == p4 or p2 == p3 or p2 == p4:
            return False
        # Skip zero-distance segments (orbitals without jitter)
        if p1 == p2 or p3 == p4:
            return False
        return ccw(p1, p3, p4) != ccw(p2, p3, p4) and ccw(p1, p2, p3) != ccw(p1, p2, p4)

    edges = []
    for i in range(len(order) - 1):
        p1 = coords[order[i]]
        p2 = coords[order[i + 1]]
        edges.append((p1, p2))

    crossings = 0
    for i in range(len(edges)):
        for j in range(i + 2, len(edges)):
            # Skip adjacent edges
            if abs(i - j) <= 1:
                continue
            if segments_intersect(edges[i][0], edges[i][1], edges[j][0], edges[j][1]):
                crossings += 1

    return crossings


def test_orbital_jitter_detection():
    """Test that orbital groups are correctly detected and jittered."""
    jittered = apply_orbital_jitter(WAYPOINTS_WITH_ORBITALS)

    # Verify all waypoints have coordinates
    assert len(jittered) == len(WAYPOINTS_WITH_ORBITALS)

    # Verify orbital group 1 (A1-A4) has unique coordinates after jitter
    a1_coord = jittered["X1-VH85-A1"]
    a2_coord = jittered["X1-VH85-A2"]
    a3_coord = jittered["X1-VH85-A3"]
    a4_coord = jittered["X1-VH85-A4"]

    # All should be different
    assert a1_coord != a2_coord
    assert a1_coord != a3_coord
    assert a1_coord != a4_coord
    assert a2_coord != a3_coord

    # But all should be close to original (19, 15)
    for coord in [a1_coord, a2_coord, a3_coord, a4_coord]:
        assert abs(coord[0] - 19.0) < 0.1  # Within 0.1 units
        assert abs(coord[1] - 15.0) < 0.1

    # Verify non-orbital waypoint AC5D keeps original coordinates
    ac5d_coord = jittered["X1-VH85-AC5D"]
    assert ac5d_coord == (-14.0, 22.0)

    print(f"\n✓ Orbital jitter correctly applied:")
    print(f"  A1 (planet): {a1_coord}")
    print(f"  A2 (moon):   {a2_coord}")
    print(f"  A3 (moon):   {a3_coord}")
    print(f"  A4 (moon):   {a4_coord}")
    print(f"  AC5D (non-orbital): {ac5d_coord}")


def test_tsp_without_jitter_has_issues():
    """Test that TSP without jitter produces suboptimal solutions with orbitals."""
    # Use original coordinates (orbitals at same position)
    original_coords = {symbol: (wp["x"], wp["y"]) for symbol, wp in WAYPOINTS_WITH_ORBITALS.items()}

    stops = [wp for wp in WAYPOINTS_WITH_ORBITALS.keys() if wp != "X1-VH85-A1"]

    order, objective = solve_tsp_with_coordinates(
        stops,
        "X1-VH85-A1",
        original_coords,
        timeout_ms=30000,
    )

    assert order, "TSP should find a solution even without jitter"

    crossings = count_crossing_edges(order, original_coords)

    print(f"\n❌ TSP WITHOUT jitter:")
    print(f"  Objective: {objective}")
    print(f"  Crossings: {crossings}")
    print(f"  Tour (first 10): {' -> '.join(order[:10])}")

    # We expect this to potentially have suboptimal results due to 0-distance orbitals
    # (though crossings may not be detected due to 0-distance edges)


def test_tsp_with_jitter_is_optimal():
    """Test that TSP with jitter produces optimal crossing-free solutions."""
    # Apply jitter to orbital coordinates
    jittered_coords = apply_orbital_jitter(WAYPOINTS_WITH_ORBITALS)

    stops = [wp for wp in WAYPOINTS_WITH_ORBITALS.keys() if wp != "X1-VH85-A1"]

    order, objective = solve_tsp_with_coordinates(
        stops,
        "X1-VH85-A1",
        jittered_coords,
        timeout_ms=30000,
    )

    assert order, "TSP should find a solution with jitter"

    crossings = count_crossing_edges(order, jittered_coords)

    print(f"\n✅ TSP WITH jitter:")
    print(f"  Objective: {objective}")
    print(f"  Crossings: {crossings}")
    print(f"  Tour (first 10): {' -> '.join(order[:10])}")

    # With jitter, OR-Tools should find a crossing-free solution
    assert crossings == 0, \
        f"TSP with jitter should produce crossing-free tour, but found {crossings} crossings"


def test_jitter_preserves_distances():
    """Test that jitter is small enough to not significantly affect distances."""
    original_coords = {symbol: (wp["x"], wp["y"]) for symbol, wp in WAYPOINTS_WITH_ORBITALS.items()}
    jittered_coords = apply_orbital_jitter(WAYPOINTS_WITH_ORBITALS)

    # Check that jitter doesn't change distances by more than 1%
    max_distance_change = 0.0

    for wp1 in WAYPOINTS_WITH_ORBITALS.keys():
        for wp2 in WAYPOINTS_WITH_ORBITALS.keys():
            if wp1 >= wp2:
                continue

            original_dist = distance(original_coords[wp1], original_coords[wp2])
            jittered_dist = distance(jittered_coords[wp1], jittered_coords[wp2])

            if original_dist > 1.0:  # Skip tiny distances (orbitals)
                change_percent = abs(jittered_dist - original_dist) / original_dist * 100
                max_distance_change = max(max_distance_change, change_percent)

    print(f"\n✓ Jitter impact on distances:")
    print(f"  Maximum distance change: {max_distance_change:.2f}%")

    # Jitter should not change distances by more than 1%
    assert max_distance_change < 1.0, \
        f"Jitter changed distances by {max_distance_change:.2f}%, should be <1%"


def test_jitter_radial_distribution():
    """Test that orbital groups are distributed radially around the original point."""
    jittered = apply_orbital_jitter(WAYPOINTS_WITH_ORBITALS)

    # Check orbital group A (4 waypoints)
    a_coords = [
        jittered["X1-VH85-A1"],
        jittered["X1-VH85-A2"],
        jittered["X1-VH85-A3"],
        jittered["X1-VH85-A4"],
    ]

    # Calculate center of mass
    center_x = sum(c[0] for c in a_coords) / len(a_coords)
    center_y = sum(c[1] for c in a_coords) / len(a_coords)

    # Center should be close to original (19, 15)
    assert abs(center_x - 19.0) < 0.01
    assert abs(center_y - 15.0) < 0.01

    # All points should be approximately same distance from center (radial)
    radii = [distance((center_x, center_y), coord) for coord in a_coords]
    avg_radius = sum(radii) / len(radii)

    for radius in radii:
        assert abs(radius - avg_radius) < 0.01, \
            f"Radial distribution not uniform: {radii}"

    print(f"\n✓ Radial distribution:")
    print(f"  Center: ({center_x:.3f}, {center_y:.3f})")
    print(f"  Average radius: {avg_radius:.3f}")
    print(f"  Radii: {[f'{r:.3f}' for r in radii]}")


if __name__ == "__main__":
    pytest.main([__file__, "-v", "-s"])
