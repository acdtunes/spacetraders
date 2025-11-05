"""
BDD step definitions for Player domain entity.

Black-box testing approach - tests ONLY observable behaviors through public interface.
NO private methods, NO white-box testing, ONLY external behavior verification.
"""
import pytest
from pytest_bdd import scenario, given, when, then, parsers
from datetime import datetime, timezone, timedelta
import time

from domain.shared.player import Player


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
# Player Initialization Scenarios
# ==============================================================================
@scenario("../../features/domain/player.feature", "Create player with all valid data")
def test_create_player_with_all_valid_data():
    pass


@when(parsers.parse('I create a player with player_id {player_id:d}, agent_symbol "{agent_symbol}", token "{token}", and created_at "{timestamp}"'))
def create_player_with_all_data(context, player_id, agent_symbol, token, timestamp):
    created_at = datetime.fromisoformat(timestamp.replace("Z", "+00:00"))
    context["player"] = Player(
        player_id=player_id,
        agent_symbol=agent_symbol,
        token=token,
        created_at=created_at
    )


@then(parsers.parse("the player player_id should be {player_id:d}"))
def check_player_id(context, player_id):
    assert context["player"].player_id == player_id


@then(parsers.parse('the player agent_symbol should be "{agent_symbol}"'))
def check_agent_symbol(context, agent_symbol):
    assert context["player"].agent_symbol == agent_symbol


@then(parsers.parse('the player token should be "{token}"'))
def check_token(context, token):
    assert context["player"].token == token


@then(parsers.parse('the player created_at should be "{timestamp}"'))
def check_created_at(context, timestamp):
    expected = datetime.fromisoformat(timestamp.replace("Z", "+00:00"))
    assert context["player"].created_at == expected


@scenario("../../features/domain/player.feature", "Create player without player_id (new player)")
def test_create_player_without_player_id():
    pass


@when(parsers.parse('I create a player with player_id None, agent_symbol "{agent_symbol}", token "{token}", and created_at "{timestamp}"'))
def create_player_with_none_id(context, agent_symbol, token, timestamp):
    created_at = datetime.fromisoformat(timestamp.replace("Z", "+00:00"))
    context["player"] = Player(
        player_id=None,
        agent_symbol=agent_symbol,
        token=token,
        created_at=created_at
    )


@then("the player player_id should be None")
def check_player_id_is_none(context):
    assert context["player"].player_id is None


@scenario("../../features/domain/player.feature", "Last_active defaults to created_at when not provided")
def test_last_active_defaults():
    pass


@when("I create a player without specifying last_active")
def create_player_without_last_active(context):
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=context["base_time"]
    )


@then("the player last_active should equal the player created_at")
def check_last_active_equals_created_at(context):
    assert context["player"].last_active == context["player"].created_at


@scenario("../../features/domain/player.feature", "Last_active uses provided value when specified")
def test_last_active_provided():
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


@then(parsers.parse('the player last_active should be "{timestamp}"'))
def check_last_active_value(context, timestamp):
    expected = datetime.fromisoformat(timestamp.replace("Z", "+00:00"))
    assert context["player"].last_active == expected


@scenario("../../features/domain/player.feature", "Metadata defaults to empty dict when not provided")
def test_metadata_defaults():
    pass


@when("I create a player without specifying metadata")
def create_player_without_metadata(context):
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=context["base_time"]
    )


@then("the player metadata should be an empty dictionary")
def check_metadata_empty(context):
    assert context["player"].metadata == {}


@scenario("../../features/domain/player.feature", "Metadata uses provided value when specified")
def test_metadata_provided():
    pass


@when(parsers.parse('I create a player with metadata containing faction="{faction}" and credits={credits:d}'))
def create_player_with_metadata(context, faction, credits):
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=context["base_time"],
        metadata={"faction": faction, "credits": credits}
    )


@then(parsers.parse('the player metadata should contain "{key}" with value "{value}"'))
def check_metadata_string_value(context, key, value):
    assert context["player"].metadata[key] == value


@then(parsers.parse('the player metadata should contain "{key}" with value {value:d}'))
def check_metadata_int_value(context, key, value):
    assert context["player"].metadata[key] == value


@scenario("../../features/domain/player.feature", "Agent_symbol whitespace is trimmed")
def test_agent_symbol_trimmed():
    pass


@when(parsers.parse('I create a player with agent_symbol "{agent_symbol}"'))
def create_player_with_agent_symbol(context, agent_symbol):
    context["player"] = Player(
        player_id=1,
        agent_symbol=agent_symbol,
        token="test-token",
        created_at=context["base_time"]
    )


