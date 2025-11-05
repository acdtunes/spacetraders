"""Dock ship command and handler"""
from dataclasses import dataclass
import asyncio
import logging
from pymediatr import Request, RequestHandler

from domain.shared.ship import Ship, InvalidNavStatusError
from domain.shared.exceptions import ShipNotFoundError, DomainException
from ports.repositories import IShipRepository
from ports.outbound.api_client import ISpaceTradersAPI
from ._ship_converter import convert_api_ship_to_entity
from .navigate_ship import calculate_arrival_wait_time

logger = logging.getLogger(__name__)


@dataclass(frozen=True)
class DockShipCommand(Request[Ship]):
    """
    Command to dock a ship at its current location.

    Docking allows the ship to:
    - Access marketplace for trading
    - Refuel
    - Repair
    - Install modules

    The ship must be in orbit to dock.
    """
    ship_symbol: str
    player_id: int


class DockShipHandler(RequestHandler[DockShipCommand, Ship]):
    """
    Handler for ship docking operations.

    Responsibilities:
    - Load ship from repository
    - Verify ship is in orbit
    - Call API to dock ship
    - Update ship's navigation status to DOCKED
    - Persist ship state
    - Return updated ship
    """

    def __init__(
        self,
        ship_repository: IShipRepository
    ):
        """
        Initialize DockShipHandler.

        Args:
            ship_repository: Repository for ship persistence
        """
        self._ship_repo = ship_repository

    async def handle(self, request: DockShipCommand) -> Ship:
        """
        Execute ship docking command.

        Process:
        1. Get API client for player
        2. Load ship from repository
        3. Verify ship is in orbit (domain rule)
        4. Call API dock_ship()
        5. Update ship state to DOCKED
        6. Persist ship
        7. Return updated ship

        Args:
            request: Dock command with ship symbol and player ID

        Returns:
            Updated Ship entity in DOCKED state

        Raises:
            ShipNotFoundError: If ship doesn't exist
            InvalidNavStatusError: If ship is not in orbit
        """
        # 1. Get API client for this player (reads token from database)
        from configuration.container import get_api_client_for_player
        api_client = get_api_client_for_player(request.player_id)

        # 2. Load ship from repository
        ship = self._ship_repo.find_by_symbol(request.ship_symbol, request.player_id)
        if ship is None:
            raise ShipNotFoundError(
                f"Ship '{request.ship_symbol}' not found for player {request.player_id}"
            )

        # 3. Handle state transitions (state machine)
        # If ship is IN_TRANSIT, wait for arrival before docking
        if ship.nav_status == Ship.IN_TRANSIT:
            # Get full ship data from API to access arrival time
            ship_response = api_client.get_ship(request.ship_symbol)
            ship_data = ship_response.get('data')
            if not ship_data:
                raise DomainException("Failed to fetch ship state")

            # Extract arrival information
            arrival_time_str = ship_data['nav']['route']['arrival']
            destination = ship_data['nav']['route']['destination']['symbol']
            wait_time = calculate_arrival_wait_time(arrival_time_str)

            logger.info(
                f"State transition: IN_TRANSIT → DOCKED (waiting {wait_time}s for arrival at {destination})"
            )

            # Wait for arrival (add 3s safety margin)
            await asyncio.sleep(wait_time + 3)

            # Re-fetch ship state after arrival
            ship_response = api_client.get_ship(request.ship_symbol)
            ship_data = ship_response.get('data')
            if not ship_data:
                raise DomainException("Failed to fetch ship state after arrival")

            # Update ship entity from API
            ship = convert_api_ship_to_entity(
                ship_data,
                request.player_id,
                ship.current_location
            )

            logger.info(f"✅ Arrived at {destination}")

        # Idempotent check: if already docked, just return
        if ship.nav_status == Ship.DOCKED:
            logger.info(f"Ship {request.ship_symbol} already docked (idempotent)")
            return ship

        # 4. Verify ship is in orbit (domain validation)
        # This will raise InvalidNavStatusError if ship is not in orbit
        ship.ensure_in_orbit()

        # 5. Call API to dock ship
        api_client.dock_ship(request.ship_symbol)

        # 6. Auto-sync: Fetch full ship state after dock
        # Dock endpoint returns {data: {nav: {...}}} not full ship object
        # So we need to fetch the complete ship state
        ship_response = api_client.get_ship(request.ship_symbol)
        ship_data = ship_response.get('data')
        if not ship_data:
            raise DomainException("Failed to fetch ship state after dock")

        # 7. Convert API response to Ship entity
        # Reuse existing waypoint since docking doesn't change location
        ship = convert_api_ship_to_entity(
            ship_data,
            request.player_id,
            ship.current_location
        )

        # 8. Persist with from_api=True to update synced_at timestamp
        self._ship_repo.update(ship, from_api=True)

        # 9. Return updated ship
        return ship
