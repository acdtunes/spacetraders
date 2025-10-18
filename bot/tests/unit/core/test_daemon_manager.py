import json
import os
from datetime import datetime, timezone
from pathlib import Path

import pytest

from spacetraders_bot.core import daemon_manager as dm


class DummyDB:
    def __init__(self):
        self.players = {1: {"agent_symbol": "AGENT", "token": "tok"}}
        self.daemons = {}
        self.created = []
        self.updated = []
        self.deleted = []

    # Context managers -------------------------------------------------
    class _Ctx:
        def __init__(self, db):
            self.db = db

        def __enter__(self):
            return object()

        def __exit__(self, exc_type, exc, tb):
            return False

    def connection(self):
        return self._Ctx(self)

    def transaction(self):
        return self._Ctx(self)

    # Player lookup ----------------------------------------------------
    def get_player_by_id(self, _conn, player_id):
        return self.players.get(player_id)

    # Daemon management -------------------------------------------------
    def create_daemon(self, _conn, player_id, daemon_id, pid, command, log_file, err_file):
        record = {
            "daemon_id": daemon_id,
            "player_id": player_id,
            "pid": pid,
            "command": command,
            "log_file": log_file,
            "err_file": err_file,
            "started_at": datetime.now(timezone.utc).isoformat(),
            "status": "running",
        }
        self.daemons[daemon_id] = record
        self.created.append(record)

    def update_daemon_status(self, _conn, player_id, daemon_id, status, stopped_at=None):
        record = self.daemons.setdefault(daemon_id, {"daemon_id": daemon_id, "player_id": player_id})
        record["status"] = status
        record["stopped_at"] = stopped_at
        self.updated.append((daemon_id, status))

    def get_daemon(self, _conn, player_id, daemon_id):
        record = self.daemons.get(daemon_id)
        if not record:
            return None
        return dict(record)

    def list_daemons(self, _conn, player_id):
        return [dict(v) for v in self.daemons.values() if v.get("player_id") == player_id]

    def delete_daemon(self, _conn, player_id, daemon_id):
        self.deleted.append(daemon_id)
        self.daemons.pop(daemon_id, None)


@pytest.fixture
def dummy_db():
    return DummyDB()


@pytest.fixture
def tmp_daemon_env(monkeypatch, tmp_path, dummy_db):
    monkeypatch.setattr(dm, "get_database", lambda path=None: dummy_db)
    monkeypatch.setattr(dm.paths, "DAEMON_DIR", tmp_path)
    monkeypatch.setattr(dm.paths, "ensure_dirs", lambda dirs: [d.mkdir(parents=True, exist_ok=True) for d in dirs])
    return tmp_path


def make_manager(dummy_db, tmp_daemon_env):
    return dm.DaemonManager(player_id=1, daemon_dir=tmp_daemon_env)


def regression_start_records_daemon(monkeypatch, tmp_daemon_env, dummy_db):
    manager = make_manager(dummy_db, tmp_daemon_env)

    popen_calls = {}

    class DummyPopen:
        def __init__(self, command, stdout, stderr, cwd, start_new_session):
            popen_calls["command"] = command
            self.pid = 4321

    monkeypatch.setattr(dm, "subprocess", SimpleNamespace(Popen=DummyPopen))
    monkeypatch.setattr(dm.psutil, "Process", lambda pid: None)
    monkeypatch.setattr(dm.DaemonManager, "is_running", lambda self, daemon_id: False)

    result = manager.start("daemon-1", ["python", "worker.py"], cwd="/tmp")

    assert result is True
    assert "daemon-1" in dummy_db.daemons
    assert popen_calls["command"] == ["python", "worker.py"]

    log_file = Path(dummy_db.daemons["daemon-1"]["log_file"])
    assert log_file.exists()


def regression_start_returns_false_if_running(monkeypatch, tmp_daemon_env, dummy_db):
    manager = make_manager(dummy_db, tmp_daemon_env)
    monkeypatch.setattr(dm.DaemonManager, "is_running", lambda self, daemon_id: True)

    result = manager.start("daemon-1", ["cmd"])
    assert result is False


