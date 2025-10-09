#!/usr/bin/env python3
"""
Multi-leg trading optimization system

This module implements intelligent multi-stop trade routes that:
1. Buy goods at one market
2. Sell at next market if profitable, OR
3. Buy additional goods for future markets
4. Optimize total profit across entire route
"""

import heapq
import json
import logging
import time
from dataclasses import dataclass
from typing import Callable, Dict, List, Optional, Tuple

from spacetraders_bot.core.api_client import APIClient
from spacetraders_bot.core.ship_controller import ShipController
from spacetraders_bot.core.smart_navigator import SmartNavigator
from spacetraders_bot.core.utils import parse_waypoint_symbol, calculate_distance
from spacetraders_bot.operations.control import CircuitBreaker


@dataclass
class TradeAction:
    """Represents a buy or sell action at a market"""
    waypoint: str
    good: str
    action: str  # 'BUY' or 'SELL'
    units: int
    price_per_unit: int
    total_value: int


@dataclass
class RouteSegment:
    """Represents one leg of a multi-stop trade route"""
    from_waypoint: str
    to_waypoint: str
    distance: int
    fuel_cost: int
    actions_at_destination: List[TradeAction]  # What to do when we arrive
    cargo_after: Dict[str, int]  # Cargo state after actions at destination
    credits_after: int  # Credits after actions
    cumulative_profit: int  # Profit up to this point


@dataclass
class MultiLegRoute:
    """Complete multi-leg trading route"""
    segments: List[RouteSegment]
    total_profit: int
    total_distance: int
    total_fuel_cost: int
    estimated_time_minutes: int

@dataclass
class MarketEvaluation:
    """Represents the result of evaluating actions at a market."""

    actions: List[TradeAction]
    cargo_after: Dict[str, int]
    credits_after: int
    net_profit: int


def _cleanup_stranded_cargo(ship: ShipController, api: APIClient, db, logger: logging.Logger) -> bool:
    """
    Emergency cleanup: Sell all cargo on ship to nearest market

    Called by circuit breakers before exiting to prevent stranded cargo.
    Accepts ANY price to recover credits - this is cleanup, not profit optimization.

    Args:
        ship: ShipController instance
        api: APIClient instance
        db: Database instance for market lookups
        logger: Logger for recording cleanup actions

    Returns:
        True if cleanup succeeded (or no cargo to clean), False on critical failure
    """
    try:
        # Get current ship status
        ship_data = ship.get_status()
        if not ship_data:
            logger.error("Failed to get ship status for cargo cleanup")
            return False

        cargo = ship_data.get('cargo', {})
        inventory = cargo.get('inventory', [])

        # If no cargo, nothing to clean up
        if not inventory or cargo.get('units', 0) == 0:
            logger.info("No stranded cargo to clean up")
            return True

        logger.warning("="*70)
        logger.warning("🧹 CARGO CLEANUP: Selling stranded cargo")
        logger.warning("="*70)

        # Log stranded cargo
        for item in inventory:
            logger.warning(f"  Stranded: {item['units']}x {item['symbol']}")

        # Get current location
        current_waypoint = ship_data['nav']['waypointSymbol']
        system = ship_data['nav']['systemSymbol']

        # Try to find nearest market that accepts cargo
        # For simplicity, try current waypoint first (we're likely already at a market)
        logger.info(f"Attempting cleanup at current location: {current_waypoint}")

        # Ensure ship is docked
        if ship_data['nav']['status'] != 'DOCKED':
            logger.info("Docking for cargo cleanup...")
            if not ship.dock():
                logger.error("Failed to dock for cargo cleanup")
                return False

        # Sell all cargo, accepting any price
        cleanup_revenue = 0
        for item in inventory:
            good = item['symbol']
            units = item['units']

            logger.warning(f"  Selling {units}x {good} (accepting any price)...")

            try:
                # Use ship.sell with check_market_prices=False to accept any price
                transaction = ship.sell(good, units, check_market_prices=False)

                if transaction:
                    revenue = transaction['totalPrice']
                    price_per_unit = revenue / units if units > 0 else 0
                    cleanup_revenue += revenue
                    logger.warning(f"  ✅ Sold {units}x {good} for {revenue:,} credits ({price_per_unit:.0f} cr/unit)")
                else:
                    logger.error(f"  ❌ Failed to sell {good}")
                    # Continue trying to sell other items

            except Exception as e:
                logger.error(f"  ❌ Error selling {good}: {e}")
                # Continue trying to sell other items

        logger.warning(f"Total cleanup revenue: {cleanup_revenue:,} credits")
        logger.warning("="*70)

        # Verify cargo is now empty
        final_status = ship.get_status()
        if final_status:
            final_cargo = final_status.get('cargo', {})
            if final_cargo.get('units', 0) == 0:
                logger.info("✅ Cargo cleanup complete - ship hold empty")
                return True
            else:
                logger.warning(f"⚠️  Partial cleanup - {final_cargo.get('units', 0)} units remaining")
                return True  # Partial success is still acceptable

        return True

    except Exception as e:
        logger.error(f"Critical error during cargo cleanup: {e}")
        import traceback
        logger.error(traceback.format_exc())
        return False


