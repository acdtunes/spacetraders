"""
Cargo Salvage Service

Handles emergency cargo cleanup when circuit breakers trigger.
Single Responsibility: Sell unprofitable cargo using intelligent market search.
"""

import logging
from typing import Dict, List, Optional

from spacetraders_bot.core.api_client import APIClient
from spacetraders_bot.core.ship import ShipController
from spacetraders_bot.core.smart_navigator import SmartNavigator
from spacetraders_bot.operations._trading.models import MultiLegRoute
from spacetraders_bot.operations._trading.market_service import find_planned_sell_destination
from spacetraders_bot.operations._trading.market_repository import MarketRepository


class CargoSalvageService:
    """
    Intelligent cargo salvage service for circuit breaker scenarios

    Three-tier salvage strategy:
    1. Navigate to planned sell destination from route (HIGHEST PRIORITY)
    2. Sell at current market if it accepts the good
    3. Search for nearby buyers (<200 units away)
    """

    def __init__(
        self,
        ship: ShipController,
        api: APIClient,
        db,
        logger: logging.Logger
    ):
        self.ship = ship
        self.api = api
        self.db = db
        self.logger = logger
        self.market_repo = MarketRepository(db)

    def salvage_cargo(
        self,
        unprofitable_item: Optional[str] = None,
        route: Optional[MultiLegRoute] = None,
        current_segment_index: Optional[int] = None
    ) -> bool:
        """
        Main entry point for cargo salvage

        Args:
            unprofitable_item: Specific good that triggered circuit breaker (if any)
            route: Planned route (for finding planned sell destinations)
            current_segment_index: Index where circuit breaker triggered

        Returns:
            True if salvage succeeded or no cargo to salvage, False on critical failure
        """
        try:
            # Get current ship status
            ship_data = self.ship.get_status()
            if not ship_data:
                self.logger.error("Failed to get ship status for cargo cleanup")
                return False

            cargo = ship_data.get('cargo', {})
            inventory = cargo.get('inventory', [])

            # Nothing to salvage
            if not inventory or cargo.get('units', 0) == 0:
                self.logger.info("No stranded cargo to clean up")
                return True

            # Determine salvage scope
            items_to_salvage, items_to_keep = self._identify_salvage_items(
                inventory,
                unprofitable_item
            )

            if not items_to_salvage:
                self.logger.warning(f"  ⚠️  {unprofitable_item} not found in cargo - nothing to salvage")
                return True

            # Log salvage plan
            self._log_salvage_plan(items_to_salvage, items_to_keep, unprofitable_item)

            # Get current location
            current_waypoint = ship_data['nav']['waypointSymbol']
            system = ship_data['nav']['systemSymbol']

            # Ensure ship is docked
            if not self._ensure_docked(ship_data):
                return False

            # Salvage each item
            cleanup_revenue = self._salvage_items(
                items_to_salvage,
                current_waypoint,
                system,
                route,
                current_segment_index
            )

            # Final summary
            return self._log_final_summary(cleanup_revenue)

        except Exception as e:
            self.logger.error(f"Critical error during cargo cleanup: {e}")
            import traceback
            self.logger.error(traceback.format_exc())
            return False

    def _identify_salvage_items(
        self,
        inventory: List[Dict],
        unprofitable_item: Optional[str]
    ) -> tuple[List[Dict], List[Dict]]:
        """
        Determine which items to salvage vs keep

        Args:
            inventory: Ship inventory
            unprofitable_item: Specific item to salvage (selective mode)

        Returns:
            Tuple of (items_to_salvage, items_to_keep)
        """
        if unprofitable_item:
            # Selective salvage - only salvage the unprofitable item
            items_to_salvage = [item for item in inventory if item['symbol'] == unprofitable_item]
            items_to_keep = [item for item in inventory if item['symbol'] != unprofitable_item]
        else:
            # Full salvage - salvage everything
            items_to_salvage = inventory
            items_to_keep = []

        return items_to_salvage, items_to_keep

    def _log_salvage_plan(
        self,
        items_to_salvage: List[Dict],
        items_to_keep: List[Dict],
        unprofitable_item: Optional[str]
    ) -> None:
        """Log salvage plan"""
        if unprofitable_item:
            self.logger.warning("="*70)
            self.logger.warning(f"🧹 SELECTIVE CARGO CLEANUP: Salvaging only {unprofitable_item}")
            self.logger.warning("="*70)
            self.logger.warning(f"  Unprofitable item: {unprofitable_item}")
            self.logger.warning("  Other cargo will be KEPT for future profitable segments")
            self.logger.warning("="*70)

            if items_to_keep:
                self.logger.info("  Items being KEPT for future segments:")
                for item in items_to_keep:
                    self.logger.info(f"    - {item['units']}x {item['symbol']}")
        else:
            self.logger.warning("="*70)
            self.logger.warning("🧹 CARGO CLEANUP: Selling ALL stranded cargo")
            self.logger.warning("="*70)

        # Log items to be salvaged
        for item in items_to_salvage:
            self.logger.warning(f"  Salvaging: {item['units']}x {item['symbol']}")

    def _ensure_docked(self, ship_data: Dict) -> bool:
        """Ensure ship is docked"""
        if ship_data['nav']['status'] != 'DOCKED':
            self.logger.info("Docking for cargo cleanup...")
            if not self.ship.dock():
                self.logger.error("Failed to dock for cargo cleanup")
                return False
        return True

    def _salvage_items(
        self,
        items: List[Dict],
        current_waypoint: str,
        system: str,
        route: Optional[MultiLegRoute],
        current_segment_index: Optional[int]
    ) -> int:
        """
        Salvage cargo items using three-tier strategy

        Returns:
            Total cleanup revenue
        """
        cleanup_revenue = 0

        for item in items:
            good = item['symbol']
            units = item['units']

            # Try planned destination first
            revenue = self._try_planned_destination(
                good, units, current_waypoint, system, route, current_segment_index
            )

            if revenue > 0:
                cleanup_revenue += revenue
                current_waypoint = self.ship.get_status()['nav']['waypointSymbol']
                continue

            # Try current market
            revenue = self._try_current_market(good, units, current_waypoint)

            if revenue > 0:
                cleanup_revenue += revenue
                continue

            # Try nearby markets
            revenue = self._try_nearby_markets(good, units, current_waypoint, system)
            cleanup_revenue += revenue

        return cleanup_revenue

    def _try_planned_destination(
        self,
        good: str,
        units: int,
        current_waypoint: str,
        system: str,
        route: Optional[MultiLegRoute],
        current_segment_index: Optional[int]
    ) -> int:
        """
        Try to navigate to planned sell destination

        Returns:
            Revenue from sale, or 0 if failed
        """
        # Check if there's a planned sell destination
        if not route or current_segment_index is None:
            return 0

        self.logger.info(f"🔍 Searching for planned sell destination for {good} from segment {current_segment_index}")
        planned_waypoint = find_planned_sell_destination(good, route, current_segment_index)

        if not planned_waypoint:
            self.logger.info(f"  No planned sell destination found in remaining segments")
            return 0

        if planned_waypoint == current_waypoint:
            return 0  # Already at planned destination

        self.logger.warning(f"  🎯 Found planned sell destination for {good}: {planned_waypoint}")
        self.logger.warning(f"  Navigating from {current_waypoint} to {planned_waypoint}...")

        try:
            navigator = SmartNavigator(self.api, system)

            if navigator.execute_route(self.ship, planned_waypoint):
                self.logger.warning(f"  ✅ Arrived at {planned_waypoint}, docking...")
                if self.ship.dock():
                    transaction = self.ship.sell(good, units, check_market_prices=False)
                    if transaction:
                        revenue = transaction['totalPrice']
                        price_per_unit = revenue / units if units > 0 else 0
                        self.logger.warning(f"  ✅ Sold {units}x {good} for {revenue:,} credits ({price_per_unit:.0f} cr/unit)")
                        self.logger.warning(f"  💰 Recovered value by navigating to planned destination!")
                        return revenue
                    else:
                        self.logger.error(f"  ❌ Failed to sell {good} at planned destination")
                else:
                    self.logger.error(f"  ❌ Failed to dock at {planned_waypoint}")
            else:
                self.logger.error(f"  ❌ Failed to navigate to {planned_waypoint}")

        except Exception as e:
            self.logger.error(f"  ❌ Error navigating to planned destination: {e}")

        return 0

    def _try_current_market(self, good: str, units: int, current_waypoint: str) -> int:
        """
        Try to sell at current market

        Returns:
            Revenue from sale, or 0 if market doesn't buy
        """
        self.logger.info(f"Checking if {current_waypoint} buys {good}...")

        if not self.market_repo.check_market_accepts_good(current_waypoint, good):
            self.logger.warning(f"  ❌ {current_waypoint} doesn't buy {good}")
            return 0

        self.logger.info(f"  ✅ {current_waypoint} buys {good}")
        self.logger.warning(f"  Selling {units}x {good} at current market...")

        try:
            transaction = self.ship.sell(good, units, check_market_prices=False)
            if transaction:
                revenue = transaction['totalPrice']
                price_per_unit = revenue / units if units > 0 else 0
                self.logger.warning(f"  ✅ Sold {units}x {good} for {revenue:,} credits ({price_per_unit:.0f} cr/unit)")
                return revenue
            else:
                self.logger.error(f"  ❌ Failed to sell {good}")
        except Exception as e:
            self.logger.error(f"  ❌ Error selling {good}: {e}")

        return 0

    def _try_nearby_markets(
        self,
        good: str,
        units: int,
        current_waypoint: str,
        system: str
    ) -> int:
        """
        Search for and navigate to nearby buyers

        Returns:
            Revenue from sale, or 0 if no nearby buyers found
        """
        self.logger.warning(f"  Current market doesn't buy {good} - searching for nearby buyers...")

        try:
            buyers = self.market_repo.find_nearby_buyers(
                good=good,
                origin_waypoint=current_waypoint,
                system=system,
                max_distance=200,
                limit=5
            )

            if not buyers:
                self.logger.warning(f"  ⚠️  No buyers found for {good} in system {system}")
                self.logger.warning(f"  Skipping {good} - will remain in cargo")
                return 0

            # Use best (closest) buyer
            best_buyer = buyers[0]
            self.logger.warning(f"  🎯 Found buyer: {best_buyer.waypoint_symbol} ({best_buyer.distance:.0f} units away, {best_buyer.purchase_price:,} cr/unit)")
            self.logger.warning(f"  Navigating to {best_buyer.waypoint_symbol}...")

            navigator = SmartNavigator(self.api, system)

            if navigator.execute_route(self.ship, best_buyer.waypoint_symbol):
                self.logger.warning(f"  Arrived at {best_buyer.waypoint_symbol}, docking...")
                if self.ship.dock():
                    transaction = self.ship.sell(good, units, check_market_prices=False)
                    if transaction:
                        revenue = transaction['totalPrice']
                        price_per_unit = revenue / units if units > 0 else 0
                        self.logger.warning(f"  ✅ Sold {units}x {good} for {revenue:,} credits ({price_per_unit:.0f} cr/unit)")
                        return revenue
                    else:
                        self.logger.error(f"  ❌ Failed to sell {good} at {best_buyer.waypoint_symbol}")
                else:
                    self.logger.error(f"  ❌ Failed to dock at {best_buyer.waypoint_symbol}")
            else:
                self.logger.error(f"  ❌ Failed to navigate to {best_buyer.waypoint_symbol}")

        except Exception as e:
            self.logger.error(f"  ❌ Error searching for nearby buyers: {e}")
            import traceback
            self.logger.error(traceback.format_exc())

        return 0

    def _log_final_summary(self, cleanup_revenue: int) -> bool:
        """
        Log final salvage summary

        Returns:
            True if salvage was fully or partially successful
        """
        self.logger.warning(f"Total cleanup revenue: {cleanup_revenue:,} credits")
        self.logger.warning("="*70)

        # Verify final cargo state
        final_status = self.ship.get_status()
        if not final_status:
            return True  # Assume success if can't verify

        final_cargo = final_status.get('cargo', {})
        if final_cargo.get('units', 0) == 0:
            self.logger.info("✅ Cargo cleanup complete - ship hold empty")
            return True
        else:
            self.logger.warning(f"⚠️  Partial cleanup - {final_cargo.get('units', 0)} units remaining")
            # Log which goods remain
            for item in final_cargo.get('inventory', []):
                self.logger.warning(f"  Remaining: {item['units']}x {item['symbol']}")
            return True  # Partial success is still acceptable
