"""Detects and handles oscillation when balancing market partitions."""

from typing import List, Optional, Callable


class BalanceOscillationDetector:
    """
    Detects when the same market oscillates back and forth during tour balancing.

    When balancing tour times between ships, sometimes a market gets moved repeatedly
    between the same two ships (oscillation). This class detects that pattern and
    finds an alternative market to break the cycle.
    """

    def __init__(self, find_boundary_market_fn: Callable[[List[str], List[str]], Optional[str]]):
        """
        Initialize the oscillation detector.

        Args:
            find_boundary_market_fn: Function to find a boundary market between two partitions.
                                    Should accept (source_markets, target_markets) and return
                                    a market symbol or None.
        """
        self._find_boundary_market = find_boundary_market_fn

    def check_and_resolve(
        self,
        market_to_move: str,
        last_moved: str,
        longest_partition: List[str],
        shortest_partition: List[str]
    ) -> Optional[str]:
        """
        Check for oscillation and resolve it by finding an alternative market.

        Args:
            market_to_move: The market that would be moved next
            last_moved: The market that was moved in the previous iteration
            longest_partition: Markets in the longest tour
            shortest_partition: Markets in the shortest tour

        Returns:
            - The original market if no oscillation detected
            - An alternative market if oscillation detected and alternative found
            - None if oscillation detected but no alternative available (caller should stop balancing)
        """
        # No oscillation if we're moving a different market than last time
        if market_to_move != last_moved:
            return market_to_move

        # Oscillation detected - same market moving back and forth
        print(f"⚠️  Detected oscillation ({market_to_move} moving back and forth)")

        # Try to find an alternative market to move instead
        remaining_markets = [m for m in longest_partition if m != market_to_move]
        if not remaining_markets:
            print("   No other markets available, stopping")
            return None

        # Find second-best market to move
        alternative_market = self._find_boundary_market(remaining_markets, shortest_partition)
        if not alternative_market:
            print("   No alternative market found, stopping")
            return None

        print(f"   Trying alternative market: {alternative_market}")
        return alternative_market
