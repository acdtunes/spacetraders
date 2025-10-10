#!/usr/bin/env python3
from __future__ import annotations

"""
OR-Tools powered routing utilities for the SpaceTraders bot.

This module exposes:
  * ORToolsRouter            - single ship routing with fuel constraints
  * ORToolsTSP               - multi-stop tour optimisation and caching
  * ORToolsFleetPartitioner  - multi-ship market partitioning
"""

import logging
import math
from dataclasses import dataclass
from typing import Dict, Iterable, List, Optional, Sequence, Tuple

from ortools.constraint_solver import pywrapcp, routing_enums_pb2
from ortools.graph.python import min_cost_flow

from .database import get_database
from .routing_config import RoutingConfig, RoutingConfigError
from ..helpers import paths

logger = logging.getLogger(__name__)


# --------------------------------------------------------------------------- #
# Shared helpers
# --------------------------------------------------------------------------- #


def _time_seconds(distance: float, multiplier: float, speed: int) -> int:
    """Convert distance into travel time using configured multiplier."""
    if speed <= 0:
        raise ValueError("ship engine speed must be positive")
    if distance <= 0:
        return 0
    seconds = round((distance * multiplier) / speed)
    return max(1, int(seconds))


def _fuel_units(distance: float, rate: float) -> int:
    """Convert distance into fuel units consumed."""
    if distance <= 0:
        return 0
    units = distance * rate
    return max(1, math.ceil(units))


def _waypoint_distance(graph: Dict, origin: str, target: str) -> Optional[float]:
    """Return Euclidean distance between two waypoints if both exist."""
    waypoints = graph.get("waypoints", {})
    wp_a = waypoints.get(origin)
    wp_b = waypoints.get(target)
    if not wp_a or not wp_b:
        return None
    dx = wp_b["x"] - wp_a["x"]
    dy = wp_b["y"] - wp_a["y"]
    return math.hypot(dx, dy)


@dataclass(frozen=True)
class EdgeMetrics:
    """Pre-computed metrics for a bi-directional edge in the system graph."""

    distance: float
    cruise_time: Optional[int]
    cruise_fuel: Optional[int]
    drift_time: int
    drift_fuel: int

    def time_for(self, mode: str) -> int:
        if mode == "CRUISE":
            if self.cruise_time is None:
                raise ValueError("CRUISE metrics unavailable")
            return self.cruise_time
        if mode == "DRIFT":
            return self.drift_time
        raise ValueError(f"Unsupported mode {mode}")

    def fuel_for(self, mode: str) -> int:
        if mode == "CRUISE":
            if self.cruise_fuel is None:
                raise ValueError("CRUISE metrics unavailable")
            return self.cruise_fuel
        if mode == "DRIFT":
            return self.drift_fuel
        raise ValueError(f"Unsupported mode {mode}")


# --------------------------------------------------------------------------- #
# OR-Tools minimum cost flow router
# --------------------------------------------------------------------------- #


class RoutingError(RuntimeError):
    """Raised when route planning fails."""


