"""Step definitions for find cheapest market feature"""
import pytest
import asyncio
from pytest_bdd import scenarios, given, when, then, parsers
from unittest.mock import Mock
from datetime import datetime, timezone, timedelta

from spacetraders.application.trading.queries.find_cheapest_market import (
    FindCheapestMarketQuery,
    FindCheapestMarketHandler,
    CheapestMarketResult
)

# Load all scenarios from the feature file
scenarios('../../../features/application/trading/find_cheapest_market.feature')


@pytest.fixture
def context():
    """Provide shared context dictionary for test data"""
    return {'markets': {}}


@given(parsers.parse('a player with agent "{agent_symbol}"'))
def player_with_agent(context, agent_symbol):
    """Register test player"""
    context['agent_symbol'] = agent_symbol
    context['player_id'] = 1


@given(parsers.parse('market "{waypoint}" sells "{good}" for {price:d} credits per unit'))
def market_sells_good(context, waypoint, good, price):
    """Add market data to context"""
    if waypoint not in context['markets']:
        context['markets'][waypoint] = []

    context['markets'][waypoint].append({
        'symbol': good,
        'sell_price': price,
        'last_updated': datetime.now(timezone.utc).isoformat()
    })


@when(parsers.parse('I search for cheapest market selling "{trade_symbol}" in system "{system}"'))
def search_cheapest_market(context, trade_symbol, system):
    """Execute find cheapest market query"""
    # Create mock database that returns our test market data
    mock_db = Mock()

    # Convert context markets to database format
    db_results = []
    for waypoint, goods in context['markets'].items():
        for good in goods:
            if good['symbol'] == trade_symbol:
                db_results.append({
                    'waypoint_symbol': waypoint,
                    'good_symbol': good['symbol'],
                    'sell_price': good['sell_price'],
                    'last_updated': good['last_updated']
                })

    # Sort by price (cheapest first)
    db_results.sort(key=lambda x: x['sell_price'])

    mock_db.find_cheapest_market_selling.return_value = db_results[0] if db_results else None

    # Create handler with mock database
    handler = FindCheapestMarketHandler(database=mock_db)

    query = FindCheapestMarketQuery(
        trade_symbol=trade_symbol,
        system=system,
        player_id=context['player_id']
    )

    try:
        result = asyncio.run(handler.handle(query))
        context['search_result'] = result
        context['search_error'] = None
    except Exception as e:
        context['search_result'] = None
        context['search_error'] = e


@then(parsers.parse('the cheapest market should be "{waypoint}"'))
def verify_cheapest_market(context, waypoint):
    """Verify the cheapest market was found"""
    assert context.get('search_result') is not None, "No market found"
    assert context['search_result'].waypoint_symbol == waypoint, \
        f"Expected waypoint {waypoint}, got {context['search_result'].waypoint_symbol}"


@then(parsers.parse('the price should be {price:d} credits per unit'))
def verify_price(context, price):
    """Verify the price at cheapest market"""
    assert context.get('search_result') is not None, "No market found"
    assert context['search_result'].sell_price == price, \
        f"Expected price {price}, got {context['search_result'].sell_price}"


@then('no market should be found')
def verify_no_market_found(context):
    """Verify no market was found"""
    assert context.get('search_result') is None, \
        f"Expected no market, but found: {context.get('search_result')}"
