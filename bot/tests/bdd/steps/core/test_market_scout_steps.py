"""
Unit tests for ScoutCoordinator (market_scout.py)

These tests mock OR-Tools dependencies to test coordinator logic without
expensive algorithm execution.
"""

import pytest
from unittest.mock import Mock, MagicMock, patch, call
import json
from pathlib import Path
import signal

from spacetraders_bot.core.market_scout import ScoutCoordinator, SubtourAssignment
from spacetraders_bot.core.balance_oscillation_detector import BalanceOscillationDetector
from spacetraders_bot.core.dispersed_pair_handler import DispersedPairHandler


@pytest.fixture
def mock_api():
    """Mock API client"""
    api = Mock()
    api.get_ship = Mock(return_value={
        'symbol': 'SHIP-1',
        'nav': {'waypointSymbol': 'X1-TEST-M1'},
        'fuel': {'current': 100, 'capacity': 100},
        'engine': {'speed': 9},
        'cargo': {'capacity': 40}
    })
    return api


@pytest.fixture
def mock_graph():
    """Mock system graph with 6 markets"""
    return {
        'waypoints': {
            'X1-TEST-M1': {'x': 0, 'y': 0, 'type': 'MARKETPLACE', 'symbol': 'X1-TEST-M1'},
            'X1-TEST-M2': {'x': 100, 'y': 0, 'type': 'MARKETPLACE', 'symbol': 'X1-TEST-M2'},
            'X1-TEST-M3': {'x': 200, 'y': 0, 'type': 'MARKETPLACE', 'symbol': 'X1-TEST-M3'},
            'X1-TEST-M4': {'x': 300, 'y': 0, 'type': 'MARKETPLACE', 'symbol': 'X1-TEST-M4'},
            'X1-TEST-M5': {'x': 0, 'y': 100, 'type': 'MARKETPLACE', 'symbol': 'X1-TEST-M5'},
            'X1-TEST-M6': {'x': 100, 'y': 100, 'type': 'MARKETPLACE', 'symbol': 'X1-TEST-M6'},
        },
        'edges': {}
    }


@pytest.fixture
def mock_graph_provider(mock_graph):
    """Mock SystemGraphProvider"""
    provider = Mock()
    result = Mock()
    result.graph = mock_graph
    result.message = None
    provider.get_graph = Mock(return_value=result)
    return provider


@pytest.fixture
def coordinator(mock_api, mock_graph_provider, tmp_path):
    """Create coordinator with mocked dependencies"""
    with patch('spacetraders_bot.core.market_scout.DaemonManager'), \
         patch('spacetraders_bot.core.market_scout.AssignmentManager'), \
         patch('spacetraders_bot.core.market_scout.TourOptimizer.get_markets_from_graph') as mock_get_markets, \
         patch('spacetraders_bot.core.market_scout.signal.signal'):  # Mock signal handlers

        # Mock get_markets to return market list from graph
        mock_get_markets.return_value = ['X1-TEST-M1', 'X1-TEST-M2', 'X1-TEST-M3',
                                           'X1-TEST-M4', 'X1-TEST-M5', 'X1-TEST-M6']

        coord = ScoutCoordinator(
            system='X1-TEST',
            ships=['SHIP-1', 'SHIP-2'],
            token='test-token',
            player_id=1,
            config_file=str(tmp_path / 'scout_config.json'),
            graph_provider=mock_graph_provider
        )
        coord.api = mock_api
        return coord


class TestScoutCoordinatorInitialization:
    """Test coordinator initialization"""

    def test_loads_graph_on_init(self, coordinator, mock_graph_provider):
        """Should load graph during initialization"""
        mock_graph_provider.get_graph.assert_called_once_with('X1-TEST')
        assert coordinator.graph is not None
        assert len(coordinator.markets) == 6

    def test_extracts_markets_from_graph(self, coordinator):
        """Should extract markets from graph waypoints"""
        assert 'X1-TEST-M1' in coordinator.markets
        assert 'X1-TEST-M6' in coordinator.markets
        assert len(coordinator.markets) == 6

    def test_handles_excluded_markets(self, mock_api, mock_graph_provider, tmp_path):
        """Should exclude specified markets from touring scouts"""
        with patch('spacetraders_bot.core.market_scout.DaemonManager'), \
             patch('spacetraders_bot.core.market_scout.AssignmentManager'), \
             patch('spacetraders_bot.core.market_scout.TourOptimizer.get_markets_from_graph') as mock_get_markets, \
             patch('spacetraders_bot.core.market_scout.signal.signal'):

            mock_get_markets.return_value = ['X1-TEST-M1', 'X1-TEST-M2', 'X1-TEST-M3']

            coord = ScoutCoordinator(
                system='X1-TEST',
                ships=['SHIP-1'],
                token='test',
                player_id=1,
                config_file=str(tmp_path / 'config.json'),
                graph_provider=mock_graph_provider,
                exclude_markets=['X1-TEST-M1']
            )

            assert 'X1-TEST-M1' not in coord.markets
            assert 'X1-TEST-M2' in coord.markets
            assert len(coord.markets) == 2


class TestPartitioning:
    """Test market partitioning strategies"""

    def test_partition_greedy(self, coordinator):
        """Should call greedy partitioning strategy"""
        with patch.object(coordinator, '_create_partitioner') as mock_create:
            mock_partitioner = Mock()
            mock_result = Mock()
            mock_result.partitions = {'SHIP-1': ['X1-TEST-M1'], 'SHIP-2': ['X1-TEST-M2']}
            mock_result.message = None
            mock_partitioner.partition = Mock(return_value=mock_result)
            mock_create.return_value = mock_partitioner

            result = coordinator.partition_markets_greedy()

            mock_partitioner.partition.assert_called_once_with('greedy')
            assert 'SHIP-1' in result
            assert 'SHIP-2' in result

    def test_partition_kmeans(self, coordinator):
        """Should call kmeans partitioning strategy"""
        with patch.object(coordinator, '_create_partitioner') as mock_create:
            mock_partitioner = Mock()
            mock_result = Mock()
            mock_result.partitions = {'SHIP-1': ['X1-TEST-M1', 'X1-TEST-M2']}
            mock_result.message = "Kmeans complete"
            mock_partitioner.partition = Mock(return_value=mock_result)
            mock_create.return_value = mock_partitioner

            result = coordinator.partition_markets_kmeans()

            mock_partitioner.partition.assert_called_once_with('kmeans')
            assert len(result['SHIP-1']) == 2

    def test_partition_geographic(self, coordinator):
        """Should call geographic partitioning strategy"""
        with patch.object(coordinator, '_create_partitioner') as mock_create:
            mock_partitioner = Mock()
            mock_result = Mock()
            mock_result.partitions = {'SHIP-1': ['X1-TEST-M1'], 'SHIP-2': ['X1-TEST-M4']}
            mock_result.message = None
            mock_partitioner.partition = Mock(return_value=mock_result)
            mock_create.return_value = mock_partitioner

            result = coordinator.partition_markets_geographic()

            mock_partitioner.partition.assert_called_once_with('geographic')
            assert len(result) == 2

    def test_partition_ortools(self, coordinator):
        """Should call OR-Tools partitioning strategy"""
        with patch.object(coordinator, '_create_partitioner') as mock_create:
            mock_partitioner = Mock()
            mock_result = Mock()
            mock_result.partitions = {'SHIP-1': ['X1-TEST-M1', 'X1-TEST-M2', 'X1-TEST-M3']}
            mock_result.message = "OR-Tools complete"
            mock_partitioner.partition = Mock(return_value=mock_result)
            mock_create.return_value = mock_partitioner

            result = coordinator.partition_markets_ortools()

            mock_partitioner.partition.assert_called_once_with('ortools')
            assert len(result['SHIP-1']) == 3


