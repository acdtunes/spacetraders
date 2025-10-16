#!/usr/bin/env python3
"""
OR-Tools Mining Fleet Optimizer

Uses Assignment Problem formulation to optimally assign mining ships to
asteroid-market pairs, maximizing total fleet profit per hour.
"""

from __future__ import annotations

import logging
import math
from dataclasses import dataclass
from typing import Dict, List, Optional, Tuple

from ortools.graph.python import linear_sum_assignment

from .database import Database
from .ortools_router import ORToolsRouter
from .routing_config import RoutingConfig

logger = logging.getLogger(__name__)


@dataclass
class MiningOpportunity:
    """Represents an asteroid-market mining opportunity."""

    asteroid: str
    market: str
    distance: float  # asteroid → market
    good: str  # Primary ore type
    market_price: int  # Credits per unit
    expected_yield: float  # Units per extraction (avg)
    cooldown_seconds: int  # Extraction cooldown
    profit_per_hour: float  # Expected credits/hour


@dataclass
class MiningAssignment:
    """Optimal ship assignment to mining opportunity."""

    ship: str
    asteroid: str
    market: str
    good: str
    profit_per_hour: float
    cycle_time_minutes: float
    fuel_cost_per_cycle: int
    revenue_per_cycle: int


class ORToolsMiningOptimizer:
    """
    Optimize mining fleet assignments using OR-Tools Assignment solver.

    Given a fleet of mining ships and a set of asteroids/markets, finds
    the optimal assignment that maximizes total fleet profit per hour.
    """

    DEFAULT_EXTRACTION_YIELD = 3.5  # Average units per extraction
    DEFAULT_COOLDOWN = 80  # Seconds between extractions
    MAX_ASTEROID_DISTANCE = 500  # Maximum profitable distance to market

    def __init__(
        self,
        system: str,
        graph: Dict,
        db: Database,
        config: Optional[RoutingConfig] = None,
    ):
        """
        Initialize mining optimizer.

        Args:
            system: System symbol (e.g., "X1-HU87")
            graph: System graph with waypoints and edges
            db: Database for market price lookups
            config: Routing configuration
        """
        self.system = system
        self.graph = graph
        self.db = db
        self.config = config or RoutingConfig()

    def optimize_fleet_assignment(
        self,
        ships: List[Dict],
        asteroids: Optional[List[str]] = None,
        markets: Optional[List[str]] = None,
    ) -> Dict[str, MiningAssignment]:
        """
        Assign ships to optimal asteroid-market pairs.

        Args:
            ships: List of ship data dicts (must have mining mounts)
            asteroids: Candidate asteroids (auto-discovered if None)
            markets: Candidate markets (auto-discovered if None)

        Returns:
            Dict mapping ship_symbol → MiningAssignment
        """
        logger.info("Optimizing mining fleet: %d ships", len(ships))

        # Auto-discover asteroids and markets if not provided
        if asteroids is None:
            asteroids = self._discover_asteroids()
        if markets is None:
            markets = self._discover_markets()

        logger.info("Candidates: %d asteroids, %d markets", len(asteroids), len(markets))

        # Generate all mining opportunities
        opportunities = self._generate_opportunities(asteroids, markets)
        if not opportunities:
            logger.warning("No profitable mining opportunities found")
            return {}

        logger.info("Generated %d mining opportunities", len(opportunities))

        # Limit opportunities to number of ships (assignment problem requires balanced bipartite graph)
        # We take the best opportunities since they're already sorted by profit descending
        if len(opportunities) > len(ships):
            logger.info(f"Limiting opportunities from {len(opportunities)} to {len(ships)} (matching ship count)")
            opportunities = opportunities[:len(ships)]

        # Build cost matrix for assignment problem
        cost_matrix = self._build_cost_matrix(ships, opportunities)

        # Solve assignment problem using OR-Tools
        assignments = self._solve_assignment(ships, opportunities, cost_matrix)

        return assignments

    # ------------------------------------------------------------------ #
    # Discovery
    # ------------------------------------------------------------------ #

    def _discover_asteroids(self) -> List[str]:
        """Find all asteroids in system with mining-suitable traits."""
        asteroids = []
        waypoints = self.graph.get("waypoints", {})

        # Prefer asteroids with ore deposits
        preferred_traits = {
            "COMMON_METAL_DEPOSITS",
            "PRECIOUS_METAL_DEPOSITS",
            "MINERAL_DEPOSITS",
        }

        # Avoid asteroids with hazardous traits
        avoid_traits = {"STRIPPED", "RADIOACTIVE", "EXPLOSIVE_GASES"}

        for symbol, data in waypoints.items():
            if data.get("type") != "ASTEROID":
                continue

            traits = set(data.get("traits", []))
            if traits & avoid_traits:
                continue

            if traits & preferred_traits:
                asteroids.append(symbol)

        logger.info("Discovered %d suitable asteroids", len(asteroids))
        return asteroids

    def _discover_markets(self) -> List[str]:
        """Find all markets in system that buy raw ores."""
        markets = []
        waypoints = self.graph.get("waypoints", {})

        for symbol, data in waypoints.items():
            traits = set(data.get("traits", []))
            # Markets with MARKETPLACE or EXCHANGE traits buy raw materials
            if "MARKETPLACE" in traits or "EXCHANGE" in traits:
                markets.append(symbol)

        logger.info("Discovered %d markets", len(markets))
        return markets

    # ------------------------------------------------------------------ #
    # Opportunity Generation
    # ------------------------------------------------------------------ #

    def _generate_opportunities(
        self,
        asteroids: List[str],
        markets: List[str],
    ) -> List[MiningOpportunity]:
        """
        Generate all viable asteroid-market mining opportunities.

        Returns:
            List of MiningOpportunity instances sorted by profit/hour descending
        """
        opportunities = []

        # Get market prices from database
        market_prices = self._get_market_prices(markets)
        logger.debug(f"Market prices: {market_prices}")

        for asteroid in asteroids:
            # Get asteroid coordinates
            asteroid_data = self.graph["waypoints"].get(asteroid)
            if not asteroid_data:
                logger.debug(f"Asteroid {asteroid} not in graph")
                continue

            for market in markets:
                # Calculate distance
                market_data = self.graph["waypoints"].get(market)
                if not market_data:
                    logger.debug(f"Market {market} not in graph")
                    continue

                distance = self._calculate_distance(asteroid_data, market_data)
                logger.debug(f"Distance {asteroid} -> {market}: {distance:.1f} (max: {self.MAX_ASTEROID_DISTANCE})")
                if distance > self.MAX_ASTEROID_DISTANCE:
                    continue  # Too far, unprofitable

                # Find best good to mine/sell at this market
                best_profit = 0
                best_good = None

                for good, price in market_prices.get(market, {}).items():
                    profit_per_hour = self._estimate_profit_per_hour(
                        distance=distance,
                        sell_price=price,
                        yield_per_extraction=self.DEFAULT_EXTRACTION_YIELD,
                        cooldown_seconds=self.DEFAULT_COOLDOWN,
                    )
                    logger.debug(f"  {good} @ {market}: price={price}, profit/hr={profit_per_hour:.0f}")

                    if profit_per_hour > best_profit:
                        best_profit = profit_per_hour
                        best_good = good

                if best_good and best_profit > 0:
                    logger.debug(f"✓ Opportunity: {asteroid} -> {market} ({best_good}): {best_profit:.0f} cr/hr")
                    opportunities.append(
                        MiningOpportunity(
                            asteroid=asteroid,
                            market=market,
                            distance=distance,
                            good=best_good,
                            market_price=market_prices[market][best_good],
                            expected_yield=self.DEFAULT_EXTRACTION_YIELD,
                            cooldown_seconds=self.DEFAULT_COOLDOWN,
                            profit_per_hour=best_profit,
                        )
                    )

        # Sort by profit descending
        opportunities.sort(key=lambda o: o.profit_per_hour, reverse=True)

        return opportunities

    def _get_market_prices(self, markets: List[str]) -> Dict[str, Dict[str, int]]:
        """
        Query database for current market buy prices.

        Returns:
            {market_symbol: {good_symbol: purchase_price}}
        """
        prices = {}

        with self.db.connection() as conn:
            for market in markets:
                # Query market_data for all goods this market buys
                rows = self.db.get_market_data(conn, market, good_symbol=None)
                market_prices = {}

                for row in rows:
                    good = row["good_symbol"]
                    purchase_price = row.get("purchase_price")
                    if purchase_price and purchase_price > 0:
                        market_prices[good] = purchase_price

                if market_prices:
                    prices[market] = market_prices

        return prices

    def _estimate_profit_per_hour(
        self,
        distance: float,
        sell_price: int,
        yield_per_extraction: float,
        cooldown_seconds: int,
    ) -> float:
        """
        Estimate profit per hour for a mining route.

        Simplified model:
        - Extraction time: cooldown_seconds per extraction
        - Travel time: (distance * 2) / speed (SpaceTraders formula)
        - Fuel cost: distance * 2 * 0.001 (DRIFT mode for fuel efficiency, round trip)
        - Revenue: yield * sell_price

        Mining operations use DRIFT mode to minimize fuel costs.

        Returns:
            Estimated credits per hour
        """
        # Assume average ship speed of 30 (can be adjusted per-ship)
        avg_speed = 30
        fuel_cost_per_unit_distance = 0.001  # DRIFT mode (fuel-efficient for mining)
        fuel_price = 100  # Credits per fuel unit (rough estimate)

        # Round trip travel time (seconds) - SpaceTraders formula: time = distance / speed
        # DRIFT mode is 3.33x slower than CRUISE
        travel_time = (distance * 2 / avg_speed) * 3.33

        # Fuel cost (round trip) - DRIFT is very fuel efficient
        fuel_cost = distance * 2 * fuel_cost_per_unit_distance * fuel_price

        # Revenue per extraction
        revenue = yield_per_extraction * sell_price

        # Total cycle time (extraction + travel)
        cycle_time_seconds = cooldown_seconds + travel_time

        # Profit per cycle
        profit_per_cycle = revenue - fuel_cost

        # Profit per hour
        cycles_per_hour = 3600 / cycle_time_seconds
        profit_per_hour = profit_per_cycle * cycles_per_hour

        return profit_per_hour

    def _calculate_distance(self, waypoint_a: Dict, waypoint_b: Dict) -> float:
        """Calculate Euclidean distance between two waypoints."""
        dx = waypoint_b["x"] - waypoint_a["x"]
        dy = waypoint_b["y"] - waypoint_a["y"]
        return math.hypot(dx, dy)

    # ------------------------------------------------------------------ #
    # Assignment Problem
    # ------------------------------------------------------------------ #

    def _build_cost_matrix(
        self,
        ships: List[Dict],
        opportunities: List[MiningOpportunity],
    ) -> List[List[int]]:
        """
        Build cost matrix for assignment problem.

        Matrix[i][j] = cost of assigning ship i to opportunity j
        (negative profit = cost, since OR-Tools minimizes)

        For ship-specific adjustments:
        - Faster ships → higher profit/hour → lower cost
        - Larger cargo → more units per trip → higher profit/hour
        """
        num_ships = len(ships)
        num_opportunities = len(opportunities)

        # Pad opportunities to match ship count (handle unbalanced assignment)
        if num_opportunities < num_ships:
            # Add dummy opportunities with zero profit
            padding = num_ships - num_opportunities
            for _ in range(padding):
                opportunities.append(
                    MiningOpportunity(
                        asteroid="",
                        market="",
                        distance=0,
                        good="",
                        market_price=0,
                        expected_yield=0,
                        cooldown_seconds=0,
                        profit_per_hour=0,
                    )
                )

        cost_matrix = []

        for ship in ships:
            ship_costs = []
            ship_speed = ship["engine"]["speed"]
            ship_cargo = ship["cargo"]["capacity"]

            for opp in opportunities:
                if opp.asteroid == "":
                    # Dummy opportunity - high cost (low priority)
                    ship_costs.append(1_000_000)
                    continue

                # Adjust profit based on ship characteristics
                adjusted_profit = self._adjust_profit_for_ship(
                    opp.profit_per_hour,
                    ship_speed,
                    ship_cargo,
                )

                # Negative profit = cost (OR-Tools minimizes cost)
                cost = -int(adjusted_profit)
                ship_costs.append(cost)

            cost_matrix.append(ship_costs)

        return cost_matrix

    def _adjust_profit_for_ship(
        self,
        base_profit: float,
        ship_speed: int,
        ship_cargo: int,
    ) -> float:
        """
        Adjust profit estimate based on ship characteristics.

        Faster ships complete cycles quicker.
        Larger cargo allows more units per trip (future enhancement).
        """
        # Speed adjustment: faster ships = proportionally higher profit
        speed_factor = ship_speed / 30  # Normalize to speed 30
        adjusted_profit = base_profit * speed_factor

        # Future: Cargo capacity could affect profit
        # (larger cargo = more units before needing to sell)

        return adjusted_profit

    def _solve_assignment(
        self,
        ships: List[Dict],
        opportunities: List[MiningOpportunity],
        cost_matrix: List[List[int]],
    ) -> Dict[str, MiningAssignment]:
        """
        Solve assignment problem using OR-Tools linear sum assignment.

        Returns:
            Dict mapping ship_symbol → MiningAssignment
        """
        assignment = linear_sum_assignment.SimpleLinearSumAssignment()

        # Add arcs (ship → opportunity) with costs
        for ship_idx, ship_costs in enumerate(cost_matrix):
            for opp_idx, cost in enumerate(ship_costs):
                assignment.add_arc_with_cost(ship_idx, opp_idx, cost)

        status = assignment.solve()

        logger.info(f"OR-Tools solver status: {status} (OPTIMAL={assignment.OPTIMAL})")
        logger.info(f"Number of ships: {len(ships)}, opportunities: {len(opportunities)}")

        if status != assignment.OPTIMAL:
            logger.error(f"Assignment solver failed with status {status}")
            return {}

        # Extract assignments
        assignments = {}
        num_nodes = assignment.num_nodes()
        logger.info(f"Assignment solver num_nodes: {num_nodes}")

        for ship_idx in range(num_nodes):
            opp_idx = assignment.right_mate(ship_idx)
            logger.debug(f"Ship[{ship_idx}] -> Opportunity[{opp_idx}]")

            if opp_idx < 0:
                logger.warning(f"Ship[{ship_idx}] has negative mate: {opp_idx}")
                continue

            if opp_idx >= len(opportunities):
                logger.warning(f"Ship[{ship_idx}] has invalid mate: {opp_idx} >= {len(opportunities)}")
                continue

            opportunity = opportunities[opp_idx]
            if opportunity.asteroid == "":
                # Skip dummy assignments
                continue

            ship = ships[ship_idx]
            ship_symbol = ship["symbol"]

            # Calculate detailed metrics for this assignment
            cycle_time_minutes = self._calculate_cycle_time(
                opportunity.distance,
                ship["engine"]["speed"],
                opportunity.cooldown_seconds,
            )

            fuel_cost = self._calculate_fuel_cost(
                opportunity.distance,
                ship["engine"]["speed"],
            )

            revenue_per_cycle = int(
                opportunity.expected_yield * opportunity.market_price
            )

            assignments[ship_symbol] = MiningAssignment(
                ship=ship_symbol,
                asteroid=opportunity.asteroid,
                market=opportunity.market,
                good=opportunity.good,
                profit_per_hour=opportunity.profit_per_hour,
                cycle_time_minutes=cycle_time_minutes,
                fuel_cost_per_cycle=fuel_cost,
                revenue_per_cycle=revenue_per_cycle,
            )

        logger.info("Assigned %d ships to mining opportunities", len(assignments))
        return assignments

    def _calculate_cycle_time(
        self,
        distance: float,
        ship_speed: int,
        cooldown_seconds: int,
    ) -> float:
        """Calculate total cycle time in minutes."""
        # Round trip travel time - SpaceTraders formula: time = distance / speed
        # Mining uses DRIFT mode (3.33x slower than CRUISE but very fuel efficient)
        travel_time_seconds = (distance * 2 / ship_speed) * 3.33

        # Total cycle time
        total_seconds = cooldown_seconds + travel_time_seconds

        return total_seconds / 60  # Convert to minutes

    def _calculate_fuel_cost(self, distance: float, ship_speed: int) -> int:
        """Calculate fuel cost for round trip in credits."""
        # Round trip fuel consumption (DRIFT mode for mining - fuel efficient)
        fuel_units = distance * 2 * 0.001

        # Fuel price (rough estimate)
        fuel_price = 100  # Credits per unit

        return int(fuel_units * fuel_price)
