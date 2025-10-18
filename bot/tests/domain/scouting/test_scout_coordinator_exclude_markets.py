#!/usr/bin/env python3
"""
Test scout coordinator exclude_markets parameter

Ensures that manually deployed stationary scouts at specific markets
don't conflict with touring scouts by allowing markets to be excluded
from auto-discovery and partitioning.
"""

import pytest
from unittest.mock import Mock, patch, MagicMock
from spacetraders_bot.core.scout_coordinator import ScoutCoordinator


@pytest.fixture
def mock_api():
    """Mock API client"""
    api = Mock()
    api.get_ship = Mock(return_value={
        'symbol': 'SCOUT-1',
        'nav': {'waypointSymbol': 'X1-TX46-A1'},
        'fuel': {'current': 400, 'capacity': 400},
        'engine': {'speed': 10},
        'cargo': {'capacity': 10}
    })
    return api


@pytest.fixture
def mock_graph_with_20_markets():
    """
    Mock graph with 20 markets including I52 and J55 (stationary scouts)
    """
    markets = [
        'X1-TX46-A1', 'X1-TX46-B7', 'X1-TX46-C3', 'X1-TX46-D4',
        'X1-TX46-E5', 'X1-TX46-F6', 'X1-TX46-G47', 'X1-TX46-H8',
        'X1-TX46-I52', 'X1-TX46-J55', 'X1-TX46-K9', 'X1-TX46-L10',
        'X1-TX46-M11', 'X1-TX46-N12', 'X1-TX46-O13', 'X1-TX46-P14',
        'X1-TX46-Q15', 'X1-TX46-R16', 'X1-TX46-S17', 'X1-TX46-T18'
    ]

    waypoints = {}
    for idx, market in enumerate(markets):
        waypoints[market] = {
            'symbol': market,
            'x': idx * 100,  # Spread out for testing
            'y': idx * 50,
            'type': 'PLANET',
            'traits': ['MARKETPLACE']  # Must be list of strings, not dictionaries
        }

    return {
        'waypoints': waypoints,
        'markets': markets
    }


def test_scout_coordinator_without_exclusions(mock_api, mock_graph_with_20_markets):
    """
    Scenario: Scout coordinator auto-discovers all markets without exclusions

    Given a system with 20 markets including I52 and J55
    When the scout coordinator is initialized without exclude_markets
    Then all 20 markets should be discovered
    And I52 and J55 should be included in the market list
    """
    with patch('spacetraders_bot.core.scout_coordinator.SystemGraphProvider') as MockProvider:
        # Setup mock graph provider
        mock_provider = MockProvider.return_value
        mock_result = Mock()
        mock_result.graph = mock_graph_with_20_markets
        mock_result.message = None
        mock_provider.get_graph.return_value = mock_result

        # Initialize coordinator WITHOUT exclude_markets
        coordinator = ScoutCoordinator(
            system='X1-TX46',
            ships=['SCOUT-1', 'SCOUT-2'],
            token='test-token',
            player_id=6,
            graph_provider=mock_provider
        )

        # Verify all 20 markets are discovered
        assert len(coordinator.markets) == 20
        assert 'X1-TX46-I52' in coordinator.markets
        assert 'X1-TX46-J55' in coordinator.markets
        assert 'X1-TX46-G47' in coordinator.markets


def test_scout_coordinator_with_exclusions(mock_api, mock_graph_with_20_markets):
    """
    Scenario: Scout coordinator excludes manually deployed stationary scouts

    Given a system with 20 markets including I52 and J55
    And I52 and J55 have manually deployed stationary scouts (60s polling)
    When the scout coordinator is initialized with exclude_markets=['X1-TX46-I52', 'X1-TX46-J55']
    Then only 18 markets should be assigned to touring scouts
    And I52 and J55 should NOT be in the market list
    And G47 should still be included (not excluded)
    """
    with patch('spacetraders_bot.core.scout_coordinator.SystemGraphProvider') as MockProvider:
        # Setup mock graph provider
        mock_provider = MockProvider.return_value
        mock_result = Mock()
        mock_result.graph = mock_graph_with_20_markets
        mock_result.message = None
        mock_provider.get_graph.return_value = mock_result

        # Initialize coordinator WITH exclude_markets
        coordinator = ScoutCoordinator(
            system='X1-TX46',
            ships=['SCOUT-1', 'SCOUT-2', 'SCOUT-3', 'SCOUT-4'],
            token='test-token',
            player_id=6,
            exclude_markets=['X1-TX46-I52', 'X1-TX46-J55'],
            graph_provider=mock_provider
        )

        # Verify only 18 markets are discovered (20 - 2 excluded)
        assert len(coordinator.markets) == 18

        # Verify excluded markets are NOT in the list
        assert 'X1-TX46-I52' not in coordinator.markets
        assert 'X1-TX46-J55' not in coordinator.markets

        # Verify non-excluded markets ARE still in the list
        assert 'X1-TX46-G47' in coordinator.markets
        assert 'X1-TX46-A1' in coordinator.markets


