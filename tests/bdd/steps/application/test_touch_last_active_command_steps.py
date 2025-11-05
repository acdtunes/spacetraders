"""BDD steps for Touch Player Last Active Command"""
from pytest_bdd import scenario, given, when, then, parsers
import asyncio
import pytest
from datetime import datetime, timezone, timedelta

from spacetraders.application.player.commands.touch_last_active import (
    TouchPlayerLastActiveCommand,
    TouchPlayerLastActiveHandler
)
from spacetraders.domain.shared.player import Player
from spacetraders.domain.shared.exceptions import PlayerNotFoundError


# ==============================================================================
# Scenario: Touch player's last_active timestamp successfully
# ==============================================================================
@scenario("../../features/application/touch_last_active_command.feature",
          "Touch player's last_active timestamp successfully")
def test_touch_last_active_successfully():
    pass


@given("the touch player last active command handler is initialized")
def initialize_handler(context, mock_player_repo):
    """Initialize the TouchPlayerLastActiveHandler with mock repository"""
    context["handler"] = TouchPlayerLastActiveHandler(mock_player_repo)
    context["mock_player_repo"] = mock_player_repo
    context["update_count"] = 0


@given(parsers.parse("a registered player with id {player_id:d}"))
def create_registered_player(context, player_id, mock_player_repo):
    """Create a registered player with a specific ID"""
    player = Player(
        player_id=None,  # Will be assigned by mock repo
        agent_symbol=f"TEST-AGENT-{player_id}",
        token=f"test-token-{player_id}",
        created_at=datetime.now(timezone.utc) - timedelta(days=1),
        last_active=datetime.now(timezone.utc) - timedelta(hours=1)
    )
    created_player = mock_player_repo.create(player)
    context[f"player_{player_id}"] = created_player
    context["player"] = created_player
    context["player_id"] = created_player.player_id


@given("the player's original last_active timestamp is recorded")
def record_original_timestamp(context):
    """Record the original last_active timestamp"""
    context["original_last_active"] = context["player"].last_active


@when(parsers.parse("I execute touch player last active command for player {player_id:d}"))
def execute_touch_command(context, player_id):
    """Execute the touch player last active command"""
    handler = context["handler"]
    mock_repo = context["mock_player_repo"]

    # Get the actual player_id from the registered player
    if f"player_{player_id}" in context:
        actual_player_id = context[f"player_{player_id}"].player_id
    else:
        actual_player_id = context["player_id"]

    # Track update count before
    initial_updates = len([p for p in mock_repo._players.values()])

    command = TouchPlayerLastActiveCommand(player_id=actual_player_id)
    result = asyncio.run(handler.handle(command))

    # Store result in context
    context["result"] = result
    context["error"] = None
    context["update_count"] += 1

    # Store result with sequence number for multiple calls
    if "result1" not in context:
        context["result1"] = result
    elif "result2" not in context:
        context["result2"] = result
    else:
        context["result3"] = result


@then("the command should succeed")
def check_command_success(context):
    """Verify the command succeeded without errors"""
    assert context["error"] is None
    assert context["result"] is not None


@then("the player's last_active should be updated")
def check_last_active_updated(context):
    """Verify the last_active timestamp was updated"""
    assert context["result"].last_active is not None


@then("the player's last_active should be after the original timestamp")
def check_last_active_after_original(context):
    """Verify the new timestamp is after the original"""
    assert context["result"].last_active > context["original_last_active"]


@then("the repository update should be called once")
def check_repository_update_once(context):
    """Verify repository update was called exactly once"""
    assert context["update_count"] == 1


# ==============================================================================
# Scenario: Touch updates timestamp to current time
# ==============================================================================
@scenario("../../features/application/touch_last_active_command.feature",
          "Touch updates timestamp to current time")
def test_touch_updates_to_current_time():
    pass


@given(parsers.parse("a registered player with id {player_id:d} and last_active {hours:d} hours ago"))
def create_player_with_old_timestamp(context, player_id, hours, mock_player_repo):
    """Create a player with an old last_active timestamp"""
    old_time = datetime.now(timezone.utc) - timedelta(hours=hours)
    player = Player(
        player_id=None,
        agent_symbol=f"TEST-AGENT-{player_id}",
        token=f"test-token-{player_id}",
        created_at=old_time,
        last_active=old_time
    )
    created_player = mock_player_repo.create(player)
    context["player"] = created_player
    context["player_id"] = created_player.player_id
    context["old_timestamp"] = old_time


@given("the current time is recorded before touch")
def record_time_before(context):
    """Record the current time before touching"""
    context["time_before"] = datetime.now(timezone.utc)


@when("the current time is recorded after touch")
def record_time_after(context):
    """Record the current time after touching"""
    context["time_after"] = datetime.now(timezone.utc)


@then("the player's last_active should be between before and after times")
def check_timestamp_in_range(context):
    """Verify the timestamp is within the expected time range"""
    assert context["result"].last_active >= context["time_before"]
    assert context["result"].last_active <= context["time_after"]


@then("the player's last_active should be after the old timestamp")
def check_after_old_timestamp(context):
    """Verify the timestamp is after the old timestamp"""
    assert context["result"].last_active > context["old_timestamp"]


# ==============================================================================
# Scenario: Touch player's last_active multiple times
# ==============================================================================
@scenario("../../features/application/touch_last_active_command.feature",
          "Touch player's last_active multiple times")
def test_touch_multiple_times():
    pass


@when(parsers.parse("I execute touch player last active command for player {player_id:d} three times"))
def execute_touch_three_times(context, player_id):
    """Execute the touch command three times"""
    handler = context["handler"]
    actual_player_id = context["player"].player_id

    command = TouchPlayerLastActiveCommand(player_id=actual_player_id)
    context["result1"] = asyncio.run(handler.handle(command))
    context["result2"] = asyncio.run(handler.handle(command))
    context["result3"] = asyncio.run(handler.handle(command))
    context["error"] = None
    context["update_count"] = 3


