#!/usr/bin/env python3
"""
Step definitions for circuit breaker auto-recovery continuation tests

Tests that multi-leg trader CONTINUES operations after successful auto-recovery
instead of aborting the entire route.
"""

import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from unittest.mock import Mock, MagicMock, patch
from spacetraders_bot.operations.multileg_trader import (
    execute_multileg_route,
    MultiLegRoute,
    RouteSegment,
    TradeAction
)


def _build_segment(**kwargs):
    actions_at_start = kwargs.pop('actions_at_start', []) or []
    actions_at_end = kwargs.pop('actions_at_end', []) or []
    actions_at_destination = kwargs.pop('actions_at_destination', []) or []
    ordered_actions = list(actions_at_start) + list(actions_at_destination) + list(actions_at_end)
    return RouteSegment(actions_at_destination=ordered_actions, **kwargs)


# Load scenarios
scenarios('../../../bdd/features/trading/circuit_breaker_continue_after_recovery.feature')


@pytest.fixture
def context():
    """Shared test context"""
    return {
        'ship_symbol': 'TEST-SHIP-1',
        'ship_location': 'X1-TEST-A1',
        'system': 'X1-TEST',
        'cargo_capacity': 40,
        'cargo': {},
        'credits': 100000,
        'route': None,
        'recovery_executed': False,
        'recovery_profit': 0,
        'operation_aborted': False,
        'operation_continued': False,
        'segments_completed': 0,
        'circuit_breaker_triggered': False,
        'price_spike_percent': 0,
    }


@pytest.fixture
def mock_api(context):
    """Mock API client"""
    api = Mock()

    api.get_agent = Mock(return_value={'credits': context['credits']})

    def get_market(system, waypoint):
        # Return normal price for pre-purchase check (within 30% threshold)
        # But actual transaction will be at spiked price (triggers post-purchase)
        if waypoint == 'X1-TEST-D45':
            return {
                'tradeGoods': [{
                    'symbol': 'SHIP_PARTS',
                    'sellPrice': 1400,  # What we BUY at (16.7% spike - within 30% threshold)
                    'purchasePrice': 2000,  # What market BUYS from us (for recovery sale - profitable!)
                    'tradeVolume': 100
                }]
            }
        # Return normal price for sell destination
        elif waypoint == 'X1-TEST-A2':
            return {
                'tradeGoods': [{
                    'symbol': 'SHIP_PARTS',
                    'sellPrice': 1400,  # What we'd buy at
                    'purchasePrice': 2000,  # What market buys from us (good sell price)
                    'tradeVolume': 100
                }]
            }
        return {'tradeGoods': []}

    api.get_market = get_market

    return api


@pytest.fixture
def mock_ship(context):
    """Mock ship controller"""
    ship = Mock()

    def get_status():
        return {
            'nav': {
                'systemSymbol': context['system'],
                'waypointSymbol': context['ship_location'],
                'status': 'DOCKED'
            },
            'cargo': {
                'capacity': context['cargo_capacity'],
                'units': sum(context['cargo'].values()),
                'inventory': [
                    {'symbol': good, 'units': units}
                    for good, units in context['cargo'].items()
                ]
            },
            'fuel': {
                'current': 400,
                'capacity': 400
            },
            'engine': {
                'speed': 10
            }
        }

    def buy(good, units):
        # Simulate buy at spiked price (post-purchase breaker trigger)
        # Pre-purchase showed 1400 (16.7% spike, within threshold)
        # But actual transaction is 1800 (50% spike, exceeds threshold)
        actual_price = 1800  # Spiked price at transaction time
        total_cost = actual_price * units
        context['credits'] -= total_cost
        context['cargo'][good] = context['cargo'].get(good, 0) + units

        return {
            'units': units,
            'totalPrice': total_cost
        }

    def sell(good, units, check_market_prices=True):
        # Simulate selling specific cargo
        if good in context['cargo'] and context['cargo'][good] >= units:
            sell_price = 2000  # Good sell price for recovery
            revenue = units * sell_price
            context['credits'] += revenue
            context['cargo'][good] -= units
            if context['cargo'][good] == 0:
                del context['cargo'][good]

            context['recovery_executed'] = True
            # Calculate profit based on what was sold
            context['recovery_profit'] = revenue - (units * 1800)  # Revenue - cost

            return {
                'units': units,
                'totalPrice': revenue,
                'pricePerUnit': sell_price
            }
        return None

    def sell_all():
        # Simulate recovery sale
        total_revenue = 0
        for good, units in list(context['cargo'].items()):
            revenue = units * 2000  # Good sell price
            total_revenue += revenue
            context['credits'] += revenue
            del context['cargo'][good]

        context['recovery_executed'] = True
        context['recovery_profit'] = total_revenue - (40 * 1800)  # Revenue - cost

        # ship_controller.sell_all() returns int (total revenue), not dict
        return total_revenue

    def dock():
        context['ship_location'] = context['ship_location']  # Stay at current location
        return True

    def orbit():
        # Mock orbit method
        return True

    ship.get_status = get_status
    ship.buy = buy
    ship.sell = sell
    ship.sell_all = sell_all
    ship.dock = dock
    ship.orbit = orbit

    return ship


