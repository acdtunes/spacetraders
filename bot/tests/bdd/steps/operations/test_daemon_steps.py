from pytest_bdd import scenarios, given, when, then, parsers
from unittest.mock import Mock, MagicMock, patch
import io
import sys

from spacetraders_bot.operations.daemon import (
    daemon_start_operation,
    daemon_stop_operation,
    daemon_status_operation,
    daemon_logs_operation,
    daemon_cleanup_operation,
)

scenarios('../../../bdd/features/operations/daemon.feature')


class MockDaemonManager:
    """Mock daemon manager."""
    def __init__(self, player_id):
        self.player_id = player_id
        self.daemons = {}
        self.started_daemons = []
        self.stopped_daemons = []

    def start(self, daemon_id, command):
        """Start a daemon."""
        self.started_daemons.append((daemon_id, command))
        self.daemons[daemon_id] = {
            'daemon_id': daemon_id,
            'command': command,
            'pid': 12345,
            'is_running': True,
            'started_at': '2025-01-01T00:00:00Z',
            'runtime_seconds': 100,
            'cpu_percent': 5.0,
            'memory_mb': 50.0,
            'log_file': f'/path/to/{daemon_id}.log',
            'err_file': f'/path/to/{daemon_id}.err'
        }
        return True

    def stop(self, daemon_id):
        """Stop a daemon."""
        if daemon_id in self.daemons:
            self.stopped_daemons.append(daemon_id)
            self.daemons[daemon_id]['is_running'] = False
            return True
        return False

    def status(self, daemon_id):
        """Get daemon status."""
        return self.daemons.get(daemon_id)

    def list_all(self):
        """List all daemons."""
        return list(self.daemons.values())

    def tail_logs(self, daemon_id, lines):
        """Tail daemon logs."""
        print(f"Tailing {lines} lines for daemon {daemon_id}")
        print("Log line 1")
        print("Log line 2")

    def cleanup_stopped(self):
        """Cleanup stopped daemons."""
        removed = []
        for daemon_id, info in list(self.daemons.items()):
            if not info['is_running']:
                removed.append(daemon_id)
                del self.daemons[daemon_id]
        print(f"Cleaned up {len(removed)} stopped daemons")


class MockAssignmentManager:
    """Mock assignment manager."""
    def __init__(self, player_id):
        self.player_id = player_id
        self.assignments = {}
        self.assigned_ships = []
        self.released_ships = []

    def list_all(self):
        """List all assignments."""
        return self.assignments

    def assign(self, ship, operator, daemon_id, operation):
        """Assign ship."""
        self.assigned_ships.append((ship, operator, daemon_id, operation))
        self.assignments[ship] = {
            'assigned_to': operator,
            'daemon_id': daemon_id,
            'operation': operation,
            'status': 'active'
        }

    def release(self, ship, reason=None):
        """Release ship."""
        self.released_ships.append((ship, reason))
        if ship in self.assignments:
            del self.assignments[ship]


@given('a daemon management system', target_fixture='daemon_ctx')
def given_daemon_system():
    """Create daemon management system."""
    return {
        'daemon_manager': None,
        'assignment_manager': None,
        'result_code': None,
        'output': '',
        'args': None,
    }


@given('no daemons are running')
def given_no_daemons(daemon_ctx):
    """Ensure no daemons running."""
    pass  # MockDaemonManager starts empty


@given(parsers.re(r'ship "(?P<ship>[^"]+)" is available'))
def given_ship_available(daemon_ctx, ship):
    """Ship is available (not assigned)."""
    pass  # No assignment needed


@given(parsers.re(r'ship "(?P<ship>[^"]+)" is assigned to "(?P<daemon_id>[^"]+)" daemon'))
def given_ship_assigned(daemon_ctx, ship, daemon_id):
    """Ship is already assigned."""
    # This will be checked when creating the AssignmentManager
    daemon_ctx['pre_assigned_ship'] = ship
    daemon_ctx['pre_assigned_daemon'] = daemon_id


