#!/usr/bin/env python3
"""
Test to reproduce and fix J53 market exclusion bug in X1-JV40 system.

Bug: Market X1-JV40-J53 (ASTEROID_BASE at coordinates -447, -565) is being
excluded during scout coordinator partitioning/balancing, even though it has
MARKETPLACE trait and is NOT a fuel station.

Expected: 21 markets should be assigned (25 MARKETPLACE waypoints - 4 fuel stations)
Actual: Only 20 markets assigned (J53 is missing)
"""

import pytest
from unittest.mock import Mock
from spacetraders_bot.core.scout_coordinator import ScoutCoordinator
from spacetraders_bot.core.system_graph_provider import GraphLoadResult


@pytest.fixture
def jv40_graph():
    """
    Create a mock X1-JV40 system graph with J53 at extreme coordinates.

    This reproduces the real-world scenario where J53 is at (-447, -565),
    far from the cluster of other markets which are mostly in positive coordinates.
    """
    # 21 non-fuel-station markets (J53 is the outlier at extreme negative coords)
    waypoints = {
        # Main cluster (positive coords)
        'X1-JV40-A1': {'x': 50, 'y': 50, 'has_fuel': True, 'traits': ['MARKETPLACE'], 'orbitals': []},
        'X1-JV40-A2': {'x': 60, 'y': 55, 'has_fuel': True, 'traits': ['MARKETPLACE'], 'orbitals': []},
        'X1-JV40-A3': {'x': 70, 'y': 60, 'has_fuel': False, 'traits': ['MARKETPLACE'], 'orbitals': []},
        'X1-JV40-A4': {'x': 80, 'y': 65, 'has_fuel': True, 'traits': ['MARKETPLACE'], 'orbitals': []},
        'X1-JV40-B7': {'x': 100, 'y': 100, 'has_fuel': True, 'traits': ['MARKETPLACE'], 'orbitals': []},
        'X1-JV40-C36': {'x': 150, 'y': 120, 'has_fuel': False, 'traits': ['MARKETPLACE'], 'orbitals': []},
        'X1-JV40-D38': {'x': 200, 'y': 150, 'has_fuel': True, 'traits': ['MARKETPLACE'], 'orbitals': []},
        'X1-JV40-D39': {'x': 210, 'y': 160, 'has_fuel': False, 'traits': ['MARKETPLACE'], 'orbitals': []},
        'X1-JV40-E40': {'x': 250, 'y': 180, 'has_fuel': True, 'traits': ['MARKETPLACE'], 'orbitals': []},
        'X1-JV40-E41': {'x': 260, 'y': 190, 'has_fuel': False, 'traits': ['MARKETPLACE'], 'orbitals': []},
        'X1-JV40-F43': {'x': 300, 'y': 200, 'has_fuel': True, 'traits': ['MARKETPLACE'], 'orbitals': []},
        'X1-JV40-F44': {'x': 310, 'y': 210, 'has_fuel': False, 'traits': ['MARKETPLACE'], 'orbitals': []},
        'X1-JV40-FA5C': {'x': 350, 'y': 220, 'has_fuel': True, 'traits': ['MARKETPLACE'], 'orbitals': []},
        'X1-JV40-G45': {'x': 400, 'y': 250, 'has_fuel': False, 'traits': ['MARKETPLACE'], 'orbitals': []},
        'X1-JV40-H46': {'x': 450, 'y': 280, 'has_fuel': True, 'traits': ['MARKETPLACE'], 'orbitals': []},
        'X1-JV40-H47': {'x': 460, 'y': 290, 'has_fuel': False, 'traits': ['MARKETPLACE'], 'orbitals': []},
        'X1-JV40-H48': {'x': 470, 'y': 300, 'has_fuel': True, 'traits': ['MARKETPLACE'], 'orbitals': []},
        'X1-JV40-H49': {'x': 480, 'y': 310, 'has_fuel': False, 'traits': ['MARKETPLACE'], 'orbitals': []},
        'X1-JV40-I50': {'x': 500, 'y': 320, 'has_fuel': True, 'traits': ['MARKETPLACE'], 'orbitals': []},
        'X1-JV40-K78': {'x': 550, 'y': 350, 'has_fuel': False, 'traits': ['MARKETPLACE'], 'orbitals': []},

        # THE OUTLIER: J53 at extreme negative coordinates (far from main cluster)
        'X1-JV40-J53': {'x': -447, 'y': -565, 'has_fuel': False, 'traits': ['MARKETPLACE', 'PIRATE_BASE'], 'orbitals': []},
    }

    # Generate fully connected edges (complete graph)
    edges = []
    wp_list = list(waypoints.keys())
    for i, wp1 in enumerate(wp_list):
        for wp2 in wp_list[i+1:]:
            wp1_data = waypoints[wp1]
            wp2_data = waypoints[wp2]
            distance = ((wp2_data['x'] - wp1_data['x'])**2 + (wp2_data['y'] - wp1_data['y'])**2)**0.5
            edges.append({
                'from': wp1,
                'to': wp2,
                'distance': round(distance, 2),
                'type': 'normal'
            })

    return {
        'system': 'X1-JV40',
        'waypoints': waypoints,
        'edges': edges
    }