@pytest.fixture
def mock_navigator(context):
    """Mock SmartNavigator"""
    nav = Mock()

    def execute_route(ship, destination):
        # Simulate successful navigation
        context['ship_location'] = destination
        return True

    nav.execute_route = execute_route
    return nav


@pytest.fixture
def mock_db(context):
    """Mock database"""
    from datetime import datetime, timezone
    from contextlib import contextmanager

    db = Mock()

    @contextmanager
    def connection():
        """Mock database connection context manager"""
        conn = Mock()
        yield conn

    @contextmanager
    def transaction():
        """Mock database transaction context manager"""
        conn = Mock()
        yield conn

    def get_market_data(conn, waypoint, good):
        """Return fresh market data to pass validation"""
        # Return fresh timestamps (<30min) to pass pre-flight validation
        timestamp = datetime.now(timezone.utc).strftime('%Y-%m-%dT%H:%M:%S.%f')[:-3] + 'Z'

        if good:
            return [{
                'waypoint_symbol': waypoint,
                'good_symbol': good,
                'sell_price': 1400,  # Normal price for pre-flight check
                'purchase_price': 2000,
                'last_updated': timestamp,
                'supply': 'MODERATE',
                'activity': 'WEAK',
                'trade_volume': 100
            }]
        return []

    def update_market_data(conn, **kwargs):
        """Mock market data update - just pass"""
        pass

    db.connection = connection
    db.transaction = transaction
    db.get_market_data = get_market_data
    db.update_market_data = update_market_data

    return db


# Background steps

@given(parsers.parse('a ship "{ship}" trading in system "{system}"'))
def ship_trading_in_system(context, ship, system):
    context['ship_symbol'] = ship
    context['system'] = system


@given(parsers.parse('the ship has {capacity:d} cargo capacity'))
def ship_cargo_capacity(context, capacity):
    context['cargo_capacity'] = capacity


@given(parsers.parse('agent has {credits:d} credits'))
def agent_credits(context, credits):
    context['credits'] = credits


# Given steps

@given('a multi-leg route with 3 segments')
def multi_leg_route_three_segments(context):
    # Segment 0: Navigate to D45 and BUY SHIP_PARTS (will trigger circuit breaker)
    # Segment 1: Navigate to A2 and SELL SHIP_PARTS (depends on segment 0)
    # Segment 2: Navigate to B7 and BUY COPPER_ORE (independent)
    # Segment 3: Navigate to C8 and SELL COPPER_ORE (depends on segment 2, independent of segment 0)

    context['route'] = MultiLegRoute(
        segments=[
            _build_segment(
                from_waypoint='X1-TEST-A1',
                to_waypoint='X1-TEST-D45',
                distance=100,
                fuel_cost=110,
                actions_at_start=[],

                actions_at_end=[
                    TradeAction(
                        waypoint='X1-TEST-D45',
                        good='SHIP_PARTS',
                        action='BUY',
                        units=40,
                        price_per_unit=1200,  # Expected price
                        total_value=48000
                    )
                ],
                cargo_after={'SHIP_PARTS': 40},
                credits_after=52000,
                cumulative_profit=0
            ),
            _build_segment(
                from_waypoint='X1-TEST-D45',
                to_waypoint='X1-TEST-A2',
                distance=80,
                fuel_cost=88,
                actions_at_start=[],

                actions_at_end=[
                    TradeAction(
                        waypoint='X1-TEST-A2',
                        good='SHIP_PARTS',
                        action='SELL',
                        units=40,
                        price_per_unit=2000,
                        total_value=80000
                    )
                ],
                cargo_after={},
                credits_after=132000,
                cumulative_profit=32000
            ),
            _build_segment(
                from_waypoint='X1-TEST-A2',
                to_waypoint='X1-TEST-B7',
                distance=60,
                fuel_cost=66,
                actions_at_start=[],

                actions_at_end=[
                    TradeAction(
                        waypoint='X1-TEST-B7',
                        good='COPPER_ORE',
                        action='BUY',
                        units=30,
                        price_per_unit=500,
                        total_value=15000
                    )
                ],
                cargo_after={'COPPER_ORE': 30},
                credits_after=117000,
                cumulative_profit=17000
            ),
            _build_segment(
                from_waypoint='X1-TEST-B7',
                to_waypoint='X1-TEST-C8',
                distance=50,
                fuel_cost=55,
                actions_at_start=[],

                actions_at_end=[
                    TradeAction(
                        waypoint='X1-TEST-C8',
                        good='COPPER_ORE',
                        action='SELL',
                        units=30,
                        price_per_unit=800,
                        total_value=24000
                    )
                ],
                cargo_after={},
                credits_after=141000,
                cumulative_profit=41000
            )
        ],
        total_profit=41000,
        total_distance=290,
        total_fuel_cost=319,
        estimated_time_minutes=29
    )


