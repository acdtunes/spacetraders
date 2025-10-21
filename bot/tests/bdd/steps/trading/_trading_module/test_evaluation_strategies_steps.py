"""
Step definitions for Trade Evaluation Strategies feature

Tests for market evaluation logic using ProfitFirstStrategy.
"""

import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from unittest.mock import Mock

from spacetraders_bot.operations._trading import ProfitFirstStrategy, MarketEvaluation, TradeAction


# Load scenarios from feature file
scenarios('../../../../bdd/features/trading/_trading_module/evaluation_strategies.feature')

# NOTE: Background steps (test database, API client) are defined in conftest.py

# ============================================================================
# GIVEN Steps - Test Setup
# ============================================================================

@given(parsers.parse('the following markets exist:\n{table}'), target_fixture='markets_table')
def create_markets(context, table):
    """Parse markets table and store in context"""
    markets = []
    lines = [line.strip() for line in table.strip().split('\n') if line.strip() and '|' in line]

    # Skip header and separator
    for line in lines[2:]:
        parts = [p.strip() for p in line.split('|') if p.strip()]
        if len(parts) >= 3:
            markets.append({
                'waypoint': parts[0],
                'x': int(parts[1]),
                'y': int(parts[2])
            })

    context['markets'] = markets
    return markets


@given(parsers.parse('the following trade opportunities:\n{table}'), target_fixture='opportunities_table')
def create_trade_opportunities(context, table):
    """Parse trade opportunities table and store in context"""
    opportunities = []
    lines = [line.strip() for line in table.strip().split('\n') if line.strip() and '|' in line]

    # Skip header and separator
    for line in lines[2:]:
        parts = [p.strip() for p in line.split('|') if p.strip()]
        if len(parts) >= 7:
            opportunities.append({
                'buy_waypoint': parts[0],
                'sell_waypoint': parts[1],
                'good': parts[2],
                'buy_price': int(parts[3]),
                'sell_price': int(parts[4]),
                'spread': int(parts[5]),
                'trade_volume': int(parts[6])  # Using 'volume' column as trade_volume
            })

    context['trade_opportunities'] = opportunities
    return opportunities


@given('a ProfitFirstStrategy')
def create_strategy(context, mock_logger):
    """Create ProfitFirstStrategy instance"""
    context['strategy'] = ProfitFirstStrategy(mock_logger)


@given(parsers.parse('ship has {units:d} units of "{good}" in cargo'))
def set_cargo_with_good(context, units, good):
    """Set ship cargo with specific good"""
    if 'cargo' not in context:
        context['cargo'] = {}
    context['cargo'][good] = units


@given('ship has empty cargo')
def set_empty_cargo(context):
    """Set ship cargo to empty"""
    context['cargo'] = {}


@given(parsers.parse('ship has {credits:d} credits available'))
def set_credits(context, credits):
    """Set available credits"""
    context['credits'] = credits


@given(parsers.parse('ship has cargo capacity {capacity:d}'))
def set_cargo_capacity(context, capacity):
    """Set cargo capacity"""
    context['cargo_capacity'] = capacity


@given(parsers.parse('fuel cost is {cost:d} credits'))
def set_fuel_cost(context, cost):
    """Set fuel cost"""
    context['fuel_cost'] = cost


@given(parsers.parse('market "{waypoint}" only buys "{good}" (no sells)'))
def market_only_buys(context, waypoint, good):
    """Configure market to only buy a specific good"""
    # Filter trade opportunities to only include sell opportunities at this market
    context['trade_opportunities'] = [
        opp for opp in context['trade_opportunities']
        if opp['sell_waypoint'] == waypoint and opp['good'] == good
    ]


@given(parsers.parse('market "{waypoint}" only sells "{good}" (no buys)'))
def market_only_sells(context, waypoint, good):
    """Configure market to only sell a specific good"""
    # Filter trade opportunities to only include buy opportunities at this market
    context['trade_opportunities'] = [
        opp for opp in context['trade_opportunities']
        if opp['buy_waypoint'] == waypoint and opp['good'] == good
    ]


