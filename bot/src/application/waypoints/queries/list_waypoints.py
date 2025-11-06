"""
List Waypoints Query

Read-only query to retrieve cached waypoints from the database.
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
    Query to list cached waypoints in a system

    This is a read-only query that retrieves waypoints from the local cache.
    No API calls are made. Supports optional filtering by trait and fuel availability.

    Args:
        system_symbol: System identifier (e.g., "X1-GZ7")
        trait_filter: Optional trait to filter by (e.g., "MARKETPLACE", "SHIPYARD")
        has_fuel: Optional flag to filter waypoints with fuel stations only
    """
    system_symbol: str
    trait_filter: Optional[str] = None
    has_fuel: Optional[bool] = None


class ListWaypointsHandler(RequestHandler[ListWaypointsQuery, List[Waypoint]]):
    """
    Handler for listing cached waypoints

    Thin orchestrator that delegates to WaypointRepository based on filters.
    Returns empty list if no waypoints are cached for the system.
    """

    def __init__(self, waypoint_repository: IWaypointRepository):
        self._waypoint_repo = waypoint_repository

    async def handle(self, request: ListWaypointsQuery) -> List[Waypoint]:
        """
        List waypoints from cache with optional filters

        Strategy:
        1. If has_fuel filter specified, query fuel waypoints
        2. Else if trait_filter specified, query by trait
        3. Else query all waypoints in system

        Args:
            request: List waypoints query

        Returns:
            List of Waypoint value objects (empty if none cached)
        """
        # Apply filters in priority order
        if request.has_fuel:
            return self._waypoint_repo.find_by_fuel(request.system_symbol)

        if request.trait_filter:
            return self._waypoint_repo.find_by_trait(
                request.system_symbol,
                request.trait_filter
            )

        # No filters - return all waypoints in system
        return self._waypoint_repo.find_by_system(request.system_symbol)
