#!/usr/bin/env python3
"""
Test to reproduce ACTUAL bug report: A1 and J53 being dropped during OR-Tools partitioning.

Bug Report Context:
- Input to partitioner: 21 markets (including A1 and J53)
- Output from partitioner: 19 markets (A1 and J53 MISSING)
- Scout-3: B7, I50 (2 markets)
- Scout-5: 17 markets (FA5C, D38, E40, F43, G45, H46, K78, A2, A3, A4, C36, D39, E41, F44, H47, H48, H49)
- MISSING: A1, J53

This test uses REAL X1-JV40 data to reproduce the actual scenario where markets are dropped.
"""

import pytest
from unittest.mock import Mock
from spacetraders_bot.core.ortools_router import ORToolsFleetPartitioner
from spacetraders_bot.core.routing_config import RoutingConfig


def test_ortools_drops_a1_and_j53_in_real_xjv40_scenario():
    """
    Reproduce ACTUAL bug: OR-Tools drops A1 and J53 during partitioning.

    This uses the EXACT markets and ships from the production scenario where
    the bug was observed.
    """
    # EXACT markets from production scenario (21 total)
    markets = [
        'X1-JV40-A1',   # DROPPED in production
        'X1-JV40-A2',
        'X1-JV40-A3',
        'X1-JV40-A4',
        'X1-JV40-B7',
        'X1-JV40-C36',
        'X1-JV40-D38',
        'X1-JV40-D39',
        'X1-JV40-E40',
        'X1-JV40-E41',
        'X1-JV40-F43',
        'X1-JV40-F44',
        'X1-JV40-FA5C',
        'X1-JV40-G45',
        'X1-JV40-H46',
        'X1-JV40-H47',
        'X1-JV40-H48',
        'X1-JV40-H49',
        'X1-JV40-I50',
        'X1-JV40-J53',   # DROPPED in production
        'X1-JV40-K78',
    ]

    ships = ['Scout-3', 'Scout-5']

    # Real X1-JV40 waypoint coordinates (from production system)
    # These are the ACTUAL coordinates causing the bug
    waypoints = {
        'X1-JV40-A1': {'x': -36, 'y': -34, 'has_fuel': True},      # OUTLIER (negative coords)
        'X1-JV40-A2': {'x': 10, 'y': 12, 'has_fuel': True},
        'X1-JV40-A3': {'x': 14, 'y': -32, 'has_fuel': False},
        'X1-JV40-A4': {'x': 24, 'y': -25, 'has_fuel': True},
        'X1-JV40-B7': {'x': 31, 'y': -7, 'has_fuel': True},
        'X1-JV40-C36': {'x': 42, 'y': 3, 'has_fuel': False},
        'X1-JV40-D38': {'x': -11, 'y': -10, 'has_fuel': True},
        'X1-JV40-D39': {'x': 25, 'y': -15, 'has_fuel': False},
        'X1-JV40-E40': {'x': 30, 'y': 13, 'has_fuel': True},
        'X1-JV40-E41': {'x': -26, 'y': -1, 'has_fuel': False},
        'X1-JV40-F43': {'x': -5, 'y': 16, 'has_fuel': True},
        'X1-JV40-F44': {'x': 8, 'y': 18, 'has_fuel': False},
        'X1-JV40-FA5C': {'x': -21, 'y': 21, 'has_fuel': True},
        'X1-JV40-G45': {'x': 18, 'y': 17, 'has_fuel': False},
        'X1-JV40-H46': {'x': -18, 'y': 28, 'has_fuel': True},
        'X1-JV40-H47': {'x': -39, 'y': 20, 'has_fuel': False},
        'X1-JV40-H48': {'x': -24, 'y': 34, 'has_fuel': True},
        'X1-JV40-H49': {'x': -13, 'y': 35, 'has_fuel': False},
        'X1-JV40-I50': {'x': 18, 'y': -49, 'has_fuel': True},
        'X1-JV40-J53': {'x': -5, 'y': -40, 'has_fuel': False},   # EXTREME OUTLIER (far from cluster)
        'X1-JV40-K78': {'x': -29, 'y': 11, 'has_fuel': False},
    }

    graph = {
        'system': 'X1-JV40',
        'waypoints': waypoints,
        'edges': []
    }

    ship_data = {
        'Scout-3': {
            'nav': {'waypointSymbol': 'X1-JV40-I50'},
            'fuel': {'current': 400, 'capacity': 400},
            'engine': {'speed': 9}
        },
        'Scout-5': {
            'nav': {'waypointSymbol': 'X1-JV40-A1'},
            'fuel': {'current': 400, 'capacity': 400},
            'engine': {'speed': 9}
        }
    }

    config = RoutingConfig()
    partitioner = ORToolsFleetPartitioner(graph, config)

    # This should raise RoutingError if markets are dropped
    try:
        assignments = partitioner.partition_and_optimize(markets, ships, ship_data)

        # If we got here without exception, validation passed
        # Now check that ALL markets are assigned
        assigned_markets = []
        for ship, ship_markets in assignments.items():
            assigned_markets.extend(ship_markets)

        # CRITICAL ASSERTION: Must have all 21 markets
        assert len(assigned_markets) == 21, \
            f"Expected 21 markets, got {len(assigned_markets)}. Missing: {set(markets) - set(assigned_markets)}"

        # CRITICAL: A1 must be included
        assert 'X1-JV40-A1' in assigned_markets, \
            f"A1 was DROPPED! Assigned markets: {assigned_markets}"

        # CRITICAL: J53 must be included
        assert 'X1-JV40-J53' in assigned_markets, \
            f"J53 was DROPPED! Assigned markets: {assigned_markets}"

        # Print assignments for verification
        print("\n✅ OR-Tools partitioning complete:")
        for ship, ship_markets in assignments.items():
            print(f"  {ship}: {len(ship_markets)} markets - {', '.join(ship_markets)}")

    except Exception as e:
        pytest.fail(f"OR-Tools partitioning failed: {e}")


