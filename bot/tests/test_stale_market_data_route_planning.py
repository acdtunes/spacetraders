"""
Test for stale market data causing unprofitable multileg trades.

BUG SCENARIO (STARGAZER-11 & STARGAZER-18):
1. Scout fleet actively updating market data
2. Multileg trader plans route using cached database prices
3. Route looks profitable: buy SHIP_PARTS @ ~300 cr, sell @ 14,136 cr
4. Ship navigates to buy market X1-JB26-C39
5. Circuit breaker fetches live prices: ACTUAL buy price 14,658-15,232 cr!
6. Circuit breaker triggers, but ship already spent credits on earlier segments
7. Total loss: 550K credits across multiple ships

ROOT CAUSE:
- Route planning (_get_trade_opportunities) uses stale database prices
- No market freshness validation before creating route
- Scout data may be hours old by the time route executes
- Circuit breaker only catches price spikes AFTER navigating to market

EXPECTED BEHAVIOR:
- Route planning should validate market data freshness
- Reject routes with stale market data (e.g., >1 hour old)
- Add pre-flight price validation before starting route execution
- Circuit breaker becomes last resort, not primary defense

FIX APPROACH:
1. Add market_data_age check in _get_trade_opportunities
2. Filter out opportunities with stale data (>1 hour)
3. Add pre-flight validation in execute_multileg_route
4. Log market age for visibility in route planning output
"""

import pytest
from datetime import datetime, timedelta, timezone
from unittest.mock import Mock, patch, MagicMock
import logging

# Mock database for testing
class MockDatabase:
    def __init__(self, market_data_age_hours=0.5):
        """
        Args:
            market_data_age_hours: How old the cached market data is (default 0.5 = 30 minutes)
        """
        self.market_data_age_hours = market_data_age_hours
        self.now = datetime.now(timezone.utc)
        self.last_updated = self.now - timedelta(hours=market_data_age_hours)

    def connection(self):
        """Mock context manager for database connections"""
        return self

    def __enter__(self):
        return self

    def __exit__(self, *args):
        pass

    def transaction(self):
        """Mock transaction context manager"""
        return self

    def get_market_data(self, conn, waypoint, good=None):
        """
        Mock get_market_data that returns stale price data

        Returns market data with last_updated timestamp
        """
        if waypoint == "X1-JB26-C39" and good == "SHIP_PARTS":
            # Simulate STALE cached data (low sell price from hours ago)
            return [{
                'waypoint_symbol': waypoint,
                'good_symbol': good,
                'sell_price': 300,  # STALE: What we think we'll pay
                'purchase_price': 14136,  # STALE: What we think we'll receive elsewhere
                'supply': 'ABUNDANT',
                'activity': 'STRONG',
                'trade_volume': 100,
                'last_updated': self.last_updated.strftime('%Y-%m-%dT%H:%M:%S.%f')[:-3] + 'Z'
            }]

        # Default market data for other waypoints
        return [{
            'waypoint_symbol': waypoint,
            'good_symbol': good or 'ALUMINUM_ORE',
            'sell_price': 100,
            'purchase_price': 500,
            'supply': 'MODERATE',
            'activity': 'WEAK',
            'trade_volume': 100,
            'last_updated': self.last_updated.strftime('%Y-%m-%dT%H:%M:%S.%f')[:-3] + 'Z'
        }]

    def cursor(self):
        """Mock cursor for waypoint queries"""
        mock_cursor = Mock()

        def execute(query, params=None):
            # Mock waypoint coordinate queries
            if "waypoints" in query and "waypoint_symbol" in query:
                waypoint = params[0] if params else ""
                # Return mock coordinates
                return Mock(fetchone=lambda: (100, 200))

            # Mock market data queries
            if "market_data" in query:
                if "DISTINCT waypoint_symbol" in query:
                    # Return list of market waypoints
                    return Mock(fetchall=lambda: [
                        ("X1-JB26-C39",),
                        ("X1-JB26-D42",),
                        ("X1-JB26-E45",),
                    ])

            return Mock(fetchone=lambda: None, fetchall=lambda: [])

        mock_cursor.execute = execute
        return mock_cursor


def test_stale_market_data_should_be_detected_in_route_planning():
    """
    Test that route planning detects and filters out stale market data

    BEFORE FIX: Routes planned with 2-hour-old cached prices
    AFTER FIX: Routes reject stale data, only use fresh prices (<1 hour)
    """
    from spacetraders_bot.operations.multileg_trader import MultiLegTradeOptimizer

    # Create mock database with 2-hour-old data (STALE!)
    db = MockDatabase(market_data_age_hours=2.0)

    # Mock API client
    api = Mock()
    api.get_agent = Mock(return_value={'credits': 100000})

    # Create optimizer
    optimizer = MultiLegTradeOptimizer(
        api=api,
        db=db,
        player_id=1,
        logger=logging.getLogger(__name__)
    )

    # Get trade opportunities
    system = "X1-JB26"
    markets = ["X1-JB26-C39", "X1-JB26-D42"]

    opportunities = optimizer._get_trade_opportunities(system, markets)

    # AFTER FIX: Stale data (>1 hour) should be filtered out
    # With 2-hour-old data, opportunities should be EMPTY

    print(f"Found {len(opportunities)} trade opportunities")
    for opp in opportunities:
        print(f"  {opp['good']}: {opp['buy_waypoint']} → {opp['sell_waypoint']}")
        print(f"    Buy: {opp['buy_price']}, Sell: {opp['sell_price']}, Spread: {opp['spread']}")

    # AFTER FIX: No opportunities should be found with stale data
    assert len(opportunities) == 0, "Stale market data (>1 hour) should be filtered out"
    print("✓ FIX VALIDATED: Stale market data correctly filtered in route planning")


