from pytest_bdd import scenarios, given, when, then, parsers
from unittest.mock import Mock, patch
import io
import sys

from spacetraders_bot.operations.assignments import (
    assignment_list_operation,
    assignment_assign_operation,
    assignment_release_operation,
    assignment_available_operation,
    assignment_find_operation,
    assignment_sync_operation,
    assignment_status_operation,
)

scenarios('../../../bdd/features/operations/assignments.feature')


class MockAssignmentManager:
    """Mock assignment manager for testing."""
    def __init__(self, player_id):
        self.player_id = player_id
        self.registry = {}
        self.daemon_statuses = {}

    def list_all(self, include_stale=False):
        """List all assignments."""
        return dict(self.registry)

    def assign(self, ship, operator, daemon_id, operation, metadata=None):
        """Assign ship."""
        if ship in self.registry:
            print(f"❌ Ship {ship} already assigned")
            return False

        self.registry[ship] = {
            'assigned_to': operator,
            'daemon_id': daemon_id,
            'operation': operation,
            'status': 'active',
            'metadata': metadata or {}
        }
        print(f"✅ Assigned {ship} to {operator}")
        return True

    def release(self, ship, reason=None):
        """Release ship."""
        if ship not in self.registry:
            print(f"⚠️  Ship {ship} not in registry")
            return False

        del self.registry[ship]
        print(f"✅ Released {ship}")
        return True

    def is_available(self, ship):
        """Check if ship is available."""
        return ship not in self.registry

    def get_assignment(self, ship):
        """Get assignment for ship."""
        return self.registry.get(ship)

    def find_available(self, requirements=None):
        """Find available ships."""
        # For testing, return ships NOT in registry
        # We'll track which ships exist in the test context
        return []

    def sync_with_daemons(self):
        """Sync with daemon status."""
        released = []
        still_active = []

        for ship, data in list(self.registry.items()):
            daemon_id = data.get('daemon_id')
            if daemon_id and daemon_id in self.daemon_statuses:
                if not self.daemon_statuses[daemon_id]:
                    # Daemon stopped, release ship
                    released.append(ship)
                    del self.registry[ship]
                else:
                    still_active.append(ship)

        return {'released': released, 'still_active': still_active}


@given('an assignment management system', target_fixture='assignment_ctx')
def given_assignment_system():
    """Create assignment management system."""
    return {
        'manager': None,
        'available_ships': [],
        'result_code': None,
        'output': '',
        'args': None,
    }


@given('no ships are assigned')
def given_no_ships_assigned(assignment_ctx):
    """No ships assigned."""
    pass  # MockAssignmentManager starts empty


@given(parsers.re(r'ship "(?P<ship>[^"]+)" is assigned to "(?P<operator>[^"]+)" daemon "(?P<daemon_id>[^"]+)" operation "(?P<operation>[^"]+)"'))
def given_ship_assigned(assignment_ctx, ship, operator, daemon_id, operation):
    """Ship is assigned."""
    if 'manager' not in assignment_ctx or not assignment_ctx['manager']:
        assignment_ctx['manager'] = MockAssignmentManager(player_id=1)

    manager = assignment_ctx['manager']
    manager.registry[ship] = {
        'assigned_to': operator,
        'daemon_id': daemon_id,
        'operation': operation,
        'status': 'active',
        'assigned_at': '2025-01-01T00:00:00Z'
    }
    # Mark daemon as running by default
    manager.daemon_statuses[daemon_id] = True


@given(parsers.re(r'ship "(?P<ship>[^"]+)" is available'))
def given_ship_available(assignment_ctx, ship):
    """Ship is available."""
    assignment_ctx['available_ships'] = assignment_ctx.get('available_ships', [])
    assignment_ctx['available_ships'].append(ship)


@given(parsers.re(r'daemon "(?P<daemon_id>[^"]+)" is stopped'))
def given_daemon_stopped(assignment_ctx, daemon_id):
    """Daemon is stopped."""
    if 'manager' not in assignment_ctx or not assignment_ctx['manager']:
        assignment_ctx['manager'] = MockAssignmentManager(player_id=1)

    assignment_ctx['manager'].daemon_statuses[daemon_id] = False


@when('I list all assignments')
def when_list_assignments(assignment_ctx):
    """List all assignments."""
    args = Mock()
    args.player_id = 1
    args.include_stale = False
    args.log_level = 'ERROR'

    assignment_ctx['args'] = args
    _run_assignment_operation(assignment_ctx, assignment_list_operation, args)


@when('I list assignments without player_id')
def when_list_without_player_id(assignment_ctx):
    """List assignments without player_id."""
    args = Mock()
    args.player_id = None
    args.log_level = 'ERROR'

    assignment_ctx['args'] = args
    _run_assignment_operation(assignment_ctx, assignment_list_operation, args)


