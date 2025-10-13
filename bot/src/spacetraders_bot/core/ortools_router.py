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
        # Use 10-fuel granularity for state space reduction (800 capacity → 81 levels)
        self._node_multiplier = (self.fuel_capacity // 10) + 1 if self.fuel_capacity > 0 else 1

        # Lazy-computed edge metrics (computed on-demand)
        self._edge_metrics: Dict[Tuple[str, str], EdgeMetrics] = {}

        # Pre-build adjacency structure (fast - just waypoint connectivity, no metrics)
        # Edge metrics are still computed lazily when needed
        self._adjacency: Dict[str, List[str]] = {wp: [] for wp in self._waypoints}
        self._build_adjacency_structure()

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
        logger.debug(f"🔍 [ROUTING] find_optimal_route: {start} → {goal}, fuel={current_fuel}")

        prefer_cruise = True  # Cruise preference enforced globally
        if start == goal:
            logger.info("✅ [ROUTING] Already at destination")
            return {
                "start": start,
                "goal": goal,
                "total_time": 0,
                "final_fuel": current_fuel,
                "steps": [],
                "ship_speed": self.engine_speed,
            }

        logger.debug(f"🔍 [ROUTING] Checking waypoint indices...")
        if start not in self._wp_index or goal not in self._wp_index:
            logger.error(f"❌ [ROUTING] Waypoint missing: start={start in self._wp_index}, goal={goal in self._wp_index}")
            logger.error("Start or goal waypoint missing from graph (start=%s, goal=%s)", start, goal)
            return None

        if self.fuel_capacity == 0:
            logger.info("🔍 [ROUTING] Probe route (no fuel)")
            return self._probe_route(start, goal)

        logger.debug(f"🔍 [ROUTING] Calling _solve_with_min_cost_flow...")
        path_steps = self._solve_with_min_cost_flow(
            start=start,
            goal=goal,
            current_fuel=current_fuel,
        )

        logger.debug(f"🔍 [ROUTING] _solve_with_min_cost_flow returned: {len(path_steps) if path_steps else 0} steps")

        # Min cost flow should always succeed after branching fix
        # If it still fails, this indicates a genuine routing issue (no path exists)
        if not path_steps:
            logger.error("❌ [ROUTING] Min cost flow failed - no valid route exists")
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

    def _build_adjacency_structure(self) -> None:
        """Pre-build adjacency lists (waypoint connectivity only, no metrics)."""
        for edge in self.graph.get("edges", []):
            origin = edge["from"]
            target = edge["to"]

            # Build undirected adjacency (both directions)
            if origin in self._adjacency and target not in self._adjacency[origin]:
                self._adjacency[origin].append(target)
            if target in self._adjacency and origin not in self._adjacency[target]:
                self._adjacency[target].append(origin)

    def _compute_edge_metrics(self, origin: str, target: str) -> Optional[EdgeMetrics]:
        """Compute edge metrics on-demand for a specific edge pair."""
        # Check if already cached
        if (origin, target) in self._edge_metrics:
            return self._edge_metrics[(origin, target)]

        # Find edge in graph
        edge_data = None
        for edge in self.graph.get("edges", []):
            if (edge["from"] == origin and edge["to"] == target) or \
               (edge["from"] == target and edge["to"] == origin):
                edge_data = edge
                break

        if not edge_data:
            # No explicit edge - compute distance from waypoint coordinates
            distance = _waypoint_distance(self.graph, origin, target)
            if distance is None:
                return None
        else:
            distance = float(edge_data["distance"])

        # Compute metrics
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

        # Cache bidirectionally
        self._edge_metrics[(origin, target)] = metrics
        self._edge_metrics[(target, origin)] = metrics

        return metrics

    def get_edge_metrics(self, origin: str, target: str) -> Optional[EdgeMetrics]:
        """Get edge metrics, computing lazily if not cached."""
        return self._compute_edge_metrics(origin, target)

    def _get_adjacency(self, waypoint: str) -> List[Tuple[str, EdgeMetrics]]:
        """Get adjacency list with edge metrics, computing metrics on-demand."""
        if waypoint not in self._adjacency:
            return []

        # Build list with edge metrics computed lazily
        neighbors_with_metrics: List[Tuple[str, EdgeMetrics]] = []

        for neighbor in self._adjacency[waypoint]:
            metrics = self._compute_edge_metrics(waypoint, neighbor)
            if metrics:
                neighbors_with_metrics.append((neighbor, metrics))

        return neighbors_with_metrics

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
        fuel_level = fuel // 10  # Convert to 10-fuel granularity
        return waypoint_idx * self._node_multiplier + fuel_level

    def _find_waypoint_path(self, start: str, goal: str) -> Optional[List[str]]:
        """Find approximate waypoint path using Dijkstra (ignoring fuel).

        This provides a quick waypoint path that ignores fuel constraints,
        which is then used to build a focused min-cost flow graph containing
        only waypoints on/near the path. This dramatically reduces the state
        space from ALL waypoints to just ~10-15 relevant waypoints.

        Returns:
            List of waypoints from start to goal, or None if no path exists.
        """
        logger.debug(f"🔍 [DIJKSTRA] Starting pathfinding: {start} → {goal}")

        if start not in self._wp_index or goal not in self._wp_index:
            logger.error(f"❌ [DIJKSTRA] Waypoint not in index")
            return None

        import heapq

        # Priority queue: (distance, waypoint)
        pq: List[Tuple[float, str]] = [(0.0, start)]
        distances: Dict[str, float] = {start: 0.0}
        previous: Dict[str, Optional[str]] = {start: None}
        visited: set = set()

        iterations = 0
        while pq:
            iterations += 1
            if iterations % 1000 == 0:
                logger.warning(f"⚠️  [DIJKSTRA] {iterations} iterations, queue size={len(pq)}, visited={len(visited)}")

            current_dist, current = heapq.heappop(pq)

            if current in visited:
                continue
            visited.add(current)

            # Goal reached - reconstruct path
            if current == goal:
                logger.debug(f"✅ [DIJKSTRA] Path found in {iterations} iterations")
                path: List[str] = []
                node: Optional[str] = goal
                while node is not None:
                    path.append(node)
                    node = previous[node]
                path.reverse()
                return path

            # Explore neighbors using lazy adjacency
            neighbors = self._get_adjacency(current)
            if iterations < 10 or iterations % 500 == 0:
                logger.debug(f"🔍 [DIJKSTRA] Exploring {current}: {len(neighbors)} neighbors")

            for neighbor, metrics in neighbors:
                if neighbor in visited:
                    continue

                tentative_distance = current_dist + metrics.distance
                if neighbor not in distances or tentative_distance < distances[neighbor]:
                    distances[neighbor] = tentative_distance
                    previous[neighbor] = current
                    heapq.heappush(pq, (tentative_distance, neighbor))

        # No path found
        logger.error(f"❌ [DIJKSTRA] No path found after {iterations} iterations")
        return None

    def _fallback_to_simple_route(
        self,
        start: str,
        goal: str,
        current_fuel: int,
    ) -> Optional[List[Dict]]:
        """Fallback to simple Dijkstra-based route when min cost flow fails.

        This method provides a simpler routing approach that:
        - Uses _find_waypoint_path() to get the waypoint sequence
        - Manually constructs navigate + refuel steps without OR-Tools
        - Ensures ship never runs out of fuel by inserting refuel stops
        - Uses CRUISE mode when possible, DRIFT when fuel-constrained
        - Returns route in same format as min cost flow solver

        This is less optimal than min cost flow but guaranteed to work
        when the solver produces branching/cycling solutions.

        Args:
            start: Starting waypoint symbol
            goal: Goal waypoint symbol
            current_fuel: Current fuel amount

        Returns:
            List of route steps (navigate + refuel actions), or None if no path exists
        """
        logger.warning(
            f"🔄 [FALLBACK] Using simple Dijkstra route fallback: {start} → {goal}, fuel={current_fuel}"
        )

        # Get waypoint path using Dijkstra (ignoring fuel)
        waypoint_path = self._find_waypoint_path(start, goal)
        if not waypoint_path or len(waypoint_path) < 2:
            logger.error(f"❌ [FALLBACK] No waypoint path found")
            return None

        logger.debug(f"✅ [FALLBACK] Found waypoint path: {' → '.join(waypoint_path)}")

        steps: List[Dict] = []
        fuel = max(0, min(current_fuel, self.fuel_capacity))
        refuel_time = self.config.refuel_time_seconds()

        # Process each leg of the journey
        for i in range(len(waypoint_path) - 1):
            from_waypoint = waypoint_path[i]
            to_waypoint = waypoint_path[i + 1]

            # Get edge metrics
            metrics = self._compute_edge_metrics(from_waypoint, to_waypoint)
            if not metrics:
                logger.error(f"❌ [FALLBACK] No edge metrics for {from_waypoint} → {to_waypoint}")
                return None

            # Determine if we need to refuel before this leg
            # Try CRUISE first, fall back to DRIFT if insufficient fuel
            mode = None
            fuel_cost = None
            travel_time = None

            # Prefer CRUISE if we have enough fuel
            if metrics.cruise_fuel is not None and fuel >= metrics.cruise_fuel:
                mode = "CRUISE"
                fuel_cost = metrics.cruise_fuel
                travel_time = metrics.cruise_time
                logger.debug(
                    f"🔍 [FALLBACK] {from_waypoint} → {to_waypoint}: CRUISE mode "
                    f"({fuel_cost} fuel, {travel_time}s)"
                )
            # Fall back to DRIFT if CRUISE unavailable or insufficient fuel
            elif fuel >= metrics.drift_fuel:
                mode = "DRIFT"
                fuel_cost = metrics.drift_fuel
                travel_time = metrics.drift_time
                logger.debug(
                    f"🔍 [FALLBACK] {from_waypoint} → {to_waypoint}: DRIFT mode "
                    f"({fuel_cost} fuel, {travel_time}s)"
                )
            else:
                # Insufficient fuel even for DRIFT - need to refuel
                waypoint_data = self.graph["waypoints"][from_waypoint]
                has_fuel = bool(waypoint_data.get("has_fuel"))

                if not has_fuel:
                    logger.error(
                        f"❌ [FALLBACK] Insufficient fuel at {from_waypoint} "
                        f"(have {fuel}, need {metrics.drift_fuel} for DRIFT) and no refuel available"
                    )
                    return None

                # Refuel to capacity
                fuel_added = self.fuel_capacity - fuel
                logger.debug(
                    f"⛽ [FALLBACK] Refueling at {from_waypoint}: +{fuel_added} fuel "
                    f"({fuel} → {self.fuel_capacity})"
                )
                steps.append({
                    "action": "refuel",
                    "waypoint": from_waypoint,
                    "fuel_added": fuel_added,
                    "time": refuel_time,
                })
                fuel = self.fuel_capacity

                # Retry mode selection after refuel
                if metrics.cruise_fuel is not None and fuel >= metrics.cruise_fuel:
                    mode = "CRUISE"
                    fuel_cost = metrics.cruise_fuel
                    travel_time = metrics.cruise_time
                elif fuel >= metrics.drift_fuel:
                    mode = "DRIFT"
                    fuel_cost = metrics.drift_fuel
                    travel_time = metrics.drift_time
                else:
                    logger.error(
                        f"❌ [FALLBACK] Still insufficient fuel after refuel at {from_waypoint}"
                    )
                    return None

            # Add navigate step
            steps.append({
                "action": "navigate",
                "from": from_waypoint,
                "to": to_waypoint,
                "mode": mode,
                "time": travel_time,
                "fuel_cost": fuel_cost,
                "distance": metrics.distance,
            })
            fuel -= fuel_cost

            logger.debug(
                f"🚀 [FALLBACK] Navigate {from_waypoint} → {to_waypoint} "
                f"({mode}, -{fuel_cost} fuel, remaining={fuel})"
            )

        logger.info(
            f"✅ [FALLBACK] Simple route complete: {len(steps)} steps, "
            f"final fuel={fuel}/{self.fuel_capacity}"
        )
        return steps

    def _solve_with_min_cost_flow(
        self,
        start: str,
        goal: str,
        current_fuel: int,
    ) -> Optional[List[Dict]]:
        logger.debug(f"🔍 [MCF] _solve_with_min_cost_flow: {start} → {goal}, fuel={current_fuel}")

        flow = min_cost_flow.SimpleMinCostFlow()
        metadata: Dict[int, Dict] = {}

        start_fuel = max(0, min(current_fuel, self.fuel_capacity))
        start_node = self._state_id(start, start_fuel)
        sink_node = len(self._waypoints) * self._node_multiplier

        logger.debug(f"🔍 [MCF] start_node={start_node}, sink_node={sink_node}")

        flow.set_node_supply(start_node, 1)
        flow.set_node_supply(sink_node, -1)

        refuel_time = self.config.refuel_time_seconds()
        drift_penalty = self.DRIFT_PENALTY_SECONDS

        # OPTIMIZATION: Path-first routing
        # Find approximate waypoint path using Dijkstra (ignoring fuel constraints)
        # Then build min-cost flow graph ONLY for waypoints on/near the path
        # This reduces state space from ALL waypoints to ~10-15 relevant waypoints
        logger.debug(f"🔍 [MCF] Finding approximate path with Dijkstra...")
        approximate_path = self._find_waypoint_path(start, goal)

        if approximate_path is None:
            logger.error(f"❌ [MCF] Dijkstra failed to find path")
            logger.warning("Failed to find approximate path from %s to %s", start, goal)
            return None

        logger.debug(f"✅ [MCF] Dijkstra found path with {len(approximate_path)} waypoints")

        # Build set of relevant waypoints: path waypoints + their immediate neighbors
        # This ensures we don't miss alternate refuel stops or better routes
        relevant_waypoints: set = set(approximate_path)
        for waypoint in approximate_path:
            for neighbor, _ in self._get_adjacency(waypoint):
                relevant_waypoints.add(neighbor)

        logger.debug(f"🔍 [MCF] Relevant waypoints: {len(relevant_waypoints)} (from {len(self._waypoints)} total)")
        logger.debug(
            "Path-first routing: %d waypoints on path, %d relevant waypoints (from %d total)",
            len(approximate_path),
            len(relevant_waypoints),
            len(self._waypoints),
        )

        # Generate arcs ONLY for relevant waypoints (dramatically reduces state space)
        logger.debug(f"🔍 [MCF] Generating arcs...")
        arc_count = 0
        for waypoint in relevant_waypoints:
            waypoint_data = self.graph["waypoints"][waypoint]
            has_fuel = bool(waypoint_data.get("has_fuel"))

            # OPTIMIZATION: Use 10-fuel granularity instead of 1-fuel increments
            # This reduces fuel states from 801 (0-800) to 81 (0, 10, 20, ..., 800)
            # For 800-capacity tank: 801 states → 81 states (10x reduction)
            for fuel in range(0, self.fuel_capacity + 1, 10):
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
                    arc_count += 1
                    if arc_count % 10000 == 0:
                        logger.debug(f"🔍 [MCF] Generated {arc_count} arcs...")

                if fuel == 0:
                    continue

                # Only consider neighbors that are in the relevant waypoint set
                # (Otherwise we'd expand to the entire graph)
                for neighbor, metrics in self._get_adjacency(waypoint):
                    if neighbor not in relevant_waypoints:
                        continue
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

                        # CRITICAL FIX: Add small penalty to zero-distance orbital hops
                        # Without this, min cost flow solver creates cycles through orbitals
                        # (E43 ↔ E45 ↔ E46, all cost=0, solver sees them as equally optimal)
                        # Penalty: 1 second for zero-distance hops (makes direct routes preferred)
                        if metrics.distance == 0 and waypoint != neighbor:
                            # Orbital hop (planet ↔ moon at same coordinates)
                            orbital_hop_penalty = 1
                            logger.debug(
                                f"   Adding orbital hop penalty: {waypoint} ↔ {neighbor} "
                                f"(distance=0, +{orbital_hop_penalty}s penalty)"
                            )
                            base_time += orbital_hop_penalty

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
                        arc_count += 1
                        if arc_count % 10000 == 0:
                            logger.debug(f"🔍 [MCF] Generated {arc_count} arcs...")

        logger.debug(f"✅ [MCF] Generated {arc_count} arcs total")

        # CRITICAL FIX: Connect goal-fuel states to sink with unique costs to prevent branching
        # The solver was creating multiple paths because all goal-fuel states had equal cost to sink
        # Solution: Connect only states that exist in our 10-fuel granularity (0, 10, 20, ..., capacity)
        # and give each a unique cost (prefer higher fuel arrivals)
        logger.debug(f"🔍 [MCF] Connecting goal state to sink (max fuel preference)...")

        # Connect only fuel states that exist in our state space (10-fuel granularity)
        # This prevents duplicate sink costs and ensures unique optimal path
        for fuel in range(0, self.fuel_capacity + 1, 10):
            # Higher fuel = lower cost (prefer arriving with more fuel remaining)
            # Cost increases as fuel decreases to break ties and force unique solution
            sink_cost = self.fuel_capacity - fuel

            arc = flow.add_arc_with_capacity_and_unit_cost(
                self._state_id(goal, fuel),
                sink_node,
                1,  # Capacity 1 ensures only one path
                sink_cost,  # Prefer higher fuel arrivals (unique costs)
            )
            metadata[arc] = {"type": "sink", "goal_fuel": fuel}

        logger.debug(f"🔍 [MCF] Solving min cost flow...")
        status = flow.solve()
        logger.debug(f"✅ [MCF] Solver completed with status={status}")

        if status != flow.OPTIMAL:
            logger.warning("Min cost flow solver failed with status %s", status)
            return None

        # Build mapping of active arcs with deterministic tie-breaking and cycle detection
        logger.debug(f"🔍 [MCF-RECONSTRUCT] Building active arc mapping from {flow.num_arcs()} total arcs...")

        # STEP 1: Collect all active arcs
        active_arcs: List[Tuple[int, int, int]] = []  # [(arc_id, tail, head), ...]
        for arc in range(flow.num_arcs()):
            if flow.flow(arc) > 0:
                tail = flow.tail(arc)
                head = flow.head(arc)
                active_arcs.append((arc, tail, head))
                logger.debug("Active arc %s -> %s (%s)", tail, head, metadata.get(arc, {}))

        logger.debug(f"✅ [MCF-RECONSTRUCT] Found {len(active_arcs)} active arcs (out of {flow.num_arcs()} total)")

        # STEP 2: Build graph of active arcs to detect cycles
        graph_arcs: Dict[int, List[Tuple[int, int]]] = {}  # tail → [(head, arc_id), ...]
        for arc_id, tail, head in active_arcs:
            if tail not in graph_arcs:
                graph_arcs[tail] = []
            graph_arcs[tail].append((head, arc_id))

        # STEP 3: For nodes with multiple outgoing arcs, select ONE using tie-breaking
        active_arc_by_tail: Dict[int, int] = {}
        branching_resolution_count = 0

        for tail, candidates in graph_arcs.items():
            if len(candidates) == 1:
                # Only one arc from this node - use it directly
                head, arc_id = candidates[0]
                active_arc_by_tail[tail] = arc_id
            else:
                # BRANCHING: Multiple arcs from same node - apply deterministic tie-breaking
                branching_resolution_count += 1
                logger.info(
                    f"🔍 [MCF-RECONSTRUCT] Multiple active arcs ({len(candidates)}) from node {tail}. "
                    f"Applying deterministic tie-breaking..."
                )

                # Evaluate all candidates and select best according to priority rules
                best_arc_id = None
                best_head = None
                best_cost = float('inf')
                best_is_nav = False
                best_is_goal = False

                for head, arc_id in candidates:
                    cost = flow.unit_cost(arc_id)
                    arc_meta = metadata.get(arc_id, {})
                    is_nav = arc_meta.get("type") == "navigate"
                    is_sink = arc_meta.get("type") == "sink"
                    is_goal = (head == sink_node) or is_sink

                    logger.debug(
                        f"   Candidate arc {arc_id} → node {head}: cost={cost}, "
                        f"type='{arc_meta.get('type')}', is_goal={is_goal}"
                    )

                    # PRIORITY 1: Prefer goal/sink arcs (ensures we reach destination)
                    if is_goal and not best_is_goal:
                        best_arc_id = arc_id
                        best_head = head
                        best_cost = cost
                        best_is_nav = is_nav
                        best_is_goal = True
                        logger.debug(f"     → NEW BEST (goal/sink arc)")
                        continue
                    elif best_is_goal and not is_goal:
                        # Keep existing best (it's a goal arc)
                        continue

                    # PRIORITY 2: Lower cost
                    if cost < best_cost:
                        best_arc_id = arc_id
                        best_head = head
                        best_cost = cost
                        best_is_nav = is_nav
                        best_is_goal = is_goal
                        logger.debug(f"     → NEW BEST (lower cost: {cost} < {best_cost})")
                        continue
                    elif cost > best_cost:
                        # Keep existing best
                        continue

                    # PRIORITY 3: Prefer navigation over refuel (when costs equal)
                    if is_nav and not best_is_nav:
                        best_arc_id = arc_id
                        best_head = head
                        best_cost = cost
                        best_is_nav = is_nav
                        best_is_goal = is_goal
                        logger.debug(f"     → NEW BEST (navigation type)")
                        continue
                    elif best_is_nav and not is_nav:
                        # Keep existing best
                        continue

                    # PRIORITY 4: Lexicographic ordering (lower arc_id wins)
                    if best_arc_id is None or arc_id < best_arc_id:
                        best_arc_id = arc_id
                        best_head = head
                        best_cost = cost
                        best_is_nav = is_nav
                        best_is_goal = is_goal
                        logger.debug(f"     → NEW BEST (lexicographic: {arc_id})")

                # Store the best arc for this tail
                active_arc_by_tail[tail] = best_arc_id
                best_meta = metadata.get(best_arc_id, {})
                logger.info(
                    f"✅ [MCF-RECONSTRUCT] Selected arc {best_arc_id} → node {best_head} "
                    f"(cost {best_cost}, type '{best_meta.get('type')}')"
                )

        if branching_resolution_count > 0:
            logger.info(
                f"✅ [MCF-RECONSTRUCT] Resolved {branching_resolution_count} branching scenarios "
                f"using priority: goal/sink > cost > navigation > lexicographic"
            )

        steps: List[Dict] = []
        current_node = start_node
        visited_nodes: set = set()  # Track visited nodes to detect infinite loops
        iterations = 0

        logger.debug(f"🔍 [MCF-RECONSTRUCT] Starting route reconstruction from node {current_node} to sink {sink_node}")

        while current_node != sink_node:
            iterations += 1

            # Emit progress warnings every 1000 iterations (like Dijkstra loop)
            if iterations % 1000 == 0:
                logger.warning(
                    f"⚠️  [MCF-RECONSTRUCT] {iterations} iterations, current_node={current_node}, "
                    f"sink_node={sink_node}, steps={len(steps)}, visited_nodes={len(visited_nodes)}"
                )

            # Safety: detect infinite loops (max 10,000 iterations or revisiting same node)
            if iterations > 10_000:
                logger.error(
                    f"❌ [MCF-RECONSTRUCT] INFINITE LOOP DETECTED: exceeded 10,000 iterations at node {current_node}. "
                    f"Steps so far: {len(steps)}, visited nodes: {len(visited_nodes)}"
                )
                return None

            if current_node in visited_nodes:
                logger.error(
                    f"❌ [MCF-RECONSTRUCT] INFINITE LOOP DETECTED: revisited node {current_node} at iteration {iterations}. "
                    f"Steps so far: {len(steps)}, visited nodes: {visited_nodes}"
                )
                return None

            visited_nodes.add(current_node)

            # Log progress every 10 iterations
            if iterations <= 10 or iterations % 10 == 0:
                logger.debug(f"🔍 [MCF-RECONSTRUCT] Iteration {iterations}: current_node={current_node}, steps={len(steps)}")

            arc = active_arc_by_tail.get(current_node)
            if arc is None:
                logger.error(
                    f"❌ [MCF-RECONSTRUCT] Failed to reconstruct routing path: missing arc for node {current_node} "
                    f"at iteration {iterations}. Steps reconstructed: {len(steps)}, visited nodes: {visited_nodes}"
                )
                return None

            data = metadata.get(arc)
            if not data:
                logger.error(f"❌ [MCF-RECONSTRUCT] Arc metadata missing for arc {arc} at iteration {iterations}")
                return None

            next_node = flow.head(arc)
            logger.debug(f"🔍 [MCF-RECONSTRUCT] Arc {arc}: {current_node} -> {next_node}, type={data['type']}")

            if data["type"] == "refuel":
                fuel_added = data["fuel_after"] - data["fuel_before"]
                if fuel_added > 0:
                    logger.debug(f"🔍 [MCF-RECONSTRUCT] Adding refuel step at {data['waypoint']}: +{fuel_added} fuel")
                    steps.append({
                        "action": "refuel",
                        "waypoint": data["waypoint"],
                        "fuel_added": fuel_added,
                        "time": data["time"],
                    })
            elif data["type"] == "navigate":
                logger.debug(
                    f"🔍 [MCF-RECONSTRUCT] Adding navigate step: {data['from']} -> {data['to']} "
                    f"({data['mode']}, {data['fuel_cost']} fuel)"
                )
                steps.append({
                    "action": "navigate",
                    "from": data["from"],
                    "to": data["to"],
                    "mode": data["mode"],
                    "time": data["time"],
                    "fuel_cost": data["fuel_cost"],
                    "distance": data["distance"],
                })
            elif data["type"] == "sink":
                logger.debug(f"🔍 [MCF-RECONSTRUCT] Reached sink arc at iteration {iterations}")

            current_node = next_node

        logger.debug(f"✅ [MCF-RECONSTRUCT] Route reconstruction complete in {iterations} iterations, {len(steps)} steps")
        return steps

    # ------------------------------------------------------------------ #
    # Probe fast-path (no fuel capacity)
    # ------------------------------------------------------------------ #

    def _probe_route(self, start: str, goal: str) -> Optional[Dict]:
        """Simplified routing for probes that do not consume fuel.

        Probes/satellites have no fuel constraints, so we use direct distance
        calculation instead of OR-Tools routing. This is instant vs 10-minute
        timeout for full routing solver.
        """
        # Direct distance calculation (instant, no routing needed)
        distance = _waypoint_distance(self.graph, start, goal)
        if distance is None:
            logger.error("Failed to compute distance %s -> %s", start, goal)
            return None

        # Compute travel time using CRUISE mode
        travel_time = _time_seconds(
            distance,
            self._cruise_cfg["time_multiplier"],
            self.engine_speed
        )

        return {
            "start": start,
            "goal": goal,
            "total_time": travel_time,
            "final_fuel": 0,
            "steps": [{
                "action": "navigate",
                "from": start,
                "to": goal,
                "mode": "CRUISE",
                "time": travel_time,
                "fuel_cost": 0,
                "distance": distance,
            }],
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

        # Check if this is a probe (fuel_capacity=0) before creating router
        fuel_capacity = int(ship_data.get("fuel", {}).get("capacity", 0))
        is_probe = fuel_capacity == 0

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
                    # For probes, skip router creation entirely (fast path)
                    if is_probe:
                        return self._build_probe_tour_from_order(cached["tour_order"], ship_data, return_to_start)
                    else:
                        router = ORToolsRouter(self.graph, ship_data, self.config)
                        return self._build_tour_from_order(cached["tour_order"], router, return_to_start)

        # Solve TSP without creating full router (performance optimization)
        order = self._solve_order(stops, start, return_to_start, ship_data)
        if order is None:
            return None

        # For probes, skip router creation (fast path)
        if is_probe:
            tour = self._build_probe_tour_from_order(order, ship_data, return_to_start)
        else:
            router = ORToolsRouter(self.graph, ship_data, self.config)
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

    def _build_probe_tour_from_order(
        self,
        order: List[str],
        ship_data: Dict,
        return_to_start: bool,
    ) -> Optional[Dict]:
        """Build tour for probes without creating router (fast path).

        Probes have fuel_capacity=0, so we skip all fuel calculations and
        router initialization. This avoids O(N²) dense edge generation.
        """
        if len(order) < 2:
            return None

        # VALIDATION: Check cached tour order for fuel stations (stale cache prevention)
        # Fuel stations should NEVER appear in scout tours - they have no trade goods
        # If found, invalidate cache and force rebuild with current filtering logic
        graph_waypoints = self.graph.get("waypoints", {})
        for waypoint in order:
            wp_data = graph_waypoints.get(waypoint)
            if wp_data and wp_data.get("type") == "FUEL_STATION":
                logger.warning(
                    "Cached tour contains fuel station %s (type=%s), invalidating cache and rebuilding",
                    waypoint,
                    wp_data.get("type")
                )
                return None  # Force cache miss and rebuild tour

        # Get config for travel time calculation
        cruise_cfg = self.config.get_flight_mode_config("CRUISE")
        engine_speed = int(ship_data["engine"]["speed"])

        total_time = 0
        total_distance = 0.0
        current = order[0]
        legs: List[Dict] = []

        # Direct distance calculation for each leg
        for target in order[1:]:
            distance = _waypoint_distance(self.graph, current, target)
            if distance is None:
                logger.error("Failed to compute distance %s -> %s", current, target)
                return None

            travel_time = _time_seconds(distance, cruise_cfg["time_multiplier"], engine_speed)

            legs.append({
                "start": current,
                "goal": target,
                "total_time": travel_time,
                "final_fuel": 0,
                "steps": [{
                    "action": "navigate",
                    "from": current,
                    "to": target,
                    "mode": "CRUISE",
                    "time": travel_time,
                    "fuel_cost": 0,
                    "distance": distance,
                }],
                "ship_speed": engine_speed,
            })

            total_time += travel_time
            total_distance += distance
            current = target

        return {
            "start": order[0],
            "stops": order[1:-1] if return_to_start else order[1:],
            "return_to_start": return_to_start,
            "total_time": total_time,
            "total_distance": total_distance,
            "final_fuel": 0,
            "legs": legs,
            "total_legs": len(legs),
        }

    def _solve_order(
        self,
        stops: Sequence[str],
        start: str,
        return_to_start: bool,
        ship_data: Dict,
    ) -> Optional[List[str]]:
        nodes = [start] + list(stops)
        if not return_to_start:
            nodes.append("__TOUR_END__")
        node_count = len(nodes)

        start_index = 0
        end_index = 0 if return_to_start else len(nodes) - 1

        manager = pywrapcp.RoutingIndexManager(len(nodes), 1, [start_index], [end_index])
        routing = pywrapcp.RoutingModel(manager)

        time_matrix = self._build_time_matrix(nodes, ship_data)

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

        logger.info(f"OR-Tools TSP: optimizing {len(stops)} stops, timeout={solver_cfg['time_limit_ms']}ms, strategy={solver_cfg['first_solution_strategy']}, metaheuristic={solver_cfg['local_search_metaheuristic']}")
        import time
        start_time = time.time()

        solution = routing.SolveWithParameters(search_params)

        elapsed = time.time() - start_time
        if solution:
            logger.info(f"OR-Tools TSP: solution found in {elapsed:.1f}s, objective={solution.ObjectiveValue()}")
        else:
            logger.warning(f"OR-Tools failed to optimise TSP order after {elapsed:.1f}s")
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

        # VALIDATION: Check cached tour order for fuel stations (stale cache prevention)
        # Fuel stations should NEVER appear in scout tours - they have no trade goods
        # If found, invalidate cache and force rebuild with current filtering logic
        graph_waypoints = self.graph.get("waypoints", {})
        for waypoint in order:
            wp_data = graph_waypoints.get(waypoint)
            if wp_data and wp_data.get("type") == "FUEL_STATION":
                logger.warning(
                    "Cached tour contains fuel station %s (type=%s), invalidating cache and rebuilding",
                    waypoint,
                    wp_data.get("type")
                )
                return None  # Force cache miss and rebuild tour

        total_time = 0
        total_distance = 0.0

        current = order[0]
        ship_fuel = router.ship["fuel"]["current"]
        legs: List[Dict] = []

        # For probes (fuel_capacity=0), use fast direct distance calculation
        # For normal ships (fuel_capacity>0), use full fuel-aware routing
        is_probe = router.fuel_capacity == 0

        if is_probe:
            # Fast path for probes: direct distance, no routing needed
            cruise_cfg = router._cruise_cfg
            engine_speed = router.engine_speed

            for target in order[1:]:
                distance = _waypoint_distance(self.graph, current, target)
                if distance is None:
                    logger.error("Failed to compute distance %s -> %s", current, target)
                    return None

                travel_time = _time_seconds(distance, cruise_cfg["time_multiplier"], engine_speed)

                legs.append({
                    "start": current,
                    "goal": target,
                    "total_time": travel_time,
                    "final_fuel": 0,
                    "steps": [{
                        "action": "navigate",
                        "from": current,
                        "to": target,
                        "mode": "CRUISE",
                        "time": travel_time,
                        "fuel_cost": 0,
                        "distance": distance,
                    }],
                    "ship_speed": engine_speed,
                })

                total_time += travel_time
                total_distance += distance
                current = target
        else:
            # Full fuel-aware routing for normal ships (with refuel stops)
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

    def _apply_orbital_jitter(self, nodes: Sequence[str]) -> Dict[str, Tuple[float, float]]:
        """
        Apply tiny coordinate jitter to orbital waypoints to prevent OR-Tools confusion.

        Orbitals (planets + their moons) have identical coordinates in database, which
        creates distance=0 and breaks TSP assumptions. Add radial jitter for TSP only.

        Returns:
            Dictionary mapping waypoint symbols to jittered (x, y) coordinates
        """
        graph_waypoints = self.graph.get("waypoints", {})
        coords: Dict[str, Tuple[float, float]] = {}
        coord_groups: Dict[Tuple[float, float], List[str]] = {}  # Group by coordinates

        # Group waypoints with identical coordinates
        for symbol in nodes:
            if symbol == "__TOUR_END__":
                continue
            if symbol not in graph_waypoints:
                continue

            x = float(graph_waypoints[symbol]["x"])
            y = float(graph_waypoints[symbol]["y"])
            coord_key = (x, y)

            if coord_key not in coord_groups:
                coord_groups[coord_key] = []
            coord_groups[coord_key].append(symbol)

        # Apply jitter to groups with >1 waypoint (orbitals)
        for (base_x, base_y), group in coord_groups.items():
            if len(group) == 1:
                # Single waypoint at this coordinate - use original
                coords[group[0]] = (base_x, base_y)
            else:
                # Multiple waypoints - apply radial jitter
                # Use 5.0 units (large enough for OR-Tools to see as distinct waypoints)
                # This is ~5% of typical distances (100-200 units) and prevents crossing confusion
                jitter_radius = 5.0
                for i, symbol in enumerate(group):
                    angle = (2 * math.pi * i) / len(group)  # Spread evenly in circle
                    jittered_x = base_x + jitter_radius * math.cos(angle)
                    jittered_y = base_y + jitter_radius * math.sin(angle)
                    coords[symbol] = (jittered_x, jittered_y)

        return coords

    def _build_time_matrix(self, nodes: Sequence[str], ship_data: Dict) -> List[List[int]]:
        """Build time matrix directly from waypoint distances (no router needed).

        Applies orbital jitter to prevent TSP confusion from waypoints at identical coordinates.
        """
        size = len(nodes)
        matrix = [[1_000_000 for _ in range(size)] for _ in range(size)]

        # Get flight mode configs
        try:
            cruise_cfg = self.config.get_flight_mode_config("CRUISE")
        except RoutingConfigError:
            cruise_cfg = None
        drift_cfg = self.config.get_flight_mode_config("DRIFT")

        # Get ship speed
        engine_speed = int(ship_data["engine"]["speed"])

        # Apply orbital jitter to prevent 0-distance waypoints confusing OR-Tools
        jittered_coords = self._apply_orbital_jitter(nodes)

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

                # Compute distance using jittered coordinates if available
                if origin in jittered_coords and target in jittered_coords:
                    c1 = jittered_coords[origin]
                    c2 = jittered_coords[target]
                    distance = math.hypot(c2[0] - c1[0], c2[1] - c1[1])
                else:
                    # Fallback to graph distance for waypoints not in jittered set
                    distance = _waypoint_distance(self.graph, origin, target)
                    if distance is None:
                        continue

                # Use CRUISE if available, otherwise DRIFT
                if cruise_cfg:
                    matrix[i][j] = _time_seconds(distance, cruise_cfg["time_multiplier"], engine_speed)
                else:
                    matrix[i][j] = _time_seconds(distance, drift_cfg["time_multiplier"], engine_speed)

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

        # CRITICAL FIX: Calculate maximum possible distance cost in system
        # This ensures disjunction penalty is ALWAYS higher than any market's cost
        # Prevents OR-Tools from dropping extreme outliers like J53
        max_distance_cost = 0
        for row in distance_matrix:
            max_distance_cost = max(max_distance_cost, max(row))

        # Set disjunction penalty = 10x max cost (makes markets essentially mandatory)
        # OR-Tools will only drop markets if literally impossible to reach (disconnected graph)
        disjunction_penalty = max(max_distance_cost * 10, 10_000_000)  # Minimum 10M to handle edge cases
        logger.info(
            f"📊 OR-Tools VRP: max distance cost={max_distance_cost}, "
            f"disjunction penalty={disjunction_penalty} (10x max cost)"
        )

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
            disjunction_penalty,  # Use calculated penalty instead of hardcoded value
            True,
            "TravelTime",
        )
        time_dimension = routing.GetDimensionOrDie("TravelTime")
        time_dimension.SetGlobalSpanCostCoefficient(100)

        for market in markets:
            routing.AddDisjunction([manager.NodeToIndex(node_index[market])], disjunction_penalty)

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
        assigned_waypoints: set = set()  # Track waypoints assigned to ANY ship

        for vehicle, ship in enumerate(ships):
            logger.info(f"Processing vehicle {vehicle} ({ship})")
            index = routing.Start(vehicle)
            while not routing.IsEnd(index):
                node = manager.IndexToNode(index)
                waypoint = nodes[node]
                # CRITICAL: Check waypoint not already assigned to ANY ship (not just this ship)
                if waypoint in markets:
                    if waypoint not in assigned_waypoints:
                        logger.info(f"  Assigning {waypoint} to {ship}")
                        assignments[ship].append(waypoint)
                        assigned_waypoints.add(waypoint)  # Mark as assigned globally
                    else:
                        logger.warning(f"  DUPLICATE DETECTED: {waypoint} already assigned, skipping for {ship}")
                index = solution.Value(routing.NextVar(index))

        # CRITICAL VALIDATION: Check if any markets were dropped by OR-Tools
        # With proper disjunction penalty (10x max distance cost), this should NEVER happen
        # If it does, it indicates a bug in penalty calculation or disconnected graph
        dropped_markets = set(markets) - assigned_waypoints
        if dropped_markets:
            # FAIL HARD - markets must be included in OR-Tools optimization
            # Manual assignment after optimization breaks tour balance and is NOT acceptable
            raise RoutingError(
                f"❌ OR-Tools VRP dropped {len(dropped_markets)} markets during partitioning! "
                f"Markets: {dropped_markets}\n"
                f"This should NEVER happen with proper disjunction penalty calculation.\n"
                f"Possible causes:\n"
                f"  1. Bug in disjunction penalty calculation (should be 10x max distance cost)\n"
                f"  2. Disconnected graph (markets unreachable from any ship)\n"
                f"  3. OR-Tools solver failure (infeasible solution)\n"
                f"DO NOT use manual fallback assignment - it breaks tour optimization."
            )

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