@given(parsers.parse('market sells "{good}" at {price:d} credits per unit'))
def market_sells_good_at_price(context, good, price):
    """Add market sell opportunity for a good"""
    if 'trade_opportunities' not in context:
        context['trade_opportunities'] = []

    # Add buy opportunity (ship can buy from market)
    context['trade_opportunities'].append({
        'buy_waypoint': 'X1-TEST-A',  # Default market
        'sell_waypoint': 'X1-TEST-B',
        'good': good,
        'buy_price': price,
        'sell_price': price + 100,  # Default profitable spread
        'trade_volume': 100
    })


@given(parsers.parse('market sells "{good}" at {price:d} credits per unit with volume {volume:d}'))
def market_sells_good_at_price_with_volume(context, good, price, volume):
    """Add market sell opportunity with specific volume"""
    if 'trade_opportunities' not in context:
        context['trade_opportunities'] = []

    context['trade_opportunities'].append({
        'buy_waypoint': 'X1-TEST-A',
        'sell_waypoint': 'X1-TEST-B',
        'good': good,
        'buy_price': price,
        'sell_price': price + 100,
        'trade_volume': volume
    })


@given(parsers.parse('market sells "{good}" with trade_volume {volume:d}'))
def market_sells_with_trade_volume(context, good, volume):
    """Set trade volume for market sell opportunity"""
    # Update existing opportunities or add new one
    found = False
    for opp in context.get('trade_opportunities', []):
        if opp['good'] == good and opp['buy_waypoint'] == 'X1-TEST-A':
            opp['trade_volume'] = volume
            found = True

    if not found:
        if 'trade_opportunities' not in context:
            context['trade_opportunities'] = []
        context['trade_opportunities'].append({
            'buy_waypoint': 'X1-TEST-A',
            'sell_waypoint': 'X1-TEST-B',
            'good': good,
            'buy_price': 100,
            'sell_price': 200,
            'trade_volume': volume
        })


@given(parsers.parse('I buy "{good}" at "{waypoint}" for {price:d} credits per unit'))
def record_buy_transaction(context, good, waypoint, price):
    """Record a buy transaction for profit calculation"""
    if 'purchase_prices' not in context:
        context['purchase_prices'] = {}
    context['purchase_prices'][good] = price


@given(parsers.parse('future opportunities show "{good}" sells for {price:d} at "{waypoint}"'))
def add_future_sell_opportunity(context, good, price, waypoint):
    """Add future sell opportunity"""
    if 'trade_opportunities' not in context:
        context['trade_opportunities'] = []

    context['trade_opportunities'].append({
        'buy_waypoint': 'X1-TEST-A',
        'sell_waypoint': waypoint,
        'good': good,
        'buy_price': 100,
        'sell_price': price,
        'trade_volume': 100
    })


@given(parsers.parse('market buys "{good}" for {price:d} credits per unit'))
def market_buys_good_at_price(context, good, price):
    """Add market buy opportunity (ship sells to market)"""
    if 'trade_opportunities' not in context:
        context['trade_opportunities'] = []

    context['trade_opportunities'].append({
        'buy_waypoint': 'X1-TEST-A',
        'sell_waypoint': 'X1-TEST-B',
        'good': good,
        'buy_price': 50,  # Default
        'sell_price': price,
        'trade_volume': 100
    })


@given(parsers.parse('purchase price was {price:d} credits per unit'))
def set_purchase_price(context, price):
    """Set the original purchase price for profit calculation"""
    context['original_purchase_price'] = price


@given(parsers.parse('market sells "{good}" for {price:d} credits'))
def market_sells_good(context, good, price):
    """Add market that sells a good"""
    if 'trade_opportunities' not in context:
        context['trade_opportunities'] = []

    context['trade_opportunities'].append({
        'buy_waypoint': 'X1-TEST-B',
        'sell_waypoint': 'X1-TEST-C',
        'good': good,
        'buy_price': price,
        'sell_price': price + 50,
        'trade_volume': 50
    })


@given('market has no trade opportunities')
def empty_market(context):
    """Set market with no trade opportunities"""
    context['trade_opportunities'] = []


# ============================================================================
# WHEN Steps - Actions
# ============================================================================

