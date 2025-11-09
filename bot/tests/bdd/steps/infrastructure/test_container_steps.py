"""BDD steps for Dependency Injection Container"""
from pytest_bdd import scenario, given, when, then, parsers
import pytest

from configuration import container


# ==============================================================================
# Background
# ==============================================================================
@given("the container is reset")
def reset_container(context):
    """Reset the container before each scenario"""
    container.reset_container()


# ==============================================================================
# Scenario: Get database returns instance
# ==============================================================================
@scenario("../../features/infrastructure/container.feature",
          "Get database returns instance")
def test_get_database_returns_instance():
    pass


@given("I get the database instance")
@when("I get the database instance")
def get_database(context):
    """Get the database instance"""
    context["database"] = container.get_database()


@then("the database should not be null")
def check_database_not_null(context):
    """Verify database is not null"""
    assert context["database"] is not None


@then("the database should have a connection attribute")
def check_database_has_connection(context):
    """Verify database has connection attribute"""
    assert hasattr(context["database"], "connection")


# ==============================================================================
# Scenario: Get database returns singleton
# ==============================================================================
@scenario("../../features/infrastructure/container.feature",
          "Get database returns singleton")
def test_database_singleton():
    pass


@when("I get the database instance twice")
def get_database_twice(context):
    """Get database instance twice"""
    context["database1"] = container.get_database()
    context["database2"] = container.get_database()


@then("both database instances should be the same")
def check_database_same(context):
    """Verify both instances are the same"""
    assert context["database1"] is context["database2"]


# ==============================================================================
# Scenario: Get database creates new instance after reset
# ==============================================================================
@scenario("../../features/infrastructure/container.feature",
          "Get database creates new instance after reset")
def test_database_reset():
    pass


@when("I reset the container")
def reset_container_step(context):
    """Reset the container"""
    container.reset_container()


@when("I get the database instance again")
def get_database_again(context):
    """Get database instance again"""
    context["database2"] = container.get_database()


@then("the new database instance should be different")
def check_database_different(context):
    """Verify new instance is different"""
    assert context["database"] is not context["database2"]


# ==============================================================================
# Scenario: Get player repository returns instance
# ==============================================================================
@scenario("../../features/infrastructure/container.feature",
          "Get player repository returns instance")
def test_get_player_repository():
    pass


@given("I get the player repository instance")
@when("I get the player repository instance")
def get_player_repository(context):
    """Get player repository instance"""
    context["player_repo"] = container.get_player_repository()


@then("the player repository should not be null")
def check_player_repo_not_null(context):
    """Verify player repository is not null"""
    assert context["player_repo"] is not None


# ==============================================================================
# Scenario: Get player repository returns singleton
# ==============================================================================
@scenario("../../features/infrastructure/container.feature",
          "Get player repository returns singleton")
def test_player_repository_singleton():
    pass


@when("I get the player repository instance twice")
def get_player_repository_twice(context):
    """Get player repository instance twice"""
    context["player_repo1"] = container.get_player_repository()
    context["player_repo2"] = container.get_player_repository()


@then("both player repository instances should be the same")
def check_player_repo_same(context):
    """Verify both instances are the same"""
    assert context["player_repo1"] is context["player_repo2"]


# ==============================================================================
# Scenario: Get player repository creates new instance after reset
# ==============================================================================
@scenario("../../features/infrastructure/container.feature",
          "Get player repository creates new instance after reset")
def test_player_repository_reset():
    pass


@when("I get the player repository instance again")
def get_player_repository_again(context):
    """Get player repository instance again"""
    context["player_repo2"] = container.get_player_repository()


@then("the new player repository instance should be different")
def check_player_repo_different(context):
    """Verify new instance is different"""
    assert context["player_repo"] is not context["player_repo2"]


# ==============================================================================
# Scenario: Get API client for player returns instance
# ==============================================================================
@scenario("../../features/infrastructure/container.feature",
          "Get API client for player returns instance")
def test_get_api_client_for_player():
    pass


