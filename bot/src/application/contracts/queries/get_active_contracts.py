"""Get active contracts query"""
from dataclasses import dataclass
from typing import List

from pymediatr import Request, RequestHandler

from domain.shared.contract import Contract
from ports.outbound.repositories import IContractRepository


@dataclass(frozen=True)
class GetActiveContractsQuery(Request[List[Contract]]):
    """Query to get active (accepted but not fulfilled) contracts"""
    player_id: int


class GetActiveContractsHandler(RequestHandler[GetActiveContractsQuery, List[Contract]]):
    """Handler for GetActiveContractsQuery"""

    def __init__(self, contract_repository: IContractRepository):
        self._contract_repo = contract_repository

    async def handle(self, request: GetActiveContractsQuery) -> List[Contract]:
        """
        Handle get active contracts query

        Args:
            request: Query with player ID

        Returns:
            List of active contracts (accepted but not fulfilled)
        """
        return self._contract_repo.find_active(request.player_id)
