"""
Segment Executor

Handles execution of a single route segment: navigation, docking, and trade actions.
Single Responsibility: Execute one segment of a multi-leg route.
"""

import logging
from typing import Dict, Set

from spacetraders_bot.core.api_client import APIClient
from spacetraders_bot.core.ship import ShipController
from spacetraders_bot.core.smart_navigator import SmartNavigator
from spacetraders_bot.operations._trading.models import RouteSegment, MultiLegRoute, SegmentDependency
from spacetraders_bot.operations._trading.trade_executor import TradeExecutor


class SegmentExecutor:
    """Executes a single route segment including navigation and trade actions"""

    def __init__(
        self,
        ship: ShipController,
        navigator: SmartNavigator,
        api: APIClient,
        db,
        system: str,
        logger: logging.Logger
    ):
        self.ship = ship
        self.navigator = navigator
        self.api = api
        self.db = db
        self.system = system
        self.logger = logger
        self.trade_executor = TradeExecutor(ship, api, db, system, logger)

    def execute(
        self,
        segment: RouteSegment,
        segment_number: int,
        total_segments: int,
        route: MultiLegRoute,
        segment_index: int,
        skipped_segments: Set[int],
        dependencies: Dict[int, SegmentDependency]
    ) -> tuple[bool, int, int]:
        """
        Execute a complete route segment

        Args:
            segment: Segment to execute
            segment_number: 1-indexed segment number (for display)
            total_segments: Total number of segments in route
            route: Full route (for profitability checking)
            segment_index: 0-indexed segment index
            skipped_segments: Set of skipped segment indices
            dependencies: Segment dependency map

        Returns:
            Tuple of (success, segment_revenue, segment_costs)
        """
        # Check if we should skip this segment due to failed dependencies
        if any(dep_idx in skipped_segments for dep_idx in dependencies[segment_index].depends_on):
            self.logger.warning("=" * 70)
            self.logger.warning(f"⏭️  SKIPPING SEGMENT {segment_number} - depends on failed segment")
            self.logger.warning("=" * 70)
            skipped_segments.add(segment_index)
            return True, 0, 0  # Success=True means continue to next segment

        # Log segment header
        self.logger.info("")
        self.logger.info("-" * 70)
        self.logger.info(f"SEGMENT {segment_number}/{total_segments}: {segment.from_waypoint} → {segment.to_waypoint}")
        self.logger.info("-" * 70)
        self.logger.info(f"Distance: {segment.distance:.0f} units")
        self.logger.info(f"Fuel cost estimate: {segment.fuel_cost}")
        self.logger.info(f"Actions planned: {len(segment.actions_at_destination)}")

        # Step 1: Navigate to destination
        if not self._navigate_to_destination(segment.to_waypoint):
            return False, 0, 0

        # Step 2: Dock at waypoint
        if not self._dock_at_destination(segment.to_waypoint):
            return False, 0, 0

        # Step 3: Execute trade actions
        segment_revenue, segment_costs = self._execute_trade_actions(
            segment,
            route,
            segment_index
        )

        return True, segment_revenue, segment_costs

    def _navigate_to_destination(self, destination: str) -> bool:
        """Navigate to destination waypoint"""
        self.logger.info(f"\n🚀 Navigating to {destination}...")

        if not self.navigator.execute_route(self.ship, destination):
            self.logger.error(f"❌ Navigation failed to {destination}")
            return False

        self.logger.info(f"✅ Arrived at {destination}")
        return True

    def _dock_at_destination(self, destination: str) -> bool:
        """Dock at destination waypoint"""
        self.logger.info(f"\n🛬 Docking at {destination}...")

        if not self.ship.dock():
            self.logger.error("❌ Failed to dock")
            return False

        return True

    def _execute_trade_actions(
        self,
        segment: RouteSegment,
        route: MultiLegRoute,
        segment_index: int
    ) -> tuple[int, int]:
        """
        Execute all trade actions at the destination

        Returns:
            Tuple of (segment_revenue, segment_costs)
        """
        self.logger.info(f"\n💼 Executing {len(segment.actions_at_destination)} trade actions...")

        segment_revenue = 0
        segment_costs = 0

        for action_num, action in enumerate(segment.actions_at_destination, 1):
            self.logger.info(f"\n  Action {action_num}/{len(segment.actions_at_destination)}: {action.action} {action.units}x {action.good}")

            if action.action == 'BUY':
                success, cost = self.trade_executor.execute_buy_action(action, route, segment_index)
                if not success:
                    self.logger.error(f"  ❌ Purchase failed for {action.good}")
                    # For now, continue with next action (could make this configurable)
                    continue
                segment_costs += cost

            elif action.action == 'SELL':
                success, revenue = self.trade_executor.execute_sell_action(action)
                if not success:
                    self.logger.error(f"  ❌ Sale failed for {action.good}")
                    continue
                segment_revenue += revenue

        return segment_revenue, segment_costs
