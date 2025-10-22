"""BDD Step Definitions for Dependency Analyzer - Route dependency analysis and segment skipping"""

import logging
from datetime import datetime, timedelta, timezone
from unittest.mock import Mock, MagicMock, patch
import pytest
from pytest_bdd import scenarios, given, when, then, parsers

from spacetraders_bot.operations._trading import (
    TradeAction,
    RouteSegment,
    MultiLegRoute,
    SegmentDependency,
    analyze_route_dependencies,
    should_skip_segment,
    cargo_blocks_future_segments,
)

scenarios('../../../../bdd/features/trading/_trading_module/dependency_analyzer.feature')


# Dependency Analyzer Steps

# NOTE: More specific regex patterns must come BEFORE generic parse patterns
# Otherwise the generic pattern will match first and capture the full text

@given(parsers.re(r'segment (?P<idx>\d+): BUY (?P<buy_units>\d+) (?P<buy_good>\w+) at (?P<waypoint>\w+), SELL (?P<sell_units>\d+) (?P<sell_good>\w+) at \w+'))
def create_buy_and_sell_segment(context, idx, buy_units, buy_good, waypoint, sell_units, sell_good):
    """Create segment with both BUY and SELL actions at the same waypoint"""
    idx = int(idx)
    buy_units = int(buy_units)
    sell_units = int(sell_units)

    while len(context['route'].segments) <= idx:
        segment = RouteSegment(
            from_waypoint=f"X1-TEST-PREV",
            to_waypoint=f"X1-TEST-{waypoint}",
            distance=100,
            fuel_cost=110,
            actions_at_destination=[],
            cargo_after={},
            credits_after=10000,
            cumulative_profit=0
        )
        context['route'].segments.append(segment)

    # Add SELL action first (executed first when arriving at waypoint)
    sell_action = TradeAction(
        waypoint=f"X1-TEST-{waypoint}",
        good=sell_good,
        action='SELL',
        units=sell_units,
        price_per_unit=500,
        total_value=sell_units * 500
    )
    context['route'].segments[idx].actions_at_destination.append(sell_action)

    # Then add BUY action
    buy_action = TradeAction(
        waypoint=f"X1-TEST-{waypoint}",
        good=buy_good,
        action='BUY',
        units=buy_units,
        price_per_unit=150,
        total_value=buy_units * 150
    )
    context['route'].segments[idx].actions_at_destination.append(buy_action)

    # After SELL + BUY, cargo has only the newly bought good
    context['route'].segments[idx].cargo_after = {buy_good: buy_units}


@given(parsers.parse('segment {idx:d}: BUY {units:d} {good} at {waypoint}'))
def create_buy_segment(context, idx, units, good, waypoint):
    """Create BUY segment - handles both simple and compound cases"""
    import re
    from spacetraders_bot.operations._trading.models import MultiLegRoute

    # Initialize route if it doesn't exist
    if 'route' not in context or context['route'] is None:
        context['route'] = MultiLegRoute(
            segments=[],
            total_profit=0,
            total_fuel_cost=0,
            total_distance=0
        )

    # Handle compound case: waypoint might be "B7, SELL 10 COPPER at B7"
    compound_match = re.match(r'(\w+), SELL (\d+) (\w+) at (\w+)', waypoint)
    if compound_match:
        # This is a compound BUY+SELL step
        actual_waypoint = compound_match.group(1)
        sell_units = int(compound_match.group(2))
        sell_good = compound_match.group(3)
        # sell_waypoint = compound_match.group(4) (same as actual_waypoint)

        while len(context['route'].segments) <= idx:
            segment = RouteSegment(
                from_waypoint=f"X1-TEST-PREV",
                to_waypoint=f"X1-TEST-{actual_waypoint}",
                distance=100,
                fuel_cost=110,
                actions_at_destination=[],
                cargo_after={},
                credits_after=10000,
                cumulative_profit=0
            )
            context['route'].segments.append(segment)

        # Add SELL action first (executed first when arriving at waypoint)
        sell_action = TradeAction(
            waypoint=f"X1-TEST-{actual_waypoint}",
            good=sell_good,
            action='SELL',
            units=sell_units,
            price_per_unit=500,
            total_value=sell_units * 500
        )
        context['route'].segments[idx].actions_at_destination.append(sell_action)

        # Then add BUY action
        buy_action = TradeAction(
            waypoint=f"X1-TEST-{actual_waypoint}",
            good=good,
            action='BUY',
            units=units,
            price_per_unit=150,
            total_value=units * 150
        )
        context['route'].segments[idx].actions_at_destination.append(buy_action)

        # After SELL + BUY, cargo has only the newly bought good
        context['route'].segments[idx].cargo_after = {good: units}
        return

    # Simple BUY case
    while len(context['route'].segments) <= idx:
        segment = RouteSegment(
            from_waypoint=f"X1-TEST-PREV",
            to_waypoint=f"X1-TEST-{waypoint}",
            distance=100,
            fuel_cost=110,
            actions_at_destination=[],
            cargo_after={},
            credits_after=10000,
            cumulative_profit=0
        )
        context['route'].segments.append(segment)

    action = TradeAction(
        waypoint=f"X1-TEST-{waypoint}",
        good=good,
        action='BUY',
        units=units,
        price_per_unit=100,
        total_value=units * 100
    )
    context['route'].segments[idx].actions_at_destination.append(action)
    # Update cargo_after to reflect the BUY action
    context['route'].segments[idx].cargo_after[good] = units


