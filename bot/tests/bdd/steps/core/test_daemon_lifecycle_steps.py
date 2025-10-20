import os
import signal
import subprocess
import time
from pathlib import Path
from unittest.mock import Mock, patch

import psutil
from pytest_bdd import scenarios, given, when, then, parsers

from spacetraders_bot.core.daemon_manager import DaemonManager
from spacetraders_bot.core.database import Database

scenarios('../../../bdd/features/core/daemon_lifecycle.feature')


class MockProcess:
    """Mock process for testing."""
    def __init__(self, pid, is_running=True):
        self.pid = pid
        self._is_running = is_running

    def is_running(self):
        return self._is_running

    def kill(self):
        self._is_running = False

    def terminate(self):
        self._is_running = False


@given('a daemon manager for player 1', target_fixture='daemon_ctx')
def given_daemon_manager(tmp_path):
    """Create daemon manager with temp directories."""
    daemon_dir = tmp_path / "daemons"
    db_path = tmp_path / "test.db"

    # Initialize database
    db = Database(str(db_path))
    with db.transaction() as conn:
        db.create_player(
            conn,
            agent_symbol="TEST-AGENT",
            faction="COSMIC",
            headquarters="X1-TEST-HQ",
            token="fake-token",
            starting_credits=100000
        )

    manager = DaemonManager(player_id=1, daemon_dir=daemon_dir, db_path=db_path)

    context = {
        'manager': manager,
        'daemon_dir': daemon_dir,
        'db': db,
        'player_id': 1,
        'processes': {}  # Track mock processes
    }
    return context


@given('the daemon database is initialized')
def given_db_initialized(daemon_ctx):
    """Database already initialized in fixture."""
    return daemon_ctx


@when(parsers.parse('I start a daemon "{daemon_id}" with command "{command}"'))
def when_start_daemon(daemon_ctx, daemon_id, command, monkeypatch):
    """Start a daemon."""
    # Mock subprocess.Popen to avoid actually starting processes
    mock_process = MockProcess(pid=12345 + len(daemon_ctx['processes']))

    original_popen = subprocess.Popen

    def mock_popen(*args, **kwargs):
        """Mock Popen that returns mock process."""
        daemon_ctx['processes'][daemon_id] = mock_process
        # Create a mock Popen object
        mock_popen_obj = Mock()
        mock_popen_obj.pid = mock_process.pid
        mock_popen_obj.poll = Mock(return_value=None)  # Process still running
        return mock_popen_obj

    monkeypatch.setattr(subprocess, 'Popen', mock_popen)

    # Also mock psutil.Process
    def mock_psutil_process(pid):
        """Return mock process for pid."""
        for daemon_id, proc in daemon_ctx['processes'].items():
            if proc.pid == pid:
                return proc
        raise psutil.NoSuchProcess(pid)

    monkeypatch.setattr(psutil, 'Process', mock_psutil_process)

    # Start the daemon
    daemon_ctx['manager'].start(daemon_id, command.split())
    daemon_ctx['last_daemon_id'] = daemon_id
    return daemon_ctx


@then('the daemon should be registered in the database')
def then_daemon_registered(daemon_ctx):
    """Verify daemon in database."""
    with daemon_ctx['db'].connection() as conn:
        daemon = daemon_ctx['db'].get_daemon(
            conn,
            daemon_ctx['player_id'],
            daemon_ctx['last_daemon_id']
        )
    assert daemon is not None
    assert daemon['daemon_id'] == daemon_ctx['last_daemon_id']


@then('the daemon status should be "running"')
def then_status_running(daemon_ctx):
    """Verify daemon status is running."""
    with daemon_ctx['db'].connection() as conn:
        daemon = daemon_ctx['db'].get_daemon(
            conn,
            daemon_ctx['player_id'],
            daemon_ctx['last_daemon_id']
        )
    assert daemon['status'] == 'running'


@then('the daemon PID should be valid')
def then_pid_valid(daemon_ctx):
    """Verify PID is set."""
    with daemon_ctx['db'].connection() as conn:
        daemon = daemon_ctx['db'].get_daemon(
            conn,
            daemon_ctx['player_id'],
            daemon_ctx['last_daemon_id']
        )
    assert daemon['pid'] is not None
    assert daemon['pid'] > 0


