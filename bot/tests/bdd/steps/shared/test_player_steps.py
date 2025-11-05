from pytest_bdd import scenario, given, when, then, parsers
from datetime import datetime, timezone
import asyncio
import json

from domain.shared.player import Player
from domain.shared.exceptions import DuplicateAgentSymbolError, PlayerNotFoundError
from application.player.commands.register_player import (
    RegisterPlayerCommand,
    RegisterPlayerHandler
)
from application.player.queries.get_player import (
    GetPlayerQuery,
    GetPlayerHandler
)
from application.player.queries.list_players import (
    ListPlayersQuery,
    ListPlayersHandler
)
from application.player.commands.update_player import (
    UpdatePlayerMetadataCommand,
    UpdatePlayerMetadataHandler
)
from application.player.commands.touch_last_active import (
    TouchPlayerLastActiveCommand,
    TouchPlayerLastActiveHandler
)

# ==============================================================================
# Scenario: Register new player
# ==============================================================================
@scenario("../../features/shared/player.feature", "Register new player")
def test_register_new_player():
    pass

@when(parsers.parse('I register player "{agent}" with token "{token}"'))
def register_player(context, agent, token, mock_player_repo):
    """
    Register a player using RegisterPlayerHandler

    Note: Using direct handler invocation (mediator integration in Wave 5)
    """
    handler = RegisterPlayerHandler(mock_player_repo)
    command = RegisterPlayerCommand(agent_symbol=agent, token=token)
    context["player"] = asyncio.run(handler.handle(command))

@then("the player should have a player_id")
def check_player_id(context):
    assert context["player"].player_id is not None

@then(parsers.parse('the player agent_symbol should be "{agent}"'))
def check_agent_symbol(context, agent):
    assert context["player"].agent_symbol == agent

@then(parsers.parse('the player token should be "{token}"'))
def check_token(context, token):
    assert context["player"].token == token

@then("last_active should be set")
def check_last_active(context):
    assert context["player"].last_active is not None

# ==============================================================================
# Scenario: Duplicate agent symbol rejected
# ==============================================================================
@scenario("../../features/shared/player.feature", "Duplicate agent symbol rejected")
def test_duplicate_agent_rejected():
    pass

@given(parsers.parse('a player with agent_symbol "{agent}" exists'))
def existing_player(context, agent, mock_player_repo):
    """Create an existing player in the repository"""
    handler = RegisterPlayerHandler(mock_player_repo)
    command = RegisterPlayerCommand(agent_symbol=agent, token="EXISTING-TOKEN")
    asyncio.run(handler.handle(command))
    context["existing_agent"] = agent

@when(parsers.re(r'I attempt to register player "(?P<agent>.*)" with token "(?P<token>.*)"'))
def attempt_register_player(context, agent, token, mock_player_repo):
    """Attempt to register a player and capture any error"""
    handler = RegisterPlayerHandler(mock_player_repo)
    command = RegisterPlayerCommand(agent_symbol=agent, token=token)
    try:
        asyncio.run(handler.handle(command))
        context["error"] = None
    except Exception as e:
        context["error"] = e

@then("registration should fail with DuplicateAgentSymbolError")
def check_duplicate_error(context):
    assert isinstance(context["error"], DuplicateAgentSymbolError)

# ==============================================================================
# Scenario: Empty agent symbol rejected
# ==============================================================================
@scenario("../../features/shared/player.feature", "Empty agent symbol rejected")
def test_empty_agent_rejected():
    pass

@then("registration should fail with ValueError")
def check_value_error(context):
    assert isinstance(context["error"], ValueError)

# ==============================================================================
# Scenario: Update player metadata
# ==============================================================================
@scenario("../../features/shared/player.feature", "Update player metadata")
def test_update_player_metadata():
    pass

@given(parsers.parse("a registered player with id {player_id:d}"))
def registered_player(context, player_id, mock_player_repo):
    """Register a player with a specific expected ID"""
    handler = RegisterPlayerHandler(mock_player_repo)
    command = RegisterPlayerCommand(
        agent_symbol=f"AGENT-{player_id}",
        token=f"TOKEN-{player_id}"
    )
    player = asyncio.run(handler.handle(command))
    context["player_id"] = player.player_id
    context["player"] = player

@when(parsers.parse('I update metadata with {metadata}'))
def update_metadata(context, metadata, mock_player_repo):
    """Update player metadata"""
    metadata_dict = json.loads(metadata)
    handler = UpdatePlayerMetadataHandler(mock_player_repo)
    command = UpdatePlayerMetadataCommand(
        player_id=context["player_id"],
        metadata=metadata_dict
    )
    context["player"] = asyncio.run(handler.handle(command))

@then(parsers.parse('the player metadata should contain "{key}"'))
def check_metadata_contains_key(context, key):
    assert key in context["player"].metadata

# ==============================================================================
# Scenario: Touch last active timestamp
# ==============================================================================
@scenario("../../features/shared/player.feature", "Touch last active timestamp")
def test_touch_last_active():
    pass

@when("I touch the player's last_active")
def touch_last_active(context, mock_player_repo):
    """Touch the player's last_active timestamp"""
    # Store the old timestamp (before touching)
    context["old_last_active"] = context["player"].last_active

    # Touch the timestamp using the handler
    handler = TouchPlayerLastActiveHandler(mock_player_repo)
    command = TouchPlayerLastActiveCommand(player_id=context["player_id"])
    context["player"] = asyncio.run(handler.handle(command))

@then("last_active should be updated")
def check_last_active_updated(context):
    assert context["player"].last_active > context["old_last_active"]

# ==============================================================================
# Scenario: List all players
# ==============================================================================
@scenario("../../features/shared/player.feature", "List all players")
def test_list_all_players():
    pass

@given(parsers.parse('players "{agent1}", "{agent2}", "{agent3}" are registered'))
def register_multiple_players(context, agent1, agent2, agent3, mock_player_repo):
    """Register multiple players"""
    handler = RegisterPlayerHandler(mock_player_repo)

    for agent in [agent1, agent2, agent3]:
        command = RegisterPlayerCommand(
            agent_symbol=agent,
            token=f"TOKEN-{agent}"
        )
        asyncio.run(handler.handle(command))

@when("I list all players")
def list_all_players(context, mock_player_repo):
    """List all players using the handler"""
    handler = ListPlayersHandler(mock_player_repo)
    query = ListPlayersQuery()
    context["players"] = asyncio.run(handler.handle(query))

@then(parsers.parse("I should see {count:d} players"))
def check_player_count(context, count):
    assert len(context["players"]) == count
