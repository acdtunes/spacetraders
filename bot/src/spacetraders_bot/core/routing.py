#!/usr/bin/env python3
from __future__ import annotations

"""
Compatibility layer exposing routing utilities backed by OR-Tools.

This module preserves the public API that other components and tests expect
(`TimeCalculator`, `FuelCalculator`, `GraphBuilder`, `RouteOptimizer`,
`TourOptimizer`) while delegating the heavy lifting to the new OR-Tools
implementations.
"""

import json
import logging
import math
from pathlib import Path
from typing import Dict, List, Optional, Sequence, Tuple

from ..helpers import paths
from .database import get_database
from .ortools_router import ORToolsRouter, ORToolsTSP
from .routing_config import RoutingConfig, RoutingConfigError

logger = logging.getLogger(__name__)

_GLOBAL_ROUTING_CONFIG = RoutingConfig()


# --------------------------------------------------------------------------- #
# Utility helpers preserved for backwards compatibility
# --------------------------------------------------------------------------- #


def euclidean_distance(x1: float, y1: float, x2: float, y2: float) -> float:
    """Calculate Euclidean distance between two coordinates."""
    return math.hypot(x2 - x1, y2 - y1)


def parse_waypoint_symbol(symbol: str) -> Tuple[str, str]:
    """Split waypoint symbol into system symbol and full waypoint identifier."""
    parts = symbol.split("-")
    if len(parts) >= 3:
        system = f"{parts[0]}-{parts[1]}"
        return system, symbol
    return symbol, symbol


# --------------------------------------------------------------------------- #
# Calculators (time & fuel)
# --------------------------------------------------------------------------- #


class TimeCalculator:
    """Calculate travel time using routing configuration constants."""

    @staticmethod
    def travel_time(distance: float, engine_speed: int, mode: str = "CRUISE") -> int:
        if distance == 0:
            return 0
        try:
            mode_cfg = _GLOBAL_ROUTING_CONFIG.get_flight_mode_config(mode)
        except RoutingConfigError:
            mode_cfg = _GLOBAL_ROUTING_CONFIG.get_flight_mode_config("CRUISE")
        multiplier = mode_cfg["time_multiplier"]
        seconds = round((distance * multiplier) / max(1, engine_speed))
        return max(1, int(seconds))

    @staticmethod
    def format_time(seconds: int) -> str:
        if seconds < 60:
            return f"{seconds}s"
        if seconds < 3600:
            return f"{seconds // 60}m {seconds % 60}s"
        hours = seconds // 3600
        minutes = (seconds % 3600) // 60
        return f"{hours}h {minutes}m"


class FuelCalculator:
    """Compute fuel consumption and affordability checks."""

    @staticmethod
    def fuel_cost(distance: float, mode: str = "CRUISE") -> int:
        if distance == 0:
            return 0
        try:
            mode_cfg = _GLOBAL_ROUTING_CONFIG.get_flight_mode_config(mode)
        except RoutingConfigError:
            mode_cfg = _GLOBAL_ROUTING_CONFIG.get_flight_mode_config("CRUISE")
        rate = mode_cfg["fuel_rate"]
        return max(1, math.ceil(distance * rate))

    @staticmethod
    def can_afford(distance: float, current_fuel: int, mode: str = "CRUISE") -> bool:
        required = FuelCalculator.fuel_cost(distance, mode)
        margin = _GLOBAL_ROUTING_CONFIG.fuel_safety_margin()
        return current_fuel >= required * (1 + margin)


# --------------------------------------------------------------------------- #
# Graph Builder (unchanged behaviour, still used for database sync)
# --------------------------------------------------------------------------- #