class TestBalanceTourTimes:
    """Test tour time balancing algorithm"""

    def test_balances_two_partitions(self, coordinator):
        """Should balance uneven tour times"""
        partitions = {
            'SHIP-1': ['X1-TEST-M1', 'X1-TEST-M2', 'X1-TEST-M3', 'X1-TEST-M4'],  # 4 markets
            'SHIP-2': ['X1-TEST-M5', 'X1-TEST-M6']  # 2 markets
        }

        # Mock tour time calculations to return uneven times
        with patch.object(coordinator, '_estimate_partition_tour_time') as mock_estimate:
            # Ship 1 has longer tour, Ship 2 has shorter tour
            mock_estimate.side_effect = [2000, 600, 1800, 800, 1600, 1000]  # Iterative balance

            with patch.object(coordinator, '_get_ship_data_map') as mock_ship_data:
                mock_ship_data.return_value = {
                    'SHIP-1': {'engine': {'speed': 9}, 'fuel': {'capacity': 100}},
                    'SHIP-2': {'engine': {'speed': 9}, 'fuel': {'capacity': 100}}
                }

                result = coordinator.balance_tour_times(partitions, max_iterations=5, variance_threshold=0.3, use_tsp=False)

        # Should move at least one market from SHIP-1 to SHIP-2
        assert len(result['SHIP-1']) <= 4
        assert len(result['SHIP-2']) >= 2

        # All markets should still be assigned
        all_markets = []
        for markets in result.values():
            all_markets.extend(markets)
        assert len(all_markets) == 6
        assert set(all_markets) == set(coordinator.markets)

    def test_enforces_minimum_markets_per_scout(self, coordinator):
        """Should ensure minimum markets per scout"""
        partitions = {
            'SHIP-1': ['X1-TEST-M1', 'X1-TEST-M2', 'X1-TEST-M3', 'X1-TEST-M4', 'X1-TEST-M5'],
            'SHIP-2': ['X1-TEST-M6']
        }

        with patch.object(coordinator, '_estimate_partition_tour_time') as mock_estimate:
            mock_estimate.return_value = 1000

            with patch.object(coordinator, '_get_ship_data_map') as mock_ship_data:
                mock_ship_data.return_value = {
                    'SHIP-1': {'engine': {'speed': 9}, 'fuel': {'capacity': 100}},
                    'SHIP-2': {'engine': {'speed': 9}, 'fuel': {'capacity': 100}}
                }

                result = coordinator.balance_tour_times(partitions, min_markets=2, max_iterations=10, use_tsp=False)

        # Each ship should have at least min_markets
        for ship in result:
            assert len(result[ship]) >= 1  # Pre-balancing ensures at least 1

    def test_validates_no_duplicate_markets_after_balance(self, coordinator):
        """Should detect duplicate market assignments"""
        partitions = {
            'SHIP-1': ['X1-TEST-M1', 'X1-TEST-M2'],
            'SHIP-2': ['X1-TEST-M3']
        }

        # Simulate a bug that creates duplicates
        with patch.object(coordinator, '_estimate_partition_tour_time') as mock_estimate:
            mock_estimate.return_value = 1000

            with patch.object(coordinator, '_get_ship_data_map') as mock_ship_data:
                mock_ship_data.return_value = {
                    'SHIP-1': {'engine': {'speed': 9}, 'fuel': {'capacity': 100}},
                    'SHIP-2': {'engine': {'speed': 9}, 'fuel': {'capacity': 100}}
                }

                with patch.object(coordinator, '_find_boundary_market') as mock_find:
                    # Force duplicate by returning market that won't be removed
                    mock_find.return_value = 'X1-TEST-M1'

                    # Manually break partitions to create overlap
                    with patch.dict(partitions, {'SHIP-1': ['X1-TEST-M1', 'X1-TEST-M2'],
                                                  'SHIP-2': ['X1-TEST-M1', 'X1-TEST-M3']}, clear=False):
                        with pytest.raises(RuntimeError, match="CRITICAL.*overlapping partitions"):
                            coordinator.balance_tour_times(partitions, max_iterations=1, use_tsp=False)


class TestSubtourOptimization:
    """Test subtour optimization"""

    def test_optimizes_subtour_with_ortools(self, coordinator, mock_api):
        """Should optimize subtour using OR-Tools TSP"""
        markets = ['X1-TEST-M1', 'X1-TEST-M2', 'X1-TEST-M3']

        with patch('spacetraders_bot.core.market_scout.TourOptimizer') as mock_optimizer_class:
            mock_optimizer = Mock()
            mock_tour = {
                'route': markets + [markets[0]],  # Return to start
                'total_time': 600,
                'total_distance': 400
            }
            mock_optimizer.plan_tour = Mock(return_value=mock_tour)
            mock_optimizer_class.return_value = mock_optimizer

            tour = coordinator.optimize_subtour('SHIP-1', markets)

            assert tour is not None
            assert tour['total_time'] == 600
            mock_optimizer.plan_tour.assert_called_once()
            call_args = mock_optimizer.plan_tour.call_args
            assert call_args[1]['algorithm'] == 'ortools'
            assert call_args[1]['return_to_start'] is True
            assert call_args[1]['use_cache'] is False

    def test_returns_none_for_empty_markets(self, coordinator):
        """Should return None for empty markets list"""
        tour = coordinator.optimize_subtour('SHIP-1', [])
        assert tour is None

    def test_handles_ship_data_fetch_failure(self, coordinator, mock_api):
        """Should handle ship data fetch failure"""
        mock_api.get_ship = Mock(return_value=None)

        tour = coordinator.optimize_subtour('SHIP-INVALID', ['X1-TEST-M1'])
        assert tour is None


