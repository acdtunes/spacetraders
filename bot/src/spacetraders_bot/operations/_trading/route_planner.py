"""
Route Planning

Multi-leg trading route optimization using greedy search strategies.
Single Responsibility: Plan optimal trading routes across multiple waypoints.
"""

import logging
from datetime import datetime, timezone
from typing import Callable, Dict, List, Optional, Tuple

from spacetraders_bot.core.api_client import APIClient
from spacetraders_bot.core.utils import calculate_distance
from spacetraders_bot.operations._trading.models import RouteSegment, MultiLegRoute, TradeAction
from spacetraders_bot.operations._trading.market_repository import MarketRepository
from spacetraders_bot.operations._trading.evaluation_strategies import (
    TradeEvaluationStrategy,
    ProfitFirstStrategy
)


class GreedyRoutePlanner:
    """
    Greedy route planning algorithm for multi-leg trading

    Strategy:
    1. Start at current location
    2. Find best next market based on evaluation strategy
    3. Generate trade actions (sell current cargo, buy new goods)
    4. Repeat until max stops reached or no profitable moves
    """

    def __init__(
        self,
        logger: logging.Logger,
        db,
        strategy: Optional[TradeEvaluationStrategy] = None
    ):
        self.logger = logger
        self.db = db
        self.market_repo = MarketRepository(db)
        self.strategy = strategy or ProfitFirstStrategy(logger)

    def find_route(
        self,
        start_waypoint: str,
        markets: List[str],
        trade_opportunities: List[Dict],
        max_stops: int,
        cargo_capacity: int,
        starting_credits: int,
        ship_speed: int,
        starting_cargo: Optional[Dict[str, int]] = None,
    ) -> Optional[MultiLegRoute]:
        """
        Find optimal multi-leg trading route using greedy search

        Args:
            start_waypoint: Starting location
            markets: List of market waypoints to consider
            trade_opportunities: List of trade opportunity dicts
            max_stops: Maximum number of stops
            cargo_capacity: Ship cargo capacity
            starting_credits: Available credits
            ship_speed: Ship speed (for time estimation)
            starting_cargo: Existing cargo from previous operations

        Returns:
            MultiLegRoute or None if no profitable route found
        """
        current_waypoint = start_waypoint
        current_cargo: Dict[str, int] = starting_cargo.copy() if starting_cargo else {}
        current_credits = starting_credits
        cumulative_profit = 0
        route_segments: List[RouteSegment] = []
        visited = set()
        pending_actions = []  # Actions accumulated from zero-distance moves
        segments_created = 0

        # Use while loop to not count zero-distance iterations against max_stops
        while segments_created < max_stops:
            next_step = self._find_best_next_market(
                current_waypoint=current_waypoint,
                current_cargo=current_cargo,
                current_credits=current_credits,
                markets=markets,
                trade_opportunities=trade_opportunities,
                cargo_capacity=cargo_capacity,
                visited=visited,
            )

            if not next_step:
                break

            next_waypoint, actions, new_cargo, new_credits, segment_profit, distance = next_step

            # If best next market is current location (distance 0), accumulate actions
            if distance == 0 and current_waypoint == next_waypoint:
                # Accumulate actions from current location
                pending_actions.extend(actions)
                current_cargo = new_cargo
                current_credits = new_credits
                cumulative_profit += segment_profit
                visited.add(current_waypoint)
                continue  # Don't increment segments_created

            # Create segment with pending actions + new actions
            all_actions = pending_actions + actions
            pending_actions = []  # Clear pending for next segment

            segment = RouteSegment(
                from_waypoint=current_waypoint,
                to_waypoint=next_waypoint,
                distance=distance,
                fuel_cost=int(distance * 1.1),
                actions_at_destination=all_actions,
                cargo_after=new_cargo.copy(),
                credits_after=new_credits,
                cumulative_profit=cumulative_profit + segment_profit,
            )

            route_segments.append(segment)
            visited.add(current_waypoint)

            current_waypoint = next_waypoint
            current_cargo = new_cargo
            current_credits = new_credits
            cumulative_profit += segment_profit
            segments_created += 1  # Only count actual segments

        if not route_segments:
            return None

        total_distance = sum(s.distance for s in route_segments)
        total_fuel_cost = sum(s.fuel_cost for s in route_segments)
        total_profit = cumulative_profit - total_fuel_cost
        estimated_time_minutes = (total_distance / ship_speed) * 60

        return MultiLegRoute(
            segments=route_segments,
            total_profit=total_profit,
            total_distance=total_distance,
            total_fuel_cost=total_fuel_cost,
            estimated_time_minutes=estimated_time_minutes,
        )

    def _find_best_next_market(
        self,
        current_waypoint: str,
        current_cargo: Dict[str, int],
        current_credits: int,
        markets: List[str],
        trade_opportunities: List[Dict],
        cargo_capacity: int,
        visited: set,
    ) -> Optional[Tuple]:
        """
        Find the most profitable next market to visit

        Returns:
            Tuple of (next_waypoint, actions, new_cargo, new_credits, net_profit, distance)
            or None if no profitable option found
        """
        best_option = None
        best_profit = 0
        current_location_option = None
        current_location_profit = 0

        for next_market in markets:
            if next_market in visited:
                continue

            distance = self.market_repo.calculate_distance(current_waypoint, next_market)
            fuel_cost = int(distance * 1.1)

            evaluation = self.strategy.evaluate(
                market=next_market,
                current_cargo=current_cargo,
                current_credits=current_credits,
                trade_opportunities=trade_opportunities,
                cargo_capacity=cargo_capacity,
                fuel_cost=fuel_cost,
            )

            actions = evaluation.actions
            new_cargo = evaluation.cargo_after
            new_credits = evaluation.credits_after
            net_profit = evaluation.net_profit

            if net_profit > best_profit:
                best_profit = net_profit
                best_option = (next_market, actions, new_cargo, new_credits, net_profit, distance)

            # Track best option at current location separately
            if distance == 0 and next_market == current_waypoint and net_profit > current_location_profit:
                current_location_profit = net_profit
                current_location_option = (next_market, actions, new_cargo, new_credits, net_profit, distance)

        # Prefer current location if its profit is within 65% of best
        # This encourages chaining opportunities that start here
        if current_location_option and current_location_profit >= best_profit * 0.65:
            return current_location_option

        return best_option