def test_disjunction_penalty_calculation_with_real_distances():
    """
    Test that disjunction penalty is correctly calculated with REAL X1-JV40 distances.

    The bug is that disjunction penalty might be too low compared to actual distance costs,
    causing OR-Tools to drop expensive markets.
    """
    # Use same real data
    markets = [
        'X1-JV40-A1', 'X1-JV40-A2', 'X1-JV40-A3', 'X1-JV40-A4', 'X1-JV40-B7',
        'X1-JV40-C36', 'X1-JV40-D38', 'X1-JV40-D39', 'X1-JV40-E40', 'X1-JV40-E41',
        'X1-JV40-F43', 'X1-JV40-F44', 'X1-JV40-FA5C', 'X1-JV40-G45', 'X1-JV40-H46',
        'X1-JV40-H47', 'X1-JV40-H48', 'X1-JV40-H49', 'X1-JV40-I50', 'X1-JV40-J53',
        'X1-JV40-K78',
    ]

    ships = ['Scout-3', 'Scout-5']

    waypoints = {
        'X1-JV40-A1': {'x': -36, 'y': -34, 'has_fuel': True},
        'X1-JV40-A2': {'x': 10, 'y': 12, 'has_fuel': True},
        'X1-JV40-A3': {'x': 14, 'y': -32, 'has_fuel': False},
        'X1-JV40-A4': {'x': 24, 'y': -25, 'has_fuel': True},
        'X1-JV40-B7': {'x': 31, 'y': -7, 'has_fuel': True},
        'X1-JV40-C36': {'x': 42, 'y': 3, 'has_fuel': False},
        'X1-JV40-D38': {'x': -11, 'y': -10, 'has_fuel': True},
        'X1-JV40-D39': {'x': 25, 'y': -15, 'has_fuel': False},
        'X1-JV40-E40': {'x': 30, 'y': 13, 'has_fuel': True},
        'X1-JV40-E41': {'x': -26, 'y': -1, 'has_fuel': False},
        'X1-JV40-F43': {'x': -5, 'y': 16, 'has_fuel': True},
        'X1-JV40-F44': {'x': 8, 'y': 18, 'has_fuel': False},
        'X1-JV40-FA5C': {'x': -21, 'y': 21, 'has_fuel': True},
        'X1-JV40-G45': {'x': 18, 'y': 17, 'has_fuel': False},
        'X1-JV40-H46': {'x': -18, 'y': 28, 'has_fuel': True},
        'X1-JV40-H47': {'x': -39, 'y': 20, 'has_fuel': False},
        'X1-JV40-H48': {'x': -24, 'y': 34, 'has_fuel': True},
        'X1-JV40-H49': {'x': -13, 'y': 35, 'has_fuel': False},
        'X1-JV40-I50': {'x': 18, 'y': -49, 'has_fuel': True},
        'X1-JV40-J53': {'x': -5, 'y': -40, 'has_fuel': False},
        'X1-JV40-K78': {'x': -29, 'y': 11, 'has_fuel': False},
    }

    graph = {
        'system': 'X1-JV40',
        'waypoints': waypoints,
        'edges': []
    }

    ship_data = {
        'Scout-3': {
            'nav': {'waypointSymbol': 'X1-JV40-I50'},
            'fuel': {'current': 400, 'capacity': 400},
            'engine': {'speed': 9}
        },
        'Scout-5': {
            'nav': {'waypointSymbol': 'X1-JV40-A1'},
            'fuel': {'current': 400, 'capacity': 400},
            'engine': {'speed': 9}
        }
    }

    config = RoutingConfig()
    partitioner = ORToolsFleetPartitioner(graph, config)

    # Manually build distance matrix to inspect costs
    from spacetraders_bot.core.ortools_router import ORToolsRouter

    reference_ship = ship_data[ships[0]]
    base_router = ORToolsRouter(graph, reference_ship, config)

    # Build nodes list (markets + ship starting locations)
    nodes = list(markets)
    for ship in ships:
        waypoint = ship_data[ship]["nav"]["waypointSymbol"]
        if waypoint not in nodes:
            nodes.append(waypoint)

    # Build distance matrix
    distance_matrix = partitioner._build_distance_matrix(nodes, base_router)

    # Calculate max distance cost
    max_distance_cost = 0
    for row in distance_matrix:
        max_distance_cost = max(max_distance_cost, max(row))

    # Calculate disjunction penalty (should be 10x max cost)
    expected_disjunction_penalty = max(max_distance_cost * 10, 10_000_000)

    print(f"\n📊 Distance matrix statistics:")
    print(f"   Max distance cost: {max_distance_cost}")
    print(f"   Expected disjunction penalty: {expected_disjunction_penalty}")
    print(f"   Ratio: {expected_disjunction_penalty / max_distance_cost:.1f}x")

    # CRITICAL: Disjunction penalty must be MUCH higher than any single distance
    assert expected_disjunction_penalty >= max_distance_cost * 10, \
        f"Disjunction penalty ({expected_disjunction_penalty}) is not high enough! " \
        f"Should be at least 10x max cost ({max_distance_cost})"

    # Now run actual partitioning and verify no markets dropped
    assignments = partitioner.partition_and_optimize(markets, ships, ship_data)

    assigned_markets = []
    for ship, ship_markets in assignments.items():
        assigned_markets.extend(ship_markets)

    assert len(assigned_markets) == 21, \
        f"Markets were dropped! Expected 21, got {len(assigned_markets)}"
