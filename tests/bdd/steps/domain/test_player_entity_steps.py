"""
BDD step definitions for Player entity domain logic.

Black-box testing approach - tests only public interface and observable behaviors.
"""
import pytest
from pytest_bdd import scenario, given, when, then, parsers
from datetime import datetime, timezone, timedelta
import time

from spacetraders.domain.shared.player import Player


# ==============================================================================
# Fixtures
# ==============================================================================
@pytest.fixture
def context():
    """Shared context for test scenarios"""
    return {}


# ==============================================================================
# Background
# ==============================================================================
@given(parsers.parse('a base timestamp of "{timestamp}"'))
def base_timestamp(context, timestamp):
    """Set base timestamp for testing"""
    context["base_time"] = datetime.fromisoformat(timestamp.replace("Z", "+00:00"))


# ==============================================================================
# Scenario: Create player with valid data
# ==============================================================================
@scenario("../../features/domain/player_entity.feature", "Create player with valid data")
def test_create_player_valid_data():
    pass


@when("I create a player with:", target_fixture="player_creation")
def create_player_with_data(context, datatable):
    """Create player with provided data"""
    # datatable is a list of lists: [headers, data_row]
    headers = datatable[0]
    data_row = datatable[1]

    # Convert to dict
    data = dict(zip(headers, data_row))

    player_id = None if data["player_id"] == "None" else int(data["player_id"])
    created_at = datetime.fromisoformat(data["created_at"].replace("Z", "+00:00"))

    context["player"] = Player(
        player_id=player_id,
        agent_symbol=data["agent_symbol"],
        token=data["token"],
        created_at=created_at
    )


@then(parsers.parse("the player should have player_id {player_id:d}"))
def check_player_id(context, player_id):
    assert context["player"].player_id == player_id


@then(parsers.parse('the player should have agent_symbol "{agent_symbol}"'))
def check_agent_symbol(context, agent_symbol):
    assert context["player"].agent_symbol == agent_symbol


@then(parsers.parse('the player should have token "{token}"'))
def check_token(context, token):
    assert context["player"].token == token


@then(parsers.parse('the player should have created_at "{timestamp}"'))
def check_created_at(context, timestamp):
    expected = datetime.fromisoformat(timestamp.replace("Z", "+00:00"))
    assert context["player"].created_at == expected


# ==============================================================================
# Scenario: Create player without player_id
# ==============================================================================
@scenario("../../features/domain/player_entity.feature", "Create player without player_id")
def test_create_player_without_id():
    pass


@then("the player should have player_id None")
def check_player_id_none(context):
    assert context["player"].player_id is None


# ==============================================================================
# Scenario: Default last_active to created_at
# ==============================================================================
@scenario("../../features/domain/player_entity.feature", "Default last_active to created_at")
def test_default_last_active():
    pass


@when("I create a player without last_active")
def create_player_without_last_active(context):
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=context["base_time"]
    )


@then("the player last_active should equal created_at")
def check_last_active_equals_created_at(context):
    assert context["player"].last_active == context["player"].created_at


# ==============================================================================
# Scenario: Set last_active when provided
# ==============================================================================
@scenario("../../features/domain/player_entity.feature", "Set last_active when provided")
def test_set_last_active():
    pass


@when(parsers.parse('I create a player with last_active "{timestamp}"'))
def create_player_with_last_active(context, timestamp):
    last_active = datetime.fromisoformat(timestamp.replace("Z", "+00:00"))
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=context["base_time"],
        last_active=last_active
    )


@then(parsers.parse('the player should have last_active "{timestamp}"'))
def check_last_active(context, timestamp):
    expected = datetime.fromisoformat(timestamp.replace("Z", "+00:00"))
    assert context["player"].last_active == expected


# ==============================================================================
# Scenario: Default metadata to empty dict
# ==============================================================================
@scenario("../../features/domain/player_entity.feature", "Default metadata to empty dict")
def test_default_metadata():
    pass


@when("I create a player without metadata")
def create_player_without_metadata(context):
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=context["base_time"]
    )


@then("the player metadata should be empty")
def check_metadata_empty(context):
    assert context["player"].metadata == {}


# ==============================================================================
# Scenario: Set metadata when provided
# ==============================================================================
@scenario("../../features/domain/player_entity.feature", "Set metadata when provided")
def test_set_metadata():
    pass


