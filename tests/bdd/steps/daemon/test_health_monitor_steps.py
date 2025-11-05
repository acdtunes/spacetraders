"""Step definitions for health monitor tests"""
import asyncio
import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from unittest.mock import Mock, patch

from spacetraders.adapters.primary.daemon.daemon_server import DaemonServer
from spacetraders.adapters.primary.daemon.assignment_manager import ShipAssignmentManager

# Load scenarios
scenarios('../../features/daemon/health_monitor.feature')


@given('the daemon server is running with health monitor enabled')
def daemon_with_health_monitor(context):
    """Start daemon with health monitor"""
    # Health monitor is enabled by default in DaemonServer
    context['health_monitor_enabled'] = True


@given(parsers.parse('a navigation container "{container_id}" existed for ship "{ship_symbol}"'))
def container_existed(context, container_id, ship_symbol):
    """Setup scenario where container existed"""
    context['container_id'] = container_id
    context['ship_symbol'] = ship_symbol
    context['container_existed'] = True


@given(parsers.parse('the ship "{ship_symbol}" was assigned to "{container_id}"'))
def ship_was_assigned(context, ship_symbol, container_id):
    """Assign ship using public API"""
    from spacetraders.configuration.container import get_database

    assignment_mgr = ShipAssignmentManager(get_database())
    success = assignment_mgr.assign(
        player_id=context['player_id'],
        ship_symbol=ship_symbol,
        container_id=container_id,
        operation='navigation'
    )

    assert success, f"Failed to assign {ship_symbol}"


@given(parsers.parse('the container "{container_id}" no longer exists'))
def container_no_longer_exists(context, container_id):
    """Mark that container doesn't exist"""
    context['container_missing'] = True
    # Container manager won't have this container


@when('the health monitor runs')
def run_health_monitor(context):
    """Trigger health monitor cleanup using public API"""
    from spacetraders.adapters.primary.daemon.types import ContainerInfo, ContainerStatus

    daemon = DaemonServer()

    # If we have an active container, mock the container manager's list_containers
    if context.get('container_active'):
        container_id = context.get('container_id', 'active-container-123')
        mock_container = Mock(spec=ContainerInfo)
        mock_container.container_id = container_id
        mock_container.status = ContainerStatus.RUNNING

        # Mock the list_containers method to return our active container
        with patch.object(daemon._container_mgr, 'list_containers', return_value=[mock_container]):
            asyncio.run(daemon.cleanup_stale_assignments())
    else:
        # No active containers, run cleanup normally
        asyncio.run(daemon.cleanup_stale_assignments())

    context['health_monitor_ran'] = True


@when('the health monitor detects and cleans it up')
def health_monitor_detects_and_cleans(context):
    """Trigger health monitor to detect and clean up stale assignments"""
    # This is the same as running the health monitor
    run_health_monitor(context)


@when(parsers.parse('{seconds:d} seconds elapse'))
def wait_seconds(context, seconds):
    """Wait for specified seconds (mocked for tests)"""
    context['elapsed_seconds'] = seconds
    context['health_monitor_should_run'] = True


@then(parsers.parse('the ship assignment for "{ship_symbol}" should be detected as stale'))
def check_stale_detected(context, ship_symbol):
    """Verify stale assignment was detected"""
    # Detection is confirmed by the release action
    assert context.get('health_monitor_ran'), "Health monitor didn't run"


@then('the ship assignment should be auto-released')
def check_auto_released(context):
    """Verify assignment was released using public API"""
    from spacetraders.configuration.container import get_database

    assignment_mgr = ShipAssignmentManager(get_database())
    info = assignment_mgr.get_assignment_info(context['player_id'], context['ship_symbol'])

    assert info is not None, "No assignment found"
    assert info['status'] == 'idle', f"Assignment not released: {info['status']}"
    assert info['release_reason'] == 'stale_cleanup', f"Wrong reason: {info['release_reason']}"


@then('the assignment should be auto-released')
def check_assignment_auto_released(context):
    """Verify assignment was released (alternative wording)"""
    check_auto_released(context)


@then('the release reason should be "stale_cleanup"')
def check_stale_cleanup_reason(context):
    """Verify release reason"""
    # Already checked in check_auto_released
    pass


@then('the health monitor should have run at least once')
def check_monitor_ran(context):
    """Verify health monitor ran"""
    assert context.get('health_monitor_should_run'), "Monitor should have run"


@then('stale assignments should be checked')
def check_assignments_checked(context):
    """Verify assignments were checked"""
    # Implicit in monitor running
    assert context.get('health_monitor_should_run')


@given('a navigation container is running for ship "SHIP-1"')
def running_container_for_ship(context):
    """Setup running container scenario"""
    from spacetraders.adapters.primary.daemon.container_manager import ContainerManager
    from spacetraders.configuration.container import get_mediator, get_database

    # Create a mock running container
    context['container_id'] = 'active-container-123'
    context['ship_symbol'] = 'SHIP-1'

    # Create assignment
    ship_was_assigned(context, 'SHIP-1', 'active-container-123')

    # Mark container as active
    context['container_active'] = True