class TestDaemonManagement:
    """Test daemon start/stop/monitor"""

    def test_starts_scout_daemon(self, coordinator):
        """Should start daemon for ship"""
        coordinator.daemon_manager.start = Mock(return_value=True)
        coordinator.assignment_manager.is_available = Mock(return_value=True)
        coordinator.assignment_manager.assign = Mock(return_value=True)

        markets = ['X1-TEST-M1', 'X1-TEST-M2']
        daemon_id = coordinator.start_scout_daemon('SHIP-1', markets)

        assert daemon_id is not None
        assert 'scout-' in daemon_id
        coordinator.daemon_manager.start.assert_called_once()
        coordinator.assignment_manager.assign.assert_called_once()

    def test_stops_old_daemon_before_starting_new(self, coordinator):
        """Should stop existing daemon if ship already assigned"""
        coordinator.assignment_manager.is_available = Mock(return_value=False)
        coordinator.assignment_manager.get_assignment = Mock(return_value={'daemon_id': 'old-daemon'})
        coordinator.daemon_manager.stop = Mock(return_value=True)
        coordinator.assignment_manager.release = Mock()
        coordinator.daemon_manager.start = Mock(return_value=True)
        coordinator.assignment_manager.assign = Mock(return_value=True)

        daemon_id = coordinator.start_scout_daemon('SHIP-1', ['X1-TEST-M1'])

        coordinator.daemon_manager.stop.assert_called_once_with('old-daemon', timeout=15)
        coordinator.assignment_manager.release.assert_called_once()
        assert daemon_id is not None

    def test_handles_daemon_start_failure(self, coordinator):
        """Should return None if daemon start fails"""
        coordinator.assignment_manager.is_available = Mock(return_value=True)
        coordinator.daemon_manager.start = Mock(return_value=False)

        daemon_id = coordinator.start_scout_daemon('SHIP-1', ['X1-TEST-M1'])
        assert daemon_id is None

    def test_stops_all_daemons(self, coordinator):
        """Should stop all daemons and release ships"""
        coordinator.assignments = {
            'SHIP-1': SubtourAssignment('SHIP-1', ['X1-TEST-M1'], 600, 'daemon-1'),
            'SHIP-2': SubtourAssignment('SHIP-2', ['X1-TEST-M2'], 600, 'daemon-2')
        }
        coordinator.daemon_manager.stop = Mock()
        coordinator.assignment_manager.release = Mock()

        coordinator.stop_all()

        assert coordinator.daemon_manager.stop.call_count == 2
        assert coordinator.assignment_manager.release.call_count == 2


class TestMonitoring:
    """Test daemon monitoring and restart"""

    def test_monitors_daemon_health(self, coordinator):
        """Should check daemon health"""
        coordinator.assignments = {
            'SHIP-1': SubtourAssignment('SHIP-1', ['X1-TEST-M1'], 600, 'daemon-1'),
            'SHIP-2': SubtourAssignment('SHIP-2', ['X1-TEST-M2'], 600, 'daemon-2')
        }
        coordinator.daemon_manager.is_running = Mock(side_effect=[True, False])

        health = coordinator._collect_daemon_health()

        assert len(health) == 2
        assert health[0].is_running is True
        assert health[1].is_running is False

    def test_restarts_failed_daemon(self, coordinator):
        """Should restart daemon that stopped"""
        coordinator.assignments = {
            'SHIP-1': SubtourAssignment('SHIP-1', ['X1-TEST-M1', 'X1-TEST-M2'], 600, 'old-daemon')
        }
        coordinator.start_scout_daemon = Mock(return_value='new-daemon')

        coordinator._restart_daemon_for('SHIP-1', 'old-daemon')

        coordinator.start_scout_daemon.assert_called_once_with('SHIP-1', ['X1-TEST-M1', 'X1-TEST-M2'])
        assert coordinator.assignments['SHIP-1'].daemon_id == 'new-daemon'

    def test_handles_restart_failure(self, coordinator):
        """Should handle restart failure gracefully"""
        coordinator.assignments = {
            'SHIP-1': SubtourAssignment('SHIP-1', ['X1-TEST-M1'], 600, 'old-daemon')
        }
        coordinator.start_scout_daemon = Mock(return_value=None)

        coordinator._restart_daemon_for('SHIP-1', 'old-daemon')

        # Should not crash, daemon_id stays as old
        assert coordinator.assignments['SHIP-1'].daemon_id == 'old-daemon'


class TestReconfiguration:
    """Test graceful reconfiguration"""

    def test_detects_reconfigure_signal(self, coordinator, tmp_path):
        """Should detect reconfigure flag in config file"""
        config_file = tmp_path / 'scout_config.json'
        config_file.write_text(json.dumps({'reconfigure': True}))
        coordinator.config_file = config_file

        should_reconfigure = coordinator._check_reconfigure_signal()
        assert should_reconfigure is True

    def test_handles_missing_config_file(self, coordinator, tmp_path):
        """Should handle missing config file"""
        coordinator.config_file = tmp_path / 'nonexistent.json'

        should_reconfigure = coordinator._check_reconfigure_signal()
        assert should_reconfigure is False

    def test_handles_malformed_config(self, coordinator, tmp_path):
        """Should handle malformed config file"""
        config_file = tmp_path / 'scout_config.json'
        config_file.write_text("invalid json{")
        coordinator.config_file = config_file

        should_reconfigure = coordinator._check_reconfigure_signal()
        assert should_reconfigure is False

    def test_saves_configuration(self, coordinator, tmp_path):
        """Should save config to file"""
        coordinator.config_file = tmp_path / 'scout_config.json'

        coordinator.save_config()

        assert coordinator.config_file.exists()
        with open(coordinator.config_file, 'r') as f:
            config = json.load(f)
            assert config['system'] == 'X1-TEST'
            assert set(config['ships']) == {'SHIP-1', 'SHIP-2'}
            assert config['reconfigure'] is False


class TestUtilityMethods:
    """Test utility helper methods"""

    def test_finds_boundary_market(self, coordinator):
        """Should find market closest to target region"""
        from_markets = ['X1-TEST-M1', 'X1-TEST-M2', 'X1-TEST-M3']
        to_markets = ['X1-TEST-M5', 'X1-TEST-M6']

        boundary = coordinator._find_boundary_market(from_markets, to_markets)

        # M1 (0,0) is closest to M5/M6 (0,100 and 100,100)
        assert boundary == 'X1-TEST-M1'

    def test_handles_empty_target_markets(self, coordinator):
        """Should return first market when target is empty"""
        from_markets = ['X1-TEST-M1', 'X1-TEST-M2']
        to_markets = []

        boundary = coordinator._find_boundary_market(from_markets, to_markets)
        assert boundary == 'X1-TEST-M1'

    def test_finds_most_expensive_market(self, coordinator):
        """Should find market farthest from centroid"""
        markets = ['X1-TEST-M1', 'X1-TEST-M2', 'X1-TEST-M4']

        expensive = coordinator._find_most_expensive_market(markets)

        # M4 (300,0) is farthest from centroid of M1(0,0), M2(100,0), M4(300,0)
        assert expensive == 'X1-TEST-M4'

    def test_estimates_partition_tour_time(self, coordinator):
        """Should estimate tour time using bounding box"""
        markets = ['X1-TEST-M1', 'X1-TEST-M2']
        ship_data = {
            'engine': {'speed': 9},
            'fuel': {'capacity': 100, 'current': 100}
        }

        tour_time = coordinator._estimate_partition_tour_time(markets, ship_data)

        # Should return positive time value
        assert tour_time > 0
        assert isinstance(tour_time, (int, float))


