"""BDD steps for Get Player Query"""
from pytest_bdd import scenario, given, when, then, parsers
import asyncio
import pytest
from datetime import datetime, timezone, timedelta

from spacetraders.application.player.queries.get_player import (
    GetPlayerQuery,
    GetPlayerHandler,
    GetPlayerByAgentQuery,
    GetPlayerByAgentHandler
)
from spacetraders.domain.shared.player import Player
from spacetraders.domain.shared.exceptions import PlayerNotFoundError


# ==============================================================================
# Background
# ==============================================================================
@given("the get player query handlers are initialized")
def initialize_handlers(context, mock_player_repo):
    """Initialize both GetPlayer handlers with mock repository"""
    context["get_player_handler"] = GetPlayerHandler(mock_player_repo)
    context["get_player_by_agent_handler"] = GetPlayerByAgentHandler(mock_player_repo)
    context["mock_player_repo"] = mock_player_repo


# ==============================================================================
# Scenario: Get player by ID successfully
# ==============================================================================
@scenario("../../features/application/get_player_query.feature",
          "Get player by ID successfully")
def test_get_player_by_id_successfully():
    pass


@given(parsers.parse('a registered player with id {player_id:d} and agent symbol "{agent_symbol}"'))
def create_player_with_id_and_symbol(context, player_id, agent_symbol, mock_player_repo):
    """Create a registered player with specific ID and agent symbol"""
    player = Player(
        player_id=None,  # Will be assigned by mock repo
        agent_symbol=agent_symbol,
        token=f"test-token-{player_id}",
        created_at=datetime.now(timezone.utc) - timedelta(days=1),
        last_active=datetime.now(timezone.utc) - timedelta(hours=1)
    )
    created_player = mock_player_repo.create(player)
    context[f"player_{player_id}"] = created_player
    context["player"] = created_player
    context["player_id"] = created_player.player_id


@when(parsers.parse("I execute get player query for player id {player_id:d}"))
def execute_get_player_query(context, player_id):
    """Execute GetPlayerQuery"""
    handler = context["get_player_handler"]

    # Get the actual player_id from the registered player
    if f"player_{player_id}" in context:
        actual_player_id = context[f"player_{player_id}"].player_id
    else:
        actual_player_id = player_id

    query = GetPlayerQuery(player_id=actual_player_id)

    try:
        result = asyncio.run(handler.handle(query))
        if "result1" not in context:
            context["result1"] = result
        elif "result2" not in context:
            context["result2"] = result
        else:
            context["result"] = result
        context["error"] = None
    except Exception as e:
        context["error"] = e
        context["result"] = None


@then("the query should succeed")
def check_query_success(context):
    """Verify the query succeeded without errors"""
    assert context["error"] is None
    assert ("result1" in context or "result" in context)


@then(parsers.parse("the returned player should have player_id {player_id:d}"))
def check_returned_player_id(context, player_id):
    """Verify the returned player has the correct player_id"""
    result = context.get("result") or context.get("result1")
    # Compare against the actual player_id stored in context
    if f"player_{player_id}" in context:
        expected_id = context[f"player_{player_id}"].player_id
        assert result.player_id == expected_id
    else:
        assert result.player_id == player_id


@then(parsers.parse('the returned player should have agent symbol "{agent_symbol}"'))
def check_returned_agent_symbol(context, agent_symbol):
    """Verify the returned player has the correct agent symbol"""
    result = context.get("result") or context.get("result1")
    assert result.agent_symbol == agent_symbol


# ==============================================================================
# Scenario: Get different players by ID
# ==============================================================================
@scenario("../../features/application/get_player_query.feature",
          "Get different players by ID")
def test_get_different_players_by_id():
    pass


@then(parsers.parse("the first result should have player_id {player_id:d}"))
def check_first_result_player_id(context, player_id):
    """Verify first result player_id"""
    expected_id = context[f"player_{player_id}"].player_id
    assert context["result1"].player_id == expected_id


