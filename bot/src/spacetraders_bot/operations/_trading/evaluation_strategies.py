"""
Trade Evaluation Strategies

Defines strategies for evaluating trading opportunities at markets.
Single Responsibility: Market evaluation logic for route planning.
"""

import logging
from typing import Dict, List, Tuple

from spacetraders_bot.operations._trading.models import TradeAction, MarketEvaluation
from spacetraders_bot.operations._trading.market_service import estimate_sell_price_with_degradation


class TradeEvaluationStrategy:
    """Abstract base class for market evaluation strategies"""

    def evaluate(
        self,
        *,
        market: str,
        current_cargo: Dict[str, int],
        current_credits: int,
        trade_opportunities: List[Dict],
        cargo_capacity: int,
        fuel_cost: int,
    ) -> MarketEvaluation:
        """
        Evaluate potential actions at a market

        Args:
            market: Waypoint symbol of market to evaluate
            current_cargo: Current cargo state {good: units}
            current_credits: Available credits
            trade_opportunities: List of trade opportunity dicts
            cargo_capacity: Ship cargo capacity
            fuel_cost: Estimated fuel cost to reach market

        Returns:
            MarketEvaluation with actions, cargo_after, credits_after, net_profit
        """
        raise NotImplementedError


class ProfitFirstStrategy(TradeEvaluationStrategy):
    """
    Default strategy that maximizes profit by mixing sell/buy actions

    Strategy:
    1. Sell all profitable cargo at this market
    2. Buy goods for future markets using freed credits
    3. Calculate net profit including fuel costs
    """

    def __init__(self, logger: logging.Logger):
        self.logger = logger

    def evaluate(
        self,
        *,
        market: str,
        current_cargo: Dict[str, int],
        current_credits: int,
        trade_opportunities: List[Dict],
        cargo_capacity: int,
        fuel_cost: int,
    ) -> MarketEvaluation:
        """
        Evaluate market using profit-first strategy

        Returns actions that maximize net profit at this market
        """
        # Step 1: Apply sell actions (free up cargo space and get credits)
        sell_actions, cargo_after_sell, credits_after_sell, revenue = self._apply_sell_actions(
            market=market,
            current_cargo=current_cargo,
            current_credits=current_credits,
            trade_opportunities=trade_opportunities,
        )

        # Step 2: Apply buy actions (use freed credits for future profit)
        buy_actions, cargo_after_buy, credits_after_buy, purchase_costs = self._apply_buy_actions(
            market=market,
            trade_opportunities=trade_opportunities,
            cargo=cargo_after_sell,
            credits=credits_after_sell,
            cargo_capacity=cargo_capacity,
            fuel_cost=fuel_cost,
        )

        # Step 3: Estimate potential future revenue from new cargo
        potential_future_revenue = self._estimate_potential_future_revenue(
            cargo_after_buy,
            market,
            trade_opportunities,
        )

        # Step 4: Calculate net profit
        net_profit = revenue - fuel_cost + (potential_future_revenue - purchase_costs)

        return MarketEvaluation(
            actions=sell_actions + buy_actions,
            cargo_after=cargo_after_buy,
            credits_after=credits_after_buy,
            net_profit=net_profit,
        )

    def _apply_sell_actions(
        self,
        *,
        market: str,
        current_cargo: Dict[str, int],
        current_credits: int,
        trade_opportunities: List[Dict],
    ) -> Tuple[List[TradeAction], Dict[str, int], int, int]:
        """
        Generate sell actions for cargo we're carrying

        Returns:
            Tuple of (actions, cargo_after, credits_after, revenue)
        """
        actions: List[TradeAction] = []
        cargo = current_cargo.copy()
        credits = current_credits
        revenue = 0

        for good, units in list(cargo.items()):
            # Find sell opportunity for this good at this market
            sell_opp = next(
                (o for o in trade_opportunities if o['sell_waypoint'] == market and o['good'] == good),
                None
            )
            if not sell_opp:
                continue

            sell_price = sell_opp['sell_price']
            trade_volume = sell_opp['trade_volume']

            # Limit units to trade volume
            units_to_sell = min(units, trade_volume)
            if units_to_sell <= 0:
                continue

            # Apply price degradation model for batch selling
            effective_sell_price = estimate_sell_price_with_degradation(sell_price, units_to_sell)
            sale_value = units_to_sell * effective_sell_price

            self.logger.debug(
                "Sell %s: %s units @ %s (cached: %s, degradation: %.1f%%) = %s",
                good,
                units_to_sell,
                effective_sell_price,
                sell_price,
                ((sell_price - effective_sell_price) / sell_price * 100) if sell_price > 0 else 0,
                sale_value,
            )

            actions.append(TradeAction(
                waypoint=market,
                good=good,
                action='SELL',
                units=units_to_sell,
                price_per_unit=effective_sell_price,
                total_value=sale_value,
            ))

            credits += sale_value
            revenue += sale_value
            cargo[good] -= units_to_sell
            if cargo[good] == 0:
                del cargo[good]

        return actions, cargo, credits, revenue

    def _apply_buy_actions(
        self,
        *,
        market: str,
        trade_opportunities: List[Dict],
        cargo: Dict[str, int],
        credits: int,
        cargo_capacity: int,
        fuel_cost: int = 0,
    ) -> Tuple[List[TradeAction], Dict[str, int], int, int]:
        """
        Generate buy actions for goods we can afford

        Returns:
            Tuple of (actions, cargo_after, credits_after, purchase_cost)
        """
        actions: List[TradeAction] = []
        updated_cargo = cargo.copy()
        updated_credits = credits
        purchase_cost = 0

        cargo_used = sum(updated_cargo.values())
        cargo_available = cargo_capacity - cargo_used

        for opp in trade_opportunities:
            if opp['buy_waypoint'] != market:
                continue

            if cargo_available <= 0 or updated_credits <= fuel_cost:
                break

            good = opp['good']
            buy_price = opp['buy_price']
            trade_volume = opp['trade_volume']

            if buy_price <= 0:
                continue

            # Calculate max affordable units (reserve fuel cost)
            credits_available_for_purchase = updated_credits - fuel_cost
            max_affordable = min(credits_available_for_purchase // buy_price, cargo_available, trade_volume)
            if max_affordable <= 0:
                continue

            purchase_value = max_affordable * buy_price
            actions.append(TradeAction(
                waypoint=market,
                good=good,
                action='BUY',
                units=max_affordable,
                price_per_unit=buy_price,
                total_value=purchase_value,
            ))

            updated_credits -= purchase_value
            updated_cargo[good] = updated_cargo.get(good, 0) + max_affordable
            cargo_available -= max_affordable
            purchase_cost += purchase_value

        return actions, updated_cargo, updated_credits, purchase_cost

    def _estimate_potential_future_revenue(
        self,
        cargo: Dict[str, int],
        current_market: str,
        trade_opportunities: List[Dict],
    ) -> int:
        """
        Estimate revenue from selling cargo at best future markets

        Args:
            cargo: Cargo we're carrying after buy actions
            current_market: Current market (exclude from future markets)
            trade_opportunities: All trade opportunities

        Returns:
            Estimated future revenue
        """
        potential_revenue = 0

        for good, units in cargo.items():
            # Find best sell price at markets other than current
            best_sell = max(
                (
                    o['sell_price']
                    for o in trade_opportunities
                    if o['good'] == good and o['sell_waypoint'] != current_market
                ),
                default=0,
            )

            # Apply degradation to future sell price estimate
            effective_sell_price = estimate_sell_price_with_degradation(best_sell, units)
            potential_revenue += units * effective_sell_price

        return potential_revenue
