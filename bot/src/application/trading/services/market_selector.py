"""MarketSelector - Selects representative markets and generates pairs for experiments."""

from typing import List
from collections import defaultdict

from adapters.secondary.persistence.work_queue_repository import MarketPair
from ports.outbound.market_repository import IMarketRepository


class MarketSelector:
    """Service for selecting diverse markets and generating market pairs."""

    def __init__(self, market_repository: IMarketRepository):
        """
        Initialize market selector.

        Args:
            market_repository: Repository for querying market data
        """
        self._market_repo = market_repository

    def select_representative_markets(
        self,
        system: str,
        good: str,
        player_id: int,
        target_count: int = 6
    ) -> List[dict]:
        """
        Select diverse markets for testing a specific good.

        Strategy:
        1. Query all markets in system selling this good
        2. Group by (supply, activity) combinations
        3. Select 1-2 markets per combination (prefer higher trade_volume)
        4. Aim for target_count markets total (4-8 recommended)

        Args:
            system: System symbol (e.g., "X1-GZ7")
            good: Good symbol (e.g., "IRON_ORE")
            player_id: Player ID for market data access
            target_count: Target number of markets to select (default: 6)

        Returns:
            List of market data dicts with diverse characteristics
        """
        # Get all markets in system that have this good
        all_markets = self._market_repo.list_markets_in_system(
            system,
            player_id,
            max_age_minutes=None  # Use all available cached data
        )

        # Filter markets that sell this good
        markets_with_good = []
        for market in all_markets:
            for trade_good in market.trade_goods:
                if trade_good.symbol == good:
                    markets_with_good.append({
                        'waypoint': market.waypoint_symbol,
                        'supply': trade_good.supply,
                        'activity': trade_good.activity,
                        'trade_volume': trade_good.trade_volume,
                        'purchase_price': trade_good.purchase_price,
                        'sell_price': trade_good.sell_price
                    })
                    break

        if not markets_with_good:
            return []

        # Group by (supply, activity) combinations
        groups = defaultdict(list)
        for market in markets_with_good:
            key = (market['supply'], market['activity'])
            groups[key].append(market)

        # Select markets from each group (prefer higher trade_volume)
        selected = []
        for group_markets in groups.values():
            # Sort by trade_volume descending
            sorted_markets = sorted(
                group_markets,
                key=lambda m: m['trade_volume'],
                reverse=True
            )

            # Take top 1-2 from each group
            selected.extend(sorted_markets[:2])

        # Sort by trade_volume and take top target_count
        selected = sorted(
            selected,
            key=lambda m: m['trade_volume'],
            reverse=True
        )[:target_count]

        return selected

    def generate_market_pairs(
        self,
        markets: List[dict],
        good: str
    ) -> List[MarketPair]:
        """
        Generate all ordered market pairs.

        For N markets, generates N × (N-1) pairs.
        Each pair represents: buy at market A, sell at market B.

        Args:
            markets: List of market dicts (from select_representative_markets)
            good: Good symbol

        Returns:
            List of MarketPair objects (all combinations except same-market pairs)
        """
        pairs = []
        for buy_market in markets:
            for sell_market in markets:
                if buy_market['waypoint'] != sell_market['waypoint']:
                    pairs.append(MarketPair(
                        queue_id=None,  # Will be assigned when enqueued
                        pair_id=f"{good}:{buy_market['waypoint']}:{sell_market['waypoint']}",
                        good_symbol=good,
                        buy_market=buy_market['waypoint'],
                        sell_market=sell_market['waypoint']
                    ))
        return pairs
