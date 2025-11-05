"""Market repository implementation"""
from typing import List, Optional
from ports.outbound.market_repository import IMarketRepository
from domain.shared.market import Market, TradeGood
from .database import Database


class MarketRepository(IMarketRepository):
    """Repository for market data persistence"""

    def __init__(self, database: Database):
        """
        Initialize market repository.

        Args:
            database: Database instance
        """
        self._db = database

    def upsert_market_data(
        self,
        waypoint: str,
        goods: List[TradeGood],
        timestamp: str,
        player_id: int
    ) -> int:
        """
        Insert or update all trade goods for a waypoint atomically.

        Args:
            waypoint: Waypoint symbol
            goods: List of TradeGood value objects
            timestamp: ISO timestamp
            player_id: Player ID

        Returns:
            Number of goods updated
        """
        goods_updated = 0

        with self._db.transaction() as conn:
            for good in goods:
                self._db.update_market_data(
                    conn,
                    waypoint_symbol=waypoint,
                    good_symbol=good.symbol,
                    supply=good.supply,
                    activity=good.activity,
                    purchase_price=good.purchase_price,
                    sell_price=good.sell_price,
                    trade_volume=good.trade_volume,
                    last_updated=timestamp,
                    player_id=player_id
                )
                goods_updated += 1

        return goods_updated

    def get_market_data(self, waypoint: str, player_id: int) -> Optional[Market]:
        """
        Get latest market snapshot for a waypoint.

        Args:
            waypoint: Waypoint symbol
            player_id: Player ID

        Returns:
            Market value object or None if not found
        """
        goods_data = self._db.get_market_data(player_id, waypoint)

        if not goods_data:
            return None

        # Map database rows to TradeGood value objects
        trade_goods = tuple(
            TradeGood(
                symbol=row['good_symbol'],
                supply=row['supply'],
                activity=row['activity'],
                purchase_price=row['purchase_price'],
                sell_price=row['sell_price'],
                trade_volume=row['trade_volume']
            )
            for row in goods_data
        )

        # Use last_updated from first good (all should have same timestamp for a waypoint)
        last_updated = goods_data[0]['last_updated'] if goods_data else ""

        return Market(
            waypoint_symbol=waypoint,
            trade_goods=trade_goods,
            last_updated=last_updated
        )

    def list_markets_in_system(
        self,
        system: str,
        player_id: int,
        max_age_minutes: Optional[int] = None
    ) -> List[Market]:
        """
        List all markets in a system with optional freshness filter.

        Args:
            system: System symbol (e.g., "X1-GZ7")
            player_id: Player ID
            max_age_minutes: Optional maximum age in minutes

        Returns:
            List of Market value objects
        """
        waypoints = self._db.list_markets_in_system(
            player_id,
            system,
            max_age_minutes
        )

        markets = []
        for waypoint in waypoints:
            market = self.get_market_data(waypoint, player_id)
            if market:
                markets.append(market)

        return markets
