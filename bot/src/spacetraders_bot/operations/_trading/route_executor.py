"""
Route Executor

Orchestrates execution of complete multi-leg trading routes.
Single Responsibility: Execute and monitor full route execution.
"""

import logging
from typing import Set

from spacetraders_bot.core.api_client import APIClient
from spacetraders_bot.core.ship import ShipController
from spacetraders_bot.core.smart_navigator import SmartNavigator
from spacetraders_bot.operations._trading.models import MultiLegRoute
from spacetraders_bot.operations._trading.segment_executor import SegmentExecutor
from spacetraders_bot.operations._trading.dependency_analyzer import analyze_route_dependencies
from spacetraders_bot.operations._trading.market_service import validate_market_data_freshness


class RouteExecutor:
    """Executes complete multi-leg trading routes with monitoring and validation"""

    def __init__(
        self,
        ship: ShipController,
        api: APIClient,
        db,
        player_id: int,
        logger: logging.Logger
    ):
        self.ship = ship
        self.api = api
        self.db = db
        self.player_id = player_id
        self.logger = logger

    def execute_route(self, route: MultiLegRoute) -> bool:
        """
        Execute a complete multi-leg trading route

        Args:
            route: The planned multi-leg trading route

        Returns:
            True if route executed successfully, False on any critical failure
        """
        self.logger.info("")
        self.logger.info("=" * 70)
        self.logger.info("ROUTE EXECUTION START")
        self.logger.info("=" * 70)

        # Get ship data to determine system
        ship_data = self.ship.get_status()
        if not ship_data:
            self.logger.error("Failed to get ship status")
            return False

        system = ship_data['nav']['systemSymbol']
        starting_location = ship_data['nav']['waypointSymbol']

        # Get starting credits
        agent = self.api.get_agent()
        if not agent:
            self.logger.error("Failed to get agent data")
            return False
        starting_credits = agent['credits']

        self.logger.info(f"Starting location: {starting_location}")
        self.logger.info(f"Starting credits: {starting_credits:,}")
        self.logger.info(f"Route segments: {len(route.segments)}")
        self.logger.info("=" * 70)

        # PRE-FLIGHT VALIDATION: Check market data freshness
        is_valid, stale_markets, aging_markets = validate_market_data_freshness(
            self.db,
            route,
            self.logger
        )
        if not is_valid:
            return False

        # Analyze dependencies for smart skip logic
        dependencies = analyze_route_dependencies(route)

        self.logger.info("")
        self.logger.info("=" * 70)
        self.logger.info("DEPENDENCY ANALYSIS")
        self.logger.info("=" * 70)
        for idx, dep in dependencies.items():
            dep_type_str = dep.dependency_type if dep.dependency_type != 'NONE' else 'INDEPENDENT'
            self.logger.info(f"Segment {idx}: {dep_type_str}, depends_on={dep.depends_on}, can_skip={dep.can_skip}")
        self.logger.info("=" * 70)

        # Create navigator and segment executor
        navigator = SmartNavigator(self.api, system)
        segment_executor = SegmentExecutor(
            self.ship,
            navigator,
            self.api,
            self.db,
            system,
            self.logger
        )

        # Track execution state
        skipped_segments: Set[int] = set()
        total_revenue = 0
        total_costs = 0

        # Execute each segment
        for segment_num, segment in enumerate(route.segments, 1):
            segment_index = segment_num - 1

            # Execute segment
            success, segment_revenue, segment_costs = segment_executor.execute(
                segment,
                segment_num,
                len(route.segments),
                route,
                segment_index,
                skipped_segments,
                dependencies
            )

            if not success:
                self.logger.error(f"❌ Segment {segment_num} failed")
                self.logger.error("Route execution aborted")
                return False

            # Track metrics
            total_revenue += segment_revenue
            total_costs += segment_costs

        # Final summary
        final_agent = self.api.get_agent()
        final_credits = final_agent['credits'] if final_agent else starting_credits
        actual_profit = final_credits - starting_credits

        self.logger.info("")
        self.logger.info("=" * 70)
        self.logger.info("✅ ROUTE EXECUTION COMPLETE")
        self.logger.info("=" * 70)
        self.logger.info(f"Revenue: {total_revenue:,} credits")
        self.logger.info(f"Costs: {total_costs:,} credits")
        self.logger.info(f"Actual profit: {actual_profit:,} credits")
        self.logger.info(f"Estimated profit: {route.total_profit:,} credits")
        accuracy = (actual_profit / route.total_profit * 100) if route.total_profit > 0 else 0
        self.logger.info(f"Accuracy: {accuracy:.1f}%")
        self.logger.info(f"Segments skipped: {len(skipped_segments)}/{len(route.segments)}")
        self.logger.info("=" * 70)

        return True