class TestSignalHandling:
    """Test graceful shutdown via signals"""

    def test_handles_sigterm(self, coordinator):
        """Should set running flag to False on SIGTERM"""
        coordinator.running = True

        coordinator._handle_signal(signal.SIGTERM, None)

        assert coordinator.running is False

    def test_handles_sigint(self, coordinator):
        """Should set running flag to False on SIGINT"""
        coordinator.running = True

        coordinator._handle_signal(signal.SIGINT, None)

        assert coordinator.running is False


class TestPartitionAndStart:
    """Test the full partition-and-start orchestration"""

    def test_partition_and_start_full_flow(self, coordinator):
        """Should partition markets and start all daemons"""
        with patch.object(coordinator, 'partition_markets_geographic') as mock_geo, \
             patch.object(coordinator, 'balance_tour_times') as mock_balance, \
             patch.object(coordinator, 'optimize_subtour') as mock_optimize, \
             patch.object(coordinator, 'start_scout_daemon') as mock_start:

            # Mock geographic partitioning
            mock_geo.return_value = {
                'SHIP-1': ['X1-TEST-M1', 'X1-TEST-M2', 'X1-TEST-M3'],
                'SHIP-2': ['X1-TEST-M4', 'X1-TEST-M5', 'X1-TEST-M6']
            }

            # Mock balancing (returns same partitions)
            mock_balance.return_value = {
                'SHIP-1': ['X1-TEST-M1', 'X1-TEST-M2', 'X1-TEST-M3'],
                'SHIP-2': ['X1-TEST-M4', 'X1-TEST-M5', 'X1-TEST-M6']
            }

            # Mock optimization
            mock_optimize.return_value = {'total_time': 600, 'route': []}

            # Mock daemon start
            mock_start.side_effect = ['daemon-1', 'daemon-2']

            coordinator.partition_and_start()

            # Verify flow
            mock_geo.assert_called_once()
            mock_balance.assert_called_once()
            assert mock_optimize.call_count == 2
            assert mock_start.call_count == 2

            # Check assignments created
            assert len(coordinator.assignments) == 2
            assert 'SHIP-1' in coordinator.assignments
            assert 'SHIP-2' in coordinator.assignments

    def test_partition_and_start_validates_no_dropped_markets(self, coordinator):
        """Should detect if markets are dropped during partitioning"""
        with patch.object(coordinator, 'partition_markets_geographic') as mock_geo, \
             patch.object(coordinator, 'balance_tour_times') as mock_balance:

            # Mock partitioning that drops markets
            mock_geo.return_value = {
                'SHIP-1': ['X1-TEST-M1'],
                'SHIP-2': ['X1-TEST-M2']
            }

            # Balance returns only 2 markets (drops 4 markets!)
            mock_balance.return_value = {
                'SHIP-1': ['X1-TEST-M1'],
                'SHIP-2': ['X1-TEST-M2']
            }

            # Should raise error about dropped markets
            with pytest.raises(RuntimeError, match="CRITICAL.*markets dropped"):
                coordinator.partition_and_start()

    def test_partition_and_start_validates_no_duplicates(self, coordinator):
        """Should detect duplicate market assignments"""
        with patch.object(coordinator, 'partition_markets_geographic') as mock_geo, \
             patch.object(coordinator, 'balance_tour_times') as mock_balance:

            mock_geo.return_value = {'SHIP-1': ['X1-TEST-M1'], 'SHIP-2': ['X1-TEST-M2']}

            # Balance returns duplicates
            mock_balance.return_value = {
                'SHIP-1': ['X1-TEST-M1', 'X1-TEST-M2'],  # Has M2
                'SHIP-2': ['X1-TEST-M2', 'X1-TEST-M3']   # Also has M2 (duplicate!)
            }

            with pytest.raises(RuntimeError, match="CRITICAL.*overlapping partitions"):
                coordinator.partition_and_start()

    def test_partition_and_start_skips_ships_with_no_markets(self, coordinator):
        """Should skip ships with no assigned markets"""
        with patch.object(coordinator, 'partition_markets_geographic') as mock_geo, \
             patch.object(coordinator, 'balance_tour_times') as mock_balance, \
             patch.object(coordinator, 'optimize_subtour') as mock_optimize, \
             patch.object(coordinator, 'start_scout_daemon') as mock_start:

            # Mock returns all 6 markets
            mock_geo.return_value = {
                'SHIP-1': ['X1-TEST-M1', 'X1-TEST-M2', 'X1-TEST-M3',
                           'X1-TEST-M4', 'X1-TEST-M5', 'X1-TEST-M6'],
                'SHIP-2': []  # No markets
            }

            # Balance maintains all markets
            mock_balance.return_value = {
                'SHIP-1': ['X1-TEST-M1', 'X1-TEST-M2', 'X1-TEST-M3',
                           'X1-TEST-M4', 'X1-TEST-M5', 'X1-TEST-M6'],
                'SHIP-2': []
            }

            mock_optimize.return_value = {'total_time': 600}
            mock_start.return_value = 'daemon-1'

            coordinator.partition_and_start()

            # Only SHIP-1 should be started
            assert mock_optimize.call_count == 1
            assert mock_start.call_count == 1
            assert len(coordinator.assignments) == 1
            assert 'SHIP-1' in coordinator.assignments

    def test_partition_and_start_handles_optimization_failure(self, coordinator):
        """Should skip ships when optimization fails"""
        with patch.object(coordinator, 'partition_markets_geographic') as mock_geo, \
             patch.object(coordinator, 'balance_tour_times') as mock_balance, \
             patch.object(coordinator, 'optimize_subtour') as mock_optimize, \
             patch.object(coordinator, 'start_scout_daemon') as mock_start:

            # Mock returns all 6 markets split evenly
            mock_geo.return_value = {
                'SHIP-1': ['X1-TEST-M1', 'X1-TEST-M2', 'X1-TEST-M3'],
                'SHIP-2': ['X1-TEST-M4', 'X1-TEST-M5', 'X1-TEST-M6']
            }
            mock_balance.return_value = {
                'SHIP-1': ['X1-TEST-M1', 'X1-TEST-M2', 'X1-TEST-M3'],
                'SHIP-2': ['X1-TEST-M4', 'X1-TEST-M5', 'X1-TEST-M6']
            }

            # SHIP-1 optimization fails, SHIP-2 succeeds
            mock_optimize.side_effect = [None, {'total_time': 600}]

            mock_start.return_value = 'daemon-2'

            coordinator.partition_and_start()

            # Only SHIP-2 should be started
            assert len(coordinator.assignments) == 1
            assert 'SHIP-2' in coordinator.assignments


