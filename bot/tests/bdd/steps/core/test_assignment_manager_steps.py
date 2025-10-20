from unittest.mock import Mock
from pytest_bdd import scenarios, given, when, then, parsers

from spacetraders_bot.core.ship_assignment_repository import AssignmentManager

scenarios('../../../bdd/features/core/assignment_manager.feature')


class MockDatabase:
    """Mock database for assignment manager tests."""
    def __init__(self):
        self.players = {}
        self.assignments = {}
        self.player_counter = 0

    def connection(self):
        """Mock connection context manager."""
        return self

    def transaction(self):
        """Mock transaction context manager."""
        return self

    def __enter__(self):
        return self

    def __exit__(self, *args):
        pass

    def create_player(self, conn, agent_symbol, token, metadata=None):
        """Create or get player."""
        # Find existing player
        for pid, player in self.players.items():
            if player['agent_symbol'] == agent_symbol:
                return pid

        # Create new player
        self.player_counter += 1
        self.players[self.player_counter] = {
            'id': self.player_counter,
            'agent_symbol': agent_symbol,
            'token': token,
            'metadata': metadata or {}
        }
        return self.player_counter

    def get_player_by_id(self, conn, player_id):
        """Get player by ID."""
        return self.players.get(player_id)

    def assign_ship(self, conn, player_id, ship, operator, daemon_id, operation, metadata=None):
        """Assign ship to operation."""
        key = (player_id, ship)
        self.assignments[key] = {
            'player_id': player_id,
            'ship_symbol': ship,
            'assigned_to': operator,
            'daemon_id': daemon_id,
            'operation': operation,
            'status': 'active',
            'metadata': metadata or {}
        }
        return True

    def release_ship(self, conn, player_id, ship, reason):
        """Release ship assignment."""
        key = (player_id, ship)
        if key in self.assignments:
            self.assignments[key]['status'] = 'idle'
            self.assignments[key]['release_reason'] = reason
            return True
        return False

    def get_ship_assignment(self, conn, player_id, ship):
        """Get ship assignment."""
        key = (player_id, ship)
        return self.assignments.get(key)

    def list_ship_assignments(self, conn, player_id):
        """List all assignments for player."""
        return [a for (pid, _), a in self.assignments.items() if pid == player_id]


class MockDaemonManager:
    """Mock daemon manager for assignment tests."""
    def __init__(self):
        self.running_daemons = set()

    def is_running(self, daemon_id):
        """Check if daemon is running."""
        return daemon_id in self.running_daemons

    def start_daemon(self, daemon_id):
        """Mark daemon as running."""
        self.running_daemons.add(daemon_id)

    def stop_daemon(self, daemon_id):
        """Mark daemon as stopped."""
        self.running_daemons.discard(daemon_id)


@given('an assignment manager for player 1', target_fixture='assign_ctx')
def given_assignment_manager_player_1():
    """Create assignment manager for player 1."""
    db = MockDatabase()
    daemon_mgr = MockDaemonManager()

    # Create player 1
    with db.transaction() as conn:
        player_id = db.create_player(conn, "TEST-AGENT-1", "fake-token-1")

    # Create manager with agent_symbol+token first, then replace internals
    manager = AssignmentManager.__new__(AssignmentManager)
    manager.player_id = player_id
    manager.agent_symbol = "TEST-AGENT-1"
    manager.token = "fake-token-1"
    manager.db = db
    manager.db_path = ":memory:"
    manager.daemon_manager = daemon_mgr
    manager._api_client = None

    context = {
        'manager': manager,
        'player_id': player_id,
        'db': db,
        'daemon_mgr': daemon_mgr,
        'result': None,
        'assignment': None
    }
    return context


@given('the assignment database is initialized')
def given_database_initialized(assign_ctx):
    """Database is already initialized."""
    return assign_ctx


@given(parsers.parse('ship "{ship}" is available'))
def given_ship_available(assign_ctx, ship):
    """Ensure ship is available."""
    # No assignment needed - ship is available by default
    return assign_ctx


