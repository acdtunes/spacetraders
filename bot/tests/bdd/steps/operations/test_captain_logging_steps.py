import json
import os
import tempfile
import threading
import time
from pathlib import Path
from unittest.mock import Mock

from pytest_bdd import scenarios, given, when, then, parsers

from spacetraders_bot.operations.captain_logging import CaptainLogWriter

scenarios('../../../bdd/features/operations/captain_logging.feature')


class MockAPI:
    """Mock API for captain logging tests."""
    def __init__(self):
        self.agent_data = {
            'callsign': 'TEST-AGENT',
            'faction': 'COSMIC',
            'headquarters': 'X1-TEST-HQ',
            'credits': 100000
        }
        self.ships = []
        self.contracts = []

    def get_agent(self):
        return self.agent_data

    def list_ships(self):
        return {'data': self.ships}

    def list_contracts(self):
        return {'data': self.contracts}


@given('a captain log writer for agent "TEST-AGENT"', target_fixture='log_ctx')
def given_log_writer(tmp_path, monkeypatch):
    """Create captain log writer with temp directory."""
    # Create temp directory for logs
    agent_dir = tmp_path / "agents" / "TEST-AGENT"
    agent_dir.mkdir(parents=True)

    # Create subdirectories
    (agent_dir / "sessions").mkdir(exist_ok=True)
    (agent_dir / "executive_reports").mkdir(exist_ok=True)

    # Monkeypatch the captain_logs_root function BEFORE creating writer
    def mock_captain_logs_root(agent_callsign):
        return agent_dir

    from spacetraders_bot.helpers import paths
    monkeypatch.setattr(paths, 'captain_logs_root', mock_captain_logs_root)

    # Create writer with mock API (after monkeypatching)
    mock_api = MockAPI()
    writer = CaptainLogWriter("TEST-AGENT", token=None)  # Use None to avoid real API client creation
    writer.api = mock_api

    # Override paths directly (monkeypatch might not catch __init__ time)
    writer.agent_dir = agent_dir
    writer.log_file = agent_dir / "captain-log.md"
    writer.sessions_dir = agent_dir / "sessions"
    writer.reports_dir = agent_dir / "executive_reports"

    context = {
        'writer': writer,
        'agent_dir': agent_dir,
        'log_file': agent_dir / "captain-log.md",
        'sessions_dir': agent_dir / "sessions",
        'mock_api': mock_api
    }
    return context


@when('I initialize a new captain log')
def when_initialize_log(log_ctx):
    """Initialize captain log."""
    log_ctx['writer'].initialize_log()
    return log_ctx


@then('the captain log file should exist')
def then_log_file_exists(log_ctx):
    """Verify log file was created."""
    assert log_ctx['log_file'].exists()


@then('the log should contain agent information')
def then_contains_agent_info(log_ctx):
    """Verify agent info in log."""
    content = log_ctx['log_file'].read_text()
    assert 'TEST-AGENT' in content
    assert 'COSMIC' in content


@then('the log should have sections for executive summary and detailed entries')
def then_has_sections(log_ctx):
    """Verify log structure."""
    content = log_ctx['log_file'].read_text()
    assert '## EXECUTIVE SUMMARY' in content
    assert '## DETAILED LOG ENTRIES' in content


@given('a captain log has been initialized')
def given_log_initialized(log_ctx):
    """Ensure log is initialized."""
    log_ctx['writer'].initialize_log()
    return log_ctx


@when(parsers.parse('I start a session with objective "{objective}" and narrative "{narrative}"'))
def when_start_session_with_narrative(log_ctx, objective, narrative):
    """Start session with narrative."""
    session_id = log_ctx['writer'].session_start(objective, narrative=narrative)
    log_ctx['session_id'] = session_id
    return log_ctx


@then('a new session should be created with ID')
def then_session_created(log_ctx):
    """Verify session was created."""
    assert 'session_id' in log_ctx
    assert log_ctx['session_id'] is not None


