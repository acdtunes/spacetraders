"""
Test for route planning with residual cargo bug

CRITICAL BUG: Route planner assumes ship starts EMPTY but ships may have residual cargo

**Bug Description:**
STARHOPPER-D started cycle 1 with 20x ALUMINUM (residual from previous operation).
Route planner generated plan assuming EMPTY cargo:
  - buy 45x SHIP_PLATING + 20x ADVANCED_CIRCUITRY = 65 units

**Actual cargo needed:** 20 (residual) + 65 (planned) = 85 units → OVERFLOW (capacity = 80)

**Evidence from Logs:**
```
[INFO] Starting location: X1-TX46-A1
[INFO] Starting credits: 2,413,578
[INFO] Cargo after: {'SHIP_PLATING': 45, 'ADVANCED_CIRCUITRY': 20}  ← Planned (65 units)

// Later during execution:
[WARNING]   Salvaging: 45x SHIP_PLATING
[WARNING]   Salvaging: 14x ADVANCED_CIRCUITRY
[WARNING]   Salvaging: 20x ALUMINUM  ← NOT IN PLAN! Residual cargo!
```
Total when failed: 45 + 14 + 20 = 79 units (tried to add 2 more → 81 → overflow)

**Root Cause:**
`GreedyRoutePlanner.find_route()` line 863 initializes:
```python
current_cargo: Dict[str, int] = {}  # ASSUMES EMPTY!
```

But the ship may have existing cargo from previous operations! The planner MUST:
1. Accept `starting_cargo` parameter
2. Initialize `current_cargo` with actual ship cargo
3. Account for residual cargo when calculating available capacity
"""

