"""
OR-Tools routing engine - ported from Python bot implementation.

Implements:
- Dijkstra pathfinding with fuel constraints
- TSP (Traveling Salesman Problem) optimization
- VRP (Vehicle Routing Problem) fleet partitioning
"""
import heapq
import logging
import math
from typing import Dict, List, Optional, Tuple, Any
from dataclasses import dataclass
from enum import Enum

from ortools.constraint_solver import routing_enums_pb2
from ortools.constraint_solver import pywrapcp

logger = logging.getLogger(__name__)


class FlightMode(Enum):
    """Flight modes with time/fuel characteristics"""
    CRUISE = ("CRUISE", 31, 1.0)     # Fast, standard fuel
    DRIFT = ("DRIFT", 26, 0.003)     # Slow, minimal fuel
    BURN = ("BURN", 15, 2.0)         # Very fast, high fuel
    STEALTH = ("STEALTH", 50, 1.0)   # Very slow, stealthy

    def __init__(self, mode_name: str, time_multiplier: int, fuel_rate: float):
        self.mode_name = mode_name
        self.time_multiplier = time_multiplier
        self.fuel_rate = fuel_rate

    def fuel_cost(self, distance: float) -> int:
        """Calculate fuel cost for given distance"""
        if distance == 0:
            return 0
        return max(1, math.ceil(distance * self.fuel_rate))

    def travel_time(self, distance: float, engine_speed: int) -> int:
        """Calculate travel time in seconds"""
        if distance == 0:
            return 0
        return max(1, int((distance * self.time_multiplier) / max(1, engine_speed)))


@dataclass
class Waypoint:
    """Waypoint representation for routing"""
    symbol: str
    x: float
    y: float
    has_fuel: bool
    fuel_price: Optional[int] = None
    orbitals: Tuple[str, ...] = ()

    def distance_to(self, other: 'Waypoint') -> float:
        """Calculate Euclidean distance to another waypoint"""
        return math.hypot(other.x - self.x, other.y - self.y)

    def is_orbital_of(self, other: 'Waypoint') -> bool:
        """Check if this waypoint orbits another"""
        return other.symbol in self.orbitals or self.symbol in other.orbitals


