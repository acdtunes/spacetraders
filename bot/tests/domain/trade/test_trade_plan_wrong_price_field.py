"""
Test for STARHOPPER-1 trading loss bug: Wrong price field used in route planning.

Root cause: multileg_trader.py line 1152 uses `purchase_price` (what market pays to BUY from traders)
instead of `sell_price` (what traders pay to BUY from market).

This causes route planner to calculate routes as profitable when they're actually unprofitable.
"""

import pytest
from datetime import datetime, timezone
from spacetraders_bot.operations.multileg_trader import MultiLegTradeOptimizer


def test_route_planner_uses_correct_price_fields():
    """
    Test that route planner uses correct database fields when calculating trade opportunities.

    Scenario: STARHOPPER-1 incident
        - D42 market: sell_price=2995 (what we pay), purchase_price=6064 (what they pay us)
        - D41 market: sell_price=2636 (what we pay), purchase_price=5398 (what they pay us)

    Expected:
        - BUY ELECTRONICS @ D42 for 2995 (using sell_price)
        - SELL ELECTRONICS @ D41 for 5398 (using purchase_price)
        - Spread: 5398 - 2995 = 2403 credits/unit (PROFITABLE)

    Bug (line 1152):
        - BUY ELECTRONICS @ D42 for 6064 (using purchase_price - WRONG!)
        - SELL ELECTRONICS @ D41 for 5398 (using purchase_price)
        - Spread: 5398 - 6064 = -666 credits/unit (UNPROFITABLE)
        - But route planner shows +2407 profit = (5398 - 2991) which matches neither field!
    """

    # Mock database with actual STARHOPPER-1 incident data
    class MockDB:
        def __init__(self):
            self.market_data = {
                ('X1-TX46-D42', 'ELECTRONICS'): [{
                    'waypoint_symbol': 'X1-TX46-D42',
                    'good_symbol': 'ELECTRONICS',
                    'sell_price': 2995,  # What WE pay to BUY from this market
                    'purchase_price': 6064,  # What market pays to BUY from us
                    'supply': 'MODERATE',
                    'activity': 'STRONG',
                    'trade_volume': 10,
                    'last_updated': datetime.now(timezone.utc).strftime('%Y-%m-%dT%H:%M:%S.%fZ')[:-3] + 'Z'
                }],
                ('X1-TX46-D41', 'ELECTRONICS'): [{
                    'waypoint_symbol': 'X1-TX46-D41',
                    'good_symbol': 'ELECTRONICS',
                    'sell_price': 2636,  # What WE pay to BUY from this market
                    'purchase_price': 5398,  # What market pays to BUY from us (higher = better sell destination)
                    'supply': 'LIMITED',
                    'activity': 'WEAK',
                    'trade_volume': 10,
                    'last_updated': datetime.now(timezone.utc).strftime('%Y-%m-%dT%H:%M:%S.%fZ')[:-3] + 'Z'
                }],
            }

        def connection(self):
            return self

        def __enter__(self):
            return self

        def __exit__(self, *args):
            pass

        def get_market_data(self, conn, waypoint, good):
            """Return market data for waypoint/good combo"""
            return self.market_data.get((waypoint, good), [])

    # Mock logger
    class MockLogger:
        def debug(self, *args, **kwargs):
            pass
        def info(self, *args, **kwargs):
            pass
        def warning(self, *args, **kwargs):
            pass
        def error(self, *args, **kwargs):
            pass

    # Create route optimizer (needs minimal setup for just calling _collect_opportunities_for_market)
    db = MockDB()
    logger = MockLogger()

    # Mock API client (not needed for this test)
    class MockAPI:
        pass

    optimizer = MultiLegTradeOptimizer(api=MockAPI(), db=db, player_id=6, logger=logger)

    # Collect trade opportunities
    opportunities = optimizer._collect_opportunities_for_market(
        conn=db,
        buy_market='X1-TX46-D42',
        buy_data=db.market_data[('X1-TX46-D42', 'ELECTRONICS')],
        markets=['X1-TX46-D41', 'X1-TX46-D42']
    )

    # Validate: Should find one opportunity (D42 → D41)
    assert len(opportunities) == 1, f"Expected 1 opportunity, found {len(opportunities)}"

    opp = opportunities[0]

    # CRITICAL ASSERTIONS: Verify correct price fields are used
    assert opp['buy_waypoint'] == 'X1-TX46-D42', "Wrong buy market"
    assert opp['sell_waypoint'] == 'X1-TX46-D41', "Wrong sell market"
    assert opp['good'] == 'ELECTRONICS', "Wrong trade good"

    # BUG CHECK: Route planner should use sell_price (2995) for buying, not purchase_price (6064)
    assert opp['buy_price'] == 2995, (
        f"BUG: Route planner used purchase_price ({opp['buy_price']}) instead of sell_price (2995) "
        f"when buying from D42. This causes false profitable routes!"
    )

    # Sell price should use purchase_price (what market pays us)
    assert opp['sell_price'] == 5398, (
        f"Expected sell_price=5398 (D41 purchase_price), got {opp['sell_price']}"
    )

    # Spread should be profitable: 5398 - 2995 = 2403
    expected_spread = 5398 - 2995
    assert opp['spread'] == expected_spread, (
        f"Expected spread={expected_spread} (5398-2995), got {opp['spread']}. "
        f"This indicates wrong price field usage!"
    )

    # Final validation: Route should be profitable
    assert opp['spread'] > 0, "Route should be profitable with correct price fields"

    print(f"✅ TEST PASSED: Route planner uses correct price fields")
    print(f"   Buy from D42 @ {opp['buy_price']} (using sell_price field)")
    print(f"   Sell to D41 @ {opp['sell_price']} (using purchase_price field)")
    print(f"   Spread: {opp['spread']} credits/unit")


if __name__ == '__main__':
    test_route_planner_uses_correct_price_fields()