@scenario("../../features/domain/player.feature", "Token whitespace is trimmed")
def test_token_trimmed():
    pass


@when(parsers.parse('I create a player with token "{token}"'))
def create_player_with_token(context, token):
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token=token,
        created_at=context["base_time"]
    )


@scenario("../../features/domain/player.feature", "Empty agent_symbol is rejected")
def test_empty_agent_symbol_rejected():
    pass


@when('I attempt to create a player with agent_symbol ""')
def attempt_create_player_empty_agent_symbol(context):
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


@then(parsers.parse('a ValueError should be raised with message "{message}"'))
def check_value_error_raised(context, message):
    assert context["error"] is not None
    assert isinstance(context["error"], ValueError)
    assert message in str(context["error"])


@scenario("../../features/domain/player.feature", "Whitespace-only agent_symbol is rejected")
def test_whitespace_agent_symbol_rejected():
    pass


@when('I attempt to create a player with agent_symbol "   "')
def attempt_create_player_whitespace_agent_symbol(context):
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


@scenario("../../features/domain/player.feature", "Empty token is rejected")
def test_empty_token_rejected():
    pass


@when('I attempt to create a player with token ""')
def attempt_create_player_empty_token(context):
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


@scenario("../../features/domain/player.feature", "Whitespace-only token is rejected")
def test_whitespace_token_rejected():
    pass


@when('I attempt to create a player with token "   "')
def attempt_create_player_whitespace_token(context):
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
# Player Property Access Scenarios
# ==============================================================================
@scenario("../../features/domain/player.feature", "Player_id property is readable")
def test_player_id_readable():
    pass


@given(parsers.parse("a player exists with player_id {player_id:d}"))
def player_exists_with_id(context, player_id):
    context["player"] = Player(
        player_id=player_id,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=context["base_time"]
    )


@scenario("../../features/domain/player.feature", "Player_id property is read-only")
def test_player_id_readonly():
    pass


@when(parsers.parse("I attempt to set player_id to {new_id:d}"))
def attempt_set_player_id(context, new_id):
    try:
        context["player"].player_id = new_id
        context["error"] = None
    except AttributeError as e:
        context["error"] = e


@then("an AttributeError should be raised")
def check_attribute_error_raised(context):
    assert context["error"] is not None
    assert isinstance(context["error"], AttributeError)


@scenario("../../features/domain/player.feature", "Agent_symbol property is readable")
def test_agent_symbol_readable():
    pass


@given(parsers.parse('a player exists with agent_symbol "{agent_symbol}"'))
def player_exists_with_agent_symbol(context, agent_symbol):
    context["player"] = Player(
        player_id=1,
        agent_symbol=agent_symbol,
        token="test-token",
        created_at=context["base_time"]
    )


@scenario("../../features/domain/player.feature", "Agent_symbol property is read-only")
def test_agent_symbol_readonly():
    pass


@when(parsers.parse('I attempt to set agent_symbol to "{new_symbol}"'))
def attempt_set_agent_symbol(context, new_symbol):
    try:
        context["player"].agent_symbol = new_symbol
        context["error"] = None
    except AttributeError as e:
        context["error"] = e


@scenario("../../features/domain/player.feature", "Token property is readable")
def test_token_readable():
    pass


@given(parsers.parse('a player exists with token "{token}"'))
def player_exists_with_token(context, token):
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token=token,
        created_at=context["base_time"]
    )


@scenario("../../features/domain/player.feature", "Token property is read-only")
def test_token_readonly():
    pass


@when(parsers.parse('I attempt to set token to "{new_token}"'))
def attempt_set_token(context, new_token):
    try:
        context["player"].token = new_token
        context["error"] = None
    except AttributeError as e:
        context["error"] = e


@scenario("../../features/domain/player.feature", "Created_at property is readable")
def test_created_at_readable():
    pass


@given(parsers.parse('a player exists with created_at "{timestamp}"'))
def player_exists_with_created_at(context, timestamp):
    created_at = datetime.fromisoformat(timestamp.replace("Z", "+00:00"))
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=created_at
    )


@scenario("../../features/domain/player.feature", "Created_at property is read-only")
def test_created_at_readonly():
    pass


@when("I attempt to set created_at to a new value")
def attempt_set_created_at(context):
    try:
        context["player"].created_at = datetime.now(timezone.utc)
        context["error"] = None
    except AttributeError as e:
        context["error"] = e


@scenario("../../features/domain/player.feature", "Last_active property is readable")
def test_last_active_readable():
    pass


@given("a player exists")
def player_exists(context):
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=context["base_time"]
    )


