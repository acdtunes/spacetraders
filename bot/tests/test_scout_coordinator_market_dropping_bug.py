#!/usr/bin/env python3
"""
Test to reproduce CRITICAL bug: Markets being dropped during scout coordinator partitioning.

Bug Report Context:
- Input: 21 markets in X1-JV40 (A1, A2, A3, ... J53, K78)
- After geographic partitioning: 21 markets (correct)
- After balance_tour_times(): 19 markets (A1 and J53 MISSING) ← BUG HERE
- Scout-3: B7, I50 (2 markets)
- Scout-5: 17 markets
- MISSING: A1, J53

Root Cause:
The scout coordinator uses partition_markets_geographic() followed by balance_tour_times(),
NOT OR-Tools. Markets are being dropped during the rebalancing phase.
"""

import pytest
from unittest.mock import Mock, patch
from spacetraders_bot.core.scout_coordinator import ScoutCoordinator


def test_scout_coordinator_drops_markets_during_balance():
    """
    Reproduce ACTUAL bug: balance_tour_times() drops A1 and J53 from partitions.

    This uses the EXACT markets and coordinate data from X1-JV40 production scenario
    where the bug was observed.
    """
    # EXACT markets from production scenario (21 total)
    markets = [
        'X1-JV40-A1',   # DROPPED during balance_tour_times()
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
        'X1-JV40-J53',   # DROPPED during balance_tour_times()
        'X1-JV40-K78',
    ]

    ships = ['SCOUT-3', 'SCOUT-5']

    # Real X1-JV40 waypoint coordinates (from production system)
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
        'SCOUT-3': {
            'nav': {'waypointSymbol': 'X1-JV40-I50'},
            'fuel': {'current': 400, 'capacity': 400},
            'engine': {'speed': 9}
        },
        'SCOUT-5': {
            'nav': {'waypointSymbol': 'X1-JV40-A1'},
            'fuel': {'current': 400, 'capacity': 400},
            'engine': {'speed': 9}
        }
    }

    # Create mock coordinator
    with patch('spacetraders_bot.core.scout_coordinator.APIClient'):
        with patch('spacetraders_bot.core.scout_coordinator.DaemonManager'):
            with patch('spacetraders_bot.core.scout_coordinator.AssignmentManager'):
                with patch('spacetraders_bot.core.scout_coordinator.SystemGraphProvider') as mock_provider:
                    # Mock graph provider to return our test graph
                    mock_result = Mock()
                    mock_result.graph = graph
                    mock_result.message = None
                    mock_provider.return_value.get_graph.return_value = mock_result

                    coordinator = ScoutCoordinator(
                        system='X1-JV40',
                        ships=ships,
                        token='fake-token',
                        player_id=1
                    )

                    # Override markets and ship data cache
                    coordinator.markets = markets
                    coordinator._ship_data_cache = ship_data

                    # Step 1: Geographic partitioning (should preserve all 21 markets)
                    print("\n📍 Step 1: Geographic partitioning...")
                    partitions_geo = coordinator.partition_markets_geographic()

                    geo_count = sum(len(m) for m in partitions_geo.values())
                    print(f"   Total markets after geographic: {geo_count}")
                    for ship, ship_markets in partitions_geo.items():
                        print(f"   {ship}: {len(ship_markets)} markets")

                    # Validate geographic partitioning preserved all markets
                    assert geo_count == 21, f"Geographic partitioning already dropped markets! Expected 21, got {geo_count}"

                    # Step 2: Balance tour times (THIS IS WHERE BUG HAPPENS)
                    print("\n⚖️  Step 2: Balance tour times...")
                    partitions_balanced = coordinator.balance_tour_times(
                        partitions_geo,
                        use_tsp=False  # Use fast estimate, same as partition_and_start()
                    )

                    # Count markets after balancing
                    balanced_count = sum(len(m) for m in partitions_balanced.values())
                    print(f"   Total markets after balancing: {balanced_count}")

                    # Collect all assigned markets
                    assigned_markets = []
                    for ship, ship_markets in partitions_balanced.items():
                        assigned_markets.extend(ship_markets)
                        print(f"   {ship}: {len(ship_markets)} markets - {ship_markets}")

                    # CRITICAL ASSERTION: Must have all 21 markets
                    missing_markets = set(markets) - set(assigned_markets)

                    if missing_markets:
                        print(f"\n❌ BUG REPRODUCED: {len(missing_markets)} markets DROPPED during balance_tour_times()!")
                        print(f"   Missing: {missing_markets}")
                        pytest.fail(f"Markets dropped: {missing_markets}")

                    assert balanced_count == 21, \
                        f"Expected 21 markets, got {balanced_count}. Missing: {missing_markets}"

                    # CRITICAL: A1 must be included
                    assert 'X1-JV40-A1' in assigned_markets, \
                        "A1 was DROPPED during balance_tour_times()!"

                    # CRITICAL: J53 must be included
                    assert 'X1-JV40-J53' in assigned_markets, \
                        "J53 was DROPPED during balance_tour_times()!"

                    print(f"\n✅ All {len(markets)} markets preserved!")