@given(parsers.parse('ship "{ship}" is assigned to operator "{operator}" with daemon "{daemon_id}"'))
def given_ship_assigned(assign_ctx, ship, operator, daemon_id):
    """Assign ship to operator."""
    manager = assign_ctx['manager']
    daemon_mgr = assign_ctx['daemon_mgr']

    # Assign ship
    manager.assign(ship, operator, daemon_id, "test_operation")

    # Mark daemon as running by default
    daemon_mgr.start_daemon(daemon_id)

    return assign_ctx


@given(parsers.re(r'ship "(?P<ship>[^"]+)" is assigned to operator "(?P<operator>[^"]+)" with daemon "(?P<daemon_id>[^"]+)" for operation "(?P<operation>[^"]+)"'))
def given_ship_assigned_with_operation(assign_ctx, ship, operator, daemon_id, operation):
    """Assign ship with specific operation."""
    manager = assign_ctx['manager']
    daemon_mgr = assign_ctx['daemon_mgr']

    manager.assign(ship, operator, daemon_id, operation)
    daemon_mgr.start_daemon(daemon_id)

    return assign_ctx


@given(parsers.parse('daemon "{daemon_id}" is running'))
def given_daemon_running(assign_ctx, daemon_id):
    """Ensure daemon is running."""
    daemon_mgr = assign_ctx['daemon_mgr']
    daemon_mgr.start_daemon(daemon_id)
    return assign_ctx


@given(parsers.parse('daemon "{daemon_id}" has stopped'))
def given_daemon_stopped(assign_ctx, daemon_id):
    """Mark daemon as stopped."""
    daemon_mgr = assign_ctx['daemon_mgr']
    daemon_mgr.stop_daemon(daemon_id)
    return assign_ctx


@given(parsers.parse('ship "{ship}" is not in the registry'))
def given_ship_not_in_registry(assign_ctx, ship):
    """Ensure ship is not in registry."""
    # Ship is not in registry by default
    return assign_ctx


@given(parsers.parse('ship "{ship}" has been released'))
def given_ship_released(assign_ctx, ship):
    """Release a ship."""
    manager = assign_ctx['manager']
    manager.release(ship)
    return assign_ctx


@given('an assignment manager for player 2')
def given_assignment_manager_player_2(assign_ctx):
    """Create second assignment manager."""
    db = assign_ctx['db']
    daemon_mgr = assign_ctx['daemon_mgr']

    # Create player 2
    with db.transaction() as conn:
        player_id_2 = db.create_player(conn, "TEST-AGENT-2", "fake-token-2")

    # Create manager for player 2
    manager_2 = AssignmentManager.__new__(AssignmentManager)
    manager_2.player_id = player_id_2
    manager_2.agent_symbol = "TEST-AGENT-2"
    manager_2.token = "fake-token-2"
    manager_2.db = db
    manager_2.db_path = ":memory:"
    manager_2.daemon_manager = daemon_mgr
    manager_2._api_client = None

    assign_ctx['manager_2'] = manager_2
    assign_ctx['player_id_2'] = player_id_2

    return assign_ctx


@given(parsers.parse('ship "{ship}" is assigned to operator "{operator}" for player 1'))
def given_ship_assigned_player_1(assign_ctx, ship, operator):
    """Assign ship for player 1."""
    manager = assign_ctx['manager']
    manager.assign(ship, operator, "daemon-1", "test_operation")
    return assign_ctx


@when(parsers.parse('I assign "{ship}" to operator "{operator}" with daemon "{daemon_id}" for operation "{operation}"'))
def when_assign_ship(assign_ctx, ship, operator, daemon_id, operation):
    """Assign ship to operation."""
    manager = assign_ctx['manager']
    assign_ctx['result'] = manager.assign(ship, operator, daemon_id, operation)
    return assign_ctx