@given(parsers.parse('a player with id {player_id:d} and token "{token}" exists'))
def create_player_with_token(context, player_id, token):
    """Create a player in the database"""
    db = container.get_database()
    with db.transaction() as conn:
        conn.execute(
            "INSERT OR REPLACE INTO players (player_id, agent_symbol, token, created_at) VALUES (?, ?, ?, datetime('now'))",
            (player_id, f"TEST_AGENT_{player_id}", token)
        )
    context["player_id"] = player_id
    context["token"] = token


@when("I get the API client for the player")
def get_api_client_for_player(context):
    """Get API client for player"""
    context["api_client"] = container.get_api_client_for_player(context["player_id"])


@then("the API client should not be null")
def check_api_client_not_null(context):
    """Verify API client is not null"""
    assert context["api_client"] is not None


@then("the API client token should match the player token")
def check_api_client_token(context):
    """Verify API client has correct token"""
    assert context["api_client"]._token == context["token"]


# ==============================================================================
# Scenario: Get API client for player raises error for nonexistent player
# ==============================================================================
@scenario("../../features/infrastructure/container.feature",
          "Get API client for player raises error for nonexistent player")
def test_api_client_no_player():
    pass


@given(parsers.parse("player {player_id:d} does not exist"))
def ensure_player_not_exists(context, player_id):
    """Ensure player does not exist in database"""
    db = container.get_database()
    with db.transaction() as conn:
        conn.execute("DELETE FROM players WHERE player_id = ?", (player_id,))
    context["player_id"] = player_id


@when("I attempt to get the API client for the nonexistent player")
def attempt_get_api_client_for_player(context):
    """Attempt to get API client for nonexistent player and capture error"""
    try:
        context["api_client"] = container.get_api_client_for_player(context["player_id"])
        context["error"] = None
    except Exception as e:
        context["error"] = e


@then("the call should fail with ValueError")
def check_value_error(context):
    """Verify ValueError was raised"""
    assert isinstance(context["error"], ValueError)


@then(parsers.parse('the error message should mention "{text}"'))
def check_error_message(context, text):
    """Verify error message contains text"""
    assert text in str(context["error"])


# ==============================================================================
# Scenario: Get API client for player creates new instance each time
# ==============================================================================
@scenario("../../features/infrastructure/container.feature",
          "Get API client for player creates new instance each time")
def test_api_client_new_instance():
    pass


@when("I get the API client for the player twice")
def get_api_client_for_player_twice(context):
    """Get API client for player twice"""
    context["api_client1"] = container.get_api_client_for_player(context["player_id"])
    context["api_client2"] = container.get_api_client_for_player(context["player_id"])


@then("both API client instances should be different")
def check_api_client_different(context):
    """Verify both instances are different (not singleton)"""
    assert context["api_client1"] is not context["api_client2"]


# ==============================================================================
# Scenario: Get ship repository returns instance
# ==============================================================================
@scenario("../../features/infrastructure/container.feature",
          "Get ship repository returns instance")
def test_get_ship_repository():
    pass


@when("I get the ship repository instance")
def get_ship_repository(context):
    """Get ship repository instance"""
    context["ship_repo"] = container.get_ship_repository()


@then("the ship repository should not be null")
def check_ship_repo_not_null(context):
    """Verify ship repository is not null"""
    assert context["ship_repo"] is not None


# ==============================================================================
# Scenario: Get ship repository returns singleton
# ==============================================================================
@scenario("../../features/infrastructure/container.feature",
          "Get ship repository returns singleton")
def test_ship_repository_singleton():
    pass


@when("I get the ship repository instance twice")
def get_ship_repository_twice(context):
    """Get ship repository instance twice"""
    context["ship_repo1"] = container.get_ship_repository()
    context["ship_repo2"] = container.get_ship_repository()


@then("both ship repository instances should be the same")
def check_ship_repo_same(context):
    """Verify both instances are the same"""
    assert context["ship_repo1"] is context["ship_repo2"]


# ==============================================================================
# Scenario: Get route repository returns instance
# ==============================================================================
# Scenario: Get graph builder for player returns instance
# ==============================================================================
@scenario("../../features/infrastructure/container.feature",
          "Get graph builder for player returns instance")
def test_get_graph_builder_for_player():
    pass


