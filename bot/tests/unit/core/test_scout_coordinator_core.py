import json
from pathlib import Path
from types import MethodType, SimpleNamespace

import pytest

import spacetraders_bot.core.scout_coordinator as coordinator_module
from spacetraders_bot.core.scout_coordinator import SubtourAssignment
from spacetraders_bot.core.system_graph_provider import GraphLoadResult


class APIStub:
    def __init__(self, token):
        self.token = token

    def get_ship(self, ship_symbol):
        return {
            'symbol': ship_symbol,
            'nav': {'waypointSymbol': 'X1-TEST-A1'},
            'engine': {'speed': 10},
            'fuel': {'current': 100, 'capacity': 100},
        }


class DaemonStub:
    def __init__(self, player_id):
        self.player_id = player_id


class AssignmentStub:
    def __init__(self, player_id):
        self.player_id = player_id


@pytest.fixture(autouse=True)
def patch_shared_dependencies(monkeypatch):
    monkeypatch.setattr(coordinator_module.paths, 'ensure_dirs', lambda dirs: None)
    monkeypatch.setattr(coordinator_module, 'APIClient', APIStub)
    monkeypatch.setattr(coordinator_module, 'DaemonManager', DaemonStub)
    monkeypatch.setattr(coordinator_module, 'AssignmentManager', AssignmentStub)
    monkeypatch.setattr(coordinator_module.signal, 'signal', lambda *args, **kwargs: None)


def _patch_markets(monkeypatch):
    monkeypatch.setattr(
        coordinator_module.TourOptimizer,
        'get_markets_from_graph',
        lambda graph: list(graph['waypoints'].keys()),
    )


def _base_graph():
    return {
        'system': 'X1-TEST',
        'waypoints': {
            'X1-TEST-M1': {'x': 0, 'y': 0, 'traits': ['MARKETPLACE']},
            'X1-TEST-M2': {'x': 200, 'y': 0, 'traits': ['MARKETPLACE']},
            'X1-TEST-M3': {'x': 0, 'y': 150, 'traits': ['MARKETPLACE']},
            'X1-TEST-M4': {'x': 200, 'y': 150, 'traits': ['MARKETPLACE']},
        },
        'edges': [],
    }


def _make_coordinator(monkeypatch, load_graph, build_graph=None):
    calls = {'count': 0, 'source': None}

    class ProviderStub:
        def __init__(self, api):
            self.api = api

        def get_graph(self, system_symbol):
            calls['count'] += 1
            if load_graph is not None:
                calls['source'] = 'database'
                return GraphLoadResult(
                    graph=load_graph,
                    source='database',
                    message=f"📊 Loaded graph for {system_symbol} from test stub",
                )
            if build_graph is not None:
                calls['source'] = 'api'
                return GraphLoadResult(
                    graph=build_graph,
                    source='api',
                    message=f"📊 Built graph for {system_symbol} in test stub",
                )
            raise RuntimeError("Graph not available in ProviderStub")

    monkeypatch.setattr(coordinator_module, 'SystemGraphProvider', lambda api: ProviderStub(api))
    _patch_markets(monkeypatch)
    coord = coordinator_module.ScoutCoordinator(
        system='X1-TEST',
        ships=['SHIP-1', 'SHIP-2'],
        token='TOKEN',
        player_id=42,
    )
    return coord, calls


def test_coordinator_uses_cached_graph(monkeypatch):
    graph = _base_graph()
    coord, calls = _make_coordinator(monkeypatch, load_graph=graph, build_graph=None)

    assert calls['count'] == 1
    assert calls['source'] == 'database'
    assert coord.graph is graph
    assert coord.markets == list(graph['waypoints'].keys())


def test_coordinator_builds_graph_when_missing(monkeypatch):
    graph = _base_graph()
    coord, calls = _make_coordinator(monkeypatch, load_graph=None, build_graph=graph)

    assert calls['count'] == 1
    assert calls['source'] == 'api'
    assert coord.graph == graph


