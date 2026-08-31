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
import time
from typing import Dict, List, NamedTuple, Optional, Tuple, Any
from dataclasses import dataclass
from enum import Enum

from ortools.constraint_solver import routing_enums_pb2
from ortools.constraint_solver import pywrapcp

logger = logging.getLogger(__name__)

# Fleet-partition outcomes. The caller must be able to tell a SOLVED partition from
# the engine's own round-robin, because a permanently-degraded solver otherwise
# passes for a working one (sp-ev79y).
PARTITION_SOLVED = "solved"
PARTITION_TRIVIAL = "trivial"
PARTITION_FALLBACK_NO_SOLUTION = "fallback:no-solution"
PARTITION_FALLBACK_BUDGET_SPENT = "fallback:budget-spent"

# How the partition budget is split. The distance matrix is a PATHFINDING phase that
# runs before the solver exists, so no OR-Tools time limit can reach it; it gets
# whatever the solver's guaranteed share leaves.
_SOLVE_BUDGET_SHARE = 0.5
_MIN_SOLVE_RESERVE_SECONDS = 1.0

# Cost of an arc between waypoints the caller never put in the graph.
_UNREACHABLE_ARC = 1_000_000

# Shared cost-model constants: the fuel kept in reserve on any hop that is not the
# last one, the penalty that makes DRIFT a last resort, and the cost of an orbital
# transfer. The all-origins sweep and find_optimal_path must price a leg alike.
FUEL_SAFETY_MARGIN = 4
DRIFT_TIME_PENALTY = 100000
ORBITAL_HOP_TIME = 1
ORBITAL_HOP_DISTANCE = 0.0


class FleetPartition(NamedTuple):
    """A fleet partition and whether it was solved or fallen back to.

    `assignments` is always a full partition of the requested markets over the
    requested ships. `fallback` is True when it is the engine's deterministic
    round-robin rather than a solved assignment, and `status` names why.
    """
    assignments: Dict[str, List[str]]
    fallback: bool
    status: str


def _set_time_limit(duration, seconds: float) -> None:
    """Write a float second count into a protobuf Duration."""
    millis = max(1, int(seconds * 1000))
    duration.seconds = millis // 1000
    duration.nanos = (millis % 1000) * 1_000_000


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