class GraphBuilder:
    """Build system navigation graphs from the SpaceTraders API."""

    def __init__(self, api_client, db_path: Optional[Path | str] = None):
        self.api = api_client
        resolved_db_path = Path(db_path) if db_path else paths.sqlite_path()
        self.db = get_database(resolved_db_path)
        self.logger = logging.getLogger(__name__ + ".GraphBuilder")

    def build_system_graph(self, system_symbol: str) -> Optional[Dict]:
        """
        Build complete navigation graph for a system and save to database.
        """
        self.logger.info("Building graph for system %s...", system_symbol)

        all_waypoints: List[Dict] = []
        page = 1
        limit = 20

        while True:
            result = self.api.list_waypoints(system_symbol, limit=limit, page=page)
            if not result or "data" not in result:
                break

            waypoints_page = result["data"]
            all_waypoints.extend(waypoints_page)

            self.logger.info("  Fetched page %s: %s waypoints", page, len(waypoints_page))

            meta = result.get("meta", {})
            total_pages = meta.get("total", 0) // limit + (1 if meta.get("total", 0) % limit > 0 else 0)

            if page >= total_pages or len(waypoints_page) < limit:
                break

            page += 1

            if page > 50:
                self.logger.warning("Reached safety limit of 50 pages")
                break

        if not all_waypoints:
            self.logger.error("No waypoints found for system %s", system_symbol)
            return None

        waypoints = all_waypoints
        graph = {
            "system": system_symbol,
            "waypoints": {},
            "edges": [],
        }

        for waypoint in waypoints:
            traits = [t["symbol"] for t in waypoint.get("traits", [])]
            has_fuel = "MARKETPLACE" in traits or "FUEL_STATION" in traits
            graph["waypoints"][waypoint["symbol"]] = {
                "type": waypoint.get("type"),
                "x": waypoint.get("x"),
                "y": waypoint.get("y"),
                "traits": traits,
                "has_fuel": has_fuel,
                "orbitals": [o["symbol"] for o in waypoint.get("orbitals", [])],
            }

        waypoint_list = list(graph["waypoints"].keys())

        for i, wp1 in enumerate(waypoint_list):
            wp1_data = graph["waypoints"][wp1]
            for wp2 in waypoint_list[i + 1:]:
                wp2_data = graph["waypoints"][wp2]

                is_orbital = wp2 in wp1_data.get("orbitals", []) or wp1 in wp2_data.get("orbitals", [])
                if is_orbital:
                    distance = 0.0
                    edge_type = "orbital"
                else:
                    distance = euclidean_distance(
                        wp1_data["x"], wp1_data["y"],
                        wp2_data["x"], wp2_data["y"],
                    )
                    edge_type = "normal"

                distance = round(distance, 2)
                graph["edges"].append({
                    "from": wp1,
                    "to": wp2,
                    "distance": distance,
                    "type": edge_type,
                })
                graph["edges"].append({
                    "from": wp2,
                    "to": wp1,
                    "distance": distance,
                    "type": edge_type,
                })

        with self.db.transaction() as conn:
            self.db.save_system_graph(conn, system_symbol, graph)

        self.logger.info("Graph saved to database")
        self.logger.info("  Waypoints: %s", len(graph["waypoints"]))
        self.logger.info("  Edges: %s", len(graph["edges"]))
        self.logger.info(
            "  Fuel stations: %s",
            sum(1 for wp in graph["waypoints"].values() if wp["has_fuel"]),
        )
        return graph

    def load_system_graph(self, system_symbol: str) -> Optional[Dict]:
        """Load system graph from the database."""
        with self.db.connection() as conn:
            graph = self.db.get_system_graph(conn, system_symbol)
            if graph:
                self.logger.info("Loaded graph for %s from database", system_symbol)
                self.logger.info("  Waypoints: %s", len(graph.get("waypoints", {})))
                self.logger.info("  Edges: %s", len(graph.get("edges", [])))
            else:
                self.logger.warning("No graph found for %s in database", system_symbol)
            return graph


# --------------------------------------------------------------------------- #
# OR-Tools backed wrappers
# --------------------------------------------------------------------------- #


class RouteOptimizer:
    """Compatibility wrapper around the new OR-Tools router."""

    def __init__(self, graph: Dict, ship_data: Dict, config: Optional[RoutingConfig] = None):
        self._config = config or RoutingConfig()
        self._router = ORToolsRouter(graph, ship_data, self._config)

    def find_optimal_route(
        self,
        start: str,
        goal: str,
        current_fuel: int,
        prefer_cruise: bool = True,
    ) -> Optional[Dict]:
        return self._router.find_optimal_route(start, goal, current_fuel, prefer_cruise=prefer_cruise)


class TourOptimizer:
    """Compatibility wrapper exposing the legacy TourOptimizer API."""

    def __init__(
        self,
        graph: Dict,
        ship_data: Dict,
        db_path: Optional[Path | str] = None,
        config: Optional[RoutingConfig] = None,
    ):
        self.graph = graph
        self.ship_data = ship_data
        self.config = config or RoutingConfig()
        resolved_db = None
        if db_path is not None:
            resolved_db = Path(db_path)
        self._tsp = ORToolsTSP(graph, self.config, resolved_db)

    def plan_tour(
        self,
        start: str,
        stops: Sequence[str],
        current_fuel: int,
        return_to_start: bool = False,
        algorithm: str = "ortools",
        use_cache: bool = True,
    ) -> Optional[Dict]:
        ship_data = json.loads(json.dumps(self.ship_data))  # simple deep copy
        ship_data["fuel"]["current"] = current_fuel

        stops_list = list(stops)
        if not stops_list:
            return None

        tour = self._tsp.optimise_tour(
            waypoints=stops_list,
            start=start,
            ship_data=ship_data,
            return_to_start=return_to_start,
            use_cache=use_cache,
            algorithm=algorithm or "ortools",
        )
        if tour:
            tour["algorithm"] = algorithm or "ortools"
        return tour

    def solve_nearest_neighbor(
        self,
        start: str,
        stops: Sequence[str],
        current_fuel: int,
        return_to_start: bool = False,
        use_cache: bool = True,
    ) -> Optional[Dict]:
        """Backward-compatible alias that now delegates to OR-Tools."""

        return self.plan_tour(
            start=start,
            stops=stops,
            current_fuel=current_fuel,
            return_to_start=return_to_start,
            algorithm="greedy",
            use_cache=use_cache,
        )

    def two_opt_improve(self, tour: Dict, max_iterations: int = 1000) -> Dict:
        """Legacy hook retained for compatibility. OR-Tools already returns optimised tours."""

        if tour is not None:
            tour.setdefault("algorithm", "2opt")
        return tour

    @staticmethod
    def get_markets_from_graph(graph: Dict) -> List[str]:
        markets = []
        for symbol, data in (graph or {}).get("waypoints", {}).items():
            if "MARKETPLACE" in data.get("traits", []):
                markets.append(symbol)
        return markets


__all__ = [
    "euclidean_distance",
    "parse_waypoint_symbol",
    "TimeCalculator",
    "FuelCalculator",
    "GraphBuilder",
    "RouteOptimizer",
    "TourOptimizer",
]