def test_partition_markets_geographic_vertical(monkeypatch):
    graph = _base_graph()
    coord, _ = _make_coordinator(monkeypatch, load_graph=graph)
    coord.ships = {'SHIP-1', 'SHIP-2'}
    coord.graph = graph
    coord.markets = list(graph['waypoints'].keys())[:2]

    partitions = coord.partition_markets_geographic()

    assert partitions['SHIP-1'] == ['X1-TEST-M1']
    assert partitions['SHIP-2'] == ['X1-TEST-M2']


def test_partition_markets_geographic_horizontal(monkeypatch):
    graph = _base_graph()
    coord, _ = _make_coordinator(monkeypatch, load_graph=graph)
    coord.ships = {'SHIP-1', 'SHIP-2'}
    coord.graph = graph
    coord.markets = ['X1-TEST-M1', 'X1-TEST-M3']

    partitions = coord.partition_markets_geographic()

    assert partitions['SHIP-1'] == ['X1-TEST-M1']
    assert partitions['SHIP-2'] == ['X1-TEST-M3']


def test_partition_markets_geographic_even_distribution(monkeypatch):
    graph = _base_graph()
    # Collapse all coordinates to trigger even distribution
    for wp in graph['waypoints'].values():
        wp['x'] = 0
        wp['y'] = 0

    coord, _ = _make_coordinator(monkeypatch, load_graph=graph)
    coord.ships = {'SHIP-1', 'SHIP-2'}
    coord.graph = graph
    coord.markets = list(graph['waypoints'].keys())

    partitions = coord.partition_markets_geographic()

    total = sum(len(v) for v in partitions.values())
    assert total == len(coord.markets)
    assert max(len(v) for v in partitions.values()) - min(len(v) for v in partitions.values()) <= 1


def test_partition_markets_greedy_assigns_all(monkeypatch):
    graph = _base_graph()
    coord, _ = _make_coordinator(monkeypatch, load_graph=graph)
    coord.graph = graph
    coord.markets = list(graph['waypoints'].keys())

    partitions = coord.partition_markets_greedy()

    assigned = sum(len(v) for v in partitions.values())
    assert assigned == len(coord.markets)


def test_start_scout_daemon_available(monkeypatch):
    graph = _base_graph()
    coord, _ = _make_coordinator(monkeypatch, load_graph=graph)

    class AssignmentHarness:
        def __init__(self):
            self.available = True
            self.assigned = []

        def is_available(self, ship):
            return self.available

        def get_assignment(self, ship):
            return {}

        def release(self, ship, reason=None):
            self.released = (ship, reason)
            return True

        def assign(self, ship, operator, daemon_id, operation, metadata=None):
            self.assigned.append((ship, operator, daemon_id, operation, metadata))
            return True

    class DaemonHarness:
        def __init__(self):
            self.started = []

        def start(self, daemon_id, command):
            self.started.append((daemon_id, command))
            return True

    assignment = AssignmentHarness()
    daemon = DaemonHarness()
    coord.assignment_manager = assignment
    coord.daemon_manager = daemon

    monkeypatch.setattr(coordinator_module.time, 'time', lambda: 1700000000)

    result = coord.start_scout_daemon('SHIP-1', ['M1', 'M2'])

    assert result.startswith('scout-1-')
    assert daemon.started
    ship, operator, daemon_id, operation, metadata = assignment.assigned[0]
    assert ship == 'SHIP-1'
    assert operation == 'scout-markets'
    assert metadata['markets'] == ['M1', 'M2']


def test_start_scout_daemon_existing_assignment(monkeypatch):
    graph = _base_graph()
    coord, _ = _make_coordinator(monkeypatch, load_graph=graph)

    class AssignmentHarness:
        def __init__(self):
            self.available = False
            self.releases = []
            self.assign_calls = []

        def is_available(self, ship):
            return False

        def get_assignment(self, ship):
            return {'daemon_id': 'old-daemon'}

        def release(self, ship, reason=None):
            self.releases.append((ship, reason))
            return True

        def assign(self, ship, operator, daemon_id, operation, metadata=None):
            self.assign_calls.append(daemon_id)
            return True

    class DaemonHarness:
        def __init__(self):
            self.stop_calls = []
            self.start_calls = []

        def stop(self, daemon_id, timeout=15):
            self.stop_calls.append((daemon_id, timeout))
            return True

        def start(self, daemon_id, command):
            self.start_calls.append(daemon_id)
            return True

    assignment = AssignmentHarness()
    daemon = DaemonHarness()
    coord.assignment_manager = assignment
    coord.daemon_manager = daemon

    monkeypatch.setattr(coordinator_module.time, 'time', lambda: 1700000001)

    result = coord.start_scout_daemon('SHIP-2', ['M1'])

    assert result.startswith('scout-2-')
    assert daemon.stop_calls[0][0] == 'old-daemon'
    assert assignment.releases[0][0] == 'SHIP-2'


