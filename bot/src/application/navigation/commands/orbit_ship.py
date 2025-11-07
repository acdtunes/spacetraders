"""Orbit ship command and handler"""
from dataclasses import dataclass
import asyncio
import logging
from datetime import datetime, timezone
from pymediatr import Request, RequestHandler

from domain.shared.ship import Ship, InvalidNavStatusError
from domain.shared.exceptions import ShipNotFoundError, DomainException
from ports.repositories import IShipRepository
from ports.outbound.api_client import ISpaceTradersAPI
from ._ship_converter import convert_api_ship_to_entity
from .navigate_ship import calculate_arrival_wait_time

logger = logging.getLogger(__name__)


@dataclass(frozen=True)
class OrbitShipCommand(Request[Ship]):
    """
    Command to put a ship into orbit at its current location.

    Orbiting allows the ship to:
    - Navigate to other waypoints
    - Scan for resources
    - Engage in combat

    The ship must be docked to orbit (depart).
    """
    ship_symbol: str
    player_id: int


class OrbitShipHandler(RequestHandler[OrbitShipCommand, Ship]):
    """
    Handler for ship orbit operations.

    Responsibilities:
    - Load ship from repository
    - Verify ship is docked
    - Call API to orbit ship
    - Update ship's navigation status to IN_ORBIT
    - Persist ship state
    - Return updated ship
    """

    def __init__(
        self,
        ship_repository: IShipRepository
    ):
        """
        Initialize OrbitShipHandler.

        Args:
            ship_repository: Repository for ship persistence
        """
        self._ship_repo = ship_repository

    async def _sync_ship(self, ship_symbol: str, player_id: int) -> Ship:
        """
        Sync ship state from API to get latest status.

        Args:
            ship_symbol: Ship's unique identifier
            player_id: Owning player's ID

        Returns:
            Ship entity with fresh state from API

        Raises:
            ShipNotFoundError: If ship doesn't exist in repository
        """
        # Get current ship from repository first
        existing_ship = self._ship_repo.find_by_symbol(ship_symbol, player_id)
        if not existing_ship:
            raise ShipNotFoundError(
                f"Ship '{ship_symbol}' not found for player {player_id}"
            )

        from configuration.container import get_api_client_for_player

        api_client = get_api_client_for_player(player_id)

        # Fetch ship from API
        ship_response = api_client.get_ship(ship_symbol)
        ship_data = ship_response.get('data')

        if not ship_data:
            raise DomainException(f"Failed to fetch ship {ship_symbol} from API")

        # Convert API response to Ship entity
        ship = convert_api_ship_to_entity(
            ship_data,
            player_id,
            existing_ship.current_location
        )

        # Ship state is API-only now - no database updates needed

        return ship

    async def handle(self, request: OrbitShipCommand) -> Ship:
        """
        Execute ship orbit command with eventual consistency.

        This is an **eventual** command - it expresses the intention to orbit
        the ship, and will wait for the ship to be in a valid state if needed.

        Process:
        1. Sync ship state from API to get latest status
        2. If already in orbit - return (idempotent)
        3. If in transit - wait for arrival (ship arrives in orbit)
        4. If docked - orbit immediately
        5. Call API orbit_ship()
        6. Sync ship state after orbiting
        7. Return updated ship

        Args:
            request: Orbit command with ship symbol and player ID

        Returns:
            Updated Ship entity in IN_ORBIT state

        Raises:
            ShipNotFoundError: If ship doesn't exist
        """
        # 1. Sync ship from API to get current state
        ship = await self._sync_ship(request.ship_symbol, request.player_id)

        # 2. Idempotent check: if already in orbit, just return
        if ship.nav_status == Ship.IN_ORBIT:
            logger.info(f"Ship {request.ship_symbol} already in orbit (idempotent)")
            return ship

        # 3. Eventual consistency: If ship is in transit, wait for arrival
        # When ships arrive from transit, they arrive IN_ORBIT
        if ship.nav_status == Ship.IN_TRANSIT:
            from configuration.container import get_api_client_for_player
            api_client = get_api_client_for_player(request.player_id)

            # Fetch ship data to get arrival time
            ship_response = api_client.get_ship(request.ship_symbol)
            ship_data = ship_response.get('data', {})
            nav_data = ship_data.get('nav', {})
            route_data = nav_data.get('route', {})
            arrival_str = route_data.get('arrival')

            if arrival_str:
                wait_seconds = calculate_arrival_wait_time(arrival_str)
                # Always wait at least until arrival, with 1s buffer for clock precision
                actual_wait = max(wait_seconds + 1, 0.1)
                logger.info(
                    f"Ship {request.ship_symbol} in transit, waiting {actual_wait}s for arrival"
                )
                await asyncio.sleep(actual_wait)

            # Sync again after waiting - ship should now be in orbit
            ship = await self._sync_ship(request.ship_symbol, request.player_id)

            # Ships arrive in orbit from transit, so if we waited, we should be done
            if ship.nav_status == Ship.IN_ORBIT:
                logger.info(f"Ship {request.ship_symbol} arrived in orbit")
                return ship

        # 4. If ship is docked, orbit it
        if ship.nav_status == Ship.DOCKED:
            from configuration.container import get_api_client_for_player
            api_client = get_api_client_for_player(request.player_id)

            api_client.orbit_ship(request.ship_symbol)

            # Sync to get updated state after orbiting
            ship = await self._sync_ship(request.ship_symbol, request.player_id)

            logger.info(f"Ship {request.ship_symbol} successfully orbited")
            return ship

        # Should not reach here
        raise InvalidNavStatusError(
            f"Ship {request.ship_symbol} in unexpected state {ship.nav_status}"
        )