@then("the player last_active should not be None")
def check_last_active_not_none(context):
    assert context["player"].last_active is not None


@scenario("../../features/domain/player.feature", "Last_active property is read-only")
def test_last_active_readonly():
    pass


@when("I attempt to set last_active to a new value")
def attempt_set_last_active(context):
    try:
        context["player"].last_active = datetime.now(timezone.utc)
        context["error"] = None
    except AttributeError as e:
        context["error"] = e


@scenario("../../features/domain/player.feature", "Metadata property returns a copy (not reference)")
def test_metadata_returns_copy():
    pass


@given(parsers.parse('a player exists with metadata containing "{key}"="{value}"'))
def player_exists_with_metadata_key(context, key, value):
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=context["base_time"],
        metadata={key: value}
    )


@when(parsers.parse('I get the metadata and modify it to "{key}"="{new_value}"'))
def get_and_modify_metadata(context, key, new_value):
    metadata = context["player"].metadata
    metadata[key] = new_value


@then(parsers.parse('the player metadata should still contain "{key}" with value "{value}"'))
def check_metadata_still_has_value(context, key, value):
    assert context["player"].metadata[key] == value


# ==============================================================================
# Update Last Active Scenarios
# ==============================================================================
@scenario("../../features/domain/player.feature", "Update_last_active sets timestamp to current UTC time")
def test_update_last_active():
    pass


@given(parsers.parse('a player exists with last_active "{timestamp}"'))
def player_exists_with_last_active(context, timestamp):
    # Handle human-readable relative timestamps
    if timestamp == "almost 2 hours ago":
        last_active = datetime.now(timezone.utc) - timedelta(hours=2, seconds=-1)
    elif timestamp == "just over 2 hours ago":
        last_active = datetime.now(timezone.utc) - timedelta(hours=2, seconds=1)
    else:
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


@scenario("../../features/domain/player.feature", "Multiple update_last_active calls increase timestamp")
def test_multiple_updates():
    pass


@when("I record the first timestamp")
def record_first_timestamp(context):
    context["first_timestamp"] = context["player"].last_active


@when("I wait a brief moment")
def wait_brief_moment(context):
    time.sleep(0.01)


@when("I call update_last_active again")
def call_update_last_active_again(context):
    context["player"].update_last_active()


@then("the second timestamp should be greater than the first timestamp")
def check_second_greater_than_first(context):
    assert context["player"].last_active > context["first_timestamp"]


@scenario("../../features/domain/player.feature", "Update_last_active uses UTC timezone")
def test_update_uses_utc():
    pass


@then("the player last_active timezone should be UTC")
def check_timezone_utc(context):
    assert context["player"].last_active.tzinfo == timezone.utc


# ==============================================================================
# Update Metadata Scenarios
# ==============================================================================
@scenario("../../features/domain/player.feature", "Update_metadata adds new key-value pairs")
def test_update_metadata_adds_pairs():
    pass


@given("a player exists with empty metadata")
def player_exists_empty_metadata(context):
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=context["base_time"],
        metadata={}
    )


@when(parsers.parse('I call update_metadata with faction="{faction}" and credits={credits:d}'))
def call_update_metadata_two_fields(context, faction, credits):
    context["player"].update_metadata({"faction": faction, "credits": credits})


@scenario("../../features/domain/player.feature", "Update_metadata updates existing keys")
def test_update_metadata_updates_existing():
    pass


@given(parsers.parse('a player exists with metadata containing faction="{faction}" and credits={credits:d}'))
def player_exists_with_metadata_two_fields(context, faction, credits):
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=context["base_time"],
        metadata={"faction": faction, "credits": credits}
    )


@when(parsers.parse('I call update_metadata with credits={credits:d}'))
def call_update_metadata_one_field(context, credits):
    context["player"].update_metadata({"credits": credits})


@scenario("../../features/domain/player.feature", "Update_metadata adds keys without removing existing ones")
def test_update_metadata_preserves_existing():
    pass


@when(parsers.parse('I call update_metadata with credits={credits:d}'))
def call_update_metadata_credits(context, credits):
    context["player"].update_metadata({"credits": credits})


@scenario("../../features/domain/player.feature", "Update_metadata handles empty dict")
def test_update_metadata_empty():
    pass


@when("I call update_metadata with an empty dictionary")
def call_update_metadata_empty(context):
    context["player"].update_metadata({})


@scenario("../../features/domain/player.feature", "Update_metadata supports multiple sequential updates - first update")
def test_multiple_updates_first():
    pass


