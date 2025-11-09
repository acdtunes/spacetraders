"""SQLAlchemy-based MarketRepository implementation."""

from typing import List, Optional
from datetime import datetime, timedelta, timezone
from sqlalchemy import select, func
from sqlalchemy.engine import Engine
from sqlalchemy.dialects.sqlite import insert as sqlite_insert
from sqlalchemy.dialects.postgresql import insert as pg_insert

from ports.outbound.market_repository import IMarketRepository
from domain.shared.market import Market, TradeGood
from .models import market_data


class MarketRepositorySQLAlchemy(IMarketRepository):
    """Repository for market data persistence using SQLAlchemy"""

    def __init__(self, engine: Engine):
        """
        Initialize market repository.

        Args:
            engine: SQLAlchemy Engine instance
        """
        self._engine = engine

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

        with self._engine.begin() as conn:
            # Detect backend
            backend = conn.engine.dialect.name

            for good in goods:
                # Use dialect-specific UPSERT
                if backend == 'postgresql':
                    stmt = pg_insert(market_data).values(
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
                    stmt = stmt.on_conflict_do_update(
                        index_elements=['waypoint_symbol', 'good_symbol'],
                        set_={
                            'supply': stmt.excluded.supply,
                            'activity': stmt.excluded.activity,
                            'purchase_price': stmt.excluded.purchase_price,
                            'sell_price': stmt.excluded.sell_price,
                            'trade_volume': stmt.excluded.trade_volume,
                            'last_updated': stmt.excluded.last_updated,
                            'player_id': stmt.excluded.player_id
                        }
                    )
                else:
                    stmt = sqlite_insert(market_data).values(
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
                    stmt = stmt.on_conflict_do_update(
                        index_elements=['waypoint_symbol', 'good_symbol'],
                        set_={
                            'supply': stmt.excluded.supply,
                            'activity': stmt.excluded.activity,
                            'purchase_price': stmt.excluded.purchase_price,
                            'sell_price': stmt.excluded.sell_price,
                            'trade_volume': stmt.excluded.trade_volume,
                            'last_updated': stmt.excluded.last_updated,
                            'player_id': stmt.excluded.player_id
                        }
                    )

                conn.execute(stmt)
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
        with self._engine.connect() as conn:
            stmt = (
                select(
                    market_data.c.waypoint_symbol,
                    market_data.c.good_symbol,
                    market_data.c.supply,
                    market_data.c.activity,
                    market_data.c.purchase_price,
                    market_data.c.sell_price,
                    market_data.c.trade_volume,
                    market_data.c.last_updated
                )
                .where(
                    market_data.c.player_id == player_id,
                    market_data.c.waypoint_symbol == waypoint
                )
                .order_by(market_data.c.good_symbol)
            )

            result = conn.execute(stmt)
            rows = result.fetchall()

            if not rows:
                return None

            # Map database rows to TradeGood value objects
            trade_goods = tuple(
                TradeGood(
                    symbol=row.good_symbol,
                    supply=row.supply,
                    activity=row.activity,
                    purchase_price=row.purchase_price,
                    sell_price=row.sell_price,
                    trade_volume=row.trade_volume
                )
                for row in rows
            )

            # Use last_updated from first good (all should have same timestamp for a waypoint)
            last_updated = str(rows[0].last_updated) if rows else ""

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
        with self._engine.connect() as conn:
            # Build query for waypoint symbols
            stmt = (
                select(
                    market_data.c.waypoint_symbol,
                    func.max(market_data.c.last_updated).label('latest_update')
                )
                .where(
                    market_data.c.player_id == player_id,
                    market_data.c.waypoint_symbol.like(f"{system}-%")
                )
                .group_by(market_data.c.waypoint_symbol)
            )

            # Add time filter if specified
            if max_age_minutes:
                cutoff = datetime.now(timezone.utc) - timedelta(minutes=max_age_minutes)
                cutoff_str = cutoff.isoformat()
                stmt = stmt.where(market_data.c.last_updated >= cutoff_str)

            stmt = stmt.order_by(market_data.c.waypoint_symbol)

            result = conn.execute(stmt)
            waypoints = [row.waypoint_symbol for row in result.fetchall()]

        # Get full market data for each waypoint
        markets = []
        for waypoint in waypoints:
            market = self.get_market_data(waypoint, player_id)
            if market:
                markets.append(market)

        return markets
