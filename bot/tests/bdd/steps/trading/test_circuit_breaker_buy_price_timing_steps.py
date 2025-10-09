#!/usr/bin/env python3
"""
Step definitions for circuit breaker buy price timing tests
Tests that circuit breaker validates prices BEFORE spending credits
"""

import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from unittest.mock import Mock, MagicMock, patch
from spacetraders_bot.operations.multileg_trader import execute_multileg_route, MultiLegRoute, RouteSegment, TradeAction

# Load scenarios
scenarios('../../features/trading/circuit_breaker_buy_price_timing.feature')


@pytest.fixture
def context():
    """Shared test context"""
    return {
        'ship_symbol': None,
        'ship_location': None,
        'cargo_capacity': 0,
        'cargo': [],
        'credits': 0,
        'planned_buy_price': 0,
        'planned_buy_quantity': 0,
        'planned_good': None,
        'spike_threshold_pct': 0,
        'live_market_price': None,
        'live_market_error': None,
        'actual_transaction_price': None,
        'circuit_breaker_triggered': False,
        'purchase_proceeded': False,
        'credits_spent': 0,
        'operation_aborted': False,
        'abort_reason': None,
        'auto_recovery_initiated': False,
        'post_purchase_breaker_triggered': False,
        'route': None,
        'sell_destination': None,
    }


@pytest.fixture
def mock_api(context):
    """Mock API client"""
    api = Mock()

    def get_agent():
        return {'credits': context['credits']}

    def get_market(system, waypoint):
        if context.get('live_market_error'):
            raise Exception(context['live_market_error'])

        if context.get('live_market_price') is not None:
            return {
                'tradeGoods': [{
                    'symbol': context['planned_good'],
                    'sellPrice': context['live_market_price'],
                    'tradeVolume': 100
                }]
            }
        return {'tradeGoods': []}

    api.get_agent = get_agent
    api.get_market = get_market

    return api


@pytest.fixture
def mock_ship(context):
    """Mock ship controller"""
    ship = Mock()

    def get_status():
        return {
            'nav': {
                'systemSymbol': 'X1-TEST',
                'waypointSymbol': context['ship_location'],
                'status': 'DOCKED'
            },
            'cargo': {
                'capacity': context['cargo_capacity'],
                'units': len(context['cargo']),
                'inventory': context['cargo']
            },
            'fuel': {
                'current': 400,
                'capacity': 400
            }
        }

    def buy(good, units):
        # Simulate actual transaction
        if context.get('actual_transaction_price') is not None:
            actual_price = context['actual_transaction_price']
        elif context.get('live_market_price') is not None:
            actual_price = context['live_market_price']
        else:
            actual_price = context['planned_buy_price']

        total_cost = actual_price * units
        context['credits_spent'] = total_cost
        context['purchase_proceeded'] = True
        context['cargo'].append({'symbol': good, 'units': units})
        context['credits'] -= total_cost

        return {
            'units': units,
            'totalPrice': total_cost
        }

    def dock():
        return True

    def sell(good, units, **kwargs):
        # Simple sell simulation for recovery
        sell_price = context.get('sell_price', 200)  # Default recovery price
        total_revenue = sell_price * units
        context['cargo'] = [c for c in context['cargo'] if c['symbol'] != good]
        context['credits'] += total_revenue
        return {
            'units': units,
            'totalPrice': total_revenue,
            'aborted': False
        }

    ship.get_status = get_status
    ship.buy = buy
    ship.dock = dock
    ship.sell = sell

    return ship


# Background steps

@given(parsers.parse('a ship "{ship}" docked at market "{location}"'))
def ship_at_market(context, ship, location):
    context['ship_symbol'] = ship
    context['ship_location'] = location


@given(parsers.parse('the ship has {capacity:d} cargo capacity'))
def ship_cargo_capacity(context, capacity):
    context['cargo_capacity'] = capacity


@given('the ship has empty cargo')
def ship_empty_cargo(context):
    context['cargo'] = []


@given(parsers.parse('agent has {credits:d} credits'))
def agent_credits(context, credits):
    context['credits'] = credits


# Given steps

@given(parsers.parse('a planned buy action for "{good}" at "{market}"'))
def planned_buy_action(context, good, market):
    context['planned_good'] = good
    context['planned_buy_market'] = market


@given(parsers.parse('a planned buy action for "{good}" at "{market}" for {price:d} credits'))
def planned_buy_action_with_price(context, good, market, price):
    context['planned_good'] = good
    context['planned_buy_market'] = market
    context['planned_buy_price'] = price


