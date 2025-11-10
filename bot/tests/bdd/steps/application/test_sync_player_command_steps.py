from pytest_bdd import scenario, given, when, then, parsers
import asyncio
from unittest.mock import patch
from datetime import datetime, timezone

from application.player.commands.sync_player import (
    SyncPlayerCommand,
    SyncPlayerHandler
)
from domain.shared.player import Player


def create_api_agent_data(
    agent_symbol: str = "TEST-AGENT",
    credits: int = 0,
    headquarters: str = "X1-TEST-A1",
    ship_count: int = 1,
    account_id: str = "test-123"
) -> dict:
    """Helper to create API agent data"""
    return {
        "data": {
            "symbol": agent_symbol,
            "credits": credits,
            "headquarters": headquarters,
            "shipCount": ship_count,
            "accountId": account_id
        }
    }


# ==============================================================================
# Scenario: Sync updates player credits from API
# ==============================================================================
@scenario("../../features/application/sync_player_command.feature", "Sync updates player credits from API")
def test_sync_updates_player_credits():
    pass


@given(parsers.parse('a player exists with player_id {player_id:d} and agent_symbol "{agent_symbol}" and credits {credits:d}'))
def player_exists_with_credits(context, player_repo, mock_api, player_id, agent_symbol, credits):
    """Create an existing player with specific credits"""
    player = Player(
        player_id=player_id,
        agent_symbol=agent_symbol,
        token="test-token-123",
        created_at=datetime.now(timezone.utc),
        credits=credits
    )
    player_repo.create(player)
    context["player_id"] = player_id
    context["agent_symbol"] = agent_symbol
    context["api"] = mock_api


@given(parsers.parse('the API returns agent "{agent_symbol}" with credits {credits:d}'))
def api_returns_agent_with_credits(context, agent_symbol, credits):
    """Set up API to return agent with specific credits"""
    context["api"].agent_data = create_api_agent_data(
        agent_symbol=agent_symbol,
        credits=credits
    )


@when(parsers.parse("I sync player data for player {player_id:d}"))
def sync_player_data(context, player_repo, player_id):
    """Execute sync player command"""
    handler = SyncPlayerHandler(player_repo)
    command = SyncPlayerCommand(player_id=player_id)

    with patch('configuration.container.get_api_client_for_player', return_value=context["api"]):
        result = asyncio.run(handler.handle(command))

    context["result"] = result
    context["repo"] = player_repo


@then(parsers.parse("the player should have credits {credits:d}"))
def check_player_credits(context, credits):
    """Verify player has correct credits"""
    player_id = context.get("player_id")
    player = context["repo"].find_by_id(player_id)
    assert player.credits == credits


# ==============================================================================
# Scenario: Sync updates player headquarters from API
# ==============================================================================
@scenario("../../features/application/sync_player_command.feature", "Sync updates player headquarters from API")
def test_sync_updates_player_headquarters():
    pass


@given(parsers.parse('a player exists with player_id {player_id:d} and agent_symbol "{agent_symbol}"'))
def player_exists(context, player_repo, mock_api, player_id, agent_symbol):
    """Create an existing player"""
    player = Player(
        player_id=player_id,
        agent_symbol=agent_symbol,
        token="test-token-123",
        created_at=datetime.now(timezone.utc)
    )
    player_repo.create(player)
    context["player_id"] = player_id
    context["agent_symbol"] = agent_symbol
    context["api"] = mock_api


@given(parsers.parse('the API returns agent "{agent_symbol}" with headquarters "{headquarters}"'))
def api_returns_agent_with_headquarters(context, agent_symbol, headquarters):
    """Set up API to return agent with headquarters"""
    context["api"].agent_data = create_api_agent_data(
        agent_symbol=agent_symbol,
        headquarters=headquarters
    )


@then(parsers.parse('the player headquarters should be "{headquarters}"'))
def check_player_headquarters(context, headquarters):
    """Verify player has correct headquarters"""
    player_id = context.get("player_id")
    player = context["repo"].find_by_id(player_id)
    assert player.metadata.get("headquarters") == headquarters


