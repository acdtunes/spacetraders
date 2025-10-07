#!/usr/bin/env python3
from __future__ import annotations

"""
SpaceTraders Routing Engine - Ship-aware pathfinding with fuel constraints

Provides intelligent route planning that:
- Accounts for ship speed differences (engine variations)
- Handles fuel constraints (capacity, consumption, refueling)
- Chooses optimal flight modes (CRUISE vs DRIFT)
- Inserts refuel stops when needed
- Solves multi-stop tour optimization (TSP)

Author: Claude Code
"""

import json
import logging
import math
from heapq import heappop, heappush
from pathlib import Path
from typing import Dict, List, Optional, Set, Tuple

from ..helpers import paths
from .database import get_database

logger = logging.getLogger(__name__)


# =============================================================================
# CONSTANTS & FORMULAS
# =============================================================================

# Travel time formula: round((distance × mode_multiplier) / engine_speed)
FLIGHT_MODE_MULTIPLIERS = {
    'CRUISE': 31,      # Confirmed via empirical testing
    'DRIFT': 26,       # Empirically measured: 166u = 476s, 616u = 1754s
    'BURN': 15,        # ~2x faster than CRUISE (estimated)
    'STEALTH': 50      # Estimated
}

# Fuel consumption estimates (fuel per unit distance)
FUEL_CONSUMPTION = {
    'CRUISE': 1.0,     # ~1 fuel per unit
    'DRIFT': 0.003,    # ~1 fuel per 300 units
    'BURN': 2.0,       # ~2 fuel per unit (high consumption)
}

# Safety margins
FUEL_SAFETY_MARGIN = 0.1  # Keep 10% fuel reserve
REFUEL_TIME = 5  # Estimated seconds for refueling


# =============================================================================
# UTILITY FUNCTIONS
# =============================================================================

def euclidean_distance(x1: float, y1: float, x2: float, y2: float) -> float:
    """Calculate Euclidean distance between two points"""
    return math.sqrt((x2 - x1) ** 2 + (y2 - y1) ** 2)


def parse_waypoint_symbol(symbol: str) -> Tuple[str, str]:
    """
    Parse waypoint symbol into system and waypoint

    Example: 'X1-HU87-A1' → ('X1-HU87', 'X1-HU87-A1')
    """
    parts = symbol.split('-')
    if len(parts) >= 3:
        system = f"{parts[0]}-{parts[1]}"
        return system, symbol
    return symbol, symbol


# =============================================================================
# CALCULATORS
# =============================================================================

class TimeCalculator:
    """Calculate travel times based on ship speed and flight mode"""

    @staticmethod
    def travel_time(distance: float, engine_speed: int, mode: str = 'CRUISE') -> int:
        """
        Calculate travel time in seconds

        Formula: round((distance × mode_multiplier) / engine_speed)

        Args:
            distance: Euclidean distance in units
            engine_speed: Ship's engine speed attribute
            mode: Flight mode (CRUISE, DRIFT, BURN, STEALTH)

        Returns:
            Travel time in seconds
        """
        if distance == 0:
            return 0

        multiplier = FLIGHT_MODE_MULTIPLIERS.get(mode, 31)
        time_seconds = round((distance * multiplier) / engine_speed)
        return max(1, time_seconds)  # Minimum 1 second

    @staticmethod
    def format_time(seconds: int) -> str:
        """Format seconds into human-readable time"""
        if seconds < 60:
            return f"{seconds}s"
        elif seconds < 3600:
            return f"{seconds // 60}m {seconds % 60}s"
        else:
            hours = seconds // 3600
            minutes = (seconds % 3600) // 60
            return f"{hours}h {minutes}m"


