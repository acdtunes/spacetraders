from dataclasses import dataclass
from pymediatr import Request, RequestHandler

from domain.shared.value_objects import Waypoint
from domain.shared.exceptions import ShipNotFoundError
from ports.outbound.repositories import IShipRepository


@dataclass(frozen=True)
class GetShipLocationQuery(Request[Waypoint]):
    """
    Query to get a ship's current location

    Returns the ship's current Waypoint position
    """
    ship_symbol: str
    player_id: int


class GetShipLocationHandler(RequestHandler[GetShipLocationQuery, Waypoint]):
    """
    Handler for retrieving ship location

    Simple read-only query that returns ship's current waypoint
    """

    def __init__(self, ship_repository: IShipRepository):
        self._ship_repo = ship_repository

    async def handle(self, request: GetShipLocationQuery) -> Waypoint:
        """
        Get ship's current location

        Args:
            request: Ship location query

        Returns:
            Waypoint where ship is currently located

        Raises:
            ShipNotFoundError: If ship doesn't exist
        """
        ship = self._ship_repo.find_by_symbol(request.ship_symbol, request.player_id)
        if not ship:
            raise ShipNotFoundError(
                f"Ship {request.ship_symbol} not found for player {request.player_id}"
            )

        return ship.current_location
