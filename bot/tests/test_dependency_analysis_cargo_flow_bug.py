"""
Test for cargo flow tracking bug in circuit breaker smart skip system

BUG DESCRIPTION:
The analyze_route_dependencies() function incorrectly marks segments as INDEPENDENT
when they depend on cargo carried through from earlier segments.

PRODUCTION EVIDENCE:
Route:
  Segment 0: BUY 18 SHIP_PARTS, 22 MEDICINE
  Segment 1: SELL 20 MEDICINE, BUY 20 DRUGS (cargo after: 18 SHIP_PARTS, 2 MEDICINE, 20 DRUGS)
  Segment 2: SELL 6 SHIP_PARTS, BUY 6 SHIP_PARTS (cargo after: 18 SHIP_PARTS, 2 MEDICINE, 20 DRUGS)
  Segment 3: SELL 6 SHIP_PARTS, BUY 6 SHIP_PARTS (cargo after: 18 SHIP_PARTS, 2 MEDICINE, 20 DRUGS)

Dependency Analysis Output (BUGGY):
  Segment 0: INDEPENDENT ✅
  Segment 1: CARGO (depends on [0]) ✅
  Segment 2: CARGO (depends on [0]) ✅
  Segment 3: INDEPENDENT ❌ WRONG! Depends on Segment 0 SHIP_PARTS!

Result:
  - Circuit breaker triggered in Segment 0 (buy price spike)
  - Smart skip salvaged cargo, skipped segments 1-2
  - Jumped to Segment 3 (marked INDEPENDENT)
  - Tried to sell 6 SHIP_PARTS → ERROR: Ship has 0 SHIP_PARTS
  - Operation failed

ROOT CAUSE:
Current logic only tracks the MOST RECENT segment that bought each good.
It doesn't track carry-through cargo that persists across multiple segments.

Segment 2 buys and sells 6 SHIP_PARTS (net 0 change), so cargo_providers[SHIP_PARTS]
gets updated from 0 to 2. Then Segment 3 tries to sell 6 SHIP_PARTS, sees
cargo_providers[SHIP_PARTS]=2, and marks itself as depending on Segment 2.

But Segment 2's net contribution is 0! The actual source is Segment 0.

EXPECTED BEHAVIOR:
Segment 3 should be marked as depending on Segment 0 (the original source).
When Segment 0 fails, smart skip should abort (not jump to Segment 3).
"""

import pytest
from spacetraders_bot.operations.multileg_trader import (
    RouteSegment,
    TradeAction,
    MultiLegRoute,
    analyze_route_dependencies,
    should_skip_segment,
)


