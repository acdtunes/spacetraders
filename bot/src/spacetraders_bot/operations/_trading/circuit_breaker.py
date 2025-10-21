"""
Circuit Breaker Service

Validates trade profitability before execution to prevent unprofitable purchases.
Implements the circuit breaker pattern for trading operations.
"""

import logging
from typing import Dict, Optional, Tuple

from spacetraders_bot.core.api_client import APIClient
from spacetraders_bot.operations._trading.models import MultiLegRoute, TradeAction
from spacetraders_bot.operations._trading.market_service import (
    estimate_sell_price_with_degradation,
    find_planned_sell_price
)


class ProfitabilityValidator:
    """Validates trade profitability before executing purchases"""

    def __init__(self, api: APIClient, logger: logging.Logger):
        self.api = api
        self.logger = logger

    def validate_purchase_profitability(
        self,
        action: TradeAction,
        route: MultiLegRoute,
        segment_index: int,
        system: str
    ) -> Tuple[bool, Optional[str]]:
        """
        Validate that a purchase action will be profitable

        Args:
            action: Purchase action to validate
            route: Full route for finding planned sell price
            segment_index: Current segment index
            system: System symbol for API calls

        Returns:
            Tuple of (is_profitable, error_message)
            - is_profitable: True if purchase is validated as profitable
            - error_message: Description of why purchase was blocked, or None
        """
        # Get fresh market data
        try:
            live_market = self.api.get_market(system, action.waypoint)
            if not live_market:
                return False, "Market data unavailable"

            # Extract current buy price from market data
            live_buy_price = None
            for good in live_market.get('tradeGoods', []):
                if good['symbol'] == action.good:
                    live_buy_price = good.get('sellPrice')  # What we pay
                    break

            if live_buy_price is None:
                return False, "No live price data for good"

            # Calculate price change
            price_change_pct = ((live_buy_price - action.price_per_unit) / action.price_per_unit) * 100 if action.price_per_unit > 0 else 0

            # Log price changes
            if abs(price_change_pct) > 5:
                self.logger.warning(f"  ⚠️  Price changed: {action.price_per_unit:,} → {live_buy_price:,} ({price_change_pct:+.1f}%)")

            # Extreme volatility warning
            if price_change_pct > 30:
                self.logger.warning("=" * 70)
                self.logger.warning("⚠️  HIGH PRICE VOLATILITY DETECTED")
                self.logger.warning("=" * 70)
                self.logger.warning(f"  Planned: {action.price_per_unit:,} cr/unit")
                self.logger.warning(f"  Current: {live_buy_price:,} cr/unit")
                self.logger.warning(f"  Increase: {price_change_pct:.1f}%")
                self.logger.warning(f"  Checking profitability before proceeding...")
                self.logger.warning("=" * 70)

            # Check profitability using planned sell price
            planned_sell_price = find_planned_sell_price(action.good, route, segment_index)
            if not planned_sell_price:
                # No planned sell price - abort if extreme volatility
                if price_change_pct > 50:
                    self.logger.error("=" * 70)
                    self.logger.error("🚨 CIRCUIT BREAKER: EXTREME VOLATILITY + NO SELL PRICE!")
                    self.logger.error("=" * 70)
                    self.logger.error(f"  Price spike: {price_change_pct:.1f}%")
                    self.logger.error(f"  No planned sell action found for {action.good}")
                    self.logger.error(f"  Cannot validate trade profitability")
                    self.logger.error(f"  🛡️  PURCHASE BLOCKED - Too risky without sell price")
                    self.logger.error("=" * 70)
                    return False, f"Extreme volatility ({price_change_pct:.1f}%) with no planned sell price"
                # Moderate volatility without sell price - allow (might be intermediate good)
                return True, None

            # Apply degradation model to expected sell price
            expected_sell_price = estimate_sell_price_with_degradation(planned_sell_price, action.units)

            # Check if purchase would be unprofitable
            if live_buy_price >= expected_sell_price:
                self.logger.error("=" * 70)
                self.logger.error("🚨 CIRCUIT BREAKER: UNPROFITABLE TRADE DETECTED!")
                self.logger.error("=" * 70)
                self.logger.error(f"  Good: {action.good}")
                self.logger.error(f"  Live buy price: {live_buy_price:,} cr/unit")
                self.logger.error(f"  Planned sell price (cached): {planned_sell_price:,} cr/unit")
                self.logger.error(f"  Expected sell price (w/ degradation): {expected_sell_price:,} cr/unit")
                if action.units > 20:
                    degradation_pct = ((planned_sell_price - expected_sell_price) / planned_sell_price) * 100
                    self.logger.error(f"  Price degradation: -{degradation_pct:.1f}% ({action.units} units)")
                self.logger.error(f"  Would lose: {live_buy_price - expected_sell_price:,} cr/unit")
                self.logger.error(f"  🛡️  PURCHASE BLOCKED - No credits spent")
                self.logger.error("=" * 70)
                return False, f"Unprofitable: buy={live_buy_price:,} >= sell={expected_sell_price:,}"

            # Profitability check passed
            profit_margin = expected_sell_price - live_buy_price
            profit_margin_pct = (profit_margin / expected_sell_price) * 100
            if price_change_pct > 30:
                self.logger.info(f"  ✅ Trade still profitable despite price spike: +{profit_margin:,} cr/unit ({profit_margin_pct:.1f}% margin)")
            else:
                self.logger.info(f"  ✅ Trade profitable: +{profit_margin:,} cr/unit ({profit_margin_pct:.1f}% margin)")

            return True, None

        except Exception as e:
            self.logger.error("=" * 70)
            self.logger.error("🚨 CIRCUIT BREAKER: MARKET API FAILURE!")
            self.logger.error("=" * 70)
            self.logger.error(f"  Error: {e}")
            self.logger.error(f"  Unable to verify price for {action.good} at {action.waypoint}")
            self.logger.error(f"  🛡️  PURCHASE BLOCKED - Cannot validate price safety")
            self.logger.error("=" * 70)
            return False, f"Market API failure: {str(e)}"


def calculate_batch_size(price_per_unit: int) -> int:
    """
    Calculate optimal batch size based on good value

    Higher value goods = smaller batches (detect spikes earlier, minimize risk)
    Lower value goods = larger batches (reduce API overhead)

    Args:
        price_per_unit: Price per unit in credits

    Returns:
        Optimal batch size (2-10 units)
    """
    if price_per_unit >= 2000:
        return 2  # High-value: 2-unit batches (minimal risk)
    elif price_per_unit >= 1500:
        return 3  # Medium-high value: 3-unit batches
    elif price_per_unit >= 50:
        return 5  # Standard: 5-unit batches (default for most goods)
    else:
        return 10  # Very low-value: 10-unit batches (bulk efficiency)
