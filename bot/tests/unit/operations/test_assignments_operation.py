from types import SimpleNamespace

import pytest

from spacetraders_bot.operations import assignments


class StubManager:
    def __init__(self, **kwargs):
        self.__dict__.update(kwargs)


def regression_assignment_list_requires_player_id(capsys):
    result = assignments.assignment_list_operation(SimpleNamespace())
    assert result == 1
    assert 'required' in capsys.readouterr().out


def regression_assignment_list_prints_assignments(monkeypatch, capsys):
    listing = {
        'SHIP-1': {'status': 'active', 'assigned_to': 'AI', 'daemon_id': 'daemon-1', 'operation': 'mining'},
        'SHIP-2': {'status': 'idle', 'assigned_to': None, 'daemon_id': None, 'operation': None},
        'SHIP-3': {'status': 'stale', 'assigned_to': 'Ops', 'daemon_id': 'daemon-3', 'operation': 'scouting'},
        'SHIP-4': {'status': 'unknown', 'assigned_to': None, 'daemon_id': None, 'operation': None},
    }
    stub = StubManager(list_all=lambda include_stale: listing)
    monkeypatch.setattr(assignments, 'AssignmentManager', lambda player_id: stub)

    args = SimpleNamespace(player_id=1, include_stale=False)
    result = assignments.assignment_list_operation(args)

    assert result == 0
    output = capsys.readouterr().out
    assert 'SHIP-1' in output
    assert 'AI' in output
    assert 'SHIP-2' in output and '⚪' in output
    assert 'SHIP-3' in output and '⚠️' in output
    assert '❓' in output


def regression_assignment_list_no_assignments(monkeypatch, capsys):
    stub = StubManager(list_all=lambda include_stale: {})
    monkeypatch.setattr(assignments, 'AssignmentManager', lambda player_id: stub)

    args = SimpleNamespace(player_id=1)
    assert assignments.assignment_list_operation(args) == 0
    output = capsys.readouterr().out
    assert 'No ship assignments' in output


def regression_assignment_assign_operation(monkeypatch):
    captured = {}

    def assign(ship, operator, daemon_id, operation_type, metadata=None):
        captured['args'] = (ship, operator, daemon_id, operation_type, metadata)
        return True

    stub = StubManager(assign=assign)
    monkeypatch.setattr(assignments, 'AssignmentManager', lambda player_id: stub)

    args = SimpleNamespace(
        player_id=1,
        ship='SHIP-1',
        operator='AI',
        daemon_id='daemon-1',
        operation_type='mining',
        duration=60,
    )

    assert assignments.assignment_assign_operation(args) == 0
    ship, operator, daemon_id, op_type, metadata = captured['args']
    assert metadata['duration'] == 60


def regression_assignment_assign_operation_failure(monkeypatch):
    stub = StubManager(assign=lambda *a, **k: False)
    monkeypatch.setattr(assignments, 'AssignmentManager', lambda player_id: stub)

    args = SimpleNamespace(player_id=1, ship='SHIP-1', operator='AI', daemon_id='daemon-1', operation_type='mining')
    assert assignments.assignment_assign_operation(args) == 1


def regression_assignment_release_operation_success(monkeypatch):
    stub = StubManager(release=lambda ship, reason=None: True)
    monkeypatch.setattr(assignments, 'AssignmentManager', lambda player_id: stub)

    args = SimpleNamespace(player_id=1, ship='SHIP-1', reason='complete')
    assert assignments.assignment_release_operation(args) == 0


def regression_assignment_available_operation_success(monkeypatch, capsys):
    stub = StubManager(is_available=lambda ship: True)
    monkeypatch.setattr(assignments, 'AssignmentManager', lambda player_id: stub)

    args = SimpleNamespace(player_id=1, ship='SHIP-1')
    assert assignments.assignment_available_operation(args) == 0
    assert 'available' in capsys.readouterr().out.lower()


def regression_assignment_available_unavailable(monkeypatch, capsys):
    stub = StubManager(
        is_available=lambda ship: False,
        get_assignment=lambda ship: {
            'assigned_to': 'AI',
            'daemon_id': 'daemon-1',
            'operation': 'mining',
            'assigned_at': 'now'
        }
    )
    monkeypatch.setattr(assignments, 'AssignmentManager', lambda player_id: stub)

    args = SimpleNamespace(player_id=1, ship='SHIP-1')
    result = assignments.assignment_available_operation(args)

    assert result == 1
    output = capsys.readouterr().out
    assert 'daemon-1' in output