def regression_cargo_flow_tracking_with_net_zero_segment():
    """
    Test cargo flow tracking when middle segments have net-zero cargo changes

    This is the exact scenario from production:
    - Segment 0: BUY 18 SHIP_PARTS, 22 MEDICINE (source)
    - Segment 1: SELL 20 MEDICINE, BUY 20 DRUGS (uses Segment 0 MEDICINE)
    - Segment 2: SELL 6 SHIP_PARTS, BUY 6 SHIP_PARTS (net 0, but updates provider)
    - Segment 3: SELL 6 SHIP_PARTS (should depend on Segment 0, NOT Segment 2)
    """

    route = MultiLegRoute(
        segments=[
            # Segment 0: Initial purchase
            RouteSegment(
                from_waypoint='X1-TEST-A1',
                to_waypoint='X1-TEST-B2',
                distance=50,
                fuel_cost=55,
                actions_at_destination=[
                    TradeAction('X1-TEST-B2', 'SHIP_PARTS', 'BUY', 18, 800, 14400),
                    TradeAction('X1-TEST-B2', 'MEDICINE', 'BUY', 22, 500, 11000),
                ],
                cargo_after={'SHIP_PARTS': 18, 'MEDICINE': 22},
                credits_after=100000 - 14400 - 11000,
                cumulative_profit=0
            ),
            # Segment 1: Sell MEDICINE, buy DRUGS
            RouteSegment(
                from_waypoint='X1-TEST-B2',
                to_waypoint='X1-TEST-C3',
                distance=60,
                fuel_cost=66,
                actions_at_destination=[
                    TradeAction('X1-TEST-C3', 'MEDICINE', 'SELL', 20, 750, 15000),
                    TradeAction('X1-TEST-C3', 'DRUGS', 'BUY', 20, 600, 12000),
                ],
                cargo_after={'SHIP_PARTS': 18, 'MEDICINE': 2, 'DRUGS': 20},
                credits_after=100000 - 14400 - 11000 + 15000 - 12000,
                cumulative_profit=3000
            ),
            # Segment 2: Net-zero SHIP_PARTS trade (sell 6, buy 6)
            RouteSegment(
                from_waypoint='X1-TEST-C3',
                to_waypoint='X1-TEST-D4',
                distance=70,
                fuel_cost=77,
                actions_at_destination=[
                    TradeAction('X1-TEST-D4', 'SHIP_PARTS', 'SELL', 6, 1200, 7200),
                    TradeAction('X1-TEST-D4', 'SHIP_PARTS', 'BUY', 6, 900, 5400),
                ],
                cargo_after={'SHIP_PARTS': 18, 'MEDICINE': 2, 'DRUGS': 20},
                credits_after=100000 - 14400 - 11000 + 15000 - 12000 + 7200 - 5400,
                cumulative_profit=4800
            ),
            # Segment 3: Sell SHIP_PARTS (depends on Segment 0, NOT Segment 2)
            RouteSegment(
                from_waypoint='X1-TEST-D4',
                to_waypoint='X1-TEST-E5',
                distance=80,
                fuel_cost=88,
                actions_at_destination=[
                    TradeAction('X1-TEST-E5', 'SHIP_PARTS', 'SELL', 6, 1300, 7800),
                ],
                cargo_after={'SHIP_PARTS': 12, 'MEDICINE': 2, 'DRUGS': 20},
                credits_after=100000 - 14400 - 11000 + 15000 - 12000 + 7200 - 5400 + 7800,
                cumulative_profit=12600
            ),
        ],
        total_profit=12600,
        total_distance=260,
        total_fuel_cost=286,
        estimated_time_minutes=180
    )

    # Analyze dependencies
    dependencies = analyze_route_dependencies(route)

    # Print dependency analysis for debugging
    print("\n" + "="*70)
    print("DEPENDENCY ANALYSIS")
    print("="*70)
    for idx, dep in dependencies.items():
        dep_type = dep.dependency_type if dep.dependency_type != 'NONE' else 'INDEPENDENT'
        print(f"Segment {idx}: {dep_type}")
        print(f"  depends_on: {dep.depends_on}")
        print(f"  required_cargo: {dep.required_cargo}")
        print(f"  can_skip: {dep.can_skip}")
    print("="*70)

    # CRITICAL ASSERTION: Segment 3 should depend on Segment 0 (original source)
    # Current buggy behavior: depends_on=[2] (the net-zero segment)
    # Expected correct behavior: depends_on=[0] (the original source)

    segment_3_dep = dependencies[3]

    # BUG REPRODUCTION: This assertion will FAIL with current implementation
    assert 0 in segment_3_dep.depends_on, \
        f"Segment 3 should depend on Segment 0 (original SHIP_PARTS source), got depends_on={segment_3_dep.depends_on}"

    # Additional check: Segment 3 should NOT be independent
    assert segment_3_dep.dependency_type == 'CARGO', \
        f"Segment 3 should have CARGO dependency, got {segment_3_dep.dependency_type}"

    assert not segment_3_dep.can_skip, \
        "Segment 3 should NOT be skippable (cargo-dependent)"

    # Verify required cargo is tracked
    assert 'SHIP_PARTS' in segment_3_dep.required_cargo, \
        "Segment 3 should require SHIP_PARTS"

    assert segment_3_dep.required_cargo['SHIP_PARTS'] == 6, \
        f"Segment 3 should require 6 SHIP_PARTS, got {segment_3_dep.required_cargo['SHIP_PARTS']}"


