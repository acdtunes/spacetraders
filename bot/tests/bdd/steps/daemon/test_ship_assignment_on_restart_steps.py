"""Step definitions for ship assignment tracking during container restart"""
import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from datetime import datetime, timezone
from configuration.container import (
    get_player_repository,
    get_ship_assignment_repository
)

# Load all scenarios from the feature file
scenarios('../../features/daemon/ship_assignment_on_restart.feature')


@pytest.fixture
def context():
    """Shared test context"""
    return type('Context', (), {})()


@given('a player exists with id 1')
def player_exists(context):
    """Create test player"""
    from domain.shared.player import Player

    player_repo = get_player_repository()

    # Check if player already exists
    existing = player_repo.find_by_id(1)
    if existing:
        context.player = existing
        return

    # Create new player
    player = Player(
        player_id=None,
        agent_symbol="TEST_AGENT",
        token="test_token_123",
        created_at=datetime.now(timezone.utc),
        credits=150000,
        metadata={}
    )
    context.player = player_repo.create(player)


@given(parsers.parse('a ship "{ship_symbol}" exists for player {player_id:d}'))
def ship_exists(context, ship_symbol, player_id):
    """Mock ship existence"""
    # Ships are API-only, so we just store the symbol in context for validation
    context.ship_symbol = ship_symbol
    context.player_id = player_id


@given(parsers.parse('ship "{ship_symbol}" is assigned to container "{container_id}" with operation "{operation}"'))
def ship_assigned_to_container(context, ship_symbol, container_id, operation):
    """Assign ship to container"""
    assignment_repo = get_ship_assignment_repository()

    success = assignment_repo.assign(
        player_id=context.player_id,
        ship_symbol=ship_symbol,
        container_id=container_id,
        operation=operation
    )

    assert success, f"Failed to assign {ship_symbol} to {container_id}"

    # Store for later verification
    context.old_container_id = container_id
    context.ship_symbol = ship_symbol


@given('the assignment status is "active"')
def assignment_status_active(context):
    """Verify assignment is active"""
    assignment_repo = get_ship_assignment_repository()

    info = assignment_repo.get_assignment_info(
        player_id=context.player_id,
        ship_symbol=context.ship_symbol
    )

    assert info is not None, "Assignment not found"
    assert info['status'] == 'active', f"Expected active status, got {info['status']}"


@given(parsers.parse('the assignment was released with reason "{reason}"'))
def assignment_released(context, reason):
    """Release the assignment with a reason"""
    assignment_repo = get_ship_assignment_repository()

    assignment_repo.release(
        player_id=context.player_id,
        ship_symbol=context.ship_symbol,
        reason=reason
    )


@when(parsers.parse('the assignment is reassigned from "{old_container_id}" to "{new_container_id}"'))
def reassign_assignment(context, old_container_id, new_container_id):
    """Reassign ship from old container to new container"""
    assignment_repo = get_ship_assignment_repository()

    # Store the old assigned_at for comparison
    old_info = assignment_repo.get_assignment_info(
        player_id=context.player_id,
        ship_symbol=context.ship_symbol
    )
    context.old_assigned_at = old_info['assigned_at'] if old_info else None

    # Perform reassignment
    success = assignment_repo.reassign(
        player_id=context.player_id,
        ship_symbol=context.ship_symbol,
        old_container_id=old_container_id,
        new_container_id=new_container_id
    )

    context.reassignment_success = success
    context.new_container_id = new_container_id


@when(parsers.parse('I attempt to reassign ship "{ship_symbol}" from "{old_container_id}" to "{new_container_id}"'))
def attempt_reassign(context, ship_symbol, old_container_id, new_container_id):
    """Attempt to reassign ship"""
    assignment_repo = get_ship_assignment_repository()

    success = assignment_repo.reassign(
        player_id=context.player_id,
        ship_symbol=ship_symbol,
        old_container_id=old_container_id,
        new_container_id=new_container_id
    )

    context.reassignment_success = success
    context.attempted_new_container = new_container_id


@then(parsers.parse('ship "{ship_symbol}" should be assigned to container "{container_id}"'))
def verify_ship_assigned_to_container(context, ship_symbol, container_id):
    """Verify ship is assigned to the specified container"""
    assignment_repo = get_ship_assignment_repository()

    info = assignment_repo.get_assignment_info(
        player_id=context.player_id,
        ship_symbol=ship_symbol
    )

    assert info is not None, f"No assignment found for {ship_symbol}"
    assert info['container_id'] == container_id, \
        f"Expected container {container_id}, got {info['container_id']}"


@then('the assignment status should be "active"')
def verify_assignment_active(context):
    """Verify assignment status is active"""
    assignment_repo = get_ship_assignment_repository()

    info = assignment_repo.get_assignment_info(
        player_id=context.player_id,
        ship_symbol=context.ship_symbol
    )

    assert info is not None, "Assignment not found"
    assert info['status'] == 'active', f"Expected active status, got {info['status']}"


@then('the assignment should have a new assigned_at timestamp')
def verify_new_timestamp(context):
    """Verify assigned_at timestamp was updated"""
    assignment_repo = get_ship_assignment_repository()

    info = assignment_repo.get_assignment_info(
        player_id=context.player_id,
        ship_symbol=context.ship_symbol
    )

    assert info is not None, "Assignment not found"

    new_assigned_at = info['assigned_at']
    assert new_assigned_at is not None, "assigned_at should not be None"

    # If we had an old timestamp, verify it changed
    if context.old_assigned_at:
        assert new_assigned_at > context.old_assigned_at, \
            f"Timestamp should be updated, old: {context.old_assigned_at}, new: {new_assigned_at}"


@then('the assignment should have no release reason')
def verify_no_release_reason(context):
    """Verify release_reason is cleared"""
    assignment_repo = get_ship_assignment_repository()

    info = assignment_repo.get_assignment_info(
        player_id=context.player_id,
        ship_symbol=context.ship_symbol
    )

    assert info is not None, "Assignment not found"
    assert info['release_reason'] is None, \
        f"Expected no release reason, got {info['release_reason']}"


@then('the assignment should have no released_at timestamp')
def verify_no_released_at(context):
    """Verify released_at is cleared"""
    assignment_repo = get_ship_assignment_repository()

    info = assignment_repo.get_assignment_info(
        player_id=context.player_id,
        ship_symbol=context.ship_symbol
    )

    assert info is not None, "Assignment not found"
    assert info['released_at'] is None, \
        f"Expected no released_at, got {info['released_at']}"


@then('the reassignment should fail')
def verify_reassignment_failed(context):
    """Verify reassignment failed"""
    assert not context.reassignment_success, "Reassignment should have failed but succeeded"


@then(parsers.parse('ship "{ship_symbol}" should still be assigned to container "{container_id}"'))
def verify_ship_still_assigned(context, ship_symbol, container_id):
    """Verify ship is still assigned to original container"""
    assignment_repo = get_ship_assignment_repository()

    info = assignment_repo.get_assignment_info(
        player_id=context.player_id,
        ship_symbol=ship_symbol
    )

    assert info is not None, f"No assignment found for {ship_symbol}"
    assert info['container_id'] == container_id, \
        f"Expected container {container_id}, got {info['container_id']}"
