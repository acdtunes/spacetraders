from pathlib import Path

import pytest

from spacetraders_bot.operations.captain_logging import CaptainLogWriter


def _fake_root(tmp_path):
    def builder(agent_symbol: str) -> Path:
        base = tmp_path / agent_symbol.lower()
        (base / "sessions").mkdir(parents=True, exist_ok=True)
        (base / "executive_reports").mkdir(parents=True, exist_ok=True)
        return base

    return builder


@pytest.fixture
def writer(monkeypatch, tmp_path):
    monkeypatch.setattr(
        "spacetraders_bot.operations.captain_logging.captain_logs_root",
        _fake_root(tmp_path),
    )
    return CaptainLogWriter("TEST_AGENT")


def test_initialize_log_creates_file(writer, tmp_path):
    writer.initialize_log()
    log_file = tmp_path / "test_agent" / "captain-log.md"
    assert log_file.exists()
    contents = log_file.read_text()
    assert "CAPTAIN'S LOG" in contents


def test_session_workflow(writer, tmp_path):
    writer.initialize_log()
    session_id = writer.session_start("Explore the sector", operator="Navigator")
    assert session_id

    writer.log_entry(
        "OPERATION_STARTED",
        operation="survey",
        ship="SHIP-1",
    )
    writer.log_entry(
        "CRITICAL_ERROR",
        message="Engine failure detected",
    )
    writer.session_end()

    log_file = tmp_path / "test_agent" / "captain-log.md"
    text = log_file.read_text()
    assert "SESSION_START" in text
    assert "SESSION_END" in text
    assert "CRITICAL_ERROR" in text


def test_log_entry_invalid_type(writer):
    with pytest.raises(ValueError):
        writer.log_entry("UNKNOWN_EVENT")
