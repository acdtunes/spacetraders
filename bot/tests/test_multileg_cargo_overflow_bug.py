"""
Test for multi-leg trader cargo overflow bug

CRITICAL BUG REPRODUCTION: STARHOPPER-D cargo capacity error

**Bug Description:**
The route planner generated a route where cumulative cargo exceeds ship capacity (80 units).
When executing segment 3 at K91, the ship tried to BUY 20x CLOTHING but cargo was already
full with 80 units from previous segments:
- 18x SHIP_PLATING
- 4x ADVANCED_CIRCUITRY
- 20x ALUMINUM
- 18x SHIP_PARTS
- 20x MEDICINE

**Error:**
POST /my/ships/STARHOPPER-D/purchase - Client Error (HTTP 400): 4217 -
Failed to update ship cargo. Cannot add 2 unit(s) to ship cargo.
Exceeds max limit of 80.

**Route That Failed:**
STARHOPPER-D: H51 → K91 → D41 → J55 → H48

**Root Cause:**
The route planner allows buying goods at multiple segments without selling them first,
causing cargo to accumulate beyond capacity. The `_apply_sell_actions` only sells goods
that have a sell opportunity AT THE CURRENT WAYPOINT. If goods are destined for FUTURE
waypoints, they remain in cargo and accumulate.

Example:
- Segment 1: Buy SHIP_PLATING (for D41), buy ADVANCED_CIRCUITRY (for J55) → 22 units
- Segment 2: At K91, buy ALUMINUM (for D41), SHIP_PARTS (for J55), MEDICINE (for H48) → 22 + 58 = 80 units
- Segment 2: At K91, try to buy CLOTHING (for H48) → 80 + 20 = 100 (OVERFLOW!)

The planner doesn't account for goods that won't be sold at intermediate waypoints.
"""

import pytest
from unittest.mock import Mock
from spacetraders_bot.operations.multileg_trader import (
    GreedyRoutePlanner,
    ProfitFirstStrategy,
    MultiLegRoute,
    RouteSegment,
    TradeAction,
)


class MockDB:
    """Mock database with proper waypoint coordinate lookup"""
    def __init__(self, waypoints):
        self.waypoints = waypoints
        self.current_waypoint = None

    def connection(self):
        return self

    def __enter__(self):
        return self

    def __exit__(self, *args):
        pass

    def cursor(self):
        return self

    def execute(self, query, params):
        """Store waypoint for fetchone"""
        self.current_waypoint = params[0]

    def fetchone(self):
        """Return coordinates for stored waypoint"""
        if self.current_waypoint and self.current_waypoint in self.waypoints:
            return self.waypoints[self.current_waypoint]
        return None