@when("I get the graph builder for the player")
def get_graph_builder_for_player(context):
    """Get graph builder for player"""
    context["graph_builder"] = container.get_graph_builder_for_player(context["player_id"])


@then("the graph builder should not be null")
def check_graph_builder_not_null(context):
    """Verify graph builder is not null"""
    assert context["graph_builder"] is not None


# ==============================================================================
# Scenario: Get graph builder for player creates new instance each time
# ==============================================================================
@scenario("../../features/infrastructure/container.feature",
          "Get graph builder for player creates new instance each time")
def test_graph_builder_new_instance():
    pass


@when("I get the graph builder for the player twice")
def get_graph_builder_for_player_twice(context):
    """Get graph builder for player twice"""
    context["graph_builder1"] = container.get_graph_builder_for_player(context["player_id"])
    context["graph_builder2"] = container.get_graph_builder_for_player(context["player_id"])


@then("both graph builder instances should be different")
def check_graph_builder_different(context):
    """Verify both instances are different (not singleton)"""
    assert context["graph_builder1"] is not context["graph_builder2"]


# ==============================================================================
# Scenario: Get graph provider for player returns instance
# ==============================================================================
@scenario("../../features/infrastructure/container.feature",
          "Get graph provider for player returns instance")
def test_get_graph_provider_for_player():
    pass


@when("I get the graph provider for the player")
def get_graph_provider_for_player(context):
    """Get graph provider for player"""
    context["graph_provider"] = container.get_graph_provider_for_player(context["player_id"])


@then("the graph provider should not be null")
def check_graph_provider_not_null(context):
    """Verify graph provider is not null"""
    assert context["graph_provider"] is not None


# ==============================================================================
# Scenario: Get graph provider for player creates new instance each time
# ==============================================================================
@scenario("../../features/infrastructure/container.feature",
          "Get graph provider for player creates new instance each time")
def test_graph_provider_new_instance():
    pass


@when("I get the graph provider for the player twice")
def get_graph_provider_for_player_twice(context):
    """Get graph provider for player twice"""
    context["graph_provider1"] = container.get_graph_provider_for_player(context["player_id"])
    context["graph_provider2"] = container.get_graph_provider_for_player(context["player_id"])


@then("both graph provider instances should be different")
def check_graph_provider_different(context):
    """Verify both instances are different (not singleton)"""
    assert context["graph_provider1"] is not context["graph_provider2"]


# ==============================================================================
# Scenario: Get routing engine returns instance
# ==============================================================================
@scenario("../../features/infrastructure/container.feature",
          "Get routing engine returns instance")
def test_get_routing_engine():
    pass


@when("I get the routing engine instance")
def get_routing_engine(context):
    """Get routing engine instance"""
    context["routing_engine"] = container.get_routing_engine()


@then("the routing engine should not be null")
def check_routing_engine_not_null(context):
    """Verify routing engine is not null"""
    assert context["routing_engine"] is not None


# ==============================================================================
# Scenario: Get routing engine returns singleton
# ==============================================================================
@scenario("../../features/infrastructure/container.feature",
          "Get routing engine returns singleton")
def test_routing_engine_singleton():
    pass


@when("I get the routing engine instance twice")
def get_routing_engine_twice(context):
    """Get routing engine instance twice"""
    context["routing_engine1"] = container.get_routing_engine()
    context["routing_engine2"] = container.get_routing_engine()


@then("both routing engine instances should be the same")
def check_routing_engine_same(context):
    """Verify both instances are the same"""
    assert context["routing_engine1"] is context["routing_engine2"]


# ==============================================================================
# Scenario: Get mediator returns instance
# ==============================================================================
@scenario("../../features/infrastructure/container.feature",
          "Get mediator returns instance")
def test_get_mediator():
    pass


@given("I get the mediator instance")
@when("I get the mediator instance")
def get_mediator(context):
    """Get mediator instance"""
    context["mediator"] = container.get_mediator()


@then("the mediator should not be null")
def check_mediator_not_null(context):
    """Verify mediator is not null"""
    assert context["mediator"] is not None


# ==============================================================================
# Scenario: Get mediator returns singleton
# ==============================================================================
@scenario("../../features/infrastructure/container.feature",
          "Get mediator returns singleton")