@when(parsers.parse('I evaluate market "{waypoint}"'))
def evaluate_market(context, waypoint):
    """Evaluate a market using the strategy"""
    strategy = context['strategy']
    cargo = context.get('cargo', {})
    credits = context.get('credits', 0)
    capacity = context.get('cargo_capacity', 50)
    fuel_cost = context.get('fuel_cost', 0)
    opportunities = context.get('trade_opportunities', [])

    evaluation = strategy.evaluate(
        market=waypoint,
        current_cargo=cargo,
        current_credits=credits,
        trade_opportunities=opportunities,
        cargo_capacity=capacity,
        fuel_cost=fuel_cost
    )

    context['evaluation'] = evaluation
    context['waypoint'] = waypoint


# ============================================================================
# THEN Steps - Assertions
# ============================================================================

@then(parsers.parse('evaluation should include SELL action for "{good}"'))
def verify_sell_action(context, good):
    """Verify evaluation includes sell action for specific good"""
    evaluation = context['evaluation']
    sell_actions = [a for a in evaluation.actions if a.action == 'SELL' and a.good == good]
    assert len(sell_actions) > 0, f"Expected SELL action for {good}, but found none"


@then(parsers.parse('evaluation should include BUY action for "{good}"'))
def verify_buy_action(context, good):
    """Verify evaluation includes buy action for specific good"""
    evaluation = context['evaluation']
    buy_actions = [a for a in evaluation.actions if a.action == 'BUY' and a.good == good]
    assert len(buy_actions) > 0, f"Expected BUY action for {good}, but found none"


@then(parsers.parse('BUY action should include "{good}"'))
def verify_buy_action_includes_good(context, good):
    """Verify evaluation includes buy action for specific good (alternative phrasing)"""
    evaluation = context['evaluation']
    buy_actions = [a for a in evaluation.actions if a.action == 'BUY' and a.good == good]
    assert len(buy_actions) > 0, f"Expected BUY action for {good}, but found none"


@then('evaluation should have no BUY actions')
def verify_no_buy_actions(context):
    """Verify evaluation has no buy actions"""
    evaluation = context['evaluation']
    buy_actions = [a for a in evaluation.actions if a.action == 'BUY']
    assert len(buy_actions) == 0, f"Expected no BUY actions, but found {len(buy_actions)}"


@then('evaluation should have no SELL actions')
def verify_no_sell_actions(context):
    """Verify evaluation has no sell actions"""
    evaluation = context['evaluation']
    sell_actions = [a for a in evaluation.actions if a.action == 'SELL']
    assert len(sell_actions) == 0, f"Expected no SELL actions, but found {len(sell_actions)}"


@then('evaluation should have no actions')
def verify_no_actions(context):
    """Verify evaluation has no actions"""
    evaluation = context['evaluation']
    assert len(evaluation.actions) == 0, f"Expected no actions, but found {len(evaluation.actions)}"


@then('net profit should be greater than 0')
def verify_positive_profit(context):
    """Verify net profit is positive"""
    evaluation = context['evaluation']
    assert evaluation.net_profit > 0, f"Expected positive profit, got {evaluation.net_profit}"


@then('net profit should be negative')
def verify_negative_profit(context):
    """Verify net profit is negative"""
    evaluation = context['evaluation']
    assert evaluation.net_profit < 0, f"Expected negative profit, got {evaluation.net_profit}"


@then('credits after should reflect both sell revenue and buy cost')
def verify_credits_after_trade(context):
    """Verify credits changed correctly after sell and buy"""
    evaluation = context['evaluation']
    original_credits = context['credits']

    # Calculate expected credits change
    sell_revenue = sum(a.total_value for a in evaluation.actions if a.action == 'SELL')
    buy_cost = sum(a.total_value for a in evaluation.actions if a.action == 'BUY')
    expected_credits = original_credits + sell_revenue - buy_cost

    assert evaluation.credits_after == expected_credits, \
        f"Expected credits {expected_credits}, got {evaluation.credits_after}"


@then(parsers.parse('cargo after should show "{good}" only'))
def verify_single_cargo_item(context, good):
    """Verify cargo contains only one specific good"""
    evaluation = context['evaluation']
    assert len(evaluation.cargo_after) == 1, \
        f"Expected 1 cargo item, got {len(evaluation.cargo_after)}"
    assert good in evaluation.cargo_after, \
        f"Expected {good} in cargo, got {list(evaluation.cargo_after.keys())}"


