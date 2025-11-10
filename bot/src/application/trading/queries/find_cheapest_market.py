"""Find cheapest market query"""
from dataclasses import dataclass
from typing import Optional

from pymediatr import Request, RequestHandler
from ports.outbound.market_repository import IMarketRepository


@dataclass(frozen=True)
class CheapestMarketResult:
    """Result from finding cheapest market"""
    waypoint_symbol: str
    trade_symbol: str
    sell_price: int
    supply: Optional[str] = None


@dataclass(frozen=True)
class FindCheapestMarketQuery(Request[Optional[CheapestMarketResult]]):
    """Query to find cheapest market selling a specific good"""
    trade_symbol: str
    system: str
    player_id: int


class FindCheapestMarketHandler(RequestHandler[FindCheapestMarketQuery, Optional[CheapestMarketResult]]):
    """Handler for FindCheapestMarketQuery"""

    def __init__(self, market_repository: IMarketRepository):
        """
        Initialize handler

        Args:
            market_repository: Market repository for market data queries
        """
        self._market_repo = market_repository

    async def handle(self, request: FindCheapestMarketQuery) -> Optional[CheapestMarketResult]:
        """
        Handle find cheapest market query

        Args:
            request: Query with trade symbol, system, and player ID

        Returns:
            CheapestMarketResult if found, None otherwise
        """
        # Query market repository for cheapest market
        result = self._market_repo.find_cheapest_market_selling(
            good_symbol=request.trade_symbol,
            system=request.system,
            player_id=request.player_id
        )

        if not result:
            return None

        # Map repository result to domain result
        return CheapestMarketResult(
            waypoint_symbol=result['waypoint_symbol'],
            trade_symbol=result['good_symbol'],
            sell_price=result['sell_price'],
            supply=result.get('supply')
        )
