"""
List Waypoints Query

Read-only query to retrieve waypoints with lazy-loading and TTL-based caching.
Repository handles lazy-loading transparently when player_id is provided.
Supports filtering by trait and fuel availability.
"""
from dataclasses import dataclass
from typing import List, Optional
from pymediatr import Request, RequestHandler

from domain.shared.value_objects import Waypoint
from ports.outbound.repositories import IWaypointRepository


@dataclass(frozen=True)
class ListWaypointsQuery(Request[List[Waypoint]]):
    """
    Query to list waypoints in a system with lazy-loading

    Automatically fetches from API if cache is empty or stale (> 2 hours).
    Supports optional filtering by trait and fuel availability.

    Args:
        system_symbol: System identifier (e.g., "X1-GZ7")
        trait_filter: Optional trait to filter by (e.g., "MARKETPLACE", "SHIPYARD")
        has_fuel: Optional flag to filter waypoints with fuel stations only
        player_id: Optional player ID for API authentication when lazy-loading
    """
    system_symbol: str
    trait_filter: Optional[str] = None
    has_fuel: Optional[bool] = None
    player_id: Optional[int] = None


class ListWaypointsHandler(RequestHandler[ListWaypointsQuery, List[Waypoint]]):
    """
    Handler for listing waypoints with lazy-loading and TTL-based caching

    Thin application service that delegates to repository.
    Repository handles lazy-loading transparently when player_id is provided.
    """

    def __init__(self, waypoint_repository: IWaypointRepository):
        """
        Initialize handler.

        Args:
            waypoint_repository: Repository for waypoint persistence and lazy-loading
        """
        self._waypoint_repo = waypoint_repository

    async def handle(self, request: ListWaypointsQuery) -> List[Waypoint]:
        """
        List waypoints - repository handles lazy-loading transparently

        Args:
            request: List waypoints query with optional filters

        Returns:
            List of Waypoint value objects
        """
        # Repository handles lazy-loading automatically when player_id provided
        if request.has_fuel:
            return self._waypoint_repo.find_by_fuel(
                request.system_symbol,
                player_id=request.player_id
            )

        if request.trait_filter:
            return self._waypoint_repo.find_by_trait(
                request.system_symbol,
                request.trait_filter,
                player_id=request.player_id
            )

        # No filters - return all waypoints
        return self._waypoint_repo.find_by_system(
            request.system_symbol,
            player_id=request.player_id
        )