class FuelCalculator:
    """Calculate fuel consumption for different flight modes"""

    @staticmethod
    def fuel_cost(distance: float, mode: str = 'CRUISE') -> int:
        """
        Calculate fuel cost for a journey

        Args:
            distance: Distance in units
            mode: Flight mode

        Returns:
            Fuel cost in units (rounded up)
        """
        if distance == 0:
            return 0

        consumption_rate = FUEL_CONSUMPTION.get(mode, 1.0)
        fuel_needed = distance * consumption_rate
        return max(1, math.ceil(fuel_needed))

    @staticmethod
    def can_afford(distance: float, current_fuel: int, mode: str = 'CRUISE',
                   safety_margin: float = FUEL_SAFETY_MARGIN) -> bool:
        """Check if ship has enough fuel for journey with safety margin"""
        fuel_needed = FuelCalculator.fuel_cost(distance, mode)
        min_fuel_required = fuel_needed * (1 + safety_margin)
        return current_fuel >= min_fuel_required


# =============================================================================
# GRAPH BUILDER
# =============================================================================

class GraphBuilder:
    """Build system navigation graphs from SpaceTraders API and save to database"""

    def __init__(self, api_client, db_path: Optional[Path | str] = None):
        self.api = api_client
        resolved_db_path = Path(db_path) if db_path else paths.sqlite_path()
        self.db = get_database(resolved_db_path)
        self.logger = logging.getLogger(__name__ + '.GraphBuilder')

    def build_system_graph(self, system_symbol: str) -> Dict:
        """
        Build complete navigation graph for a system and save to database

        Args:
            system_symbol: System to build graph for (e.g., 'X1-HU87')

        Returns:
            Graph dictionary
        """
        self.logger.info(f"Building graph for system {system_symbol}...")

        # Fetch all waypoints (with pagination support)
        all_waypoints = []
        page = 1
        limit = 20  # API max per page

        while True:
            # Fetch page
            result = self.api.list_waypoints(system_symbol, limit=limit, page=page)
            if not result or 'data' not in result:
                break

            waypoints_page = result['data']
            all_waypoints.extend(waypoints_page)

            self.logger.info(f"  Fetched page {page}: {len(waypoints_page)} waypoints")

            # Check if there are more pages
            meta = result.get('meta', {})
            total_pages = meta.get('total', 0) // limit + (1 if meta.get('total', 0) % limit > 0 else 0)

            if page >= total_pages or len(waypoints_page) < limit:
                break

            page += 1

            # Safety: don't fetch more than 50 pages (1000 waypoints)
            if page > 50:
                self.logger.warning("Reached safety limit of 50 pages")
                break

        if not all_waypoints:
            self.logger.error(f"No waypoints found for system {system_symbol}")
            return None

        waypoints = all_waypoints
        self.logger.info(f"Total waypoints fetched: {len(waypoints)}")

        # Build graph structure
        graph = {
            "system": system_symbol,
            "waypoints": {},
            "edges": []
        }

        # Process waypoints
        for wp in waypoints:
            # Check if waypoint has fuel (marketplace)
            traits = [t['symbol'] for t in wp.get('traits', [])]
            has_fuel = 'MARKETPLACE' in traits or 'FUEL_STATION' in traits

            graph["waypoints"][wp['symbol']] = {
                "type": wp['type'],
                "x": wp['x'],
                "y": wp['y'],
                "traits": traits,
                "has_fuel": has_fuel,
                "orbitals": [o['symbol'] for o in wp.get('orbitals', [])]
            }

        # Build edges
        waypoint_list = list(graph["waypoints"].keys())
        for i, wp1 in enumerate(waypoint_list):
            wp1_data = graph["waypoints"][wp1]

            for wp2 in waypoint_list[i+1:]:
                wp2_data = graph["waypoints"][wp2]

                # Check if orbital relationship (0 distance)
                is_orbital = wp2 in wp1_data.get('orbitals', []) or \
                             wp1 in wp2_data.get('orbitals', [])

                if is_orbital:
                    distance = 0
                    edge_type = "orbital"
                else:
                    distance = euclidean_distance(
                        wp1_data['x'], wp1_data['y'],
                        wp2_data['x'], wp2_data['y']
                    )
                    edge_type = "normal"

                # Add edge (undirected - add both directions for convenience)
                edge = {
                    "from": wp1,
                    "to": wp2,
                    "distance": round(distance, 2),
                    "type": edge_type
                }
                graph["edges"].append(edge)

        # Save graph to database
        with self.db.transaction() as conn:
            self.db.save_system_graph(conn, system_symbol, graph)

        self.logger.info(f"Graph saved to database")
        self.logger.info(f"  Waypoints: {len(graph['waypoints'])}")
        self.logger.info(f"  Edges: {len(graph['edges'])}")
        self.logger.info(f"  Fuel stations: {sum(1 for wp in graph['waypoints'].values() if wp['has_fuel'])}")

        return graph

    def load_system_graph(self, system_symbol: str) -> Optional[Dict]:
        """
        Load system graph from database

        Args:
            system_symbol: System to load graph for

        Returns:
            Graph dictionary or None if not found
        """
        with self.db.connection() as conn:
            graph = self.db.get_system_graph(conn, system_symbol)

            if graph:
                self.logger.info(f"Loaded graph for {system_symbol} from database")
                self.logger.info(f"  Waypoints: {len(graph.get('waypoints', {}))}")
                self.logger.info(f"  Edges: {len(graph.get('edges', []))}")
            else:
                self.logger.warning(f"No graph found for {system_symbol} in database")

            return graph


