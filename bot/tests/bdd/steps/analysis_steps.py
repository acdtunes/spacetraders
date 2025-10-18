"""Step definitions for dependency analysis scenarios."""

import pytest
from pytest_bdd import given, when, then, parsers, scenarios

from spacetraders_bot.operations.multileg_trader import (
    RouteSegment,
    TradeAction,
    MultiLegRoute,
    analyze_route_dependencies,
    should_skip_segment,
)

# Load all analysis scenarios
scenarios('../../features/analysis/dependency_analysis.feature')


@pytest.fixture
def analysis_context():
    """Shared context for dependency analysis scenarios."""
    return {
        'route': None,
        'dependencies': None,
        'skip_result': None,
        'skip_reason': None,
    }


@given("the dependency analysis system")
def setup_dependency_analysis(analysis_context):
    """Initialize dependency analysis system."""
    # System is stateless, nothing to initialize
    pass


@given(parsers.parse("a {segment_count:d}-segment trade route:\n{route_table}"))
def setup_trade_route(analysis_context, segment_count, route_table):
    """Create a multi-leg trade route from a table specification."""
    # Parse the route table
    lines = [line.strip() for line in route_table.strip().split('\n') if line.strip()]

    # Skip header and separator
    data_lines = [line for line in lines if not line.startswith('|') or '---' not in line][1:]

    segments = []

    for line in data_lines:
        # Parse table row: | segment | from | to | actions | cargo_after |
        parts = [p.strip() for p in line.split('|') if p.strip()]

        if len(parts) < 5:
            continue

        segment_idx = int(parts[0])
        from_wp = parts[1]
        to_wp = parts[2]
        actions_str = parts[3]
        cargo_after_str = parts[4]

        # Parse actions (e.g., "BUY 18 SHIP_PARTS, BUY 22 MEDICINE")
        actions = []
        for action_part in actions_str.split(','):
            action_part = action_part.strip()
            if not action_part:
                continue

            action_tokens = action_part.split()
            action_type = action_tokens[0]  # BUY or SELL
            quantity = int(action_tokens[1])
            good = '_'.join(action_tokens[2:])  # Handle multi-word goods like SHIP_PARTS

            # Create TradeAction with dummy prices
            if action_type == 'BUY':
                price = 500
                total = quantity * price
                actions.append(TradeAction(to_wp, good, 'BUY', quantity, price, total))
            elif action_type == 'SELL':
                price = 800
                total = quantity * price
                actions.append(TradeAction(to_wp, good, 'SELL', quantity, price, total))

        # Parse cargo_after (e.g., "SHIP_PARTS:18, MEDICINE:22")
        cargo_after = {}
        for cargo_part in cargo_after_str.split(','):
            cargo_part = cargo_part.strip()
            if not cargo_part or ':' not in cargo_part:
                continue
            good, qty = cargo_part.split(':')
            cargo_after[good.strip()] = int(qty.strip())

        # Create segment
        segment = RouteSegment(
            from_waypoint=from_wp,
            to_waypoint=to_wp,
            distance=50 + segment_idx * 10,
            fuel_cost=55 + segment_idx * 11,
            actions_at_destination=actions,
            cargo_after=cargo_after,
            credits_after=100000 - segment_idx * 1000,  # Dummy credits
            cumulative_profit=segment_idx * 3000
        )
        segments.append(segment)

    # Create route
    analysis_context['route'] = MultiLegRoute(
        segments=segments,
        total_profit=segment_count * 3000,
        total_distance=sum(s.distance for s in segments),
        total_fuel_cost=sum(s.fuel_cost for s in segments),
        estimated_time_minutes=180
    )


@when("I analyze route dependencies")
def analyze_dependencies(analysis_context):
    """Analyze dependencies for the route."""
    route = analysis_context['route']
    analysis_context['dependencies'] = analyze_route_dependencies(route)


@when(parsers.parse('segment {segment_idx:d} fails due to "{failure_reason}"'))
def simulate_segment_failure(analysis_context, segment_idx, failure_reason):
    """Simulate a segment failure."""
    analysis_context['failed_segment'] = segment_idx
    analysis_context['failure_reason'] = failure_reason


@when(parsers.parse("I check if segment {segment_idx:d} can be skipped"))
def check_skip_segment(analysis_context, segment_idx):
    """Check if a failed segment can be skipped."""
    dependencies = analysis_context['dependencies']
    route = analysis_context['route']
    failure_reason = analysis_context.get('failure_reason', 'Unknown failure')

    # Simulate empty cargo (segment failed before completion)
    current_cargo = {}
    current_credits = 100000

    should_skip, reason = should_skip_segment(
        segment_index=segment_idx,
        failure_reason=failure_reason,
        dependencies=dependencies,
        route=route,
        current_cargo=current_cargo,
        current_credits=current_credits
    )

    analysis_context['skip_result'] = should_skip
    analysis_context['skip_reason'] = reason