@when(parsers.parse('I call update_metadata with {key}="{value}"'))
def call_update_metadata_single_key(context, key, value):
    context["player"].update_metadata({key: value})


@scenario("../../features/domain/player.feature", "Update_metadata supports multiple sequential updates - second update")
def test_multiple_updates_second():
    pass


@given(parsers.parse('a player exists with metadata containing "{key}"="{value}"'))
def player_exists_with_metadata_single_key(context, key, value):
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=context["base_time"],
        metadata={key: value}
    )


@scenario("../../features/domain/player.feature", "Update_metadata supports multiple sequential updates - third update")
def test_multiple_updates_third():
    pass


@given(parsers.parse('a player exists with metadata containing {key1}="{value1}" and {key2}="{value2}"'))
def player_exists_with_metadata_two_keys(context, key1, value1, key2, value2):
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=context["base_time"],
        metadata={key1: value1, key2: value2}
    )


# ==============================================================================
# Is Active Within Scenarios
# ==============================================================================
@scenario("../../features/domain/player.feature", "Is_active_within returns True when last_active is within hours")
def test_is_active_within_true():
    pass


@given(parsers.parse("a player exists with last_active {hours:d} hour ago"))
def player_exists_hours_ago(context, hours):
    last_active = datetime.now(timezone.utc) - timedelta(hours=hours)
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=context["base_time"],
        last_active=last_active
    )


@when(parsers.parse("I check is_active_within with hours={hours:d}"))
def check_is_active_within(context, hours):
    context["result"] = context["player"].is_active_within(hours=hours)


@then("the result should be True")
def check_result_true(context):
    assert context["result"] is True


@scenario("../../features/domain/player.feature", "Is_active_within returns False when last_active exceeds hours")
def test_is_active_within_false():
    pass


@given(parsers.parse("a player exists with last_active {hours:d} hours ago"))
def player_exists_multiple_hours_ago(context, hours):
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


@scenario("../../features/domain/player.feature", "Is_active_within returns True at exact boundary (minus 1 second)")
def test_is_active_at_boundary():
    pass


@given('a player exists with last_active "almost 2 hours ago"')
def player_exists_almost_two_hours(context):
    last_active = datetime.now(timezone.utc) - timedelta(hours=2, seconds=-1)
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=context["base_time"],
        last_active=last_active
    )


@scenario("../../features/domain/player.feature", "Is_active_within returns False just past boundary (plus 1 second)")
def test_is_active_past_boundary():
    pass


@given('a player exists with last_active "just over 2 hours ago"')
def player_exists_just_over_two_hours(context):
    last_active = datetime.now(timezone.utc) - timedelta(hours=2, seconds=1)
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=context["base_time"],
        last_active=last_active
    )


@scenario("../../features/domain/player.feature", "Is_active_within works with fractional hours - within range")
def test_fractional_hours_within():
    pass


@given(parsers.parse("a player exists with last_active {minutes:d} minutes ago"))
def player_exists_minutes_ago(context, minutes):
    last_active = datetime.now(timezone.utc) - timedelta(minutes=minutes)
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=context["base_time"],
        last_active=last_active
    )


@when(parsers.parse("I check is_active_within with hours={hours:f}"))
def check_is_active_within_float(context, hours):
    context["result"] = context["player"].is_active_within(hours=hours)


@scenario("../../features/domain/player.feature", "Is_active_within works with fractional hours - outside range")
def test_fractional_hours_outside():
    pass


@scenario("../../features/domain/player.feature", "Is_active_within works with large hour values - within range")
def test_large_hours_within():
    pass


@given(parsers.parse("a player exists with last_active {days:d} days ago"))
def player_exists_days_ago(context, days):
    last_active = datetime.now(timezone.utc) - timedelta(days=days)
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=context["base_time"],
        last_active=last_active
    )


@scenario("../../features/domain/player.feature", "Is_active_within works with large hour values - outside range")
def test_large_hours_outside():
    pass


# ==============================================================================
# Player Repr Scenarios
# ==============================================================================
@scenario("../../features/domain/player.feature", "Repr contains player_id")
def test_repr_contains_id():
    pass


@given(parsers.parse('a player exists with player_id {player_id:d} and agent_symbol "{agent_symbol}"'))
def player_exists_with_id_and_symbol(context, player_id, agent_symbol):
    context["player"] = Player(
        player_id=player_id,
        agent_symbol=agent_symbol,
        token="test-token",
        created_at=context["base_time"]
    )


@when("I get the string representation")
def get_string_representation(context):
    context["repr"] = repr(context["player"])


