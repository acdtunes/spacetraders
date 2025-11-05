"""List all contracts query"""
from dataclasses import dataclass
from typing import List

from pymediatr import Request, RequestHandler

from domain.shared.contract import Contract
from ports.outbound.repositories import IContractRepository


@dataclass(frozen=True)
class ListContractsQuery(Request[List[Contract]]):
    """Query to list all contracts for a player"""
    player_id: int


class ListContractsHandler(RequestHandler[ListContractsQuery, List[Contract]]):
    """Handler for ListContractsQuery"""

    def __init__(self, contract_repository: IContractRepository):
        self._contract_repo = contract_repository

    async def handle(self, request: ListContractsQuery) -> List[Contract]:
        """
        Handle list contracts query

        Args:
            request: Query with player ID

        Returns:
            List of all contracts for the player
        """
        return self._contract_repo.find_all(request.player_id)
