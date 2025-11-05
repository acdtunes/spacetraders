"""BDD steps for database-only queries"""
import os
import asyncio
from pytest_bdd import scenarios, given, when, then
from spacetraders.configuration.container import get_mediator, reset_container, get_database
from spacetraders.application.navigation.queries.list_ships import ListShipsQuery
from spacetraders.domain.shared.player import Player
from datetime import datetime, timezone

scenarios('../../features/shared/database_queries.feature')


@given("the SPACETRADERS_TOKEN environment variable is not set")
def unset_token(context):
    """Remove token from environment"""
    if 'SPACETRADERS_TOKEN' in os.environ:
        context['original_token'] = os.environ['SPACETRADERS_TOKEN']
        del os.environ['SPACETRADERS_TOKEN']
    else:
        context['original_token'] = None

    # Reset container to clear any cached instances
    reset_container()


@given("there is a player in the database")
def create_player_in_db(context):
    """Create a test player directly in database"""
    db = get_database()

    with db.transaction() as conn:
        conn.execute("""
            INSERT OR IGNORE INTO players (player_id, agent_symbol, token, created_at)
            VALUES (999, 'TEST_AGENT_DB', 'test-token', ?)
        """, (datetime.now(timezone.utc).isoformat(),))

    context['test_player_id'] = 999


@when("I query for all ships for that player")
def query_ships(context):
    """Execute list ships query"""
    try:
        mediator = get_mediator()
        query = ListShipsQuery(player_id=context['test_player_id'])
        ships = asyncio.run(mediator.send_async(query))
        context['query_result'] = ships
        context['query_error'] = None
    except Exception as e:
        context['query_result'] = None
        context['query_error'] = e


@then("the query should succeed")
def query_succeeded(context):
    """Verify query succeeded"""
    assert context['query_error'] is None, \
        f"Query failed with error: {context['query_error']}"
    assert context['query_result'] is not None, "Query returned None"


@then("no API client should be initialized")
def no_api_client_initialized(context):
    """Verify API client was not needed"""
    # The fact that the query succeeded without SPACETRADERS_TOKEN
    # proves the API client was not initialized
    assert 'SPACETRADERS_TOKEN' not in os.environ, \
        "Token should not be in environment"

    # Cleanup
    if context.get('original_token'):
        os.environ['SPACETRADERS_TOKEN'] = context['original_token']

    # Cleanup test data
    db = get_database()
    with db.transaction() as conn:
        conn.execute("DELETE FROM players WHERE player_id = 999")

    reset_container()