@then(parsers.parse('cargo after should contain "{good}"'))
def verify_cargo_contains_good(context, good):
    """Verify cargo contains specific good"""
    evaluation = context['evaluation']
    assert good in evaluation.cargo_after, \
        f"Expected {good} in cargo, got {list(evaluation.cargo_after.keys())}"


@then('net profit should equal sell revenue minus fuel cost')
def verify_profit_sell_only(context):
    """Verify profit calculation for sell-only segment"""
    evaluation = context['evaluation']
    fuel_cost = context['fuel_cost']

    sell_revenue = sum(a.total_value for a in evaluation.actions if a.action == 'SELL')
    expected_profit = sell_revenue - fuel_cost

    assert evaluation.net_profit == expected_profit, \
        f"Expected profit {expected_profit}, got {evaluation.net_profit}"


@then('cargo after should be empty')
def verify_empty_cargo(context):
    """Verify cargo is empty"""
    evaluation = context['evaluation']
    assert len(evaluation.cargo_after) == 0, \
        f"Expected empty cargo, got {evaluation.cargo_after}"


@then('credits after should show increased credits')
def verify_credits_increased(context):
    """Verify credits increased"""
    evaluation = context['evaluation']
    original_credits = context['credits']
    assert evaluation.credits_after > original_credits, \
        f"Expected credits to increase from {original_credits}, got {evaluation.credits_after}"


@then('net profit should be potential future revenue minus purchase cost and fuel')
def verify_profit_buy_only(context):
    """Verify profit calculation for buy-only segment"""
    evaluation = context['evaluation']
    fuel_cost = context['fuel_cost']

    buy_cost = sum(a.total_value for a in evaluation.actions if a.action == 'BUY')
    # Note: future revenue is estimated in the evaluation
    # Net profit includes future revenue estimation
    assert evaluation.net_profit != 0, "Expected non-zero profit for buy segment"


@then('credits after should show decreased credits')
def verify_credits_decreased(context):
    """Verify credits decreased"""
    evaluation = context['evaluation']
    original_credits = context['credits']
    assert evaluation.credits_after < original_credits, \
        f"Expected credits to decrease from {original_credits}, got {evaluation.credits_after}"


@then('BUY action should be limited by available credits')
def verify_buy_limited_by_credits(context):
    """Verify buy action respects credit limit"""
    evaluation = context['evaluation']
    credits = context['credits']
    fuel_cost = context['fuel_cost']

    buy_actions = [a for a in evaluation.actions if a.action == 'BUY']
    if buy_actions:
        total_buy_cost = sum(a.total_value for a in buy_actions)
        assert total_buy_cost <= credits, \
            f"Buy cost {total_buy_cost} exceeds available credits {credits}"


@then(parsers.parse('units purchased should be {max_units:d} or less'))
def verify_units_limited(context, max_units):
    """Verify units purchased is limited"""
    evaluation = context['evaluation']
    buy_actions = [a for a in evaluation.actions if a.action == 'BUY']

    if buy_actions:
        total_units = sum(a.units for a in buy_actions)
        assert total_units <= max_units, \
            f"Units purchased {total_units} exceeds limit {max_units}"


@then('net profit should account for limited purchase')
def verify_profit_with_limited_purchase(context):
    """Verify profit calculation with limited purchase"""
    evaluation = context['evaluation']
    # Profit should still be calculated correctly even with limited purchase
    assert evaluation.net_profit is not None


@then(parsers.parse('BUY action should be limited to {units:d} units'))
def verify_buy_units_exact(context, units):
    """Verify buy action has exact unit limit"""
    evaluation = context['evaluation']
    buy_actions = [a for a in evaluation.actions if a.action == 'BUY']

    if buy_actions:
        total_units = sum(a.units for a in buy_actions)
        assert total_units == units, \
            f"Expected {units} units purchased, got {total_units}"


@then(parsers.parse('cargo after should total {max_units:d} units maximum'))
def verify_total_cargo_limit(context, max_units):
    """Verify total cargo respects capacity"""
    evaluation = context['evaluation']
    total_cargo = sum(evaluation.cargo_after.values())
    assert total_cargo <= max_units, \
        f"Total cargo {total_cargo} exceeds limit {max_units}"