class _LegTables:
    """Per-ordered-pair hop facts for one graph, plus the sweeps built on them.

    Hoisting the geometry out of the search loop is what makes an all-origins
    fuel-aware sweep affordable: the arithmetic below is evaluated once per pair
    instead of once per state expansion.
    """

    __slots__ = ("size", "orbital", "burn_fuel", "cruise_fuel", "drift_fuel",
                 "burn_time", "cruise_time", "drift_time", "has_fuel")

    def __init__(self, size: int):
        self.size = size
        table = lambda fill: [[fill] * size for _ in range(size)]  # noqa: E731
        self.orbital = table(False)
        self.burn_fuel = table(0)
        self.cruise_fuel = table(0)
        self.drift_fuel = table(0)
        self.burn_time = table(0)
        self.cruise_time = table(0)
        self.drift_time = table(0)
        self.has_fuel: List[bool] = [False] * size

    @classmethod
    def build(cls, symbols: List[str], graph: Dict[str, Waypoint],
              engine_speed: int) -> '_LegTables':
        tables = cls(len(symbols))
        for i, origin in enumerate(symbols):
            source = graph[origin]
            tables.has_fuel[i] = source.has_fuel
            for j, target in enumerate(symbols):
                if i == j:
                    continue
                destination = graph[target]
                distance = source.distance_to(destination)
                if source.is_orbital_of(destination) or distance == 0.0:
                    tables.orbital[i][j] = True
                    continue
                tables.burn_fuel[i][j] = FlightMode.BURN.fuel_cost(distance)
                tables.cruise_fuel[i][j] = FlightMode.CRUISE.fuel_cost(distance)
                tables.drift_fuel[i][j] = FlightMode.DRIFT.fuel_cost(distance)
                tables.burn_time[i][j] = FlightMode.BURN.travel_time(distance, engine_speed)
                tables.cruise_time[i][j] = FlightMode.CRUISE.travel_time(distance, engine_speed)
                tables.drift_time[i][j] = (
                    FlightMode.DRIFT.travel_time(distance, engine_speed) + DRIFT_TIME_PENALTY)
        return tables

    def direct_times(self, source: int) -> List[int]:
        """One straight-line CRUISE hop to every waypoint — no fuel constraint."""
        row = [_UNREACHABLE_ARC] * self.size
        row[source] = 0
        for target in range(self.size):
            if target == source:
                continue
            row[target] = (ORBITAL_HOP_TIME if self.orbital[source][target]
                           else self.cruise_time[source][target])
        return row

    def travel_times_from(self, source: int, fuel_capacity: int) -> List[int]:
        """Optimal travel time from `source` to every waypoint, leaving on a full tank.

        The same cost model as find_optimal_path — BURN/CRUISE/DRIFT with the fuel
        safety margin, free refuelling wherever fuel is sold, DRIFT priced as a last
        resort — solved once for all targets. Nodes are popped in travel-time order,
        and a repeat arrival is worth exploring only if it carries strictly more fuel
        than any earlier (and therefore faster) arrival there.
        """
        if fuel_capacity == 0:
            return self.direct_times(source)

        best = [_UNREACHABLE_ARC] * self.size
        best[source] = 0
        richest = [-1] * self.size
        queue = [(0, source, fuel_capacity)]
        while queue:
            elapsed, current, fuel = heapq.heappop(queue)
            if self.has_fuel[current]:
                fuel = fuel_capacity
            if fuel <= richest[current]:
                continue
            richest[current] = fuel

            orbital = self.orbital[current]
            burn_fuel, cruise_fuel, drift_fuel = (
                self.burn_fuel[current], self.cruise_fuel[current], self.drift_fuel[current])
            burn_time, cruise_time, drift_time = (
                self.burn_time[current], self.cruise_time[current], self.drift_time[current])

            for target in range(self.size):
                if target == current:
                    continue
                if orbital[target]:
                    arrival = elapsed + ORBITAL_HOP_TIME
                    if arrival < best[target]:
                        best[target] = arrival
                    if fuel > richest[target]:
                        heapq.heappush(queue, (arrival, target, fuel))
                    continue

                burn, cruise, drift = burn_fuel[target], cruise_fuel[target], drift_fuel[target]

                # Arriving may spend the safety margin; travelling ON may not.
                if fuel >= burn:
                    arrival = elapsed + burn_time[target]
                elif fuel >= cruise:
                    arrival = elapsed + cruise_time[target]
                elif fuel >= drift:
                    arrival = elapsed + drift_time[target]
                else:
                    continue
                if arrival < best[target]:
                    best[target] = arrival

                # BURN is faster, CRUISE keeps more fuel for the hops after: neither
                # dominates, so both continuations stay in the search.
                onward = False
                if fuel >= burn + FUEL_SAFETY_MARGIN:
                    onward = True
                    if fuel - burn > richest[target]:
                        heapq.heappush(queue, (elapsed + burn_time[target], target, fuel - burn))
                if fuel >= cruise + FUEL_SAFETY_MARGIN:
                    onward = True
                    if fuel - cruise > richest[target]:
                        heapq.heappush(queue, (elapsed + cruise_time[target], target, fuel - cruise))
                if not onward and fuel >= drift and fuel - drift > richest[target]:
                    heapq.heappush(queue, (elapsed + drift_time[target], target, fuel - drift))
        return best


