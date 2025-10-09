#!/usr/bin/env python3
"""
Test scout coordinator market partitioning to ensure disjoint tours
"""

import pytest
from unittest.mock import Mock, MagicMock
from spacetraders_bot.core.scout_coordinator import ScoutCoordinator
from spacetraders_bot.core.system_graph_provider import GraphLoadResult


@pytest.fixture
def mock_graph():
    """Create a mock system graph with multiple markets"""
    waypoints = {
        'X1-TEST-A2': {'x': 100, 'y': 100, 'has_fuel': True, 'traits': ['MARKETPLACE'], 'orbitals': []},
        'X1-TEST-B5': {'x': 200, 'y': 150, 'has_fuel': True, 'traits': ['MARKETPLACE'], 'orbitals': []},
        'X1-TEST-C7': {'x': 300, 'y': 200, 'has_fuel': False, 'traits': ['MARKETPLACE'], 'orbitals': []},
        'X1-TEST-D9': {'x': 400, 'y': 250, 'has_fuel': True, 'traits': ['MARKETPLACE'], 'orbitals': []},
        'X1-TEST-E47': {'x': 150, 'y': 300, 'has_fuel': False, 'traits': ['MARKETPLACE'], 'orbitals': []},
        'X1-TEST-I59': {'x': 250, 'y': 350, 'has_fuel': True, 'traits': ['MARKETPLACE'], 'orbitals': []},
        'X1-TEST-I60': {'x': 350, 'y': 400, 'has_fuel': False, 'traits': ['MARKETPLACE'], 'orbitals': []},
        'X1-TEST-K95': {'x': 450, 'y': 450, 'has_fuel': True, 'traits': ['MARKETPLACE'], 'orbitals': []},
    }

    # Generate fully connected edges (complete graph)
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
        'system': 'X1-TEST',
        'waypoints': waypoints,
        'edges': edges
    }


@pytest.fixture
def mock_graph_provider(mock_graph):
    """Mock graph provider"""
    provider = Mock()
    provider.get_graph.return_value = GraphLoadResult(
        graph=mock_graph,
        source="test",
        message="✅ Loaded graph from test fixture"
    )
    return provider


@pytest.fixture
def mock_api():
    """Mock API client that returns ship data"""
    api = Mock()

    # All ships start at A2 (the common starting location that triggers the bug)
    def get_ship(ship_symbol):
        return {
            'symbol': ship_symbol,
            'nav': {
                'waypointSymbol': 'X1-TEST-A2',  # All ships at A2 initially
                'systemSymbol': 'X1-TEST'
            },
            'fuel': {
                'current': 400,
                'capacity': 400
            },
            'engine': {
                'speed': 9
            }
        }

    # Mock get_ship to return ship data for coordinator
    api.get_ship = Mock(side_effect=get_ship)

    # Mock other methods that might be called
    api.get = Mock(return_value=None)  # Prevent real API calls
    api.list_waypoints = Mock(return_value={'data': []})

    return api