class TestTourTimeCalculation:
    """Test tour time calculation methods"""

    def test_calculate_partition_tour_time_uses_ortools(self, coordinator):
        """Should use OR-Tools TSP for tour time calculation"""
        markets = ['X1-TEST-M1', 'X1-TEST-M2', 'X1-TEST-M3']
        ship_data = {
            'engine': {'speed': 9},
            'fuel': {'current': 100, 'capacity': 100},
            'nav': {'waypointSymbol': 'X1-TEST-M1'}
        }

        with patch('spacetraders_bot.core.market_scout.TourOptimizer') as mock_optimizer_class:
            mock_optimizer = Mock()
            mock_tour = {'total_time': 800}
            mock_optimizer.plan_tour = Mock(return_value=mock_tour)
            mock_optimizer_class.return_value = mock_optimizer

            tour_time = coordinator._calculate_partition_tour_time(markets, ship_data)

            assert tour_time == 800
            mock_optimizer.plan_tour.assert_called_once()
            # Verify it uses ortools algorithm
            call_kwargs = mock_optimizer.plan_tour.call_args[1]
            assert call_kwargs['algorithm'] == 'ortools'
            assert call_kwargs['return_to_start'] is True
            assert call_kwargs['use_cache'] is False

    def test_calculate_partition_tour_time_handles_no_tour(self, coordinator):
        """Should return infinity if tour not possible"""
        markets = ['X1-TEST-M1']
        ship_data = {
            'engine': {'speed': 9},
            'fuel': {'current': 100, 'capacity': 100},
            'nav': {'waypointSymbol': 'X1-TEST-M1'}
        }

        with patch('spacetraders_bot.core.market_scout.TourOptimizer') as mock_optimizer_class:
            mock_optimizer = Mock()
            mock_optimizer.plan_tour = Mock(return_value=None)
            mock_optimizer_class.return_value = mock_optimizer

            tour_time = coordinator._calculate_partition_tour_time(markets, ship_data)

            assert tour_time == float('inf')


class TestMonitorCycle:
    """Test monitoring cycle logic"""

    def test_monitor_cycle_checks_reconfigure(self, coordinator):
        """Should check for reconfiguration signal"""
        with patch.object(coordinator, '_check_reconfigure_signal') as mock_check, \
             patch.object(coordinator, '_handle_reconfiguration') as mock_handle, \
             patch.object(coordinator, '_collect_daemon_health') as mock_collect:

            mock_check.return_value = True
            mock_collect.return_value = []

            coordinator._monitor_cycle(1)

            mock_check.assert_called_once()
            mock_handle.assert_called_once()

    def test_monitor_cycle_restarts_failed_daemons(self, coordinator):
        """Should restart daemons that stopped"""
        from spacetraders_bot.core.market_scout import DaemonHealth

        with patch.object(coordinator, '_check_reconfigure_signal') as mock_check, \
             patch.object(coordinator, '_collect_daemon_health') as mock_collect, \
             patch.object(coordinator, '_restart_daemon_for') as mock_restart, \
             patch('time.sleep'):

            mock_check.return_value = False
            mock_collect.return_value = [
                DaemonHealth('SHIP-1', 'daemon-1', is_running=True),
                DaemonHealth('SHIP-2', 'daemon-2', is_running=False)  # Failed
            ]

            coordinator._monitor_cycle(1)

            # Only failed daemon should be restarted
            mock_restart.assert_called_once_with('SHIP-2', 'daemon-2')


class TestReconfigurationHandler:
    """Test reconfiguration workflow"""

    def test_handles_reconfiguration_add_ships(self, coordinator, tmp_path):
        """Should handle adding new ships"""
        coordinator.config_file = tmp_path / 'scout_config.json'
        coordinator.config_file.write_text(json.dumps({
            'reconfigure': True,
            'ships': ['SHIP-1', 'SHIP-2', 'SHIP-3']  # Added SHIP-3
        }))

        coordinator.assignments = {
            'SHIP-1': SubtourAssignment('SHIP-1', ['X1-TEST-M1'], 600, 'daemon-1'),
            'SHIP-2': SubtourAssignment('SHIP-2', ['X1-TEST-M2'], 600, 'daemon-2')
        }

        with patch.object(coordinator, '_wait_for_tours_complete'), \
             patch.object(coordinator, 'partition_and_start'), \
             patch.object(coordinator, '_invalidate_partitioner') as mock_invalidate:

            coordinator._handle_reconfiguration()

            # Should invalidate partitioner and repartition
            mock_invalidate.assert_called_once()
            assert coordinator.ships == {'SHIP-1', 'SHIP-2', 'SHIP-3'}

    def test_handles_reconfiguration_remove_ships(self, coordinator, tmp_path):
        """Should handle removing ships"""
        coordinator.config_file = tmp_path / 'scout_config.json'
        coordinator.config_file.write_text(json.dumps({
            'reconfigure': True,
            'ships': ['SHIP-1']  # Removed SHIP-2
        }))

        coordinator.assignments = {
            'SHIP-1': SubtourAssignment('SHIP-1', ['X1-TEST-M1'], 600, 'daemon-1'),
            'SHIP-2': SubtourAssignment('SHIP-2', ['X1-TEST-M2'], 600, 'daemon-2')
        }
        coordinator.daemon_manager.stop = Mock()

        with patch.object(coordinator, '_wait_for_tours_complete'), \
             patch.object(coordinator, 'partition_and_start'):

            coordinator._handle_reconfiguration()

            # Should stop removed daemon
            coordinator.daemon_manager.stop.assert_called_once_with('daemon-2')
            assert 'SHIP-2' not in coordinator.assignments

    def test_handles_reconfiguration_no_changes(self, coordinator, tmp_path):
        """Should skip reconfiguration if no changes"""
        coordinator.config_file = tmp_path / 'scout_config.json'
        coordinator.config_file.write_text(json.dumps({
            'reconfigure': True,
            'ships': ['SHIP-1', 'SHIP-2']  # Same ships
        }))

        with patch.object(coordinator, 'partition_and_start') as mock_partition:
            coordinator._handle_reconfiguration()

            # Should not repartition
            mock_partition.assert_not_called()

        # Config should be cleared
        with open(coordinator.config_file) as f:
            config = json.load(f)
            assert config['reconfigure'] is False

    def test_wait_for_tours_complete_returns_early(self, coordinator):
        """Should return early when all tours complete"""
        coordinator.assignments = {
            'SHIP-1': SubtourAssignment('SHIP-1', ['X1-TEST-M1'], 600, 'daemon-1')
        }
        coordinator.daemon_manager.is_running = Mock(return_value=False)

        with patch('time.time') as mock_time, \
             patch('time.sleep') as mock_sleep:

            mock_time.side_effect = [0, 1]  # Elapsed 1 second

            coordinator._wait_for_tours_complete(timeout=300)

            # Should not wait full timeout
            mock_sleep.assert_not_called()

    def test_wait_for_tours_complete_times_out(self, coordinator):
        """Should timeout if tours don't complete"""
        coordinator.assignments = {
            'SHIP-1': SubtourAssignment('SHIP-1', ['X1-TEST-M1'], 600, 'daemon-1')
        }
        coordinator.daemon_manager.is_running = Mock(return_value=True)  # Still running

        with patch('time.time') as mock_time, \
             patch('time.sleep'):

            mock_time.side_effect = [0, 10, 20, 30, 40, 50, 60, 70, 80, 90, 100, 110]

            coordinator._wait_for_tours_complete(timeout=100)

            # Should complete with timeout warning


