"""Graph building adapter - constructs navigation graphs from API data"""
import logging
import math
from typing import Callable, Dict, List

from domain.shared.value_objects import Waypoint
from ports.outbound.api_client import ISpaceTradersAPI
from ports.outbound.graph_provider import IGraphBuilder
from ports.outbound.repositories import IWaypointRepository

logger = logging.getLogger(__name__)


def euclidean_distance(x1: float, y1: float, x2: float, y2: float) -> float:
    """Calculate Euclidean distance between two coordinates"""
    return math.hypot(x2 - x1, y2 - y1)


class GraphBuilder(IGraphBuilder):
    """
    Builds system navigation graphs with dual-cache strategy.

    Populates:
    1. Return value: Structure data only (x, y, type, orbitals) for navigation
    2. waypoints table: Full trait data (has_fuel, traits) via waypoint repository - 2hr TTL
    """

    def __init__(
        self,
        api_client_factory: Callable[[int], ISpaceTradersAPI],
        waypoint_repository_factory: Callable[[int], IWaypointRepository],
    ):
        """
        Initialize GraphBuilder with dependency factories.

        Args:
            api_client_factory: Factory function that creates API client for a player_id
            waypoint_repository_factory: Factory function that creates waypoint repository for a player_id
        """
        self._api_client_factory = api_client_factory
        self._waypoint_repository_factory = waypoint_repository_factory

    def build_system_graph(self, system_symbol: str, player_id: int = 1) -> Dict:
        """
        Fetch all waypoints for system and build navigation graph with dual-cache strategy.

        Populates both:
        1. Return value: Structure-only graph for navigation (infinite TTL)
        2. Waypoints table: Full trait data for queries (2hr TTL)

        Args:
            system_symbol: System to build graph for
            player_id: Player ID for API client and waypoint repository

        Returns:
            Graph dict with ONLY structure data: {waypoints: {symbol: {x, y, type, systemSymbol, orbitals}}, edges: [...]}
        """
        logger.info(f"Building graph for system {system_symbol}...")

        # Get API client for this player
        api_client = self._api_client_factory(player_id)

        # Fetch all waypoints with pagination
        all_waypoints: List[Dict] = []
        page = 1
        limit = 20

        while True:
            try:
                result = api_client.list_waypoints(system_symbol, limit=limit, page=page)
            except Exception as e:
                logger.error(f"Failed to fetch waypoints page {page}: {e}")
                raise RuntimeError(f"API error while building graph for {system_symbol}") from e

            if not result or "data" not in result:
                break

            waypoints_page = result["data"]
            all_waypoints.extend(waypoints_page)

            logger.info(f"  Fetched page {page}: {len(waypoints_page)} waypoints")

            # Check if we have more pages
            meta = result.get("meta", {})
            total = meta.get("total", 0)
            total_pages = (total // limit) + (1 if total % limit > 0 else 0)

            if page >= total_pages or len(waypoints_page) < limit:
                break

            page += 1

            # Safety limit
            if page > 50:
                logger.warning("Reached safety limit of 50 pages")
                break

        if not all_waypoints:
            logger.error(f"No waypoints found for system {system_symbol}")
            raise RuntimeError(f"No waypoints found for system {system_symbol}")

        # Build STRUCTURE-ONLY graph for navigation (infinite TTL)
        graph = {
            "system": system_symbol,
            "waypoints": {},
            "edges": [],
        }

        # Prepare waypoint objects for trait cache (2hr TTL)
        waypoint_objects: List[Waypoint] = []

        # Process waypoints with dual-cache strategy
        for waypoint in all_waypoints:
            symbol = waypoint["symbol"]
            x = waypoint["x"]
            y = waypoint["y"]
            wp_type = waypoint.get("type")
            orbitals = [o["symbol"] for o in waypoint.get("orbitals", [])]

            # Extract traits
            traits = [t["symbol"] if isinstance(t, dict) else t for t in waypoint.get("traits", [])]
            has_fuel = "MARKETPLACE" in traits or "FUEL_STATION" in traits

            # 1. STRUCTURE-ONLY data for navigation graph (no traits, no has_fuel)
            graph["waypoints"][symbol] = {
                "type": wp_type,
                "x": x,
                "y": y,
                "systemSymbol": system_symbol,
                "orbitals": orbitals,
                # NO traits or has_fuel - structure only for routing
            }

            # 2. FULL waypoint object with traits for waypoints table
            waypoint_obj = Waypoint(
                symbol=symbol,
                x=x,
                y=y,
                system_symbol=system_symbol,
                waypoint_type=wp_type,
                traits=tuple(traits),
                has_fuel=has_fuel,
                orbitals=tuple(orbitals),
            )
            waypoint_objects.append(waypoint_obj)

        # Build edges (bidirectional graph)
        waypoint_list = list(graph["waypoints"].keys())

        for i, wp1 in enumerate(waypoint_list):
            wp1_data = graph["waypoints"][wp1]

            # Only create edges with waypoints that come after this one (avoid duplicates)
            for wp2 in waypoint_list[i + 1:]:
                wp2_data = graph["waypoints"][wp2]

                # Check if this is an orbital relationship (zero distance)
                is_orbital = (
                    wp2 in wp1_data.get("orbitals", []) or
                    wp1 in wp2_data.get("orbitals", [])
                )

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

                # Add bidirectional edges
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

        # Save waypoints with traits to waypoints table (2hr TTL)
        waypoint_repository = self._waypoint_repository_factory(player_id)
        waypoint_repository.save_waypoints(waypoint_objects)

        logger.info(f"Graph built for {system_symbol}")
        logger.info(f"  Waypoints: {len(graph['waypoints'])}")
        logger.info(f"  Edges: {len(graph['edges'])}")
        logger.info(f"  Synced {len(waypoint_objects)} waypoints to waypoints table")
        logger.info(
            f"  Fuel stations: {sum(1 for wp in waypoint_objects if wp.has_fuel)}"
        )

        return graph