class ORToolsRouter:
    """Find optimal single-destination routes with fuel constraints."""

    DRIFT_PENALTY_SECONDS = 3_600  # large enough to strongly prefer CRUISE

    def __init__(self, graph: Dict, ship_data: Dict, config: Optional[RoutingConfig] = None):
        if not graph:
            raise RoutingError("system graph is required")
        if "waypoints" not in graph:
            raise RoutingError("graph missing waypoints section")
        if "edges" not in graph:
            graph["edges"] = []

        self.graph = graph
        self.ship = ship_data
        self.config = config or RoutingConfig()

        if not self.graph.get("edges"):
            self.graph["edges"] = self._build_dense_edges()

        try:
            self._cruise_cfg = self.config.get_flight_mode_config("CRUISE")
            self._drift_cfg = self.config.get_flight_mode_config("DRIFT")
        except RoutingConfigError as exc:
            raise RoutingError(f"Routing configuration invalid: {exc}") from exc

        self.engine_speed = int(ship_data["engine"]["speed"])
        self.fuel_capacity = int(ship_data["fuel"]["capacity"])

        if self.engine_speed <= 0:
            raise RoutingError("Ship engine speed must be positive")
        if self.fuel_capacity < 0:
            raise RoutingError("Fuel capacity cannot be negative")

        self._waypoints = list(self.graph["waypoints"].keys())
        self._wp_index = {symbol: idx for idx, symbol in enumerate(self._waypoints)}
        self._node_multiplier = self.fuel_capacity + 1 if self.fuel_capacity > 0 else 1

        self._edge_metrics: Dict[Tuple[str, str], EdgeMetrics] = {}
        self._adjacency: Dict[str, List[Tuple[str, EdgeMetrics]]] = {
            wp: [] for wp in self._waypoints
        }
        self._prepare_edge_metrics()

    # ------------------------------------------------------------------ #
    # Public API
    # ------------------------------------------------------------------ #

    def find_optimal_route(
        self,
        start: str,
        goal: str,
        current_fuel: int,
        prefer_cruise: bool = True,
    ) -> Optional[Dict]:
        """Compute optimal route plan, returning structure compatible with legacy RouteOptimizer."""
        prefer_cruise = True  # Cruise preference enforced globally
        if start == goal:
            return {
                "start": start,
                "goal": goal,
                "total_time": 0,
                "final_fuel": current_fuel,
                "steps": [],
                "ship_speed": self.engine_speed,
            }

        if start not in self._wp_index or goal not in self._wp_index:
            logger.error("Start or goal waypoint missing from graph (start=%s, goal=%s)", start, goal)
            return None

        if self.fuel_capacity == 0:
            return self._probe_route(start, goal)

        path_steps = self._solve_with_min_cost_flow(
            start=start,
            goal=goal,
            current_fuel=current_fuel,
        )

        if not path_steps:
            return None

        total_time = sum(step.get("time", 0) for step in path_steps)

        final_fuel = current_fuel
        for step in path_steps:
            if step["action"] == "refuel":
                final_fuel += step["fuel_added"]
                final_fuel = min(final_fuel, self.fuel_capacity)
            elif step["action"] == "navigate":
                final_fuel -= step["fuel_cost"]
                final_fuel = max(final_fuel, 0)

        return {
            "start": start,
            "goal": goal,
            "total_time": total_time,
            "final_fuel": final_fuel,
            "steps": path_steps,
            "ship_speed": self.engine_speed,
        }

    # ------------------------------------------------------------------ #
    # Internal helpers
    # ------------------------------------------------------------------ #

    def _prepare_edge_metrics(self) -> None:
        for edge in self.graph.get("edges", []):
            origin = edge["from"]
            target = edge["to"]
            distance = float(edge["distance"])

            cruise_time = cruise_fuel = None
            if self._cruise_cfg:
                cruise_time = _time_seconds(distance, self._cruise_cfg["time_multiplier"], self.engine_speed)
                cruise_fuel = _fuel_units(distance, self._cruise_cfg["fuel_rate"])

            drift_time = _time_seconds(distance, self._drift_cfg["time_multiplier"], self.engine_speed)
            drift_fuel = _fuel_units(distance, self._drift_cfg["fuel_rate"])

            metrics = EdgeMetrics(
                distance=distance,
                cruise_time=cruise_time,
                cruise_fuel=cruise_fuel,
                drift_time=drift_time,
                drift_fuel=drift_fuel,
            )

            self._edge_metrics[(origin, target)] = metrics
            self._edge_metrics[(target, origin)] = metrics

            # undirected adjacency
            if origin in self._adjacency:
                self._adjacency[origin].append((target, metrics))
            if target in self._adjacency:
                self._adjacency[target].append((origin, metrics))

    def get_edge_metrics(self, origin: str, target: str) -> Optional[EdgeMetrics]:
        """Expose edge metrics for compatibility with other modules."""
        return self._edge_metrics.get((origin, target))

    def _build_dense_edges(self) -> List[Dict]:
        waypoints = self.graph.get("waypoints", {})
        symbols = list(waypoints.keys())
        edges: List[Dict] = []

        for i, origin in enumerate(symbols):
            wp_a = waypoints.get(origin)
            xa = wp_a.get("x") if wp_a else None
            ya = wp_a.get("y") if wp_a else None
            for target in symbols[i + 1:]:
                wp_b = waypoints.get(target)
                xb = wp_b.get("x") if wp_b else None
                yb = wp_b.get("y") if wp_b else None
                if xa is None or ya is None or xb is None or yb is None:
                    continue
                distance = round(math.hypot(xb - xa, yb - ya), 2)
                edges.append({"from": origin, "to": target, "distance": distance, "type": "synthetic"})
                edges.append({"from": target, "to": origin, "distance": distance, "type": "synthetic"})
        return edges

    def _state_id(self, waypoint: str, fuel: int) -> int:
        waypoint_idx = self._wp_index[waypoint]
        return waypoint_idx * self._node_multiplier + fuel

    def _solve_with_min_cost_flow(
        self,
        start: str,
        goal: str,
        current_fuel: int,
    ) -> Optional[List[Dict]]:
        flow = min_cost_flow.SimpleMinCostFlow()
        metadata: Dict[int, Dict] = {}

        start_fuel = max(0, min(current_fuel, self.fuel_capacity))
        start_node = self._state_id(start, start_fuel)
        sink_node = len(self._waypoints) * self._node_multiplier

        flow.set_node_supply(start_node, 1)
        flow.set_node_supply(sink_node, -1)

        refuel_time = self.config.refuel_time_seconds()
        drift_penalty = self.DRIFT_PENALTY_SECONDS

        # Generate arcs for each waypoint/fuel state
        for waypoint in self._waypoints:
            waypoint_data = self.graph["waypoints"][waypoint]
            has_fuel = bool(waypoint_data.get("has_fuel"))

            for fuel in range(self.fuel_capacity + 1):
                state_id = self._state_id(waypoint, fuel)

                # Refuel action
                if has_fuel and fuel < self.fuel_capacity:
                    arc = flow.add_arc_with_capacity_and_unit_cost(
                        state_id,
                        self._state_id(waypoint, self.fuel_capacity),
                        1,
                        refuel_time,
                    )
                    metadata[arc] = {
                        "type": "refuel",
                        "waypoint": waypoint,
                        "fuel_before": fuel,
                        "fuel_after": self.fuel_capacity,
                        "time": refuel_time,
                    }

                if fuel == 0:
                    continue

                for neighbor, metrics in self._adjacency.get(waypoint, []):
                    for mode in ("CRUISE", "DRIFT"):
                        if mode == "CRUISE" and metrics.cruise_time is None:
                            continue

                        fuel_cost = metrics.fuel_for(mode)
                        if fuel_cost > fuel:
                            continue

                        remaining_fuel = fuel - fuel_cost
                        if remaining_fuel < 0:
                            continue

                        base_time = metrics.time_for(mode)
                        penalty = drift_penalty if mode == "DRIFT" else 0
                        arc = flow.add_arc_with_capacity_and_unit_cost(
                            state_id,
                            self._state_id(neighbor, remaining_fuel),
                            1,
                            base_time + penalty,
                        )
                        metadata[arc] = {
                            "type": "navigate",
                            "from": waypoint,
                            "to": neighbor,
                            "mode": mode,
                            "time": base_time,
                            "fuel_before": fuel,
                            "fuel_after": remaining_fuel,
                            "fuel_cost": fuel_cost,
                            "distance": metrics.distance,
                        }

        # Connect goal fuel states to sink
        for fuel in range(self.fuel_capacity + 1):
            arc = flow.add_arc_with_capacity_and_unit_cost(
                self._state_id(goal, fuel),
                sink_node,
                1,
                0,
            )
            metadata[arc] = {"type": "sink"}

        status = flow.solve()
        if status != flow.OPTIMAL:
            logger.warning("Min cost flow solver failed with status %s", status)
            return None

        # Build mapping of active arcs
        active_arc_by_tail: Dict[int, int] = {}
        for arc in range(flow.num_arcs()):
            if flow.flow(arc) > 0:
                tail = flow.tail(arc)
                head = flow.head(arc)
                active_arc_by_tail[tail] = arc
                logger.debug("Active arc %s -> %s (%s)", tail, head, metadata.get(arc, {}))

        steps: List[Dict] = []
        current_node = start_node

        while current_node != sink_node:
            arc = active_arc_by_tail.get(current_node)
            if arc is None:
                logger.error("Failed to reconstruct routing path: missing arc for node %s", current_node)
                return None

            data = metadata.get(arc)
            if not data:
                logger.error("Arc metadata missing for arc %s", arc)
                return None

            if data["type"] == "refuel":
                fuel_added = data["fuel_after"] - data["fuel_before"]
                if fuel_added > 0:
                    steps.append({
                        "action": "refuel",
                        "waypoint": data["waypoint"],
                        "fuel_added": fuel_added,
                        "time": data["time"],
                    })
            elif data["type"] == "navigate":
                steps.append({
                    "action": "navigate",
                    "from": data["from"],
                    "to": data["to"],
                    "mode": data["mode"],
                    "time": data["time"],
                    "fuel_cost": data["fuel_cost"],
                    "distance": data["distance"],
                })

            current_node = flow.head(arc)

        return steps

    # ------------------------------------------------------------------ #
    # Probe fast-path (no fuel capacity)
    # ------------------------------------------------------------------ #

    def _probe_route(self, start: str, goal: str) -> Optional[Dict]:
        """Simplified routing for probes that do not consume fuel."""
        # Use OR-Tools routing model with distance cost
        locations = list(self.graph["waypoints"].keys())
        index_map = {symbol: idx for idx, symbol in enumerate(locations)}
        start_idx = index_map[start]
        goal_idx = index_map[goal]

        manager = pywrapcp.RoutingIndexManager(len(locations), 1, [start_idx], [goal_idx])
        routing = pywrapcp.RoutingModel(manager)

        def time_callback(from_index: int, to_index: int) -> int:
            from_node = manager.IndexToNode(from_index)
            to_node = manager.IndexToNode(to_index)
            origin = locations[from_node]
            target = locations[to_node]
            metrics = self._edge_metrics.get((origin, target))
            if not metrics:
                return 1_000_000
            return metrics.cruise_time or metrics.drift_time

        transit_index = routing.RegisterTransitCallback(time_callback)
        routing.SetArcCostEvaluatorOfAllVehicles(transit_index)

        for node_idx in range(len(locations)):
            if node_idx in (start_idx, goal_idx):
                continue
            routing.AddDisjunction([manager.NodeToIndex(node_idx)], 0)

        search_parameters = pywrapcp.DefaultRoutingSearchParameters()
        solver_cfg = self.config.get_solver_config()
        search_parameters.first_solution_strategy = getattr(
            routing_enums_pb2.FirstSolutionStrategy,
            solver_cfg["first_solution_strategy"],
            routing_enums_pb2.FirstSolutionStrategy.PATH_CHEAPEST_ARC,
        )
        search_parameters.local_search_metaheuristic = getattr(
            routing_enums_pb2.LocalSearchMetaheuristic,
            solver_cfg["local_search_metaheuristic"],
            routing_enums_pb2.LocalSearchMetaheuristic.GUIDED_LOCAL_SEARCH,
        )
        search_parameters.time_limit.FromMilliseconds(solver_cfg["time_limit_ms"])

        solution = routing.SolveWithParameters(search_parameters)
        if not solution:
            logger.warning("Probe route solver failed for %s -> %s", start, goal)
            return None

        steps: List[Dict] = []
        index = routing.Start(0)
        total_time = 0
        while not routing.IsEnd(index):
            next_index = solution.Value(routing.NextVar(index))
            origin = locations[manager.IndexToNode(index)]
            target = locations[manager.IndexToNode(next_index)]
            metrics = self._edge_metrics.get((origin, target))
            if metrics:
                travel_time = metrics.cruise_time or metrics.drift_time
                steps.append({
                    "action": "navigate",
                    "from": origin,
                    "to": target,
                    "mode": "CRUISE",
                    "time": travel_time,
                    "fuel_cost": 0,
                    "distance": metrics.distance,
                })
                total_time += travel_time
            index = next_index

        return {
            "start": start,
            "goal": goal,
            "total_time": total_time,
            "final_fuel": 0,
            "steps": steps,
            "ship_speed": self.engine_speed,
        }