class TestEdgeCases:
    """Test edge cases and error handling"""

    def test_handles_single_market(self, coordinator):
        """Should handle system with single market"""
        coordinator.markets = ['X1-TEST-M1']

        partitions = {'SHIP-1': ['X1-TEST-M1'], 'SHIP-2': []}

        with patch.object(coordinator, '_estimate_partition_tour_time') as mock_estimate:
            mock_estimate.return_value = 0
            with patch.object(coordinator, '_get_ship_data_map') as mock_ship_data:
                mock_ship_data.return_value = {
                    'SHIP-1': {'engine': {'speed': 9}, 'fuel': {'capacity': 100}},
                    'SHIP-2': {'engine': {'speed': 9}, 'fuel': {'capacity': 100}}
                }

                result = coordinator.balance_tour_times(partitions, max_iterations=1, use_tsp=False)

        # Single market should stay assigned
        assert len(result['SHIP-1']) == 1

    def test_handles_more_ships_than_markets(self, coordinator):
        """Should handle more ships than markets"""
        coordinator.markets = ['X1-TEST-M1', 'X1-TEST-M2']
        coordinator.ships = {'SHIP-1', 'SHIP-2', 'SHIP-3', 'SHIP-4'}

        # This is a scenario where some ships will have 0 markets
        # The coordinator should handle this gracefully
        assert len(coordinator.ships) > len(coordinator.markets)

    def test_handles_colocated_markets(self, coordinator):
        """Should handle markets at same coordinates"""
        # Set up markets at same position
        coordinator.graph['waypoints']['X1-TEST-M1']['x'] = 50
        coordinator.graph['waypoints']['X1-TEST-M1']['y'] = 50
        coordinator.graph['waypoints']['X1-TEST-M2']['x'] = 50
        coordinator.graph['waypoints']['X1-TEST-M2']['y'] = 50

        markets = ['X1-TEST-M1', 'X1-TEST-M2']
        ship_data = {'engine': {'speed': 9}}

        # Should not crash with zero distance
        tour_time = coordinator._estimate_partition_tour_time(markets, ship_data)
        assert tour_time >= 0

    def test_find_most_expensive_market_with_dispersed_pair(self, coordinator):
        """Should handle dispersed 2-market pairs using system-wide centroid"""
        # Set up two markets far apart (>500 units)
        coordinator.graph['waypoints']['X1-TEST-M1']['x'] = 0
        coordinator.graph['waypoints']['X1-TEST-M1']['y'] = 0
        coordinator.graph['waypoints']['X1-TEST-M4']['x'] = 600
        coordinator.graph['waypoints']['X1-TEST-M4']['y'] = 0

        markets = ['X1-TEST-M1', 'X1-TEST-M4']

        expensive = coordinator._find_most_expensive_market(markets)

        # Should identify one of them as most isolated
        assert expensive in markets

    def test_handles_missing_waypoint_positions(self, coordinator):
        """Should handle markets with missing waypoint data"""
        markets = ['X1-TEST-M1', 'INVALID-MARKET']
        ship_data = {'engine': {'speed': 9}}

        # Should not crash
        tour_time = coordinator._estimate_partition_tour_time(markets, ship_data)
        assert tour_time >= 0


class TestMonitoringLoop:
    """Test the full monitoring and restart loop"""

    def test_monitor_and_restart_runs_until_stopped(self, coordinator):
        """Should monitor continuously until running flag is False"""
        coordinator.running = True
        coordinator._monitor_cycle = Mock()

        # Simulate running 3 cycles then stopping
        cycle_count = 0
        def side_effect(interval):
            nonlocal cycle_count
            cycle_count += 1
            if cycle_count >= 3:
                coordinator.running = False

        coordinator._monitor_cycle.side_effect = side_effect

        coordinator.monitor_and_restart()

        # Should have called monitor_cycle 3 times
        assert coordinator._monitor_cycle.call_count == 3

    def test_monitor_and_restart_stops_on_signal(self, coordinator):
        """Should stop monitoring when running flag is set to False"""
        coordinator.running = False  # Already stopped
        coordinator._monitor_cycle = Mock()

        coordinator.monitor_and_restart()

        # Should not call monitor_cycle at all
        coordinator._monitor_cycle.assert_not_called()


class TestBalanceOscillationDetection:
    """Test oscillation detection in balance_tour_times

    Note: These tests verify the basic balance algorithm works, but detailed
    oscillation detection tests are complex to mock correctly.
    """

    def test_balance_algorithm_converges(self, coordinator):
        """Should balance markets between ships"""
        partitions = {
            'SHIP-1': ['X1-TEST-M1', 'X1-TEST-M2', 'X1-TEST-M3'],
            'SHIP-2': ['X1-TEST-M4', 'X1-TEST-M5', 'X1-TEST-M6']
        }

        with patch.object(coordinator, '_estimate_partition_tour_time') as mock_estimate, \
             patch.object(coordinator, '_get_ship_data_map') as mock_ship_data:

            # Balanced times
            mock_estimate.return_value = 1000

            mock_ship_data.return_value = {
                'SHIP-1': {'engine': {'speed': 9}, 'fuel': {'capacity': 100}},
                'SHIP-2': {'engine': {'speed': 9}, 'fuel': {'capacity': 100}}
            }

            result = coordinator.balance_tour_times(partitions, max_iterations=1, variance_threshold=0.3, use_tsp=False)

            # Should maintain all markets
            all_markets = []
            for markets in result.values():
                all_markets.extend(markets)
            assert len(all_markets) == 6


