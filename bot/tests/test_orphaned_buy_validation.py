"""
Test for P1 Bug: Route Planner Creates BUY Actions Without Corresponding SELL Destinations

ROOT CAUSE: GreedyRoutePlanner can create routes where:
1. A BUY action is added for a good at market A
2. No subsequent segment has a SELL action for that same good
3. This creates "orphaned inventory" that can never be sold

PRODUCTION EVIDENCE:
- SILMARETH-D circuit breaker failure
- Route had BUY action for SHIP_PARTS
- Price spiked 102.5% (3,182 vs planned 1,571 cr/unit)
- Auto-recovery tried to find SELL destination: NOT FOUND
- Cost: 67,722 credit loss

EXPECTED BEHAVIOR:
- Route validation should reject routes with orphaned BUY actions
- Every BUY must have a matching SELL later in the route
- Planner should not create such routes in the first place
"""

import pytest
from unittest.mock import Mock, MagicMock
from spacetraders_bot.operations.multileg_trader import (
    GreedyRoutePlanner,
    ProfitFirstStrategy,
    TradeAction,
    RouteSegment,
    MultiLegRoute,
)


def test_route_validation_rejects_orphaned_buy_actions():
    """
    Test that route validation catches BUY actions without corresponding SELL actions

    Scenario: Route with orphaned BUY
    - Segment 1: BUY 10x SHIP_PARTS at D46 (no SELL for SHIP_PARTS in route)
    - Segment 2: SELL 5x ALUMINUM at B22 (different good)
    - Segment 3: SELL 8x COPPER at A2 (different good)

    This should be REJECTED by validation.
    """
    # Create a route with orphaned BUY action
    segments = [
        RouteSegment(
            from_waypoint='X1-GH18-H56',
            to_waypoint='X1-GH18-D46',
            distance=50,
            fuel_cost=55,
            actions_at_start=[
                TradeAction(
                    waypoint='X1-GH18-H56',
                    good='SHIP_PARTS',
                    action='BUY',
                    units=10,
                    price_per_unit=1571,
                    total_value=15710
                )
            ],
            actions_at_end=[],
            cargo_after={'SHIP_PARTS': 10},
            credits_after=84290,
            cumulative_profit=0
        ),
        RouteSegment(
            from_waypoint='X1-GH18-D46',
            to_waypoint='X1-GH18-B22',
            distance=60,
            fuel_cost=66,
            actions_at_start=[],
            actions_at_end=[
                TradeAction(
                    waypoint='X1-GH18-B22',
                    good='ALUMINUM',  # Different good!
                    action='SELL',
                    units=5,
                    price_per_unit=800,
                    total_value=4000
                )
            ],
            cargo_after={'SHIP_PARTS': 10, 'ALUMINUM': 0},
            credits_after=88290,
            cumulative_profit=4000
        ),
        RouteSegment(
            from_waypoint='X1-GH18-B22',
            to_waypoint='X1-GH18-A2',
            distance=40,
            fuel_cost=44,
            actions_at_start=[],
            actions_at_end=[
                TradeAction(
                    waypoint='X1-GH18-A2',
                    good='COPPER',  # Different good!
                    action='SELL',
                    units=8,
                    price_per_unit=500,
                    total_value=4000
                )
            ],
            cargo_after={'SHIP_PARTS': 10},  # Orphaned cargo!
            credits_after=92290,
            cumulative_profit=8000
        )
    ]

    route = MultiLegRoute(
        segments=segments,
        total_profit=7835,  # Fake profit (doesn't account for orphaned goods)
        total_distance=150,
        total_fuel_cost=165,
        estimated_time_minutes=90
    )

    # Validate route (this function doesn't exist yet - we need to create it)
    from spacetraders_bot.operations.multileg_trader import validate_route_completeness

    is_valid, errors = validate_route_completeness(route)

    # Test should FAIL before fix (function doesn't exist)
    # After fix, should return False with error message
    assert not is_valid, "Route with orphaned BUY should be rejected"
    assert len(errors) > 0, "Should have validation errors"
    assert any('SHIP_PARTS' in error for error in errors), "Should mention orphaned good"
    assert any('Orphaned BUY' in error or 'never sold' in error for error in errors), "Should mention orphaned goods"


