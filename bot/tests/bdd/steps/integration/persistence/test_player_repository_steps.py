"""BDD step definitions for PlayerRepository integration tests"""
from pytest_bdd import scenario, given, when, then, parsers
import pytest
import tempfile
from pathlib import Path
from datetime import datetime, timedelta

from adapters.secondary.persistence.database import Database
from adapters.secondary.persistence.player_repository import PlayerRepository
from domain.shared.player import Player


@pytest.fixture
def context():
    """Shared test context"""
    return {}


@pytest.fixture
def temp_db_path():
    """Create temporary database path"""
    with tempfile.TemporaryDirectory() as tmpdir:
        yield Path(tmpdir) / "test.db"


@pytest.fixture
def repository(temp_db_path):
    """Create player repository with fresh database"""
    db = Database(temp_db_path)
    return PlayerRepository(db)


# Common step definitions
@given("a fresh player repository")
def fresh_repository(context, repository):
    context["repository"] = repository


@given(parsers.parse('a created player "{agent}"'))
def create_test_player(context, agent, repository):
    player = Player(
        player_id=None,
        agent_symbol=agent,
        token=f"token_{agent}",
        created_at=datetime(2025, 1, 1, 12, 0, 0),
        last_active=datetime(2025, 1, 1, 12, 0, 0),
        metadata={}
    )
    created = repository.create(player)
    context[f"player_{agent}"] = created
    context["last_created"] = created


@when(parsers.parse('I create a player with agent_symbol "{agent}"'))
def create_player(context, agent, repository):
    player = Player(
        player_id=None,
        agent_symbol=agent,
        token=f"token_{agent}",
        created_at=datetime(2025, 1, 1, 12, 0, 0),
        last_active=datetime(2025, 1, 1, 12, 0, 0)
    )
    created = repository.create(player)
    context[f"player_{agent}"] = created
    context["last_created"] = created


@when("I find the player by ID")
def find_by_id(context, repository):
    context["found"] = repository.find_by_id(context["last_created"].player_id)


@when(parsers.parse("I find player by ID {player_id:d}"))
def find_by_specific_id(context, player_id, repository):
    context["found"] = repository.find_by_id(player_id)


@when(parsers.parse('I find the player by agent_symbol "{agent}"'))
def find_by_agent(context, agent, repository):
    context["found"] = repository.find_by_agent_symbol(agent)


@when(parsers.parse('I find player by agent_symbol "{agent}"'))
def find_by_agent_alt(context, agent, repository):
    context["found"] = repository.find_by_agent_symbol(agent)


@when("I list all players")
def list_all_players(context, repository):
    context["players"] = repository.list_all()


@when(parsers.parse('I check if "{agent}" exists'))
def check_existence(context, agent, repository):
    context["exists"] = repository.exists_by_agent_symbol(agent)


@when(parsers.parse('I update the player metadata to {metadata}'))
def update_metadata(context, metadata, repository):
    import json
    player = context["last_created"]
    player.update_metadata(json.loads(metadata), replace=True)
    repository.update(player)


@when("I update the player last_active timestamp")
def update_last_active(context, repository):
    player = context["last_created"]
    player.update_last_active(datetime(2025, 1, 3, 15, 0, 0))
    repository.update(player)


@when("I attempt to update a nonexistent player")
def update_nonexistent(context, repository):
    context["error"] = None
    try:
        player = Player(
            player_id=999,
            agent_symbol="FAKE",
            token="token",
            created_at=datetime(2025, 1, 1, 12, 0, 0),
            last_active=datetime(2025, 1, 1, 12, 0, 0)
        )
        repository.update(player)
    except Exception as e:
        context["error"] = e


@when(parsers.parse('I attempt to create another player with agent_symbol "{agent}"'))
def attempt_duplicate_create(context, agent, repository):
    context["error"] = None
    try:
        player = Player(
            player_id=None,
            agent_symbol=agent,
            token="different_token",
            created_at=datetime(2025, 1, 1, 12, 0, 0),
            last_active=datetime(2025, 1, 1, 12, 0, 0)
        )
        repository.create(player)
    except Exception as e:
        context["error"] = e


@when("I create a player with empty metadata")
def create_empty_metadata(context, repository):
    player = Player(
        player_id=None,
        agent_symbol="NO_META",
        token="token123",
        created_at=datetime(2025, 1, 1, 12, 0, 0),
        last_active=datetime(2025, 1, 1, 12, 0, 0),
        metadata={}
    )
    context["last_created"] = repository.create(player)


@when("I create a player with complex nested metadata")
def create_complex_metadata(context, repository):
    player = Player(
        player_id=None,
        agent_symbol="COMPLEX",
        token="token123",
        created_at=datetime(2025, 1, 1, 12, 0, 0),
        last_active=datetime(2025, 1, 1, 12, 0, 0),
        metadata={
            "faction": "COSMIC",
            "credits": 10000,
            "stats": {"ships": 5, "contracts": 10},
            "flags": ["premium", "beta_tester"]
        }
    )
    context["last_created"] = repository.create(player)


@when(parsers.parse("I create {count:d} players sequentially"))
def create_multiple_players(context, count, repository):
    context["created_players"] = []
    for i in range(count):
        player = Player(
            player_id=None,
            agent_symbol=f"AGENT_{i}",
            token=f"token_{i}",
            created_at=datetime(2025, 1, 1, 12, i, 0),
            last_active=datetime(2025, 1, 1, 12, i, 0)
        )
        created = repository.create(player)
        context["created_players"].append(created)