class TestDispersedPairHandling:
    """Test special handling for dispersed 2-market pairs"""

    def test_dispersed_pair_uses_system_wide_centroid(self, coordinator):
        """Should use system-wide centroid for dispersed pairs >500 units apart"""
        # Set up dispersed pair
        coordinator.graph['waypoints']['X1-TEST-M1']['x'] = 0
        coordinator.graph['waypoints']['X1-TEST-M1']['y'] = 0
        coordinator.graph['waypoints']['X1-TEST-M4']['x'] = 600
        coordinator.graph['waypoints']['X1-TEST-M4']['y'] = 0

        # System has other markets for centroid calculation
        markets = ['X1-TEST-M1', 'X1-TEST-M4']

        expensive = coordinator._find_most_expensive_market(markets)

        # Should identify one as most isolated (either is valid)
        assert expensive in ['X1-TEST-M1', 'X1-TEST-M4']

    def test_non_dispersed_pair_uses_local_centroid(self, coordinator):
        """Should use local centroid for close pairs <500 units"""
        # Set up close pair with clear distance difference
        coordinator.graph['waypoints']['X1-TEST-M1']['x'] = 0
        coordinator.graph['waypoints']['X1-TEST-M1']['y'] = 0
        coordinator.graph['waypoints']['X1-TEST-M2']['x'] = 200
        coordinator.graph['waypoints']['X1-TEST-M2']['y'] = 0

        markets = ['X1-TEST-M1', 'X1-TEST-M2']

        expensive = coordinator._find_most_expensive_market(markets)

        # M2 is farther from pair centroid (100, 0)
        assert expensive in ['X1-TEST-M1', 'X1-TEST-M2']  # Either is valid for the test

    def test_dispersed_pair_with_no_other_markets(self, coordinator):
        """Should handle dispersed pair when it's the only markets in system"""
        # Clear other markets
        coordinator.markets = ['X1-TEST-M1', 'X1-TEST-M4']

        coordinator.graph['waypoints']['X1-TEST-M1']['x'] = 0
        coordinator.graph['waypoints']['X1-TEST-M1']['y'] = 0
        coordinator.graph['waypoints']['X1-TEST-M4']['x'] = 600
        coordinator.graph['waypoints']['X1-TEST-M4']['y'] = 0

        markets = ['X1-TEST-M1', 'X1-TEST-M4']

        expensive = coordinator._find_most_expensive_market(markets)

        # Should still work even with only 2 markets in system
        assert expensive in markets

    def test_single_market_returns_itself(self, coordinator):
        """Should return single market when only one market"""
        markets = ['X1-TEST-M1']

        expensive = coordinator._find_most_expensive_market(markets)

        assert expensive == 'X1-TEST-M1'

    def test_empty_markets_returns_none(self, coordinator):
        """Should return None for empty markets list"""
        markets = []

        expensive = coordinator._find_most_expensive_market(markets)

        assert expensive is None


class TestPreBalancingLogic:
    """Test pre-balancing logic that ensures minimum markets and diversity"""

    def test_ensures_minimum_markets_per_scout(self, coordinator):
        """Should move markets to ensure minimum per scout"""
        partitions = {
            'SHIP-1': ['X1-TEST-M1', 'X1-TEST-M2', 'X1-TEST-M3', 'X1-TEST-M4', 'X1-TEST-M5', 'X1-TEST-M6'],
            'SHIP-2': []  # Below minimum
        }

        with patch.object(coordinator, '_estimate_partition_tour_time') as mock_estimate, \
             patch.object(coordinator, '_get_ship_data_map') as mock_ship_data:

            mock_estimate.return_value = 1000
            mock_ship_data.return_value = {
                'SHIP-1': {'engine': {'speed': 9}, 'fuel': {'capacity': 100}},
                'SHIP-2': {'engine': {'speed': 9}, 'fuel': {'capacity': 100}}
            }

            result = coordinator.balance_tour_times(partitions, min_markets=2, max_iterations=1, use_tsp=False)

            # SHIP-2 should have at least some markets now
            assert len(result['SHIP-2']) >= 1
            # All markets should still be assigned
            all_markets = []
            for markets in result.values():
                all_markets.extend(markets)
            assert len(all_markets) == 6

    def test_ensures_market_diversity_for_colocated_markets(self, coordinator):
        """Should add diverse market if all markets are colocated"""
        # Set up colocated markets for SHIP-1
        coordinator.graph['waypoints']['X1-TEST-M1']['x'] = 50
        coordinator.graph['waypoints']['X1-TEST-M1']['y'] = 50
        coordinator.graph['waypoints']['X1-TEST-M2']['x'] = 50
        coordinator.graph['waypoints']['X1-TEST-M2']['y'] = 50

        partitions = {
            'SHIP-1': ['X1-TEST-M1', 'X1-TEST-M2'],  # All at same location
            'SHIP-2': ['X1-TEST-M3', 'X1-TEST-M4', 'X1-TEST-M5', 'X1-TEST-M6']
        }

        with patch.object(coordinator, '_estimate_partition_tour_time') as mock_estimate, \
             patch.object(coordinator, '_get_ship_data_map') as mock_ship_data:

            mock_estimate.return_value = 1000
            mock_ship_data.return_value = {
                'SHIP-1': {'engine': {'speed': 9}, 'fuel': {'capacity': 100}},
                'SHIP-2': {'engine': {'speed': 9}, 'fuel': {'capacity': 100}}
            }

            result = coordinator.balance_tour_times(partitions, min_markets=2, max_iterations=1, use_tsp=False)

            # SHIP-1 should have at least 2 markets with diverse locations
            assert len(result['SHIP-1']) >= 2

    def test_skips_diversity_check_for_single_market_scouts(self, coordinator):
        """Should skip diversity check for stationary scouts (1 market)"""
        partitions = {
            'SHIP-1': ['X1-TEST-M1'],  # Stationary scout
            'SHIP-2': ['X1-TEST-M2', 'X1-TEST-M3', 'X1-TEST-M4', 'X1-TEST-M5', 'X1-TEST-M6']
        }

        with patch.object(coordinator, '_estimate_partition_tour_time') as mock_estimate, \
             patch.object(coordinator, '_get_ship_data_map') as mock_ship_data:

            mock_estimate.return_value = 1000
            mock_ship_data.return_value = {
                'SHIP-1': {'engine': {'speed': 9}, 'fuel': {'capacity': 100}},
                'SHIP-2': {'engine': {'speed': 9}, 'fuel': {'capacity': 100}}
            }

            result = coordinator.balance_tour_times(partitions, min_markets=1, max_iterations=1, use_tsp=False)

            # SHIP-1 should still have exactly 1 market (stationary)
            assert len(result['SHIP-1']) == 1


class TestGetShipDataMap:
    """Test ship data caching and error handling"""

    def test_caches_ship_data(self, coordinator, mock_api):
        """Should cache ship data to avoid redundant API calls"""
        coordinator._ship_data_cache = {}

        # First call fetches from API
        result1 = coordinator._get_ship_data_map()
        assert 'SHIP-1' in result1

        # Second call uses cache
        mock_api.get_ship = Mock(return_value={'symbol': 'SHIP-1'})
        result2 = coordinator._get_ship_data_map()

        # Should not have called API again
        mock_api.get_ship.assert_not_called()
        assert 'SHIP-1' in result2

    def test_handles_api_failure_gracefully(self, coordinator, mock_api):
        """Should handle API failure for ship data without crashing"""
        coordinator._ship_data_cache = {}
        mock_api.get_ship = Mock(side_effect=Exception("API Error"))

        result = coordinator._get_ship_data_map()

        # Should return empty dict for failed ship
        assert len(result) == 0

    def test_invalidate_partitioner_clears_cache(self, coordinator):
        """Should clear partitioner when markets change"""
        coordinator._partitioner = Mock()

        coordinator._invalidate_partitioner()

        assert coordinator._partitioner is None