def test_cargo_overflow_goods_destined_for_different_waypoints():
    """
    CRITICAL BUG TEST: Cargo overflow when goods are destined for different waypoints

    Scenario:
    - At H51: Buy GOOD_A (sell at D41), GOOD_B (sell at J55) → 30 units total
    - At K91: Buy GOOD_C (sell at D41), GOOD_D (sell at J55), GOOD_E (sell at H48) → 30 + 55 = 85 units

    Bug: At K91, planner sees empty cargo (no sells possible at K91) and tries to buy 55 units.
    But cargo already has 30 units from H51! Result: 85 units exceeds 80 capacity.
    """

    mock_logger = Mock()

    waypoints = {
        'X1-TX46-H51': (0, 0),
        'X1-TX46-K91': (100, 0),
        'X1-TX46-D41': (200, 0),
        'X1-TX46-J55': (300, 0),
        'X1-TX46-H48': (400, 0),
    }
    mock_db = MockDB(waypoints)

    # Trade opportunities where goods are destined for DIFFERENT future waypoints
    # This prevents sells at intermediate stops → cargo accumulates!
    trade_opportunities = [
        # At H51: Buy GOOD_A (for D41), GOOD_B (for J55)
        {'buy_waypoint': 'X1-TX46-H51', 'sell_waypoint': 'X1-TX46-D41',
         'good': 'GOOD_A', 'buy_price': 100, 'sell_price': 300, 'spread': 200, 'trade_volume': 100},
        {'buy_waypoint': 'X1-TX46-H51', 'sell_waypoint': 'X1-TX46-J55',
         'good': 'GOOD_B', 'buy_price': 150, 'sell_price': 400, 'spread': 250, 'trade_volume': 100},

        # At K91: Buy GOOD_C (for D41), GOOD_D (for J55), GOOD_E (for H48)
        # NOTE: None of these can be sold at K91! Cargo will accumulate.
        {'buy_waypoint': 'X1-TX46-K91', 'sell_waypoint': 'X1-TX46-D41',
         'good': 'GOOD_C', 'buy_price': 120, 'sell_price': 350, 'spread': 230, 'trade_volume': 100},
        {'buy_waypoint': 'X1-TX46-K91', 'sell_waypoint': 'X1-TX46-J55',
         'good': 'GOOD_D', 'buy_price': 180, 'sell_price': 450, 'spread': 270, 'trade_volume': 100},
        {'buy_waypoint': 'X1-TX46-K91', 'sell_waypoint': 'X1-TX46-H48',
         'good': 'GOOD_E', 'buy_price': 200, 'sell_price': 500, 'spread': 300, 'trade_volume': 100},
    ]

    strategy = ProfitFirstStrategy(mock_logger)
    planner = GreedyRoutePlanner(mock_logger, mock_db, strategy=strategy)

    route = planner.find_route(
        start_waypoint='X1-TX46-H51',
        markets=['X1-TX46-H51', 'X1-TX46-K91', 'X1-TX46-D41', 'X1-TX46-J55', 'X1-TX46-H48'],
        trade_opportunities=trade_opportunities,
        max_stops=5,
        cargo_capacity=80,  # STARHOPPER-D capacity
        starting_credits=100000,
        ship_speed=10,
    )

    assert route is not None, "Route should be created"

    # Print route for debugging
    print(f"\n=== CARGO ACCUMULATION BUG TEST ===")
    for i, segment in enumerate(route.segments):
        print(f"\nSegment {i}: {segment.from_waypoint} → {segment.to_waypoint}")
        print(f"  Actions:")
        for action in segment.actions_at_destination:
            print(f"    {action.action} {action.units}x {action.good} @ {action.price_per_unit}")
        cargo_total = sum(segment.cargo_after.values())
        print(f"  Cargo after: {segment.cargo_after}")
        print(f"  Total cargo: {cargo_total}/80 units")

        # CRITICAL: This should FAIL before fix (cargo overflow bug)
        # This should PASS after fix (cargo properly tracked)
        assert cargo_total <= 80, (
            f"Segment {i} cargo overflow: {cargo_total} > 80\n"
            f"  This is the cargo accumulation bug!\n"
            f"  Route: {segment.from_waypoint} → {segment.to_waypoint}\n"
            f"  Cargo: {segment.cargo_after}\n"
            f"  Actions: {[(a.action, a.good, a.units) for a in segment.actions_at_destination]}"
        )