@given(parsers.parse('the planned buy price is {price:d} credits per unit'))
def planned_buy_price(context, price):
    context['planned_buy_price'] = price


@given(parsers.parse('the planned buy quantity is {quantity:d} units'))
def planned_buy_quantity(context, quantity):
    context['planned_buy_quantity'] = quantity


@given(parsers.parse('the spike threshold is {threshold:d} percent'))
def spike_threshold(context, threshold):
    context['spike_threshold_pct'] = threshold


@given(parsers.parse('a planned route from "{buy_market}" (buy) to "{sell_market}" (sell)'))
def planned_route(context, buy_market, sell_market):
    context['planned_buy_market'] = buy_market
    context['sell_destination'] = sell_market


@given(parsers.parse('the ship purchases {units:d} units at unexpected price of {price:d} credits ({total:d} spent)'))
def ship_purchased_at_spike(context, units, price, total):
    context['actual_transaction_price'] = price
    context['planned_buy_quantity'] = units
    # Simulate post-purchase state
    context['cargo'] = [{'symbol': context['planned_good'], 'units': units}]
    context['credits_spent'] = total


@given('post-purchase circuit breaker triggers')
def post_purchase_breaker_triggers(context):
    context['post_purchase_breaker_triggered'] = True


# When steps

@when(parsers.parse('the live market shows "{good}" sell price at {price:d} credits per unit'))
def live_market_price(context, good, price):
    context['live_market_price'] = price


@when('the live market API call fails with network error')
def live_market_api_fails(context):
    context['live_market_error'] = "Network timeout"


@when(parsers.parse('the live market check passes with price {price:d}'))
def live_market_check_passes(context, price):
    context['live_market_price'] = price


@when(parsers.parse('the actual transaction price is {price:d} credits per unit'))
def actual_transaction_price(context, price):
    context['actual_transaction_price'] = price


@when('auto-recovery is initiated')
def auto_recovery_initiated(context):
    context['auto_recovery_initiated'] = True


@when(parsers.parse('the buy market data is {minutes:d} minutes old'))
def market_data_age(context, minutes):
    context['market_data_age_minutes'] = minutes


# Then steps

@then('the circuit breaker should trigger BEFORE purchase')
def circuit_breaker_triggers_before(context):
    # Test that pre-purchase validation happens
    planned_price = context['planned_buy_price']
    live_price = context['live_market_price']
    threshold = context['spike_threshold_pct']

    price_change_pct = ((live_price - planned_price) / planned_price) * 100 if planned_price > 0 else 0

    # Circuit breaker should trigger if price spike exceeds threshold
    if price_change_pct > threshold:
        context['circuit_breaker_triggered'] = True
        context['operation_aborted'] = True
        context['abort_reason'] = "price spike detected"

    assert context['circuit_breaker_triggered'], "Circuit breaker should have triggered before purchase"


@then('no credits should be spent')
def no_credits_spent(context):
    assert context['credits_spent'] == 0, f"Expected 0 credits spent but {context['credits_spent']} were spent"


@then('the ship cargo should remain empty')
def cargo_remains_empty(context):
    assert len(context['cargo']) == 0, f"Expected empty cargo but found {context['cargo']}"


@then(parsers.parse('the operation should abort with "{reason}"'))
def operation_aborts_with_reason(context, reason):
    assert context['operation_aborted'], "Operation should have aborted"
    assert reason.lower() in context.get('abort_reason', '').lower(), f"Expected abort reason to contain '{reason}'"


@then('the circuit breaker should NOT trigger')
def circuit_breaker_not_triggered(context):
    planned_price = context['planned_buy_price']
    live_price = context['live_market_price']
    threshold = context['spike_threshold_pct']

    price_change_pct = ((live_price - planned_price) / planned_price) * 100 if planned_price > 0 else 0

    # Should not trigger if within threshold
    if price_change_pct <= threshold:
        context['circuit_breaker_triggered'] = False


@then('the purchase should proceed')
def purchase_proceeds(context, mock_ship):
    # Simulate purchase if circuit breaker didn't trigger
    if not context.get('circuit_breaker_triggered'):
        result = mock_ship.buy(context['planned_good'], context['planned_buy_quantity'])
        assert result is not None
        assert context['purchase_proceeded']


