"""
Scout Markets Executor

Orchestrates scout operations, determining strategy and managing lifecycle.
"""

import signal
import time
import logging
from typing import Optional
from datetime import datetime, timezone

from spacetraders_bot.core.route_planner import GraphBuilder, TourOptimizer
from spacetraders_bot.core.ship import ShipController
from spacetraders_bot.core.smart_navigator import SmartNavigator
from spacetraders_bot.operations.common import get_database, log_captain_event, humanize_duration

from .market_data_service import MarketDataService
from .stationary_mode import StationaryScoutMode
from .tour_mode import TourScoutMode


class ScoutMarketsExecutor:
    """
    Executor for scout markets operation

    Handles initialization, mode selection, and continuous operation.
    """

    def __init__(self, args, api, logger, captain_logger):
        """
        Initialize executor

        Args:
            args: Parsed command-line arguments
            api: SpaceTraders API client
            logger: Python logger
            captain_logger: Captain's log writer
        """
        self.args = args
        self.api = api
        self.logger = logger
        self.captain_logger = captain_logger

        # Will be initialized in setup()
        self.ship = None
        self.navigator = None
        self.graph = None
        self.ship_data = None
        self.market_service = None
        self.stationary_mode = None
        self.tour_mode = None

        # Continuous mode tracking
        self.continuous = getattr(args, 'continuous', False)
        self.running = {'value': True}  # Dict for shared mutable state
        self.tour_count = 0
        self.operation_start = datetime.now(timezone.utc)

    def setup_signal_handlers(self):
        """Setup graceful shutdown signal handlers"""
        def handle_shutdown(signum, frame):
            self.logger.info(f"Received signal {signum}, shutting down after current tour...")
            self.running['value'] = False

        if self.continuous:
            signal.signal(signal.SIGTERM, handle_shutdown)
            signal.signal(signal.SIGINT, handle_shutdown)
            self.logger.info("🔄 CONTINUOUS MODE ENABLED - Tours will loop indefinitely")

    def setup(self) -> bool:
        """
        Initialize resources

        Returns:
            True if setup successful, False otherwise
        """
        # Load or build graph
        builder = GraphBuilder(self.api)
        self.graph = builder.load_system_graph(self.args.system)

        if not self.graph:
            self.logger.info(f"Graph not found in database, building graph for {self.args.system}...")
            self.graph = builder.build_system_graph(self.args.system)
            if not self.graph:
                self.logger.error(f"Failed to build graph for system {self.args.system}")
                self._log_error(
                    "Graph build failed",
                    f"Unable to build graph for system {self.args.system}",
                    resolution="Run graph-build operation manually",
                    escalate=True
                )
                return False

        # Get ship data
        self.ship_data = self.api.get_ship(self.args.ship)
        if not self.ship_data:
            self.logger.error(f"Failed to get ship data for {self.args.ship}")
            self._log_error(
                "Ship data unavailable",
                f"API returned no data for {self.args.ship}",
                resolution="Ensure ship exists and token has access",
                escalate=True
            )
            return False

        current_location = self.ship_data['nav']['waypointSymbol']
        self.logger.info(f"Ship {self.args.ship} currently at {current_location}")

        # Initialize components
        self.ship = ShipController(self.api, self.args.ship)
        self.navigator = SmartNavigator(self.api, self.args.system)
        db = get_database()
        self.market_service = MarketDataService(self.api, db, self.args.player_id)

        # Initialize modes
        self.stationary_mode = StationaryScoutMode(
            self.ship, self.navigator, self.market_service, self.args.system
        )
        self.tour_mode = TourScoutMode(
            self.ship,
            self.navigator,
            TourOptimizer(self.graph, self.ship_data),
            self.market_service,
            self.args.system
        )

        return True

    def determine_markets(self):
        """
        Determine which markets to visit and tour start point

        Returns:
            (tour_start, market_stops) tuple
        """
        current_location = self.ship_data['nav']['waypointSymbol']

        # Partitioned mode: coordinator provides specific markets list
        if hasattr(self.args, 'markets_list') and self.args.markets_list:
            markets = [m.strip() for m in self.args.markets_list.split(',')]
            self.logger.info(f"Using specific markets list (coordinator-assigned partition): {', '.join(markets)}")

            # Tour starts from first assigned market (partition centroid)
            tour_start = markets[0]
            self.logger.info(f"Tour will start from first assigned market (partition centroid): {tour_start}")
            market_stops = markets

        # Auto-discover mode: find all markets from graph
        else:
            markets = TourOptimizer.get_markets_from_graph(self.graph)
            self.logger.info(f"Found {len(markets)} markets in {self.args.system}: {', '.join(markets)}")

            if not markets:
                self.logger.error("No markets found in system")
                self._log_error(
                    "No markets discovered",
                    f"Graph for {self.args.system} contains no markets",
                    resolution="Verify system symbol or rebuild graph",
                    escalate=True
                )
                return None, None

            # Remove current location from markets list (will visit all others from here)
            tour_start = current_location
            market_stops = [m for m in markets if m != current_location]

            # Limit to requested number of markets (if specified)
            if hasattr(self.args, 'markets') and self.args.markets and self.args.markets < len(market_stops):
                market_stops = market_stops[:self.args.markets]
                self.logger.info(f"Limiting to {self.args.markets} markets (excluding current location)")

        return tour_start, market_stops

    def run_single_tour(self) -> bool:
        """
        Run a single tour (either stationary or touring mode)

        Returns:
            True if successful, False otherwise
        """
        self.tour_count += 1
        tour_start_time = datetime.now(timezone.utc)

        if self.continuous:
            self.logger.info(f"\n{'='*70}")
            self.logger.info(f"STARTING TOUR #{self.tour_count}")
            self.logger.info(f"{'='*70}")

        # Determine markets
        tour_start, market_stops = self.determine_markets()
        if market_stops is None:
            return False

        if not market_stops:
            self.logger.info("Ship is at the only market in the system!")
            if self.continuous:
                time.sleep(60)  # Wait a minute before next tour
            return True

        # STATIONARY MODE: Single market assignment
        if len(market_stops) == 1:
            current_location = self.ship_data['nav']['waypointSymbol']
            poll_interval = getattr(self.args, 'interval', 60)

            result = self.stationary_mode.execute(
                market_stops[0],
                current_location,
                poll_interval=poll_interval,
                continuous=self.continuous,
                running_flag=self.running
            )

            if not result.success:
                if self.continuous:
                    self.logger.info("Waiting 60s before retry...")
                    time.sleep(60)
                    return True  # Continue in continuous mode
                return False

            self._log_tour_completion(
                result.goods_updated,  # markets_scouted for stationary = goods updated
                result.goods_updated,
                "N/A",  # No planned time for stationary
                self.tour_count
            )
            return True

        # TOUR MODE: Multiple markets
        result = self.tour_mode.execute(
            tour_start,
            market_stops,
            self.ship_data,
            return_to_start=self.args.return_to_start,
            output_path=self.args.output if hasattr(self.args, 'output') else None,
            tour_count=self.tour_count if self.continuous else None
        )

        if not result.success:
            if self.continuous:
                self.logger.info("Waiting 60s before retry...")
                time.sleep(60)
                return True  # Continue in continuous mode
            return False

        self._log_tour_completion(
            result.markets_scouted,
            result.goods_updated,
            result.planned_time,
            self.tour_count
        )

        if self.continuous:
            self.logger.info(f"Tour #{self.tour_count} complete, restarting immediately...")

        return True

    def run(self) -> int:
        """
        Main execution entry point

        Returns:
            0 on success, 1 on failure
        """
        # Setup
        self.setup_signal_handlers()

        if not self.setup():
            return 1

        # Main loop (runs once if not continuous)
        while self.running['value']:
            if not self.run_single_tour():
                return 1

            # Exit loop if not continuous
            if not self.continuous:
                break

        if self.continuous:
            self.logger.info(f"\n🛑 Continuous scouting stopped after {self.tour_count} tours")

        return 0

    def _log_error(self, error: str, cause: str, resolution: str = "Manual follow-up",
                   escalate: bool = False):
        """Log critical error to captain's log"""
        from spacetraders_bot.operations.common import get_operator_name
        operator_name = get_operator_name(self.args)

        log_captain_event(
            self.captain_logger,
            'CRITICAL_ERROR',
            operator=operator_name,
            ship=self.args.ship,
            error=error,
            cause=cause,
            impact={},
            resolution=resolution,
            lesson="Review scouting configuration",
            escalate=escalate,
            tags=['scouting']
        )

    def _log_tour_completion(self, markets_scouted: int, goods_updated: int,
                            total_time: str, tour_index: int):
        """Log tour completion to captain's log"""
        from spacetraders_bot.operations.common import get_operator_name
        operator_name = get_operator_name(self.args)

        duration = humanize_duration(datetime.now(timezone.utc) - self.operation_start)
        results = {
            'Markets Visited': markets_scouted,
            'Goods Updated': goods_updated,
            'Planned Duration': total_time,
            'Tour': tour_index,
        }
        notes = f"Scouted markets in {self.args.system}."

        log_captain_event(
            self.captain_logger,
            'OPERATION_COMPLETED',
            operator=operator_name,
            ship=self.args.ship,
            duration=duration,
            results=results,
            notes=notes,
            tags=['scouting', self.args.system.lower()]
        )
