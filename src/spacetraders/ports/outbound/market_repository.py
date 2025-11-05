"""Market repository port interface"""
from abc import ABC, abstractmethod
from typing import List, Optional
from ...domain.shared.market import Market, TradeGood


class IMarketRepository(ABC):
    """Port interface for market data persistence"""

    @abstractmethod
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
        pass

    @abstractmethod
    def get_market_data(self, waypoint: str, player_id: int) -> Optional[Market]:
        """
        Get latest market snapshot for a waypoint.

        Args:
            waypoint: Waypoint symbol
            player_id: Player ID

        Returns:
            Market value object or None if not found
        """
        pass

    @abstractmethod
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
        pass