@when(parsers.re(r'I assign ship "(?P<ship>[^"]+)" to operator "(?P<operator>[^"]+)" daemon "(?P<daemon_id>[^"]+)" operation "(?P<operation>[^"]+)"'))
def when_assign_ship(assignment_ctx, ship, operator, daemon_id, operation):
    """Assign ship."""
    args = Mock()
    args.player_id = 1
    args.ship = ship
    args.operator = operator
    args.daemon_id = daemon_id
    args.operation_type = operation
    args.log_level = 'ERROR'

    assignment_ctx['args'] = args
    _run_assignment_operation(assignment_ctx, assignment_assign_operation, args)


@when(parsers.re(r'I release ship "(?P<ship>[^"]+)" with reason "(?P<reason>[^"]+)"'))
def when_release_ship(assignment_ctx, ship, reason):
    """Release ship."""
    args = Mock()
    args.player_id = 1
    args.ship = ship
    args.reason = reason
    args.log_level = 'ERROR'

    assignment_ctx['args'] = args
    _run_assignment_operation(assignment_ctx, assignment_release_operation, args)


@when(parsers.re(r'I check if ship "(?P<ship>[^"]+)" is available'))
def when_check_available(assignment_ctx, ship):
    """Check if ship is available."""
    args = Mock()
    args.player_id = 1
    args.ship = ship
    args.log_level = 'ERROR'

    assignment_ctx['args'] = args
    _run_assignment_operation(assignment_ctx, assignment_available_operation, args)


@when('I find available ships')
def when_find_available(assignment_ctx):
    """Find available ships."""
    args = Mock()
    args.player_id = 1
    args.cargo_min = None
    args.fuel_min = None
    args.log_level = 'ERROR'

    # Patch find_available to return available ships from context
    def mock_find_available(requirements=None):
        return assignment_ctx.get('available_ships', [])

    assignment_ctx['args'] = args

    # Capture stdout
    captured_output = io.StringIO()
    sys.stdout = captured_output

    if 'manager' not in assignment_ctx or not assignment_ctx['manager']:
        assignment_ctx['manager'] = MockAssignmentManager(player_id=1)

    manager = assignment_ctx['manager']
    manager.find_available = mock_find_available

    with patch('spacetraders_bot.operations.assignments.AssignmentManager', return_value=manager):
        result = assignment_find_operation(args)

    # Restore stdout
    sys.stdout = sys.__stdout__

    assignment_ctx['output'] = captured_output.getvalue()
    assignment_ctx['result_code'] = result


@when('I sync assignments')
def when_sync_assignments(assignment_ctx):
    """Sync assignments."""
    args = Mock()
    args.player_id = 1
    args.log_level = 'ERROR'

    assignment_ctx['args'] = args
    _run_assignment_operation(assignment_ctx, assignment_sync_operation, args)


@when(parsers.re(r'I get status for ship "(?P<ship>[^"]+)"'))
def when_get_status(assignment_ctx, ship):
    """Get status for ship."""
    args = Mock()
    args.player_id = 1
    args.ship = ship
    args.log_level = 'ERROR'

    assignment_ctx['args'] = args
    _run_assignment_operation(assignment_ctx, assignment_status_operation, args)


def _run_assignment_operation(assignment_ctx, operation_func, args):
    """Helper to run assignment operation with mocks."""
    # Capture stdout
    captured_output = io.StringIO()
    sys.stdout = captured_output

    if 'manager' not in assignment_ctx or not assignment_ctx['manager']:
        assignment_ctx['manager'] = MockAssignmentManager(player_id=1)

    manager = assignment_ctx['manager']

    with patch('spacetraders_bot.operations.assignments.AssignmentManager', return_value=manager):
        result = operation_func(args)

    # Restore stdout
    sys.stdout = sys.__stdout__

    assignment_ctx['output'] = captured_output.getvalue()
    assignment_ctx['result_code'] = result


@then(parsers.re(r'(?P<count>\d+) assignments should be shown'))
def then_assignments_shown(assignment_ctx, count):
    """Verify assignment count."""
    output = assignment_ctx['output']
    # Count ship symbols in output (excluding header)
    lines = output.split('\n')
    ship_lines = [l for l in lines if l.strip() and 'SHIP-' in l and 'SHIP' not in l.upper() or 'SHIP-' in l and '=' not in l]

    # More reliable: count actual SHIP- patterns
    ship_count = sum(1 for line in lines if line.strip() and line.strip()[0:5] == 'SHIP-')
    assert ship_count == int(count), f"Expected {count} assignments, found {ship_count}"


