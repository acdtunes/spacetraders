"""
Route Generator

Single Responsibility: Generate optimal trading routes using greedy search algorithm.

Coordinates route planning workflow by orchestrating opportunity finding,
strategy evaluation, and greedy route construction.
"""

import logging
from typing import Callable, Dict, List, Optional, Tuple

from spacetraders_bot.core.api_client import APIClient
from spacetraders_bot.operations._trading.models import RouteSegment, MultiLegRoute, TradeAction
from spacetraders_bot.operations._trading.market_repository import MarketRepository
from spacetraders_bot.operations._trading.evaluation_strategies import (
    TradeEvaluationStrategy,
    ProfitFirstStrategy
)
from spacetraders_bot.operations._trading.route_planning.market_validator import MarketValidator
from spacetraders_bot.operations._trading.route_planning.opportunity_finder import OpportunityFinder


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


class MultiLegRouteCoordinator:
    """
    High-level coordinator for route planning workflow

    Orchestrates:
    - OpportunityFinder for DB queries
    - MarketValidator for freshness checks
    - GreedyRoutePlanner for algorithm execution
    """

    def __init__(
        self,
        api: APIClient,
        db,
        player_id: int,
        logger: Optional[logging.Logger] = None,
        strategy_factory: Optional[Callable[[logging.Logger], TradeEvaluationStrategy]] = None,
    ):
        """
        Initialize route coordinator

        Args:
            api: APIClient instance
            db: Database instance
            player_id: Player ID for filtering market data
            logger: Optional logger (creates default if not provided)
            strategy_factory: Optional strategy factory (uses ProfitFirstStrategy by default)
        """
        self.api = api
        self.db = db
        self.player_id = player_id
        self.logger = logger or logging.getLogger(__name__)
        self._strategy_factory = strategy_factory or (lambda log: ProfitFirstStrategy(log))
        self.market_validator = MarketValidator(self.logger)
        self.opportunity_finder = OpportunityFinder(self.db, self.player_id, self.logger, self.market_validator)

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
        markets = self.opportunity_finder.get_markets_in_system(system)
        self.logger.info(f"Found {len(markets)} markets in {system}")

        if not markets:
            self.logger.error("No markets found in system")
            return None

        # Get all trade opportunities from database
        trade_opportunities = self.opportunity_finder.get_trade_opportunities(system, markets)
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