# --------------------------------------------------------------------------- #
# OR-Tools TSP optimiser
# --------------------------------------------------------------------------- #


class ORToolsTSP:
    """Optimise multi-stop tours using OR-Tools and cache results."""

    def __init__(self, graph: Dict, config: Optional[RoutingConfig] = None, db_path: Optional[str] = None):
        if not graph:
            raise RoutingError("graph is required for TSP optimisation")
        self.graph = graph
        self.config = config or RoutingConfig()
        resolved_db_path = paths.sqlite_path() if db_path is None else db_path
        self.db = get_database(resolved_db_path)

    def optimise_tour(
        self,
        waypoints: Sequence[str],
        start: str,
        ship_data: Dict,
        return_to_start: bool = False,
        use_cache: bool = True,
        algorithm: str = "ortools",
    ) -> Optional[Dict]:
        if not waypoints:
            return None

        stops = [wp for wp in waypoints if wp != start]
        if not stops:
            return None

        system = self.graph.get("system", "UNKNOWN")
        router = ORToolsRouter(self.graph, ship_data, self.config)

        if use_cache:
            with self.db.connection() as conn:
                cached = self.db.get_cached_tour(
                    conn,
                    system,
                    list(stops),
                    algorithm,
                    start if not return_to_start else None,
                )
                if cached:
                    logger.info("Tour cache hit for %s stops", len(stops))
                    return self._build_tour_from_order(cached["tour_order"], router, return_to_start)

        order = self._solve_order(stops, start, return_to_start, router)
        if order is None:
            return None

        tour = self._build_tour_from_order(order, router, return_to_start)
        if not tour:
            return None

        if use_cache:
            with self.db.transaction() as conn:
                try:
                    self.db.save_tour_cache(
                        conn,
                        system,
                        list(stops),
                        algorithm,
                        order,
                        tour["total_distance"],
                        start if not return_to_start else None,
                    )
                except Exception as exc:
                    logger.warning("Failed to cache tour order: %s", exc)

        return tour

    # ------------------------------------------------------------------ #
    # Internal helpers
    # ------------------------------------------------------------------ #

    def _solve_order(
        self,
        stops: Sequence[str],
        start: str,
        return_to_start: bool,
        router: ORToolsRouter,
    ) -> Optional[List[str]]:
        nodes = [start] + list(stops)
        if not return_to_start:
            nodes.append("__TOUR_END__")
        node_count = len(nodes)

        start_index = 0
        end_index = 0 if return_to_start else len(nodes) - 1

        manager = pywrapcp.RoutingIndexManager(len(nodes), 1, [start_index], [end_index])
        routing = pywrapcp.RoutingModel(manager)

        time_matrix = self._build_time_matrix(nodes, router)

        def time_callback(from_index: int, to_index: int) -> int:
            from_node = manager.IndexToNode(from_index)
            to_node = manager.IndexToNode(to_index)
            return time_matrix[from_node][to_node]

        transit_index = routing.RegisterTransitCallback(time_callback)
        routing.SetArcCostEvaluatorOfAllVehicles(transit_index)

        search_params = pywrapcp.DefaultRoutingSearchParameters()
        solver_cfg = self.config.get_solver_config()
        search_params.first_solution_strategy = getattr(
            routing_enums_pb2.FirstSolutionStrategy,
            solver_cfg["first_solution_strategy"],
            routing_enums_pb2.FirstSolutionStrategy.PATH_CHEAPEST_ARC,
        )
        search_params.local_search_metaheuristic = getattr(
            routing_enums_pb2.LocalSearchMetaheuristic,
            solver_cfg["local_search_metaheuristic"],
            routing_enums_pb2.LocalSearchMetaheuristic.GUIDED_LOCAL_SEARCH,
        )
        search_params.time_limit.FromMilliseconds(solver_cfg["time_limit_ms"])

        solution = routing.SolveWithParameters(search_params)
        if not solution:
            logger.warning("OR-Tools failed to optimise TSP order")
            return None

        order: List[str] = []
        index = routing.Start(0)
        while True:
            node = manager.IndexToNode(index)
            waypoint = nodes[node]
            if waypoint != "__TOUR_END__":
                order.append(waypoint)
            if routing.IsEnd(index):
                break
            index = solution.Value(routing.NextVar(index))

        if return_to_start and (not order or order[-1] != start):
            order.append(start)

        return order

    def _build_tour_from_order(
        self,
        order: List[str],
        router: ORToolsRouter,
        return_to_start: bool,
    ) -> Optional[Dict]:
        if len(order) < 2:
            return None

        total_time = 0
        total_distance = 0.0

        current = order[0]
        ship_fuel = router.ship["fuel"]["current"]
        legs: List[Dict] = []

        for target in order[1:]:
            route = router.find_optimal_route(current, target, ship_fuel, prefer_cruise=True)
            if not route:
                return None
            legs.append(route)
            current = target
            ship_fuel = route["final_fuel"]
            total_time += route["total_time"]
            total_distance += sum(step["distance"] for step in route["steps"] if step["action"] == "navigate")

        return {
            "start": order[0],
            "stops": order[1:-1] if return_to_start else order[1:],
            "return_to_start": return_to_start,
            "total_time": total_time,
            "total_distance": total_distance,
            "final_fuel": ship_fuel,
            "legs": legs,
            "total_legs": len(legs),
        }

    def _build_time_matrix(self, nodes: Sequence[str], router: ORToolsRouter) -> List[List[int]]:
        size = len(nodes)
        cruise_cfg = router._cruise_cfg
        drift_cfg = router._drift_cfg
        matrix = [[1_000_000 for _ in range(size)] for _ in range(size)]
        for i, origin in enumerate(nodes):
            for j, target in enumerate(nodes):
                if i == j:
                    matrix[i][j] = 0
                    continue
                if target == "__TOUR_END__":
                    matrix[i][j] = 0
                    continue
                if origin == "__TOUR_END__":
                    matrix[i][j] = 0
                    continue
                metrics = router.get_edge_metrics(origin, target)
                if metrics:
                    matrix[i][j] = metrics.cruise_time or metrics.drift_time
                    continue
                distance = _waypoint_distance(self.graph, origin, target)
                if distance is None:
                    continue
                if cruise_cfg:
                    matrix[i][j] = _time_seconds(distance, cruise_cfg["time_multiplier"], router.engine_speed)
                else:
                    matrix[i][j] = _time_seconds(distance, drift_cfg["time_multiplier"], router.engine_speed)
        return matrix


