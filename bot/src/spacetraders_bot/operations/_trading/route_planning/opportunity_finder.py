"""
Opportunity Finder

Single Responsibility: Query database for markets and profitable trade opportunities.

Provides database query service for discovering trading opportunities by analyzing
market data across multiple waypoints in a system.
"""

import logging
from typing import Dict, List, Optional

from spacetraders_bot.operations._trading.route_planning.market_validator import MarketValidator


class OpportunityFinder:
    """
    Database query service for trade opportunities

    Responsibilities:
    - Fetch all markets in system
    - Fetch all trade opportunities
    - Filter by profitability (spread > 0)
    - Sort by profit margin
    """

    def __init__(
        self,
        db,
        player_id: int,
        logger: Optional[logging.Logger] = None,
        market_validator: Optional[MarketValidator] = None
    ):
        """
        Initialize opportunity finder

        Args:
            db: Database instance
            player_id: Player ID for filtering player-specific market data
            logger: Optional logger (creates default if not provided)
            market_validator: Optional market validator (creates default if not provided)
        """
        self.db = db
        self.player_id = player_id
        self.logger = logger or logging.getLogger(__name__)
        self.market_validator = market_validator or MarketValidator(self.logger)

    def get_markets_in_system(self, system: str) -> List[str]:
        """
        Get all market waypoints in a system from database

        Args:
            system: System symbol (e.g., "X1-JB26")

        Returns:
            List of waypoint symbols with market data
        """
        with self.db.connection() as conn:
            cursor = conn.cursor()
            cursor.execute("""
                SELECT DISTINCT waypoint_symbol
                FROM market_data
                WHERE waypoint_symbol LIKE ?
                AND (updated_by_player = ? OR updated_by_player IS NULL)
            """, (f"{system}-%", self.player_id))

            return [row[0] for row in cursor.fetchall()]

    def get_trade_opportunities(self, system: str, markets: List[str]) -> List[Dict]:
        """
        Get all profitable trade opportunities from database

        Queries all market pairs and identifies profitable buy-sell opportunities.
        Filters by market data freshness and positive spread.

        Args:
            system: System symbol (e.g., "X1-JB26")
            markets: List of market waypoints to consider

        Returns:
            List of trade opportunity dicts with:
            - buy_waypoint, sell_waypoint
            - good
            - buy_price (what we pay), sell_price (what we receive)
            - spread (profit per unit)
            - trade_volume (transaction limit)

            Sorted by spread (most profitable first)
        """
        opportunities = []

        with self.db.connection() as conn:
            for buy_market in markets:
                buy_data = self.db.get_market_data(conn, buy_market, None)
                opportunities.extend(
                    self._collect_opportunities_for_market(
                        conn, buy_market, buy_data, markets
                    )
                )

        # Sort by spread (most profitable first)
        opportunities.sort(key=lambda x: x['spread'], reverse=True)

        return opportunities

    def _collect_opportunities_for_market(
        self,
        conn,
        buy_market: str,
        buy_data: List[Dict],
        markets: List[str],
    ) -> List[Dict]:
        """
        Collect trade opportunities for a specific buy market

        For each good available at the buy market, finds all potential
        sell markets with positive spread and fresh market data.

        Args:
            conn: Database connection
            buy_market: Buy market waypoint symbol
            buy_data: Buy market data records (list of dicts)
            markets: All markets to consider for selling

        Returns:
            List of trade opportunity dicts
        """
        opportunities = []

        for sell_market in markets:
            if sell_market == buy_market:
                continue

            for buy_record in buy_data:
                good = buy_record['good_symbol']
                buy_price = buy_record.get('sell_price')

                if not buy_price:
                    continue

                # Freshness check for buy market data
                if not self.market_validator.is_market_data_fresh(buy_record, buy_market, good, 'buy'):
                    continue

                sell_data = self.db.get_market_data(conn, sell_market, good)
                if not sell_data:
                    continue

                sell_record = sell_data[0]
                sell_price = sell_record.get('purchase_price')

                if not sell_price:
                    continue

                # Freshness check for sell market data
                if not self.market_validator.is_market_data_fresh(sell_record, sell_market, good, 'sell'):
                    continue

                spread = sell_price - buy_price
                if spread <= 0:
                    continue

                opportunities.append({
                    'buy_waypoint': buy_market,
                    'sell_waypoint': sell_market,
                    'good': good,
                    'buy_price': buy_price,
                    'sell_price': sell_price,
                    'spread': spread,
                    'trade_volume': buy_record.get('trade_volume', 100),
                })

        return opportunities

    def get_opportunity_summary(self, opportunities: List[Dict]) -> Dict:
        """
        Generate summary statistics for trade opportunities

        Args:
            opportunities: List of trade opportunity dicts

        Returns:
            Dictionary with summary statistics:
            - total_opportunities: Total count
            - unique_goods: Number of unique goods
            - unique_markets: Number of unique markets (buy + sell)
            - avg_spread: Average profit spread
            - max_spread: Maximum profit spread
            - top_goods: Top 5 goods by spread
        """
        if not opportunities:
            return {
                'total_opportunities': 0,
                'unique_goods': 0,
                'unique_markets': 0,
                'avg_spread': 0,
                'max_spread': 0,
                'top_goods': [],
            }

        unique_goods = set(opp['good'] for opp in opportunities)
        unique_markets = set()
        for opp in opportunities:
            unique_markets.add(opp['buy_waypoint'])
            unique_markets.add(opp['sell_waypoint'])

        spreads = [opp['spread'] for opp in opportunities]
        avg_spread = sum(spreads) / len(spreads)
        max_spread = max(spreads)

        # Top 5 goods by average spread
        goods_by_spread = {}
        for opp in opportunities:
            good = opp['good']
            if good not in goods_by_spread:
                goods_by_spread[good] = []
            goods_by_spread[good].append(opp['spread'])

        top_goods = sorted(
            [(good, sum(spreads) / len(spreads)) for good, spreads in goods_by_spread.items()],
            key=lambda x: x[1],
            reverse=True
        )[:5]

        return {
            'total_opportunities': len(opportunities),
            'unique_goods': len(unique_goods),
            'unique_markets': len(unique_markets),
            'avg_spread': int(avg_spread),
            'max_spread': max_spread,
            'top_goods': [(good, int(avg)) for good, avg in top_goods],
        }
