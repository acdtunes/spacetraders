"""OR-Tools based routing engine implementation"""
import heapq
import logging
import math
from typing import Optional, Dict, List, Any, Tuple, Set
from ortools.constraint_solver import routing_enums_pb2
from ortools.constraint_solver import pywrapcp

from domain.shared.value_objects import Waypoint, FlightMode
from ports.routing_engine import IRoutingEngine

logger = logging.getLogger(__name__)


class ORToolsRoutingEngine(IRoutingEngine):
    """
    Routing engine using OR-Tools for optimization.

    Features:
    - Dijkstra-based pathfinding with fuel constraints
    - OR-Tools constraint solver for TSP optimization
    - Support for orbital hops (zero distance, +1 time)
    - Automatic refuel stop insertion
    - CRUISE vs DRIFT mode selection
    - Pathfinding result caching for VRP distance matrix optimization
    """

    # Constants for orbital travel
    ORBITAL_HOP_TIME = 1  # seconds for orbital transfer
    ORBITAL_HOP_DISTANCE = 0.0  # no distance for orbital hops

    def __init__(self, tsp_timeout: int = 5, vrp_timeout: int = 30):
        """Initialize routing engine with pathfinding cache.

        Args:
            tsp_timeout: Timeout in seconds for TSP (tour optimization) solver
            vrp_timeout: Timeout in seconds for VRP (fleet partitioning) solver
        """
        # Cache for pathfinding results: (start, goal, fuel_capacity, engine_speed) -> route
        self._pathfinding_cache: Dict[Tuple[str, str, int, int], Optional[Dict[str, Any]]] = {}
        self._tsp_timeout = tsp_timeout
        self._vrp_timeout = vrp_timeout

    def clear_cache(self):
        """Clear the pathfinding cache. Useful for testing and long-running sessions."""
        self._pathfinding_cache.clear()
        logger.debug(f"Pathfinding cache cleared")

    def calculate_fuel_cost(self, distance: float, mode: FlightMode) -> int:
        """Calculate fuel cost using FlightMode's built-in method"""
        return mode.fuel_cost(distance)

    def calculate_travel_time(
        self,
        distance: float,
        mode: FlightMode,
        engine_speed: int
    ) -> int:
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
        prefer_cruise: bool = True
    ) -> Optional[Dict[str, Any]]:
        """
        Find optimal path using Dijkstra with fuel constraints.

        This implementation:
        1. Uses A* with fuel tracking
        2. Automatically inserts refuel stops when needed
        3. Considers orbital hops (distance=0, time=1)
        4. Prefers CRUISE mode when possible
        5. For fuel_capacity=0 ships (probes), uses simple direct pathfinding
        """
        import logging
        logger = logging.getLogger(__name__)

        logger.info(f"=== ROUTING ENGINE CALLED ===")
        logger.info(f"Start: {start}, Goal: {goal}")
        logger.info(f"Graph type: {type(graph)}, Graph size: {len(graph) if graph else 0}")
        logger.info(f"Fuel: {current_fuel}/{fuel_capacity}, Engine: {engine_speed}")

        if start not in graph or goal not in graph:
            logger.error(f"Start or goal not in graph! Start in graph: {start in graph}, Goal in graph: {goal in graph}")
            return None

        if start == goal:
            return {
                'steps': [],
                'total_fuel_cost': 0,
                'total_time': 0,
                'total_distance': 0.0
            }

        # Special case: fuel_capacity=0 ships (probe satellites, etc.)
        # These ships don't use fuel and can travel directly anywhere
        if fuel_capacity == 0:
            return self._find_path_no_fuel(graph, start, goal, engine_speed)

        # Priority queue: (total_time, counter, waypoint, fuel_remaining, total_fuel_used, path)
        # We prioritize by time to find fastest route
        # Counter ensures unique comparison and FIFO for equal priorities
        pq: List[Tuple[int, int, str, int, int, List[Dict[str, Any]]]] = []
        counter = 0  # Tie-breaker for heapq
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

            # State deduplication (prune if we've seen better)
            state = (current, fuel_remaining // 10)  # Discretize fuel for efficiency
            if state in visited and visited[state] <= total_time:
                continue
            visited[state] = total_time

            current_wp = graph[current]

            # Check if at start with insufficient fuel for optimal travel
            SAFETY_MARGIN = 4
            at_start_with_low_fuel = (
                current == start and
                len(path) == 0 and
                current_wp.has_fuel and
                fuel_remaining < fuel_capacity  # Don't force refuel if already full
            )

            # Calculate minimum fuel needed for direct path to goal (if visible)
            if at_start_with_low_fuel and goal in graph:
                # NEW: 90% rule at start - always refuel when below 90% capacity
                fuel_threshold = int(fuel_capacity * 0.9)
                should_refuel_90_at_start = fuel_remaining < fuel_threshold

                goal_wp = graph[goal]
                distance_to_goal = current_wp.distance_to(goal_wp)
                cruise_fuel_needed = self.calculate_fuel_cost(distance_to_goal, FlightMode.CRUISE)

                # If we can't make it in CRUISE mode OR below 90% capacity, MUST refuel first
                # Allow exact fuel for reaching the goal (no safety margin required)
                if fuel_remaining < cruise_fuel_needed or should_refuel_90_at_start:
                    # Force refuel first - don't explore travel options from start yet
                    refuel_amount = fuel_capacity - fuel_remaining
                    refuel_step = {
                        'action': 'REFUEL',
                        'waypoint': current,
                        'fuel_cost': 0,
                        'time': 0,
                        'amount': refuel_amount
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
                    continue  # Skip travel options from this state

            # Option 1: Refuel at current waypoint (if has fuel)
            # Implement 90% rule: always refuel when below 90% capacity
            must_refuel = False
            should_refuel_90 = False

            if current_wp.has_fuel and fuel_remaining < fuel_capacity:
                # NEW: 90% rule - always refuel when below 90% capacity
                fuel_threshold = int(fuel_capacity * 0.9)
                should_refuel_90 = fuel_remaining < fuel_threshold

                # Also check if MUST refuel (can't reach goal in CRUISE)
                if goal in graph:
                    goal_wp = graph[goal]
                    distance_to_goal = current_wp.distance_to(goal_wp)
                    cruise_fuel_needed = self.calculate_fuel_cost(distance_to_goal, FlightMode.CRUISE)

                    # If we can't make it in CRUISE mode, MUST refuel
                    # Allow exact fuel for reaching the goal (no safety margin required for goal)
                    if fuel_remaining < cruise_fuel_needed:
                        must_refuel = True

                # Add refuel option if we should or must refuel
                if should_refuel_90 or must_refuel:
                    refuel_amount = fuel_capacity - fuel_remaining
                    refuel_step = {
                        'action': 'REFUEL',
                        'waypoint': current,
                        'fuel_cost': 0,
                        'time': 0,  # Simplified: assume instant refuel
                        'amount': refuel_amount
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

                    # If MUST refuel or SHOULD refuel (90% rule), skip travel options (force refuel first)
                    if must_refuel or should_refuel_90:
                        continue

            # Option 2: Travel to neighboring waypoints
            for neighbor_symbol, neighbor in graph.items():
                if neighbor_symbol == current:
                    continue

                # Calculate distance
                distance = current_wp.distance_to(neighbor)

                # Check for orbital hop (parent↔child OR siblings at same coordinates)
                # Parent-child: detected via is_orbital_of() checking orbitals field
                # Siblings: detected via distance=0 (waypoints at same coordinates)
                is_orbital = current_wp.is_orbital_of(neighbor) or distance == 0.0

                if is_orbital:
                    distance = self.ORBITAL_HOP_DISTANCE
                    travel_time = self.ORBITAL_HOP_TIME
                    fuel_cost = 0
                    mode = FlightMode.CRUISE  # Mode doesn't matter for orbitals
                else:

                    # Select flight mode: NEVER use DRIFT mode
                    # Ships should ALWAYS use BURN or CRUISE, inserting refuel stops as needed
                    # Use fastest mode that maintains 4-unit safety margin
                    # Exception: Allow exact fuel when reaching the goal
                    SAFETY_MARGIN = 4
                    is_goal = (neighbor_symbol == goal)

                    # Try BURN first (fastest)
                    burn_cost = self.calculate_fuel_cost(distance, FlightMode.BURN)
                    cruise_cost = self.calculate_fuel_cost(distance, FlightMode.CRUISE)

                    if fuel_remaining >= burn_cost + SAFETY_MARGIN:
                        mode = FlightMode.BURN
                        fuel_cost = burn_cost
                    elif fuel_remaining >= cruise_cost + SAFETY_MARGIN:
                        mode = FlightMode.CRUISE
                        fuel_cost = cruise_cost
                    elif is_goal and fuel_remaining >= cruise_cost:
                        # Allow exact fuel for final hop to goal (no safety margin)
                        mode = FlightMode.CRUISE
                        fuel_cost = cruise_cost
                    else:
                        # Insufficient fuel for CRUISE with safety margin
                        # NEVER use DRIFT - skip this neighbor to force refuel stop
                        continue

                    travel_time = self.calculate_travel_time(distance, mode, engine_speed)

                # Check if we have enough fuel
                if fuel_cost > fuel_remaining:
                    continue

                # Create travel step
                travel_step = {
                    'action': 'TRAVEL',
                    'waypoint': neighbor_symbol,
                    'fuel_cost': fuel_cost,
                    'time': travel_time,
                    'mode': mode,
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

        # No path found
        logger.error(f"=== ROUTING ENGINE FAILED ===")
        logger.error(f"No path found from {start} to {goal}")
        logger.error(f"States explored: {len(visited)}, Counter: {counter}")
        logger.error(f"Graph has {len(graph)} waypoints")
        logger.error(f"Fuel stations in graph: {sum(1 for wp in graph.values() if wp.has_fuel)}")
        return None

    def _find_path_no_fuel(
        self,
        graph: Dict[str, Waypoint],
        start: str,
        goal: str,
        engine_speed: int
    ) -> Optional[Dict[str, Any]]:
        """
        Find path for ships with fuel_capacity=0 (probe satellites).

        These ships don't use fuel, so we just find the shortest path
        based on distance and time, without fuel constraints.
        """
        # Simple Dijkstra without fuel tracking
        pq: List[Tuple[int, int, str, List[Dict[str, Any]]]] = []
        counter = 0
        heapq.heappush(pq, (0, counter, start, []))
        counter += 1

        visited: Set[str] = set()

        while pq:
            total_time, _, current, path = heapq.heappop(pq)

            if current == goal:
                return {
                    'steps': path,
                    'total_fuel_cost': 0,  # No fuel used
                    'total_time': total_time,
                    'total_distance': sum(step.get('distance', 0) for step in path if step['action'] == 'TRAVEL')
                }

            if current in visited:
                continue
            visited.add(current)

            current_wp = graph[current]

            # Try all neighbors
            for neighbor_symbol, neighbor_wp in graph.items():
                if neighbor_symbol == current or neighbor_symbol in visited:
                    continue

                # Calculate distance
                distance = current_wp.distance_to(neighbor_wp)

                # Check for orbital hop (parent↔child OR siblings at same coordinates)
                is_orbital = current_wp.is_orbital_of(neighbor_wp) or distance == 0.0

                if is_orbital:
                    distance = self.ORBITAL_HOP_DISTANCE
                    time = self.ORBITAL_HOP_TIME
                    mode = FlightMode.CRUISE
                else:
                    # Use CRUISE mode for zero-fuel ships
                    mode = FlightMode.CRUISE
                    time = self.calculate_travel_time(distance, mode, engine_speed)

                # Create travel step
                travel_step = {
                    'action': 'TRAVEL',
                    'waypoint': neighbor_symbol,
                    'from': current,
                    'distance': distance,
                    'fuel_cost': 0,  # No fuel cost for zero-fuel ships
                    'time': time,
                    'mode': mode
                }

                new_path = path + [travel_step]
                new_time = total_time + time

                heapq.heappush(pq, (new_time, counter, neighbor_symbol, new_path))
                counter += 1

        # No path found
        return None

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

        This uses Google OR-Tools Constraint Programming solver to find
        the optimal order to visit all waypoints.
        """
        if start not in graph:
            return None

        # Validate all waypoints exist
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

                # Calculate distance
                distance = wp1.distance_to(wp2)

                # Check for orbital hop (parent↔child OR siblings at same coordinates)
                is_orbital = wp1.is_orbital_of(wp2) or distance == 0.0

                if is_orbital:
                    distance_matrix[i][j] = 1  # Use 1 unit to represent orbital hop
                else:
                    # Scale to integer for OR-Tools (multiply by 100 for precision)
                    distance_matrix[i][j] = int(distance * 100)

        # Create OR-Tools routing model
        manager = pywrapcp.RoutingIndexManager(n, 1, 0)  # n locations, 1 vehicle, depot at 0
        routing = pywrapcp.RoutingModel(manager)

        # Define distance callback
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

                # Calculate distance
                distance = from_wp.distance_to(to_wp)

                # Check for orbital hop (parent↔child OR siblings at same coordinates)
                is_orbital = from_wp.is_orbital_of(to_wp) or distance == 0.0

                if is_orbital:
                    distance = self.ORBITAL_HOP_DISTANCE
                    time = self.ORBITAL_HOP_TIME
                    fuel_cost = 0
                    mode = FlightMode.CRUISE
                else:

                    # Prioritize speed: try BURN first, then CRUISE, then DRIFT
                    SAFETY_MARGIN = 4
                    current_fuel = fuel_capacity  # Assume full tank for tour planning

                    burn_cost = self.calculate_fuel_cost(distance, FlightMode.BURN)
                    cruise_cost = self.calculate_fuel_cost(distance, FlightMode.CRUISE)

                    if current_fuel >= burn_cost + SAFETY_MARGIN:
                        mode = FlightMode.BURN
                    elif current_fuel >= cruise_cost + SAFETY_MARGIN:
                        mode = FlightMode.CRUISE
                    else:
                        mode = FlightMode.DRIFT

                    fuel_cost = self.calculate_fuel_cost(distance, mode)
                    time = self.calculate_travel_time(distance, mode, engine_speed)

                legs.append({
                    'from': all_waypoints[node],
                    'to': all_waypoints[next_node],
                    'distance': distance,
                    'fuel_cost': fuel_cost,
                    'time': time,
                    'mode': mode
                })

                total_distance += distance
                total_fuel_cost += fuel_cost
                total_time += time

            index = next_index

        # Handle return to start if requested
        if return_to_start and ordered_waypoints[-1] != start:
            from_wp = graph[ordered_waypoints[-1]]
            to_wp = graph[start]

            # Calculate distance
            distance = from_wp.distance_to(to_wp)

            # Check for orbital hop (parent↔child OR siblings at same coordinates)
            is_orbital = from_wp.is_orbital_of(to_wp) or distance == 0.0

            if is_orbital:
                distance = self.ORBITAL_HOP_DISTANCE
                time = self.ORBITAL_HOP_TIME
                fuel_cost = 0
                mode = FlightMode.CRUISE
            else:

                # Prioritize speed for return leg too
                SAFETY_MARGIN = 4
                current_fuel = fuel_capacity  # Assume full tank

                burn_cost = self.calculate_fuel_cost(distance, FlightMode.BURN)
                cruise_cost = self.calculate_fuel_cost(distance, FlightMode.CRUISE)

                if current_fuel >= burn_cost + SAFETY_MARGIN:
                    mode = FlightMode.BURN
                elif current_fuel >= cruise_cost + SAFETY_MARGIN:
                    mode = FlightMode.CRUISE
                else:
                    mode = FlightMode.DRIFT

                fuel_cost = self.calculate_fuel_cost(distance, mode)
                time = self.calculate_travel_time(distance, mode, engine_speed)

            legs.append({
                'from': ordered_waypoints[-1],
                'to': start,
                'distance': distance,
                'fuel_cost': fuel_cost,
                'time': time,
                'mode': mode
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

        Ported from reference implementation's ORToolsFleetPartitioner.
        """
        if not markets or not ship_locations:
            return {ship: [] for ship in ship_locations.keys()}

        ships = list(ship_locations.keys())
        nodes = list(markets)
        node_index = {node: idx for idx, node in enumerate(nodes)}

        # Add ship starting locations to node list
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

        # DEBUG: Log distance matrix
        logger.info(f"VRP Distance Matrix ({len(nodes)} nodes):")
        for i, origin in enumerate(nodes):
            logger.info(f"  {origin}: {distance_matrix[i]}")

        # CRITICAL FIX: Calculate maximum possible distance cost in system
        # This ensures disjunction penalty is ALWAYS higher than any market's cost
        # Prevents OR-Tools from dropping extreme outliers
        # Reference: ORToolsFleetPartitioner from spacetradersV2/bot
        max_distance_cost = 0
        for row in distance_matrix:
            max_distance_cost = max(max_distance_cost, max(row))

        # Set disjunction penalty = 10x max cost (makes markets essentially mandatory)
        # OR-Tools will only drop markets if literally impossible to reach (disconnected graph)
        disjunction_penalty = max(max_distance_cost * 10, 10_000_000)  # Minimum 10M to handle edge cases

        # Create OR-Tools VRP model
        manager = pywrapcp.RoutingIndexManager(
            len(nodes),
            len(ships),
            starts,
            ends
        )
        routing = pywrapcp.RoutingModel(manager)

        # Distance callback
        def distance_callback(from_index: int, to_index: int) -> int:
            from_node = manager.IndexToNode(from_index)
            to_node = manager.IndexToNode(to_index)
            return distance_matrix[from_node][to_node]

        transit_callback_index = routing.RegisterTransitCallback(distance_callback)
        routing.SetArcCostEvaluatorOfAllVehicles(transit_callback_index)

        # Add travel time dimension
        routing.AddDimension(
            transit_callback_index,
            0,  # No slack
            disjunction_penalty,
            True,  # Start cumul to zero
            "TravelTime"
        )
        time_dimension = routing.GetDimensionOrDie("TravelTime")
        # SetGlobalSpanCostCoefficient penalizes imbalance between vehicle loads
        # Higher value = more pressure to balance loads across ALL vehicles
        # CRITICAL: Use 10000 (not 100) to force all vehicles to be used
        # With high coefficient, solver minimizes max_route_time - min_route_time
        # This forces balanced distribution across ALL available ships
        time_dimension.SetGlobalSpanCostCoefficient(10000)

        # Add disjunction constraints for markets (makes them optional but penalized)
        for market in markets:
            routing.AddDisjunction(
                [manager.NodeToIndex(node_index[market])],
                disjunction_penalty
            )

        # Search parameters
        search_parameters = pywrapcp.DefaultRoutingSearchParameters()
        # Use PATH_CHEAPEST_ARC for reliability (faster convergence than PARALLEL)
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

            if start_waypoint in markets and start_waypoint not in assigned_waypoints:
                assignments[ship].append(start_waypoint)
                assigned_waypoints.add(start_waypoint)

            # Extract markets from the route
            index = routing.Start(vehicle)
            while not routing.IsEnd(index):
                node = manager.IndexToNode(index)
                waypoint = nodes[node]

                # Only assign markets (not already assigned)
                if waypoint in markets:
                    if waypoint not in assigned_waypoints:
                        assignments[ship].append(waypoint)
                        assigned_waypoints.add(waypoint)

                index = solution.Value(routing.NextVar(index))

        # Verify all markets were assigned
        dropped_markets = set(markets) - assigned_waypoints
        if dropped_markets:
            raise Exception(
                f"VRP dropped {len(dropped_markets)} markets: {dropped_markets}. "
                f"This should not happen with proper disjunction penalty."
            )

        return assignments

    def _build_distance_matrix_for_vrp(
        self,
        nodes: List[str],
        graph: Dict[str, Waypoint],
        fuel_capacity: int,
        engine_speed: int
    ) -> List[List[int]]:
        """
        Build distance/time matrix for VRP with pathfinding cache.

        Uses cached pathfinding results to avoid redundant computations.
        For N nodes, this reduces pathfinding calls from N² to ~N²/2
        (due to symmetry and caching).
        """
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

                origin_wp = graph[origin]
                target_wp = graph[target]

                # Check cache first
                cache_key = (origin, target, fuel_capacity, engine_speed)
                if cache_key in self._pathfinding_cache:
                    route = self._pathfinding_cache[cache_key]
                    cache_hits += 1
                else:
                    # Cache miss - compute pathfinding
                    route = self.find_optimal_path(
                        graph=graph,
                        start=origin,
                        goal=target,
                        current_fuel=fuel_capacity,  # Assume starting with full tank
                        fuel_capacity=fuel_capacity,
                        engine_speed=engine_speed,
                        prefer_cruise=True
                    )
                    # Store in cache
                    self._pathfinding_cache[cache_key] = route
                    cache_misses += 1

                if route and route.get('total_time'):
                    # Path exists - use actual pathfinding time (includes refueling)
                    matrix[i][j] = route['total_time']
                else:
                    # No path exists - keep as unreachable (1,000,000)
                    # This happens when fuel constraints make the route impossible
                    pass  # matrix[i][j] already initialized to 1,000,000

        logger.info(f"Distance matrix cache: {cache_hits} hits, {cache_misses} misses")
        return matrix