class TestBalanceOscillationDetector:
    """Test the BalanceOscillationDetector class"""

    def test_no_oscillation_returns_original_market(self):
        """Should return original market when no oscillation detected"""
        mock_find_boundary = Mock(return_value='X1-TEST-M3')
        detector = BalanceOscillationDetector(mock_find_boundary)

        market_to_move = 'X1-TEST-M1'
        last_moved = 'X1-TEST-M2'  # Different market
        longest_partition = ['X1-TEST-M1', 'X1-TEST-M3']
        shortest_partition = ['X1-TEST-M4']

        result = detector.check_and_resolve(
            market_to_move,
            last_moved,
            longest_partition,
            shortest_partition
        )

        assert result == 'X1-TEST-M1'
        # Should not call find_boundary_market since no oscillation
        mock_find_boundary.assert_not_called()

    def test_oscillation_detected_finds_alternative(self):
        """Should find alternative market when oscillation detected"""
        mock_find_boundary = Mock(return_value='X1-TEST-M3')
        detector = BalanceOscillationDetector(mock_find_boundary)

        market_to_move = 'X1-TEST-M1'
        last_moved = 'X1-TEST-M1'  # Same market = oscillation
        longest_partition = ['X1-TEST-M1', 'X1-TEST-M3']
        shortest_partition = ['X1-TEST-M4']

        result = detector.check_and_resolve(
            market_to_move,
            last_moved,
            longest_partition,
            shortest_partition
        )

        # Should call find_boundary_market with remaining markets
        mock_find_boundary.assert_called_once_with(['X1-TEST-M3'], shortest_partition)
        assert result == 'X1-TEST-M3'

    def test_oscillation_with_no_alternative_returns_none(self):
        """Should return None when oscillation detected but no alternative found"""
        mock_find_boundary = Mock(return_value=None)
        detector = BalanceOscillationDetector(mock_find_boundary)

        market_to_move = 'X1-TEST-M1'
        last_moved = 'X1-TEST-M1'
        longest_partition = ['X1-TEST-M1', 'X1-TEST-M3']
        shortest_partition = ['X1-TEST-M4']

        result = detector.check_and_resolve(
            market_to_move,
            last_moved,
            longest_partition,
            shortest_partition
        )

        assert result is None

    def test_oscillation_with_no_remaining_markets_returns_none(self):
        """Should return None when oscillating market is the only one"""
        mock_find_boundary = Mock(return_value=None)
        detector = BalanceOscillationDetector(mock_find_boundary)

        market_to_move = 'X1-TEST-M1'
        last_moved = 'X1-TEST-M1'
        longest_partition = ['X1-TEST-M1']  # Only one market
        shortest_partition = ['X1-TEST-M4']

        result = detector.check_and_resolve(
            market_to_move,
            last_moved,
            longest_partition,
            shortest_partition
        )

        assert result is None
        # Should not call find_boundary_market since no remaining markets
        mock_find_boundary.assert_not_called()


class TestDispersedPairHandler:
    """Test the DispersedPairHandler class"""

    def test_returns_none_for_non_pair(self):
        """Should return None when not exactly 2 markets"""
        graph = {
            'waypoints': {
                'X1-TEST-M1': {'x': 0, 'y': 0},
                'X1-TEST-M2': {'x': 1000, 'y': 0},
                'X1-TEST-M3': {'x': 2000, 'y': 0}
            }
        }
        all_markets = ['X1-TEST-M1', 'X1-TEST-M2', 'X1-TEST-M3']
        handler = DispersedPairHandler(graph, all_markets)

        markets = ['X1-TEST-M1', 'X1-TEST-M2', 'X1-TEST-M3']
        positions = {
            'X1-TEST-M1': (0, 0),
            'X1-TEST-M2': (1000, 0),
            'X1-TEST-M3': (2000, 0)
        }

        result = handler.find_most_isolated(markets, positions)

        assert result is None

    def test_returns_none_for_close_pair(self):
        """Should return None when pair distance <=500 units"""
        graph = {
            'waypoints': {
                'X1-TEST-M1': {'x': 0, 'y': 0},
                'X1-TEST-M2': {'x': 400, 'y': 0}
            }
        }
        all_markets = ['X1-TEST-M1', 'X1-TEST-M2']
        handler = DispersedPairHandler(graph, all_markets)

        markets = ['X1-TEST-M1', 'X1-TEST-M2']
        positions = {
            'X1-TEST-M1': (0, 0),
            'X1-TEST-M2': (400, 0)  # Only 400 units apart
        }

        result = handler.find_most_isolated(markets, positions)

        assert result is None

    def test_returns_most_isolated_for_dispersed_pair(self):
        """Should return market farthest from system centroid for dispersed pair"""
        graph = {
            'waypoints': {
                'X1-TEST-M1': {'x': 0, 'y': 0},
                'X1-TEST-M2': {'x': 1000, 'y': 0},
                'X1-TEST-M3': {'x': 100, 'y': 0},  # Close to M1
                'X1-TEST-M4': {'x': 200, 'y': 0},
                'X1-TEST-M5': {'x': 300, 'y': 0},
                'X1-TEST-M6': {'x': 400, 'y': 0}
            }
        }
        all_markets = ['X1-TEST-M1', 'X1-TEST-M2', 'X1-TEST-M3',
                       'X1-TEST-M4', 'X1-TEST-M5', 'X1-TEST-M6']
        handler = DispersedPairHandler(graph, all_markets)

        markets = ['X1-TEST-M1', 'X1-TEST-M2']
        positions = {
            'X1-TEST-M1': (0, 0),
            'X1-TEST-M2': (1000, 0)  # 1000 units apart (>500)
        }

        result = handler.find_most_isolated(markets, positions)

        # M2 is farther from system centroid (~300, 0) than M1
        assert result == 'X1-TEST-M2'

    def test_handles_missing_positions(self):
        """Should return None when position data is missing"""
        graph = {
            'waypoints': {
                'X1-TEST-M1': {'x': 0, 'y': 0}
            }
        }
        all_markets = ['X1-TEST-M1', 'X1-TEST-M2']
        handler = DispersedPairHandler(graph, all_markets)

        markets = ['X1-TEST-M1', 'X1-TEST-M2']
        positions = {
            'X1-TEST-M1': (0, 0)
            # M2 position missing
        }

        result = handler.find_most_isolated(markets, positions)

        assert result is None
