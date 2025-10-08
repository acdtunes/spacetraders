import sys
from types import SimpleNamespace

import pytest

from spacetraders_bot.operations import daemon as daemon_ops
from spacetraders_bot.core import assignment_manager as core_assignment_module


class StubAssignmentManager:
    def __init__(self, player_id, assignments=None, assigned=None, released=None):
        self.player_id = player_id
        self._assignments = assignments or {}
        self.assigned = assigned if assigned is not None else []
        self.released = released if released is not None else []

    def list_all(self, include_stale=False):
        return self._assignments

    def assign(self, ship, operator, daemon_id, operation):
        self.assigned.append((ship, operator, daemon_id, operation))

    def release(self, ship, reason="manual"):
        self.released.append((ship, reason))


class StubDaemonManager:
    def __init__(self, player_id, status_map=None, start_result=True, stop_result=True):
        self.player_id = player_id
        self.status_map = status_map or {}
        self.start_calls = []
        self.stop_calls = []
        self.tail_calls = []
        self.cleanup_called = False
        self._start_result = start_result
        self._stop_result = stop_result

    def start(self, daemon_id, command):
        self.start_calls.append((daemon_id, command))
        return self._start_result

    def stop(self, daemon_id):
        self.stop_calls.append(daemon_id)
        return self._stop_result

    def status(self, daemon_id):
        return self.status_map.get(daemon_id)

    def list_all(self):
        return list(self.status_map.values())

    def tail_logs(self, daemon_id, lines):
        self.tail_calls.append((daemon_id, lines))

    def cleanup_stopped(self):
        self.cleanup_called = True


def test_daemon_start_operation_success(monkeypatch):
    assigned = []
    daemon_manager = StubDaemonManager(1)

    monkeypatch.setattr(daemon_ops, 'DaemonManager', lambda player_id: daemon_manager)
    monkeypatch.setattr(core_assignment_module, 'AssignmentManager', lambda player_id: StubAssignmentManager(player_id, assignments={}, assigned=assigned))
    monkeypatch.setattr(daemon_ops, 'AssignmentManager', core_assignment_module.AssignmentManager)

    args = SimpleNamespace(
        player_id=1,
        daemon_operation='mine',
        daemon_id=None,
        operation_args=['--ship', 'SHIP-1'],
    )

    result = daemon_ops.daemon_start_operation(args)

    assert result == 0
    daemon_id, command = daemon_manager.start_calls[0]
    assert daemon_id == 'mine_SHIP-1'
    assert ['--player-id', '1'] == command[-2:]
    assert command[0] == sys.executable
    assert assigned == [('SHIP-1', 'mining_operator', 'mine_SHIP-1', 'mine')]


def test_daemon_start_operation_ship_already_assigned(monkeypatch, capsys):
    assignments = {'SHIP-1': {'status': 'active', 'assigned_to': 'alpha', 'daemon_id': 'daemon-old'}}
    monkeypatch.setattr(daemon_ops, 'DaemonManager', lambda player_id: StubDaemonManager(player_id))
    monkeypatch.setattr(core_assignment_module, 'AssignmentManager', lambda player_id: StubAssignmentManager(player_id, assignments=assignments))
    monkeypatch.setattr(daemon_ops, 'AssignmentManager', core_assignment_module.AssignmentManager)

    args = SimpleNamespace(
        player_id=1,
        daemon_operation='mine',
        daemon_id=None,
        operation_args=['--ship', 'SHIP-1'],
    )

    result = daemon_ops.daemon_start_operation(args)
    assert result == 1
    output = capsys.readouterr().out
    assert 'already assigned' in output


def test_daemon_stop_operation_releases_assignment(monkeypatch):
    released = []
    command = [sys.executable, '-m', 'spacetraders_bot.cli', 'mine', '--ship', 'SHIP-1']
    daemon_manager = StubDaemonManager(1, status_map={'daemon-1': {'daemon_id': 'daemon-1', 'command': command}})

    monkeypatch.setattr(daemon_ops, 'DaemonManager', lambda player_id: daemon_manager)
    monkeypatch.setattr(core_assignment_module, 'AssignmentManager', lambda player_id: StubAssignmentManager(player_id, released=released))
    monkeypatch.setattr(daemon_ops, 'AssignmentManager', core_assignment_module.AssignmentManager)

    args = SimpleNamespace(player_id=1, daemon_id='daemon-1')
    result = daemon_ops.daemon_stop_operation(args)

    assert result == 0
    assert daemon_manager.stop_calls == ['daemon-1']
    assert released == [('SHIP-1', 'Daemon daemon-1 stopped')]


def test_daemon_status_operation_single(monkeypatch, capsys):
    status_map = {
        'daemon-1': {
            'daemon_id': 'daemon-1',
            'is_running': True,
            'pid': 123,
            'runtime_seconds': 45,
            'cpu_percent': 12.5,
            'memory_mb': 6.0,
            'command': ['cmd'],
            'log_file': '/tmp/log',
            'err_file': '/tmp/err',
            'started_at': '2025-10-08T00:00:00Z',
        }
    }
    monkeypatch.setattr(daemon_ops, 'DaemonManager', lambda player_id: StubDaemonManager(player_id, status_map=status_map))

    args = SimpleNamespace(player_id=1, daemon_id='daemon-1')
    assert daemon_ops.daemon_status_operation(args) == 0
    output = capsys.readouterr().out
    assert 'daemon-1' in output
    assert 'CPU' in output


def test_daemon_status_operation_list(monkeypatch, capsys):
    status_map = {
        'daemon-1': {
            'daemon_id': 'daemon-1',
            'is_running': False,
            'pid': 321,
            'runtime_seconds': None,
            'cpu_percent': 0,
            'memory_mb': 0,
            'command': [],
            'log_file': '',
            'err_file': '',
        }
    }
    monkeypatch.setattr(daemon_ops, 'DaemonManager', lambda player_id: StubDaemonManager(player_id, status_map=status_map))

    args = SimpleNamespace(player_id=1, daemon_id=None)
    assert daemon_ops.daemon_status_operation(args) == 0
    output = capsys.readouterr().out
    assert 'daemon-1' in output


def test_daemon_logs_operation(monkeypatch):
    daemon_manager = StubDaemonManager(1)
    monkeypatch.setattr(daemon_ops, 'DaemonManager', lambda player_id: daemon_manager)

    args = SimpleNamespace(player_id=1, daemon_id='daemon-1', lines=50)
    assert daemon_ops.daemon_logs_operation(args) == 0
    assert daemon_manager.tail_calls == [('daemon-1', 50)]


def test_daemon_cleanup_operation(monkeypatch):
    daemon_manager = StubDaemonManager(1)
    monkeypatch.setattr(daemon_ops, 'DaemonManager', lambda player_id: daemon_manager)

    args = SimpleNamespace(player_id=1)
    assert daemon_ops.daemon_cleanup_operation(args) == 0
    assert daemon_manager.cleanup_called is True
