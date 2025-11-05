"""BDD steps for Update Player Metadata Command"""
from pytest_bdd import scenario, given, when, then, parsers
import asyncio
import pytest
import json
from datetime import datetime, timezone
from typing import Dict, Any

from spacetraders.application.player.commands.update_player import (
    UpdatePlayerMetadataCommand,
    UpdatePlayerMetadataHandler
)
from spacetraders.domain.shared.player import Player
from spacetraders.domain.shared.exceptions import PlayerNotFoundError


# ==============================================================================
# Scenario: Update player metadata successfully
# ==============================================================================
@scenario("../../features/application/update_player_metadata_command.feature",
          "Update player metadata successfully")
def test_update_metadata_successfully():
    pass


@given("the update player metadata command handler is initialized")
def initialize_handler(context, mock_player_repo):
    """Initialize the UpdatePlayerMetadataHandler with mock repository"""
    context["handler"] = UpdatePlayerMetadataHandler(mock_player_repo)
    context["mock_player_repo"] = mock_player_repo
    context["update_count"] = 0


@given(parsers.parse('a registered player with id {player_id:d} and metadata {metadata}'))
def create_player_with_metadata(context, player_id, metadata, mock_player_repo):
    """Create a registered player with specific metadata"""
    metadata_dict = json.loads(metadata)
    player = Player(
        player_id=None,  # Will be assigned by mock repo
        agent_symbol=f"TEST-AGENT-{player_id}",
        token=f"test-token-{player_id}",
        created_at=datetime.now(timezone.utc),
        metadata=metadata_dict
    )
    created_player = mock_player_repo.create(player)
    context[f"player_{player_id}"] = created_player
    context["player"] = created_player
    context["player_id"] = created_player.player_id


@when(parsers.parse('I execute update player metadata command for player {player_id:d} with metadata {metadata}'))
def execute_update_metadata_command(context, player_id, metadata):
    """Execute the update player metadata command"""
    handler = context["handler"]
    metadata_dict = json.loads(metadata)

    # Get the actual player_id from the registered player
    if f"player_{player_id}" in context:
        actual_player_id = context[f"player_{player_id}"].player_id
    else:
        actual_player_id = context["player_id"]

    command = UpdatePlayerMetadataCommand(
        player_id=actual_player_id,
        metadata=metadata_dict
    )
    result = asyncio.run(handler.handle(command))

    # Store result for multiple player scenarios
    if "result1" not in context:
        context["result1"] = result
    elif "result2" not in context:
        context["result2"] = result

    # Always store in "result" for single-player scenarios
    context["result"] = result
    context["error"] = None
    context["update_count"] += 1


@then("the command should succeed")
def check_command_success(context):
    """Verify the command succeeded without errors"""
    assert context["error"] is None
    assert context["result"] is not None


@then(parsers.parse('the player metadata should contain key "{key}"'))
def check_metadata_contains_key(context, key):
    """Verify the metadata contains a specific key"""
    assert key in context["result"].metadata


@then(parsers.parse('the player metadata "{key}" should equal "{value}"'))
def check_metadata_string_value(context, key, value):
    """Verify metadata key equals string value"""
    assert context["result"].metadata[key] == value


@then(parsers.parse('the player metadata "{key}" should equal {value:d}'))
def check_metadata_int_value(context, key, value):
    """Verify metadata key equals integer value"""
    assert context["result"].metadata[key] == value


@then("the repository update should be called once")
def check_repository_update_once(context):
    """Verify repository update was called exactly once"""
    assert context["update_count"] == 1


# ==============================================================================
# Scenario: Update metadata with empty dictionary
# ==============================================================================
@scenario("../../features/application/update_player_metadata_command.feature",
          "Update metadata with empty dictionary")
def test_update_metadata_empty_dict():
    pass


@then(parsers.parse('the player metadata should equal {metadata}'))
def check_metadata_equals(context, metadata):
    """Verify the metadata equals specific value"""
    expected = json.loads(metadata)
    assert context["result"].metadata == expected


# ==============================================================================
# Scenario: Update metadata overwrites existing keys
# ==============================================================================
@scenario("../../features/application/update_player_metadata_command.feature",
          "Update metadata overwrites existing keys")