def test_optimize_subtour_uses_two_opt(monkeypatch):
    graph = _base_graph()
    coord, _ = _make_coordinator(monkeypatch, load_graph=graph)
    coord.graph = graph
    coord.algorithm = '2opt'

    class ShipAPIStub(APIStub):
        def get_ship(self, symbol):
            data = super().get_ship(symbol)
            data['fuel'] = {'capacity': 100, 'current': 80}
            return data

    coord.api = ShipAPIStub('TOKEN')

    class OptimizerHarness:
        def __init__(self, graph, ship_data):
            self.graph = graph
            self.ship_data = ship_data
            self.nn_called = False
            self.two_opt_called = False

        def solve_nearest_neighbor(self, start, markets, fuel, return_to_start=True):
            self.nn_called = True
            return {'total_time': 120, 'legs': []}

        def two_opt_improve(self, tour, max_iterations=100):
            self.two_opt_called = True
            tour['total_time'] = 110
            return tour

    optimizer_instance = OptimizerHarness(graph, {})
    monkeypatch.setattr(coordinator_module, 'TourOptimizer', lambda graph, ship_data: optimizer_instance)

    tour = coord.optimize_subtour('SHIP-1', ['X1-TEST-M1', 'X1-TEST-M2'])

    assert optimizer_instance.nn_called and optimizer_instance.two_opt_called
    assert tour['total_time'] == 110


def test_partition_and_start_records_assignments(monkeypatch):
    graph = _base_graph()
    coord, _ = _make_coordinator(monkeypatch, load_graph=graph)
    coord.graph = graph
    coord.markets = ['X1-TEST-M1', 'X1-TEST-M2']

    class AssignmentHarness:
        def __init__(self):
            self.records = []

        def assign(self, ship, operator, daemon_id, operation, metadata=None):
            self.records.append((ship, daemon_id))
            return True

        def is_available(self, ship):
            return True

    class DaemonHarness:
        def __init__(self):
            self.started = []

        def start(self, daemon_id, command):
            self.started.append(daemon_id)
            return True

        def is_running(self, daemon_id):
            return True

    assignment = AssignmentHarness()
    daemon = DaemonHarness()
    coord.assignment_manager = assignment
    coord.daemon_manager = daemon

    monkeypatch.setattr(coordinator_module.time, 'time', lambda: 1700000100)
    monkeypatch.setattr(coordinator_module.ScoutCoordinator, 'optimize_subtour', lambda self, ship, markets: {'total_time': 120})
    monkeypatch.setattr(coordinator_module.ScoutCoordinator, 'start_scout_daemon', lambda self, ship, markets: f'id-{ship}')

    coord.partition_and_start()

    assert coord.assignments['SHIP-1'].daemon_id == 'id-SHIP-1'


def test_start_scout_daemon_start_failure(monkeypatch):
    graph = _base_graph()
    coord, _ = _make_coordinator(monkeypatch, load_graph=graph)

    class AssignmentHarness:
        def assign(self, *args, **kwargs):
            raise AssertionError('should not assign on failure')

        def is_available(self, ship):
            return True

    class DaemonHarness:
        def start(self, daemon_id, command):
            return False

    coord.assignment_manager = AssignmentHarness()
    coord.daemon_manager = DaemonHarness()

    monkeypatch.setattr(coordinator_module.time, 'time', lambda: 100)

    assert coord.start_scout_daemon('SHIP-1', ['M']) is None