def test_scout_coordinator_partitioning_respects_exclusions(mock_api, mock_graph_with_20_markets):
    """
    Scenario: Market partitioning respects exclusions (no duplicates or missing markets)

    Given a scout coordinator with exclude_markets=['X1-TX46-I52', 'X1-TX46-J55']
    When markets are partitioned across 8 scouts
    Then 18 markets should be assigned (20 total - 2 excluded)
    And I52 and J55 should NOT appear in ANY scout's partition
    And all non-excluded markets should be assigned exactly once
    And no market should be assigned to multiple scouts
    """
    with patch('spacetraders_bot.core.scout_coordinator.SystemGraphProvider') as MockProvider:
        # Setup mock graph provider
        mock_provider = MockProvider.return_value
        mock_result = Mock()
        mock_result.graph = mock_graph_with_20_markets
        mock_result.message = None
        mock_provider.get_graph.return_value = mock_result

        # Initialize coordinator WITH exclude_markets
        ships = [
            'STARHOPPER-2', 'STARHOPPER-3', 'STARHOPPER-4', 'STARHOPPER-5',
            'STARHOPPER-6', 'STARHOPPER-10', 'STARHOPPER-11', 'STARHOPPER-12'
        ]

        coordinator = ScoutCoordinator(
            system='X1-TX46',
            ships=ships,
            token='test-token',
            player_id=6,
            exclude_markets=['X1-TX46-I52', 'X1-TX46-J55'],
            graph_provider=mock_provider
        )

        # Mock ship data for all ships
        def mock_get_ship(ship_symbol):
            return {
                'symbol': ship_symbol,
                'nav': {'waypointSymbol': 'X1-TX46-A1'},
                'fuel': {'current': 400, 'capacity': 400},
                'engine': {'speed': 10},
                'cargo': {'capacity': 10}
            }

        coordinator.api.get_ship = Mock(side_effect=mock_get_ship)

        # Perform geographic partitioning
        partitions = coordinator.partition_markets_geographic()

        # Count total markets assigned
        all_assigned_markets = []
        for ship, markets in partitions.items():
            all_assigned_markets.extend(markets)

        # Verify total count (18 markets, not 20)
        assert len(all_assigned_markets) == 18

        # Verify excluded markets are NOT in partitions
        assert 'X1-TX46-I52' not in all_assigned_markets
        assert 'X1-TX46-J55' not in all_assigned_markets

        # Verify no duplicates (each market assigned exactly once)
        assert len(all_assigned_markets) == len(set(all_assigned_markets))

        # Verify G47 is included (sanity check for non-excluded market)
        assert 'X1-TX46-G47' in all_assigned_markets


def test_scout_coordinator_exclude_markets_none():
    """
    Scenario: exclude_markets parameter is optional (None by default)

    Given a scout coordinator initialized without exclude_markets
    Then the coordinator should work correctly (no filtering)
    And all markets should be discovered
    """
    with patch('spacetraders_bot.core.scout_coordinator.SystemGraphProvider') as MockProvider:
        mock_provider = MockProvider.return_value
        mock_result = Mock()
        mock_result.graph = {
            'waypoints': {
                'X1-TX46-A1': {'symbol': 'X1-TX46-A1', 'x': 0, 'y': 0, 'type': 'PLANET', 'traits': ['MARKETPLACE']},
                'X1-TX46-B7': {'symbol': 'X1-TX46-B7', 'x': 100, 'y': 50, 'type': 'PLANET', 'traits': ['MARKETPLACE']}
            },
            'markets': ['X1-TX46-A1', 'X1-TX46-B7']
        }
        mock_result.message = None
        mock_provider.get_graph.return_value = mock_result

        # Initialize WITHOUT exclude_markets (None/default)
        coordinator = ScoutCoordinator(
            system='X1-TX46',
            ships=['SCOUT-1'],
            token='test-token',
            player_id=6,
            graph_provider=mock_provider
        )

        # Verify no filtering occurred
        assert len(coordinator.markets) == 2
        assert 'X1-TX46-A1' in coordinator.markets
        assert 'X1-TX46-B7' in coordinator.markets


def test_scout_coordinator_exclude_markets_empty_list():
    """
    Scenario: exclude_markets parameter is an empty list

    Given a scout coordinator initialized with exclude_markets=[]
    Then the coordinator should work correctly (no filtering)
    And all markets should be discovered
    """
    with patch('spacetraders_bot.core.scout_coordinator.SystemGraphProvider') as MockProvider:
        mock_provider = MockProvider.return_value
        mock_result = Mock()
        mock_result.graph = {
            'waypoints': {
                'X1-TX46-A1': {'symbol': 'X1-TX46-A1', 'x': 0, 'y': 0, 'type': 'PLANET', 'traits': ['MARKETPLACE']},
                'X1-TX46-B7': {'symbol': 'X1-TX46-B7', 'x': 100, 'y': 50, 'type': 'PLANET', 'traits': ['MARKETPLACE']}
            },
            'markets': ['X1-TX46-A1', 'X1-TX46-B7']
        }
        mock_result.message = None
        mock_provider.get_graph.return_value = mock_result

        # Initialize WITH empty exclude_markets list
        coordinator = ScoutCoordinator(
            system='X1-TX46',
            ships=['SCOUT-1'],
            token='test-token',
            player_id=6,
            exclude_markets=[],
            graph_provider=mock_provider
        )

        # Verify no filtering occurred
        assert len(coordinator.markets) == 2
        assert 'X1-TX46-A1' in coordinator.markets
        assert 'X1-TX46-B7' in coordinator.markets


if __name__ == '__main__':
    pytest.main([__file__, '-v'])