@then('a log file should be created for the daemon')
def then_log_file_created(daemon_ctx):
    """Verify log file exists."""
    log_file = daemon_ctx['daemon_dir'] / "logs" / f"{daemon_ctx['last_daemon_id']}.log"
    assert log_file.exists()


@given(parsers.parse('a daemon "{daemon_id}" is running'))
def given_daemon_running(daemon_ctx, daemon_id, monkeypatch):
    """Setup a running daemon."""
    # Mock subprocess and psutil first
    mock_process = MockProcess(pid=99999)
    daemon_ctx['processes'][daemon_id] = mock_process

    def mock_popen(*args, **kwargs):
        mock_popen_obj = Mock()
        mock_popen_obj.pid = mock_process.pid
        mock_popen_obj.poll = Mock(return_value=None)
        return mock_popen_obj

    def mock_psutil_process(pid):
        for did, proc in daemon_ctx['processes'].items():
            if proc.pid == pid:
                return proc
        raise psutil.NoSuchProcess(pid)

    monkeypatch.setattr(subprocess, 'Popen', mock_popen)
    monkeypatch.setattr(psutil, 'Process', mock_psutil_process)

    # Start the daemon
    daemon_ctx['manager'].start(daemon_id, ["python3", "test.py"])
    daemon_ctx['last_daemon_id'] = daemon_id
    return daemon_ctx


@when(parsers.parse('I stop the daemon "{daemon_id}"'))
def when_stop_daemon(daemon_ctx, daemon_id):
    """Stop a daemon."""
    daemon_ctx['manager'].stop(daemon_id)
    return daemon_ctx


@then('the daemon process should be terminated')
def then_process_terminated(daemon_ctx):
    """Verify process was terminated."""
    process = daemon_ctx['processes'].get(daemon_ctx['last_daemon_id'])
    if process:
        assert not process.is_running()


@then('the daemon status should be "stopped"')
def then_status_stopped(daemon_ctx):
    """Verify status is stopped."""
    with daemon_ctx['db'].connection() as conn:
        daemon = daemon_ctx['db'].get_daemon(
            conn,
            daemon_ctx['player_id'],
            daemon_ctx['last_daemon_id']
        )
    assert daemon['status'] == 'stopped'


@then('the stopped_at timestamp should be set')
def then_stopped_at_set(daemon_ctx):
    """Verify stopped_at timestamp."""
    with daemon_ctx['db'].connection() as conn:
        daemon = daemon_ctx['db'].get_daemon(
            conn,
            daemon_ctx['player_id'],
            daemon_ctx['last_daemon_id']
        )
    assert daemon['stopped_at'] is not None


@when(parsers.parse('I check the status of "{daemon_id}"'))
def when_check_status(daemon_ctx, daemon_id):
    """Check daemon status."""
    status = daemon_ctx['manager'].status(daemon_id)
    daemon_ctx['status_result'] = status
    return daemon_ctx


@then('the status should show "running"')
def then_status_shows_running(daemon_ctx):
    """Verify status shows running."""
    assert daemon_ctx['status_result']['status'] == 'running'


@then('the status should include PID')
def then_status_has_pid(daemon_ctx):
    """Verify PID in status."""
    assert 'pid' in daemon_ctx['status_result']
    assert daemon_ctx['status_result']['pid'] is not None


@then('the status should include start time')
def then_status_has_start_time(daemon_ctx):
    """Verify start time in status."""
    assert 'started_at' in daemon_ctx['status_result']
    assert daemon_ctx['status_result']['started_at'] is not None


@given(parsers.parse('a daemon "{daemon_id}" was running but is now stopped'))
def given_daemon_stopped(daemon_ctx, daemon_id, monkeypatch):
    """Setup a stopped daemon."""
    # Create and start, then stop
    given_daemon_running(daemon_ctx, daemon_id, monkeypatch)
    daemon_ctx['manager'].stop(daemon_id)
    return daemon_ctx