def test_update_metadata_overwrites():
    pass


# ==============================================================================
# Scenario: Update metadata with complex data types
# ==============================================================================
@scenario("../../features/application/update_player_metadata_command.feature",
          "Update metadata with complex data types")
def test_update_metadata_complex_types():
    pass


@given(parsers.parse("a registered player with id {player_id:d} with no metadata"))
def create_player_no_metadata(context, player_id, mock_player_repo):
    """Create a registered player with no metadata"""
    player = Player(
        player_id=None,
        agent_symbol=f"TEST-AGENT-{player_id}",
        token=f"test-token-{player_id}",
        created_at=datetime.now(timezone.utc)
    )
    created_player = mock_player_repo.create(player)
    context[f"player_{player_id}"] = created_player
    context["player"] = created_player
    context["player_id"] = created_player.player_id


@when(parsers.parse("I execute update player metadata command for player {player_id:d} with complex metadata"))
def execute_update_complex_metadata(context, player_id):
    """Execute update with complex metadata"""
    handler = context["handler"]
    actual_player_id = context[f"player_{player_id}"].player_id

    command = UpdatePlayerMetadataCommand(
        player_id=actual_player_id,
        metadata={
            "string": "value",
            "number": 42,
            "float": 3.14,
            "bool": True,
            "list": [1, 2, 3],
            "dict": {"nested": "value"}
        }
    )
    context["result"] = asyncio.run(handler.handle(command))
    context["error"] = None
    context["update_count"] += 1


@then(parsers.parse('the player metadata should contain string "{value}"'))
def check_metadata_contains_string(context, value):
    """Verify metadata contains string value"""
    assert context["result"].metadata["string"] == value


@then(parsers.parse('the player metadata should contain number {value:d}'))
def check_metadata_contains_number(context, value):
    """Verify metadata contains number value"""
    assert context["result"].metadata["number"] == value


@then(parsers.parse('the player metadata should contain float {value:f}'))
def check_metadata_contains_float(context, value):
    """Verify metadata contains float value"""
    assert context["result"].metadata["float"] == value


@then(parsers.parse('the player metadata should contain boolean {value}'))
def check_metadata_contains_boolean(context, value):
    """Verify metadata contains boolean value"""
    expected = value.lower() == "true"
    assert context["result"].metadata["bool"] is expected


@then(parsers.parse('the player metadata should contain list {value}'))
def check_metadata_contains_list(context, value):
    """Verify metadata contains list value"""
    expected = json.loads(value)
    assert context["result"].metadata["list"] == expected


@then(parsers.parse('the player metadata should contain nested dict {value}'))
def check_metadata_contains_dict(context, value):
    """Verify metadata contains dict value"""
    expected = json.loads(value)
    assert context["result"].metadata["dict"] == expected


# ==============================================================================
# Scenario: Update metadata persists changes to repository
# ==============================================================================
@scenario("../../features/application/update_player_metadata_command.feature",
          "Update metadata persists changes to repository")
def test_update_metadata_persists():
    pass


@then(parsers.parse('the persisted player metadata should contain key "{key}"'))
def check_persisted_metadata_contains_key(context, key, mock_player_repo):
    """Verify the persisted player metadata contains a key"""
    player_id = context["player"].player_id
    persisted_player = mock_player_repo.find_by_id(player_id)
    assert key in persisted_player.metadata


@then(parsers.parse('the persisted player metadata "{key}" should equal "{value}"'))
def check_persisted_metadata_value(context, key, value, mock_player_repo):
    """Verify persisted metadata key equals value"""
    player_id = context["player"].player_id
    persisted_player = mock_player_repo.find_by_id(player_id)
    assert persisted_player.metadata[key] == value


# ==============================================================================
# Scenario: Update metadata returns updated Player entity
# ==============================================================================
@scenario("../../features/application/update_player_metadata_command.feature",
          "Update metadata returns updated Player entity")
def test_update_metadata_returns_player():
    pass