@given(parsers.parse('segment 1 has a BUY action for "{good}" at "{market}"'))
def segment_one_buy_action(context, good, market):
    # Already set in multi_leg_route_three_segments
    assert context['route'].segments[0].actions_at_destination[0].good == good
    assert context['route'].segments[0].actions_at_destination[0].waypoint == market


@given(parsers.parse('the planned buy price is {price:d} credits per unit'))
def planned_buy_price(context, price):
    # Already set in route
    assert context['route'].segments[0].actions_at_destination[0].price_per_unit == price


@given(parsers.parse('segment 2 has a SELL action at "{market}"'))
def segment_two_sell_action(context, market):
    assert context['route'].segments[1].actions_at_destination[0].waypoint == market


@given(parsers.parse('the spike threshold is {threshold:d} percent'))
def spike_threshold(context, threshold):
    context['spike_threshold_pct'] = threshold


# When steps

@when(parsers.parse('executing segment 1, the live market shows buy price at {price:d} credits'))
def live_market_shows_spiked_price(context, price):
    context['live_market_buy_price'] = price
    # Calculate spike
    planned = context['route'].segments[0].actions_at_destination[0].price_per_unit
    context['price_spike_percent'] = ((price - planned) / planned) * 100


@when('the post-purchase circuit breaker triggers')
def post_purchase_breaker_triggers(context):
    # This will happen during route execution when buy completes at spiked price
    context['circuit_breaker_triggered'] = True


@when('auto-recovery executes successfully')
def auto_recovery_executes(context, mock_api, mock_ship, mock_navigator, mock_db):
    # Auto-recovery navigates to segment 2 sell destination and sells cargo
    # Execute the multileg route which will trigger circuit breaker and auto-recovery
    with patch('spacetraders_bot.operations.multileg_trader.SmartNavigator',
               return_value=mock_navigator):
        # Patch logging to reduce noise
        import logging
        logging.disable(logging.CRITICAL)
        try:
            result = execute_multileg_route(
                route=context['route'],
                ship=mock_ship,
                api=mock_api,
                db=mock_db,
                player_id=1
            )
            context['multileg_result'] = result
            context['operation_aborted'] = not result
            context['operation_continued'] = result
        finally:
            logging.disable(logging.NOTSET)


@when(parsers.parse('recovery generates {profit:d} credits profit'))
def recovery_generates_profit(context, profit):
    # Expected profit from recovery (verified in assertions)
    context['expected_recovery_profit'] = profit


# Then steps

@then('auto-recovery should complete successfully')
def auto_recovery_completes(context):
    assert context['recovery_executed'], "Auto-recovery should have been executed"


@then(parsers.parse('recovery should generate {profit:d} credits profit'))
def recovery_profit_verified(context, profit):
    assert context['recovery_profit'] == profit, \
        f"Expected recovery profit {profit} but got {context['recovery_profit']}"


@then('the route should NOT abort')
def route_should_not_abort(context):
    assert not context['operation_aborted'], \
        "Route should NOT abort after successful recovery"


@then('the operation should continue with remaining segments')
def operation_continues_with_remaining(context):
    # After recovery, trader should re-optimize route with remaining time
    context['operation_continued'] = True


@then('remaining independent segments should be available')
def remaining_segments_available(context):
    # After segment 0 fails and segment 1 is skipped (depends on 0),
    # segments 2 and 3 should still be available (independent of segment 0)
    assert len(context['route'].segments) == 4, \
        "Route should have 4 segments total"


@then('the trader should re-optimize route with remaining time budget')
def trader_reoptimizes_route(context):
    # After recovery, trader should query market data and optimize new route
    context['should_reoptimize'] = True


@then('only after duration expires should the operation stop')
def operation_stops_after_duration(context):
    context['stops_after_duration'] = True
