"""BDD steps for Get Captain Logs Query"""
from pytest_bdd import scenario, given, when, then, parsers
import asyncio
import pytest
from datetime import datetime, timezone, timedelta
from typing import Dict, Any, List

from application.captain.queries.get_captain_logs import (
    GetCaptainLogsQuery,
    GetCaptainLogsHandler
)
from domain.shared.player import Player
from domain.shared.exceptions import PlayerNotFoundError


# ==============================================================================
# Background
# ==============================================================================
@given("the captain logging system is initialized")
def initialize_captain_logging(context, mock_player_repo, mock_captain_log_repo):
    """Initialize captain logging system with mock repositories"""
    context["get_captain_logs_handler"] = GetCaptainLogsHandler(
        mock_captain_log_repo,
        mock_player_repo
    )
    context["mock_captain_log_repo"] = mock_captain_log_repo
    context["mock_player_repo"] = mock_player_repo


# ==============================================================================
# Shared Given Steps
# ==============================================================================
@given(parsers.parse('a registered player with id {player_id:d} and agent symbol "{agent_symbol}"'))
def create_player_with_id_and_symbol(context, player_id, agent_symbol, mock_player_repo):
    """Create a registered player"""
    player = Player(
        player_id=None,
        agent_symbol=agent_symbol,
        token=f"test-token-{player_id}",
        created_at=datetime.now(timezone.utc) - timedelta(days=1),
        last_active=datetime.now(timezone.utc) - timedelta(hours=1)
    )
    created_player = mock_player_repo.create(player)
    context[f"player_{player_id}"] = created_player
    context["player"] = created_player
    context["player_id"] = created_player.player_id


@given(parsers.parse("the player has {count:d} captain logs"))
def create_captain_logs(context, count, mock_captain_log_repo):
    """Create captain logs for the player"""
    player_id = context["player_id"]
    for i in range(count):
        timestamp = (datetime.now(timezone.utc) - timedelta(minutes=count - i)).isoformat()
        mock_captain_log_repo.insert_log(
            player_id=player_id,
            timestamp=timestamp,
            entry_type="session_start",
            narrative=f"Log entry {i+1}"
        )


@given("the player has no captain logs")
def no_captain_logs(context):
    """Ensure player has no logs"""
    # Mock repo starts empty, so nothing to do
    pass


@given(parsers.parse('the player has a log with type "{entry_type}"'))
def create_log_with_type(context, entry_type, mock_captain_log_repo):
    """Create a single log with specific type"""
    player_id = context["player_id"]
    timestamp = datetime.now(timezone.utc).isoformat()
    mock_captain_log_repo.insert_log(
        player_id=player_id,
        timestamp=timestamp,
        entry_type=entry_type,
        narrative=f"Test log with type {entry_type}"
    )


@given(parsers.parse("the player has {count:d} logs with type \"{entry_type}\""))
def create_logs_with_type(context, count, entry_type, mock_captain_log_repo):
    """Create multiple logs with specific type"""
    player_id = context["player_id"]
    for i in range(count):
        timestamp = (datetime.now(timezone.utc) - timedelta(minutes=count - i)).isoformat()
        mock_captain_log_repo.insert_log(
            player_id=player_id,
            timestamp=timestamp,
            entry_type=entry_type,
            narrative=f"Log {i+1} with type {entry_type}"
        )


@given(parsers.parse('the player has logs with type "{entry_type}"'))
def create_some_logs_with_type(context, entry_type, mock_captain_log_repo):
    """Create some logs with specific type"""
    create_logs_with_type(context, 2, entry_type, mock_captain_log_repo)


@given("the player has a complete log with all fields")
def create_complete_log(context, mock_captain_log_repo):
    """Create a log with all fields populated"""
    import json
    player_id = context["player_id"]
    timestamp = datetime.now(timezone.utc).isoformat()
    mock_captain_log_repo.insert_log(
        player_id=player_id,
        timestamp=timestamp,
        entry_type="operation_completed",
        narrative="Complete test log",
        event_data=json.dumps({"ship": "TEST-1"}),
        tags=json.dumps(["test", "complete"]),
        fleet_snapshot=json.dumps({"credits": 10000})
    )


@given(parsers.parse("no player exists with id {player_id:d}"))
def no_player_exists(context, player_id):
    """Ensure no player exists"""
    context["nonexistent_player_id"] = player_id


# ==============================================================================
# When Steps
# ==============================================================================
@when(parsers.parse("I query captain logs for player {player_id:d}"))
def query_captain_logs(context, player_id):
    """Query captain logs with defaults"""
    handler = context["get_captain_logs_handler"]

    # Get actual player_id if it's a test player
    if f"player_{player_id}" in context:
        actual_player_id = context[f"player_{player_id}"].player_id
    else:
        actual_player_id = player_id

    query = GetCaptainLogsQuery(player_id=actual_player_id)

    try:
        result = asyncio.run(handler.handle(query))
        context["result"] = result
        context["error"] = None
    except Exception as e:
        context["error"] = e
        context["result"] = None


@when(parsers.parse("I query captain logs for player {player_id:d} with limit {limit:d}"))
def query_captain_logs_with_limit(context, player_id, limit):
    """Query captain logs with specific limit"""
    handler = context["get_captain_logs_handler"]

    if f"player_{player_id}" in context:
        actual_player_id = context[f"player_{player_id}"].player_id
    else:
        actual_player_id = player_id

    query = GetCaptainLogsQuery(player_id=actual_player_id, limit=limit)

    try:
        result = asyncio.run(handler.handle(query))
        context["result"] = result
        context["error"] = None
    except Exception as e:
        context["error"] = e
        context["result"] = None


