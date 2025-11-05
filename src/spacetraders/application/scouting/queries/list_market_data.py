"""List market data query"""
from dataclasses import dataclass
from typing import List, Optional
from pymediatr import Request, RequestHandler

from ....domain.shared.market import Market
from ....ports.outbound.market_repository import IMarketRepository


@dataclass(frozen=True)
class ListMarketDataQuery(Request[List[Market]]):
    """Query to list all markets in a system"""
    system: str
    player_id: int
    max_age_minutes: Optional[int] = None


class ListMarketDataHandler(RequestHandler[ListMarketDataQuery, List[Market]]):
    """Handler for listing market data"""

    def __init__(self, market_repo: IMarketRepository):
        """
        Initialize handler.

        Args:
            market_repo: Market repository instance
        """
        self._market_repo = market_repo

    async def handle(self, request: ListMarketDataQuery) -> List[Market]:
        """
        List all markets in system.

        Args:
            request: Query request

        Returns:
            List of Market value objects
        """
        return self._market_repo.list_markets_in_system(
            request.system,
            request.player_id,
            request.max_age_minutes
        )
