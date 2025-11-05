"""Sync system waypoints command and handler"""
from dataclasses import dataclass
from pymediatr import Request, RequestHandler
import logging

from ports.repositories import IWaypointRepository
from domain.shared.value_objects import Waypoint

logger = logging.getLogger(__name__)


@dataclass(frozen=True)
class SyncSystemWaypointsCommand(Request[None]):
    """
    Command to sync all waypoints for a system from the SpaceTraders API to cache.

    This command fetches ALL waypoints in a system (handling pagination) and stores
    them in the waypoint cache for fast lookup during shipyard/market discovery.

    Args:
        system_symbol: System identifier (e.g., "X1-GZ7")
        player_id: Player ID (for API authentication)
    """
    system_symbol: str
    player_id: int


class SyncSystemWaypointsHandler(RequestHandler[SyncSystemWaypointsCommand, None]):
    """
    Handler for syncing system waypoints to cache.

    Responsibilities:
    - Fetch ALL waypoints from SpaceTraders API (handle pagination)
    - Convert API response to Waypoint value objects
    - Save waypoints to cache via WaypointRepository
    - Handle pagination (20 waypoints per page)
    """

    def __init__(self, waypoint_repository: IWaypointRepository):
        """
        Initialize SyncSystemWaypointsHandler.

        Args:
            waypoint_repository: Repository for waypoint caching
        """
        self._waypoint_repo = waypoint_repository

    async def handle(self, request: SyncSystemWaypointsCommand) -> None:
        """
        Execute waypoint sync command.

        Process:
        1. Get API client for player
        2. Paginate through ALL waypoints in system (20 per page)
        3. Convert API responses to Waypoint value objects
        4. Save to cache (upsert - updates existing)

        Args:
            request: Sync command with system symbol and player ID

        Returns:
            None (waypoints are cached as side effect)
        """
        # Import API client factory here to avoid circular dependency
        from configuration.container import get_api_client_for_player

        api_client = get_api_client_for_player(request.player_id)

        # Fetch ALL waypoints with pagination
        all_waypoints = []
        page = 1
        while True:
            logger.debug(f"Fetching waypoints page {page} for system {request.system_symbol}")
            waypoints_response = api_client.list_waypoints(
                request.system_symbol,
                page=page,
                limit=20
            )

            waypoints_data = waypoints_response.get('data', [])
            if not waypoints_data:
                # No more data - we've reached the end
                break

            all_waypoints.extend(waypoints_data)
            page += 1

        logger.info(
            f"Fetched {len(all_waypoints)} waypoints for system {request.system_symbol} "
            f"across {page - 1} pages"
        )

        # Convert API response to Waypoint value objects
        waypoint_entities = []
        for waypoint_data in all_waypoints:
            # Extract traits as tuple of trait symbols
            traits_list = waypoint_data.get('traits', [])
            traits = tuple(trait.get('symbol') for trait in traits_list)

            # Check if waypoint has fuel (if has MARKETPLACE trait)
            has_fuel = 'MARKETPLACE' in traits

            # Extract orbitals (if any)
            orbitals_list = waypoint_data.get('orbitals', [])
            orbitals = tuple(orbital.get('symbol') for orbital in orbitals_list) if orbitals_list else ()

            waypoint = Waypoint(
                symbol=waypoint_data.get('symbol'),
                x=waypoint_data.get('x'),
                y=waypoint_data.get('y'),
                system_symbol=request.system_symbol,
                waypoint_type=waypoint_data.get('type'),
                traits=traits,
                has_fuel=has_fuel,
                orbitals=orbitals
            )
            waypoint_entities.append(waypoint)

        # Save to cache (upsert)
        self._waypoint_repo.save_waypoints(waypoint_entities)

        logger.info(
            f"Cached {len(waypoint_entities)} waypoints for system {request.system_symbol}"
        )
