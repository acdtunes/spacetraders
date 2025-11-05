"""Get market data query"""
from dataclasses import dataclass
from typing import Optional
from pymediatr import Request, RequestHandler

from ....domain.shared.market import Market
from ....ports.outbound.market_repository import IMarketRepository


@dataclass(frozen=True)
class GetMarketDataQuery(Request[Optional[Market]]):
    """Query to get market data for a specific waypoint"""
    waypoint_symbol: str
    player_id: int


class GetMarketDataHandler(RequestHandler[GetMarketDataQuery, Optional[Market]]):
    """Handler for getting market data"""

    def __init__(self, market_repo: IMarketRepository):
        """
        Initialize handler.

        Args:
            market_repo: Market repository instance
        """
        self._market_repo = market_repo

    async def handle(self, request: GetMarketDataQuery) -> Optional[Market]:
        """
        Get market data for waypoint.

        Args:
            request: Query request

        Returns:
            Market value object or None if not found
        """
        return self._market_repo.get_market_data(
            request.waypoint_symbol,
            request.player_id
        )
