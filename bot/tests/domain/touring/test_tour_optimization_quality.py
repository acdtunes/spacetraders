#!/usr/bin/env python3
"""
Test suite for OR-Tools TSP tour optimization quality

This test file validates that the OR-Tools TSP solver produces optimal or
near-optimal tours without edge crossings and respects solver time limits.
"""

import json
import pytest
from unittest.mock import MagicMock, patch

from spacetraders_bot.core.ortools_router import ORToolsTSP
from spacetraders_bot.core.routing_config import RoutingConfig


@pytest.fixture
def simple_grid_graph():
    """
    Create a simple 3x3 grid graph for testing tour optimization.

    Layout:
        A1 - A2 - A3
        |    |    |
        B1 - B2 - B3
        |    |    |
        C1 - C2 - C3
    """
    waypoints = {
        "X1-TEST-A1": {"x": 0, "y": 0, "has_fuel": True, "type": "PLANET", "traits": ["MARKETPLACE"]},
        "X1-TEST-A2": {"x": 10, "y": 0, "has_fuel": True, "type": "PLANET", "traits": ["MARKETPLACE"]},
        "X1-TEST-A3": {"x": 20, "y": 0, "has_fuel": True, "type": "PLANET", "traits": ["MARKETPLACE"]},
        "X1-TEST-B1": {"x": 0, "y": 10, "has_fuel": True, "type": "PLANET", "traits": ["MARKETPLACE"]},
        "X1-TEST-B2": {"x": 10, "y": 10, "has_fuel": True, "type": "PLANET", "traits": ["MARKETPLACE"]},
        "X1-TEST-B3": {"x": 20, "y": 10, "has_fuel": True, "type": "PLANET", "traits": ["MARKETPLACE"]},
        "X1-TEST-C1": {"x": 0, "y": 20, "has_fuel": True, "type": "PLANET", "traits": ["MARKETPLACE"]},
        "X1-TEST-C2": {"x": 10, "y": 20, "has_fuel": True, "type": "PLANET", "traits": ["MARKETPLACE"]},
        "X1-TEST-C3": {"x": 20, "y": 20, "has_fuel": True, "type": "PLANET", "traits": ["MARKETPLACE"]},
    }

    graph = {
        "system": "X1-TEST",
        "waypoints": waypoints,
        "edges": []
    }

    return graph


@pytest.fixture
def ship_data():
    """Standard ship configuration for testing."""
    return {
        "symbol": "TEST-SHIP-1",
        "engine": {"speed": 9},
        "fuel": {"current": 1000, "capacity": 1000},
        "nav": {"waypointSymbol": "X1-TEST-A1"},
    }


@pytest.fixture
def routing_config_short_timeout():
    """Routing config with short timeout (5 seconds) - mimics production issue."""
    config = RoutingConfig()
    config._config["solver"]["time_limit_ms"] = 5000  # 5 seconds
    return config


@pytest.fixture
def routing_config_long_timeout():
    """Routing config with longer timeout for better solutions."""
    config = RoutingConfig()
    config._config["solver"]["time_limit_ms"] = 30000  # 30 seconds
    return config


def count_edge_crossings(tour_order, graph):
    """
    Count the number of edge crossings in a tour.

    An edge crossing occurs when two tour edges intersect.
    For example, if the tour goes A→C and B→D, and these edges cross,
    that's one crossing.

    Returns:
        int: Number of edge crossings
    """
    if len(tour_order) < 4:
        return 0  # Need at least 4 waypoints to have crossings

    waypoints = graph["waypoints"]
    edges = []

    # Build list of tour edges with coordinates
    for i in range(len(tour_order) - 1):
        wp1 = tour_order[i]
        wp2 = tour_order[i + 1]

        if wp1 not in waypoints or wp2 not in waypoints:
            continue

        x1, y1 = waypoints[wp1]["x"], waypoints[wp1]["y"]
        x2, y2 = waypoints[wp2]["x"], waypoints[wp2]["y"]
        edges.append(((x1, y1), (x2, y2)))

    # Check each pair of edges for crossings
    crossings = 0
    for i in range(len(edges)):
        for j in range(i + 2, len(edges)):  # Skip adjacent edges
            if segments_intersect(edges[i][0], edges[i][1], edges[j][0], edges[j][1]):
                crossings += 1

    return crossings


def segments_intersect(p1, p2, p3, p4):
    """
    Check if line segment p1-p2 intersects with p3-p4.

    Uses the cross product method to detect intersection.
    """
    x1, y1 = p1
    x2, y2 = p2
    x3, y3 = p3
    x4, y4 = p4

    # Calculate direction of cross products
    def ccw(A, B, C):
        return (C[1] - A[1]) * (B[0] - A[0]) > (B[1] - A[1]) * (C[0] - A[0])

    # Segments intersect if endpoints are on opposite sides
    return ccw(p1, p3, p4) != ccw(p2, p3, p4) and ccw(p1, p2, p3) != ccw(p1, p2, p4)