@when(parsers.parse('I attempt to assign "{ship}" to operator "{operator}" with daemon "{daemon_id}" for operation "{operation}"'))
def when_attempt_assign_ship(assign_ctx, ship, operator, daemon_id, operation):
    """Attempt to assign ship (might fail)."""
    manager = assign_ctx['manager']
    assign_ctx['result'] = manager.assign(ship, operator, daemon_id, operation)
    return assign_ctx


@when(parsers.parse('I release "{ship}"'))
def when_release_ship(assign_ctx, ship, capsys):
    """Release ship from assignment."""
    manager = assign_ctx['manager']
    assign_ctx['result'] = manager.release(ship)
    assign_ctx['stdout'] = capsys.readouterr().out
    return assign_ctx


@when(parsers.parse('I check if "{ship}" is available'))
def when_check_available(assign_ctx, ship):
    """Check if ship is available."""
    manager = assign_ctx['manager']
    assign_ctx['result'] = manager.is_available(ship)
    return assign_ctx


@when('I sync assignments with daemons')
def when_sync_assignments(assign_ctx):
    """Sync assignments with daemon status."""
    manager = assign_ctx['manager']
    assign_ctx['result'] = manager.list_all(include_stale=True)
    return assign_ctx


@when('I find available ships')
def when_find_available(assign_ctx):
    """Find available ships."""
    manager = assign_ctx['manager']
    assign_ctx['result'] = manager.find_available()
    return assign_ctx


@when('I list all assignments')
def when_list_assignments(assign_ctx):
    """List all assignments."""
    manager = assign_ctx['manager']
    assign_ctx['result'] = manager.list_all()
    return assign_ctx


@when(parsers.parse('I get assignment details for "{ship}"'))
def when_get_assignment(assign_ctx, ship):
    """Get assignment details."""
    manager = assign_ctx['manager']
    assign_ctx['assignment'] = manager.get_assignment(ship)
    return assign_ctx


@when(parsers.parse('player 2 checks if "{ship}" is available'))
def when_player_2_check_available(assign_ctx, ship):
    """Player 2 checks ship availability."""
    manager_2 = assign_ctx['manager_2']
    assign_ctx['result'] = manager_2.is_available(ship)
    return assign_ctx


@when(parsers.parse('I assign "{ship}" with metadata containing target "{target}"'))
def when_assign_with_metadata(assign_ctx, ship, target):
    """Assign ship with metadata."""
    manager = assign_ctx['manager']
    metadata = {'target': target}
    assign_ctx['result'] = manager.assign(ship, "test_operator", "test-daemon", "test_operation", metadata)
    return assign_ctx


@then('the assignment should succeed')
def then_assignment_succeeds(assign_ctx):
    """Verify assignment succeeded."""
    assert assign_ctx['result'] is True


@then('the assignment should fail')
def then_assignment_fails(assign_ctx):
    """Verify assignment failed."""
    assert assign_ctx['result'] is False


@then('the release should succeed')
def then_release_succeeds(assign_ctx):
    """Verify release succeeded."""
    assert assign_ctx['result'] is True


@then(parsers.parse('the release should fail with message "{message}"'))
def then_release_fails_with_message(assign_ctx, message):
    """Verify release failed with message."""
    assert assign_ctx['result'] is False
    assert message.lower() in assign_ctx.get('stdout', '').lower()


@then(parsers.parse('"{ship}" should be assigned to "{operator}"'))
def then_ship_assigned_to_operator(assign_ctx, ship, operator):
    """Verify ship assigned to operator."""
    manager = assign_ctx['manager']
    assignment = manager.get_assignment(ship)
    assert assignment is not None
    assert assignment['assigned_to'] == operator


@then(parsers.parse('"{ship}" should still be assigned to "{operator}"'))
def then_ship_still_assigned_to_operator(assign_ctx, ship, operator):
    """Verify ship still assigned to operator."""
    manager = assign_ctx['manager']
    assignment = manager.get_assignment(ship)
    assert assignment is not None
    assert assignment['assigned_to'] == operator


