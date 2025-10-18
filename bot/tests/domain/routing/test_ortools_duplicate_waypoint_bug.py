#!/usr/bin/env python3
"""
Test for OR-Tools fleet partitioner duplicate waypoint bug

BUG: When partitioning markets across multiple ships, the OR-Tools VRP solver
can assign the same waypoint to multiple vehicle tours. The partition_and_optimize()
method only checks for duplicates WITHIN a single ship's tour, not ACROSS ships.

Expected: Each waypoint should appear in exactly ONE tour
Actual: Some waypoints appear in multiple tours (e.g., X1-VH85-E53 in both Tour 2 and Tour 4)

This bug causes scout coordinator to hang/loop indefinitely because scouts
try to visit the same markets, violating the disjoint partition guarantee.
"""

import pytest
from spacetraders_bot.core.ortools_router import ORToolsFleetPartitioner
from spacetraders_bot.core.routing_config import RoutingConfig


@pytest.fixture
def vh85_graph_small():
    """
    Simplified X1-VH85 graph with the problematic waypoint E53
    that gets duplicated across tours in real-world scenario
    """
    waypoints = {
        'X1-VH85-A2': {'x': 10, 'y': 12, 'has_fuel': True},
        'X1-VH85-A3': {'x': -12, 'y': 5, 'has_fuel': True},
        'X1-VH85-C46': {'x': -41, 'y': -6, 'has_fuel': True},
        'X1-VH85-E53': {'x': 18, 'y': -49, 'has_fuel': True},  # The problematic waypoint
        'X1-VH85-H62': {'x': 32, 'y': -22, 'has_fuel': True},
        'X1-VH85-I64': {'x': -34, 'y': 17, 'has_fuel': True},
        'X1-VH85-J66': {'x': 52, 'y': 41, 'has_fuel': True},
        'X1-VH85-K92': {'x': -53, 'y': 0, 'has_fuel': True},
    }

    # Generate edges (complete graph for testing)
    edges = []
    wp_list = list(waypoints.keys())
    for i, wp1 in enumerate(wp_list):
        for wp2 in wp_list[i+1:]:
            wp1_data = waypoints[wp1]
            wp2_data = waypoints[wp2]
            # Calculate Euclidean distance
            distance = ((wp2_data['x'] - wp1_data['x'])**2 + (wp2_data['y'] - wp1_data['y'])**2)**0.5
            edges.append({
                'from': wp1,
                'to': wp2,
                'distance': round(distance, 2),
                'type': 'normal'
            })

    return {
        'system': 'X1-VH85',
        'waypoints': waypoints,
        'edges': edges
    }


@pytest.fixture
def vh85_ship_data():
    """Ship data for 4 scout ships starting at A2"""
    ships = {}
    for i in range(2, 6):
        ship = f'DRAGONSPYRE-{i}'
        ships[ship] = {
            'symbol': ship,
            'nav': {
                'waypointSymbol': 'X1-VH85-A2',
                'systemSymbol': 'X1-VH85'
            },
            'fuel': {
                'current': 400,
                'capacity': 400
            },
            'engine': {
                'speed': 30
            }
        }
    return ships


def test_ortools_partitioner_no_duplicate_waypoints(vh85_graph_small, vh85_ship_data):
    """
    Test that ORToolsFleetPartitioner creates truly disjoint partitions
    without assigning the same waypoint to multiple ships.

    This test reproduces the bug where E53 appeared in both Tour 2 and Tour 4.
    """
    # Arrange
    markets = list(vh85_graph_small['waypoints'].keys())
    ships = list(vh85_ship_data.keys())
    config = RoutingConfig()

    partitioner = ORToolsFleetPartitioner(vh85_graph_small, config)

    # Act
    assignments = partitioner.partition_and_optimize(
        markets=markets,
        ships=ships,
        ship_data=vh85_ship_data
    )

    # Assert: Collect all assigned waypoints across all ships
    all_assigned_waypoints = []
    for ship, waypoints in assignments.items():
        all_assigned_waypoints.extend(waypoints)

    # Count occurrences of each waypoint
    waypoint_counts = {}
    for waypoint in all_assigned_waypoints:
        waypoint_counts[waypoint] = waypoint_counts.get(waypoint, 0) + 1

    # Find duplicates
    duplicates = {wp: count for wp, count in waypoint_counts.items() if count > 1}

    # CRITICAL ASSERTION: No waypoint should appear in multiple tours
    assert len(duplicates) == 0, \
        f"Duplicate waypoints found across tours: {duplicates}\n" \
        f"Assignments: {assignments}"


def test_ortools_partitioner_all_markets_assigned(vh85_graph_small, vh85_ship_data):
    """
    Test that all markets are assigned to exactly one ship (no missing markets).

    This ensures the partitioner doesn't lose waypoints during assignment.
    """
    # Arrange
    markets = list(vh85_graph_small['waypoints'].keys())
    ships = list(vh85_ship_data.keys())
    config = RoutingConfig()

    partitioner = ORToolsFleetPartitioner(vh85_graph_small, config)

    # Act
    assignments = partitioner.partition_and_optimize(
        markets=markets,
        ships=ships,
        ship_data=vh85_ship_data
    )

    # Assert: All markets should be assigned
    assigned_markets = set()
    for ship, waypoints in assignments.items():
        assigned_markets.update(waypoints)

    missing_markets = set(markets) - assigned_markets

    assert len(missing_markets) == 0, \
        f"Markets not assigned to any ship: {missing_markets}\n" \
        f"Assignments: {assignments}"


def test_ortools_partitioner_disjoint_property(vh85_graph_small, vh85_ship_data):
    """
    Test the mathematical disjoint property: intersection of any two tours is empty.

    For any two ships A and B: assigned_markets(A) ∩ assigned_markets(B) = ∅
    """
    # Arrange
    markets = list(vh85_graph_small['waypoints'].keys())
    ships = list(vh85_ship_data.keys())
    config = RoutingConfig()

    partitioner = ORToolsFleetPartitioner(vh85_graph_small, config)

    # Act
    assignments = partitioner.partition_and_optimize(
        markets=markets,
        ships=ships,
        ship_data=vh85_ship_data
    )

    # Assert: Check pairwise disjoint property
    ship_list = list(assignments.keys())
    overlaps = []

    for i, ship_a in enumerate(ship_list):
        for ship_b in ship_list[i+1:]:
            set_a = set(assignments[ship_a])
            set_b = set(assignments[ship_b])
            intersection = set_a & set_b

            if intersection:
                overlaps.append({
                    'ship_a': ship_a,
                    'ship_b': ship_b,
                    'overlap': list(intersection)
                })

    assert len(overlaps) == 0, \
        f"Tours are not disjoint! Found {len(overlaps)} overlapping pairs:\n" \
        f"{overlaps}\n" \
        f"Assignments: {assignments}"