class TestTourOptimizationQuality:
    """Test suite for TSP solver quality and configuration."""

    def regression_simple_grid_tour_no_crossings(self, simple_grid_graph, ship_data, routing_config_long_timeout):
        """
        Test that a simple 3x3 grid produces a tour with no edge crossings.

        Expected optimal tour should follow a path like:
        A1 → A2 → A3 → B3 → C3 → C2 → C1 → B1 → B2 → A1

        This is a "spiral" or "perimeter" pattern with no crossings.
        """
        # Mock database to avoid I/O
        with patch('spacetraders_bot.core.ortools_router.get_database') as mock_db:
            mock_db_instance = MagicMock()
            mock_db.return_value = mock_db_instance
            mock_db_instance.connection.return_value.__enter__.return_value = MagicMock()
            mock_db_instance.get_cached_tour.return_value = None  # Force optimization

            tsp = ORToolsTSP(simple_grid_graph, routing_config_long_timeout)

            waypoints = [
                "X1-TEST-A2", "X1-TEST-A3", "X1-TEST-B1", "X1-TEST-B2",
                "X1-TEST-B3", "X1-TEST-C1", "X1-TEST-C2", "X1-TEST-C3"
            ]

            tour = tsp.optimise_tour(
                waypoints=waypoints,
                start="X1-TEST-A1",
                ship_data=ship_data,
                return_to_start=True,
                use_cache=False,
                algorithm="ortools"
            )

            assert tour is not None, "Tour optimization should succeed"
            assert "stops" in tour

            # Extract tour order (start + stops)
            tour_order = [tour["start"]] + tour["stops"]

            # Count edge crossings
            crossings = count_edge_crossings(tour_order, simple_grid_graph)

            assert crossings == 0, f"Tour should have no edge crossings, but found {crossings}"

    def regression_short_timeout_may_produce_suboptimal_tour(self, simple_grid_graph, ship_data, routing_config_short_timeout):
        """
        Test that a short timeout (5 seconds) may produce a suboptimal tour.

        This test documents the current behavior with short timeouts.
        With 5 seconds, OR-Tools may not have enough time to find the optimal solution.
        """
        with patch('spacetraders_bot.core.ortools_router.get_database') as mock_db:
            mock_db_instance = MagicMock()
            mock_db.return_value = mock_db_instance
            mock_db_instance.connection.return_value.__enter__.return_value = MagicMock()
            mock_db_instance.get_cached_tour.return_value = None  # Force optimization

            tsp = ORToolsTSP(simple_grid_graph, routing_config_short_timeout)

            waypoints = [
                "X1-TEST-A2", "X1-TEST-A3", "X1-TEST-B1", "X1-TEST-B2",
                "X1-TEST-B3", "X1-TEST-C1", "X1-TEST-C2", "X1-TEST-C3"
            ]

            tour = tsp.optimise_tour(
                waypoints=waypoints,
                start="X1-TEST-A1",
                ship_data=ship_data,
                return_to_start=True,
                use_cache=False,
                algorithm="ortools"
            )

            assert tour is not None, "Tour optimization should succeed even with short timeout"

            # Extract tour order
            tour_order = [tour["start"]] + tour["stops"]

            # Count edge crossings
            crossings = count_edge_crossings(tour_order, simple_grid_graph)

            # Document that short timeouts MAY produce suboptimal solutions
            # (This test may pass or fail depending on solver luck)
            print(f"\nShort timeout (5s) produced tour with {crossings} crossings")
            print(f"Tour order: {' → '.join(tour_order)}")

    def regression_long_timeout_produces_better_tour(self, simple_grid_graph, ship_data, routing_config_short_timeout, routing_config_long_timeout):
        """
        Test that a longer timeout produces a better (or equal) tour compared to short timeout.
        """
        with patch('spacetraders_bot.core.ortools_router.get_database') as mock_db:
            mock_db_instance = MagicMock()
            mock_db.return_value = mock_db_instance
            mock_db_instance.connection.return_value.__enter__.return_value = MagicMock()
            mock_db_instance.get_cached_tour.return_value = None

            waypoints = [
                "X1-TEST-A2", "X1-TEST-A3", "X1-TEST-B1", "X1-TEST-B2",
                "X1-TEST-B3", "X1-TEST-C1", "X1-TEST-C2", "X1-TEST-C3"
            ]

            # Test with short timeout
            tsp_short = ORToolsTSP(simple_grid_graph, routing_config_short_timeout)
            tour_short = tsp_short.optimise_tour(
                waypoints=waypoints,
                start="X1-TEST-A1",
                ship_data=ship_data,
                return_to_start=True,
                use_cache=False,
                algorithm="ortools"
            )

            # Test with long timeout
            tsp_long = ORToolsTSP(simple_grid_graph, routing_config_long_timeout)
            tour_long = tsp_long.optimise_tour(
                waypoints=waypoints,
                start="X1-TEST-A1",
                ship_data=ship_data,
                return_to_start=True,
                use_cache=False,
                algorithm="ortools"
            )

            assert tour_short is not None
            assert tour_long is not None

            # Compare total distances
            distance_short = tour_short["total_distance"]
            distance_long = tour_long["total_distance"]

            # Longer timeout should produce equal or better solution
            assert distance_long <= distance_short, \
                f"Longer timeout should produce better tour (short: {distance_short}, long: {distance_long})"

            # Compare edge crossings
            tour_order_short = [tour_short["start"]] + tour_short["stops"]
            tour_order_long = [tour_long["start"]] + tour_long["stops"]

            crossings_short = count_edge_crossings(tour_order_short, simple_grid_graph)
            crossings_long = count_edge_crossings(tour_order_long, simple_grid_graph)

            print(f"\nShort timeout: {crossings_short} crossings, distance: {distance_short}")
            print(f"Long timeout: {crossings_long} crossings, distance: {distance_long}")

            assert crossings_long <= crossings_short, \
                f"Longer timeout should produce fewer crossings (short: {crossings_short}, long: {crossings_long})"