@pytest.fixture
def jv40_graph_provider(jv40_graph):
    """Mock graph provider for X1-JV40"""
    provider = Mock()
    provider.get_graph.return_value = GraphLoadResult(
        graph=jv40_graph,
        source="test",
        message="✅ Loaded X1-JV40 test graph"
    )
    return provider


@pytest.fixture
def jv40_mock_api():
    """Mock API client for J53 test"""
    api = Mock()

    def get_ship(ship_symbol):
        # Ships start at different locations to prevent clustering bias
        ship_id = ship_symbol.split('-')[-1]
        locations = {
            '3': 'X1-JV40-I50',  # Scout-3 starts at I50
            '4': 'X1-JV40-E41',  # Scout-4 starts at E41
            '5': 'X1-JV40-A1',   # Scout-5 starts at A1
        }
        return {
            'symbol': ship_symbol,
            'nav': {
                'waypointSymbol': locations.get(ship_id, 'X1-JV40-A1'),
                'systemSymbol': 'X1-JV40'
            },
            'fuel': {'current': 400, 'capacity': 400},
            'engine': {'speed': 9}
        }

    api.get_ship = Mock(side_effect=get_ship)
    api.get = Mock(return_value=None)
    api.list_waypoints = Mock(return_value={'data': []})
    return api


def test_j53_not_excluded_from_partitioning(jv40_mock_api, jv40_graph_provider):
    """
    Test that J53 is NOT excluded during geographic partitioning.

    This verifies the initial partitioning includes ALL markets including J53.
    """
    ships = ['Scout-3', 'Scout-4', 'Scout-5']
    coordinator = ScoutCoordinator(
        system='X1-JV40',
        ships=ships,
        token='test-token',
        player_id=1,
        graph_provider=jv40_graph_provider
    )

    # Get initial partitions (geographic)
    partitions = coordinator.partition_markets_geographic()

    # Count total markets in partitions
    all_markets = []
    for ship, markets in partitions.items():
        all_markets.extend(markets)

    # CRITICAL: Must have 21 markets (including J53)
    assert len(all_markets) == 21, \
        f"Expected 21 markets, but got {len(all_markets)}. Missing: {set(coordinator.markets) - set(all_markets)}"

    # Verify J53 is specifically included
    assert 'X1-JV40-J53' in all_markets, \
        "J53 must be included in partitions even though it's an extreme outlier"


def test_j53_not_dropped_during_balancing(jv40_mock_api, jv40_graph_provider):
    """
    Test that J53 survives balance_tour_times() and is not dropped.

    This is the most likely place where J53 gets lost - during the rebalancing
    logic that moves markets between scouts to equalize tour times.
    """
    ships = ['Scout-3', 'Scout-4', 'Scout-5']
    coordinator = ScoutCoordinator(
        system='X1-JV40',
        ships=ships,
        token='test-token',
        player_id=1,
        graph_provider=jv40_graph_provider
    )
    coordinator.api = jv40_mock_api

    # Get initial partitions
    partitions = coordinator.partition_markets_geographic()

    # Verify J53 is in initial partitions
    initial_markets = []
    for ship, markets in partitions.items():
        initial_markets.extend(markets)
    assert 'X1-JV40-J53' in initial_markets, "J53 should be in initial partitions"

    # Balance tour times (this is where the bug likely occurs)
    balanced_partitions = coordinator.balance_tour_times(
        partitions,
        max_iterations=50,
        variance_threshold=0.3,
        min_markets=1,
        use_tsp=False  # Use fast estimate to avoid router initialization
    )

    # Count markets after balancing
    balanced_markets = []
    for ship, markets in balanced_partitions.items():
        balanced_markets.extend(markets)

    # CRITICAL: J53 must survive balancing
    assert len(balanced_markets) == 21, \
        f"Expected 21 markets after balancing, but got {len(balanced_markets)}. Missing: {set(coordinator.markets) - set(balanced_markets)}"

    assert 'X1-JV40-J53' in balanced_markets, \
        "J53 was DROPPED during balance_tour_times()! This is the bug."


