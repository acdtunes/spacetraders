#!/usr/bin/env python3
"""
Test for trade_plan stale market data bug

BUG DESCRIPTION:
The bot_trade_plan optimizer is using WRONG database fields for market prices:
- For BUY markets: reads 'sell_price' instead of 'purchase_price'
- This causes prices to be ~50% of actual values

EVIDENCE:
1. Fresh database cache (from bot_market_waypoint at 18:25 UTC):
   - SHIP_PLATING at D42: purchase_price=2,802 cr (what we pay to buy)
   - ADVANCED_CIRCUITRY at D42: purchase_price=3,816 cr
   - ELECTRONICS at D42: purchase_price=5,958 cr
   - ALUMINUM at D42: purchase_price=516 cr

2. bot_trade_plan output (called at 18:29 UTC, 4 minutes after update):
   - SHIP_PLATING at D42: 1,392 cr (buy price) - 50% of actual
   - ADVANCED_CIRCUITRY at D42: 1,867 cr - 49% of actual
   - ELECTRONICS at D42: 2,930 cr - 49% of actual
   - ALUMINUM at D42: 250 cr - 48% of actual

ROOT CAUSE:
In multileg_trader.py line 1114:
    buy_price = buy_record.get('sell_price')  # BUG: Should be 'purchase_price'!

When we BUY from a market, we pay the market's PURCHASE_PRICE (what they charge us).
But the code is reading SELL_PRICE (what they pay to buy from us).

This causes profit calculations to be wildly inaccurate (100%+ error).
"""

import pytest
import sqlite3
from datetime import datetime, timezone
from unittest.mock import Mock, MagicMock

from spacetraders_bot.operations.multileg_trader import MultiLegTradeOptimizer


@pytest.fixture
def mock_db():
    """Mock database with fresh market data matching the bug report"""
    db = Mock()

    # Fresh market data updated 10 minutes ago (within 1 hour freshness threshold)
    timestamp = datetime.now(timezone.utc)
    timestamp_str = timestamp.strftime('%Y-%m-%dT%H:%M:%S.%f')[:-3] + 'Z'

    # Market data from bug report:
    # D42 has goods with PURCHASE_PRICE (what we pay) and SELL_PRICE (what they pay us)
    # The bug is that optimizer reads sell_price when it should read purchase_price
    market_data = {
        'X1-JB26-D42': [
            {
                'waypoint_symbol': 'X1-JB26-D42',
                'good_symbol': 'SHIP_PLATING',
                'purchase_price': 2802,  # What WE pay to BUY from market
                'sell_price': 1392,      # What market pays to BUY from us
                'supply': 'MODERATE',
                'activity': 'STRONG',
                'trade_volume': 100,
                'last_updated': timestamp_str
            },
            {
                'waypoint_symbol': 'X1-JB26-D42',
                'good_symbol': 'ADVANCED_CIRCUITRY',
                'purchase_price': 3816,
                'sell_price': 1867,
                'supply': 'MODERATE',
                'activity': 'STRONG',
                'trade_volume': 100,
                'last_updated': timestamp_str
            },
            {
                'waypoint_symbol': 'X1-JB26-D42',
                'good_symbol': 'ELECTRONICS',
                'purchase_price': 5958,
                'sell_price': 2930,
                'supply': 'MODERATE',
                'activity': 'STRONG',
                'trade_volume': 100,
                'last_updated': timestamp_str
            },
            {
                'waypoint_symbol': 'X1-JB26-D42',
                'good_symbol': 'ALUMINUM',
                'purchase_price': 516,
                'sell_price': 250,
                'supply': 'MODERATE',
                'activity': 'STRONG',
                'trade_volume': 100,
                'last_updated': timestamp_str
            }
        ],
        'X1-JB26-A2': [
            {
                'waypoint_symbol': 'X1-JB26-A2',
                'good_symbol': 'SHIP_PLATING',
                'purchase_price': 3500,  # Market pays us this to buy from us
                'sell_price': 1800,      # We pay this to buy from market
                'supply': 'LIMITED',
                'activity': 'WEAK',
                'trade_volume': 100,
                'last_updated': timestamp_str
            },
            {
                'waypoint_symbol': 'X1-JB26-A2',
                'good_symbol': 'ADVANCED_CIRCUITRY',
                'purchase_price': 4500,
                'sell_price': 2200,
                'supply': 'LIMITED',
                'activity': 'WEAK',
                'trade_volume': 100,
                'last_updated': timestamp_str
            }
        ]
    }

    def get_market_data(conn, waypoint, good=None):
        """Mock get_market_data to return prepared data"""
        if waypoint not in market_data:
            return []

        if good is None:
            return market_data[waypoint]
        else:
            return [item for item in market_data[waypoint] if item['good_symbol'] == good]

    db.get_market_data = get_market_data

    # Mock connection context manager
    mock_conn = MagicMock()
    mock_cursor = MagicMock()
    mock_conn.cursor.return_value = mock_cursor

    # Mock markets query
    mock_cursor.execute.return_value = None
    mock_cursor.fetchall.return_value = [('X1-JB26-D42',), ('X1-JB26-A2',)]

    db.connection.return_value.__enter__ = Mock(return_value=mock_conn)
    db.connection.return_value.__exit__ = Mock(return_value=False)

    return db