def regression_stop_updates_database(monkeypatch, tmp_daemon_env, dummy_db):
    manager = make_manager(dummy_db, tmp_daemon_env)
    dummy_db.daemons["daemon-1"] = {
        "daemon_id": "daemon-1",
        "player_id": 1,
        "pid": 999,
        "command": ["python"],
        "started_at": datetime.now(timezone.utc).isoformat(),
        "log_file": str(tmp_daemon_env / "logs" / "daemon.log"),
        "err_file": str(tmp_daemon_env / "logs" / "daemon.err"),
    }

    class FakeProcess:
        def __init__(self, pid):
            assert pid == 999
            self.terminated = False
            self.killed = False

        def terminate(self):
            self.terminated = True

        def wait(self, timeout):
            return None

    monkeypatch.setattr(dm.psutil, "Process", FakeProcess)

    result = manager.stop("daemon-1")
    assert result is True
    assert dummy_db.daemons["daemon-1"]["status"] == "stopped"


def regression_stop_handles_missing_process(monkeypatch, tmp_daemon_env, dummy_db):
    manager = make_manager(dummy_db, tmp_daemon_env)
    dummy_db.daemons["daemon-1"] = {
        "daemon_id": "daemon-1",
        "player_id": 1,
        "pid": 111,
    }

    class RaisingProcess:
        def __init__(self, pid):
            raise dm.psutil.NoSuchProcess(pid)

    monkeypatch.setattr(dm.psutil, "Process", RaisingProcess)

    assert manager.stop("daemon-1") is True
    assert dummy_db.daemons["daemon-1"]["status"] == "crashed"


def regression_stop_handles_process_disappearing(monkeypatch, tmp_daemon_env, dummy_db):
    manager = make_manager(dummy_db, tmp_daemon_env)
    dummy_db.daemons["daemon-1"] = {
        "daemon_id": "daemon-1",
        "player_id": 1,
        "pid": 222,
    }

    class VanishingProcess:
        def __init__(self, pid):
            self.pid = pid

        def terminate(self):
            raise dm.psutil.NoSuchProcess(self.pid)

    monkeypatch.setattr(dm.psutil, "Process", VanishingProcess)

    assert manager.stop("daemon-1") is True
    assert dummy_db.daemons["daemon-1"]["status"] == "crashed"


def regression_stop_handles_generic_exception(monkeypatch, tmp_daemon_env, dummy_db):
    manager = make_manager(dummy_db, tmp_daemon_env)
    dummy_db.daemons["daemon-1"] = {
        "daemon_id": "daemon-1",
        "player_id": 1,
        "pid": 333,
    }

    class BrokenProcess:
        def __init__(self, pid):
            self.pid = pid

        def terminate(self):
            raise RuntimeError("boom")

    monkeypatch.setattr(dm.psutil, "Process", BrokenProcess)

    assert manager.stop("daemon-1") is False


def regression_is_running_updates_crashed(monkeypatch, tmp_daemon_env, dummy_db):
    manager = make_manager(dummy_db, tmp_daemon_env)
    dummy_db.daemons["daemon-1"] = {
        "daemon_id": "daemon-1",
        "player_id": 1,
        "pid": 555,
    }

    class FakeProcess:
        def __init__(self, pid):
            self.pid = pid

        def is_running(self):
            return False

    monkeypatch.setattr(dm.psutil, "Process", FakeProcess)

    assert manager.is_running("daemon-1") is False
    assert dummy_db.daemons["daemon-1"]["status"] == "crashed"


def regression_fetch_process_none(tmp_daemon_env, dummy_db):
    manager = make_manager(dummy_db, tmp_daemon_env)
    assert manager._fetch_process(None) is None


def regression_status_handles_missing_process(monkeypatch, tmp_daemon_env, dummy_db):
    started_at = datetime.now(timezone.utc).isoformat()
    dummy_db.daemons["daemon-1"] = {
        "daemon_id": "daemon-1",
        "player_id": 1,
        "pid": 9999,
        "command": ["cmd"],
        "started_at": started_at,
        "log_file": "log",
        "err_file": "err",
        "status": "running",
    }

    class RaisingProcess:
        def __init__(self, pid):
            raise dm.psutil.NoSuchProcess(pid)

    monkeypatch.setattr(dm.psutil, "Process", RaisingProcess)

    manager = make_manager(dummy_db, tmp_daemon_env)
    status = manager.status("daemon-1")

    assert status["is_running"] is False
    assert status["runtime_seconds"] is None