class TestProductionScenario:
    """Test the specific production scenario from Satellite 3."""

    def regression_large_grid_tour_quality(self):
        """
        Test a larger 5x5 grid (25 waypoints) to simulate production scenario.

        This tests solver performance with problem sizes similar to production (23 waypoints).
        """
        # Create 5x5 grid
        graph = {
            "system": "X1-TEST",
            "waypoints": {},
            "edges": []
        }

        # Generate 25 waypoints in 5x5 grid
        for row in range(5):
            for col in range(5):
                symbol = f"X1-TEST-{chr(65+row)}{col+1}"
                graph["waypoints"][symbol] = {
                    "x": col * 20,
                    "y": row * 20,
                    "has_fuel": True,
                    "type": "PLANET",
                    "traits": ["MARKETPLACE"]
                }

        ship_data = {
            "symbol": "TEST-SHIP-1",
            "engine": {"speed": 9},
            "fuel": {"current": 1000, "capacity": 1000},
            "nav": {"waypointSymbol": "X1-TEST-A1"},
        }

        # Test with current production timeout (30 seconds)
        config = RoutingConfig()
        config._config["solver"]["time_limit_ms"] = 30000

        with patch('spacetraders_bot.core.ortools_router.get_database') as mock_db:
            mock_db_instance = MagicMock()
            mock_db.return_value = mock_db_instance
            mock_db_instance.connection.return_value.__enter__.return_value = MagicMock()
            mock_db_instance.get_cached_tour.return_value = None

            tsp = ORToolsTSP(graph, config)

            # Select 23 waypoints (similar to production)
            waypoints = [f"X1-TEST-{chr(65+row)}{col+1}"
                        for row in range(5) for col in range(5)
                        if not (row == 0 and col == 0)][:23]

            tour = tsp.optimise_tour(
                waypoints=waypoints,
                start="X1-TEST-A1",
                ship_data=ship_data,
                return_to_start=True,
                use_cache=False,
                algorithm="ortools"
            )

            assert tour is not None, "Tour optimization should succeed"
            assert "stops" in tour

            # Extract tour order
            tour_order = [tour["start"]] + tour["stops"]

            # Count edge crossings
            crossings = count_edge_crossings(tour_order, graph)

            print(f"\n25-waypoint grid (23 + start): {crossings} crossings")
            print(f"Tour distance: {tour['total_distance']:.2f}")
            print(f"Tour order sample: {' → '.join(tour_order[:5])}...{' → '.join(tour_order[-3:])}")

            assert crossings == 0, \
                f"Large tour should have no crossings with 30s timeout, found {crossings}"

    def regression_dragonspyre_23_waypoint_tour(self):
        """
        Test the actual 23-waypoint tour from DRAGONSPYRE-3.

        Markets: A1, AC5D, B6, B7, C47, D48, E53, F55, G58, H59, K92,
                 A2, A3, A4, C46, D49, D50, D51, E54, F56, H60, H61, H62

        This test validates that the tour has minimal crossings with proper timeout.
        """
        # This test would require real X1-VH85 graph data
        # For now, document the expected behavior
        pytest.skip("Requires real X1-VH85 graph data - use for manual validation")