def regression_assignment_find_no_available(monkeypatch, capsys):
    stub = StubManager(find_available=lambda requirements=None: [])
    monkeypatch.setattr(assignments, 'AssignmentManager', lambda player_id: stub)

    args = SimpleNamespace(player_id=1, cargo_min=10, fuel_min=5)
    result = assignments.assignment_find_operation(args)

    assert result == 0
    assert 'No ships available' in capsys.readouterr().out


def regression_assignment_find_available(monkeypatch, capsys):
    stub = StubManager(find_available=lambda requirements=None: ['SHIP-1'])
    monkeypatch.setattr(assignments, 'AssignmentManager', lambda player_id: stub)

    args = SimpleNamespace(player_id=1, cargo_min=None, fuel_min=None)
    result = assignments.assignment_find_operation(args)

    assert result == 0
    assert 'SHIP-1' in capsys.readouterr().out


def regression_assignment_sync_operation(monkeypatch, capsys):
    stub = StubManager(sync_with_daemons=lambda: {'released': ['SHIP-1'], 'still_active': ['SHIP-2']})
    monkeypatch.setattr(assignments, 'AssignmentManager', lambda player_id: stub)

    args = SimpleNamespace(player_id=1)
    result = assignments.assignment_sync_operation(args)

    assert result == 0
    out = capsys.readouterr().out
    assert 'SHIP-1' in out and 'SHIP-2' in out


def regression_assignment_reassign_operation_no_ships(capsys):
    args = SimpleNamespace(player_id=1, ships='', from_operation='mining')
    assert assignments.assignment_reassign_operation(args) == 1
    assert 'No ships specified' in capsys.readouterr().out


def regression_assignment_reassign_operation_success(monkeypatch, capsys):
    captured = {}

    def reassign_ships(ships, from_operation, stop_daemons=True, timeout=10):
        captured['args'] = (ships, from_operation, stop_daemons, timeout)
        return True

    stub = StubManager(reassign_ships=reassign_ships)
    monkeypatch.setattr(assignments, 'AssignmentManager', lambda player_id: stub)

    args = SimpleNamespace(player_id=1, ships='SHIP-1,SHIP-2', from_operation='mining', no_stop=True, timeout=5)
    result = assignments.assignment_reassign_operation(args)

    assert result == 0
    ships, from_operation, stop_daemons, timeout = captured['args']
    assert ships == ['SHIP-1', 'SHIP-2']
    assert not stop_daemons
    assert timeout == 5
    assert 'Reassignment complete' in capsys.readouterr().out


def regression_assignment_reassign_operation_failure(monkeypatch, capsys):
    stub = StubManager(reassign_ships=lambda *a, **k: False)
    monkeypatch.setattr(assignments, 'AssignmentManager', lambda player_id: stub)

    args = SimpleNamespace(player_id=1, ships='SHIP-1', from_operation='mining')
    assert assignments.assignment_reassign_operation(args) == 1
    assert 'errors' in capsys.readouterr().out.lower()


def regression_assignment_status_unknown(monkeypatch, capsys):
    stub = StubManager(get_assignment=lambda ship: None)
    monkeypatch.setattr(assignments, 'AssignmentManager', lambda player_id: stub)

    args = SimpleNamespace(player_id=1, ship='SHIP-1')
    result = assignments.assignment_status_operation(args)

    assert result == 0
    assert 'available' in capsys.readouterr().out.lower()


def regression_assignment_status_details(monkeypatch, capsys):
    assignment = {
        'status': 'stale',
        'assigned_to': 'AI',
        'daemon_id': 'daemon-1',
        'operation': 'mining'
    }
    stub = StubManager(get_assignment=lambda ship: assignment)
    monkeypatch.setattr(assignments, 'AssignmentManager', lambda player_id: stub)

    args = SimpleNamespace(player_id=1, ship='SHIP-1')
    result = assignments.assignment_status_operation(args)

    assert result == 0
    output = capsys.readouterr().out
    assert 'STALE' in output