@when("I create a player with metadata:")
def create_player_with_metadata(context, datatable):
    # datatable is a list of lists: [headers, data_row]
    headers = datatable[0]
    data_row = datatable[1]

    metadata = {}
    for key, value in zip(headers, data_row):
        # Try to convert to int if possible
        try:
            metadata[key] = int(value)
        except (ValueError, TypeError):
            metadata[key] = value

    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=context["base_time"],
        metadata=metadata
    )


@then(parsers.parse('the player metadata should contain "{key}" with value "{value}"'))
def check_metadata_string_value(context, key, value):
    assert context["player"].metadata[key] == value


@then(parsers.parse('the player metadata should contain "{key}" with value {value:d}'))
def check_metadata_int_value(context, key, value):
    assert context["player"].metadata[key] == value


# ==============================================================================
# Scenario: Trim agent_symbol whitespace
# ==============================================================================
@scenario("../../features/domain/player_entity.feature", "Trim agent_symbol whitespace")
def test_trim_agent_symbol():
    pass


@when(parsers.parse('I create a player with agent_symbol "{agent_symbol}"'))
def create_player_with_agent_symbol(context, agent_symbol):
    context["player"] = Player(
        player_id=1,
        agent_symbol=agent_symbol,
        token="test-token",
        created_at=context["base_time"]
    )


# ==============================================================================
# Scenario: Trim token whitespace
# ==============================================================================
@scenario("../../features/domain/player_entity.feature", "Trim token whitespace")
def test_trim_token():
    pass


@when(parsers.parse('I create a player with token "{token}"'))
def create_player_with_token(context, token):
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token=token,
        created_at=context["base_time"]
    )


# ==============================================================================
# Scenario: Reject empty agent_symbol
# ==============================================================================
@scenario("../../features/domain/player_entity.feature", "Reject empty agent_symbol")
def test_reject_empty_agent_symbol():
    pass


@when(parsers.parse('I attempt to create a player with agent_symbol "{agent_symbol}"'))
def attempt_create_player_with_agent_symbol(context, agent_symbol):
    try:
        context["player"] = Player(
            player_id=1,
            agent_symbol=agent_symbol,
            token="test-token",
            created_at=context["base_time"]
        )
        context["error"] = None
    except Exception as e:
        context["error"] = e


@when("I attempt to create a player with empty agent_symbol")
def attempt_create_player_with_empty_agent_symbol(context):
    try:
        context["player"] = Player(
            player_id=1,
            agent_symbol="",
            token="test-token",
            created_at=context["base_time"]
        )
        context["error"] = None
    except Exception as e:
        context["error"] = e


@when("I attempt to create a player with whitespace agent_symbol")
def attempt_create_player_with_whitespace_agent_symbol(context):
    try:
        context["player"] = Player(
            player_id=1,
            agent_symbol="   ",
            token="test-token",
            created_at=context["base_time"]
        )
        context["error"] = None
    except Exception as e:
        context["error"] = e


@then(parsers.parse('player creation should fail with error "{error_msg}"'))
def check_creation_error(context, error_msg):
    assert context["error"] is not None
    assert isinstance(context["error"], ValueError)
    assert error_msg in str(context["error"])


# ==============================================================================
# Scenario: Reject whitespace-only agent_symbol
# ==============================================================================
@scenario("../../features/domain/player_entity.feature", "Reject whitespace-only agent_symbol")
def test_reject_whitespace_agent_symbol():
    pass


# ==============================================================================
# Scenario: Reject empty token
# ==============================================================================
@scenario("../../features/domain/player_entity.feature", "Reject empty token")
def test_reject_empty_token():
    pass


@when(parsers.parse('I attempt to create a player with token "{token}"'))
def attempt_create_player_with_token(context, token):
    try:
        context["player"] = Player(
            player_id=1,
            agent_symbol="TEST_AGENT",
            token=token,
            created_at=context["base_time"]
        )
        context["error"] = None
    except Exception as e:
        context["error"] = e


@when("I attempt to create a player with empty token")
def attempt_create_player_with_empty_token(context):
    try:
        context["player"] = Player(
            player_id=1,
            agent_symbol="TEST_AGENT",
            token="",
            created_at=context["base_time"]
        )
        context["error"] = None
    except Exception as e:
        context["error"] = e


@when("I attempt to create a player with whitespace token")
def attempt_create_player_with_whitespace_token(context):
    try:
        context["player"] = Player(
            player_id=1,
            agent_symbol="TEST_AGENT",
            token="   ",
            created_at=context["base_time"]
        )
        context["error"] = None
    except Exception as e:
        context["error"] = e


# ==============================================================================
# Scenario: Reject whitespace-only token
# ==============================================================================
@scenario("../../features/domain/player_entity.feature", "Reject whitespace-only token")
def test_reject_whitespace_token():
    pass