def test_save_config_and_check_reconfigure(tmp_path, monkeypatch):
    graph = _base_graph()
    coord, _ = _make_coordinator(monkeypatch, load_graph=graph)
    coord.config_file = tmp_path / 'config.json'

    coord.save_config()
    assert coord.config_file.exists()

    # Write reconfigure flag
    config = json.loads(coord.config_file.read_text())
    config['reconfigure'] = True
    coord.config_file.write_text(json.dumps(config))

    assert coord._check_reconfigure_signal() is True


def test_handle_reconfiguration_updates_ships(tmp_path, monkeypatch):
    graph = _base_graph()
    coord, _ = _make_coordinator(monkeypatch, load_graph=graph)
    coord.graph = graph
    coord.config_file = tmp_path / 'config.json'
    coord.assignments = {
        'SHIP-1': SubtourAssignment('SHIP-1', ['M1'], 100, 'daemon-old')
    }

    class DaemonHarness:
        def __init__(self):
            self.stopped = []

        def stop(self, daemon_id):
            self.stopped.append(daemon_id)
            return True

    class AssignmentHarness:
        def __init__(self):
            self.released = []

        def release(self, ship, reason=None):
            self.released.append((ship, reason))
            return True

    coord.daemon_manager = DaemonHarness()
    coord.assignment_manager = AssignmentHarness()

    monkeypatch.setattr(coordinator_module.time, 'time', lambda: 200)
    monkeypatch.setattr(coordinator_module.ScoutCoordinator, '_wait_for_tours_complete', lambda self, timeout=300: None)
    called = {}

    def fake_partition_and_start(self):
        called['partition'] = True

    monkeypatch.setattr(coordinator_module.ScoutCoordinator, 'partition_and_start', fake_partition_and_start)

    config = {
        'ships': ['SHIP-2'],
        'reconfigure': True
    }
    coord.config_file.write_text(json.dumps(config))

    coord._handle_reconfiguration()

    assert 'daemon-old' in coord.daemon_manager.stopped
    assert called.get('partition') is True
    new_config = json.loads(coord.config_file.read_text())
    assert new_config['reconfigure'] is False
def test_estimate_partition_tour_time(monkeypatch):
    graph = _base_graph()
    coord, _ = _make_coordinator(monkeypatch, load_graph=graph)
    coord.graph = graph

    markets = ['X1-TEST-M1', 'X1-TEST-M2', 'X1-TEST-M3']
    ship_data = coord.api.get_ship('SHIP-1')
    tour_time = coord._estimate_partition_tour_time(markets, ship_data)

    assert tour_time > 0


def test_balance_tour_times_reallocates(monkeypatch):
    graph = _base_graph()
    coord, _ = _make_coordinator(monkeypatch, load_graph=graph)
    coord.graph = graph
    coord.markets = list(graph['waypoints'].keys())

    partitions = {
        'SHIP-1': ['X1-TEST-M1', 'X1-TEST-M2', 'X1-TEST-M3', 'X1-TEST-M4'],
        'SHIP-2': [],
    }

    balanced = coord.balance_tour_times(partitions, min_markets=1, variance_threshold=1.0)

    assert balanced['SHIP-2']


def test_balance_tour_times_missing_ship_data(monkeypatch):
    graph = _base_graph()
    coord, _ = _make_coordinator(monkeypatch, load_graph=graph)
    coord.graph = graph

    def missing_ship_data(ship):
        return None

    coord.api.get_ship = missing_ship_data
    coord._estimate_partition_tour_time = MethodType(lambda self, markets, ship_data: 0.0, coord)
    coord._calculate_partition_tour_time = MethodType(lambda self, markets, ship_data: 0.0, coord)

    partitions = {
        'SHIP-1': ['X1-TEST-M1'],
        'SHIP-2': [],
    }

    balanced = coord.balance_tour_times(partitions, use_tsp=False)

    # Without ship data, partitions should remain unchanged and tour time zero
    assert balanced == partitions


