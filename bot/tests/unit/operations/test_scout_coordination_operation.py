import json
from types import SimpleNamespace
from unittest.mock import MagicMock

import pytest

from spacetraders_bot.operations import scout_coordination as sc
class _DummyConn:
    def __enter__(self):
        return self

    def __exit__(self, exc_type, exc, tb):
        return False


class _DummyDB:
    def __init__(self, player):
        self._player = player

    def connection(self):
        return _DummyConn()

    def get_player_by_id(self, _conn, player_id):
        return self._player


@pytest.fixture(autouse=True)
def stub_logging(monkeypatch):
    monkeypatch.setattr(sc, 'setup_logging', lambda *args, **kwargs: None)


def _make_args(**overrides):
    defaults = {
        'ships': 'SHIP-1,SHIP-2',
        'player_id': 99,
        'system': 'X1-TEST',
        'ship': 'SHIP-3',
        'log_level': 'INFO',
        'interval': 1,
        'duration': 1,
    }
    defaults.update(overrides)
    return SimpleNamespace(**defaults)


def regression_coordinator_start_operation_happy_path(monkeypatch):
    dummy_coordinator = MagicMock()
    monkeypatch.setattr(sc, 'ScoutCoordinator', MagicMock(return_value=dummy_coordinator))
    monkeypatch.setattr(sc, 'get_database', lambda: _DummyDB({'token': 'XYZ'}))

    args = _make_args(ships='SHIP-1, SHIP-2')

    rc = sc.coordinator_start_operation(args)

    assert rc == 0
    sc.ScoutCoordinator.assert_called_once_with(
        system='X1-TEST',
        ships=['SHIP-1', 'SHIP-2'],
        token='XYZ',
        player_id=99,
    )
    dummy_coordinator.save_config.assert_called_once()
    dummy_coordinator.partition_and_start.assert_called_once()


def regression_coordinator_start_operation_player_missing(monkeypatch):
    monkeypatch.setattr(sc, 'ScoutCoordinator', MagicMock())
    monkeypatch.setattr(sc, 'get_database', lambda: _DummyDB(None))

    rc = sc.coordinator_start_operation(_make_args())

    assert rc == 1
    sc.ScoutCoordinator.assert_not_called()


def regression_coordinator_add_ship_operation_updates_config(tmp_path, monkeypatch):
    monkeypatch.setattr(sc.paths, 'AGENT_CONFIG_DIR', tmp_path)

    config_path = tmp_path / 'scout_config_X1-TEST.json'
    config_path.write_text(json.dumps({'ships': ['SHIP-1']}))

    args = _make_args(system='X1-TEST', ship='SHIP-2')

    rc = sc.coordinator_add_ship_operation(args)

    assert rc == 0

    updated = json.loads(config_path.read_text())
    assert updated['ships'] == ['SHIP-1', 'SHIP-2']
    assert updated['reconfigure'] is True


def regression_coordinator_add_ship_operation_rejects_duplicate(tmp_path, monkeypatch):
    monkeypatch.setattr(sc.paths, 'AGENT_CONFIG_DIR', tmp_path)

    config_path = tmp_path / 'scout_config_X1-TEST.json'
    config_path.write_text(json.dumps({'ships': ['SHIP-1', 'SHIP-2']}))

    args = _make_args(system='X1-TEST', ship='SHIP-2')

    rc = sc.coordinator_add_ship_operation(args)

    assert rc == 1
    updated = json.loads(config_path.read_text())
    assert updated['ships'] == ['SHIP-1', 'SHIP-2']


def regression_coordinator_remove_ship_operation_updates_config(tmp_path, monkeypatch):
    monkeypatch.setattr(sc.paths, 'AGENT_CONFIG_DIR', tmp_path)

    config_path = tmp_path / 'scout_config_X1-TEST.json'
    config_path.write_text(json.dumps({'ships': ['SHIP-1', 'SHIP-2']}))

    args = _make_args(system='X1-TEST', ship='SHIP-2')

    rc = sc.coordinator_remove_ship_operation(args)

    assert rc == 0
    updated = json.loads(config_path.read_text())
    assert updated['ships'] == ['SHIP-1']
    assert updated['reconfigure'] is True


def regression_coordinator_remove_ship_operation_prevents_last_ship(tmp_path, monkeypatch):
    monkeypatch.setattr(sc.paths, 'AGENT_CONFIG_DIR', tmp_path)

    config_path = tmp_path / 'scout_config_X1-TEST.json'
    config_path.write_text(json.dumps({'ships': ['SHIP-2']}))

    args = _make_args(system='X1-TEST', ship='SHIP-2')

    rc = sc.coordinator_remove_ship_operation(args)

    assert rc == 1
    updated = json.loads(config_path.read_text())
    assert updated['ships'] == ['SHIP-2']


def regression_coordinator_stop_operation_stops_daemons(tmp_path, monkeypatch):
    monkeypatch.setattr(sc.paths, 'AGENT_CONFIG_DIR', tmp_path)

    config_path = tmp_path / 'scout_config_X1-TEST.json'
    config_path.write_text(json.dumps({'ships': ['SHIP-1', 'SHIP-9']}))

    stopped = []

    class DummyDaemonManager:
        def __init__(self, player_id):
            self.player_id = player_id

        def is_running(self, daemon_id):
            return True

        def stop(self, daemon_id):
            stopped.append(daemon_id)

    monkeypatch.setattr('spacetraders_bot.core.daemon_manager.DaemonManager', DummyDaemonManager)

    args = _make_args(system='X1-TEST', player_id=7)

    rc = sc.coordinator_stop_operation(args)

    assert rc == 0
    assert stopped == ['scout-1', 'scout-9']
    assert not config_path.exists()


def regression_coordinator_stop_operation_handles_missing_config(tmp_path, monkeypatch):
    monkeypatch.setattr(sc.paths, 'AGENT_CONFIG_DIR', tmp_path)
    monkeypatch.setattr('spacetraders_bot.core.daemon_manager.DaemonManager', MagicMock())

    args = _make_args(system='X1-TEST', player_id=7)

    rc = sc.coordinator_stop_operation(args)

    assert rc == 0


def regression_coordinator_status_operation_reports_state(tmp_path, monkeypatch, capsys):
    monkeypatch.setattr(sc.paths, 'AGENT_CONFIG_DIR', tmp_path)

    config_path = tmp_path / 'scout_config_X1-TEST.json'
    config_path.write_text(json.dumps({'ships': ['SHIP-1'], 'algorithm': 'greedy'}))

    class DummyDaemonManager:
        def __init__(self, player_id):
            self.player_id = player_id

        def is_running(self, daemon_id):
            return daemon_id == 'scout-1'

    monkeypatch.setattr('spacetraders_bot.core.daemon_manager.DaemonManager', DummyDaemonManager)

    args = _make_args(system='X1-TEST', player_id=5)

    rc = sc.coordinator_status_operation(args)

    assert rc == 0
    captured = capsys.readouterr().out
    assert 'SCOUT COORDINATOR STATUS' in captured
    assert '🟢' in captured
