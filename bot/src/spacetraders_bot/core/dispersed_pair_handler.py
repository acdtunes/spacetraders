"""Handles dispersed 2-market pairs using system-wide centroid."""

from typing import Dict, List, Optional


class DispersedPairHandler:
    """
    Identifies isolated markets in dispersed 2-market pairs.

    When a scout has only 2 markets that are far apart (>500 units), using the
    pair's local centroid to determine which is "most expensive" doesn't work well.
    Instead, this class uses the system-wide centroid to identify which market is
    more isolated from the rest of the system.
    """

    def __init__(self, graph: Dict, all_markets: List[str]):
        """
        Initialize the handler with system graph data.

        Args:
            graph: System graph containing waypoint positions
            all_markets: All markets in the system (for calculating system-wide centroid)
        """
        self.graph = graph
        self.all_markets = all_markets

    def find_most_isolated(
        self,
        markets: List[str],
        positions: Dict[str, tuple]
    ) -> Optional[str]:
        """
        Find the most isolated market in a dispersed 2-market pair.

        Args:
            markets: List of market symbols (should be exactly 2 for this handler)
            positions: Dict mapping market symbols to (x, y) coordinates

        Returns:
            Market symbol farthest from system-wide centroid, or None if:
            - Not exactly 2 markets
            - Pair distance <=500 units (not dispersed)
            - Missing position data
        """
        # Only handle 2-market pairs
        if len(markets) != 2:
            return None

        market1, market2 = list(markets)
        pos1 = positions.get(market1)
        pos2 = positions.get(market2)

        if not pos1 or not pos2:
            return None

        # Calculate distance between the two markets
        distance = ((pos2[0] - pos1[0])**2 + (pos2[1] - pos1[1])**2)**0.5

        # Only handle dispersed pairs (>500 units apart)
        if distance <= 500:
            return None

        print(f"   Detected dispersed 2-market pair: {market1} and {market2} ({distance:.0f} units apart)")
        print(f"   Using system-wide centroid to find most isolated market...")

        # Calculate system-wide centroid from ALL markets
        all_positions = []
        for m in self.all_markets:
            wp = self.graph['waypoints'].get(m)
            if wp:
                all_positions.append((wp['x'], wp['y']))

        if not all_positions:
            return None

        system_centroid_x = sum(p[0] for p in all_positions) / len(all_positions)
        system_centroid_y = sum(p[1] for p in all_positions) / len(all_positions)

        # Find market farthest from system-wide centroid (most isolated)
        def distance_from_system_centroid(market: str) -> float:
            if market not in positions:
                return 0
            x, y = positions[market]
            return ((x - system_centroid_x)**2 + (y - system_centroid_y)**2)**0.5

        most_isolated = max(markets, key=distance_from_system_centroid)
        dist1 = distance_from_system_centroid(market1)
        dist2 = distance_from_system_centroid(market2)
        print(f"   Distance from system centroid: {market1}={dist1:.0f}, {market2}={dist2:.0f}")
        print(f"   Most isolated: {most_isolated}")
        return most_isolated