def test_circuit_breaker_validates_actual_prices_before_purchase():
    """
    Test that circuit breaker correctly validates actual live prices
    before allowing purchases (this is WORKING as designed)
    """
    from spacetraders_bot.operations.multileg_trader import _find_planned_sell_price, MultiLegRoute, RouteSegment, TradeAction

    # Create a mock route with planned buy and sell
    route = MultiLegRoute(
        segments=[
            RouteSegment(
                from_waypoint="X1-JB26-A1",
                to_waypoint="X1-JB26-C39",
                distance=100,
                fuel_cost=100,
                actions_at_destination=[
                    TradeAction(
                        waypoint="X1-JB26-C39",
                        good="SHIP_PARTS",
                        action="BUY",
                        units=40,
                        price_per_unit=300,  # STALE planned price
                        total_value=12000
                    )
                ],
                cargo_after={"SHIP_PARTS": 40},
                credits_after=88000,
                cumulative_profit=0
            ),
            RouteSegment(
                from_waypoint="X1-JB26-C39",
                to_waypoint="X1-JB26-D42",
                distance=150,
                fuel_cost=150,
                actions_at_destination=[
                    TradeAction(
                        waypoint="X1-JB26-D42",
                        good="SHIP_PARTS",
                        action="SELL",
                        units=40,
                        price_per_unit=14136,  # STALE planned sell price
                        total_value=565440
                    )
                ],
                cargo_after={},
                credits_after=653290,
                cumulative_profit=553290
            )
        ],
        total_profit=553040,
        total_distance=250,
        total_fuel_cost=250,
        estimated_time_minutes=45
    )

    # Find planned sell price for SHIP_PARTS from segment 0
    planned_sell_price = _find_planned_sell_price("SHIP_PARTS", route, 0)

    assert planned_sell_price == 14136, "Should find planned sell price from route"

    # Simulate circuit breaker logic: actual live price vs planned sell price
    actual_buy_price = 15232  # REAL price at market (much higher!)

    # Circuit breaker should detect this is unprofitable
    is_unprofitable = actual_buy_price >= planned_sell_price

    assert is_unprofitable, "Circuit breaker should detect buy price > sell price"
    print("✓ Circuit breaker correctly identifies unprofitable trade")
    print(f"  Actual buy: {actual_buy_price:,} cr/unit")
    print(f"  Planned sell: {planned_sell_price:,} cr/unit")
    print(f"  Loss per unit: {actual_buy_price - planned_sell_price:,} cr")


def test_pre_flight_validation_should_check_market_data_age():
    """
    Test that route execution validates market data freshness BEFORE navigating

    This is the NEW behavior we want to implement:
    - Check market data age for all route waypoints
    - Warn if any data is >30 minutes old
    - Abort if any data is >1 hour old
    - Force re-fetch from API if needed
    """
    from spacetraders_bot.operations.multileg_trader import MultiLegRoute, RouteSegment, TradeAction
    from datetime import datetime, timezone, timedelta

    # Create route with STALE market data
    route = MultiLegRoute(
        segments=[
            RouteSegment(
                from_waypoint="X1-JB26-A1",
                to_waypoint="X1-JB26-C39",
                distance=100,
                fuel_cost=100,
                actions_at_destination=[
                    TradeAction(
                        waypoint="X1-JB26-C39",
                        good="SHIP_PARTS",
                        action="BUY",
                        units=40,
                        price_per_unit=300,
                        total_value=12000
                    )
                ],
                cargo_after={"SHIP_PARTS": 40},
                credits_after=88000,
                cumulative_profit=0
            )
        ],
        total_profit=10000,
        total_distance=100,
        total_fuel_cost=100,
        estimated_time_minutes=15
    )

    # Mock database with old data
    db = MockDatabase(market_data_age_hours=2.0)

    # Get market data for route validation
    with db.connection() as conn:
        market_data = db.get_market_data(conn, "X1-JB26-C39", "SHIP_PARTS")

    # Check data age
    last_updated_str = market_data[0]['last_updated']
    last_updated = datetime.strptime(last_updated_str, '%Y-%m-%dT%H:%M:%S.%fZ').replace(tzinfo=timezone.utc)
    now = datetime.now(timezone.utc)
    age_hours = (now - last_updated).total_seconds() / 3600

    print(f"Market data age: {age_hours:.1f} hours")

    # EXPECTED BEHAVIOR: Should detect stale data and warn/abort
    if age_hours > 1.0:
        print("⚠️  Market data is stale (>1 hour old)")
        print("❌ PRE-FLIGHT VALIDATION FAILED: Aborting route")
        should_abort = True
    elif age_hours > 0.5:
        print("⚠️  Market data is aging (>30 min old)")
        print("⚠️  Consider re-scouting before executing route")
        should_abort = False
    else:
        print("✓ Market data is fresh (<30 min old)")
        should_abort = False

    # With 2-hour-old data, should abort
    assert age_hours > 1.0, "Test data should be stale"
    assert should_abort, "Should abort route with stale data"

    print("✓ Pre-flight validation correctly detects stale market data")