def regression_status_reports_metrics(monkeypatch, tmp_daemon_env, dummy_db):
    started_at = datetime.now(timezone.utc).isoformat()
    dummy_db.daemons["daemon-1"] = {
        "daemon_id": "daemon-1",
        "player_id": 1,
        "pid": 777,
        "command": ["cmd"],
        "started_at": started_at,
        "log_file": "log",
        "err_file": "err",
        "status": "running",
    }

    class FakeProcess:
        def __init__(self, pid):
            self.pid = pid

        def is_running(self):
            return True

        def cpu_percent(self, interval):
            return 12.5

        class Mem:
            rss = 5 * 1024 * 1024

        def memory_info(self):
            return self.Mem()

    monkeypatch.setattr(dm.psutil, "Process", FakeProcess)

    manager = make_manager(dummy_db, tmp_daemon_env)
    status = manager.status("daemon-1")

    assert status["is_running"] is True
    assert status["cpu_percent"] == pytest.approx(12.5)
    assert status["memory_mb"] == pytest.approx(5.0)


def regression_list_all_sorted(monkeypatch, tmp_daemon_env, dummy_db):
    now = datetime.now(timezone.utc)
    dummy_db.daemons.update(
        {
            "daemon-1": {
                "daemon_id": "daemon-1",
                "player_id": 1,
                "pid": 1,
                "started_at": (now.isoformat()),
                "command": ["a"],
            },
            "daemon-2": {
                "daemon_id": "daemon-2",
                "player_id": 1,
                "pid": 2,
                "started_at": (now.replace(microsecond=0)).isoformat(),
                "command": ["b"],
            },
        }
    )

    def fake_status(self, daemon_id):
        record = dummy_db.daemons[daemon_id]
        return {
            "daemon_id": daemon_id,
            "started_at": record["started_at"],
            "is_running": True,
        }

    monkeypatch.setattr(dm.DaemonManager, "status", fake_status)

    manager = make_manager(dummy_db, tmp_daemon_env)
    order = [entry["daemon_id"] for entry in manager.list_all()]
    assert order == ["daemon-1", "daemon-2"]


def regression_tail_logs_missing_file(tmp_daemon_env, dummy_db, capsys):
    manager = make_manager(dummy_db, tmp_daemon_env)
    dummy_db.daemons["daemon-1"] = {
        "daemon_id": "daemon-1",
        "player_id": 1,
        "log_file": str(tmp_daemon_env / "logs" / "missing.log"),
    }

    manager.tail_logs("daemon-1")
    out = capsys.readouterr().out
    assert "Log file not found" in out


def regression_tail_logs_reads_file(tmp_daemon_env, dummy_db, capsys):
    log_path = tmp_daemon_env / "logs" / "daemon-1.log"
    log_path.parent.mkdir(parents=True, exist_ok=True)
    log_path.write_text("line1\nline2\nline3\n")

    manager = make_manager(dummy_db, tmp_daemon_env)
    dummy_db.daemons["daemon-1"] = {
        "daemon_id": "daemon-1",
        "player_id": 1,
        "log_file": str(log_path),
    }

    manager.tail_logs("daemon-1", lines=2)
    out = capsys.readouterr().out
    assert "line2" in out and "line3" in out


def regression_cleanup_stopped_removes_entries(monkeypatch, tmp_daemon_env, dummy_db):
    dummy_db.daemons.update(
        {
            "daemon-1": {"daemon_id": "daemon-1", "player_id": 1, "pid": 1},
            "daemon-2": {"daemon_id": "daemon-2", "player_id": 1, "pid": 2},
        }
    )

    manager = make_manager(dummy_db, tmp_daemon_env)

    def fake_is_running(self, daemon_id):
        return daemon_id == "daemon-2"

    monkeypatch.setattr(dm.DaemonManager, "is_running", fake_is_running)

    manager.cleanup_stopped()

    assert "daemon-1" not in dummy_db.daemons
    assert "daemon-2" in dummy_db.daemons


# Helper for monkeypatch in start test
class SimpleNamespace:
    def __init__(self, **kwargs):
        self.__dict__.update(kwargs)