def test_mediator_singleton():
    pass


@when("I get the mediator instance twice")
def get_mediator_twice(context):
    """Get mediator instance twice"""
    context["mediator1"] = container.get_mediator()
    context["mediator2"] = container.get_mediator()


@then("both mediator instances should be the same")
def check_mediator_same(context):
    """Verify both instances are the same"""
    assert context["mediator1"] is context["mediator2"]


# ==============================================================================
# Scenario: Mediator has behaviors registered
# ==============================================================================
@scenario("../../features/infrastructure/container.feature",
          "Mediator has behaviors registered")
def test_mediator_has_behaviors():
    pass


@then("the mediator should have behaviors or handlers attribute")
def check_mediator_has_behaviors(context):
    """Verify mediator has behaviors or handlers"""
    mediator = context["mediator"]
    assert hasattr(mediator, "_behaviors") or hasattr(mediator, "behaviors")


# ==============================================================================
# Scenario: Reset container resets all singletons
# ==============================================================================
@scenario("../../features/infrastructure/container.feature",
          "Reset container resets all singletons")
def test_reset_all_singletons():
    pass


@given("I get all singleton instances")
def get_all_singletons(context):
    """Get all singleton instances"""
    context["db1"] = container.get_database()
    context["player_repo1"] = container.get_player_repository()
    context["ship_repo1"] = container.get_ship_repository()
    context["routing_engine1"] = container.get_routing_engine()
    context["mediator1"] = container.get_mediator()


@when("I get all singleton instances again")
def get_all_singletons_again(context):
    """Get all singleton instances again"""
    context["db2"] = container.get_database()
    context["player_repo2"] = container.get_player_repository()
    context["ship_repo2"] = container.get_ship_repository()
    context["routing_engine2"] = container.get_routing_engine()
    context["mediator2"] = container.get_mediator()


@then("all new instances should be different from original instances")
def check_all_instances_different(context):
    """Verify all new instances are different"""
    assert context["db2"] is not context["db1"]
    assert context["player_repo2"] is not context["player_repo1"]
    assert context["ship_repo2"] is not context["ship_repo1"]
    assert context["routing_engine2"] is not context["routing_engine1"]
    assert context["mediator2"] is not context["mediator1"]


# ==============================================================================
# Scenario: Reset container can be called multiple times
# ==============================================================================
@scenario("../../features/infrastructure/container.feature",
          "Reset container can be called multiple times")
def test_reset_multiple_times():
    pass


@when(parsers.parse("I reset the container {count:d} times"))
def reset_container_multiple(context, count):
    """Reset container multiple times"""
    for _ in range(count):
        container.reset_container()
    context["reset_count"] = count


@then("no errors should occur")
def check_no_errors(context):
    """Verify no errors occurred"""
    assert context["reset_count"] == 3


# ==============================================================================
# Scenario: Reset container allows fresh configuration
# ==============================================================================
@scenario("../../features/infrastructure/container.feature",
          "Reset container allows fresh configuration")
def test_reset_fresh_configuration():
    pass


@when("I get the mediator instance again")
def get_mediator_again(context):
    """Get mediator instance again"""
    context["mediator2"] = container.get_mediator()


@then("both mediators should be valid but different")
def check_mediators_valid_different(context):
    """Verify both mediators are valid but different"""
    assert context["mediator"] is not None
    assert context["mediator2"] is not None
    assert context["mediator"] is not context["mediator2"]


# ==============================================================================
# Scenario: All repositories created successfully
# ==============================================================================
@scenario("../../features/infrastructure/container.feature",
          "All repositories created successfully")
def test_all_repositories_created():
    pass


@when("I create all repository instances")
def create_all_repositories(context):
    """Create all repository instances"""
    context["db"] = container.get_database()
    context["player_repo"] = container.get_player_repository()
    context["ship_repo"] = container.get_ship_repository()


@then("all repositories should be created without errors")
def check_all_repos_created(context):
    """Verify all repositories were created"""
    assert context["db"] is not None
    assert context["player_repo"] is not None
    assert context["ship_repo"] is not None
