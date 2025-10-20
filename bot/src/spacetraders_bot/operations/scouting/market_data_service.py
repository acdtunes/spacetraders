"""
Market Data Service

Handles market data collection and database updates.
Eliminates duplicate code and centralizes API quirk handling.
"""

from typing import Dict, List, Optional
import logging


class MarketDataService:
    """Service for collecting and persisting market data"""

    def __init__(self, api, db, player_id: int):
        """
        Initialize market data service

        Args:
            api: SpaceTraders API client
            db: Database connection
            player_id: Player ID for data attribution
        """
        self.api = api
        self.db = db
        self.player_id = player_id
        self.logger = logging.getLogger(__name__)

    def collect_market_data(self, waypoint: str, system: str) -> Optional[Dict]:
        """
        Fetch market data from API

        Args:
            waypoint: Waypoint symbol (e.g., "X1-HU87-B7")
            system: System symbol (e.g., "X1-HU87")

        Returns:
            Market data dict with 'tradeGoods' list, or None if failed
        """
        try:
            market = self.api.get_market(system, waypoint)
            if not market:
                self.logger.warning(f"Failed to get market data for {waypoint}")
                return None

            return market
        except Exception as e:
            self.logger.error(f"Error fetching market data for {waypoint}: {e}")
            return None

    def update_database(self, waypoint: str, trade_goods: List[Dict], timestamp: str) -> int:
        """
        Update database with trade goods data

        CRITICAL: API field names are counter-intuitive!
        - API purchasePrice = ship PAYS to BUY (high) → DB sell_price (market asks)
        - API sellPrice = ship RECEIVES to SELL (low) → DB purchase_price (market bids)

        Args:
            waypoint: Waypoint symbol
            trade_goods: List of trade good dicts from API
            timestamp: ISO timestamp string

        Returns:
            Number of goods updated
        """
        goods_updated = 0

        try:
            with self.db.transaction() as db_conn:
                for good in trade_goods:
                    self.db.update_market_data(
                        db_conn,
                        waypoint_symbol=waypoint,
                        good_symbol=good['symbol'],
                        supply=good.get('supply'),
                        activity=good.get('activity'),
                        purchase_price=good.get('sellPrice', 0),      # API sellPrice → DB purchase_price
                        sell_price=good.get('purchasePrice', 0),      # API purchasePrice → DB sell_price
                        trade_volume=good.get('tradeVolume', 0),
                        last_updated=timestamp,
                        player_id=self.player_id
                    )
                    goods_updated += 1

            self.logger.debug(f"Updated {goods_updated} goods for {waypoint}")
            return goods_updated

        except Exception as e:
            self.logger.error(f"Error updating database for {waypoint}: {e}")
            return 0

    def collect_and_update(self, waypoint: str, system: str, timestamp: str) -> int:
        """
        Collect market data and update database in one call

        Args:
            waypoint: Waypoint symbol
            system: System symbol
            timestamp: ISO timestamp string

        Returns:
            Number of goods updated (0 if failed)
        """
        market = self.collect_market_data(waypoint, system)
        if not market:
            return 0

        trade_goods = market.get('tradeGoods', [])
        if not trade_goods:
            self.logger.warning(f"No trade goods found at {waypoint}")
            return 0

        return self.update_database(waypoint, trade_goods, timestamp)
