"""BDD steps for verifying ship assignment lock release functionality"""
import pytest
from pytest_bdd import scenarios, when, then, parsers

from configuration.container import get_ship_assignment_repository

scenarios('../../features/daemon/ship_lock_release_verification.feature')


@pytest.fixture
def assignment_mgr():
    """Fixture for assignment repository"""
    return get_ship_assignment_repository()


@when(parsers.parse('I assign ship "{ship_symbol}" to container "{container_id}"'))
def assign_ship(context, assignment_mgr, ship_symbol, container_id):
    """Assign ship to container"""
    player_id = context['player_id']
    success = assignment_mgr.assign(player_id, ship_symbol, container_id, "test")
    context['assignment_success'] = success
    context['ship_symbol'] = ship_symbol
    context['container_id'] = container_id


@when(parsers.parse('I release the ship "{ship_symbol}" assignment with reason "{reason}"'))
def release_ship(context, assignment_mgr, ship_symbol, reason):
    """Release ship assignment"""
    player_id = context['player_id']
    assignment_mgr.release(player_id, ship_symbol, reason)


@then(parsers.parse('the ship "{ship_symbol}" should have assignment status "{expected_status}"'))
def verify_assignment_status(context, assignment_mgr, ship_symbol, expected_status):
    """Verify ship assignment status"""
    player_id = context['player_id']
    assignment_info = assignment_mgr.get_assignment_info(player_id, ship_symbol)

    assert assignment_info is not None, f"No assignment record found for {ship_symbol}"
    actual_status = assignment_info['status']
    assert actual_status == expected_status, \
        f"Expected status '{expected_status}' but got '{actual_status}'"


@then(parsers.parse('the ship "{ship_symbol}" should be available for new assignments'))
def verify_ship_available(context, assignment_mgr, ship_symbol):
    """Verify ship is available"""
    player_id = context['player_id']
    is_available = assignment_mgr.check_available(player_id, ship_symbol)
    assert is_available, f"Ship {ship_symbol} is not available"


@then(parsers.parse('the release reason should be "{expected_reason}"'))
def verify_release_reason(context, assignment_mgr, expected_reason):
    """Verify release reason"""
    player_id = context['player_id']
    ship_symbol = context['ship_symbol']
    assignment_info = assignment_mgr.get_assignment_info(player_id, ship_symbol)

    assert assignment_info is not None, "No assignment record found"
    actual_reason = assignment_info['release_reason']
    assert actual_reason == expected_reason, \
        f"Expected reason '{expected_reason}' but got '{actual_reason}'"


@then(parsers.parse('I can assign ship "{ship_symbol}" to container "{container_id}"'))
def can_assign_ship(context, assignment_mgr, ship_symbol, container_id):
    """Verify we can assign ship to new container"""
    player_id = context['player_id']
    success = assignment_mgr.assign(player_id, ship_symbol, container_id, "test")
    assert success, f"Failed to assign ship {ship_symbol} to {container_id}"
    context['container_id'] = container_id


@then(parsers.parse('I can release ship "{ship_symbol}" assignment with reason "{reason}"'))
def can_release_ship(context, assignment_mgr, ship_symbol, reason):
    """Verify we can release ship assignment"""
    player_id = context['player_id']
    # This should not raise an exception
    assignment_mgr.release(player_id, ship_symbol, reason)


@then(parsers.parse('assigning ship "{ship_symbol}" to container "{container_id}" should fail'))
def assignment_should_fail(context, assignment_mgr, ship_symbol, container_id):
    """Verify assignment fails when ship is already assigned"""
    player_id = context['player_id']
    success = assignment_mgr.assign(player_id, ship_symbol, container_id, "test")
    assert not success, f"Assignment should have failed but succeeded"


@then(parsers.parse('the ship "{ship_symbol}" should still be assigned to "{expected_container}"'))
def verify_assigned_to_container(context, assignment_mgr, ship_symbol, expected_container):
    """Verify ship is still assigned to expected container"""
    player_id = context['player_id']
    assignment_info = assignment_mgr.get_assignment_info(player_id, ship_symbol)

    assert assignment_info is not None, "No assignment record found"
    actual_container = assignment_info['container_id']
    assert actual_container == expected_container, \
        f"Expected container '{expected_container}' but got '{actual_container}'"
    assert assignment_info['status'] == 'active', \
        f"Expected status 'active' but got '{assignment_info['status']}'"