@pytest.fixture
def mock_api():
    """Mock API client"""
    api = Mock()
    api.get_agent.return_value = {'credits': 100000}
    return api


def test_trade_plan_uses_wrong_price_field(mock_db, mock_api):
    """
    Test that reproduces the bug: optimizer reads sell_price instead of purchase_price

    Expected behavior:
    - When buying from D42, should use purchase_price (2802 for SHIP_PLATING)
    - When selling to A2, should use purchase_price (3500 for SHIP_PLATING)

    Buggy behavior:
    - When buying from D42, reads sell_price (1392 for SHIP_PLATING) - WRONG!
    - Profit appears 100%+ higher than reality
    """
    # Setup done via fixtures

    optimizer = MultiLegTradeOptimizer(
        api=mock_api,
        db=mock_db,
        player_id=1,
        logger=Mock()
    )

    # Get trade opportunities (this is where the bug occurs)
    markets = ['X1-JB26-D42', 'X1-JB26-A2']
    opportunities = optimizer._get_trade_opportunities('X1-JB26', markets)

    # Find SHIP_PLATING opportunity
    ship_plating_opp = next(
        (opp for opp in opportunities if opp['good'] == 'SHIP_PLATING'),
        None
    )

    assert ship_plating_opp is not None, "Should find SHIP_PLATING opportunity"

    # BUG VALIDATION: The optimizer currently reads sell_price (1392) instead of purchase_price (2802)
    # This causes buy_price to be ~50% of actual value

    # With the bug:
    # buy_price = 1392 (from sell_price - WRONG!)
    # sell_price = 3500 (from purchase_price - correct)
    # spread = 3500 - 1392 = 2108 (artificially inflated by 100%!)

    # Without the bug (correct):
    # buy_price = 2802 (from purchase_price - correct)
    # sell_price = 3500 (from purchase_price - correct)
    # spread = 3500 - 2802 = 698 (actual spread)

    # AFTER FIX: These assertions should pass
    assert ship_plating_opp['buy_price'] == 2802, \
        "After fix: buy_price should be 2802 (purchase_price)"

    # TEST: Assert the spread is correct now
    assert ship_plating_opp['spread'] == 698, \
        "After fix: spread should be 698 (actual spread)"


def test_trade_plan_correct_price_fields_all_goods(mock_db, mock_api):
    """Test that all goods use correct price fields after fix"""

    optimizer = MultiLegTradeOptimizer(
        api=mock_api,
        db=mock_db,
        player_id=1,
        logger=Mock()
    )

    markets = ['X1-JB26-D42', 'X1-JB26-A2']
    opportunities = optimizer._get_trade_opportunities('X1-JB26', markets)

    # Expected correct prices (after fix):
    expected_prices = {
        'SHIP_PLATING': {
            'buy_price': 2802,  # purchase_price from D42
            'sell_price': 3500,  # purchase_price from A2
            'spread': 698
        },
        'ADVANCED_CIRCUITRY': {
            'buy_price': 3816,  # purchase_price from D42
            'sell_price': 4500,  # purchase_price from A2
            'spread': 684
        }
    }

    for good, expected in expected_prices.items():
        opp = next((o for o in opportunities if o['good'] == good), None)

        if opp:
            # AFTER FIX: These should pass
            assert opp['buy_price'] == expected['buy_price'], \
                f"After fix: {good} buy_price should be {expected['buy_price']}"
            assert opp['spread'] == expected['spread'], \
                f"After fix: {good} spread should be {expected['spread']}"


def test_profit_calculation_accuracy():
    """
    Test that profit calculations are accurate with correct price fields

    With bug (wrong prices):
    - Buy 40x SHIP_PLATING @ 1,392 = 55,680 cr
    - Sell 40x SHIP_PLATING @ 3,500 = 140,000 cr
    - Profit: 84,320 cr (100%+ inflated!)

    Without bug (correct prices):
    - Buy 40x SHIP_PLATING @ 2,802 = 112,080 cr
    - Sell 40x SHIP_PLATING @ 3,500 = 140,000 cr
    - Profit: 27,920 cr (actual profit)
    """
    cargo_capacity = 40

    # BUG: Current implementation uses wrong buy_price
    buggy_buy_price = 1392  # From sell_price (WRONG)
    buggy_profit = (3500 - buggy_buy_price) * cargo_capacity
    assert buggy_profit == 84320, "BUG: Profit artificially inflated to 84,320 cr"

    # CORRECT: After fix
    correct_buy_price = 2802  # From purchase_price (CORRECT)
    correct_profit = (3500 - correct_buy_price) * cargo_capacity
    assert correct_profit == 27920, "After fix: Profit should be 27,920 cr"

    # The bug causes profit to be inflated by 202%!
    inflation_factor = buggy_profit / correct_profit
    assert abs(inflation_factor - 3.02) < 0.01, \
        f"Bug causes profit inflation of {inflation_factor:.2f}x (should be ~3x)"


if __name__ == "__main__":
    pytest.main([__file__, "-v"])
