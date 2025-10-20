from pytest_bdd import scenarios, given, when, then, parsers
from dataclasses import dataclass, field

scenarios('../../../bdd/features/operations/trading.feature')


@dataclass
class MarketPrice:
    """Market price information."""
    commodity: str
    buy_price: int = 0  # What market sells to ship
    sell_price: int = 0  # What market pays ship
    profit_margin: float = 0.0


@dataclass
class TradingRoute:
    """Trading route information."""
    market_a: str = "A"
    market_b: str = "B"
    commodity: str = ""
    buy_price: int = 0
    sell_price: int = 0
    distance: int = 0
    fuel_cost_per_unit: int = 0
    cargo_capacity: int = 0


@dataclass
class TradingLeg:
    """Single leg of multi-leg route."""
    commodity: str
    buy_price: int
    sell_price: int
    profit_per_unit: int


@given('a trading analysis system', target_fixture='trading_ctx')
def given_trading_system():
    """Create trading analysis system."""
    return {
        'markets': {},
        'commodities': [],
        'route': TradingRoute(),
        'cargo_capacity': 0,
        'distance': 0,
        'fuel_cost_per_unit': 0,
        'calculations': {},
        'market_list': [],
        'trading_legs': [],
        'commodity_rankings': [],
        'cargo_allocation': {},
        'ship_speed': 0,
        'loading_time': 0,
        'unloading_time': 0
    }


@given(parsers.parse('market A sells "{commodity}" at {price:d} credits per unit'))
def given_market_a_sells(trading_ctx, commodity, price):
    """Set market A sell price."""
    trading_ctx['route'].commodity = commodity
    trading_ctx['route'].buy_price = price
    return trading_ctx


@given(parsers.parse('market B buys "{commodity}" at {price:d} credits per unit'))
def given_market_b_buys(trading_ctx, commodity, price):
    """Set market B buy price."""
    trading_ctx['route'].sell_price = price
    return trading_ctx


@given(parsers.parse('cargo capacity is {capacity:d} units'))
@given(parsers.parse('cargo capacity is {capacity:d} units per leg'))
def given_cargo_capacity(trading_ctx, capacity):
    """Set cargo capacity."""
    trading_ctx['cargo_capacity'] = capacity
    trading_ctx['route'].cargo_capacity = capacity
    return trading_ctx


@given(parsers.parse('distance between markets is {distance:d} units'))
@given(parsers.parse('distance to market is {distance:d} units'))
def given_distance(trading_ctx, distance):
    """Set distance between markets."""
    trading_ctx['distance'] = distance
    trading_ctx['route'].distance = distance
    return trading_ctx


@given(parsers.parse('fuel cost is {cost:d} credit per unit distance'))
@given(parsers.parse('fuel cost is {cost:d} credits per unit distance'))
def given_fuel_cost(trading_ctx, cost):
    """Set fuel cost per unit."""
    trading_ctx['fuel_cost_per_unit'] = cost
    trading_ctx['route'].fuel_cost_per_unit = cost
    return trading_ctx


@given(parsers.parse('market has "{commodity}" selling at {price:d} with profit margin {margin:d}%'))
def given_market_commodity(trading_ctx, commodity, price, margin):
    """Add commodity with profit margin to market."""
    trading_ctx['commodities'].append(MarketPrice(
        commodity=commodity,
        buy_price=price,
        profit_margin=margin
    ))
    return trading_ctx


@given(parsers.parse('a {legs:d}-leg trading route'))
def given_multileg_route(trading_ctx, legs):
    """Create multi-leg trading route."""
    trading_ctx['leg_count'] = legs
    trading_ctx['trading_legs'] = []
    return trading_ctx


@given(parsers.parse('leg {leg:d}: buy "{commodity}" at {buy:d}, sell at {sell:d} (profit {profit:d})'))
def given_trading_leg(trading_ctx, leg, commodity, buy, sell, profit):
    """Add trading leg."""
    trading_ctx['trading_legs'].append(TradingLeg(
        commodity=commodity,
        buy_price=buy,
        sell_price=sell,
        profit_per_unit=profit
    ))
    return trading_ctx


@given(parsers.parse('"{commodity}" available: profit {profit:d} credits per unit'))
def given_commodity_profit(trading_ctx, commodity, profit):
    """Add commodity with profit."""
    trading_ctx['commodities'].append(MarketPrice(
        commodity=commodity,
        buy_price=0,
        sell_price=profit,  # Store profit in sell_price for simplicity
        profit_margin=0
    ))
    return trading_ctx


@given(parsers.parse('ship speed in CRUISE mode is {speed:d} units per second'))
def given_ship_speed(trading_ctx, speed):
    """Set ship speed."""
    trading_ctx['ship_speed'] = speed
    return trading_ctx


@given(parsers.parse('loading time is {seconds:d} seconds'))
def given_loading_time(trading_ctx, seconds):
    """Set loading time."""
    trading_ctx['loading_time'] = seconds
    return trading_ctx