@given(parsers.re(r'daemon "(?P<daemon_id>[^"]+)" is running with ship "(?P<ship>[^"]+)"'))
def given_daemon_running_with_ship(daemon_ctx, daemon_id, ship):
    """Daemon is running with ship."""
    daemon_ctx['running_daemons'] = daemon_ctx.get('running_daemons', [])
    daemon_ctx['running_daemons'].append({
        'daemon_id': daemon_id,
        'ship': ship,
        'is_running': True
    })


@given(parsers.re(r'daemon "(?P<daemon_id>[^"]+)" is running$'))
def given_daemon_running(daemon_ctx, daemon_id):
    """Daemon is running."""
    daemon_ctx['running_daemons'] = daemon_ctx.get('running_daemons', [])
    daemon_ctx['running_daemons'].append({
        'daemon_id': daemon_id,
        'is_running': True
    })


@given(parsers.re(r'daemon "(?P<daemon_id>[^"]+)" is stopped'))
def given_daemon_stopped(daemon_ctx, daemon_id):
    """Daemon is stopped."""
    daemon_ctx['stopped_daemons'] = daemon_ctx.get('stopped_daemons', [])
    daemon_ctx['stopped_daemons'].append(daemon_id)


@given(parsers.re(r'daemon "(?P<daemon_id>[^"]+)" has log entries'))
def given_daemon_has_logs(daemon_ctx, daemon_id):
    """Daemon has log entries."""
    daemon_ctx['daemon_with_logs'] = daemon_id


@when(parsers.re(r'I start daemon "(?P<daemon_id>[^"]+)" with operation "(?P<operation>[^"]+)" and ship "(?P<ship>[^"]+)"'))
def when_start_daemon_with_ship(daemon_ctx, daemon_id, operation, ship):
    """Start daemon with ship."""
    args = Mock()
    args.player_id = 1
    args.daemon_id = daemon_id
    args.daemon_operation = operation
    args.operation_args = ['--ship', ship]

    daemon_ctx['args'] = args
    _run_daemon_operation(daemon_ctx, daemon_start_operation, args)


@when(parsers.re(r'I start daemon with operation "(?P<operation>[^"]+)" and ship "(?P<ship>[^"]+)" without daemon_id'))
def when_start_daemon_without_id(daemon_ctx, operation, ship):
    """Start daemon without explicit daemon_id."""
    args = Mock()
    args.player_id = 1
    args.daemon_id = None
    args.daemon_operation = operation
    args.operation_args = ['--ship', ship]

    daemon_ctx['args'] = args
    _run_daemon_operation(daemon_ctx, daemon_start_operation, args)


@when(parsers.re(r'I stop daemon "(?P<daemon_id>[^"]+)"'))
def when_stop_daemon(daemon_ctx, daemon_id):
    """Stop daemon."""
    args = Mock()
    args.player_id = 1
    args.daemon_id = daemon_id

    daemon_ctx['args'] = args
    _run_daemon_operation(daemon_ctx, daemon_stop_operation, args)


@when(parsers.re(r'I check status for daemon "(?P<daemon_id>[^"]+)"'))
def when_check_status(daemon_ctx, daemon_id):
    """Check daemon status."""
    args = Mock()
    args.player_id = 1
    args.daemon_id = daemon_id

    daemon_ctx['args'] = args
    _run_daemon_operation(daemon_ctx, daemon_status_operation, args)


@when('I list all daemons')
def when_list_all_daemons(daemon_ctx):
    """List all daemons."""
    args = Mock()
    args.player_id = 1
    args.daemon_id = None

    daemon_ctx['args'] = args
    _run_daemon_operation(daemon_ctx, daemon_status_operation, args)


@when(parsers.re(r'I tail logs for daemon "(?P<daemon_id>[^"]+)" with (?P<lines>\d+) lines'))
def when_tail_logs(daemon_ctx, daemon_id, lines):
    """Tail daemon logs."""
    args = Mock()
    args.player_id = 1
    args.daemon_id = daemon_id
    args.lines = int(lines)

    daemon_ctx['args'] = args
    _run_daemon_operation(daemon_ctx, daemon_logs_operation, args)


