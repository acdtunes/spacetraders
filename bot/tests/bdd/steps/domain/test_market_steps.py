"""Step definitions for market domain value objects"""
import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from dataclasses import FrozenInstanceError

from domain.shared.market import TradeGood, Market, TourResult, PollResult

scenarios('../../features/domain/market.feature')


@pytest.fixture
def context():
    """Shared context for scenario steps"""
    return {}


# TradeGood steps

@given('a trade good with symbol "IRON_ORE"')
def trade_good_symbol_iron(context):
    context['symbol'] = 'IRON_ORE'


@given('a trade good with symbol "FUEL"')
def trade_good_symbol_fuel(context):
    context['symbol'] = 'FUEL'


@given('supply level "MODERATE"')
def supply_moderate(context):
    context['supply'] = 'MODERATE'


@given('supply level "HIGH"')
def supply_high(context):
    context['supply'] = 'HIGH'


@given('no supply level')
def no_supply(context):
    context['supply'] = None


@given('activity level "STRONG"')
def activity_strong(context):
    context['activity'] = 'STRONG'


@given('activity level "WEAK"')
def activity_weak(context):
    context['activity'] = 'WEAK'


@given('no activity level')
def no_activity(context):
    context['activity'] = None


@given(parsers.parse('purchase price {price:d}'))
def purchase_price(context, price):
    context['purchase_price'] = price


@given(parsers.parse('sell price {price:d}'))
def sell_price(context, price):
    context['sell_price'] = price


@given(parsers.parse('trade volume {volume:d}'))
def trade_volume(context, volume):
    context['trade_volume'] = volume


@when('I create a TradeGood')
def create_trade_good(context):
    context['trade_good'] = TradeGood(
        symbol=context['symbol'],
        supply=context.get('supply'),
        activity=context.get('activity'),
        purchase_price=context['purchase_price'],
        sell_price=context['sell_price'],
        trade_volume=context['trade_volume']
    )


@then('the trade good should be valid')
def trade_good_valid(context):
    good = context['trade_good']
    assert good.symbol == context['symbol']
    assert good.purchase_price == context['purchase_price']
    assert good.sell_price == context['sell_price']
    assert good.trade_volume == context['trade_volume']


@then('the trade good should be immutable')
def trade_good_immutable(context):
    good = context['trade_good']
    with pytest.raises(FrozenInstanceError):
        good.symbol = 'CHANGED'


# Market steps

@given(parsers.parse('a waypoint "{waypoint}"'))
def waypoint(context, waypoint):
    context['waypoint'] = waypoint


@given('trade goods:')
def trade_goods_table(context, datatable):
    """Parse trade goods from table"""
    goods = []
    # datatable is list of lists, first row is headers
    headers = datatable[0]
    for row in datatable[1:]:
        row_dict = dict(zip(headers, row))
        goods.append(TradeGood(
            symbol=row_dict['symbol'],
            supply=row_dict['supply'] if row_dict['supply'] else None,
            activity=row_dict['activity'] if row_dict['activity'] else None,
            purchase_price=int(row_dict['purchase_price']),
            sell_price=int(row_dict['sell_price']),
            trade_volume=int(row_dict['trade_volume'])
        ))
    context['trade_goods'] = tuple(goods)


@given(parsers.parse('last updated timestamp "{timestamp}"'))
def last_updated(context, timestamp):
    context['last_updated'] = timestamp


@when('I create a Market')
def create_market(context):
    context['market'] = Market(
        waypoint_symbol=context['waypoint'],
        trade_goods=context['trade_goods'],
        last_updated=context['last_updated']
    )


@then('the market should be valid')
def market_valid(context):
    market = context['market']
    assert market.waypoint_symbol == context['waypoint']
    assert market.last_updated == context['last_updated']


@then('the market should be immutable')
def market_immutable(context):
    market = context['market']
    with pytest.raises((FrozenInstanceError, AttributeError)):
        market.waypoint_symbol = 'CHANGED'


@then(parsers.parse('the market should have {count:d} trade goods'))
def market_trade_goods_count(context, count):
    market = context['market']
    assert len(market.trade_goods) == count


# TourResult steps

@given(parsers.parse('markets visited count {count:d}'))
def markets_visited(context, count):
    context['markets_visited'] = count


@given(parsers.parse('goods updated count {count:d}'))
def goods_updated_count(context, count):
    context['goods_updated'] = count


@given(parsers.parse('duration {seconds:f} seconds'))
def duration_seconds(context, seconds):
    context['duration_seconds'] = seconds


@when('I create a TourResult')
def create_tour_result(context):
    context['tour_result'] = TourResult(
        markets_visited=context['markets_visited'],
        goods_updated=context['goods_updated'],
        duration_seconds=context['duration_seconds']
    )


@then('the tour result should be valid')
def tour_result_valid(context):
    result = context['tour_result']
    assert result.markets_visited == context['markets_visited']
    assert result.goods_updated == context['goods_updated']
    assert result.duration_seconds == context['duration_seconds']


@then('the tour result should be immutable')
def tour_result_immutable(context):
    result = context['tour_result']
    with pytest.raises(FrozenInstanceError):
        result.markets_visited = 999


# PollResult steps

@when('I create a PollResult')
def create_poll_result(context):
    context['poll_result'] = PollResult(
        goods_updated=context['goods_updated'],
        waypoint=context['waypoint']
    )


@then('the poll result should be valid')
def poll_result_valid(context):
    result = context['poll_result']
    assert result.goods_updated == context['goods_updated']
    assert result.waypoint == context['waypoint']


@then('the poll result should be immutable')
def poll_result_immutable(context):
    result = context['poll_result']
    with pytest.raises(FrozenInstanceError):
        result.goods_updated = 999