class ORToolsRoutingEngine:
    """
    Routing engine using OR-Tools for optimization.

    Ported from Python bot's ortools_engine.py
    """

    def __init__(self, tsp_timeout: int = 5, vrp_timeout: int = 30):
        """
        Initialize routing engine.

        Args:
            tsp_timeout: Timeout in seconds for TSP (tour optimization) solver
            vrp_timeout: Timeout in seconds for VRP (fleet partitioning) solver
        """
        self._travel_time_cache: Dict[Tuple[str, int, int], Dict[str, int]] = {}
        self._tsp_timeout = tsp_timeout
        self._vrp_timeout = vrp_timeout

    def clear_cache(self):
        """Clear the pathfinding cache"""
        self._travel_time_cache.clear()
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
                    distance = ORBITAL_HOP_DISTANCE
                    travel_time = ORBITAL_HOP_TIME
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
                is_goal = (neighbor_symbol == goal)

                burn_cost = self.calculate_fuel_cost(distance, FlightMode.BURN)
                cruise_cost = self.calculate_fuel_cost(distance, FlightMode.CRUISE)

                # Build list of viable modes (mode, fuel_cost)
                viable_modes = []

                # Check BURN (skip if prefer_cruise is set)
                if not prefer_cruise:
                    if fuel_remaining >= burn_cost + FUEL_SAFETY_MARGIN:
                        viable_modes.append((FlightMode.BURN, burn_cost))
                    elif is_goal and fuel_remaining >= burn_cost:
                        viable_modes.append((FlightMode.BURN, burn_cost))

                # Check CRUISE
                if fuel_remaining >= cruise_cost + FUEL_SAFETY_MARGIN:
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
                        travel_time += DRIFT_TIME_PENALTY

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
            distance = ORBITAL_HOP_DISTANCE
            time = ORBITAL_HOP_TIME
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
                    distance = ORBITAL_HOP_DISTANCE
                    time = ORBITAL_HOP_TIME
                    fuel_cost = 0
                    mode = FlightMode.CRUISE
                else:
                    # Select flight mode: NEVER use DRIFT mode
                    # Ships should ALWAYS use BURN or CRUISE, inserting refuel stops as needed
                    # Use fastest mode that maintains the fuel safety margin
                    current_fuel = fuel_capacity

                    burn_cost = self.calculate_fuel_cost(distance, FlightMode.BURN)
                    cruise_cost = self.calculate_fuel_cost(distance, FlightMode.CRUISE)

                    if current_fuel >= burn_cost + FUEL_SAFETY_MARGIN:
                        mode = FlightMode.BURN
                    elif current_fuel >= cruise_cost + FUEL_SAFETY_MARGIN:
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
        engine_speed: int,
        time_limit: Optional[float] = None,
    ) -> FleetPartition:
        """
        Partition markets across multiple ships using multi-vehicle VRP.

        Returns a FleetPartition: ship_symbol -> assigned markets, plus whether the
        answer was solved or fallen back to. The whole call is bounded by
        `time_limit` (default: the engine's VRP timeout) — matrix build included,
        because the solver's own limit cannot reach a phase that runs before it.
        """
        budget = float(self._vrp_timeout if time_limit is None else time_limit)
        deadline = time.monotonic() + budget

        if not markets or not ship_locations:
            return FleetPartition({ship: [] for ship in ship_locations.keys()},
                                  False, PARTITION_TRIVIAL)

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

        # The deterministic round-robin partition, computed up front. It is both the
        # solver's SEED (a first solution the search is guaranteed to have, see below)
        # and the answer returned if everything after this point fails.
        seed_routes, seed_assignments = self._round_robin_seed(
            ships, markets, node_index, starts)

        # Build distance matrix
        solve_reserve = max(_MIN_SOLVE_RESERVE_SECONDS, budget * _SOLVE_BUDGET_SHARE)
        distance_matrix = self._build_distance_matrix_for_vrp(
            nodes, graph, fuel_capacity, engine_speed,
            deadline=deadline - solve_reserve,
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

        # Add travel time dimension (arcs are per-leg travel TIME, not distance/count).
        routing.AddDimension(
            transit_callback_index,
            0,
            disjunction_penalty,
            True,
            "TravelTime"
        )
        time_dimension = routing.GetDimensionOrDie("TravelTime")
        # sp-cc2na: MIN-MAKESPAN. The global span cost penalizes the MAXIMUM per-probe
        # tour time (max end-cumul, since starts are fixed to 0), so the solver balances
        # probes by TIME — freshness per market is its probe's circuit time, and uneven
        # geography makes equal-COUNT tours have unequal freshness. This dominates the
        # arc-cost sum (which only keeps each route individually short), so the partition
        # equalizes tour time rather than market count.
        time_dimension.SetGlobalSpanCostCoefficient(100)

        # Add disjunction constraints for markets
        for market in markets:
            routing.AddDisjunction(
                [manager.NodeToIndex(node_index[market])],
                disjunction_penalty
            )

        # sp-cc2na: force EVERY probe to take >=1 market when there are at least as many
        # markets as probes. Min-makespan alone leaves a probe idle whenever using it
        # would not lower the max tour time — a tight far cluster (splitting it barely
        # changes the depot-leg-dominated time) or an outlier that pins the makespan. The
        # secondary arc-cost sum then packs markets onto fewer vehicles, so with N floored
        # scout slots only <N actually scout and the whole partition covers at (N-1)-probe
        # cadence (the live sp-cc2na symptom: "3 disjoint tours" logged, 2 probes moving).
        # A non-empty route means Start does not go straight to End, which needs a node
        # that is not already some vehicle's start — so the guard counts SEEDABLE routes
        # rather than markets, and a crew parked on its own market list is left free.
        if sum(1 for route in seed_routes if route) == len(ships):
            solver = routing.solver()
            for vehicle in range(len(ships)):
                solver.Add(routing.NextVar(routing.Start(vehicle)) != routing.End(vehicle))

        # Search parameters
        search_parameters = pywrapcp.DefaultRoutingSearchParameters()
        search_parameters.first_solution_strategy = (
            routing_enums_pb2.FirstSolutionStrategy.PATH_CHEAPEST_ARC
        )
        search_parameters.local_search_metaheuristic = (
            routing_enums_pb2.LocalSearchMetaheuristic.GUIDED_LOCAL_SEARCH
        )
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            logger.warning(
                "fleet partition spent its whole %.1fs budget building the distance "
                "matrix for %d ship(s) over %d market(s); returning the round-robin "
                "partition unsolved", budget, len(ships), len(markets),
            )
            return FleetPartition(seed_assignments, True, PARTITION_FALLBACK_BUDGET_SPENT)
        _set_time_limit(search_parameters.time_limit, remaining)

        # Solve FROM the round-robin seed. Every first-solution strategy OR-Tools
        # offers fails on this model — the forced non-empty routes are not something
        # a greedy insertion heuristic respects, so it burns the whole limit and
        # returns nothing (sp-ev79y). Handing the search a feasible assignment makes
        # the first solution exist by construction: the local search then only ever
        # improves on the round-robin, and when the budget expires it returns the best
        # it reached rather than throwing the work away.
        initial = routing.ReadAssignmentFromRoutes(seed_routes, True)
        if initial is None:
            logger.warning("fleet partition seed rejected by the model for %d ship(s) "
                           "over %d market(s)", len(ships), len(markets))
            solution = routing.SolveWithParameters(search_parameters)
        else:
            solution = routing.SolveFromAssignmentWithParameters(initial, search_parameters)

        if not solution:
            # The VRP found no solution at all. A single-ship scout keeps ALL its
            # markets (it bypasses the VRP entirely); a 2+ ship partition must keep
            # parity rather than collapse to an empty partition of 0 tours (sp-t73c).
            # Degrade to a deterministic balanced partition instead of returning empty.
            logger.warning(
                "VRP returned no solution (status %s) for %d ship(s) over %d market(s); "
                "falling back to a balanced round-robin partition",
                routing.status(), len(ships), len(markets),
            )
            return FleetPartition(seed_assignments, True, PARTITION_FALLBACK_NO_SOLUTION)

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

        # Any market the VRP could not place — an unreachable outlier, a symbol
        # missing from the system graph (the sp-8k9m cache-scope miss), or a drop
        # under a tight solve budget — is distributed across the ships rather than
        # silently omitted. Single-ship scouting keeps every market, so a 2+ ship
        # partition must too: never shrink the book or collapse to an empty partition
        # of 0 tours (sp-t73c). The VRP has already optimised the placement of
        # everything it could route; this only tops up the remainder, preserving
        # input order for determinism.
        unplaced_markets = [m for m in markets if m not in assigned_waypoints]
        if unplaced_markets:
            logger.warning(
                "VRP left %d of %d market(s) unplaced (unreachable/ungraphed outliers); "
                "distributing them across ships to keep parity with single-ship "
                "scouting: %s",
                len(unplaced_markets), len(markets), unplaced_markets,
            )
            self._distribute_evenly(unplaced_markets, assignments)

        status = PARTITION_SOLVED
        if unplaced_markets:
            status = "%s:unplaced=%d" % (PARTITION_SOLVED, len(unplaced_markets))
        return FleetPartition(assignments, False, status)

    def _round_robin_seed(
        self,
        ships: List[str],
        markets: List[str],
        node_index: Dict[str, int],
        starts: List[int],
    ) -> Tuple[List[List[int]], Dict[str, List[str]]]:
        """The balanced round-robin partition, as OR-Tools routes and as assignments.

        Both views describe the same cut. The routes seed the search; the assignments
        are what the caller gets if the search cannot better them. A market a vehicle
        is already parked on belongs to that vehicle and is left out of the routes,
        because a vehicle's own start is not a node it can be told to visit.
        """
        start_nodes = set(starts)
        assignments: Dict[str, List[str]] = {ship: [] for ship in ships}
        claimed: set = set()
        for vehicle, ship in enumerate(ships):
            for market in markets:
                if market not in claimed and node_index[market] == starts[vehicle]:
                    assignments[ship].append(market)
                    claimed.add(market)
                    break

        routes: List[List[int]] = [[] for _ in ships]
        for market in markets:
            if market in claimed or node_index[market] in start_nodes:
                continue
            vehicle = min(range(len(ships)), key=lambda v: len(assignments[ships[v]]))
            assignments[ships[vehicle]].append(market)
            routes[vehicle].append(node_index[market])
        return routes, assignments

    def _distribute_evenly(
        self,
        markets_to_place: List[str],
        assignments: Dict[str, List[str]],
    ) -> None:
        """Assign each market to the currently least-loaded ship, in place.

        A deterministic safety net that keeps a fleet partition non-empty and
        balanced when the VRP cannot place every market (an unreachable/ungraphed
        outlier) or returns no solution at all. Ships are compared by current market
        count with a stable tie-break on fleet order, so the result is reproducible.
        """
        if not assignments:
            return
        ships = list(assignments.keys())
        for market in markets_to_place:
            least_loaded = min(ships, key=lambda ship: len(assignments[ship]))
            assignments[least_loaded].append(market)

    def _build_distance_matrix_for_vrp(
        self,
        nodes: List[str],
        graph: Dict[str, Waypoint],
        fuel_capacity: int,
        engine_speed: int,
        deadline: Optional[float] = None,
    ) -> List[List[int]]:
        """Travel-time matrix over `nodes`, priced by the fuel-aware pathfinder.

        ONE fuel-constrained sweep per ORIGIN rather than one search per ORDERED
        PAIR. The per-pair form re-walked the whole graph for every target and was
        the phase that ran the partition minutes past its own time limit — it runs
        before the solver exists, so no OR-Tools limit can reach it (sp-ev79y). The
        arc values are unchanged: a sweep pops nodes in travel-time order, so the
        time it records for a target is the one the per-pair search returned.

        `deadline` is a wall-clock cut-off. Origins still unpriced when it passes
        fall back to straight-line CRUISE time, so the phase cannot overrun the
        budget the caller gave the whole call however large the graph gets.
        """
        size = len(nodes)
        matrix = [[_UNREACHABLE_ARC] * size for _ in range(size)]
        for i in range(size):
            matrix[i][i] = 0

        symbols = list(graph)
        if not symbols:
            return matrix
        index_of = {symbol: i for i, symbol in enumerate(symbols)}
        legs = _LegTables.build(symbols, graph, engine_speed)

        rows: Dict[str, List[int]] = {}
        estimated = 0
        for origin in nodes:
            if origin in rows or origin not in index_of:
                continue
            key = (origin, fuel_capacity, engine_speed)
            row = self._travel_time_cache.get(key)
            if row is None:
                if deadline is not None and time.monotonic() >= deadline:
                    row = legs.direct_times(index_of[origin])
                    estimated += 1
                else:
                    row = legs.travel_times_from(index_of[origin], fuel_capacity)
                self._travel_time_cache[key] = row
            rows[origin] = row

        for i, origin in enumerate(nodes):
            row = rows.get(origin)
            if row is None:
                continue
            for j, target in enumerate(nodes):
                if i != j and target in index_of:
                    matrix[i][j] = row[index_of[target]]

        if estimated:
            logger.warning(
                "Distance matrix hit its deadline: %d of %d origin(s) priced by "
                "straight-line estimate instead of the fuel-aware pathfinder",
                estimated, len(rows),
            )
        logger.info("Distance matrix: %d origin sweep(s) over %d waypoint(s)",
                    len(rows), len(symbols))
        return matrix
