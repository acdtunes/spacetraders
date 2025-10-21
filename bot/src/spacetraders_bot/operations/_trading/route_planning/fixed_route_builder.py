"""
Fixed Route Builder

Single Responsibility: Build simple fixed buy→sell routes without optimization.

This module provides prescriptive trading route construction where the user
specifies exact buy and sell waypoints, without greedy optimization.
"""

import logging
from typing import Optional, Dict, Tuple

from spacetraders_bot.core.utils import calculate_distance
from spacetraders_bot.operations._trading.models import RouteSegment, MultiLegRoute, TradeAction


class FixedRouteBuilder:
    """
    Builder for simple fixed 2-stop routes (buy → sell)

    Used for prescriptive trading mode where user specifies:
    - Buy waypoint
    - Sell waypoint
    - Good to trade

    No optimization, just route construction and validation.
    """

    def __init__(self, db, logger: Optional[logging.Logger] = None):
        """
        Initialize fixed route builder

        Args:
            db: Database instance
            logger: Optional logger (creates default if not provided)
        """
        self.db = db
        self.logger = logger or logging.getLogger(__name__)

    def build_route(
        self,
        current_waypoint: str,
        buy_waypoint: str,
        sell_waypoint: str,
        good: str,
        cargo_capacity: int,
        starting_credits: int,
        ship_speed: int,
    ) -> Optional[MultiLegRoute]:
        """
        Build a fixed buy→sell route

        Args:
            current_waypoint: Starting location
            buy_waypoint: Where to buy the good
            sell_waypoint: Where to sell the good
            good: Trade good symbol
            cargo_capacity: Ship cargo capacity
            starting_credits: Available credits
            ship_speed: Ship speed for time estimation

        Returns:
            MultiLegRoute or None if route is not viable
        """
        self.logger.info("="*70)
        self.logger.info("CREATING FIXED ROUTE")
        self.logger.info("="*70)
        self.logger.info(f"Route: {current_waypoint} → {buy_waypoint} → {sell_waypoint}")
        self.logger.info(f"Good: {good}")
        self.logger.info(f"Cargo capacity: {cargo_capacity}")

        # Get market data from database
        with self.db.transaction() as conn:
            buy_market_rows = self.db.get_market_data(conn, buy_waypoint, good)
            sell_market_rows = self.db.get_market_data(conn, sell_waypoint, good)

        # Extract first row (get_market_data returns List[Dict])
        buy_market = buy_market_rows[0] if buy_market_rows else None
        sell_market = sell_market_rows[0] if sell_market_rows else None

        if not buy_market or not sell_market:
            self.logger.error("Missing market data for route")
            return None

        buy_price = buy_market['sell_price']  # What we pay
        sell_price = sell_market['purchase_price']  # What we receive
        trade_volume = buy_market.get('trade_volume', cargo_capacity)

        self.logger.info(f"Buy price @ {buy_waypoint}: {buy_price:,} cr/unit")
        self.logger.info(f"Sell price @ {sell_waypoint}: {sell_price:,} cr/unit")
        self.logger.info(f"Spread: {sell_price - buy_price:,} cr/unit")

        # Get waypoint coordinates
        current_coords = self._get_waypoint_coordinates(current_waypoint)
        buy_coords = self._get_waypoint_coordinates(buy_waypoint)
        sell_coords = self._get_waypoint_coordinates(sell_waypoint)

        # Validate that we have all coordinates
        if not current_coords or not buy_coords or not sell_coords:
            missing = []
            if not current_coords:
                missing.append(current_waypoint)
            if not buy_coords:
                missing.append(buy_waypoint)
            if not sell_coords:
                missing.append(sell_waypoint)
            self.logger.error(f"Missing waypoint coordinate data for: {', '.join(missing)}")
            return None

        # Calculate distances
        dist_to_buy = calculate_distance(current_coords, buy_coords)
        dist_buy_to_sell = calculate_distance(buy_coords, sell_coords)
        total_distance = dist_to_buy + dist_buy_to_sell

        # Estimate fuel costs
        fuel_to_buy = int(dist_to_buy * 1.1)  # CRUISE mode estimate
        fuel_buy_to_sell = int(dist_buy_to_sell * 1.1)
        total_fuel = fuel_to_buy + fuel_buy_to_sell

        # Calculate units to buy
        units_to_buy = self._calculate_purchase_units(
            cargo_capacity, starting_credits, buy_price, trade_volume
        )

        if units_to_buy <= 0:
            self.logger.error("Cannot afford any units")
            return None

        # Calculate profitability
        purchase_cost = units_to_buy * buy_price
        sale_revenue = units_to_buy * sell_price
        estimated_fuel_cost = total_fuel * 1  # Rough estimate: 1 cr/fuel
        profit = sale_revenue - purchase_cost - estimated_fuel_cost

        self.logger.info(f"Units to trade: {units_to_buy}")
        self.logger.info(f"Purchase cost: {purchase_cost:,}")
        self.logger.info(f"Sale revenue: {sale_revenue:,}")
        self.logger.info(f"Estimated profit: {profit:,}")

        if profit <= 0:
            self.logger.warning("Route not profitable based on current market data")
            return None

        # Build route segments
        if current_waypoint == buy_waypoint:
            # Ship is already at buy market - create single bundled segment
            segments = [self._build_single_segment_route(
                buy_waypoint, sell_waypoint, good,
                units_to_buy, buy_price, sell_price,
                dist_buy_to_sell, fuel_buy_to_sell,
                purchase_cost, sale_revenue,
                starting_credits, profit
            )]
        else:
            # Ship needs to navigate to buy market first - create 2 segments
            segments = self._build_two_segment_route(
                current_waypoint, buy_waypoint, sell_waypoint, good,
                units_to_buy, buy_price, sell_price,
                dist_to_buy, dist_buy_to_sell,
                fuel_to_buy, fuel_buy_to_sell,
                purchase_cost, sale_revenue,
                starting_credits, profit
            )

        route = MultiLegRoute(
            segments=segments,
            total_profit=profit,
            total_distance=total_distance,
            total_fuel_cost=total_fuel,
            estimated_time_minutes=int((total_distance / ship_speed) * 4)  # Rough estimate
        )

        self.logger.info("="*70)
        self.logger.info("FIXED ROUTE CREATED")
        self.logger.info("="*70)
        self.logger.info(f"Segments: {len(segments)}")
        self.logger.info(f"Total distance: {total_distance} units")
        self.logger.info(f"Estimated profit: {profit:,} credits")
        self.logger.info("="*70)

        return route

    def _get_waypoint_coordinates(self, waypoint: str) -> Optional[Dict[str, int]]:
        """
        Fetch waypoint coordinates from database

        Args:
            waypoint: Waypoint symbol

        Returns:
            Dictionary with 'x' and 'y' coordinates, or None if not found
        """
        with self.db.connection() as conn:
            cursor = conn.cursor()
            cursor.execute(
                "SELECT x, y FROM waypoints WHERE waypoint_symbol = ?",
                (waypoint,)
            )
            row = cursor.fetchone()
            return {'x': row[0], 'y': row[1]} if row else None

    def _calculate_purchase_units(
        self,
        cargo_capacity: int,
        starting_credits: int,
        buy_price: int,
        trade_volume: Optional[int]
    ) -> int:
        """
        Calculate optimal units to purchase

        Limited by:
        - Cargo capacity
        - Available credits (use 85% to leave buffer)
        - Trade volume (market transaction limit)

        Args:
            cargo_capacity: Ship cargo capacity
            starting_credits: Available credits
            buy_price: Price per unit
            trade_volume: Market transaction limit (None = unlimited)

        Returns:
            Units to purchase
        """
        max_by_credits = int((starting_credits * 0.85) / buy_price) if buy_price > 0 else cargo_capacity
        return min(cargo_capacity, max_by_credits, trade_volume or cargo_capacity)

    def _build_single_segment_route(
        self,
        buy_waypoint: str,
        sell_waypoint: str,
        good: str,
        units: int,
        buy_price: int,
        sell_price: int,
        distance: float,
        fuel_cost: int,
        purchase_cost: int,
        sale_revenue: int,
        starting_credits: int,
        profit: int
    ) -> RouteSegment:
        """
        Build route when ship is already at buy market

        Creates single segment with bundled BUY + SELL actions.

        Args:
            buy_waypoint: Buy market waypoint
            sell_waypoint: Sell market waypoint
            good: Trade good symbol
            units: Units to trade
            buy_price: Buy price per unit
            sell_price: Sell price per unit
            distance: Distance from buy to sell
            fuel_cost: Fuel cost estimate
            purchase_cost: Total purchase cost
            sale_revenue: Total sale revenue
            starting_credits: Starting credits
            profit: Net profit

        Returns:
            RouteSegment with BUY and SELL actions
        """
        return RouteSegment(
            from_waypoint=buy_waypoint,
            to_waypoint=sell_waypoint,
            distance=distance,
            fuel_cost=fuel_cost,
            actions_at_destination=[
                TradeAction(
                    waypoint=buy_waypoint,
                    good=good,
                    action='BUY',
                    units=units,
                    price_per_unit=buy_price,
                    total_value=purchase_cost
                ),
                TradeAction(
                    waypoint=sell_waypoint,
                    good=good,
                    action='SELL',
                    units=units,
                    price_per_unit=sell_price,
                    total_value=sale_revenue
                )
            ],
            cargo_after={},
            credits_after=starting_credits - purchase_cost + sale_revenue,
            cumulative_profit=profit
        )

    def _build_two_segment_route(
        self,
        current_waypoint: str,
        buy_waypoint: str,
        sell_waypoint: str,
        good: str,
        units: int,
        buy_price: int,
        sell_price: int,
        dist_to_buy: float,
        dist_buy_to_sell: float,
        fuel_to_buy: int,
        fuel_buy_to_sell: int,
        purchase_cost: int,
        sale_revenue: int,
        starting_credits: int,
        profit: int
    ) -> list[RouteSegment]:
        """
        Build route when ship needs to navigate to buy market first

        Creates two segments:
        1. Current → Buy market (with BUY action)
        2. Buy market → Sell market (with SELL action)

        Args:
            current_waypoint: Current location
            buy_waypoint: Buy market waypoint
            sell_waypoint: Sell market waypoint
            good: Trade good symbol
            units: Units to trade
            buy_price: Buy price per unit
            sell_price: Sell price per unit
            dist_to_buy: Distance to buy market
            dist_buy_to_sell: Distance from buy to sell
            fuel_to_buy: Fuel cost to buy market
            fuel_buy_to_sell: Fuel cost from buy to sell
            purchase_cost: Total purchase cost
            sale_revenue: Total sale revenue
            starting_credits: Starting credits
            profit: Net profit

        Returns:
            List of 2 RouteSegments
        """
        # Segment 1: Current → Buy market
        segment1 = RouteSegment(
            from_waypoint=current_waypoint,
            to_waypoint=buy_waypoint,
            distance=dist_to_buy,
            fuel_cost=fuel_to_buy,
            actions_at_destination=[
                TradeAction(
                    waypoint=buy_waypoint,
                    good=good,
                    action='BUY',
                    units=units,
                    price_per_unit=buy_price,
                    total_value=purchase_cost
                )
            ],
            cargo_after={good: units},
            credits_after=starting_credits - purchase_cost,
            cumulative_profit=-fuel_to_buy  # Just fuel cost for segment 1
        )

        # Segment 2: Buy market → Sell market
        segment2 = RouteSegment(
            from_waypoint=buy_waypoint,
            to_waypoint=sell_waypoint,
            distance=dist_buy_to_sell,
            fuel_cost=fuel_buy_to_sell,
            actions_at_destination=[
                TradeAction(
                    waypoint=sell_waypoint,
                    good=good,
                    action='SELL',
                    units=units,
                    price_per_unit=sell_price,
                    total_value=sale_revenue
                )
            ],
            cargo_after={},
            credits_after=starting_credits - purchase_cost + sale_revenue,
            cumulative_profit=profit
        )

        return [segment1, segment2]