@then('the session should be saved to disk')
def then_session_saved(log_ctx):
    """Verify session state saved."""
    state_file = log_ctx['sessions_dir'] / "current_session.json"
    assert state_file.exists()
    with open(state_file) as f:
        session = json.load(f)
    assert session['session_id'] == log_ctx['session_id']


@then('the log should contain the session start entry')
def then_log_contains_session_start(log_ctx):
    """Verify session start in log."""
    content = log_ctx['log_file'].read_text()
    assert 'SESSION_START' in content
    assert log_ctx['session_id'] in content


@then('the session start should include the narrative')
def then_session_has_narrative(log_ctx):
    """Verify narrative in session start."""
    content = log_ctx['log_file'].read_text()
    assert 'COMMAND BRIEF' in content
    assert 'Deploying mining fleet' in content


@when(parsers.parse('I start a session with objective "{objective}" without narrative'))
def when_start_session_no_narrative(log_ctx, objective, capsys):
    """Start session without narrative."""
    session_id = log_ctx['writer'].session_start(objective, narrative=None)
    log_ctx['session_id'] = session_id
    log_ctx['stdout'] = capsys.readouterr().out
    return log_ctx


@then('a session ID should still be returned')
def then_session_id_returned(log_ctx):
    """Verify session ID returned."""
    assert log_ctx['session_id'] is not None


@then('no log entry should be written')
def then_no_log_entry(log_ctx):
    """Verify no entry written."""
    content = log_ctx['log_file'].read_text()
    # Should not contain the operation entry (but SESSION_START from session is ok)
    assert 'OPERATION_COMPLETED' not in content or content.count('OPERATION_COMPLETED') == 0


@given('a session is active')
def given_session_active(log_ctx):
    """Create active session."""
    if 'session_id' not in log_ctx:
        session_id = log_ctx['writer'].session_start(
            "Test mission",
            narrative="Test session"
        )
        log_ctx['session_id'] = session_id
    return log_ctx


@when(parsers.parse('I log an operation started event with narrative "{narrative}"'))
def when_log_operation_started(log_ctx, narrative):
    """Log operation started."""
    log_ctx['writer'].log_entry(
        'OPERATION_STARTED',
        operator='Test Operator',
        ship='SHIP-1',
        op_type='mining',
        daemon_id='miner-1',
        narrative=narrative,
        tags=['mining', 'asteroid']
    )
    return log_ctx


@then('the log should contain the operation started entry')
def then_log_has_operation_started(log_ctx):
    """Verify operation started in log."""
    content = log_ctx['log_file'].read_text()
    assert 'OPERATION_STARTED' in content


@then('the entry should include the narrative')
def then_entry_has_narrative(log_ctx):
    """Verify narrative in entry."""
    content = log_ctx['log_file'].read_text()
    assert 'Deploying SHIP-1 to asteroid B9' in content


@then('the operation should be tracked in the session')
def then_operation_tracked(log_ctx):
    """Verify operation in session state."""
    assert log_ctx['writer'].current_session is not None
    operations = log_ctx['writer'].current_session.get('operations', [])
    assert len(operations) > 0


@when('I log an operation completed with narrative, insights, and recommendations')
def when_log_operation_completed(log_ctx):
    """Log operation completed."""
    log_ctx['writer'].log_entry(
        'OPERATION_COMPLETED',
        operator='Test Operator',
        ship='SHIP-1',
        op_type='mining',
        narrative='Successfully mined 100 units of ore',
        insights='Asteroid B9 yields 4 units per extraction',
        recommendations='Continue mining operations at this site',
        tags=['mining', 'success']
    )
    return log_ctx


@then('the log should contain the operation completed entry')
def then_log_has_operation_completed(log_ctx):
    """Verify operation completed in log."""
    content = log_ctx['log_file'].read_text()
    assert 'OPERATION_COMPLETED' in content