# ==============================================================================
# Scenario: Player_id property is readonly
# ==============================================================================
@scenario("../../features/domain/player_entity.feature", "Player_id property is readonly")
def test_player_id_readonly():
    pass


@given(parsers.parse("a player with player_id {player_id:d}"))
def player_with_id(context, player_id):
    context["player"] = Player(
        player_id=player_id,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=context["base_time"]
    )


@when(parsers.parse("I attempt to modify player_id to {new_id:d}"))
def attempt_modify_player_id(context, new_id):
    try:
        context["player"].player_id = new_id
        context["error"] = None
    except AttributeError as e:
        context["error"] = e


@then("the modification should be rejected")
def check_modification_rejected(context):
    assert context["error"] is not None
    assert isinstance(context["error"], AttributeError)


# ==============================================================================
# Scenario: Agent_symbol property is readonly
# ==============================================================================
@scenario("../../features/domain/player_entity.feature", "Agent_symbol property is readonly")
def test_agent_symbol_readonly():
    pass


@given(parsers.parse('a player with agent_symbol "{agent_symbol}"'))
def player_with_agent_symbol(context, agent_symbol):
    context["player"] = Player(
        player_id=1,
        agent_symbol=agent_symbol,
        token="test-token",
        created_at=context["base_time"]
    )


@when(parsers.parse('I attempt to modify agent_symbol to "{new_symbol}"'))
def attempt_modify_agent_symbol(context, new_symbol):
    try:
        context["player"].agent_symbol = new_symbol
        context["error"] = None
    except AttributeError as e:
        context["error"] = e


# ==============================================================================
# Scenario: Token property is readonly
# ==============================================================================
@scenario("../../features/domain/player_entity.feature", "Token property is readonly")
def test_token_readonly():
    pass


@given(parsers.parse('a player with token "{token}"'))
def player_with_token(context, token):
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token=token,
        created_at=context["base_time"]
    )


@when(parsers.parse('I attempt to modify token to "{new_token}"'))
def attempt_modify_token(context, new_token):
    try:
        context["player"].token = new_token
        context["error"] = None
    except AttributeError as e:
        context["error"] = e


# ==============================================================================
# Scenario: Created_at property is readonly
# ==============================================================================
@scenario("../../features/domain/player_entity.feature", "Created_at property is readonly")
def test_created_at_readonly():
    pass


@given(parsers.parse('a player with created_at "{timestamp}"'))
def player_with_created_at(context, timestamp):
    created_at = datetime.fromisoformat(timestamp.replace("Z", "+00:00"))
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=created_at
    )


@when("I attempt to modify created_at")
def attempt_modify_created_at(context):
    try:
        context["player"].created_at = datetime.now(timezone.utc)
        context["error"] = None
    except AttributeError as e:
        context["error"] = e


# ==============================================================================
# Scenario: Last_active property is readonly
# ==============================================================================
@scenario("../../features/domain/player_entity.feature", "Last_active property is readonly")
def test_last_active_readonly():
    pass


@given("a player with last_active set")
def player_with_last_active(context):
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=context["base_time"]
    )


@when("I attempt to modify last_active")
def attempt_modify_last_active(context):
    try:
        context["player"].last_active = datetime.now(timezone.utc)
        context["error"] = None
    except AttributeError as e:
        context["error"] = e


# ==============================================================================
# Scenario: Metadata returns copy preventing external mutation
# ==============================================================================
@scenario("../../features/domain/player_entity.feature", "Metadata returns copy preventing external mutation")
def test_metadata_returns_copy():
    pass


@given(parsers.parse('a player with metadata containing "{key}" = "{value}"'))
def player_with_metadata_key(context, key, value):
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=context["base_time"],
        metadata={key: value}
    )


@when(parsers.parse('I modify the returned metadata to "{key}" = "{new_value}"'))
def modify_returned_metadata(context, key, new_value):
    returned_metadata = context["player"].metadata
    returned_metadata[key] = new_value


@then(parsers.parse('the player metadata should still contain "{key}" with value "{value}"'))
def check_metadata_unchanged(context, key, value):
    assert context["player"].metadata[key] == value


# ==============================================================================
# Scenario: Update last_active to current time
# ==============================================================================
@scenario("../../features/domain/player_entity.feature", "Update last_active to current time")
def test_update_last_active():
    pass