@when(parsers.parse('I query captain logs for player {player_id:d} filtered by type "{entry_type}"'))
def query_captain_logs_filtered(context, player_id, entry_type):
    """Query captain logs filtered by entry type"""
    handler = context["get_captain_logs_handler"]

    if f"player_{player_id}" in context:
        actual_player_id = context[f"player_{player_id}"].player_id
    else:
        actual_player_id = player_id

    query = GetCaptainLogsQuery(player_id=actual_player_id, entry_type=entry_type)

    try:
        result = asyncio.run(handler.handle(query))
        context["result"] = result
        context["error"] = None
    except Exception as e:
        context["error"] = e
        context["result"] = None


@when(parsers.parse("I attempt to query captain logs for player {player_id:d}"))
def attempt_query_captain_logs(context, player_id):
    """Attempt to query captain logs and capture errors"""
    query_captain_logs(context, player_id)


# ==============================================================================
# Then Steps
# ==============================================================================
@then("the query should succeed")
def check_query_success(context):
    """Verify query succeeded"""
    assert context["error"] is None
    assert context["result"] is not None


@then(parsers.parse("the result should contain {count:d} log"))
def check_single_log_count(context, count):
    """Verify result contains 1 log (singular)"""
    assert len(context["result"]) == count


@then(parsers.parse("the result should contain {count:d} logs"))
def check_log_count(context, count):
    """Verify result contains expected number of logs"""
    assert len(context["result"]) == count


@then(parsers.parse("the result should contain exactly {count:d} logs"))
def check_exact_log_count(context, count):
    """Verify exact log count"""
    check_log_count(context, count)


@then(parsers.parse("all logs should belong to player {player_id:d}"))
def check_logs_belong_to_player(context, player_id):
    """Verify all logs belong to specified player"""
    actual_player_id = context[f"player_{player_id}"].player_id
    for log in context["result"]:
        assert log["player_id"] == actual_player_id


@then(parsers.parse('all logs should have type "{entry_type}"'))
def check_logs_have_type(context, entry_type):
    """Verify all logs have specified type"""
    for log in context["result"]:
        assert log["entry_type"] == entry_type


@then("the result should be empty")
def check_result_empty(context):
    """Verify result is empty list"""
    assert context["result"] == []
    assert len(context["result"]) == 0


@then("the query should fail with PlayerNotFoundError")
def check_player_not_found_error(context):
    """Verify PlayerNotFoundError was raised"""
    assert isinstance(context["error"], PlayerNotFoundError)


@then(parsers.parse('the error message should mention "{text}"'))
def check_error_message_contains(context, text):
    """Verify error message contains specific text"""
    assert text in str(context["error"])


@then("the first log should have all required fields")
def check_first_log_has_fields(context):
    """Verify first log has all required fields"""
    log = context["result"][0]
    assert "log_id" in log
    assert "player_id" in log
    assert "timestamp" in log
    assert "entry_type" in log
    assert "narrative" in log


@then("the log should have log_id")
def check_log_has_log_id(context):
    """Verify log has log_id"""
    log = context["result"][0]
    assert log["log_id"] is not None


@then("the log should have player_id")
def check_log_has_player_id(context):
    """Verify log has player_id"""
    log = context["result"][0]
    assert log["player_id"] is not None


@then("the log should have timestamp")
def check_log_has_timestamp(context):
    """Verify log has timestamp"""
    log = context["result"][0]
    assert log["timestamp"] is not None


@then("the log should have entry_type")
def check_log_has_entry_type(context):
    """Verify log has entry_type"""
    log = context["result"][0]
    assert log["entry_type"] is not None


@then("the log should have narrative")
def check_log_has_narrative(context):
    """Verify log has narrative"""
    log = context["result"][0]
    assert log["narrative"] is not None


# ==============================================================================
# Scenario Definitions
# ==============================================================================
@scenario("../../../features/application/captain/get_captain_logs.feature",
          "Get recent logs for player")
def test_get_recent_logs():
    pass


@scenario("../../../features/application/captain/get_captain_logs.feature",
          "Get logs with default limit")
def test_get_logs_default_limit():
    pass


@scenario("../../../features/application/captain/get_captain_logs.feature",
          "Filter logs by entry type")
def test_filter_logs_by_type():
    pass


@scenario("../../../features/application/captain/get_captain_logs.feature",
          "Filter logs by operation_started type")
def test_filter_logs_operation_started():
    pass


@scenario("../../../features/application/captain/get_captain_logs.feature",
          "Respect limit parameter")
def test_respect_limit():
    pass


@scenario("../../../features/application/captain/get_captain_logs.feature",
          "Return all logs when count below limit")
def test_return_all_when_below_limit():
    pass


@scenario("../../../features/application/captain/get_captain_logs.feature",
          "Return empty list for player with no logs")
def test_return_empty_no_logs():
    pass


@scenario("../../../features/application/captain/get_captain_logs.feature",
          "Return empty list when filter matches nothing")
def test_return_empty_filter_matches_nothing():
    pass


@scenario("../../../features/application/captain/get_captain_logs.feature",
          "Query fails for non-existent player")
def test_query_fails_nonexistent_player():
    pass


@scenario("../../../features/application/captain/get_captain_logs.feature",
          "Returned logs contain all fields")
def test_returned_logs_contain_all_fields():
    pass