@when(parsers.parse('I update player "{agent}" metadata'))
def update_specific_player_metadata(context, agent, repository):
    player = context[f"player_{agent}"]
    player.update_metadata({"updated": True}, replace=True)
    repository.update(player)


@when("I update the player metadata")
def update_generic_metadata(context, repository):
    player = context["last_created"]
    player.update_metadata({"updated": True}, replace=True)
    repository.update(player)


@when("I attempt to create a player with empty agent_symbol")
def create_empty_agent(context):
    context["error"] = None
    try:
        Player(
            player_id=None,
            agent_symbol="",
            token="token123",
            created_at=datetime(2025, 1, 1, 12, 0, 0),
            last_active=datetime(2025, 1, 1, 12, 0, 0)
        )
    except Exception as e:
        context["error"] = e


@when("I attempt to create a player with empty token")
def create_empty_token(context):
    context["error"] = None
    try:
        Player(
            player_id=None,
            agent_symbol="AGENT",
            token="",
            created_at=datetime(2025, 1, 1, 12, 0, 0),
            last_active=datetime(2025, 1, 1, 12, 0, 0)
        )
    except Exception as e:
        context["error"] = e


@then("the player should have an auto-generated player_id")
def check_auto_id(context):
    assert context["last_created"].player_id is not None
    assert context["last_created"].player_id > 0


@then(parsers.parse('the player should have agent_symbol "{agent}"'))
def check_agent_symbol(context, agent):
    player = context.get("found") or context.get("last_created")
    assert player.agent_symbol == agent


@then(parsers.parse('the player agent_symbol should be "{agent}"'))
def check_agent_symbol_alt(context, agent):
    assert context["found"].agent_symbol == agent


@then("the player should have the provided token")
def check_token(context):
    assert context["last_created"].token.startswith("token_")


@then(parsers.parse('player "{agent2}" should have a higher ID than "{agent1}"'))
def check_higher_id(context, agent1, agent2):
    player1 = context[f"player_{agent1}"]
    player2 = context[f"player_{agent2}"]
    assert player2.player_id > player1.player_id


@then("the player should be found")
def check_found(context):
    assert context["found"] is not None


@then("the player should not be found")
def check_not_found(context):
    assert context["found"] is None


@then(parsers.parse("I should see {count:d} player"))
def check_player_count_singular(context, count):
    assert len(context["players"]) == count


@then(parsers.parse("I should see {count:d} players"))
def check_player_count(context, count):
    assert len(context["players"]) == count


@then(parsers.parse('the player metadata should contain "{key}"'))
def check_metadata_key(context, key):
    player = context.get("found") or context.get("last_created")
    assert key in player.metadata


@then("the player last_active should be updated")
def check_last_active_updated(context):
    assert context["found"].last_active == datetime(2025, 1, 3, 15, 0, 0)


@then("no error should occur")
def check_no_error(context):
    assert context.get("error") is None


@then("existence check should return true")
def check_exists_true(context):
    assert context["exists"] is True


@then("existence check should return false")
def check_exists_false(context):
    assert context["exists"] is False


@then("creation should fail with an IntegrityError")
def check_integrity_error(context):
    assert context["error"] is not None
    # Can be sqlite3.IntegrityError or wrapped exception


@then("the player metadata should be empty")
def check_empty_metadata(context):
    player = context.get("found") or context.get("last_created")
    assert player.metadata == {}


@then("the player metadata should contain nested structures")
def check_nested_metadata(context):
    player = context.get("found") or context.get("last_created")
    assert "stats" in player.metadata
    assert player.metadata["stats"]["ships"] == 5


@then(parsers.parse("all {count:d} players should be in the database"))
def check_all_in_db(context, count, repository):
    all_players = repository.list_all()
    assert len(all_players) == count


@then("all players should have unique IDs")
def check_unique_ids(context):
    ids = {p.player_id for p in context["created_players"]}
    assert len(ids) == len(context["created_players"])


@then("the player should have the updated metadata")
def check_updated_metadata(context):
    assert context["found"].metadata == {"updated": True}


@then(parsers.parse('player "{agent}" should have updated metadata in the list'))
def check_list_has_updated(context, agent):
    player = next((p for p in context["players"] if p.agent_symbol == agent), None)
    assert player is not None
    assert player.metadata == {"updated": True}


@then("creation should fail with ValueError")
def check_value_error(context):
    assert isinstance(context["error"], ValueError)


# Generate scenario decorators
scenarios_list = [
    "Create new player",
    "Create assigns auto-incrementing IDs",
    "Find player by ID when exists",
    "Find player by ID when not exists",
    "Find player by agent symbol when exists",
    "Find player by agent symbol when not exists",
    "Agent symbol lookup is case sensitive",
    "List all players when empty",
    "List all players with single player",
    "List all players with multiple players",
    "Update player metadata",
    "Update player last_active",
    "Update nonexistent player does not raise error",
    "Check existence by agent symbol when exists",
    "Check existence by agent symbol when not exists",
    "Duplicate agent symbol fails",
    "Player with null metadata",
    "Player with complex metadata",
    "Concurrent player creates",
    "Find after update returns updated values",
    "List all returns fresh data after updates",
    "Empty agent symbol raises ValueError",
    "Empty token raises ValueError",
]

for scenario_name in scenarios_list:
    func_name = f"test_{scenario_name.lower().replace(' ', '_')}"
    scenario_func = scenario(
        "../../../features/integration/persistence/player_repository.feature",
        scenario_name
    )
    globals()[func_name] = scenario_func(lambda: None)