class TradeEvaluationStrategy:
    """Defines the contract for market evaluation strategies."""

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
        raise NotImplementedError


class ProfitFirstStrategy(TradeEvaluationStrategy):
    """Default strategy that maximizes profit by mixing sell/buy actions."""

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
        sell_actions, cargo_after_sell, credits_after_sell, revenue = self._apply_sell_actions(
            market=market,
            current_cargo=current_cargo,
            current_credits=current_credits,
            trade_opportunities=trade_opportunities,
        )

        buy_actions, cargo_after_buy, credits_after_buy, purchase_costs = self._apply_buy_actions(
            market=market,
            trade_opportunities=trade_opportunities,
            cargo=cargo_after_sell,
            credits=credits_after_sell,
            cargo_capacity=cargo_capacity,
        )

        potential_future_revenue = self._estimate_potential_future_revenue(
            cargo_after_buy,
            market,
            trade_opportunities,
        )

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
        actions: List[TradeAction] = []
        cargo = current_cargo.copy()
        credits = current_credits
        revenue = 0

        for good, units in list(cargo.items()):
            sell_opp = next((o for o in trade_opportunities if o['sell_waypoint'] == market and o['good'] == good), None)
            if not sell_opp:
                continue

            sell_price = sell_opp['sell_price']
            trade_volume = sell_opp['trade_volume']

            units_to_sell = min(units, trade_volume)
            if units_to_sell <= 0:
                continue

            sale_value = units_to_sell * sell_price
            self.logger.debug(
                "Sell %s: %s units @ %s = %s",
                good,
                units_to_sell,
                sell_price,
                sale_value,
            )
            actions.append(TradeAction(
                waypoint=market,
                good=good,
                action='SELL',
                units=units_to_sell,
                price_per_unit=sell_price,
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
    ) -> Tuple[List[TradeAction], Dict[str, int], int, int]:
        actions: List[TradeAction] = []
        updated_cargo = cargo.copy()
        updated_credits = credits
        purchase_cost = 0

        cargo_used = sum(updated_cargo.values())
        cargo_available = cargo_capacity - cargo_used

        for opp in trade_opportunities:
            if opp['buy_waypoint'] != market:
                continue

            if cargo_available <= 0 or updated_credits <= 0:
                break

            good = opp['good']
            buy_price = opp['buy_price']
            trade_volume = opp['trade_volume']

            if buy_price <= 0:
                continue

            max_affordable = min(updated_credits // buy_price, cargo_available, trade_volume)
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
        potential_revenue = 0
        for good, units in cargo.items():
            best_sell = max(
                (
                    o['sell_price']
                    for o in trade_opportunities
                    if o['good'] == good and o['sell_waypoint'] != current_market
                ),
                default=0,
            )
            potential_revenue += units * best_sell
        return potential_revenue


class GreedyRoutePlanner:
    """Encapsulates the greedy multi-leg route search logic."""

    def __init__(self, logger: logging.Logger, db, strategy: Optional[TradeEvaluationStrategy] = None):
        self.logger = logger
        self.db = db
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
    ) -> Optional[MultiLegRoute]:
        current_waypoint = start_waypoint
        current_cargo: Dict[str, int] = {}
        current_credits = starting_credits
        cumulative_profit = 0
        route_segments: List[RouteSegment] = []
        visited = {start_waypoint}

        for _ in range(max_stops):
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

            segment = RouteSegment(
                from_waypoint=current_waypoint,
                to_waypoint=next_waypoint,
                distance=distance,
                fuel_cost=int(distance * 1.1),
                actions_at_destination=actions,
                cargo_after=new_cargo.copy(),
                credits_after=new_credits,
                cumulative_profit=cumulative_profit + segment_profit,
            )

            route_segments.append(segment)

            current_waypoint = next_waypoint
            current_cargo = new_cargo
            current_credits = new_credits
            cumulative_profit += segment_profit
            visited.add(next_waypoint)

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
        best_option = None
        best_profit = 0

        for next_market in markets:
            if next_market in visited:
                continue

            distance = self._estimate_distance(current_waypoint, next_market)
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

        return best_option

    def _estimate_distance(self, from_waypoint: str, to_waypoint: str) -> float:
        with self.db.connection() as conn:
            cursor = conn.cursor()
            cursor.execute("SELECT x, y FROM waypoints WHERE waypoint_symbol = ?", (from_waypoint,))
            from_row = cursor.fetchone()
            cursor.execute("SELECT x, y FROM waypoints WHERE waypoint_symbol = ?", (to_waypoint,))
            to_row = cursor.fetchone()

            if not from_row or not to_row:
                return 150.0

            dx = to_row[0] - from_row[0]
            dy = to_row[1] - from_row[1]
            return (dx**2 + dy**2) ** 0.5


class MultiLegTradeOptimizer:
    """
    Optimizes multi-leg trading routes using market data

    Strategy:
    1. Start at current location with empty cargo
    2. For each potential next market:
       - Check what we can buy/sell profitably
       - Calculate profit if we sell current cargo
       - Calculate profit if we buy new goods for future markets
    3. Use A* search to find optimal route
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
        current_fuel: int
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

        strategy = self._strategy_factory(self.logger)
        planner = GreedyRoutePlanner(self.logger, self.db, strategy=strategy)
        best_route = planner.find_route(
            start_waypoint=start_waypoint,
            markets=markets,
            trade_opportunities=trade_opportunities,
            max_stops=max_stops,
            cargo_capacity=cargo_capacity,
            starting_credits=starting_credits,
            ship_speed=ship_speed,
        )

        if best_route:
            self.logger.info("\n" + "="*70)
            self.logger.info("OPTIMAL ROUTE FOUND")
            self.logger.info("="*70)
            self.logger.info(f"Total profit: {best_route.total_profit:,} credits")
            self.logger.info(f"Total distance: {best_route.total_distance:.0f} units")
            self.logger.info(f"Estimated time: {best_route.estimated_time_minutes:.0f} minutes")
            self.logger.info(f"Stops: {len(best_route.segments)}")
            self.logger.info("\nRoute:")

            for i, segment in enumerate(best_route.segments, 1):
                self.logger.info(f"\n  Stop {i}: {segment.to_waypoint}")
                self.logger.info(f"    Distance: {segment.distance:.0f} units")
                self.logger.info(f"    Actions:")
                for action in segment.actions_at_destination:
                    symbol = '💰' if action.action == 'BUY' else '💵'
                    self.logger.info(f"      {symbol} {action.action} {action.units}x {action.good} @ {action.price_per_unit:,} = {action.total_value:,}")
                self.logger.info(f"    Cargo after: {segment.cargo_after}")
                self.logger.info(f"    Cumulative profit: {segment.cumulative_profit:,}")

            self.logger.info("="*70)
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
        opportunities = []

        for sell_market in markets:
            if sell_market == buy_market:
                continue

            for buy_record in buy_data:
                good = buy_record['good_symbol']
                buy_price = buy_record.get('sell_price')

                if not buy_price:
                    continue

                sell_data = self.db.get_market_data(conn, sell_market, good)
                if not sell_data:
                    continue

                sell_record = sell_data[0]
                sell_price = sell_record.get('purchase_price')

                if not sell_price:
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


def execute_multileg_route(
    route: MultiLegRoute,
    ship: ShipController,
    api: APIClient,
    db,
    player_id: int
) -> bool:
    """
    Execute a multi-leg trading route with live monitoring and circuit breakers

    Args:
        route: The planned multi-leg trading route
        ship: ShipController instance for the executing ship
        api: APIClient instance
        db: Database instance for market data
        player_id: Player ID for database queries

    Returns:
        True if route executed successfully, False on any critical failure
    """
    logging.info("\n" + "="*70)
    logging.info("ROUTE EXECUTION START")
    logging.info("="*70)

    # Get ship data to determine system
    ship_data = ship.get_status()
    if not ship_data:
        logging.error("Failed to get ship status")
        return False

    system = ship_data['nav']['systemSymbol']

    # Create SmartNavigator for intelligent routing
    navigator = SmartNavigator(api, system)

    # Track cumulative metrics
    total_revenue = 0
    total_costs = 0
    starting_location = ship_data['nav']['waypointSymbol']

    # Get starting credits
    agent = api.get_agent()
    if not agent:
        logging.error("Failed to get agent data")
        return False
    starting_credits = agent['credits']

    logging.info(f"Starting location: {starting_location}")
    logging.info(f"Starting credits: {starting_credits:,}")
    logging.info(f"Route segments: {len(route.segments)}")
    logging.info("="*70)

    # Execute each segment
    for segment_num, segment in enumerate(route.segments, 1):
        logging.info("\n" + "-"*70)
        logging.info(f"SEGMENT {segment_num}/{len(route.segments)}: {segment.from_waypoint} → {segment.to_waypoint}")
        logging.info("-"*70)
        logging.info(f"Distance: {segment.distance:.0f} units")
        logging.info(f"Fuel cost estimate: {segment.fuel_cost}")
        logging.info(f"Actions planned: {len(segment.actions_at_destination)}")

        try:
            # Step 1: Navigate to destination waypoint
            logging.info(f"\n🚀 Navigating to {segment.to_waypoint}...")

            if not navigator.execute_route(ship, segment.to_waypoint):
                logging.error(f"❌ Navigation failed to {segment.to_waypoint}")
                logging.error("Route execution aborted")
                _cleanup_stranded_cargo(ship, api, db, logging.getLogger(__name__))
                return False

            logging.info(f"✅ Arrived at {segment.to_waypoint}")

            # Step 2: Dock at waypoint for trading
            logging.info(f"\n🛬 Docking at {segment.to_waypoint}...")
            if not ship.dock():
                logging.error("❌ Failed to dock")
                logging.error("Route execution aborted")
                _cleanup_stranded_cargo(ship, api, db, logging.getLogger(__name__))
                return False

            # Step 3: Execute trade actions at this waypoint
            logging.info(f"\n💼 Executing {len(segment.actions_at_destination)} trade actions...")

            segment_revenue = 0
            segment_costs = 0

            for action_num, action in enumerate(segment.actions_at_destination, 1):
                logging.info(f"\n  Action {action_num}/{len(segment.actions_at_destination)}: {action.action} {action.units}x {action.good}")

                if action.action == 'BUY':
                    # Purchase cargo
                    logging.info(f"  💰 Buying {action.units}x {action.good} @ {action.price_per_unit:,} = {action.total_value:,} credits")

                    # Get fresh market data before purchase
                    try:
                        live_market = api.get_market(system, action.waypoint)
                        if live_market:
                            # Check current buy price
                            live_buy_price = None
                            trade_volume = None
                            for good in live_market.get('tradeGoods', []):
                                if good['symbol'] == action.good:
                                    live_buy_price = good.get('sellPrice')  # What we pay
                                    trade_volume = good.get('tradeVolume')
                                    break

                            if live_buy_price:
                                price_change_pct = ((live_buy_price - action.price_per_unit) / action.price_per_unit) * 100 if action.price_per_unit > 0 else 0

                                if abs(price_change_pct) > 5:
                                    logging.warning(f"  ⚠️  Price changed: {action.price_per_unit:,} → {live_buy_price:,} ({price_change_pct:+.1f}%)")

                                # CIRCUIT BREAKER: Abort if buy price increased too much
                                if price_change_pct > 30:
                                    logging.error("="*70)
                                    logging.error("🚨 CIRCUIT BREAKER: BUY PRICE SPIKE!")
                                    logging.error("="*70)
                                    logging.error(f"  Expected: {action.price_per_unit:,} cr/unit")
                                    logging.error(f"  Current: {live_buy_price:,} cr/unit")
                                    logging.error(f"  Increase: {price_change_pct:.1f}%")
                                    logging.error("  Route execution aborted to prevent loss")
                                    logging.error("="*70)
                                    _cleanup_stranded_cargo(ship, api, db, logging.getLogger(__name__))
                                    return False
                    except Exception as e:
                        logging.warning(f"  ⚠️  Live market check failed: {e}, proceeding with planned purchase...")

                    # Execute purchase
                    transaction = ship.buy(action.good, action.units)
                    if not transaction:
                        logging.error(f"  ❌ Purchase failed for {action.good}")
                        logging.error("  Route execution aborted")
                        _cleanup_stranded_cargo(ship, api, db, logging.getLogger(__name__))
                        return False

                    actual_cost = transaction['totalPrice']
                    actual_price_per_unit = actual_cost / transaction['units'] if transaction['units'] > 0 else 0

                    # CIRCUIT BREAKER: Check actual purchase price vs planned price
                    if action.price_per_unit > 0:
                        actual_price_change_pct = ((actual_price_per_unit - action.price_per_unit) / action.price_per_unit) * 100

                        if actual_price_change_pct > 30:
                            logging.error("="*70)
                            logging.error("🚨 CIRCUIT BREAKER: ACTUAL BUY PRICE SPIKE!")
                            logging.error("="*70)
                            logging.error(f"  Planned: {action.price_per_unit:,} cr/unit")
                            logging.error(f"  Actual: {actual_price_per_unit:,.0f} cr/unit")
                            logging.error(f"  Increase: {actual_price_change_pct:.1f}%")
                            logging.error(f"  Already spent: {actual_cost:,} credits on this purchase")
                            logging.error("  Aborting route to prevent further losses")
                            logging.error("="*70)
                            _cleanup_stranded_cargo(ship, api, db, logging.getLogger(__name__))
                            return False
                        elif abs(actual_price_change_pct) > 5:
                            logging.warning(f"  ⚠️  Actual price: {action.price_per_unit:,} → {actual_price_per_unit:,.0f} ({actual_price_change_pct:+.1f}%)")

                    segment_costs += actual_cost
                    total_costs += actual_cost

                    logging.info(f"  ✅ Purchased {transaction['units']}x {action.good} for {actual_cost:,} credits")

                elif action.action == 'SELL':
                    # Sell cargo with live monitoring
                    logging.info(f"  💵 Selling {action.units}x {action.good} @ {action.price_per_unit:,} = {action.total_value:,} credits")

                    # Get fresh market data for sell price monitoring
                    live_sell_price = None
                    sell_trade_volume = None
                    try:
                        live_market = api.get_market(system, action.waypoint)
                        if live_market:
                            for good in live_market.get('tradeGoods', []):
                                if good['symbol'] == action.good:
                                    live_sell_price = good.get('purchasePrice')  # What market pays us
                                    sell_trade_volume = good.get('tradeVolume')
                                    break

                            if live_sell_price:
                                price_change_pct = ((live_sell_price - action.price_per_unit) / action.price_per_unit) * 100 if action.price_per_unit > 0 else 0

                                if abs(price_change_pct) > 5:
                                    logging.warning(f"  ⚠️  Sell price changed: {action.price_per_unit:,} → {live_sell_price:,} ({price_change_pct:+.1f}%)")

                                # CIRCUIT BREAKER: Abort if sell price crashed
                                if price_change_pct < -30:
                                    logging.error("="*70)
                                    logging.error("🚨 CIRCUIT BREAKER: SELL PRICE CRASH!")
                                    logging.error("="*70)
                                    logging.error(f"  Expected: {action.price_per_unit:,} cr/unit")
                                    logging.error(f"  Current: {live_sell_price:,} cr/unit")
                                    logging.error(f"  Drop: {price_change_pct:.1f}%")
                                    logging.error("  Route execution aborted to prevent loss")
                                    logging.error("="*70)
                                    _cleanup_stranded_cargo(ship, api, db, logging.getLogger(__name__))
                                    return False
                    except Exception as e:
                        logging.warning(f"  ⚠️  Live market check failed: {e}, proceeding with planned sale...")

                    # Execute sale with live monitoring
                    transaction = ship.sell(
                        action.good,
                        action.units,
                        max_per_transaction=sell_trade_volume,
                        check_market_prices=True,
                        min_acceptable_price=live_sell_price if live_sell_price else action.price_per_unit
                    )

                    if not transaction:
                        logging.error(f"  ❌ Sale failed for {action.good}")
                        logging.error("  Route execution aborted")
                        _cleanup_stranded_cargo(ship, api, db, logging.getLogger(__name__))
                        return False

                    # Check if sale was aborted mid-batch due to price collapse
                    if transaction.get('aborted'):
                        remaining = transaction.get('remaining_units', 0)
                        logging.error("="*70)
                        logging.error("🚨 CIRCUIT BREAKER: SALE ABORTED MID-BATCH!")
                        logging.error("="*70)
                        logging.error(f"  Sold: {transaction['units']} units")
                        logging.error(f"  Remaining: {remaining} units (unsold due to price collapse)")
                        logging.error("  Route execution aborted")
                        logging.error("="*70)
                        return False

                    actual_revenue = transaction['totalPrice']
                    segment_revenue += actual_revenue
                    total_revenue += actual_revenue

                    logging.info(f"  ✅ Sold {transaction['units']}x {action.good} for {actual_revenue:,} credits")

                # Rate limiting between actions
                time.sleep(0.6)

            # Segment complete - calculate segment profit
            segment_profit = segment_revenue - segment_costs

            logging.info(f"\n📊 Segment {segment_num} complete:")
            logging.info(f"  Revenue: {segment_revenue:,} credits")
            logging.info(f"  Costs: {segment_costs:,} credits")
            logging.info(f"  Profit: {segment_profit:,} credits")

            # CIRCUIT BREAKER: Check if segment was unprofitable
            if segment_profit < 0:
                logging.error("="*70)
                logging.error("🚨 CIRCUIT BREAKER: SEGMENT UNPROFITABLE!")
                logging.error("="*70)
                logging.error(f"  Segment {segment_num} lost {abs(segment_profit):,} credits")
                logging.error("  Aborting route to prevent further losses")
                logging.error("="*70)
                return False

            # Get current credits to verify progress
            current_agent = api.get_agent()
            if current_agent:
                current_credits = current_agent['credits']
                net_change = current_credits - starting_credits
                logging.info(f"  Current credits: {current_credits:,} (net: {net_change:+,})")

        except Exception as e:
            logging.error("="*70)
            logging.error(f"🚨 CRITICAL ERROR during segment {segment_num}")
            logging.error("="*70)
            logging.error(f"Error: {str(e)}")
            logging.error("Route execution aborted")
            logging.error("="*70)
            import traceback
            logging.error(traceback.format_exc())
            return False

    # All segments complete
    logging.info("\n" + "="*70)
    logging.info("✅ ALL SEGMENTS COMPLETE")
    logging.info("="*70)

    # Final accounting
    final_agent = api.get_agent()
    if final_agent:
        final_credits = final_agent['credits']
        actual_profit = final_credits - starting_credits

        logging.info(f"Starting credits: {starting_credits:,}")
        logging.info(f"Final credits: {final_credits:,}")
        logging.info(f"Actual profit: {actual_profit:,}")
        logging.info(f"Estimated profit: {route.total_profit:,}")

        if route.total_profit > 0:
            accuracy = (actual_profit / route.total_profit) * 100
            logging.info(f"Estimate accuracy: {accuracy:.1f}%")

        # Final circuit breaker: Check if overall route was profitable
        if actual_profit < 0:
            logging.warning("="*70)
            logging.warning("⚠️  WARNING: Overall route was unprofitable!")
            logging.warning("="*70)
            logging.warning(f"  Net loss: {abs(actual_profit):,} credits")
            logging.warning("  Market conditions changed unfavorably")
            logging.warning("="*70)
            # Don't return False here - we completed the route, just didn't make profit

    logging.info("="*70)
    return True


def trade_plan_operation(args):
    """Analyze and propose a multi-leg trading route without executing it."""
    from .common import get_api_client, get_database

    player_id = getattr(args, "player_id", None)
    ship_symbol = getattr(args, "ship", None)

    if not player_id:
        print("❌ --player-id required")
        return 1

    if not ship_symbol:
        print("❌ --ship required")
        return 1

    max_stops = getattr(args, "max_stops", 4) or 4
    try:
        max_stops = int(max_stops)
    except (TypeError, ValueError):
        print("❌ --max-stops must be an integer")
        return 1

    if max_stops < 2:
        print("❌ --max-stops must be at least 2")
        return 1

    try:
        api = get_api_client(player_id)
    except Exception as exc:  # Surface errors directly
        print(f"❌ {exc}")
        return 1

    db = get_database()
    ship = ShipController(api, ship_symbol)

    ship_data = ship.get_status()
    if not ship_data:
        print("❌ Failed to get ship status")
        return 1

    system_override = getattr(args, "system", None)
    system = system_override or ship_data['nav']['systemSymbol']
    start_waypoint = ship_data['nav']['waypointSymbol']

    cargo_capacity = ship_data['cargo']['capacity']
    ship_speed = ship_data['engine']['speed']
    fuel_capacity = ship_data['fuel']['capacity']
    current_fuel = ship_data['fuel']['current']

    agent = api.get_agent()
    if not agent:
        print("❌ Failed to get agent data")
        return 1

    starting_credits = agent['credits']

    optimizer = MultiLegTradeOptimizer(api, db, player_id)
    route = optimizer.find_optimal_route(
        start_waypoint=start_waypoint,
        system=system,
        max_stops=max_stops,
        cargo_capacity=cargo_capacity,
        starting_credits=starting_credits,
        ship_speed=ship_speed,
        fuel_capacity=fuel_capacity,
        current_fuel=current_fuel,
    )

    if not route:
        print("❌ No profitable route found")
        return 1

    summary = {
        "ship": ship_symbol,
        "player_id": player_id,
        "system": system,
        "start_waypoint": start_waypoint,
        "max_stops": max_stops,
        "total_profit": route.total_profit,
        "total_distance": route.total_distance,
        "total_fuel_cost": route.total_fuel_cost,
        "estimated_time_minutes": route.estimated_time_minutes,
        "segment_count": len(route.segments),
        "segments": [],
    }

    for index, segment in enumerate(route.segments, start=1):
        actions = [
            {
                "type": action.action,
                "good": action.good,
                "units": action.units,
                "price_per_unit": action.price_per_unit,
                "total_value": action.total_value,
            }
            for action in segment.actions_at_destination
        ]

        summary["segments"].append(
            {
                "index": index,
                "from_waypoint": segment.from_waypoint,
                "to_waypoint": segment.to_waypoint,
                "distance": segment.distance,
                "fuel_cost": segment.fuel_cost,
                "actions": actions,
                "cargo_after": segment.cargo_after,
                "credits_after": segment.credits_after,
                "cumulative_profit": segment.cumulative_profit,
            }
        )

    print(json.dumps(summary, indent=2))
    return 0


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
    current_fuel
) -> Optional[MultiLegRoute]:
    """
    Create a fixed 2-stop route (buy → sell) without optimization

    This is the prescriptive mode for single-leg trading
    """
    from spacetraders_bot.core.utils import calculate_distance

    logging.info("="*70)
    logging.info("CREATING FIXED ROUTE")
    logging.info("="*70)
    logging.info(f"Route: {current_waypoint} → {buy_waypoint} → {sell_waypoint}")
    logging.info(f"Good: {good}")
    logging.info(f"Cargo capacity: {cargo_capacity}")

    # Get market data from database
    with db.transaction() as conn:
        buy_market_rows = db.get_market_data(conn, buy_waypoint, good)
        sell_market_rows = db.get_market_data(conn, sell_waypoint, good)

    # Extract first row (get_market_data returns List[Dict])
    buy_market = buy_market_rows[0] if buy_market_rows else None
    sell_market = sell_market_rows[0] if sell_market_rows else None

    if not buy_market or not sell_market:
        logging.error("Missing market data for route")
        return None

    buy_price = buy_market['sell_price']  # What we pay
    sell_price = sell_market['purchase_price']  # What we receive
    trade_volume = buy_market.get('trade_volume', cargo_capacity)

    logging.info(f"Buy price @ {buy_waypoint}: {buy_price:,} cr/unit")
    logging.info(f"Sell price @ {sell_waypoint}: {sell_price:,} cr/unit")
    logging.info(f"Spread: {sell_price - buy_price:,} cr/unit")

    # Look up waypoint coordinates from database
    # (calculate_distance expects coordinate dictionaries, not waypoint symbols)
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
        logging.error(f"Missing waypoint coordinate data for: {', '.join(missing)}")
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
        logging.error("Cannot afford any units")
        return None

    purchase_cost = units_to_buy * buy_price
    sale_revenue = units_to_buy * sell_price
    estimated_fuel_cost = total_fuel * 100  # Rough estimate: 100 cr/fuel
    profit = sale_revenue - purchase_cost - estimated_fuel_cost

    logging.info(f"Units to trade: {units_to_buy}")
    logging.info(f"Purchase cost: {purchase_cost:,}")
    logging.info(f"Sale revenue: {sale_revenue:,}")
    logging.info(f"Estimated profit: {profit:,}")

    if profit <= 0:
        logging.warning("Route not profitable based on current market data")
        return None

    # Build route segments
    segments = []

    # Segment 1: Current → Buy market
    if current_waypoint != buy_waypoint:
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
            cumulative_profit=0
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

    logging.info("="*70)
    logging.info("FIXED ROUTE CREATED")
    logging.info("="*70)
    logging.info(f"Segments: {len(segments)}")
    logging.info(f"Total distance: {total_distance} units")
    logging.info(f"Estimated profit: {profit:,} credits")
    logging.info("="*70)

    return route


def multileg_trade_operation(args):
    """Execute a multi-leg trading operation"""
    from .common import setup_logging, get_api_client, get_database
    from spacetraders_bot.core.ship_controller import ShipController
    from datetime import datetime, timedelta

    log_file = setup_logging("multileg_trade", args.ship, getattr(args, 'log_level', 'INFO'))

    logging.info("="*70)
    logging.info("MULTI-LEG TRADING OPERATION")
    logging.info("="*70)

    api = get_api_client(args.player_id)
    db = get_database()
    ship = ShipController(api, args.ship)

    # Get ship status
    ship_data = ship.get_status()
    if not ship_data:
        logging.error("Failed to get ship status")
        return 1

    system = getattr(args, 'system', None) or ship_data['nav']['systemSymbol']
    current_waypoint = ship_data['nav']['waypointSymbol']
    cargo_capacity = getattr(args, 'cargo', None) or ship_data['cargo']['capacity']
    ship_speed = ship_data['engine']['speed']
    fuel_capacity = ship_data['fuel']['capacity']
    current_fuel = ship_data['fuel']['current']

    # Get current credits
    agent = api.get_agent()
    if not agent:
        logging.error("Failed to get agent data")
        return 1

    starting_credits = agent['credits']

    # Determine operation mode
    fixed_route_mode = bool(getattr(args, 'good', None) and
                            getattr(args, 'buy_from', None) and
                            getattr(args, 'sell_to', None))
    looping_mode = getattr(args, 'cycles', None) is not None or getattr(args, 'duration', None) is not None

    if fixed_route_mode:
        logging.info("Mode: FIXED-ROUTE (prescriptive trading)")
        logging.info(f"Route: {args.good} from {args.buy_from} → {args.sell_to}")
    else:
        logging.info("Mode: AUTONOMOUS (route optimization)")

    if looping_mode:
        cycles = getattr(args, 'cycles', None)
        duration = getattr(args, 'duration', None)
        if cycles is not None:
            if cycles == -1:
                logging.info("Looping: INFINITE")
            else:
                logging.info(f"Looping: {cycles} cycles")
        else:
            logging.info(f"Duration: {duration} hours")
    else:
        logging.info("Mode: ONE-SHOT")

    logging.info(f"Min profit threshold: {args.min_profit:,} credits")
    logging.info("="*70)

    # Initialize loop control
    if looping_mode:
        if getattr(args, 'duration', None):
            start_time = datetime.now()
            end_time = start_time + timedelta(hours=args.duration)
            cycles_remaining = float('inf')
        else:
            end_time = None
            cycles_remaining = args.cycles if args.cycles != -1 else float('inf')
    else:
        end_time = None
        cycles_remaining = 1

    cycle_num = 0
    total_profit = 0
    low_profit_breaker = CircuitBreaker(limit=3)

    # Main trading loop
    while cycles_remaining > 0:
        cycle_num += 1

        # Check time limit
        if end_time and datetime.now() >= end_time:
            logging.info("Duration limit reached")
            break

        if looping_mode:
            logging.info(f"\n{'='*70}")
            logging.info(f"CYCLE {cycle_num}")
            if cycles_remaining != float('inf'):
                logging.info(f"Remaining: {int(cycles_remaining)} cycles")
            logging.info('='*70)

        # Get current ship status
        ship_data = ship.get_status()
        if not ship_data:
            logging.error("Failed to get ship status")
            return 1

        current_waypoint = ship_data['nav']['waypointSymbol']
        current_fuel = ship_data['fuel']['current']

        # Get current credits
        agent = api.get_agent()
        if not agent:
            logging.error("Failed to get agent data")
            return 1

        cycle_start_credits = agent['credits']

        # Find or create route
        if fixed_route_mode:
            route = create_fixed_route(
                api, db, args.player_id,
                current_waypoint,
                args.buy_from,
                args.sell_to,
                args.good,
                cargo_capacity,
                cycle_start_credits,
                ship_speed,
                fuel_capacity,
                current_fuel
            )
        else:
            optimizer = MultiLegTradeOptimizer(api, db, args.player_id)
            route = optimizer.find_optimal_route(
                start_waypoint=current_waypoint,
                system=system,
                max_stops=args.max_stops,
                cargo_capacity=cargo_capacity,
                starting_credits=cycle_start_credits,
                ship_speed=ship_speed,
                fuel_capacity=fuel_capacity,
                current_fuel=current_fuel
            )

        if not route:
            logging.error("No profitable route found")
            if looping_mode:
                logging.warning("Breaking loop - no profitable routes available")
            return 1

        # Execute the route
        logging.info("\n" + "="*70)
        logging.info("EXECUTING MULTI-LEG ROUTE")
        logging.info("="*70)

        success = execute_multileg_route(route, ship, api, db, args.player_id)

        if not success:
            logging.error("\n" + "="*70)
            logging.error("❌ MULTI-LEG ROUTE FAILED")
            logging.error("="*70)
            return 1

        # Calculate cycle profit
        final_agent = api.get_agent()
        cycle_end_credits = final_agent['credits'] if final_agent else cycle_start_credits
        cycle_profit = cycle_end_credits - cycle_start_credits
        total_profit += cycle_profit

        logging.info("\n" + "="*70)
        if looping_mode:
            logging.info(f"CYCLE {cycle_num} COMPLETE")
        else:
            logging.info("✅ MULTI-LEG ROUTE COMPLETE")
        logging.info("="*70)
        logging.info(f"Cycle profit: {cycle_profit:,}")
        logging.info(f"Estimated profit: {route.total_profit:,}")
        logging.info(f"Accuracy: {(cycle_profit/route.total_profit*100) if route.total_profit > 0 else 0:.1f}%")

        if looping_mode:
            logging.info(f"Total profit: {total_profit:,}")
            logging.info(f"Average profit/cycle: {total_profit // cycle_num:,}")

        logging.info("="*70)

        # Circuit breaker: Check profitability for looping mode
        if looping_mode:
            if cycle_profit < args.min_profit:
                failures = low_profit_breaker.record_failure()
                logging.warning(
                    "Low profit (%s < %s)",
                    f"{cycle_profit:,}",
                    f"{args.min_profit:,}",
                )
                logging.warning("Consecutive low cycles: %s", failures)

                if low_profit_breaker.tripped():
                    logging.error(
                        "🚨 %s consecutive low-profit cycles", failures
                    )
                    logging.error("🛑 STOPPING")
                    break
            else:
                low_profit_breaker.record_success()

            if cycle_profit < 0:
                logging.error(f"🚨 CIRCUIT BREAKER: NEGATIVE PROFIT ({cycle_profit:,})")
                logging.error("🛑 STOPPING")
                break

        # Decrement cycles
        if cycles_remaining != float('inf'):
            cycles_remaining -= 1

        # Brief pause between cycles
        if looping_mode and cycles_remaining > 0:
            time.sleep(2)

    # Final summary
    final_agent = api.get_agent()
    final_credits = final_agent['credits'] if final_agent else starting_credits

    logging.info(f"\n{'='*70}")
    logging.info("OPERATION COMPLETE")
    logging.info('='*70)
    logging.info(f"Starting credits: {starting_credits:,}")
    logging.info(f"Final credits: {final_credits:,}")
    logging.info(f"Total profit: {total_profit:,}")
    logging.info(f"Cycles completed: {cycle_num}")
    if cycle_num > 0:
        logging.info(f"Average profit/cycle: {total_profit // cycle_num:,}")
    logging.info('='*70)

    return 0