@then(parsers.parse('the assignment should reference daemon "{daemon_id}"'))
def then_assignment_references_daemon(assign_ctx, daemon_id):
    """Verify assignment references daemon."""
    manager = assign_ctx['manager']
    # Get the most recently assigned ship (we'd need to track this)
    # For now, we'll check that at least one assignment has this daemon
    assignments = manager.list_all()
    daemon_ids = [a['daemon_id'] for a in assignments.values()]
    assert daemon_id in daemon_ids


@then(parsers.parse('"{ship}" should be available'))
def then_ship_available(assign_ctx, ship):
    """Verify ship is available."""
    manager = assign_ctx['manager']
    assert manager.is_available(ship) is True


@then('the ship should be reported as available')
def then_reported_available(assign_ctx):
    """Verify availability check returned True."""
    assert assign_ctx['result'] is True


@then('the ship should be reported as available due to stale daemon')
def then_reported_available_stale(assign_ctx):
    """Verify ship available due to stale daemon."""
    assert assign_ctx['result'] is True


@then(parsers.parse('"{ship}" should be marked as stale'))
def then_ship_marked_stale(assign_ctx, ship):
    """Verify ship marked as stale."""
    result = assign_ctx['result']
    assert ship in result
    assert result[ship]['status'] == 'stale'


@then(parsers.parse('"{ship}" should remain active'))
def then_ship_remains_active(assign_ctx, ship):
    """Verify ship remains active."""
    result = assign_ctx['result']
    assert ship in result
    assert result[ship]['status'] == 'active'


@then(parsers.parse('the results should include "{ship}"'))
def then_results_include_ship(assign_ctx, ship):
    """Verify ship in results."""
    assert ship in assign_ctx['result']


@then(parsers.parse('the results should not include "{ship}"'))
def then_results_not_include_ship(assign_ctx, ship):
    """Verify ship not in results."""
    assert ship not in assign_ctx['result']


@then(parsers.parse('the list should contain {count:d} assignments'))
def then_list_contains_count(assign_ctx, count):
    """Verify assignment count."""
    result = assign_ctx['result']
    assert len(result) == count


@then(parsers.parse('the list should include assignment for "{ship}"'))
def then_list_includes_assignment(assign_ctx, ship):
    """Verify assignment in list."""
    result = assign_ctx['result']
    assert ship in result


@then(parsers.parse('the details should show operator "{operator}"'))
def then_details_show_operator(assign_ctx, operator):
    """Verify operator in details."""
    assignment = assign_ctx['assignment']
    assert assignment is not None
    assert assignment['assigned_to'] == operator


@then(parsers.parse('the details should show daemon "{daemon_id}"'))
def then_details_show_daemon(assign_ctx, daemon_id):
    """Verify daemon in details."""
    assignment = assign_ctx['assignment']
    assert assignment is not None
    assert assignment['daemon_id'] == daemon_id


@then(parsers.parse('the details should show operation "{operation}"'))
def then_details_show_operation(assign_ctx, operation):
    """Verify operation in details."""
    assignment = assign_ctx['assignment']
    assert assignment is not None
    assert assignment['operation'] == operation


@then(parsers.parse('player 2 should see "{ship}" as available'))
def then_player_2_sees_available(assign_ctx, ship):
    """Verify player 2 sees ship as available."""
    assert assign_ctx['result'] is True


@then(parsers.parse("player 2's assignments should not include \"{ship}\""))
def then_player_2_assignments_not_include(assign_ctx, ship):
    """Verify player 2's assignments don't include ship."""
    manager_2 = assign_ctx['manager_2']
    assignments = manager_2.list_all()
    assert ship not in assignments


@then(parsers.parse('the assignment metadata should contain target "{target}"'))
def then_metadata_contains_target(assign_ctx, target):
    """Verify metadata contains target."""
    manager = assign_ctx['manager']
    # Get the most recent assignment (we'd need to track the ship)
    # For now, check any assignment with the metadata
    assignments = manager.list_all()
    found = False
    for assignment in assignments.values():
        if assignment.get('metadata', {}).get('target') == target:
            found = True
            break
    assert found is True