class ORToolsRoutingEngine:
    """
    Routing engine using OR-Tools for optimization.

    Ported from Python bot's ortools_engine.py
    """

    # Constants for orbital travel
    ORBITAL_HOP_TIME = 1  # seconds for orbital transfer
    ORBITAL_HOP_DISTANCE = 0.0  # no distance for orbital hops

    def __init__(self, tsp_timeout: int = 5, vrp_timeout: int = 30):
        """
        Initialize routing engine.

        Args:
            tsp_timeout: Timeout in seconds for TSP (tour optimization) solver
            vrp_timeout: Timeout in seconds for VRP (fleet partitioning) solver
        """
        self._pathfinding_cache: Dict[Tuple[str, str, int, int], Optional[Dict[str, Any]]] = {}
        self._tsp_timeout = tsp_timeout
        self._vrp_timeout = vrp_timeout

    def clear_cache(self):
        """Clear the pathfinding cache"""
        self._pathfinding_cache.clear()
        logger.debug("Pathfinding cache cleared")

    def calculate_fuel_cost(self, distance: float, mode: FlightMode) -> int:
        """Calculate fuel cost using FlightMode's built-in method"""
        return mode.fuel_cost(distance)

    def calculate_travel_time(self, distance: float, mode: FlightMode, engine_speed: int) -> int:
        """Calculate travel time using FlightMode's built-in method"""
        return mode.travel_time(distance, engine_speed)

    def find_optimal_path(
        self,
        graph: Dict[str, Waypoint],
        start: str,
        goal: str,
        current_fuel: int,
        fuel_capacity: int,
        engine_speed: int
    ) -> Optional[Dict[str, Any]]:
        """
        Find optimal path using Dijkstra with fuel constraints.

        Returns dict with:
        - steps: List of route steps (TRAVEL or REFUEL actions)
        - total_fuel_cost: Total fuel consumed
        - total_time: Total time in seconds
        - total_distance: Total distance traveled
        """
        logger.info(f"Finding path: {start} -> {goal}, fuel={current_fuel}/{fuel_capacity}")

        if start not in graph or goal not in graph:
            logger.error(f"Start or goal not in graph")
            return None

        if start == goal:
            return {
                'steps': [],
                'total_fuel_cost': 0,
                'total_time': 0,
                'total_distance': 0.0
            }

        # Special case: fuel_capacity=0 ships (probe satellites)
        if fuel_capacity == 0:
            return self._find_path_no_fuel(graph, start, goal, engine_speed)

        # Priority queue: (total_time, counter, waypoint, fuel_remaining, total_fuel_used, path)
        pq: List[Tuple[int, int, str, int, int, List[Dict[str, Any]]]] = []
        counter = 0
        heapq.heappush(pq, (0, counter, start, current_fuel, 0, []))
        counter += 1

        # Track best cost to reach each (waypoint, fuel_level) state
        visited: Dict[Tuple[str, int], int] = {}

        while pq:
            total_time, _, current, fuel_remaining, total_fuel_used, path = heapq.heappop(pq)

            # Goal check
            if current == goal:
                return {
                    'steps': path,
                    'total_fuel_cost': total_fuel_used,
                    'total_time': total_time,
                    'total_distance': sum(step.get('distance', 0) for step in path if step['action'] == 'TRAVEL')
                }

            # State deduplication
            state = (current, fuel_remaining // 10)
            if state in visited and visited[state] <= total_time:
                continue
            visited[state] = total_time

            current_wp = graph[current]

            # Check if at start with insufficient fuel
            SAFETY_MARGIN = 4
            at_start_with_low_fuel = (
                current == start and
                len(path) == 0 and
                current_wp.has_fuel and
                fuel_remaining < fuel_capacity
            )

            if at_start_with_low_fuel and goal in graph:
                # 90% rule at start - always refuel when below 90% capacity
                fuel_threshold = int(fuel_capacity * 0.9)
                should_refuel_90_at_start = fuel_remaining < fuel_threshold

                goal_wp = graph[goal]
                distance_to_goal = current_wp.distance_to(goal_wp)
                cruise_fuel_needed = self.calculate_fuel_cost(distance_to_goal, FlightMode.CRUISE)

                if fuel_remaining < cruise_fuel_needed or should_refuel_90_at_start:
                    # Force refuel first
                    refuel_amount = fuel_capacity - fuel_remaining
                    refuel_step = {
                        'action': 'REFUEL',
                        'waypoint': current,
                        'fuel_cost': 0,
                        'time': 0,
                        'refuel_amount': refuel_amount
                    }
                    new_path = path + [refuel_step]
                    heapq.heappush(pq, (
                        total_time,
                        counter,
                        current,
                        fuel_capacity,
                        total_fuel_used,
                        new_path
                    ))
                    counter += 1
                    continue

            # Option 1: Refuel at current waypoint (if has fuel)
            must_refuel = False
            should_refuel_90 = False

            if current_wp.has_fuel and fuel_remaining < fuel_capacity:
                # 90% rule - always refuel when below 90% capacity
                fuel_threshold = int(fuel_capacity * 0.9)
                should_refuel_90 = fuel_remaining < fuel_threshold

                # Check if MUST refuel
                if goal in graph:
                    goal_wp = graph[goal]
                    distance_to_goal = current_wp.distance_to(goal_wp)
                    cruise_fuel_needed = self.calculate_fuel_cost(distance_to_goal, FlightMode.CRUISE)

                    if fuel_remaining < cruise_fuel_needed:
                        must_refuel = True

                if should_refuel_90 or must_refuel:
                    refuel_amount = fuel_capacity - fuel_remaining
                    refuel_step = {
                        'action': 'REFUEL',
                        'waypoint': current,
                        'fuel_cost': 0,
                        'time': 0,
                        'refuel_amount': refuel_amount
                    }
                    new_path = path + [refuel_step]
                    heapq.heappush(pq, (
                        total_time,
                        counter,
                        current,
                        fuel_capacity,
                        total_fuel_used,
                        new_path
                    ))
                    counter += 1

                    if must_refuel or should_refuel_90:
                        continue

            # Option 2: Travel to neighboring waypoints
            for neighbor_symbol, neighbor in graph.items():
                if neighbor_symbol == current:
                    continue

                distance = current_wp.distance_to(neighbor)

                # Check for orbital hop
                is_orbital = current_wp.is_orbital_of(neighbor) or distance == 0.0

                if is_orbital:
                    distance = self.ORBITAL_HOP_DISTANCE
                    travel_time = self.ORBITAL_HOP_TIME
                    fuel_cost = 0
                    mode = FlightMode.CRUISE
                else:
                    # Select flight mode: NEVER use DRIFT
                    SAFETY_MARGIN = 4
                    is_goal = (neighbor_symbol == goal)

                    burn_cost = self.calculate_fuel_cost(distance, FlightMode.BURN)
                    cruise_cost = self.calculate_fuel_cost(distance, FlightMode.CRUISE)

                    if fuel_remaining >= burn_cost + SAFETY_MARGIN:
                        mode = FlightMode.BURN
                        fuel_cost = burn_cost
                    elif fuel_remaining >= cruise_cost + SAFETY_MARGIN:
                        mode = FlightMode.CRUISE
                        fuel_cost = cruise_cost
                    elif is_goal and fuel_remaining >= cruise_cost:
                        # Allow exact fuel for final hop
                        mode = FlightMode.CRUISE
                        fuel_cost = cruise_cost
                    else:
                        # Insufficient fuel - skip this neighbor
                        continue

                    travel_time = self.calculate_travel_time(distance, mode, engine_speed)

                if fuel_cost > fuel_remaining:
                    continue

                # Create travel step
                travel_step = {
                    'action': 'TRAVEL',
                    'waypoint': neighbor_symbol,
                    'fuel_cost': fuel_cost,
                    'time': travel_time,
                    'mode': mode.mode_name,
                    'distance': distance
                }

                new_path = path + [travel_step]
                new_fuel = fuel_remaining - fuel_cost
                new_time = total_time + travel_time
                new_fuel_used = total_fuel_used + fuel_cost

                heapq.heappush(pq, (
                    new_time,
                    counter,
                    neighbor_symbol,
                    new_fuel,
                    new_fuel_used,
                    new_path
                ))
                counter += 1

        logger.error(f"No path found from {start} to {goal}")
        return None

    def _find_path_no_fuel(
        self,
        graph: Dict[str, Waypoint],
        start: str,
        goal: str,
        engine_speed: int
    ) -> Optional[Dict[str, Any]]:
        """Find path for ships with fuel_capacity=0 (probe satellites)"""
        if start not in graph or goal not in graph:
            return None

        start_wp = graph[start]
        goal_wp = graph[goal]

        distance = start_wp.distance_to(goal_wp)
        is_orbital = start_wp.is_orbital_of(goal_wp) or distance == 0.0

        if is_orbital:
            distance = self.ORBITAL_HOP_DISTANCE
            time = self.ORBITAL_HOP_TIME
            mode = FlightMode.CRUISE
        else:
            mode = FlightMode.CRUISE
            time = self.calculate_travel_time(distance, mode, engine_speed)

        travel_step = {
            'action': 'TRAVEL',
            'waypoint': goal,
            'distance': distance,
            'fuel_cost': 0,
            'time': time,
            'mode': mode.mode_name
        }

        return {
            'steps': [travel_step],
            'total_fuel_cost': 0,
            'total_time': time,
            'total_distance': distance
        }

    def optimize_tour(
        self,
        graph: Dict[str, Waypoint],
        waypoints: List[str],
        start: str,
        return_to_start: bool,
        fuel_capacity: int,
        engine_speed: int
    ) -> Optional[Dict[str, Any]]:
        """
        Optimize multi-waypoint tour using OR-Tools TSP solver.

        Returns dict with:
        - ordered_waypoints: Optimized visit order
        - legs: List of route legs between waypoints
        - total_distance: Total distance
        - total_fuel_cost: Total fuel
        - total_time: Total time
        """
        if start not in graph:
            return None

        # Remove start from waypoints if present to avoid duplicates
        # This happens when a ship's current location is also one of the markets to visit
        waypoints_without_start = [wp for wp in waypoints if wp != start]
        all_waypoints = [start] + waypoints_without_start
        for wp in all_waypoints:
            if wp not in graph:
                return None

        if len(all_waypoints) == 1:
            return {
                'ordered_waypoints': [start],
                'legs': [],
                'total_distance': 0.0,
                'total_fuel_cost': 0,
                'total_time': 0
            }

        # Build distance matrix
        n = len(all_waypoints)
        distance_matrix = [[0] * n for _ in range(n)]

        for i, wp1_symbol in enumerate(all_waypoints):
            wp1 = graph[wp1_symbol]
            for j, wp2_symbol in enumerate(all_waypoints):
                if i == j:
                    continue
                wp2 = graph[wp2_symbol]

                distance = wp1.distance_to(wp2)
                is_orbital = wp1.is_orbital_of(wp2) or distance == 0.0

                if is_orbital:
                    distance_matrix[i][j] = 1
                else:
                    # Scale to integer for OR-Tools
                    distance_matrix[i][j] = int(distance * 100)

        # Create OR-Tools routing model
        manager = pywrapcp.RoutingIndexManager(n, 1, 0)
        routing = pywrapcp.RoutingModel(manager)

        def distance_callback(from_index: int, to_index: int) -> int:
            from_node = manager.IndexToNode(from_index)
            to_node = manager.IndexToNode(to_index)
            return distance_matrix[from_node][to_node]

        transit_callback_index = routing.RegisterTransitCallback(distance_callback)
        routing.SetArcCostEvaluatorOfAllVehicles(transit_callback_index)

        # Add constraint: must visit all waypoints
        for node in range(1, n):
            routing.AddDisjunction([manager.NodeToIndex(node)], 999999)

        # Configure solver
        search_parameters = pywrapcp.DefaultRoutingSearchParameters()
        search_parameters.first_solution_strategy = (
            routing_enums_pb2.FirstSolutionStrategy.PATH_CHEAPEST_ARC
        )
        search_parameters.local_search_metaheuristic = (
            routing_enums_pb2.LocalSearchMetaheuristic.GUIDED_LOCAL_SEARCH
        )
        search_parameters.time_limit.seconds = self._tsp_timeout

        # Solve
        solution = routing.SolveWithParameters(search_parameters)

        if not solution:
            return None

        # Extract solution
        ordered_waypoints = []
        legs = []
        total_distance = 0.0
        total_fuel_cost = 0
        total_time = 0

        index = routing.Start(0)
        while not routing.IsEnd(index):
            node = manager.IndexToNode(index)
            ordered_waypoints.append(all_waypoints[node])

            next_index = solution.Value(routing.NextVar(index))
            if not routing.IsEnd(next_index):
                next_node = manager.IndexToNode(next_index)

                from_wp = graph[all_waypoints[node]]
                to_wp = graph[all_waypoints[next_node]]

                distance = from_wp.distance_to(to_wp)
                is_orbital = from_wp.is_orbital_of(to_wp) or distance == 0.0

                if is_orbital:
                    distance = self.ORBITAL_HOP_DISTANCE
                    time = self.ORBITAL_HOP_TIME
                    fuel_cost = 0
                    mode = FlightMode.CRUISE
                else:
                    # Select flight mode: NEVER use DRIFT mode
                    # Ships should ALWAYS use BURN or CRUISE, inserting refuel stops as needed
                    # Use fastest mode that maintains 4-unit safety margin
                    SAFETY_MARGIN = 4
                    current_fuel = fuel_capacity

                    burn_cost = self.calculate_fuel_cost(distance, FlightMode.BURN)
                    cruise_cost = self.calculate_fuel_cost(distance, FlightMode.CRUISE)

                    if current_fuel >= burn_cost + SAFETY_MARGIN:
                        mode = FlightMode.BURN
                    elif current_fuel >= cruise_cost + SAFETY_MARGIN:
                        mode = FlightMode.CRUISE
                    else:
                        # If insufficient fuel even for CRUISE, use CRUISE anyway
                        # (TSP doesn't handle refueling - caller must ensure ship has fuel)
                        mode = FlightMode.CRUISE

                    fuel_cost = self.calculate_fuel_cost(distance, mode)
                    time = self.calculate_travel_time(distance, mode, engine_speed)

                legs.append({
                    'from': all_waypoints[node],
                    'to': all_waypoints[next_node],
                    'distance': distance,
                    'fuel_cost': fuel_cost,
                    'time': time,
                    'mode': mode.mode_name
                })

                total_distance += distance
                total_fuel_cost += fuel_cost
                total_time += time

            index = next_index

        # Handle return to start if requested
        if return_to_start and ordered_waypoints[-1] != start:
            from_wp = graph[ordered_waypoints[-1]]
            to_wp = graph[start]

            distance = from_wp.distance_to(to_wp)
            is_orbital = from_wp.is_orbital_of(to_wp) or distance == 0.0

            if is_orbital:
                distance = self.ORBITAL_HOP_DISTANCE
                time = self.ORBITAL_HOP_TIME
                fuel_cost = 0
                mode = FlightMode.CRUISE
            else:
                # Select flight mode: NEVER use DRIFT mode
                # Use fastest mode that maintains 4-unit safety margin
                SAFETY_MARGIN = 4
                current_fuel = fuel_capacity

                burn_cost = self.calculate_fuel_cost(distance, FlightMode.BURN)
                cruise_cost = self.calculate_fuel_cost(distance, FlightMode.CRUISE)

                if current_fuel >= burn_cost + SAFETY_MARGIN:
                    mode = FlightMode.BURN
                elif current_fuel >= cruise_cost + SAFETY_MARGIN:
                    mode = FlightMode.CRUISE
                else:
                    # If insufficient fuel even for CRUISE, use CRUISE anyway
                    mode = FlightMode.CRUISE

                fuel_cost = self.calculate_fuel_cost(distance, mode)
                time = self.calculate_travel_time(distance, mode, engine_speed)

            legs.append({
                'from': ordered_waypoints[-1],
                'to': start,
                'distance': distance,
                'fuel_cost': fuel_cost,
                'time': time,
                'mode': mode.mode_name
            })

            ordered_waypoints.append(start)
            total_distance += distance
            total_fuel_cost += fuel_cost
            total_time += time

        return {
            'ordered_waypoints': ordered_waypoints,
            'legs': legs,
            'total_distance': total_distance,
            'total_fuel_cost': total_fuel_cost,
            'total_time': total_time
        }

    def optimize_fleet_tour(
        self,
        graph: Dict[str, Waypoint],
        markets: List[str],
        ship_locations: Dict[str, str],
        fuel_capacity: int,
        engine_speed: int
    ) -> Optional[Dict[str, List[str]]]:
        """
        Partition markets across multiple ships using multi-vehicle VRP.

        Returns dict mapping ship_symbol -> List[assigned_markets]
        """
        if not markets or not ship_locations:
            return {ship: [] for ship in ship_locations.keys()}

        ships = list(ship_locations.keys())
        nodes = list(markets)
        node_index = {node: idx for idx, node in enumerate(nodes)}

        # Add ship starting locations
        starts = []
        ends = []
        for ship in ships:
            waypoint = ship_locations[ship]
            if waypoint not in node_index:
                node_index[waypoint] = len(nodes)
                nodes.append(waypoint)
            index = node_index[waypoint]
            starts.append(index)
            ends.append(index)

        # Build distance matrix
        distance_matrix = self._build_distance_matrix_for_vrp(
            nodes, graph, fuel_capacity, engine_speed
        )

        # Calculate disjunction penalty
        max_distance_cost = 0
        for row in distance_matrix:
            max_distance_cost = max(max_distance_cost, max(row))

        disjunction_penalty = max(max_distance_cost * 10, 10_000_000)

        # Create OR-Tools VRP model
        manager = pywrapcp.RoutingIndexManager(
            len(nodes),
            len(ships),
            starts,
            ends
        )
        routing = pywrapcp.RoutingModel(manager)

        def distance_callback(from_index: int, to_index: int) -> int:
            from_node = manager.IndexToNode(from_index)
            to_node = manager.IndexToNode(to_index)
            return distance_matrix[from_node][to_node]

        transit_callback_index = routing.RegisterTransitCallback(distance_callback)
        routing.SetArcCostEvaluatorOfAllVehicles(transit_callback_index)

        # Add travel time dimension
        routing.AddDimension(
            transit_callback_index,
            0,
            disjunction_penalty,
            True,
            "TravelTime"
        )
        time_dimension = routing.GetDimensionOrDie("TravelTime")
        time_dimension.SetGlobalSpanCostCoefficient(100)

        # Add disjunction constraints for markets
        for market in markets:
            routing.AddDisjunction(
                [manager.NodeToIndex(node_index[market])],
                disjunction_penalty
            )

        # Search parameters
        search_parameters = pywrapcp.DefaultRoutingSearchParameters()
        search_parameters.first_solution_strategy = (
            routing_enums_pb2.FirstSolutionStrategy.PATH_CHEAPEST_ARC
        )
        search_parameters.local_search_metaheuristic = (
            routing_enums_pb2.LocalSearchMetaheuristic.GUIDED_LOCAL_SEARCH
        )
        search_parameters.time_limit.seconds = self._vrp_timeout

        # Solve
        solution = routing.SolveWithParameters(search_parameters)
        if not solution:
            return {ship: [] for ship in ships}

        # Extract assignments
        assignments: Dict[str, List[str]] = {ship: [] for ship in ships}
        assigned_waypoints: set = set()

        for vehicle, ship in enumerate(ships):
            # DON'T add start waypoint here - let optimize_tour() handle it!
            # The start location will be added by optimize_tour as position 0 and return position
            # If we add it here, it appears 3 times: once from VRP, once at TSP start, once at TSP end

            # Extract markets from the route
            index = routing.Start(vehicle)
            while not routing.IsEnd(index):
                node = manager.IndexToNode(index)
                waypoint = nodes[node]

                if waypoint in markets:
                    if waypoint not in assigned_waypoints:
                        assignments[ship].append(waypoint)
                        assigned_waypoints.add(waypoint)

                index = solution.Value(routing.NextVar(index))

        # Verify all markets were assigned
        dropped_markets = set(markets) - assigned_waypoints
        if dropped_markets:
            logger.warning(f"VRP dropped {len(dropped_markets)} markets: {dropped_markets}")

        return assignments

    def _build_distance_matrix_for_vrp(
        self,
        nodes: List[str],
        graph: Dict[str, Waypoint],
        fuel_capacity: int,
        engine_speed: int
    ) -> List[List[int]]:
        """Build distance/time matrix for VRP with pathfinding cache"""
        size = len(nodes)
        matrix = [[1_000_000 for _ in range(size)] for _ in range(size)]

        cache_hits = 0
        cache_misses = 0

        for i, origin in enumerate(nodes):
            for j, target in enumerate(nodes):
                if i == j:
                    matrix[i][j] = 0
                    continue

                if origin not in graph or target not in graph:
                    continue

                # Check cache
                cache_key = (origin, target, fuel_capacity, engine_speed)
                if cache_key in self._pathfinding_cache:
                    route = self._pathfinding_cache[cache_key]
                    cache_hits += 1
                else:
                    # Compute pathfinding
                    route = self.find_optimal_path(
                        graph=graph,
                        start=origin,
                        goal=target,
                        current_fuel=fuel_capacity,
                        fuel_capacity=fuel_capacity,
                        engine_speed=engine_speed
                    )
                    self._pathfinding_cache[cache_key] = route
                    cache_misses += 1

                if route and route.get('total_time'):
                    matrix[i][j] = route['total_time']

        logger.info(f"Distance matrix cache: {cache_hits} hits, {cache_misses} misses")
        return matrix
