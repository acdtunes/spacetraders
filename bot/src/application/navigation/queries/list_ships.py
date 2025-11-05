from dataclasses import dataclass
from typing import List
from pymediatr import Request, RequestHandler

from domain.shared.ship import Ship
from ports.outbound.repositories import IShipRepository


@dataclass(frozen=True)
class ListShipsQuery(Request[List[Ship]]):
    """
    Query to list all ships for a player

    Returns list of all ships belonging to the specified player
    """
    player_id: int


class ListShipsHandler(RequestHandler[ListShipsQuery, List[Ship]]):
    """
    Handler for listing player's ships

    Simple read-only query that returns all ships for a player
    """

    def __init__(self, ship_repository: IShipRepository):
        self._ship_repo = ship_repository

    async def handle(self, request: ListShipsQuery) -> List[Ship]:
        """
        List all ships for player

        Args:
            request: List ships query

        Returns:
            List of Ship entities (empty list if player has no ships)
        """
        return self._ship_repo.find_all_by_player(request.player_id)