def test_cargo_overflow_starhopper_d_exact_scenario():
    """
    Exact STARHOPPER-D scenario from bug report

    The key insight: Goods bought at H51 and K91 are ALL destined for D41, J55, or H48.
    NONE are sold at K91! So when K91 evaluates buy opportunities, it sees "empty" cargo
    (after sells at K91) but actually cargo has 22 units from H51.
    """

    mock_logger = Mock()

    waypoints = {
        'X1-TX46-H51': (0, 0),
        'X1-TX46-K91': (100, 0),
        'X1-TX46-D41': (200, 0),
        'X1-TX46-J55': (300, 0),
        'X1-TX46-H48': (400, 0),
    }
    mock_db = MockDB(waypoints)

    # Exact scenario from bug report:
    # H51: Buy SHIP_PLATING (18u, for D41), ADVANCED_CIRCUITRY (4u, for J55)
    # K91: Buy ALUMINUM (20u, for D41), SHIP_PARTS (18u, for J55), MEDICINE (20u, for H48)
    # K91: Try to buy CLOTHING (20u, for H48) → OVERFLOW!
    trade_opportunities = [
        # From H51 (segment 1)
        {'buy_waypoint': 'X1-TX46-H51', 'sell_waypoint': 'X1-TX46-D41',
         'good': 'SHIP_PLATING', 'buy_price': 300, 'sell_price': 600, 'spread': 300, 'trade_volume': 100},
        {'buy_waypoint': 'X1-TX46-H51', 'sell_waypoint': 'X1-TX46-J55',
         'good': 'ADVANCED_CIRCUITRY', 'buy_price': 1000, 'sell_price': 2000, 'spread': 1000, 'trade_volume': 100},

        # From K91 (segment 2) - NONE sold at K91!
        {'buy_waypoint': 'X1-TX46-K91', 'sell_waypoint': 'X1-TX46-D41',
         'good': 'ALUMINUM', 'buy_price': 100, 'sell_price': 300, 'spread': 200, 'trade_volume': 100},
        {'buy_waypoint': 'X1-TX46-K91', 'sell_waypoint': 'X1-TX46-J55',
         'good': 'SHIP_PARTS', 'buy_price': 400, 'sell_price': 900, 'spread': 500, 'trade_volume': 100},
        {'buy_waypoint': 'X1-TX46-K91', 'sell_waypoint': 'X1-TX46-H48',
         'good': 'MEDICINE', 'buy_price': 200, 'sell_price': 500, 'spread': 300, 'trade_volume': 100},
        {'buy_waypoint': 'X1-TX46-K91', 'sell_waypoint': 'X1-TX46-H48',
         'good': 'CLOTHING', 'buy_price': 150, 'sell_price': 450, 'spread': 300, 'trade_volume': 100},
    ]

    strategy = ProfitFirstStrategy(mock_logger)
    planner = GreedyRoutePlanner(mock_logger, mock_db, strategy=strategy)

    route = planner.find_route(
        start_waypoint='X1-TX46-H51',
        markets=['X1-TX46-H51', 'X1-TX46-K91', 'X1-TX46-D41', 'X1-TX46-J55', 'X1-TX46-H48'],
        trade_opportunities=trade_opportunities,
        max_stops=5,
        cargo_capacity=80,
        starting_credits=100000,
        ship_speed=10,
    )

    assert route is not None, "Route should be created"

    # Print detailed route
    print(f"\n=== STARHOPPER-D EXACT REPRODUCTION ===")
    for i, segment in enumerate(route.segments):
        print(f"\nSegment {i}: {segment.from_waypoint} → {segment.to_waypoint}")
        print(f"  Actions at destination:")
        for action in segment.actions_at_destination:
            print(f"    {action.action} {action.units}x {action.good} @ {action.price_per_unit} (total: {action.total_value})")
        cargo_total = sum(segment.cargo_after.values())
        print(f"  Cargo after: {segment.cargo_after}")
        print(f"  Total cargo: {cargo_total}/80 units {'⚠️ AT LIMIT' if cargo_total == 80 else ''}")

        # This should catch the bug!
        assert cargo_total <= 80, (
            f"\n🚨 CARGO OVERFLOW DETECTED IN SEGMENT {i}!\n"
            f"   Route: {segment.from_waypoint} → {segment.to_waypoint}\n"
            f"   Cargo: {cargo_total}/80 units (EXCEEDS CAPACITY)\n"
            f"   Contents: {segment.cargo_after}\n"
            f"   Actions: {[(a.action, a.good, a.units) for a in segment.actions_at_destination]}\n"
            f"\n   This reproduces the STARHOPPER-D bug!"
        )


def test_cargo_capacity_with_incremental_buys():
    """
    Test that planner respects cargo capacity even when buying incrementally
    """

    mock_logger = Mock()

    waypoints = {
        'X1-TEST-A': (0, 0),
        'X1-TEST-B': (50, 0),
        'X1-TEST-C': (100, 0),
    }
    mock_db = MockDB(waypoints)

    # Simple incremental buying without sells
    trade_opportunities = [
        {'buy_waypoint': 'X1-TEST-A', 'sell_waypoint': 'X1-TEST-C',
         'good': 'GOOD_A', 'buy_price': 100, 'sell_price': 200, 'spread': 100, 'trade_volume': 100},
        {'buy_waypoint': 'X1-TEST-B', 'sell_waypoint': 'X1-TEST-C',
         'good': 'GOOD_B', 'buy_price': 150, 'sell_price': 250, 'spread': 100, 'trade_volume': 100},
    ]

    strategy = ProfitFirstStrategy(mock_logger)
    planner = GreedyRoutePlanner(mock_logger, mock_db, strategy=strategy)

    route = planner.find_route(
        start_waypoint='X1-TEST-A',
        markets=['X1-TEST-A', 'X1-TEST-B', 'X1-TEST-C'],
        trade_opportunities=trade_opportunities,
        max_stops=3,
        cargo_capacity=40,
        starting_credits=10000,
        ship_speed=10,
    )

    assert route is not None, "Route should be created"

    for i, segment in enumerate(route.segments):
        cargo_units = sum(segment.cargo_after.values())
        assert cargo_units <= 40, (
            f"Segment {i} exceeds 40-unit capacity with {cargo_units} units"
        )


if __name__ == "__main__":
    pytest.main([__file__, "-v"])