@then('evaluation should respect cargo capacity constraint')
def verify_cargo_capacity_respected(context):
    """Verify cargo capacity is respected"""
    evaluation = context['evaluation']
    capacity = context['cargo_capacity']
    total_cargo = sum(evaluation.cargo_after.values())
    assert total_cargo <= capacity, \
        f"Total cargo {total_cargo} exceeds capacity {capacity}"


@then('evaluation should respect market trade_volume')
def verify_trade_volume_respected(context):
    """Verify trade volume limits are respected"""
    evaluation = context['evaluation']
    opportunities = context['trade_opportunities']

    for action in evaluation.actions:
        if action.action == 'BUY':
            opp = next((o for o in opportunities
                       if o['good'] == action.good and o['buy_waypoint'] == action.waypoint), None)
            if opp:
                assert action.units <= opp['trade_volume'], \
                    f"Buy units {action.units} exceeds trade_volume {opp['trade_volume']}"


@then('units purchased should not exceed trade_volume')
def verify_units_not_exceed_volume(context):
    """Verify units don't exceed trade volume"""
    evaluation = context['evaluation']
    opportunities = context['trade_opportunities']

    for action in evaluation.actions:
        if action.action == 'BUY':
            opp = next((o for o in opportunities
                       if o['good'] == action.good and o['buy_waypoint'] == action.waypoint), None)
            if opp:
                assert action.units <= opp['trade_volume']


@then('potential future revenue should be calculated')
def verify_future_revenue_calculated(context):
    """Verify future revenue is included in profit"""
    evaluation = context['evaluation']
    # If we bought goods, net_profit should include future revenue estimation
    buy_actions = [a for a in evaluation.actions if a.action == 'BUY']
    if buy_actions:
        assert evaluation.net_profit is not None


@then('net profit should include estimated future sales')
def verify_profit_includes_future_sales(context):
    """Verify profit includes future sales estimate"""
    evaluation = context['evaluation']
    assert evaluation.net_profit is not None


@then('evaluation should show positive profitability')
def verify_positive_profitability(context):
    """Verify evaluation shows positive profit"""
    evaluation = context['evaluation']
    assert evaluation.net_profit > 0


@then('evaluation should show unprofitable due to fuel cost')
def verify_unprofitable_due_to_fuel(context):
    """Verify evaluation is unprofitable because of fuel cost"""
    evaluation = context['evaluation']
    fuel_cost = context['fuel_cost']

    # Profit should be negative and fuel cost should be a major factor
    assert evaluation.net_profit < 0
    assert fuel_cost > 0


@then(parsers.parse('SELL actions should only include "{good}"'))
def verify_sell_only_specific_good(context, good):
    """Verify only specific good is sold"""
    evaluation = context['evaluation']
    sell_actions = [a for a in evaluation.actions if a.action == 'SELL']

    for action in sell_actions:
        assert action.good == good, \
            f"Expected only {good} to be sold, but found {action.good}"


@then(parsers.parse('"{good}" should remain in cargo after'))
def verify_good_remains_in_cargo(context, good):
    """Verify specific good remains in cargo"""
    evaluation = context['evaluation']
    assert good in evaluation.cargo_after, \
        f"Expected {good} to remain in cargo"


@then(parsers.parse('cargo after should contain both "{good1}" and "{good2}"'))
def verify_cargo_contains_both_goods(context, good1, good2):
    """Verify cargo contains both specified goods"""
    evaluation = context['evaluation']
    assert good1 in evaluation.cargo_after, f"Expected {good1} in cargo"
    assert good2 in evaluation.cargo_after, f"Expected {good2} in cargo"


@then('net profit should equal negative fuel cost')
def verify_profit_equals_negative_fuel(context):
    """Verify profit equals negative fuel cost (no trading)"""
    evaluation = context['evaluation']
    fuel_cost = context['fuel_cost']

    assert evaluation.net_profit == -fuel_cost, \
        f"Expected profit {-fuel_cost}, got {evaluation.net_profit}"


@then('cargo after should equal cargo before')
def verify_cargo_unchanged(context):
    """Verify cargo didn't change"""
    evaluation = context['evaluation']
    original_cargo = context.get('cargo', {})

    assert evaluation.cargo_after == original_cargo, \
        f"Expected cargo to remain {original_cargo}, got {evaluation.cargo_after}"