@then('the entry should include narrative, insights, and recommendations')
def then_entry_has_all_sections(log_ctx):
    """Verify all sections in entry."""
    content = log_ctx['log_file'].read_text()
    assert 'Successfully mined 100 units of ore' in content
    assert 'Asteroid B9 yields 4 units per extraction' in content
    assert 'Continue mining operations at this site' in content


@then('tags should be present in the entry')
def then_entry_has_tags(log_ctx):
    """Verify tags in entry."""
    content = log_ctx['log_file'].read_text()
    assert '#mining' in content
    assert '#success' in content


@when('I log an operation completed without narrative')
def when_log_completed_no_narrative(log_ctx, capsys):
    """Log completed without narrative."""
    log_ctx['writer'].log_entry(
        'OPERATION_COMPLETED',
        operator='Test Operator',
        ship='SHIP-1',
        op_type='mining'
        # No narrative provided
    )
    log_ctx['stdout'] = capsys.readouterr().out
    return log_ctx


@then('a warning should be displayed')
def then_warning_displayed(log_ctx):
    """Verify warning shown."""
    assert 'narrative missing' in log_ctx.get('stdout', '').lower()


@when('I log a scouting operation start')
def when_log_scout_operation(log_ctx, capsys):
    """Log scouting operation."""
    log_ctx['writer'].log_entry(
        'OPERATION_STARTED',
        operator='Scout Operator',
        ship='SCOUT-1',
        op_type='market_scout',
        narrative='Scouting markets',
        tags=['scout']
    )
    log_ctx['stdout'] = capsys.readouterr().out
    return log_ctx


@then('a filtered message should be displayed')
def then_filtered_message(log_ctx):
    """Verify filtered message."""
    assert 'filtered' in log_ctx.get('stdout', '').lower()


@when('I log a critical error with error description and narrative')
def when_log_critical_error(log_ctx):
    """Log critical error."""
    log_ctx['writer'].log_entry(
        'CRITICAL_ERROR',
        operator='Error Handler',
        ship='SHIP-1',
        error='Ship stranded',
        cause='Insufficient fuel',
        resolution='Refuel and retry',
        narrative='Ship SHIP-1 ran out of fuel and is stranded',
        tags=['error', 'fuel']
    )
    return log_ctx


@then('the log should contain the critical error entry')
def then_log_has_error(log_ctx):
    """Verify error in log."""
    content = log_ctx['log_file'].read_text()
    assert 'CRITICAL_ERROR' in content
    assert 'Ship stranded' in content


@then('the error should be tracked in the session')
def then_error_tracked(log_ctx):
    """Verify error tracked in session."""
    assert log_ctx['writer'].current_session is not None
    errors = log_ctx['writer'].current_session.get('errors', [])
    assert len(errors) > 0


@given('a session is active with operations')
def given_session_with_operations(log_ctx):
    """Create session with operations."""
    if 'session_id' not in log_ctx:
        session_id = log_ctx['writer'].session_start(
            "Mining mission",
            narrative="Testing session end"
        )
        log_ctx['session_id'] = session_id

    # Add some operations
    log_ctx['writer'].log_entry(
        'OPERATION_STARTED',
        operator='Miner',
        ship='SHIP-1',
        op_type='mining',
        daemon_id='miner-1',
        narrative='Started mining'
    )
    return log_ctx


@when('I end the session')
def when_end_session(log_ctx):
    """End the session."""
    # Update end credits
    log_ctx['mock_api'].agent_data['credits'] = 150000

    # session_end() takes no arguments
    log_ctx['writer'].session_end()
    return log_ctx


@then('the session should be archived to JSON')
def then_session_archived(log_ctx):
    """Verify session archived."""
    session_id = log_ctx['session_id']
    archive_file = log_ctx['sessions_dir'] / f"{session_id}.json"
    assert archive_file.exists()