@then(parsers.parse('the first result should have agent symbol "{agent_symbol}"'))
def check_first_result_agent_symbol(context, agent_symbol):
    """Verify first result agent symbol"""
    assert context["result1"].agent_symbol == agent_symbol


@then(parsers.parse("the second result should have player_id {player_id:d}"))
def check_second_result_player_id(context, player_id):
    """Verify second result player_id"""
    expected_id = context[f"player_{player_id}"].player_id
    assert context["result2"].player_id == expected_id


@then(parsers.parse('the second result should have agent symbol "{agent_symbol}"'))
def check_second_result_agent_symbol(context, agent_symbol):
    """Verify second result agent symbol"""
    assert context["result2"].agent_symbol == agent_symbol


# ==============================================================================
# Scenario: Get player returns same instance on multiple calls
# ==============================================================================
@scenario("../../features/application/get_player_query.feature",
          "Get player returns same instance on multiple calls")
def test_get_player_same_instance():
    pass


@then("both results should be the same player instance")
def check_same_instance(context):
    """Verify both results are the same object instance"""
    assert context["result1"] is context["result2"]


# ==============================================================================
# Scenario: Cannot get non-existent player by ID
# ==============================================================================
@scenario("../../features/application/get_player_query.feature",
          "Cannot get non-existent player by ID")
def test_cannot_get_nonexistent_player():
    pass


@given(parsers.parse("no player exists with id {player_id:d}"))
def no_player_exists_with_id(context, player_id):
    """Ensure no player exists with the given ID"""
    context["nonexistent_player_id"] = player_id


@when(parsers.parse("I attempt to get player by id {player_id:d}"))
def attempt_get_player_by_id(context, player_id):
    """Attempt to get player by ID and capture any errors"""
    handler = context["get_player_handler"]
    query = GetPlayerQuery(player_id=player_id)

    try:
        context["result"] = asyncio.run(handler.handle(query))
        context["error"] = None
    except Exception as e:
        context["error"] = e
        context["result"] = None


@then("the query should fail with PlayerNotFoundError")
def check_player_not_found_error(context):
    """Verify PlayerNotFoundError was raised"""
    assert isinstance(context["error"], PlayerNotFoundError)


@then(parsers.parse('the error message should mention "{text}"'))
def check_error_message_contains(context, text):
    """Verify error message contains specific text"""
    assert text in str(context["error"])


# ==============================================================================
# Scenario: Cannot get player with zero ID
# ==============================================================================
@scenario("../../features/application/get_player_query.feature",
          "Cannot get player with zero ID")
def test_cannot_get_player_zero_id():
    pass


# ==============================================================================
# Scenario: Cannot get player with negative ID
# ==============================================================================
@scenario("../../features/application/get_player_query.feature",
          "Cannot get player with negative ID")
def test_cannot_get_player_negative_id():
    pass


# ==============================================================================
# Scenario: Get player by agent symbol successfully
# ==============================================================================
@scenario("../../features/application/get_player_query.feature",
          "Get player by agent symbol successfully")
def test_get_player_by_agent_successfully():
    pass


@when(parsers.parse('I execute get player by agent query for agent symbol "{agent_symbol}"'))
def execute_get_player_by_agent_query(context, agent_symbol):
    """Execute GetPlayerByAgentQuery"""
    handler = context["get_player_by_agent_handler"]
    query = GetPlayerByAgentQuery(agent_symbol=agent_symbol)

    try:
        result = asyncio.run(handler.handle(query))
        if "result1" not in context:
            context["result1"] = result
        else:
            context["result"] = result
        context["error"] = None
    except Exception as e:
        context["error"] = e
        context["result"] = None


# ==============================================================================
# Scenario: Get correct player by agent symbol when multiple exist
# ==============================================================================
@scenario("../../features/application/get_player_query.feature",
          "Get correct player by agent symbol when multiple exist")
