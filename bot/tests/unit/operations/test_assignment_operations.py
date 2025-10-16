from types import SimpleNamespace
from unittest.mock import MagicMock, patch

import pytest

from spacetraders_bot.operations import assignments as ops


def ns(**kwargs):
    return SimpleNamespace(**kwargs)


@patch("spacetraders_bot.operations.assignments.AssignmentManager")
def regression_assignment_list_operation_requires_player_id(mock_manager_cls):
    result = ops.assignment_list_operation(ns())
    assert result == 1
    mock_manager_cls.assert_not_called()


@patch("spacetraders_bot.operations.assignments.AssignmentManager")
def regression_assignment_list_operation_no_assignments(mock_manager_cls, capsys):
    mock_manager = mock_manager_cls.return_value
    mock_manager.list_all.return_value = {}

    result = ops.assignment_list_operation(ns(player_id=7))

    assert result == 0
    mock_manager.list_all.assert_called_once_with(include_stale=False)
    captured = capsys.readouterr()
    assert "No ship assignments" in captured.out


@patch("spacetraders_bot.operations.assignments.AssignmentManager")
def regression_assignment_list_operation_with_assignments(mock_manager_cls, capsys):
    mock_manager = mock_manager_cls.return_value
    mock_manager.list_all.return_value = {
        "SHIP-A": {
            "status": "active",
            "assigned_to": "alpha",
            "daemon_id": "alpha-1",
            "operation": "mine",
        }
    }

    result = ops.assignment_list_operation(ns(player_id=42, include_stale=True))

    assert result == 0
    mock_manager.list_all.assert_called_once_with(include_stale=True)
    output = capsys.readouterr().out
    assert "SHIP ASSIGNMENTS" in output
    assert "SHIP-A" in output
    assert "alpha" in output


@patch("spacetraders_bot.operations.assignments.AssignmentManager")
def regression_assignment_assign_operation_success(mock_manager_cls):
    mock_manager = mock_manager_cls.return_value
    mock_manager.assign.return_value = True

    args = ns(
        player_id=5,
        ship="SHIP-1",
        operator="operator",
        daemon_id="daemon-1",
        operation_type="trade",
        duration=3600,
    )

    assert ops.assignment_assign_operation(args) == 0
    mock_manager.assign.assert_called_once_with(
        "SHIP-1",
        "operator",
        "daemon-1",
        "trade",
        metadata={"duration": 3600},
    )


@patch("spacetraders_bot.operations.assignments.AssignmentManager")
def regression_assignment_assign_operation_failure(mock_manager_cls):
    mock_manager = mock_manager_cls.return_value
    mock_manager.assign.return_value = False

    args = ns(
        player_id=1,
        ship="SHIP-X",
        operator="beta",
        daemon_id="beta-1",
        operation_type="mine",
    )

    assert ops.assignment_assign_operation(args) == 1


@patch("spacetraders_bot.operations.assignments.AssignmentManager")
def regression_assignment_release_operation(mock_manager_cls):
    mock_manager = mock_manager_cls.return_value
    mock_manager.release.return_value = True

    args = ns(player_id=1, ship="SHIP-2", reason="operation_complete")
    assert ops.assignment_release_operation(args) == 0
    mock_manager.release.assert_called_once_with("SHIP-2", reason="operation_complete")


@patch("spacetraders_bot.operations.assignments.AssignmentManager")
def regression_assignment_available_operation_available(mock_manager_cls, capsys):
    mock_manager = mock_manager_cls.return_value
    mock_manager.is_available.return_value = True

    result = ops.assignment_available_operation(ns(player_id=1, ship="SHIP-3"))
    assert result == 0
    cap = capsys.readouterr().out
    assert "SHIP-3" in cap and "available" in cap


@patch("spacetraders_bot.operations.assignments.AssignmentManager")
def regression_assignment_available_operation_unavailable(mock_manager_cls, capsys):
    mock_manager = mock_manager_cls.return_value
    mock_manager.is_available.return_value = False
    mock_manager.get_assignment.return_value = {
        "assigned_to": "gamma",
        "daemon_id": "gamma-1",
        "operation": "mine",
        "assigned_at": "2025-01-01T00:00:00Z",
    }

    result = ops.assignment_available_operation(ns(player_id=1, ship="SHIP-4"))

    assert result == 1
    output = capsys.readouterr().out
    assert "gamma" in output
    assert "mine" in output


@patch("spacetraders_bot.operations.assignments.AssignmentManager")
def regression_assignment_find_operation_with_requirements(mock_manager_cls, capsys):
    mock_manager = mock_manager_cls.return_value
    mock_manager.find_available.return_value = ["SHIP-A", "SHIP-B"]

    args = ns(player_id=1, cargo_min=50, fuel_min=100)
    result = ops.assignment_find_operation(args)

    assert result == 0
    mock_manager.find_available.assert_called_once_with({"cargo_min": 50, "fuel_min": 100})
    out = capsys.readouterr().out
    assert "SHIP-A" in out and "SHIP-B" in out


@patch("spacetraders_bot.operations.assignments.AssignmentManager")
def regression_assignment_find_operation_none_available(mock_manager_cls, capsys):
    mock_manager = mock_manager_cls.return_value
    mock_manager.find_available.return_value = []

    args = ns(player_id=1)
    result = ops.assignment_find_operation(args)

    assert result == 0
    assert "No ships available" in capsys.readouterr().out


@patch("spacetraders_bot.operations.assignments.AssignmentManager")
def regression_assignment_sync_operation(mock_manager_cls, capsys):
    mock_manager = mock_manager_cls.return_value
    mock_manager.sync_with_daemons.return_value = {
        "released": ["SHIP-1"],
        "still_active": ["SHIP-2"],
    }

    result = ops.assignment_sync_operation(ns(player_id=1))

    assert result == 0
    output = capsys.readouterr().out
    assert "SHIP-1" in output and "SHIP-2" in output


@patch("spacetraders_bot.operations.assignments.AssignmentManager")
def regression_assignment_reassign_operation_requires_ships(mock_manager_cls):
    result = ops.assignment_reassign_operation(ns(player_id=1, ships="", from_operation="mine"))
    assert result == 1
    mock_manager_cls.assert_called_once()


@patch("spacetraders_bot.operations.assignments.AssignmentManager")
def regression_assignment_reassign_operation_success(mock_manager_cls, capsys):
    mock_manager = mock_manager_cls.return_value
    mock_manager.reassign_ships.return_value = True

    args = ns(player_id=1, ships="SHIP-1,SHIP-2", from_operation="mine", no_stop=False, timeout=5)
    result = ops.assignment_reassign_operation(args)

    assert result == 0
    mock_manager.reassign_ships.assert_called_once_with(
        ["SHIP-1", "SHIP-2"],
        "mine",
        stop_daemons=True,
        timeout=5,
    )
    assert "Reassigning" in capsys.readouterr().out


@patch("spacetraders_bot.operations.assignments.AssignmentManager")
def regression_assignment_reassign_operation_failure(mock_manager_cls):
    mock_manager = mock_manager_cls.return_value
    mock_manager.reassign_ships.return_value = False

    args = ns(player_id=1, ships="SHIP-3", from_operation="trade", no_stop=True)
    result = ops.assignment_reassign_operation(args)

    assert result == 1
    mock_manager.reassign_ships.assert_called_once()