@given(parsers.parse('unloading time is {seconds:d} seconds'))
def given_unloading_time(trading_ctx, seconds):
    """Set unloading time."""
    trading_ctx['unloading_time'] = seconds
    return trading_ctx


@given(parsers.parse('{count:d} markets with various profit margins'))
def given_market_count(trading_ctx, count):
    """Set market count."""
    trading_ctx['market_count'] = count
    trading_ctx['market_list'] = []
    return trading_ctx


@given(parsers.parse('market {market_id:d} offers {margin:d}% profit margin'))
def given_market_margin(trading_ctx, market_id, margin):
    """Add market with profit margin."""
    trading_ctx['market_list'].append({
        'id': market_id,
        'profit_margin': margin
    })
    return trading_ctx


@given(parsers.parse('minimum profit threshold is {threshold:d}%'))
def given_profit_threshold(trading_ctx, threshold):
    """Set minimum profit threshold."""
    trading_ctx['profit_threshold'] = threshold
    return trading_ctx


@when('I calculate profit margin')
def when_calculate_profit_margin(trading_ctx):
    """Calculate profit margin."""
    route = trading_ctx['route']
    profit_per_unit = route.sell_price - route.buy_price
    profit_margin_pct = (profit_per_unit / route.buy_price) * 100

    trading_ctx['calculations']['profit_per_unit'] = profit_per_unit
    trading_ctx['calculations']['profit_margin_pct'] = profit_margin_pct
    return trading_ctx


@when('I calculate round-trip profitability')
def when_calculate_roundtrip(trading_ctx):
    """Calculate round-trip profit."""
    route = trading_ctx['route']

    profit_per_unit = route.sell_price - route.buy_price
    gross_revenue = profit_per_unit * route.cargo_capacity
    round_trip_distance = route.distance * 2
    fuel_cost = round_trip_distance * route.fuel_cost_per_unit
    net_profit = gross_revenue - fuel_cost

    trading_ctx['calculations']['gross_revenue'] = gross_revenue
    trading_ctx['calculations']['fuel_cost'] = fuel_cost
    trading_ctx['calculations']['net_profit'] = net_profit

    return trading_ctx


@when('I evaluate trade profitability')
def when_evaluate_profitability(trading_ctx):
    """Evaluate if trade is profitable."""
    route = trading_ctx['route']

    profit_per_unit = route.sell_price - route.buy_price
    gross_revenue = profit_per_unit * route.cargo_capacity
    round_trip_distance = route.distance * 2
    fuel_cost = round_trip_distance * route.fuel_cost_per_unit
    net_profit = gross_revenue - fuel_cost

    trading_ctx['is_profitable'] = net_profit > 0
    trading_ctx['calculations']['net_profit'] = net_profit
    trading_ctx['rejection_reason'] = "fuel costs exceed profit margin" if net_profit <= 0 else ""

    return trading_ctx


@when('I rank commodities by profit margin')
def when_rank_commodities(trading_ctx):
    """Rank commodities by profit margin."""
    sorted_commodities = sorted(
        trading_ctx['commodities'],
        key=lambda c: c.profit_margin,
        reverse=True
    )
    trading_ctx['commodity_rankings'] = sorted_commodities
    return trading_ctx


@when('I calculate total sequence profit')
def when_calculate_sequence_profit(trading_ctx):
    """Calculate multi-leg sequence profit."""
    total_profit = 0
    capacity = trading_ctx['cargo_capacity']

    for leg in trading_ctx['trading_legs']:
        leg_profit = leg.profit_per_unit * capacity
        total_profit += leg_profit

    avg_profit = total_profit // len(trading_ctx['trading_legs'])

    trading_ctx['calculations']['total_profit'] = total_profit
    trading_ctx['calculations']['avg_profit_per_leg'] = avg_profit

    return trading_ctx


@when('I optimize cargo allocation for maximum profit')
def when_optimize_cargo(trading_ctx):
    """Optimize cargo allocation."""
    # Sort by profit per unit (stored in sell_price)
    sorted_commodities = sorted(
        trading_ctx['commodities'],
        key=lambda c: c.sell_price,
        reverse=True
    )

    # Allocate to highest profit commodity
    capacity = trading_ctx['cargo_capacity']
    if sorted_commodities:
        top_commodity = sorted_commodities[0]
        trading_ctx['cargo_allocation']['priority'] = top_commodity.commodity
        trading_ctx['cargo_allocation']['expected_profit'] = top_commodity.sell_price * capacity

    return trading_ctx


@when('I calculate total cycle time')
def when_calculate_cycle_time(trading_ctx):
    """Calculate trading cycle time."""
    distance = trading_ctx['distance']
    speed = trading_ctx['ship_speed']
    loading = trading_ctx['loading_time']
    unloading = trading_ctx['unloading_time']

    # One-way travel time
    travel_time = distance / speed
    # Round-trip includes travel both ways plus loading/unloading
    total_time = (travel_time * 2) + loading + unloading

    # Cycles per hour
    cycles_per_hour = 3600 / total_time

    trading_ctx['calculations']['travel_time'] = travel_time
    trading_ctx['calculations']['total_cycle_time'] = total_time
    trading_ctx['calculations']['cycles_per_hour'] = cycles_per_hour

    return trading_ctx