@then('the status should show "stopped"')
def then_status_shows_stopped(daemon_ctx):
    """Verify status shows stopped."""
    assert daemon_ctx['status_result']['status'] == 'stopped'


@then('the status should include stopped_at timestamp')
def then_status_has_stopped_at(daemon_ctx):
    """Verify stopped_at in status."""
    assert 'stopped_at' in daemon_ctx['status_result']
    assert daemon_ctx['status_result']['stopped_at'] is not None


@then('the PID should not be in the process list')
def then_pid_not_in_process_list(daemon_ctx):
    """Verify process is not running."""
    pid = daemon_ctx['status_result'].get('pid')
    if pid:
        process = daemon_ctx['processes'].get(daemon_ctx['last_daemon_id'])
        if process:
            assert not process.is_running()


@given(parsers.parse('a daemon "{daemon_id}" is registered as running'))
def given_daemon_registered_running(daemon_ctx, daemon_id, monkeypatch):
    """Setup daemon registered but process will be dead."""
    given_daemon_running(daemon_ctx, daemon_id, monkeypatch)
    return daemon_ctx


@given('the process is no longer alive')
def given_process_dead(daemon_ctx):
    """Mark process as dead."""
    process = daemon_ctx['processes'].get(daemon_ctx['last_daemon_id'])
    if process:
        process._is_running = False
    return daemon_ctx


@then('the daemon should be detected as stale')
def then_detected_as_stale(daemon_ctx):
    """Verify daemon detected as stale."""
    # Status check should detect stale process
    assert daemon_ctx['status_result']['status'] == 'stopped'


@then('the status should automatically update to "stopped"')
def then_auto_update_stopped(daemon_ctx):
    """Verify auto-update to stopped."""
    # Re-check database
    with daemon_ctx['db'].connection() as conn:
        daemon = daemon_ctx['db'].get_daemon(
            conn,
            daemon_ctx['player_id'],
            daemon_ctx['last_daemon_id']
        )
    assert daemon['status'] == 'stopped'


@given('multiple daemons exist with some processes dead')
def given_multiple_daemons_mixed(daemon_ctx, monkeypatch):
    """Create multiple daemons with mixed states."""
    # Setup mocks
    def mock_popen(*args, **kwargs):
        mock_popen_obj = Mock()
        mock_popen_obj.pid = 10000 + len(daemon_ctx['processes'])
        mock_popen_obj.poll = Mock(return_value=None)
        return mock_popen_obj

    def mock_psutil_process(pid):
        for did, proc in daemon_ctx['processes'].items():
            if proc.pid == pid:
                return proc
        raise psutil.NoSuchProcess(pid)

    monkeypatch.setattr(subprocess, 'Popen', mock_popen)
    monkeypatch.setattr(psutil, 'Process', mock_psutil_process)

    # Create 3 daemons
    for i in range(3):
        daemon_id = f"daemon-{i}"
        mock_process = MockProcess(pid=10000 + i, is_running=(i != 1))  # daemon-1 is dead
        daemon_ctx['processes'][daemon_id] = mock_process

        # Manually create in DB
        with daemon_ctx['db'].transaction() as conn:
            daemon_ctx['db'].create_daemon(
                conn,
                player_id=daemon_ctx['player_id'],
                daemon_id=daemon_id,
                pid=10000 + i,
                command=["python3", "test.py"],
                operation_type="test"
            )

    return daemon_ctx


@when('I run cleanup stale daemons')
def when_cleanup_stale(daemon_ctx):
    """Run cleanup."""
    daemon_ctx['manager'].cleanup()
    return daemon_ctx


@then('all dead daemon statuses should be updated to "stopped"')
def then_dead_updated(daemon_ctx):
    """Verify dead daemons marked stopped."""
    with daemon_ctx['db'].connection() as conn:
        # daemon-1 should be stopped
        daemon = daemon_ctx['db'].get_daemon(conn, daemon_ctx['player_id'], "daemon-1")
        assert daemon['status'] == 'stopped'