def test_disjoint_partitions_with_common_start_location(mock_api, mock_graph_provider):
    """
    Test that scout coordinator creates truly disjoint market partitions
    even when all ships start at the same waypoint.

    This is a regression test for the bug where all scouts returned to A2
    because they all started there, violating the non-overlapping guarantee.
    """
    # Setup coordinator with 4 ships all starting at A2
    ships = ['SHIP-2', 'SHIP-3', 'SHIP-4', 'SHIP-5']
    coordinator = ScoutCoordinator(
        system='X1-TEST',
        ships=ships,
        token='test-token',
        player_id=1,
        algorithm='greedy',
        graph_provider=mock_graph_provider
    )

    # Get geographic partitions
    partitions = coordinator.partition_markets_geographic()

    # Verify we have partitions for all ships
    assert len(partitions) == 4, "Should have 4 partitions for 4 ships"

    # Collect all markets across all partitions
    all_partitioned_markets = set()
    for ship, markets in partitions.items():
        all_partitioned_markets.update(markets)

    # Verify partitions are disjoint (no market appears twice)
    total_markets = sum(len(markets) for markets in partitions.values())
    assert len(all_partitioned_markets) == total_markets, \
        "Partitions must be disjoint - no market should appear in multiple partitions"

    # Now optimize subtours for each partition
    # This is where the bug occurred - optimize_subtour would use current_location
    # instead of partition centroid, causing all tours to return to A2
    optimized_tours = {}
    for ship, markets in partitions.items():
        if markets:
            # Mock the TourOptimizer to track what start point is used
            tour = coordinator.optimize_subtour(ship, markets)
            if tour:
                optimized_tours[ship] = tour

    # After optimization, verify that tours are still disjoint
    # Extract the markets visited in each tour (excluding return-to-start)
    tour_markets = {}
    for ship, tour in optimized_tours.items():
        # Get tour route order from the optimized tour
        # The tour structure includes 'legs' with navigation steps
        if 'legs' in tour:
            visited = set()
            for leg in tour['legs']:
                # Each leg has a 'goal' which is the destination
                if 'goal' in leg:
                    visited.add(leg['goal'])
            tour_markets[ship] = visited

    # Verify tours don't overlap (except possibly at start/end for continuous mode)
    # For truly disjoint tours, no two ships should visit the same market
    all_tour_markets = []
    for ship, visited in tour_markets.items():
        all_tour_markets.extend(visited)

    # Count occurrences of each market
    market_counts = {}
    for market in all_tour_markets:
        market_counts[market] = market_counts.get(market, 0) + 1

    # Check for overlap - identify markets visited by multiple ships
    overlapping_markets = {market: count for market, count in market_counts.items() if count > 1}

    # CRITICAL ASSERTION: No market should appear in multiple tours
    # (except for the shared starting location which is handled separately)
    assert len(overlapping_markets) == 0, \
        f"Tours must be disjoint! Markets visited by multiple ships: {overlapping_markets}"


def test_partition_balance_preserves_disjoint_property(mock_api, mock_graph_provider):
    """
    Test that balance_tour_times() doesn't break the disjoint partition guarantee
    """
    ships = ['SHIP-2', 'SHIP-3', 'SHIP-4', 'SHIP-5']
    coordinator = ScoutCoordinator(
        system='X1-TEST',
        ships=ships,
        token='test-token',
        player_id=1,
        algorithm='greedy',
        graph_provider=mock_graph_provider
    )
    # Inject mock API to prevent real HTTP calls
    coordinator.api = mock_api

    # Get initial partitions
    partitions = coordinator.partition_markets_geographic()

    # Balance tour times
    balanced_partitions = coordinator.balance_tour_times(partitions, max_iterations=5)

    # Verify balanced partitions are still disjoint
    all_markets = []
    for ship, markets in balanced_partitions.items():
        all_markets.extend(markets)

    # Count occurrences
    market_counts = {}
    for market in all_markets:
        market_counts[market] = market_counts.get(market, 0) + 1

    overlapping = {m: c for m, c in market_counts.items() if c > 1}

    assert len(overlapping) == 0, \
        f"balance_tour_times() broke disjoint property! Overlapping markets: {overlapping}"


def test_centroid_based_start_location(mock_api, mock_graph_provider):
    """
    Test that optimize_subtour uses partition centroid instead of current_location
    as the starting point for tour optimization.
    """
    coordinator = ScoutCoordinator(
        system='X1-TEST',
        ships=['SHIP-2'],
        token='test-token',
        player_id=1,
        algorithm='greedy',
        graph_provider=mock_graph_provider
    )
    # Inject mock API to prevent real HTTP calls
    coordinator.api = mock_api

    # Partition with specific markets far from A2
    test_markets = ['X1-TEST-I60', 'X1-TEST-K95']  # Both far from A2 (100,100)

    # Get ship data - it's at A2 (100, 100)
    ship_data = mock_api.get_ship('SHIP-2')
    assert ship_data['nav']['waypointSymbol'] == 'X1-TEST-A2', "Ship should start at A2"

    # Optimize subtour
    tour = coordinator.optimize_subtour('SHIP-2', test_markets)

    assert tour is not None, "Tour should be generated"

    # The tour should NOT start from A2
    # Instead, it should start from the market closest to the centroid of [I60, K95]
    # Centroid of I60 (350, 400) and K95 (450, 450) = (400, 425)
    # Closest market to (400, 425) is K95 (450, 450) with dist ~56
    # So tour should start from K95, not A2

    # Extract first leg's starting point
    if 'legs' in tour and len(tour['legs']) > 0:
        first_leg = tour['legs'][0]
        if 'start' in first_leg:
            start_point = first_leg['start']
            # Start point should be one of the assigned markets, NOT A2
            assert start_point in test_markets, \
                f"Tour should start from partition centroid ({test_markets}), not A2. Got: {start_point}"