def test_balance_tour_times_preserves_all_markets_simple():
    """
    Simpler test: balance_tour_times() must preserve ALL input markets.

    This is a fundamental invariant - rebalancing should move markets between
    ships but NEVER drop them entirely.
    """
    # Simple 5-market scenario
    markets = ['A', 'B', 'C', 'D', 'E']
    ships = ['SHIP-1', 'SHIP-2']

    waypoints = {
        'A': {'x': 0, 'y': 0, 'has_fuel': True},
        'B': {'x': 10, 'y': 0, 'has_fuel': True},
        'C': {'x': 20, 'y': 0, 'has_fuel': True},
        'D': {'x': 30, 'y': 0, 'has_fuel': True},
        'E': {'x': 40, 'y': 0, 'has_fuel': True},
    }

    graph = {
        'system': 'TEST',
        'waypoints': waypoints,
        'edges': []
    }

    ship_data = {
        'SHIP-1': {
            'nav': {'waypointSymbol': 'A'},
            'fuel': {'current': 400, 'capacity': 400},
            'engine': {'speed': 9}
        },
        'SHIP-2': {
            'nav': {'waypointSymbol': 'E'},
            'fuel': {'current': 400, 'capacity': 400},
            'engine': {'speed': 9}
        }
    }

    # Start with unbalanced partition
    initial_partitions = {
        'SHIP-1': ['A', 'B', 'C', 'D'],  # 4 markets
        'SHIP-2': ['E']                   # 1 market
    }

    with patch('spacetraders_bot.core.scout_coordinator.APIClient'):
        with patch('spacetraders_bot.core.scout_coordinator.DaemonManager'):
            with patch('spacetraders_bot.core.scout_coordinator.AssignmentManager'):
                with patch('spacetraders_bot.core.scout_coordinator.SystemGraphProvider') as mock_provider:
                    mock_result = Mock()
                    mock_result.graph = graph
                    mock_result.message = None
                    mock_provider.return_value.get_graph.return_value = mock_result

                    coordinator = ScoutCoordinator(
                        system='TEST',
                        ships=ships,
                        token='fake-token',
                        player_id=1
                    )

                    coordinator.markets = markets
                    coordinator._ship_data_cache = ship_data

                    # Run balance_tour_times
                    balanced = coordinator.balance_tour_times(
                        initial_partitions,
                        use_tsp=False,
                        max_iterations=10
                    )

                    # Count markets
                    assigned = []
                    for ship_markets in balanced.values():
                        assigned.extend(ship_markets)

                    # CRITICAL: Must preserve all 5 markets
                    missing = set(markets) - set(assigned)
                    assert len(assigned) == 5, \
                        f"Markets dropped! Expected 5, got {len(assigned)}. Missing: {missing}"

                    assert set(assigned) == set(markets), \
                        f"Market set changed! Missing: {set(markets) - set(assigned)}, Extra: {set(assigned) - set(markets)}"


def test_partition_and_start_validates_markets():
    """
    Test that partition_and_start() enforces market preservation validation.

    This is a preventive measure to catch market dropping bugs early.
    """
    markets = ['A', 'B', 'C', 'D', 'E']
    ships = ['SHIP-1', 'SHIP-2']

    waypoints = {
        'A': {'x': 0, 'y': 0, 'has_fuel': True},
        'B': {'x': 10, 'y': 0, 'has_fuel': True},
        'C': {'x': 20, 'y': 0, 'has_fuel': True},
        'D': {'x': 30, 'y': 0, 'has_fuel': True},
        'E': {'x': 40, 'y': 0, 'has_fuel': True},
    }

    graph = {
        'system': 'TEST',
        'waypoints': waypoints,
        'edges': []
    }

    ship_data = {
        'SHIP-1': {
            'nav': {'waypointSymbol': 'A'},
            'fuel': {'current': 400, 'capacity': 400},
            'engine': {'speed': 9}
        },
        'SHIP-2': {
            'nav': {'waypointSymbol': 'E'},
            'fuel': {'current': 400, 'capacity': 400},
            'engine': {'speed': 9}
        }
    }

    with patch('spacetraders_bot.core.scout_coordinator.APIClient') as mock_api:
        with patch('spacetraders_bot.core.scout_coordinator.DaemonManager'):
            with patch('spacetraders_bot.core.scout_coordinator.AssignmentManager'):
                with patch('spacetraders_bot.core.scout_coordinator.SystemGraphProvider') as mock_provider:
                    mock_result = Mock()
                    mock_result.graph = graph
                    mock_result.message = None
                    mock_provider.return_value.get_graph.return_value = mock_result

                    # Mock api.get_ship() to return ship data
                    mock_api.return_value.get_ship = Mock(side_effect=lambda ship: ship_data[ship])

                    coordinator = ScoutCoordinator(
                        system='TEST',
                        ships=ships,
                        token='fake-token',
                        player_id=1
                    )

                    coordinator.markets = markets
                    coordinator._ship_data_cache = ship_data

                    # Mock balance_tour_times to SIMULATE market dropping bug
                    def buggy_balance(partitions, **kwargs):
                        # Simulate bug: drop market 'C'
                        return {
                            'SHIP-1': ['A', 'B'],      # Missing 'C'
                            'SHIP-2': ['D', 'E']
                        }

                    coordinator.balance_tour_times = buggy_balance

                    # partition_and_start() should RAISE RuntimeError due to validation
                    with pytest.raises(RuntimeError, match="markets dropped during partitioning"):
                        coordinator.partition_and_start()

                    print("\n✅ Validation correctly caught simulated market dropping bug!")


if __name__ == '__main__':
    pytest.main([__file__, '-v', '-s'])