def create_fixed_route(
    api,
    db,
    player_id: int,
    current_waypoint: str,
    buy_waypoint: str,
    sell_waypoint: str,
    good: str,
    cargo_capacity: int,
    starting_credits: int,
    ship_speed: int,
    fuel_capacity: int,
    current_fuel: int,
    logger: Optional[logging.Logger] = None
) -> Optional[MultiLegRoute]:
    """
    Create a fixed 2-stop route (buy → sell) without optimization

    This is the prescriptive mode for single-leg trading.
    Builds a simple buy-at-X, sell-at-Y route without greedy optimization.

    Legacy function wrapper for backward compatibility.
    Delegates to FixedRouteBuilder.build_route()

    Args:
        api: APIClient instance (unused, kept for signature compatibility)
        db: Database instance
        player_id: Player ID (unused, kept for signature compatibility)
        current_waypoint: Starting location
        buy_waypoint: Where to buy the good
        sell_waypoint: Where to sell the good
        good: Trade good symbol
        cargo_capacity: Ship cargo capacity
        starting_credits: Available credits
        ship_speed: Ship speed for time estimation
        fuel_capacity: Fuel tank capacity (unused, kept for signature compatibility)
        current_fuel: Current fuel level (unused, kept for signature compatibility)
        logger: Optional logger (creates default if not provided)

    Returns:
        MultiLegRoute or None if route is not viable
    """
    builder = FixedRouteBuilder(db, logger)
    return builder.build_route(
        current_waypoint=current_waypoint,
        buy_waypoint=buy_waypoint,
        sell_waypoint=sell_waypoint,
        good=good,
        cargo_capacity=cargo_capacity,
        starting_credits=starting_credits,
        ship_speed=ship_speed,
    )
