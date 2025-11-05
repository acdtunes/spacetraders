"""Get contract by ID query"""
from dataclasses import dataclass
from typing import Optional

from pymediatr import Request, RequestHandler

from domain.shared.contract import Contract
from ports.outbound.repositories import IContractRepository


@dataclass(frozen=True)
class GetContractQuery(Request[Optional[Contract]]):
    """Query to get contract by ID"""
    contract_id: str
    player_id: int


class GetContractHandler(RequestHandler[GetContractQuery, Optional[Contract]]):
    """Handler for GetContractQuery"""

    def __init__(self, contract_repository: IContractRepository):
        self._contract_repo = contract_repository

    async def handle(self, request: GetContractQuery) -> Optional[Contract]:
        """
        Handle get contract query

        Args:
            request: Query with contract ID and player ID

        Returns:
            Contract if found, None otherwise
        """
        return self._contract_repo.find_by_id(
            request.contract_id,
            request.player_id
        )