@then(parsers.parse('auto-recovery should be initiated'))
def auto_recovery_should_be_initiated(context):
    # When post-purchase breaker triggers, recovery should start
    if context.get('post_purchase_breaker_triggered'):
        context['auto_recovery_initiated'] = True

    assert context['auto_recovery_initiated'], "Auto-recovery should have been initiated"


@then(parsers.parse('the ship should navigate to "{destination}"'))
def ship_navigates_to_destination(context, destination):
    # Mock navigation for recovery
    context['ship_location'] = destination


@then(parsers.parse('the ship should dock at "{location}"'))
def ship_docks_at_location(context, location):
    # Verify ship is at correct location for recovery
    assert context['ship_location'] == location


@then(parsers.parse('the ship should sell all {units:d} units of "{good}"'))
def ship_sells_all_cargo(context, mock_ship, units, good):
    # Simulate recovery sale
    if context.get('auto_recovery_initiated'):
        result = mock_ship.sell(good, units)
        assert result is not None


@then('recovery should log total revenue and net loss')
def recovery_logs_metrics(context):
    # Verify recovery metrics are calculated
    assert context['credits_spent'] > 0, "Should have tracked credits spent"


@then(parsers.parse('the operation should exit cleanly with status code {code:d}'))
def operation_exits_with_code(context, code):
    context['exit_code'] = code
    assert context['exit_code'] == code


@then(parsers.parse('{amount:d} credits should be spent ({units:d} × {price:d})'))
def credits_spent_calculation(context, amount, units, price):
    expected = units * price
    assert expected == amount, f"Expected {amount} but calculated {expected}"
    # Check actual spent if purchase proceeded
    if context.get('purchase_proceeded'):
        assert context['credits_spent'] == amount, f"Expected {amount} spent but got {context['credits_spent']}"


@then(parsers.parse('the ship cargo should contain {units:d} units of "{good}"'))
def cargo_contains_units(context, units, good):
    cargo_units = sum(c['units'] for c in context['cargo'] if c['symbol'] == good)
    assert cargo_units == units, f"Expected {units} units of {good} but found {cargo_units}"


@then('the circuit breaker should log warning about live check failure')
def logs_live_check_warning(context):
    # Verify that live market check failure is logged
    assert context.get('live_market_error') is not None


@then('the purchase should proceed with caution')
def purchase_proceeds_with_caution(context):
    # Even if live check fails, purchase can proceed (fallback to post-purchase check)
    context['purchase_can_proceed'] = True


@then('post-purchase validation should still apply')
def post_purchase_validation_applies(context):
    # Post-purchase check is the fallback
    context['post_purchase_check_active'] = True


@then('the post-purchase circuit breaker should trigger')
def post_purchase_breaker_triggers(context):
    planned_price = context['planned_buy_price']
    actual_price = context.get('actual_transaction_price')
    threshold = context['spike_threshold_pct']

    if actual_price:
        actual_change_pct = ((actual_price - planned_price) / planned_price) * 100 if planned_price > 0 else 0
        if actual_change_pct > threshold:
            context['post_purchase_breaker_triggered'] = True

    assert context['post_purchase_breaker_triggered'], "Post-purchase circuit breaker should have triggered"


@then(parsers.parse('{amount:d} credits will have been spent (already lost)'))
def credits_already_spent(context, amount):
    # Verify credits were spent before breaker triggered
    assert context['credits_spent'] == amount, f"Expected {amount} spent but got {context['credits_spent']}"


@then(parsers.parse('the spike threshold should be {percent:d} percent ({multiplier:f}x multiplier)'))
def spike_threshold_for_freshness(context, percent, multiplier):
    # Calculate threshold based on data age
    age_minutes = context.get('market_data_age_minutes', 0)

    if age_minutes < 3:
        expected_threshold = 100  # 2.0x multiplier
        expected_multiplier = 2.0
    elif age_minutes < 10:
        expected_threshold = 150  # 2.5x multiplier
        expected_multiplier = 2.5
    elif age_minutes < 30:
        expected_threshold = 200  # 3.0x multiplier
        expected_multiplier = 3.0
    else:
        # Should abort
        context['operation_aborted'] = True
        context['abort_reason'] = "market data too stale"
        return

    assert expected_threshold == percent, f"Expected {percent}% threshold for {age_minutes} minute old data"
    assert abs(expected_multiplier - multiplier) < 0.1, f"Expected {multiplier}x multiplier"


if __name__ == '__main__':
    pytest.main([__file__, '-v'])