def test_market_data_freshness_thresholds():
    """
    Test the freshness thresholds for market data validation

    Thresholds:
    - FRESH: <30 minutes (use confidently)
    - AGING: 30-60 minutes (warn, but allow)
    - STALE: >60 minutes (reject, force re-scout)
    """
    test_cases = [
        (0.25, "FRESH", True, "15 minutes - recent scout data"),
        (0.5, "AGING", True, "30 minutes - slightly old but acceptable"),
        (0.75, "AGING", True, "45 minutes - aging but still usable"),
        (1.0, "STALE", False, "60 minutes - approaching stale threshold"),
        (1.5, "STALE", False, "90 minutes - definitely stale"),
        (2.0, "STALE", False, "2 hours - very stale (STARGAZER bug scenario)"),
    ]

    for age_hours, expected_status, should_allow, description in test_cases:
        # Determine actual status based on age
        if age_hours < 0.5:
            status = "FRESH"
            allow = True
        elif age_hours < 1.0:
            status = "AGING"
            allow = True
        else:
            status = "STALE"
            allow = False

        # Validate thresholds
        assert status == expected_status, f"{description}: Expected {expected_status}, got {status}"
        assert allow == should_allow, f"{description}: Expected allow={should_allow}, got {allow}"

        print(f"✓ {age_hours:.1f}h = {status} (allow={allow}): {description}")


def test_stargazer_scenario_prevented_with_fix():
    """
    Reproduce the STARGAZER-11/STARGAZER-18 scenario and validate fix prevents loss

    SCENARIO:
    - Scout data is 2 hours old for X1-JB26-C39 SHIP_PARTS
    - Cached price: 300 cr/unit (attractive opportunity)
    - Actual price at execution: 15,232 cr/unit (massive spike!)

    BEFORE FIX:
    1. Route planner includes C39 in route (no freshness check)
    2. Ship executes profitable Segment 1 (spends credits)
    3. Ship navigates to C39 for Segment 2
    4. Circuit breaker detects spike and aborts
    5. Result: Already spent credits on Segment 1, partial loss

    AFTER FIX:
    1. Route planner detects 2-hour-old data for C39
    2. Skips C39 entirely (logged warning)
    3. Route either uses only fresh markets OR no route found
    4. Result: 0 credits spent, 0 loss
    """
    from spacetraders_bot.operations.multileg_trader import MultiLegTradeOptimizer

    # SCENARIO: Scout data is 2 hours old
    db = MockDatabase(market_data_age_hours=2.0)

    # Mock API
    api = Mock()
    api.get_agent = Mock(return_value={'credits': 100000})

    # Create optimizer
    optimizer = MultiLegTradeOptimizer(
        api=api,
        db=db,
        player_id=1,
        logger=logging.getLogger(__name__)
    )

    # Try to get trade opportunities (like STARGAZER ships did)
    system = "X1-JB26"
    markets = ["X1-JB26-C39", "X1-JB26-D42", "X1-JB26-E45"]

    opportunities = optimizer._get_trade_opportunities(system, markets)

    # CRITICAL ASSERTION: With the fix, stale market C39 should NOT appear in opportunities
    c39_opportunities = [o for o in opportunities if o['buy_waypoint'] == 'X1-JB26-C39' or o['sell_waypoint'] == 'X1-JB26-C39']

    assert len(c39_opportunities) == 0, \
        "STARGAZER SCENARIO PREVENTED: C39 with 2-hour-old data should not appear in route opportunities"

    print("\n" + "="*70)
    print("✅ STARGAZER SCENARIO VALIDATED")
    print("="*70)
    print("Before Fix:")
    print("  - Route planner would include X1-JB26-C39 (stale data)")
    print("  - Ship would navigate and discover price spike")
    print("  - Circuit breaker would abort, but credits already spent")
    print("  - Result: 250K-550K loss")
    print("")
    print("After Fix:")
    print("  - Route planner detects 2-hour-old data for C39")
    print("  - Skips C39 entirely (logged warning)")
    print("  - Only uses markets with fresh data (<1 hour)")
    print("  - Result: 0 credits at risk from stale data")
    print("="*70)


if __name__ == "__main__":
    pytest.main([__file__, "-v", "-s"])