@when('I cleanup stopped daemons')
def when_cleanup_stopped(daemon_ctx):
    """Cleanup stopped daemons."""
    args = Mock()
    args.player_id = 1

    daemon_ctx['args'] = args
    _run_daemon_operation(daemon_ctx, daemon_cleanup_operation, args)


@when('I start daemon without player_id')
def when_start_without_player_id(daemon_ctx):
    """Start daemon without player_id."""
    args = Mock()
    args.player_id = None
    args.daemon_id = "test-daemon"
    args.daemon_operation = "mine"
    args.operation_args = []

    daemon_ctx['args'] = args
    _run_daemon_operation(daemon_ctx, daemon_start_operation, args)


def _run_daemon_operation(daemon_ctx, operation_func, args):
    """Helper to run daemon operation with mocks."""
    # Capture stdout
    captured_output = io.StringIO()
    sys.stdout = captured_output

    # Create mocks
    daemon_manager = MockDaemonManager(player_id=getattr(args, 'player_id', 1))
    assignment_manager = MockAssignmentManager(player_id=getattr(args, 'player_id', 1))

    # Setup pre-existing daemons
    for daemon_info in daemon_ctx.get('running_daemons', []):
        daemon_id = daemon_info['daemon_id']
        command = ['python', '-m', 'spacetraders_bot.cli', 'mine']
        if 'ship' in daemon_info:
            command.extend(['--ship', daemon_info['ship']])
        daemon_manager.daemons[daemon_id] = {
            'daemon_id': daemon_id,
            'command': command,
            'pid': 12345,
            'is_running': daemon_info.get('is_running', True),
            'started_at': '2025-01-01T00:00:00Z',
            'runtime_seconds': 100,
            'cpu_percent': 5.0,
            'memory_mb': 50.0,
            'log_file': f'/path/to/{daemon_id}.log',
            'err_file': f'/path/to/{daemon_id}.err'
        }

    for daemon_id in daemon_ctx.get('stopped_daemons', []):
        daemon_manager.daemons[daemon_id] = {
            'daemon_id': daemon_id,
            'command': ['python', '-m', 'spacetraders_bot.cli', 'mine'],
            'pid': 12345,
            'is_running': False,
            'started_at': '2025-01-01T00:00:00Z',
            'runtime_seconds': None,
            'cpu_percent': 0.0,
            'memory_mb': 0.0,
            'log_file': f'/path/to/{daemon_id}.log',
            'err_file': f'/path/to/{daemon_id}.err'
        }

    # Setup pre-assigned ship
    if 'pre_assigned_ship' in daemon_ctx:
        ship = daemon_ctx['pre_assigned_ship']
        daemon_id = daemon_ctx['pre_assigned_daemon']
        assignment_manager.assignments[ship] = {
            'assigned_to': 'existing_operator',
            'daemon_id': daemon_id,
            'operation': 'mine',
            'status': 'active'
        }

    # Store managers in context
    daemon_ctx['daemon_manager'] = daemon_manager
    daemon_ctx['assignment_manager'] = assignment_manager

    # Mock the manager constructors
    # Note: daemon_stop_operation imports AssignmentManager locally, so patch it in core module too
    with patch('spacetraders_bot.operations.daemon.DaemonManager', return_value=daemon_manager):
        with patch('spacetraders_bot.operations.daemon.AssignmentManager', return_value=assignment_manager):
            with patch('spacetraders_bot.core.assignment_manager.AssignmentManager', return_value=assignment_manager):
                result = operation_func(args)

    # Restore stdout
    sys.stdout = sys.__stdout__

    daemon_ctx['output'] = captured_output.getvalue()
    daemon_ctx['result_code'] = result


