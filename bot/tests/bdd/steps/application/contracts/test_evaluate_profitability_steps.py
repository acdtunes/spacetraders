"""Step definitions for evaluate contract profitability feature"""
import pytest
import asyncio
from pytest_bdd import scenarios, given, when, then, parsers
from unittest.mock import Mock, AsyncMock
from datetime import datetime, timezone, timedelta

from application.contracts.queries.evaluate_profitability import (
    EvaluateContractProfitabilityQuery,
    EvaluateContractProfitabilityHandler,
    ProfitabilityResult
)
from domain.shared.contract import Contract, ContractTerms, Delivery, Payment
from domain.shared.value_objects import Waypoint

# Load all scenarios from the feature file
scenarios('../../../features/application/contracts/evaluate_profitability.feature')


@pytest.fixture
def context():
    """Provide shared context dictionary for test data"""
    return {'deliveries': [], 'markets': {}}


@given(parsers.parse('a player with agent "{agent_symbol}"'))
def player_with_agent(context, agent_symbol):
    """Register test player"""
    context['agent_symbol'] = agent_symbol
    context['player_id'] = 1


@given(parsers.parse('a contract pays {on_accepted:d} credits on acceptance and {on_fulfilled:d} on fulfillment'))
def contract_payment(context, on_accepted, on_fulfilled):
    """Set contract payment terms"""
    context['payment'] = Payment(
        on_accepted=on_accepted,
        on_fulfilled=on_fulfilled
    )


@given(parsers.parse('the contract requires {units:d} units of "{trade_symbol}" delivery'))
def contract_delivery_requirement(context, units, trade_symbol):
    """Add delivery requirement to contract"""
    delivery = Delivery(
        trade_symbol=trade_symbol,
        destination=Waypoint(symbol="X1-TEST-DEST", x=0.0, y=0.0),
        units_required=units,
        units_fulfilled=0
    )
    context['deliveries'].append(delivery)


@given(parsers.parse('the cheapest market sells "{trade_symbol}" for {price:d} credits per unit'))
def market_price(context, trade_symbol, price):
    """Set market price for a good"""
    context['markets'][trade_symbol] = {
        'waypoint_symbol': 'X1-TEST-M1',
        'sell_price': price
    }


@given(parsers.parse('no market sells "{trade_symbol}"'))
def no_market_sells(context, trade_symbol):
    """Mark that no market sells this good"""
    context['markets'][trade_symbol] = None


@given(parsers.parse('the ship has cargo capacity of {capacity:d} units'))
def ship_cargo_capacity(context, capacity):
    """Set ship cargo capacity"""
    context['cargo_capacity'] = capacity


@given(parsers.parse('estimated fuel cost per trip is {fuel_cost:d} credits'))
def fuel_cost_per_trip(context, fuel_cost):
    """Set estimated fuel cost per trip"""
    context['fuel_cost_per_trip'] = fuel_cost


@when('I evaluate contract profitability')
def evaluate_profitability(context):
    """Execute profitability evaluation query"""
    # Create contract from context
    terms = ContractTerms(
        deadline=datetime.now(timezone.utc) + timedelta(days=7),
        payment=context['payment'],
        deliveries=context['deliveries']
    )

    contract = Contract(
        contract_id="TEST-CONTRACT-1",
        faction_symbol="COSMIC",
        type="PROCUREMENT",
        terms=terms,
        accepted=False,
        fulfilled=False,
        deadline_to_accept=datetime.now(timezone.utc) + timedelta(days=1)
    )

    # Mock FindCheapestMarketQuery handler
    mock_find_market_handler = Mock()

    async def mock_find_cheapest(query):
        market_data = context['markets'].get(query.trade_symbol)
        if market_data is None:
            return None
        from application.trading.queries.find_cheapest_market import CheapestMarketResult
        return CheapestMarketResult(
            waypoint_symbol=market_data['waypoint_symbol'],
            trade_symbol=query.trade_symbol,
            sell_price=market_data['sell_price']
        )

    mock_find_market_handler.handle = AsyncMock(side_effect=mock_find_cheapest)

    # Create handler with mocked dependencies
    handler = EvaluateContractProfitabilityHandler(
        find_market_handler=mock_find_market_handler
    )

    query = EvaluateContractProfitabilityQuery(
        contract=contract,
        cargo_capacity=context['cargo_capacity'],
        fuel_cost_per_trip=context.get('fuel_cost_per_trip', 0),
        player_id=context['player_id']
    )

    try:
        result = asyncio.run(handler.handle(query))
        context['profitability_result'] = result
        context['profitability_error'] = None
    except Exception as e:
        context['profitability_result'] = None
        context['profitability_error'] = e


@then('the contract should be profitable')
def verify_profitable(context):
    """Verify contract is profitable"""
    error = context.get('profitability_error')
    if error:
        raise AssertionError(f"Handler raised exception: {error}")
    assert context.get('profitability_result') is not None, "No profitability result"
    assert context['profitability_result'].is_profitable is True, \
        f"Expected profitable, but got is_profitable={context['profitability_result'].is_profitable}"


@then('the contract should not be profitable')
def verify_not_profitable(context):
    """Verify contract is not profitable"""
    assert context.get('profitability_result') is not None, "No profitability result"
    assert context['profitability_result'].is_profitable is False, \
        f"Expected not profitable, but got is_profitable={context['profitability_result'].is_profitable}"


@then(parsers.parse('the net profit should be {profit:d} credits'))
def verify_net_profit(context, profit):
    """Verify net profit amount"""
    assert context.get('profitability_result') is not None, "No profitability result"
    assert context['profitability_result'].net_profit == profit, \
        f"Expected net profit {profit}, got {context['profitability_result'].net_profit}"


@then(parsers.parse('{trips:d} trip should be required'))
@then(parsers.parse('{trips:d} trips should be required'))
def verify_trips_required(context, trips):
    """Verify number of trips required"""
    assert context.get('profitability_result') is not None, "No profitability result"
    assert context['profitability_result'].trips_required == trips, \
        f"Expected {trips} trips, got {context['profitability_result'].trips_required}"


@then(parsers.parse('the evaluation should indicate "{message}"'))
def verify_error_message(context, message):
    """Verify error message in result"""
    assert context.get('profitability_result') is not None, "No profitability result"
    assert message.lower() in context['profitability_result'].reason.lower(), \
        f"Expected message containing '{message}', got: {context['profitability_result'].reason}"