class MultiLegTradeOptimizer:
    """
    High-level optimizer for finding optimal multi-leg trading routes

    Coordinates database queries, strategy selection, and route planning
    to find the most profitable path through a system's markets.
    """

    def __init__(
        self,
        api: APIClient,
        db,
        player_id: int,
        logger: Optional[logging.Logger] = None,
        strategy_factory: Optional[Callable[[logging.Logger], TradeEvaluationStrategy]] = None,
    ):
        self.api = api
        self.db = db
        self.player_id = player_id
        self.logger = logger or logging.getLogger(__name__)
        self._strategy_factory = strategy_factory or (lambda log: ProfitFirstStrategy(log))

    def find_optimal_route(
        self,
        start_waypoint: str,
        system: str,
        max_stops: int,
        cargo_capacity: int,
        starting_credits: int,
        ship_speed: int,
        fuel_capacity: int,
        current_fuel: int,
        starting_cargo: Optional[Dict[str, int]] = None,
    ) -> Optional[MultiLegRoute]:
        """
        Find the most profitable multi-leg trade route

        Args:
            start_waypoint: Current location
            system: System symbol (e.g., "X1-JB26")
            max_stops: Maximum number of stops (3-5 recommended)
            cargo_capacity: Ship cargo capacity
            starting_credits: Available credits for purchases
            ship_speed: Ship speed (for time estimation)
            fuel_capacity: Fuel tank capacity
            current_fuel: Current fuel level
            starting_cargo: Existing cargo from previous operations

        Returns:
            MultiLegRoute with optimal path, or None if no profitable route found
        """
        self.logger.info("="*70)
        self.logger.info("MULTI-LEG ROUTE OPTIMIZATION")
        self.logger.info("="*70)
        self.logger.info(f"Start: {start_waypoint}")
        self.logger.info(f"Max stops: {max_stops}")
        self.logger.info(f"Cargo capacity: {cargo_capacity}")
        self.logger.info(f"Starting credits: {starting_credits:,}")
        self.logger.info("="*70)

        # Get all markets in system from database
        markets = self._get_markets_in_system(system)
        self.logger.info(f"Found {len(markets)} markets in {system}")

        if not markets:
            self.logger.error("No markets found in system")
            return None

        # Get all trade opportunities from database
        trade_opportunities = self._get_trade_opportunities(system, markets)
        self.logger.info(f"Found {len(trade_opportunities)} trade opportunities")

        # Create strategy and planner
        strategy = self._strategy_factory(self.logger)
        planner = GreedyRoutePlanner(self.logger, self.db, strategy=strategy)

        # Find best route
        best_route = planner.find_route(
            start_waypoint=start_waypoint,
            markets=markets,
            trade_opportunities=trade_opportunities,
            max_stops=max_stops,
            cargo_capacity=cargo_capacity,
            starting_credits=starting_credits,
            ship_speed=ship_speed,
            starting_cargo=starting_cargo,
        )

        if best_route:
            self._log_route_summary(best_route)
        else:
            self.logger.warning("No profitable multi-leg route found")

        return best_route

    def _get_markets_in_system(self, system: str) -> List[str]:
        """Get all market waypoints in a system from database"""
        with self.db.connection() as conn:
            cursor = conn.cursor()
            cursor.execute("""
                SELECT DISTINCT waypoint_symbol
                FROM market_data
                WHERE waypoint_symbol LIKE ?
                AND (updated_by_player = ? OR updated_by_player IS NULL)
            """, (f"{system}-%", self.player_id))

            return [row[0] for row in cursor.fetchall()]

    def _get_trade_opportunities(self, system: str, markets: List[str]) -> List[Dict]:
        """
        Get all profitable trade opportunities from database

        Returns list of dicts with:
        - buy_waypoint, sell_waypoint
        - good
        - buy_price (what we pay), sell_price (what we receive)
        - spread (profit per unit)
        - trade_volume (transaction limit)
        """
        opportunities = []

        with self.db.connection() as conn:
            for buy_market in markets:
                buy_data = self.db.get_market_data(conn, buy_market, None)
                opportunities.extend(
                    self._collect_opportunities_for_market(
                        conn, buy_market, buy_data, markets
                    )
                )

        # Sort by spread (most profitable first)
        opportunities.sort(key=lambda x: x['spread'], reverse=True)

        return opportunities

    def _collect_opportunities_for_market(
        self,
        conn,
        buy_market: str,
        buy_data: List[Dict],
        markets: List[str],
    ) -> List[Dict]:
        """Collect trade opportunities for a specific buy market"""
        opportunities = []

        for sell_market in markets:
            if sell_market == buy_market:
                continue

            for buy_record in buy_data:
                good = buy_record['good_symbol']
                buy_price = buy_record.get('sell_price')

                if not buy_price:
                    continue

                # Freshness check for buy market data
                if not self._is_market_data_fresh(buy_record, buy_market, good, 'buy'):
                    continue

                sell_data = self.db.get_market_data(conn, sell_market, good)
                if not sell_data:
                    continue

                sell_record = sell_data[0]
                sell_price = sell_record.get('purchase_price')

                if not sell_price:
                    continue

                # Freshness check for sell market data
                if not self._is_market_data_fresh(sell_record, sell_market, good, 'sell'):
                    continue

                spread = sell_price - buy_price
                if spread <= 0:
                    continue

                opportunities.append({
                    'buy_waypoint': buy_market,
                    'sell_waypoint': sell_market,
                    'good': good,
                    'buy_price': buy_price,
                    'sell_price': sell_price,
                    'spread': spread,
                    'trade_volume': buy_record.get('trade_volume', 100),
                })

        return opportunities

    def _is_market_data_fresh(
        self,
        record: Dict,
        waypoint: str,
        good: str,
        action_type: str
    ) -> bool:
        """
        Check if market data is fresh enough for trading

        Args:
            record: Market data record
            waypoint: Waypoint symbol
            good: Good symbol
            action_type: 'buy' or 'sell'

        Returns:
            True if data is fresh (<1 hour), False otherwise
        """
        last_updated = record.get('last_updated')
        if not last_updated:
            return True  # No timestamp, assume fresh

        try:
            timestamp = datetime.strptime(last_updated, '%Y-%m-%dT%H:%M:%S.%fZ').replace(tzinfo=timezone.utc)
            age_hours = (datetime.now(timezone.utc) - timestamp).total_seconds() / 3600

            if age_hours > 1.0:
                self.logger.warning(f"⚠️  Skipping stale {action_type} data: {waypoint} {good} ({age_hours:.1f}h old)")
                return False
            elif age_hours > 0.5:
                self.logger.info(f"  ⏰ Aging {action_type} data: {waypoint} {good} ({age_hours:.1f}h old)")

            return True
        except (ValueError, TypeError) as e:
            self.logger.warning(f"  ⚠️  Invalid timestamp for {waypoint} {good}: {e}")
            return True  # Assume fresh if timestamp parsing fails

    def _log_route_summary(self, route: MultiLegRoute) -> None:
        """Log detailed route summary"""
        self.logger.info("\n" + "="*70)
        self.logger.info("OPTIMAL ROUTE FOUND")
        self.logger.info("="*70)
        self.logger.info(f"Total profit: {route.total_profit:,} credits")
        self.logger.info(f"Total distance: {route.total_distance:.0f} units")
        self.logger.info(f"Estimated time: {route.estimated_time_minutes:.0f} minutes")
        self.logger.info(f"Stops: {len(route.segments)}")
        self.logger.info("\nRoute:")

        for i, segment in enumerate(route.segments, 1):
            self.logger.info(f"\n  Stop {i}: {segment.to_waypoint}")
            self.logger.info(f"    Distance: {segment.distance:.0f} units")
            self.logger.info(f"    Actions:")
            for action in segment.actions_at_destination:
                symbol = '💰' if action.action == 'BUY' else '💵'
                self.logger.info(f"      {symbol} {action.action} {action.units}x {action.good} @ {action.price_per_unit:,} = {action.total_value:,}")
            self.logger.info(f"    Cargo after: {segment.cargo_after}")
            self.logger.info(f"    Cumulative profit: {segment.cumulative_profit:,}")

        self.logger.info("="*70)


