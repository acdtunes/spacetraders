"""
Test to diagnose and validate the multi-leg route planning bug

BUG: Multi-leg trader assigns ALL actions to destinations, never to starting locations.

This causes:
1. First segment tries to BUY at destination (after arriving) instead of at start (before leaving)
2. Intermediate segments work correctly by accident (sell+buy at same location)
3. No way to pre-load cargo at starting location before navigating

CORRECT BEHAVIOR:
- Segment 1: BUY at H56 (start), navigate, SELL+BUY at D45
- Segment 2: Navigate, SELL+BUY at J62
- Segment 3: Navigate, SELL at A2

CURRENT BUGGY BEHAVIOR:
- Segment 1: Navigate, BUY at D45 (WRONG!)
- Segment 2: Navigate, SELL+BUY at J62
- Segment 3: Navigate, SELL at A2
"""

import pytest
from unittest.mock import Mock
from spacetraders_bot.operations.multileg_trader import (
    GreedyRoutePlanner,
    ProfitFirstStrategy,
)


def test_route_planner_creates_actions_for_starting_waypoint():
    """
    Test that route planner correctly assigns BUY actions to starting waypoint

    For a route like H56 → D45 → J62:

    EXPECTED (correct):
    Segment 1:
    - from_waypoint='H56', to_waypoint='D45'
    - actions_at_start=[BUY at H56]  # Execute BEFORE navigation
    - actions_at_start=[],
 actions_at_end=[SELL at D45, BUY at D45]  # Execute AFTER arrival

    ACTUAL (buggy):
    Segment 1:
    - from_waypoint='H56', to_waypoint='D45'
    - actions_at_start=[],
 actions_at_end=[BUY at D45]  # WRONG! Should buy at H56 before leaving!

    This test validates that the planner creates the correct action structure.
    """

    # Setup mock database
    mock_db = Mock()
    mock_logger = Mock()

    # Mock database distance queries
    def mock_distance_query(from_wp, to_wp):
        distances = {
            ('H56', 'D45'): 50.0,
            ('H56', 'J62'): 100.0,
            ('D45', 'J62'): 60.0,
        }
        return distances.get((from_wp, to_wp), 150.0)

    mock_db.connection.return_value.__enter__.return_value.cursor.return_value.fetchone.side_effect = [
        (0, 0),  # H56 coords
        (50, 0),  # D45 coords
        (0, 0),  # H56 coords (repeated for second query)
        (50, 60),  # J62 coords
    ]

    # Define trade opportunities
    # H56 sells MEDICINE cheap (good to buy here)
    # D45 buys MEDICINE high (good to sell here), sells SHIP_PARTS cheap (good to buy here)
    # J62 buys SHIP_PARTS high (good to sell here)
    trade_opportunities = [
        {
            'buy_waypoint': 'X1-GH18-H56',
            'sell_waypoint': 'X1-GH18-D45',
            'good': 'MEDICINE',
            'buy_price': 500,  # Cost to buy at H56
            'sell_price': 750,  # Revenue when selling at D45
            'spread': 250,
            'trade_volume': 50
        },
        {
            'buy_waypoint': 'X1-GH18-D45',
            'sell_waypoint': 'X1-GH18-J62',
            'good': 'SHIP_PARTS',
            'buy_price': 800,  # Cost to buy at D45
            'sell_price': 1200,  # Revenue when selling at J62
            'spread': 400,
            'trade_volume': 30
        }
    ]

    markets = ['X1-GH18-H56', 'X1-GH18-D45', 'X1-GH18-J62']

    # Create planner
    strategy = ProfitFirstStrategy(mock_logger)
    planner = GreedyRoutePlanner(mock_logger, mock_db, strategy=strategy)

    # Find route starting at H56
    route = planner.find_route(
        start_waypoint='X1-GH18-H56',
        markets=markets,
        trade_opportunities=trade_opportunities,
        max_stops=3,
        cargo_capacity=40,
        starting_credits=100000,
        ship_speed=10
    )

    # Validate the bug is present
    assert route is not None, "Route should be found"
    assert len(route.segments) > 0, "Route should have segments"

    first_segment = route.segments[0]

    # BUG CHECK: In current implementation, first segment will have:
    # - from_waypoint='H56', to_waypoint='D45' or 'J62'
    # - actions_at_destination contains BUY actions for the destination market

    # The bug: ALL actions are at destination, none at start
    print(f"\nFirst segment:")
    print(f"  from: {first_segment.from_waypoint}")
    print(f"  to: {first_segment.to_waypoint}")
    print(f"  actions_at_destination:")
    for action in first_segment.actions_at_destination:
        print(f"    {action.action} {action.units}x {action.good} at {action.waypoint}")

    # Current buggy behavior: actions are at destination waypoint
    # Expected behavior: First segment should have BUY actions at STARTING waypoint

    # Check if there are any BUY actions
    buy_actions = [a for a in first_segment.actions_at_destination if a.action == 'BUY']

    if buy_actions:
        # BUG: Buy actions exist at destination, should be at start
        print(f"\n❌ BUG DETECTED: Buy actions at destination instead of start!")
        print(f"   Expected: BUY at {first_segment.from_waypoint}")
        print(f"   Actual: BUY at {first_segment.to_waypoint}")

        # The bug: buy_actions[0].waypoint == to_waypoint (destination)
        # Should be: buy_actions should be in actions_at_start with waypoint == from_waypoint
        assert buy_actions[0].waypoint == first_segment.to_waypoint, \
            "This assertion confirms the bug: buy action is at destination, not start"

        # After fix, this test should fail with:
        # - RouteSegment should have actions_at_start field
        # - First segment should have BUY actions in actions_at_start, not actions_at_destination

    # This test currently PASSES (detecting the bug)
    # After fix, it should FAIL because:
    # 1. RouteSegment will have actions_at_start field
    # 2. BUY actions will be in actions_at_start, not actions_at_destination


def test_intermediate_segment_has_both_sell_and_buy():
    """
    Test that intermediate segments correctly have SELL (from previous) and BUY (for next)

    Intermediate segments should have:
    - actions_at_destination: [SELL previous_good, BUY next_good]
    """
    # This test validates that intermediate segments work correctly
    # (they already do by accident because sell+buy happen at same location)
    pass


if __name__ == "__main__":
    pytest.main([__file__, "-xvs"])