@then('output should show no ship assignments')
def then_no_assignments(assignment_ctx):
    """Verify no assignments shown."""
    output = assignment_ctx['output']
    assert 'No ship assignments' in output


@then(parsers.re(r'assignment for "(?P<ship>[^"]+)" should show operator "(?P<operator>[^"]+)"'))
def then_assignment_shows_operator(assignment_ctx, ship, operator):
    """Verify assignment shows operator."""
    output = assignment_ctx['output']
    # Find the line with this ship
    lines = output.split('\n')
    for line in lines:
        if ship in line:
            assert operator in line
            return
    assert False, f"Ship {ship} not found in output"


@then(parsers.re(r'ship "(?P<ship>[^"]+)" should be assigned successfully'))
def then_ship_assigned(assignment_ctx, ship):
    """Verify ship assigned."""
    output = assignment_ctx['output']
    assert f'Assigned {ship}' in output
    assert assignment_ctx['result_code'] == 0


@then(parsers.re(r'assignment registry should contain "(?P<ship>[^"]+)"'))
def then_registry_contains(assignment_ctx, ship):
    """Verify registry contains ship."""
    manager = assignment_ctx['manager']
    assert ship in manager.registry


@then(parsers.re(r'ship "(?P<ship>[^"]+)" should be released successfully'))
def then_ship_released(assignment_ctx, ship):
    """Verify ship released."""
    output = assignment_ctx['output']
    assert f'Released {ship}' in output
    assert assignment_ctx['result_code'] == 0


@then(parsers.re(r'ship "(?P<ship>[^"]+)" should not be in registry'))
def then_not_in_registry(assignment_ctx, ship):
    """Verify ship not in registry."""
    manager = assignment_ctx['manager']
    assert ship not in manager.registry


@then('operation should succeed')
def then_operation_succeeds(assignment_ctx):
    """Verify operation succeeded."""
    assert assignment_ctx['result_code'] == 0


@then('operation should fail')
def then_operation_fails(assignment_ctx):
    """Verify operation failed."""
    assert assignment_ctx['result_code'] == 1


@then('output should show ship is available')
def then_ship_is_available(assignment_ctx):
    """Verify ship shown as available."""
    output = assignment_ctx['output']
    assert 'is available' in output


@then('output should show ship is assigned')
def then_ship_is_assigned(assignment_ctx):
    """Verify ship shown as assigned."""
    output = assignment_ctx['output']
    assert 'is currently assigned' in output


@then(parsers.re(r'(?P<count>\d+) available ships should be found'))
def then_available_ships_found(assignment_ctx, count):
    """Verify available ship count."""
    output = assignment_ctx['output']
    assert f'({count})' in output


@then(parsers.re(r'"(?P<ship>[^"]+)" should be in available list'))
def then_ship_in_available_list(assignment_ctx, ship):
    """Verify ship in available list."""
    output = assignment_ctx['output']
    assert ship in output


@then(parsers.re(r'ship "(?P<ship>[^"]+)" should be released'))
def then_ship_released_by_sync(assignment_ctx, ship):
    """Verify ship released by sync."""
    manager = assignment_ctx['manager']
    assert ship not in manager.registry


@then(parsers.re(r'sync output should show (?P<count>\d+) released ships?'))
def then_sync_shows_released(assignment_ctx, count):
    """Verify sync released count."""
    output = assignment_ctx['output']
    assert f'Released (daemon stopped): {count}' in output


@then(parsers.re(r'status should show operator "(?P<operator>[^"]+)"'))
def then_status_shows_operator(assignment_ctx, operator):
    """Verify status shows operator."""
    output = assignment_ctx['output']
    assert f'Operator: {operator}' in output


@then(parsers.re(r'status should show daemon "(?P<daemon_id>[^"]+)"'))
def then_status_shows_daemon(assignment_ctx, daemon_id):
    """Verify status shows daemon."""
    output = assignment_ctx['output']
    assert f'Daemon: {daemon_id}' in output


@then(parsers.re(r'status should show operation "(?P<operation>[^"]+)"'))
def then_status_shows_operation(assignment_ctx, operation):
    """Verify status shows operation."""
    output = assignment_ctx['output']
    assert f'Operation: {operation}' in output


@then('status should show ship is not in registry')
def then_status_shows_not_in_registry(assignment_ctx):
    """Verify status shows not in registry."""
    output = assignment_ctx['output']
    assert 'Not in registry' in output


@then('error message should mention player_id required')
def then_error_player_id_required(assignment_ctx):
    """Verify error message about player_id."""
    output = assignment_ctx['output']
    assert 'player-id required' in output or 'player_id required' in output