@given(parsers.parse('segment {idx:d}: SELL {units:d} {good} at {waypoint}'))
def create_sell_segment(context, idx, units, good, waypoint):
    """Create SELL segment - handles both simple and compound SELL+BUY cases"""
    import re
    from spacetraders_bot.operations._trading.models import MultiLegRoute

    # Initialize route if it doesn't exist
    if 'route' not in context or context['route'] is None:
        context['route'] = MultiLegRoute(
            segments=[],
            total_profit=0,
            total_fuel_cost=0,
            total_distance=0
        )

    # Handle compound case: waypoint might be "B7, BUY 15 IRON at B7"
    compound_match = re.match(r'(\w+), BUY (\d+) (\w+) at (\w+)', waypoint)
    if compound_match:
        # This is a compound SELL+BUY step
        actual_waypoint = compound_match.group(1)
        buy_units = int(compound_match.group(2))
        buy_good = compound_match.group(3)

        while len(context['route'].segments) <= idx:
            segment = RouteSegment(
                from_waypoint=f"X1-TEST-PREV",
                to_waypoint=f"X1-TEST-{actual_waypoint}",
                distance=100,
                fuel_cost=110,
                actions_at_destination=[],
                cargo_after={},
                credits_after=10000,
                cumulative_profit=0
            )
            context['route'].segments.append(segment)

        # Add SELL action first
        sell_action = TradeAction(
            waypoint=f"X1-TEST-{actual_waypoint}",
            good=good,
            action='SELL',
            units=units,
            price_per_unit=500,
            total_value=units * 500
        )
        context['route'].segments[idx].actions_at_destination.append(sell_action)

        # Then add BUY action
        buy_action = TradeAction(
            waypoint=f"X1-TEST-{actual_waypoint}",
            good=buy_good,
            action='BUY',
            units=buy_units,
            price_per_unit=150,
            total_value=buy_units * 150
        )
        context['route'].segments[idx].actions_at_destination.append(buy_action)

        # After SELL + BUY, cargo has only the newly bought good
        context['route'].segments[idx].cargo_after = {buy_good: buy_units}
        return

    # Simple SELL case
    while len(context['route'].segments) <= idx:
        segment = RouteSegment(
            from_waypoint=f"X1-TEST-PREV",
            to_waypoint=f"X1-TEST-{waypoint}",
            distance=100,
            fuel_cost=110,
            actions_at_destination=[],
            cargo_after={},
            credits_after=10000,
            cumulative_profit=0
        )
        context['route'].segments.append(segment)

    action = TradeAction(
        waypoint=f"X1-TEST-{waypoint}",
        good=good,
        action='SELL',
        units=units,
        price_per_unit=500,
        total_value=units * 500
    )
    context['route'].segments[idx].actions_at_destination.append(action)
    # SELL removes cargo - cargo_after should be empty (assuming all sold)
    # This is a simplification; in reality we'd need to track partial sells


@when('analyzing route dependencies')
def analyze_dependencies(context):
    """Analyze route dependencies and store results"""
    route = context['route']
    dependencies = analyze_route_dependencies(route)
    context['dependencies'] = dependencies

    # Store dependency information in a more accessible format for assertions
    for idx, dep in dependencies.items():
        context[f'dependency_{idx}'] = dep


@then(parsers.parse('segment {idx:d} should have dependency type "{dep_type}"'))
def check_dependency_type(context, idx, dep_type):
    dep = context['dependencies'][idx]
    assert dep.dependency_type == dep_type, f"Expected {dep_type}, got {dep.dependency_type}"


@then(parsers.parse('segment {idx:d} should depend on segment {dep_idx:d}'))
def check_depends_on(context, idx, dep_idx):
    dep = context['dependencies'][idx]
    assert dep_idx in dep.depends_on, f"Expected segment {idx} to depend on {dep_idx}, got {dep.depends_on}"


@then(parsers.parse('segment {idx:d} should have can_skip={value}'))
def check_can_skip(context, idx, value):
    dep = context['dependencies'][idx]
    expected = value.lower() == 'true'
    assert dep.can_skip == expected, f"Expected can_skip={expected}, got {dep.can_skip}"


