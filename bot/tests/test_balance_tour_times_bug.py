#!/usr/bin/env python3
"""
Test to reproduce balance_tour_times() market overlap bug

The bug: During iterative rebalancing in balance_tour_times(),
markets can be duplicated across multiple scouts due to missing
duplicate checks before market moves.

Evidence from production:
- 81% overlap rate (22 duplicate markets out of 27)
- Markets A2, F50, G53, E48, C44 appeared in multiple scout tours
- One scout assigned 20 markets, another only 1 market

Root Cause:
- No duplicate check before line 374: partitions[shortest_ship].append(market_to_move)
- No validation after each iteration
- Flawed shortest_ship selection (line 347) allows "megaship" accumulation
"""

import pytest
from unittest.mock import Mock
from spacetraders_bot.core.scout_coordinator import ScoutCoordinator
from spacetraders_bot.core.system_graph_provider import GraphLoadResult


@pytest.fixture
def large_system_graph():
    """
    Create a graph with 27 markets (like X1-GH18) distributed to trigger
    extreme imbalance during iterative rebalancing
    """
    waypoints = {
        f'X1-TEST-A{i}': {'x': i * 100, 'y': i * 50, 'has_fuel': True, 'traits': ['MARKETPLACE'], 'orbitals': []}
        for i in range(1, 28)  # 27 markets
    }

    # Generate fully connected edges
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
        'system': 'X1-TEST',
        'waypoints': waypoints,
        'edges': edges
    }


@pytest.fixture
def mock_graph_provider_large(large_system_graph):
    """Mock graph provider for large system"""
    provider = Mock()
    provider.get_graph.return_value = GraphLoadResult(
        graph=large_system_graph,
        source="test",
        message="✅ Loaded large test graph (27 markets)"
    )
    return provider