import pytest
from unittest.mock import Mock
from spacetraders_bot.operations.multileg_trader import (
    GreedyRoutePlanner,
    ProfitFirstStrategy,
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


def test_route_planning_with_residual_cargo_basic():
    """
    CRITICAL BUG TEST: Route planner must account for residual cargo

    Scenario:
    - Ship has 20x ALUMINUM (residual from previous operation)
    - Planner generates route with 65 units planned purchases
    - Actual need: 20 (residual) + 65 (planned) = 85 units → OVERFLOW (capacity 80)

    Expected behavior:
    - Planner sees starting_cargo={'ALUMINUM': 20}
    - Available capacity = 80 - 20 = 60 units
    - Plans route that never exceeds 80 units total
    - Can sell ALUMINUM at B7 to free up space
    """

    mock_logger = Mock()

    waypoints = {
        'X1-TX46-A1': (0, 0),
        'X1-TX46-B7': (100, 0),
        'X1-TX46-C5': (200, 0),
    }
    mock_db = MockDB(waypoints)

    # Ship starts with 20x ALUMINUM residual
    starting_cargo = {'ALUMINUM': 20}

    # Trade opportunities including sell for ALUMINUM to free space
    trade_opportunities = [
        # Can sell residual ALUMINUM at B7
        {'buy_waypoint': 'X1-TX46-A1', 'sell_waypoint': 'X1-TX46-B7',
         'good': 'ALUMINUM', 'buy_price': 100, 'sell_price': 150,
         'spread': 50, 'trade_volume': 100},
        # Then buy profitable goods
        {'buy_waypoint': 'X1-TX46-B7', 'sell_waypoint': 'X1-TX46-C5',
         'good': 'SHIP_PLATING', 'buy_price': 300, 'sell_price': 600,
         'spread': 300, 'trade_volume': 100},
        {'buy_waypoint': 'X1-TX46-B7', 'sell_waypoint': 'X1-TX46-C5',
         'good': 'ADVANCED_CIRCUITRY', 'buy_price': 1000, 'sell_price': 2000,
         'spread': 1000, 'trade_volume': 100},
    ]

    strategy = ProfitFirstStrategy(mock_logger)
    planner = GreedyRoutePlanner(mock_logger, mock_db, strategy=strategy)

    # Call with starting_cargo (this parameter doesn't exist yet - should be added!)
    route = planner.find_route(
        start_waypoint='X1-TX46-A1',
        markets=['X1-TX46-A1', 'X1-TX46-B7', 'X1-TX46-C5'],
        trade_opportunities=trade_opportunities,
        max_stops=3,
        cargo_capacity=80,  # STARHOPPER-D capacity
        starting_credits=100000,
        ship_speed=10,
        starting_cargo=starting_cargo,  # NEW PARAMETER!
    )

    assert route is not None, "Route should be created (can sell ALUMINUM then buy new goods)"

    # Print route for debugging
    print(f"\n=== RESIDUAL CARGO BUG TEST ===")
    print(f"Starting cargo: {starting_cargo} (20 units)")
    print(f"Capacity: 80 units")
    print(f"Available for planning: 60 units\n")

    for i, segment in enumerate(route.segments):
        print(f"\nSegment {i}: {segment.from_waypoint} → {segment.to_waypoint}")
        print(f"  Actions:")
        for action in segment.actions_at_destination:
            print(f"    {action.action} {action.units}x {action.good} @ {action.price_per_unit}")
        cargo_total = sum(segment.cargo_after.values())
        print(f"  Cargo after: {segment.cargo_after}")
        print(f"  Total cargo: {cargo_total}/80 units")

        # CRITICAL: This should PASS with fix (cargo properly includes residual)
        assert cargo_total <= 80, (
            f"\n🚨 CARGO OVERFLOW WITH RESIDUAL!\n"
            f"   Segment {i}: {segment.from_waypoint} → {segment.to_waypoint}\n"
            f"   Starting cargo: {starting_cargo} (20 units)\n"
            f"   Planned purchases: {sum(a.units for a in segment.actions_at_destination if a.action == 'BUY')} units\n"
            f"   Total after segment: {cargo_total}/80 units\n"
            f"   EXCEEDS CAPACITY!\n"
            f"\n   This is the residual cargo bug - planner assumed empty ship!"
        )


def test_route_planning_starhopper_d_exact_scenario():
    """
    Exact STARHOPPER-D scenario from production logs

    Ship state at cycle start:
    - Location: X1-TX46-A1
    - Cargo: 20x ALUMINUM (residual)
    - Capacity: 80 units
    - Available: 60 units

    Route planner generated (assuming empty):
    - Segment 1: Buy 45x SHIP_PLATING + 20x ADVANCED_CIRCUITRY = 65 units

    Actual execution:
    - Tried to load 20 + 65 = 85 units → OVERFLOW!
    - Had to salvage 20x ALUMINUM to make room
    """

    mock_logger = Mock()

    waypoints = {
        'X1-TX46-A1': (0, 0),
        'X1-TX46-I52': (100, 0),
        'X1-TX46-J55': (200, 0),
        'X1-TX46-H48': (300, 0),
    }
    mock_db = MockDB(waypoints)

    # EXACT STARHOPPER-D state
    starting_cargo = {'ALUMINUM': 20}

    # Trade opportunities from production logs
    trade_opportunities = [
        {'buy_waypoint': 'X1-TX46-A1', 'sell_waypoint': 'X1-TX46-I52',
         'good': 'SHIP_PLATING', 'buy_price': 300, 'sell_price': 600,
         'spread': 300, 'trade_volume': 100},
        {'buy_waypoint': 'X1-TX46-A1', 'sell_waypoint': 'X1-TX46-J55',
         'good': 'ADVANCED_CIRCUITRY', 'buy_price': 1000, 'sell_price': 2000,
         'spread': 1000, 'trade_volume': 100},
        {'buy_waypoint': 'X1-TX46-I52', 'sell_waypoint': 'X1-TX46-H48',
         'good': 'MEDICINE', 'buy_price': 200, 'sell_price': 500,
         'spread': 300, 'trade_volume': 100},
    ]

    strategy = ProfitFirstStrategy(mock_logger)
    planner = GreedyRoutePlanner(mock_logger, mock_db, strategy=strategy)

    route = planner.find_route(
        start_waypoint='X1-TX46-A1',
        markets=['X1-TX46-A1', 'X1-TX46-I52', 'X1-TX46-J55', 'X1-TX46-H48'],
        trade_opportunities=trade_opportunities,
        max_stops=4,
        cargo_capacity=80,
        starting_credits=2413578,  # Exact from logs
        ship_speed=10,
        starting_cargo=starting_cargo,  # Pass residual cargo!
    )

    assert route is not None, "Route should be created"

    print(f"\n=== STARHOPPER-D EXACT REPRODUCTION ===")
    print(f"Starting state:")
    print(f"  Location: X1-TX46-A1")
    print(f"  Cargo: {starting_cargo} (20 units residual)")
    print(f"  Capacity: 80 units")
    print(f"  Available: 60 units")
    print(f"  Credits: 2,413,578\n")

    for i, segment in enumerate(route.segments):
        print(f"\nSegment {i}: {segment.from_waypoint} → {segment.to_waypoint}")
        print(f"  Actions at destination:")
        for action in segment.actions_at_destination:
            print(f"    {action.action} {action.units}x {action.good} @ {action.price_per_unit}")
        cargo_total = sum(segment.cargo_after.values())
        print(f"  Cargo after: {segment.cargo_after}")
        print(f"  Total cargo: {cargo_total}/80 units")

        # With fix, this should NEVER overflow
        assert cargo_total <= 80, (
            f"\n🚨 STARHOPPER-D BUG REPRODUCED!\n"
            f"   Segment {i}: {segment.from_waypoint} → {segment.to_waypoint}\n"
            f"   Started with: {starting_cargo} (20 units)\n"
            f"   Route planned: {sum(a.units for a in segment.actions_at_destination if a.action == 'BUY')} units BUY\n"
            f"   Final cargo: {cargo_total}/80 units (OVERFLOW!)\n"
            f"\n   Planner didn't account for residual ALUMINUM!"
        )


def test_route_planning_empty_cargo_still_works():
    """
    Ensure fix doesn't break normal case where ship is actually empty
    """

    mock_logger = Mock()

    waypoints = {
        'X1-TEST-A': (0, 0),
        'X1-TEST-B': (100, 0),
        'X1-TEST-C': (200, 0),
    }
    mock_db = MockDB(waypoints)

    # Ship is actually empty (normal case)
    starting_cargo = {}

    trade_opportunities = [
        {'buy_waypoint': 'X1-TEST-A', 'sell_waypoint': 'X1-TEST-B',
         'good': 'GOOD_A', 'buy_price': 100, 'sell_price': 200,
         'spread': 100, 'trade_volume': 100},
        {'buy_waypoint': 'X1-TEST-B', 'sell_waypoint': 'X1-TEST-C',
         'good': 'GOOD_B', 'buy_price': 150, 'sell_price': 300,
         'spread': 150, 'trade_volume': 100},
    ]

    strategy = ProfitFirstStrategy(mock_logger)
    planner = GreedyRoutePlanner(mock_logger, mock_db, strategy=strategy)

    route = planner.find_route(
        start_waypoint='X1-TEST-A',
        markets=['X1-TEST-A', 'X1-TEST-B', 'X1-TEST-C'],
        trade_opportunities=trade_opportunities,
        max_stops=3,
        cargo_capacity=80,
        starting_credits=100000,
        ship_speed=10,
        starting_cargo=starting_cargo,  # Empty dict - normal case
    )

    assert route is not None, "Route should work with empty starting cargo"

    # Should still respect capacity
    for segment in route.segments:
        cargo_total = sum(segment.cargo_after.values())
        assert cargo_total <= 80, f"Cargo overflow even with empty start: {cargo_total}/80"


def test_route_planning_without_starting_cargo_defaults_empty():
    """
    Ensure backward compatibility - if starting_cargo not provided, defaults to empty
    """

    mock_logger = Mock()

    waypoints = {
        'X1-TEST-A': (0, 0),
        'X1-TEST-B': (100, 0),
        'X1-TEST-C': (200, 0),
    }
    mock_db = MockDB(waypoints)

    trade_opportunities = [
        {'buy_waypoint': 'X1-TEST-A', 'sell_waypoint': 'X1-TEST-B',
         'good': 'GOOD_A', 'buy_price': 100, 'sell_price': 200,
         'spread': 100, 'trade_volume': 100},
        {'buy_waypoint': 'X1-TEST-B', 'sell_waypoint': 'X1-TEST-C',
         'good': 'GOOD_B', 'buy_price': 150, 'sell_price': 300,
         'spread': 150, 'trade_volume': 100},
    ]

    strategy = ProfitFirstStrategy(mock_logger)
    planner = GreedyRoutePlanner(mock_logger, mock_db, strategy=strategy)

    # Don't pass starting_cargo at all - should default to {}
    route = planner.find_route(
        start_waypoint='X1-TEST-A',
        markets=['X1-TEST-A', 'X1-TEST-B', 'X1-TEST-C'],
        trade_opportunities=trade_opportunities,
        max_stops=3,
        cargo_capacity=80,
        starting_credits=100000,
        ship_speed=10,
        # starting_cargo NOT PROVIDED - should default to empty
    )

    assert route is not None, "Route should work without starting_cargo parameter (backward compat)"


if __name__ == "__main__":
    pytest.main([__file__, "-v", "-s"])