def test_get_player_by_agent_multiple_exist():
    pass


# ==============================================================================
# Scenario: Agent symbol lookup is case-sensitive
# ==============================================================================
@scenario("../../features/application/get_player_query.feature",
          "Agent symbol lookup is case-sensitive")
def test_agent_lookup_case_sensitive():
    pass


@when(parsers.parse('I attempt to get player by agent symbol "{agent_symbol}"'))
def attempt_get_player_by_agent(context, agent_symbol):
    """Attempt to get player by agent symbol and capture any errors"""
    handler = context["get_player_by_agent_handler"]
    query = GetPlayerByAgentQuery(agent_symbol=agent_symbol)

    try:
        context["result"] = asyncio.run(handler.handle(query))
        context["error"] = None
    except Exception as e:
        context["error"] = e
        context["result"] = None


@when('I attempt to get player by agent symbol ""')
def attempt_get_player_by_empty_agent(context):
    """Attempt to get player by empty agent symbol and capture any errors"""
    handler = context["get_player_by_agent_handler"]
    query = GetPlayerByAgentQuery(agent_symbol="")

    try:
        context["result"] = asyncio.run(handler.handle(query))
        context["error"] = None
    except Exception as e:
        context["error"] = e
        context["result"] = None


# ==============================================================================
# Scenario: Get player with special characters in agent symbol
# ==============================================================================
@scenario("../../features/application/get_player_query.feature",
          "Get player with special characters in agent symbol")
def test_get_player_special_characters():
    pass


# ==============================================================================
# Scenario: Cannot get non-existent player by agent symbol
# ==============================================================================
@scenario("../../features/application/get_player_query.feature",
          "Cannot get non-existent player by agent symbol")
def test_cannot_get_nonexistent_player_by_agent():
    pass


@given(parsers.parse('no player exists with agent symbol "{agent_symbol}"'))
def no_player_exists_with_agent(context, agent_symbol):
    """Ensure no player exists with the given agent symbol"""
    context["nonexistent_agent_symbol"] = agent_symbol


@given('no player exists with agent symbol ""')
def no_player_exists_with_empty_agent(context):
    """Ensure no player exists with empty agent symbol"""
    context["nonexistent_agent_symbol"] = ""


# ==============================================================================
# Scenario: Cannot get player with empty agent symbol
# ==============================================================================
@scenario("../../features/application/get_player_query.feature",
          "Cannot get player with empty agent symbol")
def test_cannot_get_player_empty_agent():
    pass


# ==============================================================================
# Scenario: Get player handler initializes with repository correctly
# ==============================================================================
@scenario("../../features/application/get_player_query.feature",
          "Get player handler initializes with repository correctly")
def test_get_player_handler_initializes():
    pass


@given("a mock player repository is created")
def create_mock_repo(context, mock_player_repo):
    """Create a mock player repository"""
    context["test_mock_repo"] = mock_player_repo


@when("I create a get player handler with the repository")
def create_get_player_handler(context):
    """Create a GetPlayerHandler with the repository"""
    context["test_handler"] = GetPlayerHandler(context["test_mock_repo"])


@then("the handler should have the repository initialized")
def check_handler_has_repository(context):
    """Verify the handler has the repository initialized"""
    assert context["test_handler"]._player_repo is context["test_mock_repo"]


# ==============================================================================
# Scenario: Get player by agent handler initializes with repository correctly
# ==============================================================================
@scenario("../../features/application/get_player_query.feature",
          "Get player by agent handler initializes with repository correctly")
def test_get_player_by_agent_handler_initializes():
    pass


@when("I create a get player by agent handler with the repository")
def create_get_player_by_agent_handler(context):
    """Create a GetPlayerByAgentHandler with the repository"""
    context["test_handler"] = GetPlayerByAgentHandler(context["test_mock_repo"])
