from pathlib import Path

from spacetraders_bot.helpers import paths


def test_captain_logs_root_creates_directories(tmp_path, monkeypatch):
    monkeypatch.setattr(paths, 'LOGS_DIR', tmp_path / 'logs')

    created = []

    def fake_ensure_dirs(directories):
        created.extend(directories)

    monkeypatch.setattr(paths, 'ensure_dirs', fake_ensure_dirs)

    root = paths.captain_logs_root('CMD-TEST')

    expected = tmp_path / 'logs' / 'captain' / 'cmd-test'
    assert root == expected
    assert expected in created
    assert expected / 'sessions' in created
    assert expected / 'executive_reports' in created