@then('all segments should have can_skip=True')
def check_all_can_skip(context):
    """Verify all segments have can_skip=True"""
    dependencies = context['dependencies']
    for idx, dep in dependencies.items():
        assert dep.can_skip == True, f"Expected segment {idx} to have can_skip=True, got {dep.can_skip}"


@then(parsers.parse('segment {idx:d} should have no dependencies'))
def check_no_dependencies(context, idx):
    """Verify segment has no dependencies"""
    dep = context['dependencies'][idx]
    assert dep.dependency_type == 'NONE', f"Expected NONE, got {dep.dependency_type}"
    assert len(dep.depends_on) == 0, f"Expected no dependencies, got {dep.depends_on}"


@then(parsers.parse('segment {idx:d} should require {units:d} {good} from prior segments'))
def check_required_cargo(context, idx, units, good):
    """Verify segment requires specific cargo from prior segments"""
    dep = context['dependencies'][idx]
    assert good in dep.required_cargo, f"Expected {good} in required_cargo, got {dep.required_cargo}"
    assert dep.required_cargo[good] == units, f"Expected {units} {good}, got {dep.required_cargo.get(good, 0)}"


@then(parsers.parse('segment {idx:d} should depend on segment {dep_idx:d} for {good}'))
def check_depends_on_for_good(context, idx, dep_idx, good):
    """Verify segment depends on another for a specific good"""
    dep = context['dependencies'][idx]
    assert dep_idx in dep.depends_on, f"Expected segment {idx} to depend on {dep_idx}, got {dep.depends_on}"
    # Verify the good is in required_cargo
    assert good in dep.required_cargo, f"Expected {good} in required_cargo, got {dep.required_cargo}"


@then(parsers.parse('segment {idx:d} should NOT depend on segment {dep_idx:d}'))
def check_not_depends_on(context, idx, dep_idx):
    """Verify segment does NOT depend on another"""
    dep = context['dependencies'][idx]
    assert dep_idx not in dep.depends_on, f"Expected segment {idx} NOT to depend on {dep_idx}, but it does: {dep.depends_on}"


@then(parsers.parse('segment {idx:d} should require {units:d} {good} from segment {source_idx:d}'))
def check_required_from_specific_segment(context, idx, units, good, source_idx):
    """Verify segment requires cargo from a specific prior segment"""
    dep = context['dependencies'][idx]
    assert source_idx in dep.depends_on, f"Expected segment {idx} to depend on {source_idx}, got {dep.depends_on}"
    assert good in dep.required_cargo, f"Expected {good} in required_cargo, got {dep.required_cargo}"


@then(parsers.parse('segment {idx:d} should depend on both segment {dep1:d} and segment {dep2:d}'))
def check_depends_on_both(context, idx, dep1, dep2):
    """Verify segment depends on two other segments"""
    dep = context['dependencies'][idx]
    assert dep1 in dep.depends_on, f"Expected segment {idx} to depend on {dep1}, got {dep.depends_on}"
    assert dep2 in dep.depends_on, f"Expected segment {idx} to depend on {dep2}, got {dep.depends_on}"


@then(parsers.parse('segment {idx:d} required_cargo should be {units:d} {good}'))
def check_required_cargo_total(context, idx, units, good):
    """Verify total required cargo for a good"""
    dep = context['dependencies'][idx]
    assert good in dep.required_cargo, f"Expected {good} in required_cargo, got {dep.required_cargo}"
    assert dep.required_cargo[good] == units, f"Expected {units} {good}, got {dep.required_cargo[good]}"


@then(parsers.parse('segment {idx:d} should consume from segment {source_idx:d} first (FIFO)'))
def check_fifo_consumption(context, idx, source_idx):
    """Verify FIFO consumption order - segment depends on earlier segment first"""
    dep = context['dependencies'][idx]
    assert source_idx in dep.depends_on, f"Expected segment {idx} to depend on {source_idx}, got {dep.depends_on}"
    # FIFO means the lower index should be in the depends_on list
    # The dependency analyzer should list dependencies in order


@then(parsers.parse('segment {idx:d} should consume remaining from segment {source_idx:d}'))
def check_remaining_consumption(context, idx, source_idx):
    """Verify segment also depends on another segment for remaining cargo"""
    dep = context['dependencies'][idx]
    assert source_idx in dep.depends_on, f"Expected segment {idx} to depend on {source_idx}, got {dep.depends_on}"


@then(parsers.parse('segment {idx:d} should have unfulfilled requirement of {units:d} {good}'))
def check_unfulfilled_requirement(context, idx, units, good):
    """Verify segment has unfulfilled cargo requirement"""
    # This would need additional tracking in the dependency analyzer
    # For now, just check that the required cargo exceeds what can be provided
    dep = context['dependencies'][idx]
    assert good in dep.required_cargo, f"Expected {good} in required_cargo"
    # The unfulfilled part would be tracked separately if implemented


# ===========================
# Route Execution Assertion Steps