def test_balance_tour_times_no_boundary_market(monkeypatch):
    graph = _base_graph()
    coord, _ = _make_coordinator(monkeypatch, load_graph=graph)
    coord.graph = graph

    partitions = {
        'SHIP-1': ['X1-TEST-M1'],
        'SHIP-2': [],
    }

    coord._find_boundary_market = lambda source, target: None

    result = coord.balance_tour_times(partitions, min_markets=1)

    # When no market can be moved, partitions stay the same
    assert result == partitions


def test_balance_tour_times_tsp_path(monkeypatch):
    graph = _base_graph()
    coord, _ = _make_coordinator(monkeypatch, load_graph=graph)
    coord.graph = graph

    calls = {}

    class OptimizerStub:
        def __init__(self, graph, ship_data):
            calls['init'] = True

        def solve_nearest_neighbor(self, *args, **kwargs):
            return {'total_time': 120, 'legs': []}

        def two_opt_improve(self, tour, max_iterations=100):
            tour['total_time'] = 110
            return tour

    monkeypatch.setattr(coordinator_module, 'TourOptimizer', OptimizerStub)

    partitions = {
        'SHIP-1': ['X1-TEST-M1', 'X1-TEST-M2'],
    }

    result = coord.balance_tour_times(partitions, use_tsp=True, variance_threshold=0.0)

    assert result['SHIP-1']
    assert calls.get('init') is True


def test_monitor_cycle_triggers_reconfigure(monkeypatch):
    graph = _base_graph()
    coord, _ = _make_coordinator(monkeypatch, load_graph=graph)
    coord.config_file = str(Path('/tmp/nonexistent'))
    coord.assignments = {}

    flag = {}
    monkeypatch.setattr(coord, '_check_reconfigure_signal', lambda: True)
    monkeypatch.setattr(coord, '_handle_reconfiguration', lambda: flag.setdefault('called', True))

    coord._monitor_cycle(check_interval=5)

    assert flag.get('called') is True


def test_monitor_cycle_restarts_daemon(monkeypatch):
    graph = _base_graph()
    coord, _ = _make_coordinator(monkeypatch, load_graph=graph)
    coord.assignments = {
        'SHIP-1': SubtourAssignment('SHIP-1', ['M1'], 100, 'daemon-old')
    }

    health_record = coordinator_module.DaemonHealth(ship='SHIP-1', daemon_id='daemon-old', is_running=False)
    monkeypatch.setattr(coord, '_check_reconfigure_signal', lambda: False)
    monkeypatch.setattr(coord, '_collect_daemon_health', lambda: [health_record])

    restarted = {}
    monkeypatch.setattr(coord, '_restart_daemon_for', lambda ship, daemon: restarted.setdefault(ship, daemon))
    monkeypatch.setattr(coordinator_module.time, 'sleep', lambda *_: None)

    coord._monitor_cycle(check_interval=1)

    assert restarted.get('SHIP-1') == 'daemon-old'


def test_restart_daemon_for_missing_assignment(monkeypatch, capsys):
    graph = _base_graph()
    coord, _ = _make_coordinator(monkeypatch, load_graph=graph)
    coord.assignments = {}

    coord._restart_daemon_for('SHIP-1', 'daemon-1')

    captured = capsys.readouterr().out
    assert 'No assignment found for ship' in captured


def test_check_reconfigure_signal_invalid_json(tmp_path, monkeypatch):
    graph = _base_graph()
    coord, _ = _make_coordinator(monkeypatch, load_graph=graph)

    config_path = tmp_path / 'config.json'
    config_path.write_text('{invalid')
    coord.config_file = str(config_path)

    assert coord._check_reconfigure_signal() is False


def test_handle_reconfiguration_no_changes(tmp_path, monkeypatch):
    graph = _base_graph()
    coord, _ = _make_coordinator(monkeypatch, load_graph=graph)
    coord.config_file = str(tmp_path / 'config.json')
    coord.ships = {'SHIP-1'}

    config = {
        'system': 'X1-TEST',
        'ships': ['SHIP-1'],
        'algorithm': '2opt',
        'reconfigure': True
    }
    Path(coord.config_file).write_text(json.dumps(config))

    coord._handle_reconfiguration()

    new_config = json.loads(Path(coord.config_file).read_text())
    assert new_config['reconfigure'] is False