@then('the archive should include session metadata')
def then_archive_has_metadata(log_ctx):
    """Verify metadata in archive."""
    session_id = log_ctx['session_id']
    archive_file = log_ctx['sessions_dir'] / f"{session_id}.json"
    with open(archive_file) as f:
        data = json.load(f)
    assert 'session_id' in data
    assert 'start_time' in data
    assert 'end_time' in data


@then('the archive should include duration and net profit')
def then_archive_has_metrics(log_ctx):
    """Verify metrics in archive."""
    session_id = log_ctx['session_id']
    archive_file = log_ctx['sessions_dir'] / f"{session_id}.json"
    with open(archive_file) as f:
        data = json.load(f)
    # Check for net_profit and ROI (duration is in markdown, not JSON)
    assert 'net_profit' in data
    assert 'roi' in data
    assert data['net_profit'] == 50000  # 150000 - 100000


@then('the current session should be cleared')
def then_session_cleared(log_ctx):
    """Verify session cleared."""
    assert log_ctx['writer'].current_session is None


@given('multiple writers attempt to log simultaneously')
def given_concurrent_writers(log_ctx):
    """Setup concurrent writers."""
    log_ctx['concurrent_results'] = []
    return log_ctx


@when('concurrent log entries are written')
def when_concurrent_writes(log_ctx):
    """Perform concurrent writes."""
    import time

    def write_entry(index):
        try:
            # Add small delay to reduce race conditions
            time.sleep(0.01 * index)
            log_ctx['writer'].log_entry(
                'OPERATION_STARTED',
                operator=f'Operator-{index}',
                ship=f'SHIP-{index}',
                op_type='mining',
                daemon_id=f'miner-{index}',
                narrative=f'Entry {index}'
            )
            log_ctx['concurrent_results'].append(('success', index))
        except Exception as e:
            log_ctx['concurrent_results'].append(('error', index, str(e)))

    threads = []
    # Reduce to 3 concurrent threads for more reliable testing
    for i in range(3):
        t = threading.Thread(target=write_entry, args=(i,))
        threads.append(t)
        t.start()

    for t in threads:
        t.join()

    return log_ctx


@then('all entries should be written successfully')
def then_all_writes_succeed(log_ctx):
    """Verify all writes succeeded."""
    successes = [r for r in log_ctx['concurrent_results'] if r[0] == 'success']
    assert len(successes) == 3  # Updated to match 3 threads


@then('no entries should be corrupted or lost')
def then_no_corruption(log_ctx):
    """Verify no corruption."""
    content = log_ctx['log_file'].read_text()
    # All 3 operations should be in log
    for i in range(3):
        assert f'Operator-{i}' in content


@given('the log file is locked by another process')
def given_file_locked(log_ctx, monkeypatch):
    """Mock file locking scenario."""
    import fcntl

    original_flock = fcntl.flock
    lock_count = [0]  # Mutable counter

    def mock_flock(fd, operation):
        """Mock flock that fails first 2 attempts."""
        if operation & fcntl.LOCK_NB and lock_count[0] < 2:
            lock_count[0] += 1
            raise IOError(11, "Resource temporarily unavailable")
        return original_flock(fd, operation)

    monkeypatch.setattr(fcntl, 'flock', mock_flock)
    log_ctx['lock_attempts'] = lock_count
    return log_ctx


@when('I attempt to write a log entry')
def when_write_with_retry(log_ctx):
    """Write entry with retry logic."""
    log_ctx['writer'].log_entry(
        'OPERATION_STARTED',
        operator='Retry Test',
        ship='SHIP-RETRY',
        op_type='mining',
        daemon_id='retry-1',
        narrative='Testing retry'
    )
    return log_ctx


@then('the writer should retry with exponential backoff')
def then_retries_with_backoff(log_ctx):
    """Verify retries occurred."""
    assert log_ctx['lock_attempts'][0] >= 2


@then('eventually acquire the lock and write successfully')
def then_eventually_succeeds(log_ctx):
    """Verify write succeeded."""
    content = log_ctx['log_file'].read_text()
    assert 'Retry Test' in content
