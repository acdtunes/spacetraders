"""Refuel ship command and handler"""
from dataclasses import dataclass
from typing import Optional
from pymediatr import Request, RequestHandler

from ....domain.shared.ship import Ship, InvalidNavStatusError
from ....domain.shared.exceptions import ShipNotFoundError, DomainException
from ....ports.repositories import IShipRepository
from ....ports.outbound.api_client import ISpaceTradersAPI
from ._ship_converter import convert_api_ship_to_entity


@dataclass(frozen=True)
class RefuelShipResponse:
    """
    Response for refuel ship operation.

    Contains updated ship state and refuel transaction details.
    """
    ship: Ship
    fuel_added: int
    cost: Optional[int] = None


@dataclass(frozen=True)
class RefuelShipCommand(Request[RefuelShipResponse]):
    """
    Command to refuel a ship at its current location.

    Refueling:
    - Requires ship to be docked
    - Current location must have a marketplace with fuel
    - Costs credits based on fuel units purchased
    - Fills ship to maximum fuel capacity

    The ship must be docked to refuel.
    """
    ship_symbol: str
    player_id: int
    units: Optional[int] = None  # If None, refuel to full


class RefuelShipHandler(RequestHandler[RefuelShipCommand, RefuelShipResponse]):
    """
    Handler for ship refueling operations.

    Responsibilities:
    - Load ship from repository
    - Verify ship is docked
    - Call API to refuel ship
    - Update ship's fuel state
    - Persist ship state
    - Return refuel response with ship and transaction details
    """

    def __init__(
        self,
        ship_repository: IShipRepository
    ):
        """
        Initialize RefuelShipHandler.

        Args:
            ship_repository: Repository for ship persistence
        """
        self._ship_repo = ship_repository

    async def handle(self, request: RefuelShipCommand) -> RefuelShipResponse:
        """
        Execute ship refuel command.

        Process:
        1. Get API client for player
        2. Load ship from repository
        3. Verify ship is docked (domain rule)
        4. Call API refuel_ship()
        5. Calculate fuel added
        6. Update ship fuel state
        7. Persist ship
        8. Return response with ship and transaction details

        Args:
            request: Refuel command with ship symbol and player ID

        Returns:
            RefuelShipResponse with updated ship and refuel details

        Raises:
            ShipNotFoundError: If ship doesn't exist
            InvalidNavStatusError: If ship is not docked
            ValueError: If location doesn't have marketplace or fuel unavailable
        """
        # 1. Get API client for this player (reads token from database)
        from ....configuration.container import get_api_client_for_player
        api_client = get_api_client_for_player(request.player_id)

        # 2. Load ship from repository
        ship = self._ship_repo.find_by_symbol(request.ship_symbol, request.player_id)
        if ship is None:
            raise ShipNotFoundError(
                f"Ship '{request.ship_symbol}' not found for player {request.player_id}"
            )

        # 3. Verify ship is docked (domain validation)
        # This will raise InvalidNavStatusError if ship is not docked
        ship.ensure_docked()

        # 4. Check if location has fuel for refueling
        if not ship.current_location.has_fuel:
            raise ValueError(
                f"Cannot refuel at {ship.current_location.symbol}: no fuel available"
            )

        # Calculate fuel before refuel for comparison
        fuel_before = ship.fuel.current

        # 5. Call API to refuel ship
        refuel_result = api_client.refuel_ship(request.ship_symbol)

        # 5. Auto-sync: Extract ship data from API response
        ship_data = refuel_result.get('data', {}).get('ship')
        if not ship_data:
            raise DomainException("API returned no ship data for refuel operation")

        # 6. Convert API response to Ship entity
        # Reuse existing waypoint since refueling doesn't change location
        ship = convert_api_ship_to_entity(
            ship_data,
            request.player_id,
            ship.current_location
        )

        # 7. Calculate fuel added from API data
        fuel_added = ship.fuel.current - fuel_before

        # 8. Extract cost from API result if available
        cost = None
        transaction = refuel_result.get('data', {}).get('transaction')
        if transaction:
            cost = transaction.get('totalPrice', None)

        # 9. Persist with from_api=True to update synced_at timestamp
        self._ship_repo.update(ship, from_api=True)

        # 10. Return response
        return RefuelShipResponse(
            ship=ship,
            fuel_added=fuel_added,
            cost=cost
        )