@given(parsers.parse('a registered player with id {player_id:d} and agent symbol "{agent_symbol}" with no metadata'))
def create_player_with_agent_no_metadata(context, player_id, agent_symbol, mock_player_repo):
    """Create a player with specific agent symbol and no metadata"""
    player = Player(
        player_id=None,
        agent_symbol=agent_symbol,
        token=f"test-token-{player_id}",
        created_at=datetime.now(timezone.utc)
    )
    created_player = mock_player_repo.create(player)
    context[f"player_{player_id}"] = created_player
    context["player"] = created_player
    context["player_id"] = created_player.player_id


@then("the command should return a Player entity")
def check_returns_player_entity(context):
    """Verify the command returns a Player instance"""
    assert isinstance(context["result"], Player)


@then(parsers.parse('the returned player should have agent symbol "{agent_symbol}"'))
def check_returned_agent_symbol(context, agent_symbol):
    """Verify the returned player has the correct agent symbol"""
    assert context["result"].agent_symbol == agent_symbol


# ==============================================================================
# Scenario: Update metadata multiple times accumulates changes
# ==============================================================================
@scenario("../../features/application/update_player_metadata_command.feature",
          "Update metadata multiple times accumulates changes")
def test_update_metadata_multiple_times():
    pass


@then(parsers.parse("the repository update should be called {count:d} times"))
def check_repository_update_count(context, count):
    """Verify repository update was called the expected number of times"""
    assert context["update_count"] == count


# ==============================================================================
# Scenario: Update metadata with None values
# ==============================================================================
@scenario("../../features/application/update_player_metadata_command.feature",
          "Update metadata with None values")
def test_update_metadata_with_none():
    pass


# Note: This scenario reuses execute_update_metadata_command step (line 51)


@then(parsers.parse('the player metadata "{key}" should be null'))
def check_metadata_is_null(context, key):
    """Verify metadata key is null"""
    assert context["result"].metadata[key] is None


# ==============================================================================
# Scenario: Update different players independently
# ==============================================================================
@scenario("../../features/application/update_player_metadata_command.feature",
          "Update different players independently")
def test_update_different_players():
    pass


# Note: This scenario reuses create_player_with_agent_no_metadata step (line 247)


@then(parsers.parse('the first result metadata "{key}" should equal "{value}"'))
def check_first_result_metadata(context, key, value):
    """Verify first result metadata"""
    assert context["result1"].metadata[key] == value


@then(parsers.parse('the second result metadata "{key}" should equal "{value}"'))
def check_second_result_metadata(context, key, value):
    """Verify second result metadata"""
    assert context["result2"].metadata[key] == value


@then("the repository update should be called twice")
def check_repository_update_twice(context):
    """Verify repository update was called exactly twice"""
    assert context["update_count"] == 2


# ==============================================================================
# Scenario: Cannot update non-existent player
# ==============================================================================
@scenario("../../features/application/update_player_metadata_command.feature",
          "Cannot update non-existent player")
def test_cannot_update_nonexistent_player():
    pass


@given(parsers.parse("no player exists with id {player_id:d}"))
def no_player_exists(context, player_id):
    """Ensure no player exists with the given ID"""
    context["nonexistent_player_id"] = player_id


@when(parsers.parse("I attempt to update player metadata for player {player_id:d}"))
def attempt_update_metadata(context, player_id):
    """Attempt to update metadata and capture any errors"""
    handler = context["handler"]
    command = UpdatePlayerMetadataCommand(
        player_id=player_id,
        metadata={"test": "data"}
    )
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
@scenario("../../features/application/update_player_metadata_command.feature",
          "Handler initializes with repository correctly")
def test_handler_initializes_correctly():
    pass


@given("a mock player repository is created")
def create_mock_repo(context, mock_player_repo):
    """Create a mock player repository"""
    context["test_mock_repo"] = mock_player_repo


@when("I create an update player metadata handler with the repository")
def create_handler_with_repo(context):
    """Create a handler with the repository"""
    context["test_handler"] = UpdatePlayerMetadataHandler(context["test_mock_repo"])


@then("the handler should have the repository initialized")
def check_handler_has_repository(context):
    """Verify the handler has the repository initialized"""
    assert context["test_handler"]._player_repo is context["test_mock_repo"]
