"""
Tour Scout Mode

For multi-market assignments, plan optimized tour and execute.
Uses OR-Tools for tour optimization with fuel awareness.
"""

from dataclasses import dataclass
from datetime import datetime, timezone
from typing import Dict, List, Optional
import logging
import json
from pathlib import Path

from spacetraders_bot.core.utils import timestamp_iso
from spacetraders_bot.core.route_planner import TimeCalculator


@dataclass
class TourScoutResult:
    """Result from tour scouting"""
    success: bool
    markets_scouted: int
    goods_updated: int
    planned_time: str
    error_message: Optional[str] = None


class TourScoutMode:
    """
    Tour scout strategy

    Plans optimized multi-market tours and executes them.
    """

    def __init__(self, ship, navigator, optimizer, market_service, system: str):
        """
        Initialize tour scout mode

        Args:
            ship: ShipController instance
            navigator: SmartNavigator instance
            optimizer: TourOptimizer instance
            market_service: MarketDataService instance
            system: System symbol (e.g., "X1-HU87")
        """
        self.ship = ship
        self.navigator = navigator
        self.optimizer = optimizer
        self.market_service = market_service
        self.system = system
        self.logger = logging.getLogger(__name__)

    def plan_tour(
        self,
        tour_start: str,
        market_stops: List[str],
        current_fuel: int,
        return_to_start: bool = False
    ) -> Optional[Dict]:
        """
        Plan optimized tour using OR-Tools

        Args:
            tour_start: Starting waypoint
            market_stops: List of markets to visit
            current_fuel: Current fuel level
            return_to_start: Whether to return to start point

        Returns:
            Tour dict with legs and metrics, or None if planning failed
        """
        self.logger.info("Using OR-Tools optimizer with caching...")

        tour = self.optimizer.plan_tour(
            tour_start,
            market_stops,
            current_fuel,
            return_to_start=return_to_start,
            algorithm='ortools',
            use_cache=True,
        )

        if not tour:
            self.logger.error("Failed to find tour")
            return None

        return tour

    def print_tour_summary(self, tour: Dict, ship_data: Dict, tour_count: Optional[int] = None):
        """
        Print tour summary to console

        Args:
            tour: Tour dict from optimizer
            ship_data: Ship data dict with fuel capacity
            tour_count: Optional tour number for continuous mode
        """
        print(f"\n{'='*70}")
        print(f"MARKET SCOUT TOUR - {self.system}")
        if tour_count:
            print(f"Tour #{tour_count}")
        print(f"{'='*70}")
        print(f"Algorithm: OR-TOOLS")
        print(f"Markets to visit: {len(tour['legs'])}")
        planned_time = TimeCalculator.format_time(tour['total_time'])
        print(f"Total time: {planned_time}")
        print(f"Total legs: {tour['total_legs']}")
        print(f"Final fuel: {tour['final_fuel']}/{ship_data['fuel']['capacity']}")
        print(f"\nRoute order:")
        for i, leg in enumerate(tour['legs'], 1):
            dest = leg['goal'] if 'goal' in leg else leg['legs'][0]['goal']
            print(f"  {i}. {dest}")

    def save_tour_to_file(self, tour: Dict, output_path: str):
        """
        Save tour to JSON file

        Args:
            tour: Tour dict from optimizer
            output_path: Path to output file
        """
        output_file = Path(output_path)
        output_file.parent.mkdir(parents=True, exist_ok=True)
        with open(output_file, 'w') as f:
            json.dump(tour, f, indent=2)
        print(f"\nTour saved to: {output_file}")

    def execute_tour(self, tour: Dict) -> TourScoutResult:
        """
        Execute the planned tour

        Navigates to each market, collects data, and updates database.

        Args:
            tour: Tour dict from plan_tour()

        Returns:
            TourScoutResult with metrics
        """
        self.logger.info(f"\n{'='*70}")
        self.logger.info("EXECUTING TOUR - NAVIGATING AND COLLECTING MARKET DATA")
        self.logger.info(f"{'='*70}")

        markets_scouted = 0
        goods_updated = 0
        planned_time = TimeCalculator.format_time(tour['total_time'])

        # Visit each market in the optimized tour order
        for i, leg in enumerate(tour['legs'], 1):
            destination = leg['goal'] if 'goal' in leg else leg['legs'][0]['goal']
            self.logger.info(f"\n[{i}/{len(tour['legs'])}] Navigating to {destination}...")

            # Navigate to market
            if not self.navigator.execute_route(self.ship, destination):
                self.logger.warning(f"Failed to navigate to {destination}, skipping")
                continue

            # Dock to access market
            if not self.ship.dock():
                self.logger.warning(f"Failed to dock at {destination}, skipping")
                continue

            # Collect and update market data
            timestamp = timestamp_iso()
            goods_count = self.market_service.collect_and_update(destination, self.system, timestamp)

            if goods_count > 0:
                markets_scouted += 1
                goods_updated += goods_count
                self.logger.info(f"✅ Updated database: {goods_count} goods")
            else:
                self.logger.warning(f"Failed to get market data for {destination}")

        self.logger.info(f"\n{'='*70}")
        self.logger.info("TOUR EXECUTION COMPLETE")
        self.logger.info(f"{'='*70}")
        self.logger.info(f"Markets scouted: {markets_scouted}/{len(tour['legs'])}")
        self.logger.info(f"Goods updated in database: {goods_updated}")
        self.logger.info(f"{'='*70}")

        return TourScoutResult(
            success=True,
            markets_scouted=markets_scouted,
            goods_updated=goods_updated,
            planned_time=planned_time
        )

    def execute(
        self,
        tour_start: str,
        market_stops: List[str],
        ship_data: Dict,
        return_to_start: bool = False,
        output_path: Optional[str] = None,
        tour_count: Optional[int] = None
    ) -> TourScoutResult:
        """
        Plan and execute a tour

        Args:
            tour_start: Starting waypoint
            market_stops: List of markets to visit
            ship_data: Ship data dict with fuel info
            return_to_start: Whether to return to start
            output_path: Optional file path to save tour
            tour_count: Optional tour number for continuous mode

        Returns:
            TourScoutResult
        """
        # Plan tour
        tour = self.plan_tour(
            tour_start,
            market_stops,
            ship_data['fuel']['current'],
            return_to_start
        )

        if not tour:
            return TourScoutResult(
                success=False,
                markets_scouted=0,
                goods_updated=0,
                planned_time="N/A",
                error_message="Failed to plan tour"
            )

        # Print summary
        self.print_tour_summary(tour, ship_data, tour_count)

        # Save to file if requested
        if output_path:
            self.save_tour_to_file(tour, output_path)

        # Execute tour
        return self.execute_tour(tour)
