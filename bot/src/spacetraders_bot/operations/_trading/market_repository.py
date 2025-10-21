"""
Market Repository

Data access layer for market-related database operations.
Single Responsibility: Encapsulate all database queries for market data.
"""

from dataclasses import dataclass
from typing import List, Optional, Tuple


@dataclass
class NearbyMarketBuyer:
    """Represents a market that buys a specific good"""
    waypoint_symbol: str
    purchase_price: int
    x: int
    y: int
    distance: float

    @classmethod
    def from_row(cls, row: Tuple) -> 'NearbyMarketBuyer':
        """Create from database row"""
        return cls(
            waypoint_symbol=row[0],
            purchase_price=row[1],
            x=row[2],
            y=row[3],
            distance=row[4] ** 0.5  # Convert distance_squared to distance
        )


class MarketRepository:
    """Repository for market data access"""

    def __init__(self, db):
        self.db = db

    def get_waypoint_coordinates(self, waypoint: str) -> Optional[Tuple[float, float]]:
        """
        Get coordinates for a waypoint

        Args:
            waypoint: Waypoint symbol

        Returns:
            Tuple of (x, y) or None if not found
        """
        with self.db.connection() as conn:
            cursor = conn.cursor()
            cursor.execute(
                "SELECT x, y FROM waypoints WHERE waypoint_symbol = ?",
                (waypoint,)
            )
            row = cursor.fetchone()
            return (row[0], row[1]) if row else None

    def calculate_distance(self, from_waypoint: str, to_waypoint: str) -> float:
        """
        Calculate Euclidean distance between two waypoints

        Args:
            from_waypoint: Source waypoint symbol
            to_waypoint: Destination waypoint symbol

        Returns:
            Distance in units, or 150.0 if coordinates not found
        """
        with self.db.connection() as conn:
            cursor = conn.cursor()

            cursor.execute(
                "SELECT x, y FROM waypoints WHERE waypoint_symbol = ?",
                (from_waypoint,)
            )
            from_row = cursor.fetchone()

            cursor.execute(
                "SELECT x, y FROM waypoints WHERE waypoint_symbol = ?",
                (to_waypoint,)
            )
            to_row = cursor.fetchone()

            if not from_row or not to_row:
                return 150.0

            dx = to_row[0] - from_row[0]
            dy = to_row[1] - from_row[1]
            return (dx**2 + dy**2) ** 0.5

    def find_nearby_buyers(
        self,
        good: str,
        origin_waypoint: str,
        system: str,
        max_distance: int = 200,
        limit: int = 5
    ) -> List[NearbyMarketBuyer]:
        """
        Find markets that buy a specific good within distance threshold

        Args:
            good: Trade good symbol
            origin_waypoint: Current waypoint
            system: System symbol (e.g., "X1-HU87")
            max_distance: Maximum distance in units
            limit: Maximum number of results

        Returns:
            List of NearbyMarketBuyer objects sorted by distance
        """
        origin_coords = self.get_waypoint_coordinates(origin_waypoint)
        if not origin_coords:
            return []

        with self.db.connection() as conn:
            cursor = conn.cursor()

            # Find markets that buy this good, sorted by distance
            cursor.execute("""
                SELECT
                    m.waypoint_symbol,
                    m.purchase_price,
                    w.x,
                    w.y,
                    ((w.x - ?) * (w.x - ?) + (w.y - ?) * (w.y - ?)) as distance_squared
                FROM market_data m
                JOIN waypoints w ON m.waypoint_symbol = w.waypoint_symbol
                WHERE m.good_symbol = ?
                AND m.purchase_price > 0
                AND w.waypoint_symbol LIKE ?
                ORDER BY distance_squared ASC
                LIMIT ?
            """, (
                origin_coords[0], origin_coords[0],
                origin_coords[1], origin_coords[1],
                good,
                f"{system}-%",
                limit
            ))

            rows = cursor.fetchall()
            buyers = [NearbyMarketBuyer.from_row(row) for row in rows]

            # Filter by max distance
            return [b for b in buyers if b.distance <= max_distance]

    def check_market_accepts_good(self, waypoint: str, good: str) -> bool:
        """
        Check if a market accepts (buys) a specific good

        Args:
            waypoint: Market waypoint symbol
            good: Trade good symbol

        Returns:
            True if market has purchase_price > 0 for the good
        """
        with self.db.connection() as conn:
            market_data = self.db.get_market_data(conn, waypoint, good)
            if market_data and len(market_data) > 0:
                return market_data[0].get('purchase_price', 0) > 0
            return False