@then(parsers.parse('the representation should contain "{text}"'))
def check_repr_contains(context, text):
    assert text in context["repr"]


@scenario("../../features/domain/player.feature", "Repr contains agent_symbol")
def test_repr_contains_symbol():
    pass


@scenario("../../features/domain/player.feature", "Repr handles None player_id - contains None")
def test_repr_none_id_contains_none():
    pass


@given(parsers.parse('a player exists with player_id None and agent_symbol "{agent_symbol}"'))
def player_exists_with_none_id(context, agent_symbol):
    context["player"] = Player(
        player_id=None,
        agent_symbol=agent_symbol,
        token="test-token",
        created_at=context["base_time"]
    )


@scenario("../../features/domain/player.feature", "Repr handles None player_id - contains agent_symbol")
def test_repr_none_id_contains_symbol():
    pass


# ==============================================================================
# Credits Management Scenarios
# ==============================================================================
@scenario("../../features/domain/player.feature", "Player initializes with zero credits by default")
def test_player_credits_default():
    pass


@when("I create a player without specifying credits")
def create_player_without_credits(context):
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=context["base_time"]
    )


@then(parsers.parse("the player credits should be {credits:d}"))
def check_player_credits(context, credits):
    assert context["player"].credits == credits


@scenario("../../features/domain/player.feature", "Player initializes with specified credits")
def test_player_credits_specified():
    pass


@when(parsers.parse("I create a player with credits {credits:d}"))
def create_player_with_credits(context, credits):
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=context["base_time"],
        credits=credits
    )


@scenario("../../features/domain/player.feature", "Credits property is readable")
def test_credits_readable():
    pass


@given(parsers.parse("a player exists with credits {credits:d}"))
def player_exists_with_credits(context, credits):
    context["player"] = Player(
        player_id=1,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=context["base_time"],
        credits=credits
    )


@scenario("../../features/domain/player.feature", "Credits property is read-only")
def test_credits_read_only():
    pass


@when(parsers.parse("I attempt to set credits to {credits:d}"))
def attempt_set_credits(context, credits):
    try:
        context["player"].credits = credits
        context["error"] = None
    except AttributeError as e:
        context["error"] = e


@scenario("../../features/domain/player.feature", "Add_credits increases credits balance")
def test_add_credits():
    pass


@when(parsers.parse("I call add_credits with amount {amount:d}"))
def call_add_credits(context, amount):
    context["player"].add_credits(amount)


@scenario("../../features/domain/player.feature", "Add_credits with zero amount is allowed")
def test_add_credits_zero():
    pass


@scenario("../../features/domain/player.feature", "Add_credits with negative amount raises ValueError")
def test_add_credits_negative():
    pass


@when(parsers.parse("I attempt to add_credits with amount {amount:d}"))
def attempt_add_credits(context, amount):
    try:
        context["player"].add_credits(amount)
        context["error"] = None
    except ValueError as e:
        context["error"] = e


@scenario("../../features/domain/player.feature", "Spend_credits decreases credits balance")
def test_spend_credits():
    pass


@when(parsers.parse("I call spend_credits with amount {amount:d}"))
def call_spend_credits(context, amount):
    context["player"].spend_credits(amount)


@scenario("../../features/domain/player.feature", "Spend_credits with zero amount is allowed")
def test_spend_credits_zero():
    pass


@scenario("../../features/domain/player.feature", "Spend_credits with negative amount raises ValueError")
def test_spend_credits_negative():
    pass


@when(parsers.parse("I attempt to spend_credits with amount {amount:d}"))
def attempt_spend_credits(context, amount):
    try:
        context["player"].spend_credits(amount)
        context["error"] = None
    except (ValueError, InsufficientCreditsError) as e:
        context["error"] = e


@scenario("../../features/domain/player.feature", "Spend_credits with insufficient balance raises InsufficientCreditsError")
def test_spend_credits_insufficient():
    pass


from domain.shared.exceptions import InsufficientCreditsError


@then(parsers.parse('an InsufficientCreditsError should be raised with message "{message}"'))
def check_insufficient_credits_error(context, message):
    assert context["error"] is not None
    assert isinstance(context["error"], InsufficientCreditsError)
    assert message in str(context["error"])


@scenario("../../features/domain/player.feature", "Spend_credits with exact balance succeeds")
def test_spend_credits_exact():
    pass


@scenario("../../features/domain/player.feature", "Multiple credit operations work sequentially - add then spend")
def test_multiple_credits_add_spend():
    pass


@scenario("../../features/domain/player.feature", "Multiple credit operations work sequentially - spend then add")
def test_multiple_credits_spend_add():
    pass