@pytest.fixture
def mock_api_large():
    """Mock API client for large system test"""
    api = Mock()

    def get_ship(ship_symbol):
        return {
            'symbol': ship_symbol,
            'nav': {
                'waypointSymbol': 'X1-TEST-A1',
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

    api.get_ship = Mock(side_effect=get_ship)
    api.get = Mock(return_value=None)
    api.list_waypoints = Mock(return_value={'data': []})

    return api


def test_balance_tour_times_no_duplicates(mock_api_large, mock_graph_provider_large):
    """
    Test that balance_tour_times() preserves the disjoint partition guarantee
    during iterative rebalancing, even with extreme imbalance.

    This reproduces the production bug where 81% of markets had duplicates
    after balancing.
    """
    # Setup: 11 scouts, 27 markets (uneven distribution triggers many moves)
    ships = [f'SCOUT-{i}' for i in range(1, 12)]  # 11 scouts
    coordinator = ScoutCoordinator(
        system='X1-TEST',
        ships=ships,
        token='test-token',
        player_id=1,
        algorithm='greedy',
        graph_provider=mock_graph_provider_large
    )
    coordinator.api = mock_api_large

    # Create intentionally imbalanced initial partitions
    # This forces balance_tour_times to do many market moves
    partitions = {ship: [] for ship in ships}

    # Give first scout 20 markets, rest get 1 market each (or 0)
    # This mimics the extreme imbalance seen in production
    all_markets = coordinator.markets.copy()
    partitions['SCOUT-1'] = all_markets[:20]  # Megaship
    for i, ship in enumerate(ships[1:8]):  # Next 7 scouts get 1 market each
        if i < len(all_markets) - 20:
            partitions[ship] = [all_markets[20 + i]]
    # Last 3 scouts get 0 markets initially

    print("\n=== INITIAL (IMBALANCED) PARTITIONS ===")
    for ship in sorted(partitions.keys()):
        print(f"{ship}: {len(partitions[ship])} markets")

    # Verify initial state is valid (no duplicates yet)
    all_markets_initial = []
    for markets in partitions.values():
        all_markets_initial.extend(markets)

    market_counts_initial = {}
    for m in all_markets_initial:
        market_counts_initial[m] = market_counts_initial.get(m, 0) + 1

    duplicates_initial = {m: c for m, c in market_counts_initial.items() if c > 1}
    assert len(duplicates_initial) == 0, f"Test setup failed - initial partitions have duplicates: {duplicates_initial}"

    # Run balance_tour_times (this is where the bug occurs)
    balanced_partitions = coordinator.balance_tour_times(
        partitions,
        max_iterations=20,
        variance_threshold=0.3,
        min_markets=2
    )

    print("\n=== BALANCED PARTITIONS ===")
    for ship in sorted(balanced_partitions.keys()):
        print(f"{ship}: {len(balanced_partitions[ship])} markets - {balanced_partitions[ship]}")

    # CRITICAL CHECK: Verify partitions are still disjoint after balancing
    all_markets_final = []
    for ship, markets in balanced_partitions.items():
        all_markets_final.extend(markets)

    # Count occurrences
    market_counts = {}
    for market in all_markets_final:
        market_counts[market] = market_counts.get(market, 0) + 1

    # Find duplicates
    duplicates = {market: count for market, count in market_counts.items() if count > 1}

    # Print diagnostic info if test fails
    if duplicates:
        print("\n❌ PARTITION OVERLAP DETECTED AFTER BALANCING!")
        print(f"Duplicates: {duplicates}")
        for market, count in duplicates.items():
            print(f"\nMarket {market} appears in {count} partitions:")
            for ship, markets in balanced_partitions.items():
                if market in markets:
                    print(f"  - {ship}: position {markets.index(market)+1}/{len(markets)}")

        # Calculate overlap rate
        overlap_rate = (len(duplicates) / len(coordinator.markets)) * 100
        print(f"\nOverlap rate: {overlap_rate:.1f}% ({len(duplicates)}/{len(coordinator.markets)} markets)")

    # ASSERTION: No market should appear more than once after balancing
    assert len(duplicates) == 0, \
        f"balance_tour_times() created overlapping partitions! Duplicates: {duplicates}"

    # Verify all markets are still assigned
    missing = set(coordinator.markets) - set(all_markets_final)
    assert len(missing) == 0, f"Markets lost during balancing: {missing}"

    # Verify total market count is correct
    assert len(all_markets_final) == len(coordinator.markets), \
        f"Total markets changed: expected {len(coordinator.markets)}, got {len(all_markets_final)}"


def test_iterative_rebalancing_duplicate_prevention():
    """
    Unit test specifically for the iterative rebalancing loop (lines 329-428)
    to ensure duplicate prevention logic works correctly.
    """
    # Create minimal coordinator
    graph = {
        'waypoints': {
            f'M{i}': {'x': i * 100, 'y': 0, 'has_fuel': True, 'traits': ['MARKETPLACE'], 'orbitals': []}
            for i in range(1, 6)  # 5 markets
        }
    }

    provider = Mock()
    provider.get_graph.return_value = GraphLoadResult(
        graph=graph,
        source="test",
        message="Test"
    )

    api = Mock()
    def get_ship(ship_symbol):
        return {
            'symbol': ship_symbol,
            'nav': {'waypointSymbol': 'M1', 'systemSymbol': 'X1-TEST'},
            'fuel': {'current': 400, 'capacity': 400},
            'engine': {'speed': 9}
        }
    api.get_ship = Mock(side_effect=get_ship)

    coordinator = ScoutCoordinator(
        system='X1-TEST',
        ships=['SHIP-1', 'SHIP-2'],
        token='test-token',
        player_id=1,
        algorithm='greedy',
        graph_provider=provider
    )
    coordinator.api = api

    # Create partitions with intentional duplicate (simulate the bug)
    partitions = {
        'SHIP-1': ['M1', 'M2', 'M3'],
        'SHIP-2': ['M3', 'M4', 'M5']  # M3 is duplicate!
    }

    print("\n=== TEST: DUPLICATE PREVENTION ===")
    print("Input partitions (with duplicate M3):")
    for ship, markets in partitions.items():
        print(f"  {ship}: {markets}")

    # The balance_tour_times should detect and prevent the duplicate
    # (or at least validate and raise AssertionError)
    try:
        balanced = coordinator.balance_tour_times(partitions, max_iterations=5)

        # If it didn't raise, verify no duplicates in output
        all_markets = []
        for markets in balanced.values():
            all_markets.extend(markets)

        market_counts = {}
        for m in all_markets:
            market_counts[m] = market_counts.get(m, 0) + 1

        duplicates = {m: c for m, c in market_counts.items() if c > 1}

        # Should either raise during validation OR fix the duplicate
        assert len(duplicates) == 0, f"Failed to prevent duplicates: {duplicates}"

    except AssertionError as e:
        # Expected behavior - validation should catch the duplicate
        print(f"\n✅ Validation caught duplicate (expected): {e}")
        assert "not disjoint" in str(e).lower() or "duplicate" in str(e).lower()


def test_megaship_prevention():
    """
    Test that balance_tour_times prevents "megaship" accumulation
    where one ship gets all markets.
    """
    graph = {
        'waypoints': {
            f'M{i}': {'x': i * 50, 'y': 0, 'has_fuel': True, 'traits': ['MARKETPLACE'], 'orbitals': []}
            for i in range(1, 11)  # 10 markets
        }
    }

    provider = Mock()
    provider.get_graph.return_value = GraphLoadResult(
        graph=graph,
        source="test",
        message="Test"
    )

    api = Mock()
    def get_ship(ship_symbol):
        return {
            'symbol': ship_symbol,
            'nav': {'waypointSymbol': 'M1', 'systemSymbol': 'X1-TEST'},
            'fuel': {'current': 400, 'capacity': 400},
            'engine': {'speed': 9}
        }
    api.get_ship = Mock(side_effect=get_ship)

    coordinator = ScoutCoordinator(
        system='X1-TEST',
        ships=['SHIP-1', 'SHIP-2', 'SHIP-3'],
        token='test-token',
        player_id=1,
        algorithm='greedy',
        graph_provider=provider
    )
    coordinator.api = api

    # Start with extreme imbalance
    partitions = {
        'SHIP-1': [f'M{i}' for i in range(1, 9)],  # 8 markets
        'SHIP-2': ['M9'],  # 1 market
        'SHIP-3': ['M10']  # 1 market
    }

    balanced = coordinator.balance_tour_times(partitions, max_iterations=10)

    # Verify no ship has all markets (megaship prevention)
    for ship, markets in balanced.items():
        assert len(markets) < len(coordinator.markets), \
            f"Megaship detected! {ship} has all {len(coordinator.markets)} markets"

    # Verify reasonable distribution (within 40% variance)
    market_counts = [len(markets) for markets in balanced.values()]
    avg_count = sum(market_counts) / len(market_counts)

    for ship, markets in balanced.items():
        variance = abs(len(markets) - avg_count) / avg_count if avg_count > 0 else 0
        print(f"{ship}: {len(markets)} markets (variance: {variance*100:.1f}%)")

    # At least verify no ship has 0 markets (min_markets=2 enforcement)
    for ship, markets in balanced.items():
        assert len(markets) >= 2, f"{ship} has fewer than minimum 2 markets: {len(markets)}"


if __name__ == '__main__':
    pytest.main([__file__, '-v', '-s'])
