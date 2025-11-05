"""Step definitions for market data queries"""
import pytest
import asyncio
from pytest_bdd import scenarios, given, when, then, parsers
from datetime import datetime, timezone

from application.scouting.queries.get_market_data import GetMarketDataQuery
from application.scouting.queries.list_market_data import ListMarketDataQuery
from domain.shared.market import TradeGood
from configuration.container import get_mediator, get_market_repository, reset_container

scenarios('../../../features/application/scouting/market_queries.feature')


@pytest.fixture
def context():
    """Shared context for scenario steps"""
    # Reset container to ensure clean state
    reset_container()
    return {
        'mediator': get_mediator(),
        'market_repo': get_market_repository()
    }


@pytest.fixture(autouse=True)
def cleanup():
    """Cleanup after each test"""
    yield
    reset_container()


# Background steps

@given(parsers.parse('a player with ID {player_id:d}'))
def player_id(context, player_id):
    """Create a player in the database"""
    context['player_id'] = player_id

    # Insert player into database to satisfy foreign key constraint
    from configuration.container import get_database
    db = get_database()
    with db.transaction() as conn:
        conn.execute("""
            INSERT OR IGNORE INTO players (player_id, agent_symbol, token, created_at)
            VALUES (?, ?, ?, ?)
        """, (player_id, f'TEST_AGENT_{player_id}', 'test_token', '2025-01-01T00:00:00Z'))


@given('market data exists for waypoint "X1-GZ7-A1" with goods:')
def market_data_a1(context, datatable):
    """Insert market data for X1-GZ7-A1"""
    from datetime import datetime, timezone
    # Use current time so freshness filter works correctly
    now = datetime.now(timezone.utc).isoformat()
    _insert_market_data(context, 'X1-GZ7-A1', datatable, now)


@given('market data exists for waypoint "X1-GZ7-B2" with goods:')
def market_data_b2(context, datatable):
    """Insert market data for X1-GZ7-B2"""
    from datetime import datetime, timezone
    # Use current time so freshness filter works correctly
    now = datetime.now(timezone.utc).isoformat()
    _insert_market_data(context, 'X1-GZ7-B2', datatable, now)


@given('market data exists for waypoint "X1-GZ7-C3" with goods:')
def market_data_c3(context, datatable):
    """Insert market data for X1-GZ7-C3"""
    context['c3_waypoint'] = 'X1-GZ7-C3'
    context['c3_datatable'] = datatable


@given(parsers.parse('the market data was last updated "{timestamp}"'))
def last_updated(context, timestamp):
    context['last_updated'] = timestamp


@given(parsers.parse('the last market update was "{timestamp}"'))
def last_market_update(context, timestamp):
    """Insert old market data for C3"""
    _insert_market_data(
        context,
        context['c3_waypoint'],
        context['c3_datatable'],
        timestamp
    )


def _insert_market_data(context, waypoint, datatable, timestamp):
    """Helper to insert market data"""
    headers = datatable[0]
    trade_goods = []

    for row in datatable[1:]:
        row_dict = dict(zip(headers, row))
        trade_goods.append(TradeGood(
            symbol=row_dict['symbol'],
            supply=row_dict.get('supply'),
            activity=row_dict.get('activity'),
            purchase_price=int(row_dict['purchase_price']),
            sell_price=int(row_dict['sell_price']),
            trade_volume=int(row_dict['trade_volume'])
        ))

    # Use repository to insert
    context['market_repo'].upsert_market_data(
        waypoint,
        trade_goods,
        timestamp,
        context['player_id']
    )


# Query steps

@when(parsers.parse('I query market data for waypoint "{waypoint}"'))
def query_market_data(context, waypoint):
    """Execute GetMarketDataQuery"""
    query = GetMarketDataQuery(
        waypoint_symbol=waypoint,
        player_id=context['player_id']
    )
    context['result'] = asyncio.run(context['mediator'].send_async(query))


@when(parsers.parse('I list markets in system "{system}"'))
def list_markets(context, system):
    """Execute ListMarketDataQuery without freshness filter"""
    query = ListMarketDataQuery(
        system=system,
        player_id=context['player_id']
    )
    context['result'] = asyncio.run(context['mediator'].send_async(query))


@when(parsers.parse('I list markets in system "{system}" with max age {minutes:d} minutes'))
def list_markets_with_age(context, system, minutes):
    """Execute ListMarketDataQuery with freshness filter"""
    query = ListMarketDataQuery(
        system=system,
        player_id=context['player_id'],
        max_age_minutes=minutes
    )
    context['result'] = asyncio.run(context['mediator'].send_async(query))


# Assertion steps

@then('I should receive market data')
def should_receive_data(context):
    assert context['result'] is not None, "Expected market data but got None"


@then('I should receive no market data')
def should_receive_no_data(context):
    assert context['result'] is None, "Expected None but got market data"


@then(parsers.parse('the market should have {count:d} trade goods'))
def market_goods_count(context, count):
    market = context['result']
    assert len(market.trade_goods) == count, \
        f"Expected {count} goods but got {len(market.trade_goods)}"


@then(parsers.parse('the market should be for waypoint "{waypoint}"'))
def market_waypoint(context, waypoint):
    market = context['result']
    assert market.waypoint_symbol == waypoint, \
        f"Expected waypoint {waypoint} but got {market.waypoint_symbol}"


@then(parsers.parse('I should receive {count:d} markets'))
def markets_count(context, count):
    markets = context['result']
    assert len(markets) == count, \
        f"Expected {count} markets but got {len(markets)}"


@then(parsers.parse('the markets should include "{waypoint}"'))
def markets_include(context, waypoint):
    markets = context['result']
    waypoints = [m.waypoint_symbol for m in markets]
    assert waypoint in waypoints, \
        f"Expected {waypoint} in markets but got {waypoints}"


@then(parsers.parse('the markets should not include "{waypoint}"'))
def markets_not_include(context, waypoint):
    markets = context['result']
    waypoints = [m.waypoint_symbol for m in markets]
    assert waypoint not in waypoints, \
        f"Did not expect {waypoint} in markets but it was present"
