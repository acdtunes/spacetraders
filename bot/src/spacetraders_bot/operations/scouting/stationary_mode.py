"""
Stationary Scout Mode

For single-market assignments, navigate once and poll continuously.
Provides 100% utilization with <1 minute data freshness vs touring overhead.
"""

from dataclasses import dataclass
from datetime import datetime, timezone
from typing import Optional
import logging
import random
import time

from spacetraders_bot.core.utils import timestamp_iso


@dataclass
class StationaryScoutResult:
    """Result from stationary scouting"""
    success: bool
    poll_count: int
    goods_updated: int
    error_message: Optional[str] = None


class StationaryScoutMode:
    """
    Stationary scout strategy

    Navigates to a single market once, then polls continuously while docked.
    """

    def __init__(self, ship, navigator, market_service, system: str):
        """
        Initialize stationary scout mode

        Args:
            ship: ShipController instance
            navigator: SmartNavigator instance
            market_service: MarketDataService instance
            system: System symbol (e.g., "X1-HU87")
        """
        self.ship = ship
        self.navigator = navigator
        self.market_service = market_service
        self.system = system
        self.logger = logging.getLogger(__name__)

    def execute(
        self,
        market: str,
        current_location: str,
        poll_interval: int = 60,
        continuous: bool = False,
        running_flag=None
    ) -> StationaryScoutResult:
        """
        Execute stationary scouting

        Args:
            market: Market waypoint to scout
            current_location: Ship's current location
            poll_interval: Seconds between polls (default 60)
            continuous: If True, poll indefinitely (until running_flag becomes False)
            running_flag: Shared flag for graceful shutdown (optional)

        Returns:
            StationaryScoutResult with poll count and goods updated
        """
        self.logger.info(f"\n🎯 STATIONARY SCOUT MODE ACTIVATED")
        self.logger.info(f"   Single market assignment: {market}")
        self.logger.info(f"   Will navigate once, then poll every {poll_interval}s while docked")

        # One-time navigation to the assigned market
        if current_location != market:
            self.logger.info(f"Navigating to assigned market: {market}...")
            if not self.navigator.execute_route(self.ship, market):
                error_msg = f"Failed to navigate to {market}"
                self.logger.error(error_msg)
                return StationaryScoutResult(
                    success=False,
                    poll_count=0,
                    goods_updated=0,
                    error_message=error_msg
                )

        # Dock at the market
        if not self.ship.dock():
            error_msg = f"Failed to dock at {market}"
            self.logger.error(error_msg)
            return StationaryScoutResult(
                success=False,
                poll_count=0,
                goods_updated=0,
                error_message=error_msg
            )

        self.logger.info(f"✅ Positioned at {market} (DOCKED)")
        self.logger.info(f"Starting continuous market polling ({poll_interval}s intervals)...\n")

        # Random initial delay to stagger stationary scouts (0 to poll_interval seconds)
        initial_delay = random.uniform(0, poll_interval)
        self.logger.info(f"⏱️  Random initial delay: {initial_delay:.1f}s (to stagger API calls)")
        time.sleep(initial_delay)

        # Stationary polling loop
        poll_count = 0
        goods_updated_total = 0

        # Default running flag if not provided
        if running_flag is None:
            running_flag = {'value': True}

        while running_flag.get('value', True):
            poll_count += 1
            poll_start = datetime.now(timezone.utc)

            self.logger.info(f"[Poll #{poll_count}] Querying market data at {market}...")

            # Collect and update market data
            timestamp = timestamp_iso()
            goods_count = self.market_service.collect_and_update(market, self.system, timestamp)

            if goods_count > 0:
                goods_updated_total += goods_count
                self.logger.info(f"✅ Updated {goods_count} goods in database")
            else:
                self.logger.warning(f"Failed to update market data for {market}")

            # Check if we should stop (only run once if not continuous)
            if not continuous:
                break

            # Wait poll_interval seconds before next poll
            self.logger.info(f"Waiting {poll_interval}s before next poll...\n")
            time.sleep(poll_interval)

        self.logger.info(f"\n🛑 Stationary scout stopped after {poll_count} polls ({goods_updated_total} goods updated)")

        return StationaryScoutResult(
            success=True,
            poll_count=poll_count,
            goods_updated=goods_updated_total
        )