@then('only alive daemons should remain as "running"')
def then_alive_still_running(daemon_ctx):
    """Verify alive daemons still running."""
    with daemon_ctx['db'].connection() as conn:
        # daemon-0 and daemon-2 should be running
        daemon0 = daemon_ctx['db'].get_daemon(conn, daemon_ctx['player_id'], "daemon-0")
        daemon2 = daemon_ctx['db'].get_daemon(conn, daemon_ctx['player_id'], "daemon-2")
        assert daemon0['status'] == 'running'
        assert daemon2['status'] == 'running'


@given('multiple daemons are running for player 1')
def given_multiple_daemons_player1(daemon_ctx, monkeypatch):
    """Create multiple daemons for player 1."""
    given_multiple_daemons_mixed(daemon_ctx, monkeypatch)
    return daemon_ctx


@given('other players have their own daemons')
def given_other_player_daemons(daemon_ctx):
    """Create daemons for other players."""
    # Create player 2
    with daemon_ctx['db'].transaction() as conn:
        daemon_ctx['db'].create_player(
            conn,
            agent_symbol="OTHER-AGENT",
            faction="VOID",
            headquarters="X1-OTHER-HQ",
            token="other-token",
            starting_credits=50000
        )

        # Create daemon for player 2
        daemon_ctx['db'].create_daemon(
            conn,
            player_id=2,
            daemon_id="other-daemon",
            pid=20000,
            command=["python3", "other.py"],
            operation_type="test"
        )

    return daemon_ctx


@when('I list all daemons')
def when_list_daemons(daemon_ctx):
    """List daemons."""
    daemons = daemon_ctx['manager'].list()
    daemon_ctx['daemon_list'] = daemons
    return daemon_ctx


@then("I should see only player 1's daemons")
def then_only_player1_daemons(daemon_ctx):
    """Verify only player 1 daemons."""
    daemons = daemon_ctx['daemon_list']
    # Should not include other-daemon from player 2
    daemon_ids = [d['daemon_id'] for d in daemons]
    assert 'other-daemon' not in daemon_ids


@then('each daemon should have ID, status, and PID')
def then_daemons_have_fields(daemon_ctx):
    """Verify daemon fields."""
    for daemon in daemon_ctx['daemon_list']:
        assert 'daemon_id' in daemon
        assert 'status' in daemon
        assert 'pid' in daemon


@given('the daemon has written log output')
def given_daemon_has_logs(daemon_ctx):
    """Create log output."""
    log_file = daemon_ctx['daemon_dir'] / "logs" / f"{daemon_ctx['last_daemon_id']}.log"
    with open(log_file, 'w') as f:
        for i in range(20):
            f.write(f"Log line {i}\n")
    return daemon_ctx


@when(parsers.parse('I tail the logs for "{daemon_id}" with {lines:d} lines'))
def when_tail_logs(daemon_ctx, daemon_id, lines):
    """Tail daemon logs."""
    log_output = daemon_ctx['manager'].logs(daemon_id, lines=lines)
    daemon_ctx['log_output'] = log_output
    return daemon_ctx


@then('I should see the last 10 lines of output')
def then_see_last_lines(daemon_ctx):
    """Verify tail output."""
    output = daemon_ctx['log_output']
    lines = [line for line in output.split('\n') if line.strip()]
    assert len(lines) <= 10
    # Should contain recent log lines
    assert 'Log line' in output


@then('a PID file should be created')
def then_pid_file_created(daemon_ctx):
    """Verify PID file."""
    pid_file = daemon_ctx['daemon_dir'] / "pids" / f"{daemon_ctx['last_daemon_id']}.json"
    assert pid_file.exists()


@then('the PID file should remain for audit purposes')
def then_pid_file_remains(daemon_ctx):
    """Verify PID file persists."""
    pid_file = daemon_ctx['daemon_dir'] / "pids" / f"{daemon_ctx['last_daemon_id']}.json"
    assert pid_file.exists()


@then('the database record should show "stopped"')
def then_db_shows_stopped(daemon_ctx):
    """Verify database status."""
    with daemon_ctx['db'].connection() as conn:
        daemon = daemon_ctx['db'].get_daemon(
            conn,
            daemon_ctx['player_id'],
            daemon_ctx['last_daemon_id']
        )
    assert daemon['status'] == 'stopped'
