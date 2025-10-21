"""
Fleet Trade Optimizer

Multi-ship fleet coordination with conflict avoidance.
Single Responsibility: Prevent resource conflicts between ships in same system.
"""

import logging
from typing import Dict, List, Optional

from spacetraders_bot.core.api_client import APIClient
from spacetraders_bot.operations._trading.models import MultiLegRoute
from spacetraders_bot.operations._trading.route_planner import MultiLegTradeOptimizer
from spacetraders_bot.operations._trading.evaluation_strategies import ProfitFirstStrategy
from spacetraders_bot.operations._trading.route_planner import GreedyRoutePlanner


class FleetTradeOptimizer:
    """
    Multi-ship fleet trade route optimizer with conflict avoidance

    Prevents (resource, waypoint) collisions between ships using greedy sequential assignment.
    Ensures each ship gets an independently profitable route while maximizing total fleet profit.

    Algorithm:
    1. Assign best route to Ship 1 (standard single-ship optimization)
    2. Record all (resource, waypoint) BUY pairs from Ship 1's route
    3. For Ship 2, exclude any trade opportunities that would buy same resource at same waypoint
    4. Repeat for Ship N

    Example:
        Ship 1: D42 (buy ADVANCED_CIRCUITRY) → A4 (sell)
        Ship 2: Cannot buy ADVANCED_CIRCUITRY at D42 (conflict!)
        Ship 2: C39 (buy COPPER_ORE) → B7 (sell) ✓ (no conflict)
    """

    def __init__(
        self,
        api: APIClient,
        db,
        player_id: int,
        logger: Optional[logging.Logger] = None,
    ):
        self.api = api
        self.db = db
        self.player_id = player_id
        self.logger = logger or logging.getLogger(__name__)

    def optimize_fleet(
        self,
        ships: List[Dict],
        system: str,
        max_stops: int,
        starting_credits: int,
    ) -> Optional[Dict]:
        """
        Find conflict-free routes for multiple ships

        Args:
            ships: List of ship data dicts with symbol, cargo capacity, fuel, etc.
            system: System symbol (e.g., "X1-TX46")
            max_stops: Maximum stops per route
            starting_credits: Available credits for purchases

        Returns:
            Dict with:
            - ship_routes: {ship_symbol: MultiLegRoute}
            - total_fleet_profit: Sum of all ship profits
            - conflicts: Number of conflicts avoided
        """
        self.logger.info("="*70)
        self.logger.info("FLEET TRADE ROUTE OPTIMIZATION")
        self.logger.info("="*70)
        self.logger.info(f"Ships: {len(ships)}")
        self.logger.info(f"System: {system}")
        self.logger.info(f"Max stops per route: {max_stops}")
        self.logger.info("="*70)

        # Track (resource, waypoint) BUY pairs across all assigned routes
        # This prevents ships from buying same resource at same waypoint
        reserved_resource_waypoints: set[tuple[str, str]] = set()

        # Store results
        ship_routes: Dict[str, MultiLegRoute] = {}

        # Process ships sequentially (greedy assignment)
        for i, ship_data in enumerate(ships, 1):
            ship_symbol = ship_data['symbol']
            self.logger.info(f"\n--- Optimizing Ship {i}/{len(ships)}: {ship_symbol} ---")

            # Get ship parameters
            start_waypoint = ship_data['nav']['waypointSymbol']
            cargo_capacity = ship_data['cargo']['capacity']
            ship_speed = ship_data['engine']['speed']
            fuel_capacity = ship_data['fuel']['capacity']
            current_fuel = ship_data['fuel']['current']

            # CRITICAL FIX: Extract actual ship cargo (may have residual from previous operations)
            starting_cargo = {item['symbol']: item['units']
                             for item in ship_data['cargo']['inventory']}

            # Create single-ship optimizer
            optimizer = MultiLegTradeOptimizer(
                api=self.api,
                db=self.db,
                player_id=self.player_id,
                logger=self.logger,
            )

            # Get all markets in system
            markets = optimizer._get_markets_in_system(system)

            # Get ALL trade opportunities (before filtering)
            all_opportunities = optimizer._get_trade_opportunities(system, markets)
            self.logger.info(f"  Total opportunities available: {len(all_opportunities)}")

            # CRITICAL: Filter out opportunities that would cause conflicts
            filtered_opportunities = self._filter_conflicting_opportunities(
                all_opportunities,
                reserved_resource_waypoints
            )

            conflicts_avoided = len(all_opportunities) - len(filtered_opportunities)
            self.logger.info(f"  Conflicts avoided: {conflicts_avoided}")
            self.logger.info(f"  Remaining opportunities: {len(filtered_opportunities)}")

            if not filtered_opportunities:
                self.logger.warning(f"  No conflict-free opportunities for {ship_symbol}")
                continue

            # Find best route using filtered opportunities
            route = self._find_ship_route(
                start_waypoint=start_waypoint,
                markets=markets,
                trade_opportunities=filtered_opportunities,
                max_stops=max_stops,
                cargo_capacity=cargo_capacity,
                starting_credits=starting_credits,
                ship_speed=ship_speed,
                starting_cargo=starting_cargo,
            )

            if not route:
                self.logger.warning(f"  No profitable route found for {ship_symbol}")
                continue

            if route.total_profit <= 0:
                self.logger.warning(f"  Route unprofitable for {ship_symbol}: {route.total_profit:,} cr")
                continue

            # SUCCESS: Assign route to ship
            ship_routes[ship_symbol] = route

            # Reserve (resource, waypoint) BUY pairs from this route
            new_reservations = self._extract_buy_pairs(route)
            self.logger.info(f"  ✅ Route assigned: {route.total_profit:,} cr profit")
            self.logger.info(f"  Reserved {len(new_reservations)} (resource, waypoint) pairs:")
            for good, waypoint in new_reservations:
                self.logger.info(f"     - {good} @ {waypoint}")
                reserved_resource_waypoints.add((good, waypoint))

        if not ship_routes:
            self.logger.error("No profitable conflict-free routes found for any ship")
            return None

        # Calculate fleet totals
        total_fleet_profit = sum(route.total_profit for route in ship_routes.values())

        self.logger.info("\n" + "="*70)
        self.logger.info("FLEET OPTIMIZATION COMPLETE")
        self.logger.info("="*70)
        self.logger.info(f"Ships with routes: {len(ship_routes)}/{len(ships)}")
        self.logger.info(f"Total fleet profit: {total_fleet_profit:,} cr")
        self.logger.info(f"Reserved (resource, waypoint) pairs: {len(reserved_resource_waypoints)}")
        self.logger.info("="*70)

        return {
            'ship_routes': ship_routes,
            'total_fleet_profit': total_fleet_profit,
            'reserved_pairs': reserved_resource_waypoints,
            'conflicts': 0,  # Conflicts were avoided, not detected after assignment
        }

    def _filter_conflicting_opportunities(
        self,
        opportunities: List[Dict],
        reserved_pairs: set[tuple[str, str]]
    ) -> List[Dict]:
        """
        Filter trade opportunities to remove those that would cause conflicts

        Args:
            opportunities: List of trade opportunity dicts
            reserved_pairs: Set of (resource, waypoint) BUY pairs already assigned

        Returns:
            Filtered list with conflicting opportunities removed
        """
        filtered = []

        for opp in opportunities:
            # Check if this opportunity's BUY location conflicts
            buy_pair = (opp['good'], opp['buy_waypoint'])

            if buy_pair in reserved_pairs:
                # CONFLICT: Another ship already buys this resource at this waypoint
                continue

            # No conflict - include this opportunity
            filtered.append(opp)

        return filtered

    def _extract_buy_pairs(self, route: MultiLegRoute) -> set[tuple[str, str]]:
        """
        Extract all (resource, waypoint) BUY pairs from a route

        Args:
            route: MultiLegRoute to analyze

        Returns:
            Set of (good, waypoint) tuples representing BUY actions
        """
        buy_pairs = set()

        for segment in route.segments:
            for action in segment.actions_at_destination:
                if action.action == 'BUY':
                    pair = (action.good, action.waypoint)
                    buy_pairs.add(pair)

        return buy_pairs

    def _find_ship_route(
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
        Find optimal route for a single ship using greedy planner

        Args:
            start_waypoint: Ship's current location
            markets: Available markets
            trade_opportunities: Filtered trade opportunities (conflicts removed)
            max_stops: Maximum route stops
            cargo_capacity: Ship cargo capacity
            starting_credits: Available credits
            ship_speed: Ship speed for time estimation
            starting_cargo: Existing cargo from previous operations (residual)

        Returns:
            MultiLegRoute if found, None otherwise
        """
        strategy = ProfitFirstStrategy(self.logger)
        planner = GreedyRoutePlanner(self.logger, self.db, strategy=strategy)

        route = planner.find_route(
            start_waypoint=start_waypoint,
            markets=markets,
            trade_opportunities=trade_opportunities,
            max_stops=max_stops,
            cargo_capacity=cargo_capacity,
            starting_credits=starting_credits,
            ship_speed=ship_speed,
            starting_cargo=starting_cargo,
        )

        return route