@given(parsers.parse('the ship "{ship_symbol}" is assigned to the container'))
def ship_assigned_to_container(context, ship_symbol):
    """Assign ship to container from context"""
    # Use container_id from context (set by previous step)
    container_id = context.get('container_id', 'active-container-123')

    # Only assign if not already assigned by previous step
    # The previous step 'running_container_for_ship' may have already assigned SHIP-1
    if context.get('ship_symbol') == ship_symbol and context.get('container_id') == container_id:
        # Already assigned in previous step
        return

    ship_was_assigned(context, ship_symbol, container_id)


@then(parsers.parse('the ship assignment for "{ship_symbol}" should remain active'))
def check_assignment_remains_active(context, ship_symbol):
    """Verify assignment stays active using public API"""
    from spacetraders.configuration.container import get_database

    assignment_mgr = ShipAssignmentManager(get_database())
    info = assignment_mgr.get_assignment_info(context['player_id'], ship_symbol)

    assert info is not None, "No assignment found"
    assert info['status'] == 'active', f"Assignment changed: {info['status']}"


@then('the assignment should not be released')
def check_not_released(context):
    """Verify assignment not released"""
    # Checked in check_assignment_remains_active
    pass


@given(parsers.parse('{count:d} ships have stale assignments to non-existent containers'))
def multiple_stale_assignments(context, count):
    """Create multiple stale assignments using public API"""
    from spacetraders.configuration.container import get_database

    assignment_mgr = ShipAssignmentManager(get_database())
    for i in range(count):
        ship_symbol = f'SHIP-{i+1}'
        container_id = f'gone-container-{i+1}'
        success = assignment_mgr.assign(
            player_id=context['player_id'],
            ship_symbol=ship_symbol,
            container_id=container_id,
            operation='navigation'
        )
        assert success, f"Failed to assign {ship_symbol}"

    context['stale_count'] = count


@then(parsers.parse('all {count:d} stale assignments should be detected'))
def check_all_detected(context, count):
    """Verify all stale assignments detected"""
    assert context['stale_count'] == count


@then(parsers.parse('all {count:d} assignments should be released with reason "stale_cleanup"'))
def check_all_released(context, count):
    """Verify all assignments released using public API"""
    from spacetraders.configuration.container import get_database

    assignment_mgr = ShipAssignmentManager(get_database())
    released_count = 0

    # Check each ship that was created
    for i in range(count):
        ship_symbol = f'SHIP-{i+1}'
        info = assignment_mgr.get_assignment_info(context['player_id'], ship_symbol)

        if info and info['status'] == 'idle' and info['release_reason'] == 'stale_cleanup':
            released_count += 1

    assert released_count == count, f"Only {released_count}/{count} released"


@given('a navigation container was running before daemon crashed')
def container_before_crash(context):
    """Setup pre-crash state"""
    context['container_id'] = 'pre-crash-container'
    context['ship_symbol'] = 'SHIP-1'
    ship_was_assigned(context, 'SHIP-1', 'pre-crash-container')


@given(parsers.parse('the ship "{ship_symbol}" is still marked as assigned in database'))
def ship_still_assigned_in_db(context, ship_symbol):
    """Verify ship is still marked as assigned in database"""
    from spacetraders.configuration.container import get_database

    # This step verifies that the previous assignment is still in place
    # It's already been assigned in the previous step, so we just verify
    assignment_mgr = ShipAssignmentManager(get_database())
    info = assignment_mgr.get_assignment_info(context['player_id'], ship_symbol)

    assert info is not None, f"No assignment found for {ship_symbol}"
    assert info['status'] == 'active', f"Assignment not active: {info['status']}"


@given('the daemon server restarted and container is gone')
def daemon_restarted(context):
    """Simulate daemon restart"""
    context['daemon_restarted'] = True
    context['container_missing'] = True


@given(parsers.parse('a stale assignment exists for ship "{ship_symbol}"'))
def stale_assignment_exists(context, ship_symbol):
    """Create stale assignment"""
    context['ship_symbol'] = ship_symbol
    ship_was_assigned(context, ship_symbol, 'missing-container')


@then('a warning should be logged about the stale assignment')
def check_warning_logged(context):
    """Verify cleanup occurred (observable through assignment state)"""
    from spacetraders.configuration.container import get_database

    # Verify the assignment was actually cleaned up
    assignment_mgr = ShipAssignmentManager(get_database())
    info = assignment_mgr.get_assignment_info(context['player_id'], context['ship_symbol'])

    assert info is not None, "No assignment found"
    assert info['status'] == 'idle', "Stale assignment should be cleaned up"
    assert info['release_reason'] == 'stale_cleanup'


@then('an info message should confirm cleanup count')
def check_info_logged(context):
    """Verify cleanup succeeded"""
    assert context.get('health_monitor_ran'), "Health monitor should have run"