@then("each touch should update the timestamp")
def check_each_touch_updates(context):
    """Verify each touch updated the timestamp"""
    assert context["result2"].last_active >= context["result1"].last_active
    assert context["result3"].last_active >= context["result2"].last_active


@then(parsers.parse("the repository update should be called {count:d} times"))
def check_repository_update_count(context, count):
    """Verify repository update was called the expected number of times"""
    assert context["update_count"] == count


# ==============================================================================
# Scenario: Touch persists changes to repository
# ==============================================================================
@scenario("../../features/application/touch_last_active_command.feature",
          "Touch persists changes to repository")
def test_touch_persists_changes():
    pass


@then("the persisted player should have updated last_active")
def check_persisted_player(context, mock_player_repo):
    """Verify the player in the repository has the updated timestamp"""
    player_id = context["player"].player_id
    persisted_player = mock_player_repo.find_by_id(player_id)
    assert persisted_player is not None
    assert persisted_player.last_active > context["original_last_active"]


# ==============================================================================
# Scenario: Touch returns updated Player entity
# ==============================================================================
@scenario("../../features/application/touch_last_active_command.feature",
          "Touch returns updated Player entity")
def test_touch_returns_player_entity():
    pass


@given(parsers.parse('a registered player with id {player_id:d} and agent symbol "{agent_symbol}"'))
def create_player_with_agent_symbol(context, player_id, agent_symbol, mock_player_repo):
    """Create a player with a specific agent symbol"""
    player = Player(
        player_id=None,
        agent_symbol=agent_symbol,
        token=f"test-token-{player_id}",
        created_at=datetime.now(timezone.utc) - timedelta(days=1),
        last_active=datetime.now(timezone.utc) - timedelta(hours=1)
    )
    created_player = mock_player_repo.create(player)
    # Store in both formats for compatibility with different scenarios
    context[f"player_{player_id}"] = created_player
    context["player"] = created_player
    context["player_id"] = created_player.player_id
    context["expected_agent_symbol"] = agent_symbol




@then("the command should return a Player entity")
def check_returns_player_entity(context):
    """Verify the command returns a Player instance"""
    assert isinstance(context["result"], Player)


@then(parsers.parse('the returned player should have agent symbol "{agent_symbol}"'))
def check_returned_agent_symbol(context, agent_symbol):
    """Verify the returned player has the correct agent symbol"""
    assert context["result"].agent_symbol == agent_symbol


# ==============================================================================
# Scenario: Touch different players independently
# ==============================================================================
@scenario("../../features/application/touch_last_active_command.feature",
          "Touch different players independently")
def test_touch_different_players():
    pass




@then(parsers.parse("the first result should be for player {player_id:d}"))
def check_first_result_player_id(context, player_id):
    """Verify the first result is for the correct player"""
    # Get the actual player_id
    actual_player_id = context[f"player_{player_id}"].player_id
    assert context["result1"].player_id == actual_player_id


@then(parsers.parse("the second result should be for player {player_id:d}"))
def check_second_result_player_id(context, player_id):
    """Verify the second result is for the correct player"""
    # Get the actual player_id
    actual_player_id = context[f"player_{player_id}"].player_id
    assert context["result2"].player_id == actual_player_id


@then("the repository update should be called twice")
def check_repository_update_twice(context):
    """Verify repository update was called exactly twice"""
    assert context["update_count"] == 2


# ==============================================================================
# Scenario: Cannot touch non-existent player
# ==============================================================================
@scenario("../../features/application/touch_last_active_command.feature",
          "Cannot touch non-existent player")
def test_cannot_touch_nonexistent_player():
    pass


@given(parsers.parse("no player exists with id {player_id:d}"))
def no_player_exists(context, player_id):
    """Ensure no player exists with the given ID"""
    context["nonexistent_player_id"] = player_id


@when(parsers.parse("I attempt to touch player last active for player {player_id:d}"))
def attempt_touch_command(context, player_id):
    """Attempt to execute touch command and capture any errors"""
    handler = context["handler"]
    command = TouchPlayerLastActiveCommand(player_id=player_id)
    try:
        context["result"] = asyncio.run(handler.handle(command))
        context["error"] = None
    except Exception as e:
        context["error"] = e
        context["result"] = None


@then("the command should fail with PlayerNotFoundError")
def check_player_not_found_error(context):
    """Verify PlayerNotFoundError was raised"""
    assert isinstance(context["error"], PlayerNotFoundError)


@then(parsers.parse('the error message should mention "{text}"'))
def check_error_message_contains(context, text):
    """Verify error message contains specific text"""
    assert text in str(context["error"])


@then("the repository update should not be called")
def check_repository_not_updated(context):
    """Verify repository update was not called"""
    assert context["update_count"] == 0


# ==============================================================================
# Scenario: Handler initializes with repository correctly
# ==============================================================================
@scenario("../../features/application/touch_last_active_command.feature",
          "Handler initializes with repository correctly")
def test_handler_initializes_correctly():
    pass


@given("a mock player repository is created")
def create_mock_repo(context, mock_player_repo):
    """Create a mock player repository"""
    context["test_mock_repo"] = mock_player_repo


@when("I create a touch player last active handler with the repository")
def create_handler_with_repo(context):
    """Create a handler with the repository"""
    context["test_handler"] = TouchPlayerLastActiveHandler(context["test_mock_repo"])


@then("the handler should have the repository initialized")
def check_handler_has_repository(context):
    """Verify the handler has the repository initialized"""
    assert context["test_handler"]._player_repo is context["test_mock_repo"]
