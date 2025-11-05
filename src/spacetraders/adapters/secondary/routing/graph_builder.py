"""Graph building adapter - constructs navigation graphs from API data"""
import logging
import math
from typing import Dict, List

from ....ports.outbound.api_client import ISpaceTradersAPI
from ....ports.outbound.graph_provider import IGraphBuilder

logger = logging.getLogger(__name__)


def euclidean_distance(x1: float, y1: float, x2: float, y2: float) -> float:
    """Calculate Euclidean distance between two coordinates"""
    return math.hypot(x2 - x1, y2 - y1)


class GraphBuilder(IGraphBuilder):
    """Builds system navigation graphs by fetching waypoints from the API"""

    def __init__(self, api_client: ISpaceTradersAPI):
        self.api = api_client

    def build_system_graph(self, system_symbol: str) -> Dict:
        """
        Fetch all waypoints for system and build navigation graph

        Returns:
            Graph dict: {waypoints: {symbol: {...}}, edges: [{from, to, distance, type}]}
        """
        logger.info(f"Building graph for system {system_symbol}...")

        # Fetch all waypoints with pagination
        all_waypoints: List[Dict] = []
        page = 1
        limit = 20

        while True:
            try:
                result = self.api.list_waypoints(system_symbol, limit=limit, page=page)
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

        # Build graph structure
        graph = {
            "system": system_symbol,
            "waypoints": {},
            "edges": [],
        }

        # Process waypoints
        for waypoint in all_waypoints:
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

        logger.info(f"Graph built for {system_symbol}")
        logger.info(f"  Waypoints: {len(graph['waypoints'])}")
        logger.info(f"  Edges: {len(graph['edges'])}")
        logger.info(
            f"  Fuel stations: {sum(1 for wp in graph['waypoints'].values() if wp['has_fuel'])}"
        )

        return graph