# ==============================================================================
# Scenario: Sync updates both credits and headquarters
# ==============================================================================
@scenario("../../features/application/sync_player_command.feature", "Sync updates both credits and headquarters")
def test_sync_updates_credits_and_headquarters():
    pass


@given(parsers.parse('the API returns agent "{agent_symbol}" with credits {credits:d} and headquarters "{headquarters}"'))
def api_returns_agent_with_credits_and_headquarters(context, agent_symbol, credits, headquarters):
    """Set up API to return agent with credits and headquarters"""
    context["api"].agent_data = create_api_agent_data(
        agent_symbol=agent_symbol,
        credits=credits,
        headquarters=headquarters
    )


# ==============================================================================
# Scenario: Sync updates existing metadata without replacing it
# ==============================================================================
@scenario("../../features/application/sync_player_command.feature", "Sync updates existing metadata without replacing it")
def test_sync_preserves_existing_metadata():
    pass


@given(parsers.parse('a player exists with player_id {player_id:d} and agent_symbol "{agent_symbol}" with metadata key "{key}" value "{value}"'))
def player_exists_with_metadata(context, player_repo, mock_api, player_id, agent_symbol, key, value):
    """Create an existing player with metadata"""
    player = Player(
        player_id=player_id,
        agent_symbol=agent_symbol,
        token="test-token-123",
        created_at=datetime.now(timezone.utc),
        metadata={key: value}
    )
    player_repo.create(player)
    context["player_id"] = player_id
    context["agent_symbol"] = agent_symbol
    context["api"] = mock_api


@then(parsers.parse('the player metadata should contain "{key}" with value "{value}"'))
def check_player_metadata_string(context, key, value):
    """Verify player metadata contains key with string value"""
    player_id = context.get("player_id")
    player = context["repo"].find_by_id(player_id)
    assert player.metadata.get(key) == value


# ==============================================================================
# Scenario: Sync converts API agent data correctly
# ==============================================================================
@scenario("../../features/application/sync_player_command.feature", "Sync converts API agent data correctly")
def test_sync_converts_api_data():
    pass


@given(parsers.parse('the API returns agent "{agent_symbol}" with:'))
def api_returns_agent_with_data(context, mock_api, agent_symbol, datatable):
    """Set up API to return agent with specific data from table"""
    data = {}
    for i, row in enumerate(datatable):
        if i == 0:  # Skip header row
            continue
        field = row[0]
        value = row[1]

        # Convert to appropriate type
        if field == "credits" or field == "shipCount":
            data[field] = int(value)
        else:
            data[field] = value

    mock_api.agent_data = {
        "data": {
            "symbol": agent_symbol,
            "credits": data.get("credits", 0),
            "headquarters": data.get("headquarters", "X1-TEST-A1"),
            "shipCount": data.get("shipCount", 1),
            "accountId": data.get("accountId", "test-123")
        }
    }
    context["api"] = mock_api


@then(parsers.parse("the player metadata should contain \"{key}\" with value {value:d}"))
def check_player_metadata_int(context, key, value):
    """Verify player metadata contains key with integer value"""
    player_id = context.get("player_id")
    player = context["repo"].find_by_id(player_id)
    assert player.metadata.get(key) == value


# ==============================================================================
# Scenario: Sync returns updated player entity
# ==============================================================================
@scenario("../../features/application/sync_player_command.feature", "Sync returns updated player entity")
def test_sync_returns_updated_player():
    pass


@then("the sync should return a Player entity")
def check_returns_player_entity(context):
    """Verify sync returns a Player entity"""
    assert isinstance(context["result"], Player)


@then(parsers.parse("the returned player should have credits {credits:d}"))
def check_returned_player_credits(context, credits):
    """Verify returned player has correct credits"""
    assert context["result"].credits == credits
