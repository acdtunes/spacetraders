"""
Trade Executor

Handles execution of individual buy and sell actions.
Single Responsibility: Execute trades and update market data.
"""

import logging
from typing import Dict, Optional

from spacetraders_bot.core.api_client import APIClient
from spacetraders_bot.core.ship import ShipController
from spacetraders_bot.operations._trading.models import TradeAction, MultiLegRoute
from spacetraders_bot.operations._trading.circuit_breaker import ProfitabilityValidator, calculate_batch_size
from spacetraders_bot.operations._trading.market_service import update_market_price_from_transaction


class TradeExecutor:
    """Executes buy and sell trade actions with validation and database updates"""

    def __init__(
        self,
        ship: ShipController,
        api: APIClient,
        db,
        system: str,
        logger: logging.Logger
    ):
        self.ship = ship
        self.api = api
        self.db = db
        self.system = system
        self.logger = logger
        self.profitability_validator = ProfitabilityValidator(api, logger)

    def execute_buy_action(
        self,
        action: TradeAction,
        route: MultiLegRoute,
        segment_index: int
    ) -> tuple[bool, int]:
        """
        Execute a purchase action with profitability validation and batch buying

        Args:
            action: Buy action to execute
            route: Full route (for profitability checking)
            segment_index: Current segment index

        Returns:
            Tuple of (success, total_cost)
        """
        self.logger.info(f"  💰 Buying {action.units}x {action.good} @ {action.price_per_unit:,} = {action.total_value:,} credits")

        # Determine batch size based on good value
        batch_size = calculate_batch_size(action.price_per_unit)
        total_units_to_buy = action.units
        units_remaining = total_units_to_buy

        # Log batch sizing decision
        self._log_batch_sizing(action.price_per_unit, batch_size)

        # Small purchase optimization - single transaction
        if total_units_to_buy <= batch_size:
            self.logger.info(f"  📦 Small purchase ({total_units_to_buy} units) - using single transaction")

            # Validate profitability before purchase
            is_profitable, error_msg = self.profitability_validator.validate_purchase_profitability(
                action, route, segment_index, self.system
            )
            if not is_profitable:
                self.logger.error(f"  ❌ Purchase blocked: {error_msg}")
                return False, 0

            # Check cargo space
            if not self._has_cargo_space(total_units_to_buy):
                self.logger.error("  ❌ Insufficient cargo space")
                return False, 0

            # Execute purchase
            return self._execute_single_purchase(action.waypoint, action.good, total_units_to_buy)

        # Batch purchasing for larger quantities
        self.logger.info(f"  📦 Batch purchasing: {total_units_to_buy} units in batches of {batch_size}")

        total_cost = 0
        batch_num = 0

        while units_remaining > 0:
            batch_num += 1
            units_this_batch = min(batch_size, units_remaining)

            self.logger.info(f"  📦 Batch {batch_num}: Purchasing {units_this_batch} units...")

            # Check cargo space before each batch
            if not self._has_cargo_space(units_this_batch):
                self.logger.error(f"  ❌ Cargo full - purchased {total_units_to_buy - units_remaining}/{total_units_to_buy} units")
                return units_remaining == 0, total_cost

            # Execute purchase
            success, batch_cost = self._execute_single_purchase(action.waypoint, action.good, units_this_batch)
            if not success:
                self.logger.error(f"  ❌ Batch {batch_num} failed - stopping purchases")
                return False, total_cost

            total_cost += batch_cost
            units_remaining -= units_this_batch
            self.logger.info(f"  ✅ Batch {batch_num} complete: {units_this_batch} units for {batch_cost:,} credits")

        self.logger.info(f"  ✅ All batches complete: {total_units_to_buy} units for {total_cost:,} credits")
        return True, total_cost

    def execute_sell_action(self, action: TradeAction) -> tuple[bool, int]:
        """
        Execute a sell action

        Args:
            action: Sell action to execute

        Returns:
            Tuple of (success, total_revenue)
        """
        self.logger.info(f"  💵 Selling {action.units}x {action.good} @ {action.price_per_unit:,} = {action.total_value:,} credits")

        # Sell the cargo
        result = self.ship.sell(action.good, action.units, check_market_prices=False)
        if not result:
            self.logger.error(f"  ❌ Failed to sell {action.good}")
            return False, 0

        total_revenue = result['totalPrice']
        actual_price_per_unit = result['pricePerUnit']

        # Update database with actual transaction price
        update_market_price_from_transaction(
            self.db,
            action.waypoint,
            action.good,
            'SELL',
            actual_price_per_unit,
            self.logger
        )

        # Log accuracy vs planned price
        price_diff_pct = ((actual_price_per_unit - action.price_per_unit) / action.price_per_unit * 100) if action.price_per_unit > 0 else 0
        if abs(price_diff_pct) > 5:
            self.logger.warning(f"  ⚠️  Actual price {actual_price_per_unit:,} vs planned {action.price_per_unit:,} ({price_diff_pct:+.1f}%)")

        self.logger.info(f"  ✅ Sold {action.units}x {action.good} for {total_revenue:,} credits")
        return True, total_revenue

    def _execute_single_purchase(self, waypoint: str, good: str, units: int) -> tuple[bool, int]:
        """Execute a single purchase transaction"""
        result = self.ship.buy(good, units, check_market_prices=False)
        if not result:
            return False, 0

        total_cost = result['totalPrice']
        actual_price_per_unit = result['pricePerUnit']

        # Update database with actual transaction price
        update_market_price_from_transaction(
            self.db,
            waypoint,
            good,
            'PURCHASE',
            actual_price_per_unit,
            self.logger
        )

        return True, total_cost

    def _has_cargo_space(self, units_needed: int) -> bool:
        """Check if ship has enough cargo space"""
        ship_data = self.ship.get_status()
        if not ship_data:
            return False

        current_cargo_units = sum(item['units'] for item in ship_data['cargo']['inventory'])
        cargo_available = ship_data['cargo']['capacity'] - current_cargo_units
        return cargo_available >= units_needed

    def _log_batch_sizing(self, price_per_unit: int, batch_size: int) -> None:
        """Log batch sizing decision with rationale"""
        if price_per_unit >= 2000:
            self.logger.info(f"  📊 Dynamic batch size: {batch_size} units (high-value good ≥2000 cr/unit - minimal risk strategy)")
        elif price_per_unit >= 1500:
            self.logger.info(f"  📊 Dynamic batch size: {batch_size} units (medium-high value ≥1500 cr/unit - cautious approach)")
        elif price_per_unit >= 50:
            self.logger.info(f"  📊 Dynamic batch size: {batch_size} units (standard good ≥50 cr/unit - default batching)")
        else:
            self.logger.info(f"  📊 Dynamic batch size: {batch_size} units (bulk good <50 cr/unit - efficiency mode)")