@then(parsers.parse("segment {segment_idx:d} should be marked as INDEPENDENT"))
def verify_independent(analysis_context, segment_idx):
    """Verify segment is marked as independent."""
    dependencies = analysis_context['dependencies']
    dep = dependencies[segment_idx]

    assert dep.dependency_type == 'NONE' or dep.dependency_type == 'INDEPENDENT', \
        f"Segment {segment_idx} should be INDEPENDENT, got {dep.dependency_type}"

    assert dep.can_skip, \
        f"Segment {segment_idx} should be skippable (independent)"


@then(parsers.parse('segment {dependent_idx:d} should depend on segment {source_idx:d} for {good} cargo'))
def verify_cargo_dependency(analysis_context, dependent_idx, source_idx, good):
    """Verify segment has cargo dependency on another segment."""
    dependencies = analysis_context['dependencies']
    dep = dependencies[dependent_idx]

    assert source_idx in dep.depends_on, \
        f"Segment {dependent_idx} should depend on segment {source_idx}, got depends_on={dep.depends_on}"

    assert dep.dependency_type == 'CARGO', \
        f"Segment {dependent_idx} should have CARGO dependency, got {dep.dependency_type}"

    assert good in dep.required_cargo, \
        f"Segment {dependent_idx} should require {good}, got required_cargo={dep.required_cargo}"


@then(parsers.parse("segment {dependent_idx:d} should NOT depend on segment {other_idx:d}"))
def verify_no_dependency(analysis_context, dependent_idx, other_idx):
    """Verify segment does NOT depend on another segment."""
    dependencies = analysis_context['dependencies']
    dep = dependencies[dependent_idx]

    assert other_idx not in dep.depends_on, \
        f"Segment {dependent_idx} should NOT depend on segment {other_idx}, but depends_on={dep.depends_on}"


@then(parsers.parse("segment {segment_idx:d} should require {quantity:d} {good}"))
def verify_required_cargo(analysis_context, segment_idx, quantity, good):
    """Verify segment requires specific cargo quantity."""
    dependencies = analysis_context['dependencies']
    dep = dependencies[segment_idx]

    assert good in dep.required_cargo, \
        f"Segment {segment_idx} should require {good}, got required_cargo={dep.required_cargo}"

    assert dep.required_cargo[good] == quantity, \
        f"Segment {segment_idx} should require {quantity} {good}, got {dep.required_cargo[good]}"


@then(parsers.parse("segment {segment_idx:d} should NOT be skippable"))
def verify_not_skippable(analysis_context, segment_idx):
    """Verify segment should not be skipped."""
    should_skip = analysis_context.get('skip_result')

    assert not should_skip, \
        f"Segment {segment_idx} should NOT be skippable, but got should_skip={should_skip}"


@then(parsers.parse('the reason should contain "{expected_text}"'))
def verify_skip_reason(analysis_context, expected_text):
    """Verify the skip reason contains expected text."""
    reason = analysis_context.get('skip_reason', '')

    assert expected_text in reason, \
        f"Expected reason to contain '{expected_text}', got: {reason}"


@then(parsers.parse("all segments {seg_list} should depend on segment {source_idx:d}"))
def verify_all_depend_on_source(analysis_context, seg_list, source_idx):
    """Verify multiple segments all depend on a source segment."""
    dependencies = analysis_context['dependencies']

    # Parse segment list (e.g., "1, 2, and 3" -> [1, 2, 3])
    seg_list = seg_list.replace(' and ', ', ')
    segment_indices = [int(s.strip()) for s in seg_list.split(',') if s.strip().isdigit()]

    for seg_idx in segment_indices:
        dep = dependencies[seg_idx]
        assert source_idx in dep.depends_on, \
            f"Segment {seg_idx} should depend on segment {source_idx}, got depends_on={dep.depends_on}"


@then(parsers.parse("all segments {seg_list} should have CARGO dependency type"))
def verify_all_cargo_type(analysis_context, seg_list):
    """Verify multiple segments all have CARGO dependency type."""
    dependencies = analysis_context['dependencies']

    # Parse segment list
    seg_list = seg_list.replace(' and ', ', ')
    segment_indices = [int(s.strip()) for s in seg_list.split(',') if s.strip().isdigit()]

    for seg_idx in segment_indices:
        dep = dependencies[seg_idx]
        assert dep.dependency_type == 'CARGO', \
            f"Segment {seg_idx} should have CARGO dependency, got {dep.dependency_type}"
