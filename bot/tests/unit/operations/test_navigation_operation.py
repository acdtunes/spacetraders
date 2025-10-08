from types import SimpleNamespace
from unittest.mock import MagicMock, patch

import pytest

from spacetraders_bot.operations import navigation as nav_ops


def ns(**kwargs):
    return SimpleNamespace(**kwargs)


@patch("spacetraders_bot.operations.navigation.SmartNavigator")
@patch("spacetraders_bot.operations.navigation.ShipController")
def test_navigate_ship_success(mock_ship_cls, mock_nav_cls):
    args = ns(ship="SHIP-1", destination="X1-TEST-B2")
    api = MagicMock()
    logger = MagicMock()

    ship = mock_ship_cls.return_value
    initial_status = {
        "nav": {
            "waypointSymbol": "X1-TEST-A1",
            "systemSymbol": "X1-TEST",
        }
    }
    final_status = {
        "nav": {"waypointSymbol": "X1-TEST-B2"},
        "fuel": {"current": 90, "capacity": 100},
    }
    ship.get_status.side_effect = [initial_status, final_status]

    navigator = mock_nav_cls.return_value
    navigator.validate_route.return_value = (True, None)
    navigator.execute_route.return_value = True

    success = nav_ops.navigate_ship(args, api, logger)

    assert success is True
    navigator.validate_route.assert_called_once()
    navigator.execute_route.assert_called_once_with(ship, "X1-TEST-B2")
    logger.info.assert_any_call("✅ Navigation complete!")


@patch("spacetraders_bot.operations.navigation.ShipController")
def test_navigate_ship_same_location(mock_ship_cls):
    ship = mock_ship_cls.return_value
    status = {
        "nav": {
            "waypointSymbol": "X1-TEST-A1",
            "systemSymbol": "X1-TEST",
        }
    }
    ship.get_status.return_value = status

    args = ns(ship="SHIP-1", destination="X1-TEST-A1")
    logger = MagicMock()

    assert nav_ops.navigate_ship(args, MagicMock(), logger) is True
    logger.info.assert_any_call("Ship already at destination X1-TEST-A1")


@patch("spacetraders_bot.operations.navigation.ShipController")
def test_navigate_ship_cross_system_fails(mock_ship_cls):
    ship = mock_ship_cls.return_value
    ship.get_status.return_value = {
        "nav": {
            "waypointSymbol": "X1-TEST-A1",
            "systemSymbol": "X1-TEST",
        }
    }

    args = ns(ship="SHIP-1", destination="X2-OTHER-B1")
    logger = MagicMock()

    assert nav_ops.navigate_ship(args, MagicMock(), logger) is False
    logger.error.assert_called_once()


@patch("spacetraders_bot.operations.navigation.SmartNavigator")
@patch("spacetraders_bot.operations.navigation.ShipController")
def test_navigate_ship_validation_failure(mock_ship_cls, mock_nav_cls):
    ship = mock_ship_cls.return_value
    status = {
        "nav": {
            "waypointSymbol": "X1-TEST-A1",
            "systemSymbol": "X1-TEST",
        }
    }
    ship.get_status.return_value = status
    navigator = mock_nav_cls.return_value
    navigator.validate_route.return_value = (False, "No fuel")

    args = ns(ship="SHIP-1", destination="X1-TEST-B2")
    logger = MagicMock()

    assert nav_ops.navigate_ship(args, MagicMock(), logger) is False
    logger.error.assert_called_with("Route validation failed: No fuel")


def test_navigate_operation_success(monkeypatch):
    fake_db = MagicMock()

    class Conn:
        def __enter__(self):
            return MagicMock(get_player_by_id=lambda *_: {"token": "test-token"})

        def __exit__(self, exc_type, exc, tb):
            return False

    fake_db.connection.return_value = Conn()

    monkeypatch.setattr(nav_ops, "setup_logging", lambda *args, **kwargs: "logfile.log")
    monkeypatch.setattr(nav_ops, "get_database", lambda: fake_db)

    navigate_ship_mock = MagicMock(return_value=True)
    monkeypatch.setattr(nav_ops, "navigate_ship", navigate_ship_mock)

    args = ns(player_id=1, ship="SHIP-1", destination="X1-TEST-B2")
    assert nav_ops.navigate_operation(args) is True
    navigate_ship_mock.assert_called_once()


def test_navigate_operation_missing_player(monkeypatch):
    class Conn:
        def __enter__(self):
            return MagicMock(get_player_by_id=lambda *_: None)

        def __exit__(self, exc_type, exc, tb):
            return False

    fake_db = MagicMock()
    fake_db.connection.return_value = Conn()
    fake_db.get_player_by_id.return_value = None

    monkeypatch.setattr(nav_ops, "setup_logging", lambda *args, **kwargs: "logfile.log")
    monkeypatch.setattr(nav_ops, "get_database", lambda: fake_db)

    logger = MagicMock()
    monkeypatch.setattr(nav_ops.logging, "getLogger", lambda *_: logger)
    monkeypatch.setattr(nav_ops, "navigate_ship", MagicMock())

    args = ns(player_id=99, ship="S", destination="X1-TEST-B2")
    result = nav_ops.navigate_operation(args)
    assert result is False
    logger.error.assert_called_once()