def regression_should_skip_segment_abort_when_source_fails():
    """
    Test that should_skip_segment() correctly aborts when source segment fails

    When Segment 0 fails (circuit breaker), Segment 3 should be marked as affected
    (transitive dependency) and operation should abort (not skip to Segment 3).
    """

    # Same route as above
    route = MultiLegRoute(
        segments=[
            # Segment 0: Initial purchase (THIS WILL FAIL)
            RouteSegment(
                from_waypoint='X1-TEST-A1',
                to_waypoint='X1-TEST-B2',
                distance=50,
                fuel_cost=55,
                actions_at_destination=[
                    TradeAction('X1-TEST-B2', 'SHIP_PARTS', 'BUY', 18, 800, 14400),
                    TradeAction('X1-TEST-B2', 'MEDICINE', 'BUY', 22, 500, 11000),
                ],
                cargo_after={'SHIP_PARTS': 18, 'MEDICINE': 22},
                credits_after=100000 - 14400 - 11000,
                cumulative_profit=0
            ),
            # Segment 1: Sell MEDICINE, buy DRUGS
            RouteSegment(
                from_waypoint='X1-TEST-B2',
                to_waypoint='X1-TEST-C3',
                distance=60,
                fuel_cost=66,
                actions_at_destination=[
                    TradeAction('X1-TEST-C3', 'MEDICINE', 'SELL', 20, 750, 15000),
                    TradeAction('X1-TEST-C3', 'DRUGS', 'BUY', 20, 600, 12000),
                ],
                cargo_after={'SHIP_PARTS': 18, 'MEDICINE': 2, 'DRUGS': 20},
                credits_after=100000 - 14400 - 11000 + 15000 - 12000,
                cumulative_profit=3000
            ),
            # Segment 2: Net-zero SHIP_PARTS trade
            RouteSegment(
                from_waypoint='X1-TEST-C3',
                to_waypoint='X1-TEST-D4',
                distance=70,
                fuel_cost=77,
                actions_at_destination=[
                    TradeAction('X1-TEST-D4', 'SHIP_PARTS', 'SELL', 6, 1200, 7200),
                    TradeAction('X1-TEST-D4', 'SHIP_PARTS', 'BUY', 6, 900, 5400),
                ],
                cargo_after={'SHIP_PARTS': 18, 'MEDICINE': 2, 'DRUGS': 20},
                credits_after=100000 - 14400 - 11000 + 15000 - 12000 + 7200 - 5400,
                cumulative_profit=4800
            ),
            # Segment 3: Sell SHIP_PARTS (depends on Segment 0)
            RouteSegment(
                from_waypoint='X1-TEST-D4',
                to_waypoint='X1-TEST-E5',
                distance=80,
                fuel_cost=88,
                actions_at_destination=[
                    TradeAction('X1-TEST-E5', 'SHIP_PARTS', 'SELL', 6, 1300, 7800),
                ],
                cargo_after={'SHIP_PARTS': 12, 'MEDICINE': 2, 'DRUGS': 20},
                credits_after=100000 - 14400 - 11000 + 15000 - 12000 + 7200 - 5400 + 7800,
                cumulative_profit=12600
            ),
        ],
        total_profit=12600,
        total_distance=260,
        total_fuel_cost=286,
        estimated_time_minutes=180
    )

    # Analyze dependencies
    dependencies = analyze_route_dependencies(route)

    # Simulate circuit breaker failure at Segment 0
    current_cargo = {}  # Segment 0 failed before buying cargo
    current_credits = 100000

    should_skip, reason = should_skip_segment(
        segment_index=0,
        failure_reason="BUY price spike",
        dependencies=dependencies,
        route=route,
        current_cargo=current_cargo,
        current_credits=current_credits
    )

    # CRITICAL ASSERTION: Should NOT skip (all segments depend on Segment 0)
    # Current buggy behavior: should_skip=True (thinks Segment 3 is independent)
    # Expected correct behavior: should_skip=False (all segments depend on Segment 0)

    print(f"\nShould skip Segment 0? {should_skip}")
    print(f"Reason: {reason}")

    assert not should_skip, \
        f"Should NOT skip Segment 0 failure (all segments depend on it), but got should_skip={should_skip}, reason={reason}"

    assert "All remaining segments depend on failed segment" in reason, \
        f"Expected abort reason, got: {reason}"


