"""
Dependency Analyzer

Analyzes route segment dependencies to enable smart skip logic.
Single Responsibility: Dependency analysis and skip decision logic.
"""

from typing import Dict, List, Tuple

from spacetraders_bot.operations._trading.models import MultiLegRoute, RouteSegment, SegmentDependency


def analyze_route_dependencies(route: MultiLegRoute) -> Dict[int, SegmentDependency]:
    """
    Analyze route and build dependency graph for smart skip logic

    Properly tracks cargo flow by simulating cargo state at each segment,
    accounting for carry-through cargo and partial sells.

    Key insight: A segment depends on the ORIGINAL SOURCE of cargo, not just
    the most recent segment that touched that good.

    Returns:
        Map of segment_index → SegmentDependency
    """
    dependencies = {}

    # Track cargo state as we progress through route
    # Maps good -> {segment_index: units} to track WHERE each unit originated
    cargo_sources: Dict[str, Dict[int, int]] = {}

    for i, segment in enumerate(route.segments):
        dep = SegmentDependency(
            segment_index=i,
            depends_on=[],
            dependency_type='NONE',
            required_cargo={},
            required_credits=0,
            can_skip=True
        )

        # Check CARGO dependencies: Do we need to SELL goods from prior segments?
        sell_actions = [a for a in segment.actions_at_destination if a.action == 'SELL']
        for sell_action in sell_actions:
            good = sell_action.good
            units_to_sell = sell_action.units

            # Find ALL source segments that contributed to this cargo
            if good in cargo_sources:
                remaining_to_sell = units_to_sell
                # Consume cargo from sources in order (FIFO-like)
                for source_idx in sorted(cargo_sources[good].keys()):
                    available_from_source = cargo_sources[good][source_idx]
                    units_consumed = min(remaining_to_sell, available_from_source)

                    if units_consumed > 0:
                        # This segment depends on source_idx
                        if source_idx not in dep.depends_on:
                            dep.depends_on.append(source_idx)
                        dep.dependency_type = 'CARGO'
                        dep.required_cargo[good] = dep.required_cargo.get(good, 0) + units_consumed
                        dep.can_skip = False

                        remaining_to_sell -= units_consumed
                        if remaining_to_sell == 0:
                            break

        # Update cargo sources: Process SELL actions (remove cargo)
        for sell_action in sell_actions:
            good = sell_action.good
            units_to_sell = sell_action.units

            if good in cargo_sources:
                remaining_to_sell = units_to_sell
                # Remove sold cargo from sources (FIFO-like)
                for source_idx in sorted(list(cargo_sources[good].keys())):
                    available = cargo_sources[good][source_idx]
                    consumed = min(remaining_to_sell, available)

                    cargo_sources[good][source_idx] -= consumed
                    remaining_to_sell -= consumed

                    # Clean up depleted sources
                    if cargo_sources[good][source_idx] == 0:
                        del cargo_sources[good][source_idx]

                    if remaining_to_sell == 0:
                        break

                # Clean up empty goods
                if not cargo_sources[good]:
                    del cargo_sources[good]

        # Update cargo sources: Process BUY actions (add cargo)
        buy_actions = [a for a in segment.actions_at_destination if a.action == 'BUY']
        for buy_action in buy_actions:
            good = buy_action.good
            units = buy_action.units

            # Add this segment as a source for this good
            if good not in cargo_sources:
                cargo_sources[good] = {}
            cargo_sources[good][i] = cargo_sources[good].get(i, 0) + units

        # Check CREDIT dependencies: Do we need revenue to afford purchases?
        total_buy_cost = sum(a.total_value for a in buy_actions)

        if total_buy_cost > 0:
            dep.required_credits = total_buy_cost

        dependencies[i] = dep

    return dependencies


def should_skip_segment(
    segment_index: int,
    failure_reason: str,
    dependencies: Dict[int, SegmentDependency],
    route: MultiLegRoute,
    current_cargo: Dict[str, int],
    current_credits: int
) -> Tuple[bool, str]:
    """
    Determine if failed segment should be skipped vs abort operation

    Returns:
        (should_skip, reason)
    """
    dep = dependencies[segment_index]

    # Build transitive closure of dependencies: segments affected by this failure
    affected_segments = {segment_index}  # Start with the failed segment itself

    # Keep expanding until no new dependencies found (transitive closure)
    changed = True
    while changed:
        changed = False
        for seg_idx in range(segment_index + 1, len(route.segments)):
            if seg_idx not in affected_segments:
                seg_dep = dependencies[seg_idx]
                # Check if this segment depends on any affected segment
                if any(dep_idx in affected_segments for dep_idx in seg_dep.depends_on):
                    affected_segments.add(seg_idx)
                    changed = True

    # Check if any remaining segments are independent
    remaining_segments = route.segments[segment_index + 1:]
    independent_segments = []

    for i, remaining_seg in enumerate(remaining_segments):
        remaining_idx = segment_index + 1 + i
        if remaining_idx not in affected_segments:
            independent_segments.append((remaining_idx, remaining_seg))

    # IMPORTANT: Check dependency first before profit
    # If all segments depend on failed segment, abort regardless of profit calculation
    if len(independent_segments) == 0:
        return False, "All remaining segments depend on failed segment - must abort"

    # Check if remaining independent segments are profitable enough
    # Calculate incremental profit for each independent segment
    remaining_profit = 0
    for seg_idx, seg in independent_segments:
        # Calculate this segment's profit contribution
        if seg_idx > 0:
            prior_seg = route.segments[seg_idx - 1]
            segment_profit = seg.cumulative_profit - prior_seg.cumulative_profit
        else:
            segment_profit = seg.cumulative_profit

        remaining_profit += segment_profit

    if remaining_profit < 5000:  # Configurable minimum threshold
        return False, f"Remaining profit too low ({remaining_profit:,} credits < 5,000 minimum)"

    return True, f"Can skip - {len(independent_segments)} independent segments remain with {remaining_profit:,} profit"


def cargo_blocks_future_segments(
    cargo: Dict[str, int],
    remaining_segments: List[RouteSegment],
    ship_capacity: int
) -> bool:
    """
    Check if current cargo prevents future segments from executing

    Returns:
        True if cargo blocks any segment's buy actions
    """
    cargo_used = sum(cargo.values())
    cargo_available = ship_capacity - cargo_used

    for segment in remaining_segments:
        buy_actions = [a for a in segment.actions_at_destination if a.action == 'BUY']
        units_needed = sum(a.units for a in buy_actions)

        if units_needed > cargo_available:
            return True

    return False
