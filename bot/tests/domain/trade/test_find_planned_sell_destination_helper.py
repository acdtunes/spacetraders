"""
Unit test for _find_planned_sell_destination helper function

This test validates that the helper correctly identifies the planned sell destination
for a good when cargo cleanup is triggered mid-route.
"""

import pytest
from spacetraders_bot.operations.multileg_trader import (
    RouteSegment,
    TradeAction,
    MultiLegRoute,
    _find_planned_sell_destination,
)


def test_find_planned_sell_destination_finds_next_segment():
    """
    Test that helper finds SELL action in the next segment

    Route structure:
    - Segment 0: D42 → E45, BUY ALUMINUM @ E45
    - Segment 1: E45 → D42, SELL ALUMINUM @ D42

    When circuit breaker triggers after segment 0, helper should find segment 1's SELL action.
    """
    route = MultiLegRoute(
        segments=[
            # Segment 0: D42 → E45, BUY ALUMINUM
            RouteSegment(
                from_waypoint='X1-JB26-D42',
                to_waypoint='X1-JB26-E45',
                distance=50,
                fuel_cost=55,
                actions_at_destination=[
                    TradeAction('X1-JB26-E45', 'ALUMINUM', 'BUY', 60, 68, 4080)
                ],
                cargo_after={'ALUMINUM': 60},
                credits_after=95920,
                cumulative_profit=-4080
            ),
            # Segment 1: E45 → D42, SELL ALUMINUM
            RouteSegment(
                from_waypoint='X1-JB26-E45',
                to_waypoint='X1-JB26-D42',
                distance=50,
                fuel_cost=55,
                actions_at_destination=[
                    TradeAction('X1-JB26-D42', 'ALUMINUM', 'SELL', 60, 558, 33480)
                ],
                cargo_after={},
                credits_after=129400,
                cumulative_profit=29400
            )
        ],
        total_profit=29400,
        total_distance=100,
        total_fuel_cost=110,
        estimated_time_minutes=60
    )

    # Circuit breaker triggers after segment 0 completes
    current_segment_index = 0

    # Helper should find segment 1's SELL action for ALUMINUM
    result = _find_planned_sell_destination('ALUMINUM', route, current_segment_index)

    print(f"\nSearching for ALUMINUM sell destination from segment {current_segment_index}")
    print(f"Result: {result}")
    print(f"Expected: X1-JB26-D42")

    assert result == 'X1-JB26-D42', \
        f"Should find planned sell destination D42, got {result}"


def test_find_planned_sell_destination_returns_none_if_no_sell_action():
    """
    Test that helper returns None when no SELL action exists for the good
    """
    route = MultiLegRoute(
        segments=[
            RouteSegment(
                from_waypoint='X1-JB26-D42',
                to_waypoint='X1-JB26-E45',
                distance=50,
                fuel_cost=55,
                actions_at_destination=[
                    TradeAction('X1-JB26-E45', 'ALUMINUM', 'BUY', 60, 68, 4080)
                ],
                cargo_after={'ALUMINUM': 60},
                credits_after=95920,
                cumulative_profit=-4080
            ),
            # Segment 1 has SELL but for different good
            RouteSegment(
                from_waypoint='X1-JB26-E45',
                to_waypoint='X1-JB26-D42',
                distance=50,
                fuel_cost=55,
                actions_at_destination=[
                    TradeAction('X1-JB26-D42', 'COPPER', 'SELL', 40, 300, 12000)
                ],
                cargo_after={},
                credits_after=107920,
                cumulative_profit=7920
            )
        ],
        total_profit=7920,
        total_distance=100,
        total_fuel_cost=110,
        estimated_time_minutes=60
    )

    current_segment_index = 0

    # Helper should return None (no SELL action for ALUMINUM)
    result = _find_planned_sell_destination('ALUMINUM', route, current_segment_index)

    assert result is None, \
        f"Should return None when no SELL action exists for ALUMINUM, got {result}"


def test_find_planned_sell_destination_skips_to_correct_segment():
    """
    Test that helper searches only future segments, not past segments
    """
    route = MultiLegRoute(
        segments=[
            # Segment 0: Already completed
            RouteSegment(
                from_waypoint='X1-JB26-A1',
                to_waypoint='X1-JB26-B2',
                distance=30,
                fuel_cost=35,
                actions_at_destination=[
                    TradeAction('X1-JB26-B2', 'ALUMINUM', 'SELL', 40, 500, 20000)
                ],
                cargo_after={},
                credits_after=120000,
                cumulative_profit=20000
            ),
            # Segment 1: Current segment (circuit breaker triggers here)
            RouteSegment(
                from_waypoint='X1-JB26-B2',
                to_waypoint='X1-JB26-E45',
                distance=50,
                fuel_cost=55,
                actions_at_destination=[
                    TradeAction('X1-JB26-E45', 'ALUMINUM', 'BUY', 60, 68, 4080)
                ],
                cargo_after={'ALUMINUM': 60},
                credits_after=115920,
                cumulative_profit=15920
            ),
            # Segment 2: Future segment with planned SELL
            RouteSegment(
                from_waypoint='X1-JB26-E45',
                to_waypoint='X1-JB26-D42',
                distance=50,
                fuel_cost=55,
                actions_at_destination=[
                    TradeAction('X1-JB26-D42', 'ALUMINUM', 'SELL', 60, 558, 33480)
                ],
                cargo_after={},
                credits_after=149400,
                cumulative_profit=49400
            )
        ],
        total_profit=49400,
        total_distance=130,
        total_fuel_cost=145,
        estimated_time_minutes=90
    )

    # Circuit breaker triggers after segment 1
    current_segment_index = 1

    # Helper should find segment 2's SELL action (not segment 0's past SELL)
    result = _find_planned_sell_destination('ALUMINUM', route, current_segment_index)

    assert result == 'X1-JB26-D42', \
        f"Should find future SELL action in segment 2 (D42), got {result}"


if __name__ == '__main__':
    pytest.main([__file__, '-v', '-s'])