def regression_cargo_flow_tracking_with_partial_sells():
    """
    Test cargo flow tracking when segments partially sell cargo

    Scenario:
    - Segment 0: BUY 30 IRON
    - Segment 1: SELL 10 IRON (20 remaining, still provided by Segment 0)
    - Segment 2: SELL 10 IRON (10 remaining, still provided by Segment 0)
    - Segment 3: SELL 10 IRON (0 remaining, still provided by Segment 0)

    All three SELL segments should depend on Segment 0.
    """

    route = MultiLegRoute(
        segments=[
            # Segment 0: Buy 30 IRON
            RouteSegment(
                from_waypoint='X1-TEST-A1',
                to_waypoint='X1-TEST-B2',
                distance=50,
                fuel_cost=55,
                actions_at_destination=[
                    TradeAction('X1-TEST-B2', 'IRON', 'BUY', 30, 500, 15000),
                ],
                cargo_after={'IRON': 30},
                credits_after=100000 - 15000,
                cumulative_profit=0
            ),
            # Segment 1: Sell 10 IRON
            RouteSegment(
                from_waypoint='X1-TEST-B2',
                to_waypoint='X1-TEST-C3',
                distance=60,
                fuel_cost=66,
                actions_at_destination=[
                    TradeAction('X1-TEST-C3', 'IRON', 'SELL', 10, 800, 8000),
                ],
                cargo_after={'IRON': 20},
                credits_after=100000 - 15000 + 8000,
                cumulative_profit=3000
            ),
            # Segment 2: Sell 10 IRON
            RouteSegment(
                from_waypoint='X1-TEST-C3',
                to_waypoint='X1-TEST-D4',
                distance=70,
                fuel_cost=77,
                actions_at_destination=[
                    TradeAction('X1-TEST-D4', 'IRON', 'SELL', 10, 850, 8500),
                ],
                cargo_after={'IRON': 10},
                credits_after=100000 - 15000 + 8000 + 8500,
                cumulative_profit=6500
            ),
            # Segment 3: Sell final 10 IRON
            RouteSegment(
                from_waypoint='X1-TEST-D4',
                to_waypoint='X1-TEST-E5',
                distance=80,
                fuel_cost=88,
                actions_at_destination=[
                    TradeAction('X1-TEST-E5', 'IRON', 'SELL', 10, 900, 9000),
                ],
                cargo_after={'IRON': 0},
                credits_after=100000 - 15000 + 8000 + 8500 + 9000,
                cumulative_profit=10500
            ),
        ],
        total_profit=10500,
        total_distance=260,
        total_fuel_cost=286,
        estimated_time_minutes=180
    )

    dependencies = analyze_route_dependencies(route)

    # All three SELL segments should depend on Segment 0
    assert 0 in dependencies[1].depends_on, "Segment 1 should depend on Segment 0"
    assert 0 in dependencies[2].depends_on, "Segment 2 should depend on Segment 0"
    assert 0 in dependencies[3].depends_on, "Segment 3 should depend on Segment 0"

    # All should be CARGO type
    assert dependencies[1].dependency_type == 'CARGO', "Segment 1 should have CARGO dependency"
    assert dependencies[2].dependency_type == 'CARGO', "Segment 2 should have CARGO dependency"
    assert dependencies[3].dependency_type == 'CARGO', "Segment 3 should have CARGO dependency"