def test_j53_assignment_in_final_deployments(jv40_mock_api, jv40_graph_provider):
    """
    Test that J53 appears in final scout assignments.

    This is the end-to-end test that verifies J53 makes it all the way through
    the partitioning and balancing pipeline.
    """
    ships = ['Scout-3', 'Scout-4', 'Scout-5']
    coordinator = ScoutCoordinator(
        system='X1-JV40',
        ships=ships,
        token='test-token',
        player_id=1,
        graph_provider=jv40_graph_provider
    )
    coordinator.api = jv40_mock_api

    # Run full partitioning pipeline (geographic + balance)
    partitions = coordinator.partition_markets_geographic()
    balanced_partitions = coordinator.balance_tour_times(partitions, use_tsp=False)

    # Extract all assigned markets
    all_assigned = []
    for ship, markets in balanced_partitions.items():
        all_assigned.extend(markets)

    # Verify J53 is assigned to exactly one scout
    j53_count = all_assigned.count('X1-JV40-J53')
    assert j53_count == 1, \
        f"J53 should be assigned to exactly 1 scout, but found {j53_count} assignments"

    # Identify which scout got J53
    j53_scout = None
    for ship, markets in balanced_partitions.items():
        if 'X1-JV40-J53' in markets:
            j53_scout = ship
            break

    assert j53_scout is not None, "J53 must be assigned to a scout"
    print(f"\n✅ J53 correctly assigned to {j53_scout}")


def test_all_markets_preserved_count_invariant(jv40_mock_api, jv40_graph_provider):
    """
    Test the core invariant: market count before and after balancing must match.

    This is a general test that would catch ANY market being dropped, not just J53.
    """
    ships = ['Scout-3', 'Scout-4', 'Scout-5']
    coordinator = ScoutCoordinator(
        system='X1-JV40',
        ships=ships,
        token='test-token',
        player_id=1,
        graph_provider=jv40_graph_provider
    )
    coordinator.api = jv40_mock_api

    # Get initial partitions
    partitions = coordinator.partition_markets_geographic()

    initial_count = sum(len(markets) for markets in partitions.values())
    initial_markets_set = set()
    for markets in partitions.values():
        initial_markets_set.update(markets)

    # Balance
    balanced = coordinator.balance_tour_times(partitions, use_tsp=False)

    balanced_count = sum(len(markets) for markets in balanced.values())
    balanced_markets_set = set()
    for markets in balanced.values():
        balanced_markets_set.update(markets)

    # INVARIANT: Total market count must remain constant
    assert initial_count == balanced_count, \
        f"Market count changed during balancing: {initial_count} → {balanced_count}"

    # INVARIANT: All markets must be preserved
    missing = initial_markets_set - balanced_markets_set
    assert len(missing) == 0, \
        f"Markets were DROPPED during balancing: {missing}"

    # INVARIANT: No markets should be duplicated
    assert len(balanced_markets_set) == balanced_count, \
        "Markets were duplicated during balancing"


def test_j53_not_dropped_by_ortools_partitioner(jv40_mock_api, jv40_graph_provider):
    """
    Test that OR-Tools partitioner does NOT drop J53 despite its extreme location.

    This is the ROOT CAUSE test - OR-Tools VRP uses disjunctions with penalty 1,000,000,
    but J53's distance penalty exceeds this, causing OR-Tools to drop it as "optional".
    """
    ships = ['Scout-3', 'Scout-4', 'Scout-5']
    coordinator = ScoutCoordinator(
        system='X1-JV40',
        ships=ships,
        token='test-token',
        player_id=1,
        graph_provider=jv40_graph_provider
    )
    coordinator.api = jv40_mock_api

    # Use OR-Tools partitioner (this is where the bug occurs)
    partitions = coordinator.partition_markets_ortools()

    # Count assigned markets
    all_markets = []
    for ship, markets in partitions.items():
        all_markets.extend(markets)

    # CRITICAL: J53 must be assigned by OR-Tools partitioner
    assert len(all_markets) == 21, \
        f"OR-Tools dropped markets! Expected 21, got {len(all_markets)}. Missing: {set(coordinator.markets) - set(all_markets)}"

    assert 'X1-JV40-J53' in all_markets, \
        "OR-Tools partitioner DROPPED J53 due to disjunction penalty being too low!"