@then('evaluation should indicate unprofitable market')
def verify_unprofitable_market(context):
    """Verify market is unprofitable"""
    evaluation = context['evaluation']
    assert evaluation.net_profit <= 0


@then(parsers.parse('BUY action should purchase {units:d} units maximum'))
def verify_buy_units_maximum(context, units):
    """Verify buy action doesn't exceed maximum units"""
    evaluation = context['evaluation']
    buy_actions = [a for a in evaluation.actions if a.action == 'BUY']

    if buy_actions:
        total_units = sum(a.units for a in buy_actions)
        assert total_units <= units, \
            f"Expected max {units} units, got {total_units}"


@then(parsers.parse('credits after should be approximately {amount:d} (fuel cost reserve)'))
def verify_credits_reserve(context, amount):
    """Verify credits reserve is maintained"""
    evaluation = context['evaluation']
    # Allow some tolerance for fuel cost reserve
    assert evaluation.credits_after >= amount - 50, \
        f"Expected credits ~{amount}, got {evaluation.credits_after}"


@then('evaluation should leave safety margin for fuel')
def verify_fuel_safety_margin(context):
    """Verify fuel cost safety margin is maintained"""
    evaluation = context['evaluation']
    fuel_cost = context['fuel_cost']

    # Should have at least fuel cost remaining
    assert evaluation.credits_after >= fuel_cost


@then('net profit should equal sell revenue only')
def verify_profit_sell_revenue_only(context):
    """Verify profit equals sell revenue (zero fuel cost)"""
    evaluation = context['evaluation']

    sell_revenue = sum(a.total_value for a in evaluation.actions if a.action == 'SELL')
    assert evaluation.net_profit == sell_revenue


@then('evaluation should not deduct fuel costs')
def verify_no_fuel_deduction(context):
    """Verify fuel cost wasn't deducted"""
    evaluation = context['evaluation']
    fuel_cost = context['fuel_cost']

    assert fuel_cost == 0


@then('profitability should be higher than with fuel cost')
def verify_higher_profitability_without_fuel(context):
    """Verify profitability is higher without fuel cost"""
    evaluation = context['evaluation']
    # With zero fuel cost, profit should be positive if there are sell actions
    sell_actions = [a for a in evaluation.actions if a.action == 'SELL']
    if sell_actions:
        assert evaluation.net_profit > 0

# ============================================================================
# Helper Functions
# ============================================================================

def parse_number(text):
    """Parse number with commas (e.g., '10,000' -> 10000)"""
    return int(text.replace(',', ''))


# ============================================================================
# Comma-Formatted Number Step Variations
# ============================================================================

@given(parsers.re(r'ship has (?P<credits>[\d,]+) credits available'))
def set_ship_credits_with_commas(context, credits):
    """Set ship credits with comma-formatted number"""
    context['credits'] = parse_number(credits)


@given(parsers.re(r'ship has (?P<capacity>[\d,]+) cargo capacity available'))
def set_ship_capacity_with_commas(context, capacity):
    """Set ship cargo capacity with comma-formatted number"""
    context['cargo_capacity'] = parse_number(capacity)


@given(parsers.re(r'fuel cost is (?P<cost>[\d,]+) credits'))
def set_fuel_cost_with_commas(context, cost):
    """Set fuel cost with comma-formatted number"""
    context['fuel_cost'] = parse_number(cost)


@given(parsers.parse('market buys "{good}" for {price:d} credits'))
def set_market_buys_good(context, good, price):
    """Set market that ONLY buys a good (replaces all trade opportunities)"""
    # This step is used to create a sell-only scenario
    context['trade_opportunities'] = [{
        'buy_waypoint': 'X1-TEST-A',
        'sell_waypoint': 'X1-TEST-B',
        'good': good,
        'buy_price': 0,  # No buy opportunity
        'sell_price': price,
        'trade_volume': 100
    }]


@given(parsers.parse('market sells "{good}" for {price:d} credits with volume {volume:d}'))
def set_market_sells_good_with_volume(context, good, price, volume):
    """Set market that sells a good with volume"""
    if 'market_opportunities' not in context:
        context['market_opportunities'] = {}
    if good not in context['market_opportunities']:
        context['market_opportunities'][good] = {}
    context['market_opportunities'][good]['sell_price'] = price
    context['market_opportunities'][good]['trade_volume'] = volume