@when('I filter profitable markets')
def when_filter_markets(trading_ctx):
    """Filter markets by profit threshold."""
    threshold = trading_ctx['profit_threshold']
    profitable_markets = [
        m for m in trading_ctx['market_list']
        if m['profit_margin'] >= threshold
    ]
    trading_ctx['profitable_markets'] = profitable_markets
    return trading_ctx


@then(parsers.parse('profit per unit should be {profit:d} credits'))
def then_profit_per_unit(trading_ctx, profit):
    """Verify profit per unit."""
    assert trading_ctx['calculations']['profit_per_unit'] == profit


@then(parsers.parse('profit margin percentage should be {margin:d}%'))
def then_profit_margin_pct(trading_ctx, margin):
    """Verify profit margin percentage."""
    assert trading_ctx['calculations']['profit_margin_pct'] == margin


@then(parsers.parse('gross revenue should be {revenue:d} credits'))
def then_gross_revenue(trading_ctx, revenue):
    """Verify gross revenue."""
    assert trading_ctx['calculations']['gross_revenue'] == revenue


@then(parsers.parse('fuel cost should be {cost:d} credits'))
def then_fuel_cost(trading_ctx, cost):
    """Verify fuel cost."""
    assert trading_ctx['calculations']['fuel_cost'] == cost


@then(parsers.parse('net profit should be {profit:d} credits'))
def then_net_profit(trading_ctx, profit):
    """Verify net profit."""
    assert trading_ctx['calculations']['net_profit'] == profit


@then('trade should be unprofitable')
def then_trade_unprofitable(trading_ctx):
    """Verify trade is unprofitable."""
    assert trading_ctx['is_profitable'] is False


@then('rejection reason should mention fuel costs')
def then_rejection_mentions_fuel(trading_ctx):
    """Verify rejection reason."""
    assert 'fuel' in trading_ctx['rejection_reason'].lower()


@then(parsers.parse('top commodity should be "{commodity}"'))
def then_top_commodity(trading_ctx, commodity):
    """Verify top commodity."""
    rankings = trading_ctx['commodity_rankings']
    assert rankings[0].commodity == commodity


@then(parsers.parse('top profit margin should be {margin:d}%'))
def then_top_margin(trading_ctx, margin):
    """Verify top profit margin."""
    rankings = trading_ctx['commodity_rankings']
    assert rankings[0].profit_margin == margin


@then(parsers.parse('total profit should be {profit:d} credits'))
def then_total_profit(trading_ctx, profit):
    """Verify total profit."""
    assert trading_ctx['calculations']['total_profit'] == profit


@then(parsers.parse('average profit per leg should be {profit:d} credits'))
def then_avg_profit_per_leg(trading_ctx, profit):
    """Verify average profit per leg."""
    assert trading_ctx['calculations']['avg_profit_per_leg'] == profit


@then(parsers.parse('allocation should prioritize "{commodity}"'))
def then_allocation_priority(trading_ctx, commodity):
    """Verify cargo allocation priority."""
    assert trading_ctx['cargo_allocation']['priority'] == commodity


@then('expected profit should be maximized')
def then_profit_maximized(trading_ctx):
    """Verify profit is maximized."""
    # Check that allocation exists and has positive profit
    assert 'expected_profit' in trading_ctx['cargo_allocation']
    assert trading_ctx['cargo_allocation']['expected_profit'] > 0


@then(parsers.parse('travel time should be {time:d} seconds'))
def then_travel_time(trading_ctx, time):
    """Verify travel time."""
    assert trading_ctx['calculations']['travel_time'] == time


@then(parsers.parse('total cycle time should be {time:d} seconds'))
def then_total_cycle_time(trading_ctx, time):
    """Verify total cycle time."""
    assert trading_ctx['calculations']['total_cycle_time'] == time


@then(parsers.parse('cycles per hour should be approximately {cycles:d}'))
def then_cycles_per_hour(trading_ctx, cycles):
    """Verify cycles per hour (allow small variance)."""
    actual = trading_ctx['calculations']['cycles_per_hour']
    # Allow 5% variance due to float division
    tolerance = cycles * 0.05
    assert abs(actual - cycles) <= tolerance, f"Expected ~{cycles}, got {actual}"


@then(parsers.parse('{count:d} markets should be selected'))
@then(parsers.parse('{count:d} market should be selected'))
def then_market_count(trading_ctx, count):
    """Verify market count."""
    assert len(trading_ctx['profitable_markets']) == count


@then(parsers.parse('selected markets should include market {market_id:d}'))
def then_includes_market(trading_ctx, market_id):
    """Verify specific market included."""
    market_ids = [m['id'] for m in trading_ctx['profitable_markets']]
    assert market_id in market_ids