@given(parsers.parse('a player with last_active "{timestamp}"'))
def player_with_last_active_time(context, timestamp):
    last_active = datetime.fromisoformat(timestamp.replace("Z", "+00:00"))
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=context["base_time"],
        last_active=last_active
    )
    context["old_last_active"] = last_active


@when("I call update_last_active")
def call_update_last_active(context):
    context["player"].update_last_active()


@then(parsers.parse('the player last_active should be more recent than "{timestamp}"'))
def check_last_active_more_recent(context, timestamp):
    expected = datetime.fromisoformat(timestamp.replace("Z", "+00:00"))
    assert context["player"].last_active > expected


# ==============================================================================
# Scenario: Multiple updates increase timestamp
# ==============================================================================
@scenario("../../features/domain/player_entity.feature", "Multiple updates increase timestamp")
def test_multiple_updates():
    pass


@when("I wait briefly")
def wait_briefly(context):
    time.sleep(0.01)


@when("I call update_last_active again")
def call_update_last_active_again(context):
    context["first_timestamp"] = context["player"].last_active
    time.sleep(0.01)
    context["player"].update_last_active()


@then("the second timestamp should be greater than the first")
def check_second_greater_than_first(context):
    assert context["player"].last_active > context["first_timestamp"]


# ==============================================================================
# Scenario: Update uses UTC timezone
# ==============================================================================
@scenario("../../features/domain/player_entity.feature", "Update uses UTC timezone")
def test_update_uses_utc():
    pass


@given("a player")
def create_basic_player(context):
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=context["base_time"]
    )


@then("the last_active timezone should be UTC")
def check_timezone_utc(context):
    assert context["player"].last_active.tzinfo == timezone.utc


# ==============================================================================
# Scenario: Update metadata with new values
# ==============================================================================
@scenario("../../features/domain/player_entity.feature", "Update metadata with new values")
def test_update_metadata_new_values():
    pass


@given("a player with empty metadata")
def player_with_empty_metadata(context):
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=context["base_time"],
        metadata={}
    )


@when("I update metadata with:")
def update_metadata_with_data(context, datatable):
    # datatable is a list of lists: [headers, data_row]
    headers = datatable[0]
    data_row = datatable[1]

    metadata = {}
    for key, value in zip(headers, data_row):
        # Try to convert to int if possible
        try:
            metadata[key] = int(value)
        except (ValueError, TypeError):
            metadata[key] = value

    context["player"].update_metadata(metadata)


# ==============================================================================
# Scenario: Update existing metadata keys
# ==============================================================================
@scenario("../../features/domain/player_entity.feature", "Update existing metadata keys")
def test_update_existing_metadata():
    pass


@given("a player with metadata:")
def player_with_metadata(context, datatable):
    # datatable is a list of lists: [headers, data_row]
    headers = datatable[0]
    data_row = datatable[1]

    metadata = {}
    for key, value in zip(headers, data_row):
        # Try to convert to int if possible
        try:
            metadata[key] = int(value)
        except (ValueError, TypeError):
            metadata[key] = value

    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=context["base_time"],
        metadata=metadata
    )


# ==============================================================================
# Scenario: Add new keys to metadata
# ==============================================================================
@scenario("../../features/domain/player_entity.feature", "Add new keys to metadata")
def test_add_new_metadata_keys():
    pass


# ==============================================================================
# Scenario: Handle empty metadata update
# ==============================================================================
@scenario("../../features/domain/player_entity.feature", "Handle empty metadata update")
def test_empty_metadata_update():
    pass


@when("I update metadata with empty dict")
def update_metadata_empty(context):
    context["player"].update_metadata({})


# ==============================================================================
# Scenario: Handle multiple metadata updates
# ==============================================================================
@scenario("../../features/domain/player_entity.feature", "Handle multiple metadata updates")
def test_multiple_metadata_updates():
    pass


@when(parsers.parse('I update metadata with "{key}" = "{value}"'))
def update_metadata_single_key(context, key, value):
    context["player"].update_metadata({key: value})


# ==============================================================================
# Scenario: Active within specified hours
# ==============================================================================
@scenario("../../features/domain/player_entity.feature", "Active within specified hours")
def test_active_within_hours():
    pass


@given(parsers.parse("a player with last_active {hours:d} hour ago"))
def player_with_last_active_hours_ago(context, hours):
    last_active = datetime.now(timezone.utc) - timedelta(hours=hours)
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=context["base_time"],
        last_active=last_active
    )


@when(parsers.parse("I check if active within {hours:d} hours"))
def check_active_within_hours(context, hours):
    context["result"] = context["player"].is_active_within(hours=hours)