def test_route_validation_accepts_complete_routes():
    """
    Test that validation accepts routes where every BUY has a matching SELL

    Scenario: Valid route
    - Segment 1: BUY 10x SHIP_PARTS at H56
    - Segment 2: Navigate to D46, SELL 10x SHIP_PARTS, BUY 8x ALUMINUM
    - Segment 3: Navigate to A2, SELL 8x ALUMINUM

    All goods purchased are eventually sold - VALID route.
    """
    segments = [
        RouteSegment(
            from_waypoint='X1-GH18-H56',
            to_waypoint='X1-GH18-D46',
            distance=50,
            fuel_cost=55,
            actions_at_start=[
                TradeAction(
                    waypoint='X1-GH18-H56',
                    good='SHIP_PARTS',
                    action='BUY',
                    units=10,
                    price_per_unit=1500,
                    total_value=15000
                )
            ],
            actions_at_end=[
                TradeAction(
                    waypoint='X1-GH18-D46',
                    good='SHIP_PARTS',
                    action='SELL',
                    units=10,
                    price_per_unit=2000,
                    total_value=20000
                ),
                TradeAction(
                    waypoint='X1-GH18-D46',
                    good='ALUMINUM',
                    action='BUY',
                    units=8,
                    price_per_unit=500,
                    total_value=4000
                )
            ],
            cargo_after={'ALUMINUM': 8},
            credits_after=101000,
            cumulative_profit=5000
        ),
        RouteSegment(
            from_waypoint='X1-GH18-D46',
            to_waypoint='X1-GH18-A2',
            distance=40,
            fuel_cost=44,
            actions_at_start=[],
            actions_at_end=[
                TradeAction(
                    waypoint='X1-GH18-A2',
                    good='ALUMINUM',
                    action='SELL',
                    units=8,
                    price_per_unit=800,
                    total_value=6400
                )
            ],
            cargo_after={},
            credits_after=107400,
            cumulative_profit=11400
        )
    ]

    route = MultiLegRoute(
        segments=segments,
        total_profit=11301,
        total_distance=90,
        total_fuel_cost=99,
        estimated_time_minutes=54
    )

    from spacetraders_bot.operations.multileg_trader import validate_route_completeness

    is_valid, errors = validate_route_completeness(route)

    assert is_valid, f"Valid route should be accepted: {errors}"
    assert len(errors) == 0, "Valid route should have no errors"


def test_greedy_planner_does_not_create_orphaned_buys():
    """
    Test that GreedyRoutePlanner.find_route() validates and rejects orphaned BUYs

    This test creates a scenario where:
    1. Planner would naturally create an orphaned BUY
    2. Validation catches it
    3. Planner returns None (no valid route)
    """
    mock_db = MagicMock()
    mock_logger = MagicMock()

    # Mock distance queries (simple grid)
    waypoint_coords = {
        'X1-TEST-A': (0, 0),
        'X1-TEST-B': (50, 0),
        'X1-TEST-C': (100, 0),
    }

    mock_cursor = MagicMock()
    mock_cursor.fetchone.side_effect = [
        (0, 0), (50, 0),   # A to B
        (0, 0), (100, 0),  # A to C
        (50, 0), (100, 0), # B to C
    ]

    mock_conn = MagicMock()
    mock_conn.cursor.return_value = mock_cursor
    mock_db.connection.return_value.__enter__.return_value = mock_conn

    # Trade opportunities that would create orphaned BUY
    # A sells MEDICINE cheap (good BUY)
    # B sells SHIP_PARTS cheap (good BUY)
    # C buys ALUMINUM (but we never bought ALUMINUM!)
    trade_opportunities = [
        {
            'buy_waypoint': 'X1-TEST-A',
            'sell_waypoint': 'X1-TEST-C',  # Far away, low profit
            'good': 'MEDICINE',
            'buy_price': 500,
            'sell_price': 550,  # Only 50 profit
            'spread': 50,
            'trade_volume': 10
        },
        {
            'buy_waypoint': 'X1-TEST-B',
            'sell_waypoint': 'X1-TEST-C',  # Far away, low profit
            'good': 'SHIP_PARTS',
            'buy_price': 1500,
            'sell_price': 1600,  # Only 100 profit
            'spread': 100,
            'trade_volume': 5
        },
        {
            'buy_waypoint': 'X1-TEST-A',
            'sell_waypoint': 'X1-TEST-B',  # Close, high profit
            'good': 'ALUMINUM',
            'buy_price': 300,
            'sell_price': 800,  # 500 profit!
            'spread': 500,
            'trade_volume': 20
        },
    ]

    # Problem: If planner chooses A→B route for ALUMINUM, then B→C for SHIP_PARTS
    # it will BUY SHIP_PARTS at B but never SELL it (C only buys ALUMINUM)

    markets = ['X1-TEST-A', 'X1-TEST-B', 'X1-TEST-C']

    strategy = ProfitFirstStrategy(mock_logger)
    planner = GreedyRoutePlanner(mock_logger, mock_db, strategy=strategy)

    route = planner.find_route(
        start_waypoint='X1-TEST-A',
        markets=markets,
        trade_opportunities=trade_opportunities,
        max_stops=3,
        cargo_capacity=40,
        starting_credits=100000,
        ship_speed=10
    )

    # After fix: Route should either:
    # 1. Be None (no valid route found)
    # 2. Be valid (all BUYs have matching SELLs)

    if route is not None:
        from spacetraders_bot.operations.multileg_trader import validate_route_completeness
        is_valid, errors = validate_route_completeness(route)
        assert is_valid, f"Planner created invalid route with orphaned BUYs: {errors}"


if __name__ == "__main__":
    pytest.main([__file__, "-xvs"])