# =============================================================================
# ROUTE OPTIMIZER
# =============================================================================

class RouteOptimizer:
    """
    Ship-aware route optimization with fuel constraints

    Uses A* pathfinding with state = (waypoint, fuel_level)
    """

    def __init__(self, graph: Dict, ship_data: Dict):
        """
        Initialize route optimizer

        Args:
            graph: System graph from GraphBuilder
            ship_data: Ship data containing engine.speed, fuel.capacity, etc.
        """
        self.graph = graph
        self.ship = ship_data
        self.engine_speed = ship_data['engine']['speed']
        self.fuel_capacity = ship_data['fuel']['capacity']
        self.logger = logging.getLogger(__name__ + '.RouteOptimizer')

        # Build adjacency list for faster lookups
        self.adjacency = self._build_adjacency()

    def _build_adjacency(self) -> Dict[str, List[Tuple[str, float]]]:
        """Build adjacency list from edges"""
        adj = {}
        for wp in self.graph['waypoints']:
            adj[wp] = []

        for edge in self.graph['edges']:
            adj[edge['from']].append((edge['to'], edge['distance']))
            adj[edge['to']].append((edge['from'], edge['distance']))  # Undirected

        return adj

    def heuristic(self, wp: str, goal: str) -> int:
        """A* heuristic: optimistic time estimate (CRUISE, straight line)"""
        wp_data = self.graph['waypoints'][wp]
        goal_data = self.graph['waypoints'][goal]

        distance = euclidean_distance(
            wp_data['x'], wp_data['y'],
            goal_data['x'], goal_data['y']
        )

        return TimeCalculator.travel_time(distance, self.engine_speed, 'CRUISE')

    def find_optimal_route(self, start: str, goal: str, current_fuel: int,
                          prefer_cruise: bool = True) -> Optional[Dict]:
        """
        Find optimal route from start to goal with fuel constraints using A* algorithm

        Uses A* pathfinding with state = (waypoint, fuel_level) to find optimal routes.
        The algorithm explores three types of actions:
        1. Refuel at current waypoint (if has_fuel=True)
        2. Navigate to neighbor using CRUISE mode (fast, high fuel cost)
        3. Navigate to neighbor using DRIFT mode (slow, low fuel cost)

        Cost Function Strategy:
        - When prefer_cruise=True:
          * CRUISE routes use actual travel time as cost
          * DRIFT routes have 5x time penalty applied to cost (but not actual time)
          * This makes A* strongly prefer routes that enable CRUISE travel
          * Refuel stops become cheap compared to slow DRIFT travel
          * Result: Algorithm inserts refuel stops along route to enable CRUISE mode

        - When prefer_cruise=False:
          * Both modes use actual travel time as cost
          * Algorithm optimizes for fuel efficiency
          * Result: Minimal refueling, maximizes DRIFT usage

        Why This Works:
        The 5x penalty on DRIFT cost (when prefer_cruise=True) makes the algorithm
        perceive DRIFT routes as much longer than they actually are. This causes A*
        to explore alternative paths that include:
        - Refuel stops at start position (if has_fuel)
        - Refuel stops at waypoints along the route
        - Multiple refuel stops for very long journeys

        The key insight: A 5-second refuel stop + 5-minute CRUISE leg appears cheaper
        to A* than a 25-minute DRIFT leg (5min * 5x penalty = 25min cost).

        Args:
            start: Starting waypoint symbol
            goal: Destination waypoint symbol
            current_fuel: Current fuel level
            prefer_cruise: If True, strongly prefer CRUISE mode (insert refuel stops as needed)
                          If False, optimize for fuel efficiency (minimize refueling)

        Returns:
            Route plan dict with steps, or None if no route found

        Example Route Plans:
            Scenario 1: Ship at B17 with 97 fuel, going to H48 (303 units away)
            - With prefer_cruise=True:
              1. Refuel at B17 (+303 fuel)
              2. Navigate B17 → H48 (CRUISE, 303u, 5m)

            Scenario 2: Ship at A1 with 50 fuel, going to C1 via fuel station B1
            - With prefer_cruise=True:
              1. Navigate A1 → B1 (DRIFT, 100u, conserve fuel)
              2. Refuel at B1 (+351 fuel)
              3. Navigate B1 → C1 (CRUISE, 200u, fast)
        """
        self.logger.info(f"Planning route: {start} → {goal}")
        self.logger.info(f"Ship speed: {self.engine_speed}, Fuel: {current_fuel}/{self.fuel_capacity}")

        # Check if waypoints exist in graph
        if start not in self.graph['waypoints']:
            self.logger.error(f"Start waypoint {start} not in graph")
            return None
        if goal not in self.graph['waypoints']:
            self.logger.error(f"Goal waypoint {goal} not in graph")
            return None

        # Priority queue: (estimated_total_time, counter, current_time, waypoint, fuel, path)
        # Counter serves as tiebreaker to avoid comparing complex objects
        counter = 0
        queue = [(self.heuristic(start, goal), counter, 0, start, current_fuel, [])]

        # Visited states: (waypoint, fuel_bucket)
        # Use fuel buckets to reduce state space (bucket = fuel // 50)
        visited = set()

        best_route = None
        iterations = 0
        max_iterations = 10000

        while queue and iterations < max_iterations:
            iterations += 1
            est_total, _, current_time, wp, fuel, path = heappop(queue)
            counter += 1

            # Goal check
            if wp == goal:
                route = {
                    "start": start,
                    "goal": goal,
                    "total_time": current_time,
                    "final_fuel": fuel,
                    "steps": path,
                    "ship_speed": self.engine_speed
                }
                self.logger.info(f"Route found in {iterations} iterations")
                return route

            # Visited check (with fuel bucketing to reduce state space)
            fuel_bucket = fuel // 50
            state = (wp, fuel_bucket)
            if state in visited:
                continue
            visited.add(state)

            wp_data = self.graph['waypoints'][wp]

            # Action 1: Refuel (if at fuel station and not full)
            if wp_data['has_fuel'] and fuel < self.fuel_capacity:
                refuel_amount = self.fuel_capacity - fuel
                new_path = path + [{"action": "refuel", "waypoint": wp, "fuel_added": refuel_amount}]

                heappush(queue, (
                    current_time + REFUEL_TIME + self.heuristic(wp, goal),
                    counter,
                    current_time + REFUEL_TIME,
                    wp,
                    self.fuel_capacity,
                    new_path
                ))
                counter += 1

            # Action 2: Navigate to neighbors
            for neighbor, distance in self.adjacency[wp]:
                # Special case: 0 fuel capacity ships (probes) don't consume fuel
                if self.fuel_capacity == 0:
                    drift_time = TimeCalculator.travel_time(distance, self.engine_speed, 'DRIFT')
                    new_path = path + [{
                        "action": "navigate",
                        "from": wp,
                        "to": neighbor,
                        "mode": "DRIFT",
                        "distance": distance,
                        "fuel_cost": 0,
                        "time": drift_time
                    }]

                    heappush(queue, (
                        current_time + drift_time + self.heuristic(neighbor, goal),
                        counter,
                        current_time + drift_time,
                        neighbor,
                        0,  # Probes always have 0 fuel
                        new_path
                    ))
                    counter += 1
                    continue

                # Calculate fuel and time for both modes
                cruise_fuel = FuelCalculator.fuel_cost(distance, 'CRUISE')
                cruise_time = TimeCalculator.travel_time(distance, self.engine_speed, 'CRUISE')
                drift_fuel = FuelCalculator.fuel_cost(distance, 'DRIFT')
                drift_time = TimeCalculator.travel_time(distance, self.engine_speed, 'DRIFT')

                # Try CRUISE if fuel allows
                # Always explore CRUISE option when we have enough fuel
                if fuel >= cruise_fuel * (1 + FUEL_SAFETY_MARGIN):
                    new_fuel = fuel - cruise_fuel
                    new_path = path + [{
                        "action": "navigate",
                        "from": wp,
                        "to": neighbor,
                        "mode": "CRUISE",
                        "distance": distance,
                        "fuel_cost": cruise_fuel,
                        "time": cruise_time
                    }]

                    # CRUISE cost: Use actual travel time
                    # This is naturally fast, so it's preferred when fuel allows
                    # Example: 100u @ speed 30 = ~2min travel time
                    cruise_cost = current_time + cruise_time + self.heuristic(neighbor, goal)

                    heappush(queue, (
                        cruise_cost,
                        counter,
                        current_time + cruise_time,
                        neighbor,
                        new_fuel,
                        new_path
                    ))
                    counter += 1

                # Try DRIFT if fuel allows
                # Always explore DRIFT option (very low fuel cost)
                if fuel >= drift_fuel:
                    new_fuel = fuel - drift_fuel
                    new_path = path + [{
                        "action": "navigate",
                        "from": wp,
                        "to": neighbor,
                        "mode": "DRIFT",
                        "distance": distance,
                        "fuel_cost": drift_fuel,
                        "time": drift_time
                    }]

                    # DRIFT cost calculation depends on prefer_cruise flag
                    if prefer_cruise:
                        # Apply 5x time penalty to DRIFT cost (but NOT actual time!)
                        # This makes A* perceive DRIFT as much slower than it really is
                        # Example: 100u DRIFT @ speed 30 = ~5min actual, but 25min cost
                        #
                        # Why this works:
                        # - Refuel stop = 5s cost
                        # - CRUISE 100u = ~2min cost (actual time)
                        # - DRIFT 100u = ~25min cost (5min * 5x penalty)
                        # → A* prefers: refuel (5s) + CRUISE (2min) = 2:05 cost
                        #   over: DRIFT = 25min cost
                        #
                        # Result: Algorithm inserts refuel stops to enable CRUISE mode
                        drift_cost = current_time + (drift_time * 5) + self.heuristic(neighbor, goal)
                    else:
                        # Fuel-efficient mode: Use actual time without penalty
                        # DRIFT becomes attractive due to minimal fuel consumption
                        drift_cost = current_time + drift_time + self.heuristic(neighbor, goal)

                    heappush(queue, (
                        drift_cost,
                        counter,
                        current_time + drift_time,  # Store actual time (not penalized)
                        neighbor,
                        new_fuel,
                        new_path
                    ))
                    counter += 1

        self.logger.warning(f"No route found after {iterations} iterations")
        return None


