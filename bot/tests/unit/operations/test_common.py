import logging
from datetime import timedelta
from pathlib import Path
from types import SimpleNamespace

import pytest

from spacetraders_bot.operations import common


class DummyDB:
    def __init__(self, players):
        self.players = players
        self.calls = {"connection": 0, "transaction": 0}

    def connection(self):
        self.calls["connection"] += 1

        class Ctx:
            def __enter__(self_inner):
                return object()

            def __exit__(self_inner, exc_type, exc, tb):
                return False

        return Ctx()

    def transaction(self):
        self.calls["transaction"] += 1

        class Ctx:
            def __enter__(self_inner):
                return object()

            def __exit__(self_inner, exc_type, exc, tb):
                return False

        return Ctx()

    def get_player_by_id(self, _conn, player_id):
        return self.players.get(player_id)


@pytest.fixture(autouse=True)
def clear_logger_handlers():
    root = logging.getLogger()
    previous_handlers = root.handlers[:]
    previous_level = root.level
    yield
    root.handlers = previous_handlers
    root.setLevel(previous_level)


def test_get_api_client_returns_client(monkeypatch):
    dummy_db = DummyDB({1: {"token": "test-token"}})
    monkeypatch.setattr(common, "get_database", lambda path: dummy_db)
    monkeypatch.setattr(common, "sqlite_path", lambda: Path("dummy.db"))

    captured_token = {}

    class DummyClient:
        def __init__(self, token):
            captured_token["value"] = token

    monkeypatch.setattr(common, "APIClient", DummyClient)

    client = common.get_api_client(1)
    assert isinstance(client, DummyClient)
    assert captured_token["value"] == "test-token"


def test_get_api_client_missing_player(monkeypatch):
    dummy_db = DummyDB({})
    monkeypatch.setattr(common, "get_database", lambda path: dummy_db)
    monkeypatch.setattr(common, "sqlite_path", lambda: Path("dummy.db"))

    with pytest.raises(ValueError):
        common.get_api_client(99)


def test_setup_logging_creates_file(monkeypatch, tmp_path):
    monkeypatch.setattr(common, "LOGS_DIR", tmp_path)
    monkeypatch.setattr(common, "timestamp_iso", lambda: "2025-10-08T00:00:00Z")

    log_path = common.setup_logging("testop", "SHIP-1", log_level="INFO")

    assert log_path.parent == tmp_path
    assert log_path.exists()

    root = logging.getLogger()
    assert len(root.handlers) == 2

    content = log_path.read_text()
    assert "SpaceTraders Bot" in content


def test_get_captain_logger_returns_cached_instance(monkeypatch):
    dummy_db = DummyDB({1: {"agent_symbol": "AGENT", "token": "tok"}})
    monkeypatch.setattr(common, "get_database", lambda path: dummy_db)
    monkeypatch.setattr(common, "sqlite_path", lambda: Path("dummy.db"))

    common._cached_captain_logger.cache_clear()
    instances = []

    class DummyWriter:
        def __init__(self, agent_symbol, token):
            instances.append((agent_symbol, token))

    monkeypatch.setattr(common, "CaptainLogWriter", DummyWriter)

    writer_a = common.get_captain_logger(1)
    writer_b = common.get_captain_logger(1)

    assert writer_a is writer_b
    assert instances == [("AGENT", "tok")]
    common._cached_captain_logger.cache_clear()


def test_get_captain_logger_missing_player(monkeypatch, caplog):
    dummy_db = DummyDB({})
    monkeypatch.setattr(common, "get_database", lambda path: dummy_db)
    monkeypatch.setattr(common, "sqlite_path", lambda: Path("dummy.db"))

    with caplog.at_level(logging.WARNING):
        writer = common.get_captain_logger(7)
    assert writer is None
    assert "unknown player_id" in caplog.text


def test_log_captain_event_invokes_writer():
    events = []

    class DummyWriter:
        def log_entry(self, entry_type, **kwargs):
            events.append((entry_type, kwargs))

    writer = DummyWriter()
    common.log_captain_event(writer, "EVENT", data=123)
    assert events == [("EVENT", {"data": 123})]


def test_log_captain_event_handles_none():
    # Should simply not raise
    common.log_captain_event(None, "EVENT", data=123)


@pytest.mark.parametrize(
    "delta,expected",
    [
        (timedelta(seconds=45), "45s"),
        (timedelta(minutes=2, seconds=5), "2m 5s"),
        (timedelta(hours=3, minutes=15), "3h 15m"),
    ],
)
def test_humanize_duration(delta, expected):
    assert common.humanize_duration(delta) == expected


def test_get_operator_name():
    assert common.get_operator_name(SimpleNamespace(operator="Custom")) == "Custom"
    assert common.get_operator_name(SimpleNamespace()) == common.CAPTAIN_DEFAULT_OPERATOR