def test_wait_for_tours_complete_timeout(monkeypatch):
    graph = _base_graph()
    coord, _ = _make_coordinator(monkeypatch, load_graph=graph)
    coord.assignments = {
        'SHIP-1': SubtourAssignment('SHIP-1', ['M1'], 100, 'daemon-1')
    }

    start = 0

    def fake_time():
        nonlocal start
        start += 50
        return start

    monkeypatch.setattr(coordinator_module.time, 'time', fake_time)
    monkeypatch.setattr(coordinator_module.time, 'sleep', lambda *_: None)
    coord.daemon_manager = SimpleNamespace(is_running=lambda daemon_id: True, stop=lambda *_: None)

    coord._wait_for_tours_complete(timeout=100)


def test_wait_for_tours_complete_success(monkeypatch):
    graph = _base_graph()
    coord, _ = _make_coordinator(monkeypatch, load_graph=graph)
    coord.assignments = {
        'SHIP-1': SubtourAssignment('SHIP-1', ['M1'], 100, 'daemon-1')
    }

    state = {'calls': 0}

    def fake_is_running(_daemon_id):
        state['calls'] += 1
        return state['calls'] < 2

    coord.daemon_manager = SimpleNamespace(is_running=fake_is_running, stop=lambda *_: None)
    monkeypatch.setattr(coordinator_module.time, 'time', lambda: 0)
    monkeypatch.setattr(coordinator_module.time, 'sleep', lambda *_: None)

    coord._wait_for_tours_complete(timeout=10)

def test_partition_markets_kmeans(monkeypatch):
    graph = _base_graph()
    coord, _ = _make_coordinator(monkeypatch, load_graph=graph)
    coord.graph = graph
    coord.algorithm = 'kmeans'

    partitions = coord.partition_markets_kmeans()
    assert sum(len(v) for v in partitions.values()) == len(coord.markets)


def test_monitor_and_restart(monkeypatch):
    graph = _base_graph()
    coord, _ = _make_coordinator(monkeypatch, load_graph=graph)
    coord.assignments = {
        'SHIP-1': SubtourAssignment('SHIP-1', ['M1'], 60, 'daemon-1')
    }

    class DaemonHarness:
        def __init__(self):
            self.running_checks = 0

        def is_running(self, daemon_id):
            self.running_checks += 1
            return False

    restarts = []

    def fake_start(self, ship, markets):
        restarts.append((ship, tuple(markets)))
        coord.running = False
        return 'daemon-new'

    coord.daemon_manager = DaemonHarness()
    coord.start_scout_daemon = fake_start.__get__(coord, type(coord))
    coord.running = True

    monkeypatch.setattr(coordinator_module.ScoutCoordinator, '_check_reconfigure_signal', lambda self: False)
    monkeypatch.setattr(coordinator_module.time, 'sleep', lambda seconds: setattr(coord, 'running', False))

    coord.monitor_and_restart()

    assert restarts == [('SHIP-1', ('M1',))]


def test_stop_all(monkeypatch):
    graph = _base_graph()
    coord, _ = _make_coordinator(monkeypatch, load_graph=graph)
    coord.assignments = {
        'SHIP-1': SubtourAssignment('SHIP-1', ['M1'], 60, 'daemon-1'),
        'SHIP-2': SubtourAssignment('SHIP-2', ['M2'], 60, 'daemon-2'),
    }

    class DaemonHarness:
        def __init__(self):
            self.stop_calls = []

        def stop(self, daemon_id):
            self.stop_calls.append(daemon_id)
            return True

    class AssignmentHarness:
        def __init__(self):
            self.release_calls = []

        def release(self, ship, reason=None):
            self.release_calls.append((ship, reason))
            return True

    coord.daemon_manager = DaemonHarness()
    coord.assignment_manager = AssignmentHarness()

    coord.stop_all()

    assert set(coord.daemon_manager.stop_calls) == {'daemon-1', 'daemon-2'}
    assert ('SHIP-1', 'coordinator_shutdown') in coord.assignment_manager.release_calls
    assert ('SHIP-2', 'coordinator_shutdown') in coord.assignment_manager.release_calls