@then("the result should be True")
def check_result_true(context):
    assert context["result"] is True


# ==============================================================================
# Scenario: Not active within specified hours
# ==============================================================================
@scenario("../../features/domain/player_entity.feature", "Not active within specified hours")
def test_not_active_within_hours():
    pass


@given(parsers.parse("a player with last_active {hours:d} hours ago"))
def player_with_last_active_multiple_hours_ago(context, hours):
    last_active = datetime.now(timezone.utc) - timedelta(hours=hours)
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=context["base_time"],
        last_active=last_active
    )


@then("the result should be False")
def check_result_false(context):
    assert context["result"] is False


# ==============================================================================
# Scenario: Active at exact boundary
# ==============================================================================
@scenario("../../features/domain/player_entity.feature", "Active at exact boundary")
def test_active_at_boundary():
    pass


@given(parsers.parse("a player with last_active almost {hours:d} hours ago"))
def player_with_last_active_almost_hours_ago(context, hours):
    # Almost X hours ago (minus 1 second)
    last_active = datetime.now(timezone.utc) - timedelta(hours=hours, seconds=-1)
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=context["base_time"],
        last_active=last_active
    )


# ==============================================================================
# Scenario: Not active just past boundary
# ==============================================================================
@scenario("../../features/domain/player_entity.feature", "Not active just past boundary")
def test_not_active_past_boundary():
    pass


@given(parsers.parse("a player with last_active just over {hours:d} hours ago"))
def player_with_last_active_just_over_hours_ago(context, hours):
    # Just over X hours ago (plus 1 second)
    last_active = datetime.now(timezone.utc) - timedelta(hours=hours, seconds=1)
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=context["base_time"],
        last_active=last_active
    )


# ==============================================================================
# Scenario: Works with fractional hours - within
# ==============================================================================
@scenario("../../features/domain/player_entity.feature", "Works with fractional hours - within")
def test_fractional_hours_within():
    pass


@given(parsers.parse("a player with last_active {minutes:d} minutes ago"))
def player_with_last_active_minutes_ago(context, minutes):
    last_active = datetime.now(timezone.utc) - timedelta(minutes=minutes)
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=context["base_time"],
        last_active=last_active
    )


@when(parsers.re(r'I check if active within (?P<hours>[\d.]+) hours?'))
def check_active_within_hours_flexible(context, hours):
    context["result"] = context["player"].is_active_within(hours=float(hours))


# ==============================================================================
# Scenario: Works with fractional hours - not within
# ==============================================================================
@scenario("../../features/domain/player_entity.feature", "Works with fractional hours - not within")
def test_fractional_hours_not_within():
    pass


# ==============================================================================
# Scenario: Works with large hour values - within
# ==============================================================================
@scenario("../../features/domain/player_entity.feature", "Works with large hour values - within")
def test_large_hours_within():
    pass


@given(parsers.parse("a player with last_active {days:d} days ago"))
def player_with_last_active_days_ago(context, days):
    last_active = datetime.now(timezone.utc) - timedelta(days=days)
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=context["base_time"],
        last_active=last_active
    )


# ==============================================================================
# Scenario: Works with large hour values - not within
# ==============================================================================
@scenario("../../features/domain/player_entity.feature", "Works with large hour values - not within")
def test_large_hours_not_within():
    pass


# ==============================================================================
# Scenario: Repr contains player info
# ==============================================================================
@scenario("../../features/domain/player_entity.feature", "Repr contains player info")
def test_repr_contains_info():
    pass


@given(parsers.parse('a player with player_id {player_id:d} and agent_symbol "{agent_symbol}"'))
def player_with_id_and_symbol(context, player_id, agent_symbol):
    context["player"] = Player(
        player_id=player_id,
        agent_symbol=agent_symbol,
        token="test-token",
        created_at=context["base_time"]
    )


@when("I get the repr string")
def get_repr_string(context):
    context["repr"] = repr(context["player"])


@then(parsers.parse('the repr should contain "{text}"'))
def check_repr_contains(context, text):
    assert text in context["repr"]


# ==============================================================================
# Scenario: Repr with None player_id
# ==============================================================================
@scenario("../../features/domain/player_entity.feature", "Repr with None player_id")
def test_repr_none_player_id():
    pass


@given(parsers.parse('a player with player_id None and agent_symbol "{agent_symbol}"'))
def player_with_none_id_and_symbol(context, agent_symbol):
    context["player"] = Player(
        player_id=None,
        agent_symbol=agent_symbol,
        token="test-token",
        created_at=context["base_time"]
    )