# =============================================================================
# TOUR OPTIMIZER (Multi-Stop TSP)
# =============================================================================

class TourOptimizer:
    """
    Solve multi-stop tour optimization (Traveling Salesman Problem variant)
    with fuel constraints
    """

    def __init__(self, graph: Dict, ship_data: Dict, db_path: Optional[Path | str] = None):
        self.route_optimizer = RouteOptimizer(graph, ship_data)
        self.graph = graph
        self.ship = ship_data
        resolved_db_path = Path(db_path) if db_path else paths.sqlite_path()
        self.db = get_database(resolved_db_path)
        self.logger = logging.getLogger(__name__ + '.TourOptimizer')

    def _estimate_fuel_needed(self, from_wp: str, to_wp: str, mode: str = 'CRUISE') -> int:
        """Quick estimate of fuel needed for a leg (without full pathfinding)"""
        wp1 = self.graph['waypoints'][from_wp]
        wp2 = self.graph['waypoints'][to_wp]

        distance = euclidean_distance(wp1['x'], wp1['y'], wp2['x'], wp2['y'])
        return FuelCalculator.fuel_cost(distance, mode)

    def _calculate_tour_distance(self, tour_order: List[str]) -> float:
        """Calculate total distance of a tour given waypoint order"""
        if len(tour_order) < 2:
            return 0.0

        total_distance = 0.0
        for i in range(len(tour_order) - 1):
            wp1 = self.graph['waypoints'][tour_order[i]]
            wp2 = self.graph['waypoints'][tour_order[i + 1]]
            distance = euclidean_distance(wp1['x'], wp1['y'], wp2['x'], wp2['y'])
            total_distance += distance

        return total_distance

    def plan_tour(self, start: str, stops: List[str], current_fuel: int,
                  return_to_start: bool = False, algorithm: str = 'greedy',
                  use_cache: bool = True) -> Optional[Dict]:
        """
        Plan tour with caching support

        Args:
            start: Starting waypoint
            stops: List of waypoints to visit
            current_fuel: Current fuel level
            return_to_start: If True, return to start after visiting all stops
            algorithm: 'greedy' (nearest neighbor) or '2opt' (2-opt optimization)
            use_cache: If True, check cache before calculating and save results

        Returns:
            Tour plan dict or None if no route found
        """
        system = self.graph['system']

        # Check cache if enabled
        if use_cache:
            cache_start = start if not return_to_start else None
            with self.db.connection() as conn:
                cached = self.db.get_cached_tour(
                    conn, system, stops, algorithm, cache_start
                )
                if cached:
                    self.logger.info(
                        f"Cache HIT: Tour for {len(stops)} stops with {algorithm} "
                        f"(distance: {cached['total_distance']:.1f}, "
                        f"cached at {cached['calculated_at']})"
                    )
                    # Reconstruct full tour using cached waypoint order
                    tour_order = cached['tour_order']
                    return self._build_tour_from_order(
                        tour_order, current_fuel, return_to_start
                    )

            self.logger.info(f"Cache MISS: Calculating new tour...")

        # Calculate tour using selected algorithm
        if algorithm == 'greedy':
            tour = self.solve_nearest_neighbor(start, stops, current_fuel, return_to_start)
        elif algorithm == '2opt':
            # First get greedy solution
            greedy_tour = self.solve_nearest_neighbor(start, stops, current_fuel, return_to_start)
            if not greedy_tour:
                return None
            # Then optimize with 2-opt
            tour = self.two_opt_improve(greedy_tour)
        else:
            self.logger.error(f"Unknown algorithm: {algorithm}")
            return None

        # Save to cache if enabled and tour found
        if use_cache and tour:
            # Extract waypoint order from tour
            tour_order = [start] + tour['stops']
            if return_to_start:
                tour_order.append(start)

            total_distance = self._calculate_tour_distance(tour_order)

            cache_start = start if not return_to_start else None
            with self.db.transaction() as conn:
                self.db.save_tour_cache(
                    conn, system, stops, algorithm, tour_order, total_distance, cache_start
                )
            self.logger.info(
                f"Saved tour to cache: {len(stops)} stops, "
                f"distance: {total_distance:.1f}, algorithm: {algorithm}"
            )

        return tour

    def _build_tour_from_order(self, tour_order: List[str], current_fuel: int,
                               return_to_start: bool) -> Optional[Dict]:
        """
        Rebuild full tour plan from cached waypoint order

        Args:
            tour_order: Ordered list of waypoints
            current_fuel: Current fuel level
            return_to_start: Whether tour returns to start

        Returns:
            Full tour dict with routes and fuel planning
        """
        if len(tour_order) < 2:
            self.logger.error("Tour order must have at least 2 waypoints")
            return None

        start = tour_order[0]
        stops = tour_order[1:-1] if return_to_start else tour_order[1:]

        # Use the cached order to build the tour
        current = start
        fuel = current_fuel
        total_time = 0
        tour_steps = []

        # Visit stops in cached order
        for i, stop in enumerate(stops):
            route = self.route_optimizer.find_optimal_route(current, stop, fuel)
            if not route:
                self.logger.error(f"Cannot reach {stop} from {current} with {fuel} fuel")
                return None

            tour_steps.append(route)
            current = stop
            fuel = route['final_fuel']
            total_time += route['total_time']

            # Check if refuel needed for next leg
            if i < len(stops) - 1 or return_to_start:
                next_dest = stops[i + 1] if i < len(stops) - 1 else start
                fuel_needed = self._estimate_fuel_needed(current, next_dest, 'CRUISE')
                current_wp = self.graph['waypoints'][current]
                fuel_threshold = fuel_needed * (1 + FUEL_SAFETY_MARGIN)

                if fuel < fuel_threshold and current_wp['has_fuel']:
                    refuel_amount = self.ship['fuel']['capacity'] - fuel
                    self.logger.info(f"Refueling at {current} before next leg")

                    tour_steps[-1]['steps'].append({
                        'action': 'refuel',
                        'waypoint': current,
                        'fuel_added': refuel_amount
                    })
                    tour_steps[-1]['final_fuel'] = self.ship['fuel']['capacity']
                    tour_steps[-1]['total_time'] += REFUEL_TIME

                    fuel = self.ship['fuel']['capacity']
                    total_time += REFUEL_TIME

        # Return to start if requested
        if return_to_start:
            return_route = self.route_optimizer.find_optimal_route(current, start, fuel)
            if return_route:
                tour_steps.append(return_route)
                total_time += return_route['total_time']
                fuel = return_route['final_fuel']
            else:
                self.logger.error(f"Cannot return to start from {current}")
                return None

        tour = {
            "start": start,
            "stops": stops,
            "return_to_start": return_to_start,
            "total_time": total_time,
            "final_fuel": fuel,
            "legs": tour_steps,
            "total_legs": len(tour_steps)
        }

        return tour

    def solve_nearest_neighbor(self, start: str, stops: List[str],
                               current_fuel: int, return_to_start: bool = False) -> Optional[Dict]:
        """
        Solve TSP using nearest neighbor heuristic with lookahead fuel planning

        Args:
            start: Starting waypoint
            stops: List of waypoints to visit
            current_fuel: Current fuel level
            return_to_start: If True, return to start after visiting all stops

        Returns:
            Tour plan with ordered stops and routes
        """
        self.logger.info(f"Planning tour from {start} visiting {len(stops)} stops")

        remaining = set(stops)
        if return_to_start:
            # Add return leg to planning
            remaining_with_return = list(remaining) + [start]
        else:
            remaining_with_return = list(remaining)

        current = start
        fuel = current_fuel
        total_time = 0
        tour_steps = []

        # Visit each stop using nearest neighbor
        while remaining:
            # Find nearest unvisited stop
            best_stop = None
            best_route = None
            best_time = float('inf')

            for stop in remaining:
                route = self.route_optimizer.find_optimal_route(current, stop, fuel)
                if route and route['total_time'] < best_time:
                    best_stop = stop
                    best_route = route
                    best_time = route['total_time']

            if not best_route:
                self.logger.error(f"Cannot reach any remaining stops from {current}")
                return None

            # Add this leg to tour
            tour_steps.append(best_route)
            remaining.remove(best_stop)
            current = best_stop
            fuel = best_route['final_fuel']
            total_time += best_route['total_time']

            # LOOKAHEAD: Check if we need to refuel for the next leg
            if remaining or return_to_start:
                # Determine next destination
                if remaining:
                    # Peek at closest next stop (greedy approximation)
                    next_stops = list(remaining)
                    if return_to_start and len(remaining) == 0:
                        next_dest = start
                    else:
                        next_dest = next_stops[0]  # Approximation
                else:
                    next_dest = start  # Return leg

                # Estimate fuel needed for next leg
                fuel_needed = self._estimate_fuel_needed(current, next_dest, 'CRUISE')

                # Check if we need to refuel NOW (with safety margin)
                current_wp = self.graph['waypoints'][current]
                fuel_threshold = fuel_needed * (1 + FUEL_SAFETY_MARGIN)

                if fuel < fuel_threshold and current_wp['has_fuel']:
                    # Insert refuel action
                    refuel_amount = self.ship['fuel']['capacity'] - fuel
                    self.logger.info(f"Refueling at {current} before next leg (need {fuel_needed}, have {fuel})")

                    # Add refuel step to last route
                    tour_steps[-1]['steps'].append({
                        'action': 'refuel',
                        'waypoint': current,
                        'fuel_added': refuel_amount
                    })
                    tour_steps[-1]['final_fuel'] = self.ship['fuel']['capacity']
                    tour_steps[-1]['total_time'] += REFUEL_TIME

                    fuel = self.ship['fuel']['capacity']
                    total_time += REFUEL_TIME

        # Return to start if requested
        if return_to_start:
            return_route = self.route_optimizer.find_optimal_route(current, start, fuel)
            if return_route:
                tour_steps.append(return_route)
                total_time += return_route['total_time']
                fuel = return_route['final_fuel']
            else:
                self.logger.error(f"Cannot return to start from {current}")
                return None

        tour = {
            "start": start,
            "stops": stops,
            "return_to_start": return_to_start,
            "total_time": total_time,
            "final_fuel": fuel,
            "legs": tour_steps,
            "total_legs": len(tour_steps)
        }

        self.logger.info(f"Tour complete: {total_time}s total time, final fuel: {fuel}")
        return tour

    def two_opt_improve(self, tour: Dict, max_iterations: int = 1000) -> Dict:
        """
        Improve tour using 2-Opt local search algorithm

        Complexity: O(n² × k) where k = iterations until no improvement

        Args:
            tour: Tour dict from solve_nearest_neighbor()
            max_iterations: Maximum optimization iterations

        Returns:
            Improved tour dict
        """
        self.logger.info(f"Running 2-Opt optimization (max {max_iterations} iterations)...")

        stops = tour['stops'].copy()
        start = tour['start']
        return_to_start = tour['return_to_start']
        current_fuel = self.ship['fuel']['current']

        best_tour = tour
        best_time = tour['total_time']
        improved = True
        iterations = 0
        improvements = 0

        while improved and iterations < max_iterations:
            improved = False
            iterations += 1

            # Try swapping edges by reversing segments
            for i in range(len(stops) - 1):
                for j in range(i + 2, len(stops) + 1):
                    # Create new tour by reversing segment [i:j]
                    new_stops = stops[:i] + stops[i:j][::-1] + stops[j:]

                    # Evaluate new tour
                    new_tour = self.solve_nearest_neighbor(
                        start, new_stops, current_fuel, return_to_start
                    )

                    if new_tour and new_tour['total_time'] < best_time:
                        best_tour = new_tour
                        best_time = new_tour['total_time']
                        stops = new_stops
                        improved = True
                        improvements += 1
                        self.logger.info(f"  Improvement #{improvements}: {best_time}s (saved {tour['total_time'] - best_time}s)")
                        break

                if improved:
                    break

        if tour['total_time'] > 0:
            improvement_pct = ((tour['total_time'] - best_time) / tour['total_time']) * 100
            self.logger.info(f"2-Opt complete: {iterations} iterations, {improvements} improvements, {improvement_pct:.1f}% faster")
        else:
            self.logger.info(f"2-Opt complete: {iterations} iterations, {improvements} improvements")

        return best_tour

    @staticmethod
    def get_markets_from_graph(graph: Dict) -> List[str]:
        """
        Auto-discover all markets in a system graph

        Args:
            graph: System graph from GraphBuilder

        Returns:
            List of waypoint symbols with marketplaces
        """
        markets = []
        for wp_symbol, wp_data in graph['waypoints'].items():
            if 'MARKETPLACE' in wp_data.get('traits', []):
                markets.append(wp_symbol)
        return markets