def create_fixed_route(
    api, db, player_id,
    current_waypoint,
    buy_waypoint,
    sell_waypoint,
    good,
    cargo_capacity,
    starting_credits,
    ship_speed,
    fuel_capacity,
    current_fuel,
    logger: Optional[logging.Logger] = None
) -> Optional[MultiLegRoute]:
    """
    Create a fixed 2-stop route (buy → sell) without optimization

    This is the prescriptive mode for single-leg trading.
    Builds a simple buy-at-X, sell-at-Y route without greedy optimization.

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
    if logger is None:
        logger = logging.getLogger(__name__)

    logger.info("="*70)
    logger.info("CREATING FIXED ROUTE")
    logger.info("="*70)
    logger.info(f"Route: {current_waypoint} → {buy_waypoint} → {sell_waypoint}")
    logger.info(f"Good: {good}")
    logger.info(f"Cargo capacity: {cargo_capacity}")

    # Get market data from database
    with db.transaction() as conn:
        buy_market_rows = db.get_market_data(conn, buy_waypoint, good)
        sell_market_rows = db.get_market_data(conn, sell_waypoint, good)

    # Extract first row (get_market_data returns List[Dict])
    buy_market = buy_market_rows[0] if buy_market_rows else None
    sell_market = sell_market_rows[0] if sell_market_rows else None

    if not buy_market or not sell_market:
        logger.error("Missing market data for route")
        return None

    buy_price = buy_market['sell_price']  # What we pay
    sell_price = sell_market['purchase_price']  # What we receive
    trade_volume = buy_market.get('trade_volume', cargo_capacity)

    logger.info(f"Buy price @ {buy_waypoint}: {buy_price:,} cr/unit")
    logger.info(f"Sell price @ {sell_waypoint}: {sell_price:,} cr/unit")
    logger.info(f"Spread: {sell_price - buy_price:,} cr/unit")

    # Look up waypoint coordinates from database
    with db.connection() as conn:
        cursor = conn.cursor()

        # Get coordinates for current waypoint
        cursor.execute("SELECT x, y FROM waypoints WHERE waypoint_symbol = ?", (current_waypoint,))
        current_coords_row = cursor.fetchone()

        # Get coordinates for buy waypoint
        cursor.execute("SELECT x, y FROM waypoints WHERE waypoint_symbol = ?", (buy_waypoint,))
        buy_coords_row = cursor.fetchone()

        # Get coordinates for sell waypoint
        cursor.execute("SELECT x, y FROM waypoints WHERE waypoint_symbol = ?", (sell_waypoint,))
        sell_coords_row = cursor.fetchone()

    # Validate that we have all coordinates
    if not current_coords_row or not buy_coords_row or not sell_coords_row:
        missing = []
        if not current_coords_row:
            missing.append(current_waypoint)
        if not buy_coords_row:
            missing.append(buy_waypoint)
        if not sell_coords_row:
            missing.append(sell_waypoint)
        logger.error(f"Missing waypoint coordinate data for: {', '.join(missing)}")
        return None

    # Convert database rows to coordinate dictionaries
    current_coords = {'x': current_coords_row[0], 'y': current_coords_row[1]}
    buy_coords = {'x': buy_coords_row[0], 'y': buy_coords_row[1]}
    sell_coords = {'x': sell_coords_row[0], 'y': sell_coords_row[1]}

    # Calculate distances using coordinate dictionaries
    dist_to_buy = calculate_distance(current_coords, buy_coords)
    dist_buy_to_sell = calculate_distance(buy_coords, sell_coords)
    total_distance = dist_to_buy + dist_buy_to_sell

    # Estimate fuel costs
    fuel_to_buy = int(dist_to_buy * 1.1)  # CRUISE mode estimate
    fuel_buy_to_sell = int(dist_buy_to_sell * 1.1)
    total_fuel = fuel_to_buy + fuel_buy_to_sell

    # Calculate units to buy (limited by credits and cargo)
    max_units_by_credits = int((starting_credits * 0.85) / buy_price) if buy_price > 0 else cargo_capacity
    units_to_buy = min(cargo_capacity, max_units_by_credits, trade_volume if trade_volume else cargo_capacity)

    if units_to_buy <= 0:
        logger.error("Cannot afford any units")
        return None

    purchase_cost = units_to_buy * buy_price
    sale_revenue = units_to_buy * sell_price
    estimated_fuel_cost = total_fuel * 1  # Rough estimate: 1 cr/fuel
    profit = sale_revenue - purchase_cost - estimated_fuel_cost

    logger.info(f"Units to trade: {units_to_buy}")
    logger.info(f"Purchase cost: {purchase_cost:,}")
    logger.info(f"Sale revenue: {sale_revenue:,}")
    logger.info(f"Estimated profit: {profit:,}")

    if profit <= 0:
        logger.warning("Route not profitable based on current market data")
        return None

    # Build route segments
    segments = []

    if current_waypoint == buy_waypoint:
        # Ship is already at buy market - create single bundled segment
        # BUY at buy_waypoint, navigate to sell_waypoint, SELL at sell_waypoint
        segments.append(RouteSegment(
            from_waypoint=buy_waypoint,
            to_waypoint=sell_waypoint,
            distance=dist_buy_to_sell,
            fuel_cost=fuel_buy_to_sell,
            actions_at_destination=[
                TradeAction(
                    waypoint=buy_waypoint,
                    good=good,
                    action='BUY',
                    units=units_to_buy,
                    price_per_unit=buy_price,
                    total_value=purchase_cost
                ),
                TradeAction(
                    waypoint=sell_waypoint,
                    good=good,
                    action='SELL',
                    units=units_to_buy,
                    price_per_unit=sell_price,
                    total_value=sale_revenue
                )
            ],
            cargo_after={},
            credits_after=starting_credits - purchase_cost + sale_revenue,
            cumulative_profit=profit
        ))
    else:
        # Ship needs to navigate to buy market first - create 2 segments
        # Segment 1: Current → Buy market
        segments.append(RouteSegment(
            from_waypoint=current_waypoint,
            to_waypoint=buy_waypoint,
            distance=dist_to_buy,
            fuel_cost=fuel_to_buy,
            actions_at_destination=[
                TradeAction(
                    waypoint=buy_waypoint,
                    good=good,
                    action='BUY',
                    units=units_to_buy,
                    price_per_unit=buy_price,
                    total_value=purchase_cost
                )
            ],
            cargo_after={good: units_to_buy},
            credits_after=starting_credits - purchase_cost,
            cumulative_profit=-fuel_to_buy  # Just fuel cost for segment 1
        ))

        # Segment 2: Buy market → Sell market
        segments.append(RouteSegment(
            from_waypoint=buy_waypoint,
            to_waypoint=sell_waypoint,
            distance=dist_buy_to_sell,
            fuel_cost=fuel_buy_to_sell,
            actions_at_destination=[
                TradeAction(
                    waypoint=sell_waypoint,
                    good=good,
                    action='SELL',
                    units=units_to_buy,
                    price_per_unit=sell_price,
                    total_value=sale_revenue
                )
            ],
            cargo_after={},
            credits_after=starting_credits - purchase_cost + sale_revenue,
            cumulative_profit=profit
        ))

    route = MultiLegRoute(
        segments=segments,
        total_profit=profit,
        total_distance=total_distance,
        total_fuel_cost=total_fuel,
        estimated_time_minutes=int((total_distance / ship_speed) * 4)  # Rough estimate
    )

    logger.info("="*70)
    logger.info("FIXED ROUTE CREATED")
    logger.info("="*70)
    logger.info(f"Segments: {len(segments)}")
    logger.info(f"Total distance: {total_distance} units")
    logger.info(f"Estimated profit: {profit:,} credits")
    logger.info("="*70)

    return route
