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
        engine_speed: int,
        fuel_efficient: bool = False,
        prefer_cruise: bool = False
    ) -> Optional[Dict[str, Any]]:
        """
        Find optimal path using Dijkstra with fuel constraints.

        Args:
            fuel_efficient: When True, removes DRIFT penalty to allow DRIFT-assisted routes
                           for fuel preservation (used by mining transports)
            prefer_cruise: When True, prefer CRUISE over BURN for fuel efficiency
                          (DRIFT penalty still applies unless fuel_efficient is True)

        Returns dict with:
        - steps: List of route steps (TRAVEL or REFUEL actions)
        - total_fuel_cost: Total fuel consumed
        - total_time: Total time in seconds
        - total_distance: Total distance traveled
        """
        logger.info(f"Finding path: {start} -> {goal}, fuel={current_fuel}/{fuel_capacity}, fuel_efficient={fuel_efficient}, prefer_cruise={prefer_cruise}")

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
            # Add refuel option to queue - Dijkstra will determine if it's optimal
            if current_wp.has_fuel and fuel_remaining < fuel_capacity:
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
            # Don't force refuel - continue to explore all neighbor options
            # Dijkstra will find the optimal path by comparing total travel times

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

                    # Create travel step for orbital hop
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
                    continue

                # Explore viable flight modes for this neighbor
                # NEVER use DRIFT except as last resort for final destination
                SAFETY_MARGIN = 4
                is_goal = (neighbor_symbol == goal)

                burn_cost = self.calculate_fuel_cost(distance, FlightMode.BURN)
                cruise_cost = self.calculate_fuel_cost(distance, FlightMode.CRUISE)

                # Build list of viable modes (mode, fuel_cost)
                viable_modes = []

                # Check BURN (skip if prefer_cruise is set)
                if not prefer_cruise:
                    if fuel_remaining >= burn_cost + SAFETY_MARGIN:
                        viable_modes.append((FlightMode.BURN, burn_cost))
                    elif is_goal and fuel_remaining >= burn_cost:
                        viable_modes.append((FlightMode.BURN, burn_cost))

                # Check CRUISE
                if fuel_remaining >= cruise_cost + SAFETY_MARGIN:
                    viable_modes.append((FlightMode.CRUISE, cruise_cost))
                elif is_goal and fuel_remaining >= cruise_cost:
                    viable_modes.append((FlightMode.CRUISE, cruise_cost))

                # DRIFT: Only as absolute last resort with massive time penalty
                # This ensures BURN/CRUISE paths are always preferred
                if len(viable_modes) == 0:
                    drift_cost = self.calculate_fuel_cost(distance, FlightMode.DRIFT)
                    if fuel_remaining >= drift_cost:
                        viable_modes.append((FlightMode.DRIFT, drift_cost))

                # Skip if no viable modes
                if not viable_modes:
                    continue

                # Add a path for each viable mode to let Dijkstra find optimal
                for mode, fuel_cost in viable_modes:
                    travel_time = self.calculate_travel_time(distance, mode, engine_speed)

                    # Add massive penalty to DRIFT so it's only chosen as last resort
                    # UNLESS fuel_efficient mode is enabled (for mining transports)
                    if mode == FlightMode.DRIFT and not fuel_efficient:
                        travel_time += 100000  # 100k second penalty

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

        # Build complete waypoint list: start + targets
        all_waypoints = [start] + waypoints
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

        # TSP: All waypoints are mandatory by default (no need for AddDisjunction)
        # The routing model with num_vehicles=1 automatically creates a Hamiltonian path
        # that visits all nodes exactly once

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

        return {
            'ordered_waypoints': ordered_waypoints,
            'legs': legs,
            'total_distance': total_distance,
            'total_fuel_cost': total_fuel_cost,
            'total_time': total_time
        }

    def optimize_fueled_tour(
        self,
        graph: Dict[str, Waypoint],
        waypoints: List[str],
        start: str,
        return_waypoint: Optional[str],
        current_fuel: int,
        fuel_capacity: int,
        engine_speed: int
    ) -> Optional[Dict[str, Any]]:
        """
        Optimize tour with global fuel constraints using time-based TSP.

        This builds a cost matrix using actual fuel-constrained travel times,
        then solves TSP to minimize total travel time while tracking fuel state.

        Returns dict with:
        - ordered_waypoints: Optimized visit order
        - legs: List of TourLeg dicts with flight mode, refuel flags, etc.
        - total_time: Total travel time
        - total_fuel_cost: Total fuel consumed
        - total_distance: Total distance
        - refuel_stops: Number of refuel stops
        """
        logger.info(f"OptimizeFueledTour: start={start}, waypoints={waypoints}, return={return_waypoint}")

        if start not in graph:
            logger.error(f"Start waypoint {start} not in graph")
            return None

        # Build complete node list
        nodes = [start] + waypoints
        if return_waypoint and return_waypoint not in nodes:
            nodes.append(return_waypoint)

        for wp in nodes:
            if wp not in graph:
                logger.error(f"Waypoint {wp} not in graph")
                return None

        # Trivial case: no waypoints to visit
        if len(waypoints) == 0:
            return {
                'ordered_waypoints': [start],
                'legs': [],
                'total_time': 0,
                'total_fuel_cost': 0,
                'total_distance': 0.0,
                'refuel_stops': 0
            }

        # Build time-based cost matrix using Dijkstra pathfinding
        # Each cost[i][j] = actual travel time from i to j with full fuel
        n = len(nodes)
        cost_matrix = [[0] * n for _ in range(n)]
        path_cache = {}  # Cache pathfinding results: (from, to) -> path_result

        logger.info(f"Building {n}x{n} fuel-aware cost matrix")

        for i, from_symbol in enumerate(nodes):
            for j, to_symbol in enumerate(nodes):
                if i == j:
                    continue

                # Use Dijkstra to find optimal path with fuel constraints
                # Assume full fuel at start of each leg (will refuel at markets)
                path_result = self.find_optimal_path(
                    graph, from_symbol, to_symbol,
                    current_fuel=fuel_capacity,  # Assume full tank
                    fuel_capacity=fuel_capacity,
                    engine_speed=engine_speed
                )

                if path_result:
                    cost_matrix[i][j] = path_result['total_time']
                    path_cache[(from_symbol, to_symbol)] = path_result
                else:
                    # Unreachable - use large penalty
                    cost_matrix[i][j] = 1_000_000
                    logger.warning(f"No path found from {from_symbol} to {to_symbol}")

        # Create OR-Tools TSP routing model
        manager = pywrapcp.RoutingIndexManager(n, 1, 0)
        routing = pywrapcp.RoutingModel(manager)

        def time_callback(from_index: int, to_index: int) -> int:
            from_node = manager.IndexToNode(from_index)
            to_node = manager.IndexToNode(to_index)
            return cost_matrix[from_node][to_node]

        transit_callback_index = routing.RegisterTransitCallback(time_callback)
        routing.SetArcCostEvaluatorOfAllVehicles(transit_callback_index)

        # If return_waypoint is specified and different from start,
        # we need to force the tour to end at return_waypoint
        if return_waypoint and return_waypoint in nodes and return_waypoint != start:
            return_idx = nodes.index(return_waypoint)
            # Create a model where the end is at return_waypoint
            # This is tricky with OR-Tools, so we'll handle it by including
            # return_waypoint in the tour and just not requiring return to start
            pass  # Standard TSP will work since we included return_waypoint in nodes

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
            logger.error("TSP solver found no solution")
            return None

        # Extract ordered waypoints from solution
        ordered_nodes = []
        index = routing.Start(0)
        while not routing.IsEnd(index):
            node = manager.IndexToNode(index)
            ordered_nodes.append(nodes[node])
            index = solution.Value(routing.NextVar(index))

        # If we have a return waypoint, append it if not already last
        if return_waypoint and (not ordered_nodes or ordered_nodes[-1] != return_waypoint):
            ordered_nodes.append(return_waypoint)

        logger.info(f"TSP solution: {ordered_nodes}")

        # Build legs with fuel tracking
        legs = []
        fuel_state = current_fuel  # Track actual fuel state
        total_time = 0
        total_fuel_cost = 0
        total_distance = 0.0
        refuel_stops = 0

        for i in range(len(ordered_nodes) - 1):
            from_wp = ordered_nodes[i]
            to_wp = ordered_nodes[i + 1]

            # For the first leg, always compute path with actual current fuel
            # since cached paths assume full fuel which may not match reality
            if i == 0:
                logger.info(f"Computing first leg {from_wp} -> {to_wp} with actual fuel {fuel_state}")
                path_result = self.find_optimal_path(
                    graph, from_wp, to_wp,
                    current_fuel=fuel_state,
                    fuel_capacity=fuel_capacity,
                    engine_speed=engine_speed
                )
                if not path_result:
                    logger.error(f"No path found for first leg {from_wp} -> {to_wp}")
                    return None
            else:
                # Get cached path result for subsequent legs
                path_key = (from_wp, to_wp)
                if path_key not in path_cache:
                    # Compute path if not cached (shouldn't happen)
                    path_result = self.find_optimal_path(
                        graph, from_wp, to_wp,
                        current_fuel=fuel_capacity,
                        fuel_capacity=fuel_capacity,
                        engine_speed=engine_speed
                    )
                    if not path_result:
                        logger.error(f"No path found for leg {from_wp} -> {to_wp}")
                        continue
                else:
                    path_result = path_cache[path_key]

            # Determine if we need to refuel before this leg
            refuel_before = False
            refuel_amount = 0

            # Check if we have enough fuel for this leg
            leg_fuel_cost = path_result['total_fuel_cost']
            if fuel_state < leg_fuel_cost:
                # Need to refuel - check if current location has fuel
                if graph[from_wp].has_fuel:
                    refuel_before = True
                    refuel_amount = fuel_capacity - fuel_state
                    fuel_state = fuel_capacity
                    refuel_stops += 1
                    logger.info(f"Refueling {refuel_amount} at {from_wp} before leg to {to_wp}")
                else:
                    # Can't refuel here - try re-computing path with actual fuel state
                    # This may find a path that uses less fuel (e.g., via refuel stops)
                    logger.info(f"Re-computing path {from_wp} -> {to_wp} with actual fuel {fuel_state}")
                    recomputed_path = self.find_optimal_path(
                        graph, from_wp, to_wp,
                        current_fuel=fuel_state,
                        fuel_capacity=fuel_capacity,
                        engine_speed=engine_speed
                    )
                    if recomputed_path:
                        path_result = recomputed_path
                        leg_fuel_cost = path_result['total_fuel_cost']
                        logger.info(f"Re-computed path uses {leg_fuel_cost} fuel")
                    else:
                        logger.error(f"No valid path from {from_wp} to {to_wp} with {fuel_state} fuel")
                        return None

            # Extract flight mode from path steps
            flight_mode = "CRUISE"  # Default
            intermediate_stops = []

            for step in path_result['steps']:
                if step['action'] == 'TRAVEL':
                    flight_mode = step.get('mode', 'CRUISE')
                elif step['action'] == 'REFUEL':
                    intermediate_stops.append({
                        'waypoint': step['waypoint'],
                        'flight_mode': 'CRUISE',  # Mode before this stop
                        'fuel_cost': 0,
                        'time_seconds': step.get('time', 0),
                        'refuel_amount': step.get('refuel_amount', fuel_capacity)
                    })
                    refuel_stops += 1

            # Update fuel state
            fuel_state -= leg_fuel_cost

            # Build leg
            leg = {
                'from_waypoint': from_wp,
                'to_waypoint': to_wp,
                'flight_mode': flight_mode,
                'fuel_cost': leg_fuel_cost,
                'time_seconds': path_result['total_time'],
                'distance': path_result['total_distance'],
                'refuel_before': refuel_before,
                'refuel_amount': refuel_amount if refuel_before else 0,
                'intermediate_stops': intermediate_stops
            }
            legs.append(leg)

            total_time += path_result['total_time']
            total_fuel_cost += leg_fuel_cost
            total_distance += path_result['total_distance']

        # Extract visit order (excluding start and return)
        ordered_waypoints = [wp for wp in ordered_nodes if wp not in [start, return_waypoint]]
        if not ordered_waypoints and ordered_nodes:
            # If all nodes are start/return, just use ordered_nodes without first
            ordered_waypoints = ordered_nodes[1:] if len(ordered_nodes) > 1 else []

        logger.info(f"FueledTour complete: {len(legs)} legs, {total_time}s, {refuel_stops} refuels")

        return {
            'ordered_waypoints': ordered_waypoints,
            'legs': legs,
            'total_time': total_time,
            'total_fuel_cost': total_fuel_cost,
            'total_distance': total_distance,
            'refuel_stops': refuel_stops
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
            # CRITICAL FIX: If ship starts AT a market, assign it immediately
            # OR-Tools VRP treats depot nodes as "already there" and doesn't include them in routes
            # This causes markets at ship locations to be dropped from assignments
            start_node = manager.IndexToNode(routing.Start(vehicle))
            start_waypoint = nodes[start_node]

            logger.info(f"[VRP] Processing {ship} (vehicle {vehicle}), starts at {start_waypoint}, assigned_waypoints={assigned_waypoints}")

            if start_waypoint in markets and start_waypoint not in assigned_waypoints:
                assignments[ship].append(start_waypoint)
                assigned_waypoints.add(start_waypoint)
                logger.info(f"[VRP] Ship {ship} starts at market {start_waypoint} - AUTO-ASSIGNED")
            elif start_waypoint in markets:
                logger.info(f"[VRP] Ship {ship} starts at market {start_waypoint} but ALREADY ASSIGNED to another ship - SKIPPING")

            # Extract markets from the route
            index = routing.Start(vehicle)
            while not routing.IsEnd(index):
                node = manager.IndexToNode(index)
                waypoint = nodes[node]

                if waypoint in markets:
                    if waypoint not in assigned_waypoints:
                        logger.info(f"[VRP] {ship} route includes market {waypoint} - ASSIGNING")
                        assignments[ship].append(waypoint)
                        assigned_waypoints.add(waypoint)
                    else:
                        logger.info(f"[VRP] {ship} route includes market {waypoint} but ALREADY ASSIGNED - SKIPPING")

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