@then(parsers.re(r'daemon "(?P<daemon_id>[^"]+)" should be running'))
def then_daemon_running(daemon_ctx, daemon_id):
    """Verify daemon is running."""
    daemon_manager = daemon_ctx['daemon_manager']
    assert daemon_id in daemon_manager.daemons
    assert daemon_manager.daemons[daemon_id]['is_running']


@then(parsers.re(r'ship "(?P<ship>[^"]+)" should be assigned to "(?P<operator>[^"]+)"'))
def then_ship_assigned(daemon_ctx, ship, operator):
    """Verify ship is assigned."""
    assignment_manager = daemon_ctx['assignment_manager']
    assert ship in assignment_manager.assignments
    assert assignment_manager.assignments[ship]['assigned_to'] == operator


@then('daemon start should fail')
def then_daemon_start_fails(daemon_ctx):
    """Verify daemon start failed."""
    assert daemon_ctx['result_code'] == 1


@then('error message should mention ship already assigned')
def then_error_ship_assigned(daemon_ctx):
    """Verify error message about ship assignment."""
    assert 'already assigned' in daemon_ctx['output']


@then(parsers.re(r'daemon "(?P<daemon_id>[^"]+)" should be stopped'))
def then_daemon_stopped(daemon_ctx, daemon_id):
    """Verify daemon is stopped."""
    daemon_manager = daemon_ctx['daemon_manager']
    assert daemon_id in daemon_manager.stopped_daemons


@then(parsers.re(r'ship "(?P<ship>[^"]+)" should be released'))
def then_ship_released(daemon_ctx, ship):
    """Verify ship is released."""
    assignment_manager = daemon_ctx['assignment_manager']
    released_ships = [s for s, _ in assignment_manager.released_ships]
    assert ship in released_ships


@then('status should show daemon is running')
def then_status_shows_running(daemon_ctx):
    """Verify status shows running."""
    assert 'RUNNING' in daemon_ctx['output']


@then('status should include PID and runtime')
def then_status_includes_details(daemon_ctx):
    """Verify status includes details."""
    assert 'PID:' in daemon_ctx['output']
    assert 'Runtime:' in daemon_ctx['output'] or 'Started:' in daemon_ctx['output']


@then(parsers.re(r'(?P<count>\d+) daemons should be listed'))
def then_daemons_listed(daemon_ctx, count):
    """Verify daemon count."""
    # Count daemon IDs in output (excluding headers)
    lines = daemon_ctx['output'].split('\n')
    daemon_lines = [l for l in lines if l.strip() and '=' not in l and 'DAEMON ID' not in l and 'STATUS' not in l]
    # Each daemon has one line in the table
    assert len(daemon_lines) >= int(count)


@then(parsers.re(r'(?P<count>\d+) daemons should show as running'))
def then_daemons_running(daemon_ctx, count):
    """Verify running daemon count."""
    running_count = daemon_ctx['output'].count('RUNNING')
    assert running_count == int(count)


@then('log output should be displayed')
def then_log_output_displayed(daemon_ctx):
    """Verify log output."""
    assert 'Tailing' in daemon_ctx['output']
    assert 'Log line' in daemon_ctx['output']


@then('stopped daemons should be removed')
def then_stopped_removed(daemon_ctx):
    """Verify stopped daemons removed."""
    assert 'Cleaned up' in daemon_ctx['output']


@then('running daemons should remain')
def then_running_remain(daemon_ctx):
    """Verify running daemons remain."""
    daemon_manager = daemon_ctx['daemon_manager']
    # Check that at least one daemon is still running
    running = [d for d in daemon_manager.daemons.values() if d['is_running']]
    assert len(running) > 0


@then('operation should fail')
def then_operation_fails(daemon_ctx):
    """Verify operation failed."""
    assert daemon_ctx['result_code'] == 1


@then('error message should mention player_id required')
def then_error_player_id(daemon_ctx):
    """Verify error message about player_id."""
    assert 'player-id required' in daemon_ctx['output'] or 'player_id required' in daemon_ctx['output']