# --------------------------------------------------------------------------- #
# Fleet partitioning (multi-vehicle VRP)
# --------------------------------------------------------------------------- #


class ORToolsFleetPartitioner:
    """Partition markets amongst scout ships using OR-Tools."""

    def __init__(self, graph: Dict, config: Optional[RoutingConfig] = None):
        self.graph = graph
        self.config = config or RoutingConfig()

    def partition_and_optimize(
        self,
        markets: Sequence[str],
        ships: Sequence[str],
        ship_data: Dict[str, Dict],
    ) -> Dict[str, List[str]]:
        if not markets or not ships:
            return {ship: [] for ship in ships}

        reference_ship = ship_data[ships[0]]
        base_router = ORToolsRouter(self.graph, reference_ship, self.config)
        nodes = list(markets)
        node_index = {node: idx for idx, node in enumerate(nodes)}

        starts = []
        ends = []
        for ship in ships:
            waypoint = ship_data[ship]["nav"]["waypointSymbol"]
            if waypoint not in node_index:
                node_index[waypoint] = len(nodes)
                nodes.append(waypoint)
            index = node_index[waypoint]
            starts.append(index)
            ends.append(index)

        distance_matrix = self._build_distance_matrix(nodes, base_router)

        manager = pywrapcp.RoutingIndexManager(len(nodes), len(ships), starts, ends)
        routing = pywrapcp.RoutingModel(manager)

        def time_callback(from_index: int, to_index: int) -> int:
            from_node = manager.IndexToNode(from_index)
            to_node = manager.IndexToNode(to_index)
            return distance_matrix[from_node][to_node]

        transit_index = routing.RegisterTransitCallback(time_callback)
        routing.SetArcCostEvaluatorOfAllVehicles(transit_index)

        routing.AddDimension(
            transit_index,
            0,
            1_000_000,
            True,
            "TravelTime",
        )
        time_dimension = routing.GetDimensionOrDie("TravelTime")
        time_dimension.SetGlobalSpanCostCoefficient(100)

        for market in markets:
            routing.AddDisjunction([manager.NodeToIndex(node_index[market])], 1_000_000)

        search_params = pywrapcp.DefaultRoutingSearchParameters()
        solver_cfg = self.config.get_solver_config()
        search_params.first_solution_strategy = getattr(
            routing_enums_pb2.FirstSolutionStrategy,
            solver_cfg["first_solution_strategy"],
            routing_enums_pb2.FirstSolutionStrategy.PATH_CHEAPEST_ARC,
        )
        search_params.local_search_metaheuristic = getattr(
            routing_enums_pb2.LocalSearchMetaheuristic,
            solver_cfg["local_search_metaheuristic"],
            routing_enums_pb2.LocalSearchMetaheuristic.GUIDED_LOCAL_SEARCH,
        )
        search_params.time_limit.FromMilliseconds(solver_cfg["time_limit_ms"])

        solution = routing.SolveWithParameters(search_params)
        if not solution:
            logger.warning("OR-Tools failed to partition markets, falling back to empty partitions")
            return {ship: [] for ship in ships}

        assignments: Dict[str, List[str]] = {ship: [] for ship in ships}

        for vehicle, ship in enumerate(ships):
            index = routing.Start(vehicle)
            while not routing.IsEnd(index):
                node = manager.IndexToNode(index)
                waypoint = nodes[node]
                if waypoint in markets and waypoint not in assignments[ship]:
                    assignments[ship].append(waypoint)
                index = solution.Value(routing.NextVar(index))

        return assignments

    def _build_distance_matrix(self, nodes: Sequence[str], router: ORToolsRouter) -> List[List[int]]:
        size = len(nodes)
        matrix = [[1_000_000 for _ in range(size)] for _ in range(size)]
        for i, origin in enumerate(nodes):
            for j, target in enumerate(nodes):
                if i == j:
                    matrix[i][j] = 0
                    continue
                metrics = router.get_edge_metrics(origin, target)
                if metrics:
                    matrix[i][j] = metrics.cruise_time or metrics.drift_time
                    continue
                distance = _waypoint_distance(self.graph, origin, target)
                if distance is None:
                    continue
                multiplier = router._cruise_cfg["time_multiplier"] if router._cruise_cfg else router._drift_cfg["time_multiplier"]
                matrix[i][j] = _time_seconds(distance, multiplier, router.engine_speed)
        return matrix
