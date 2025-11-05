"""Market domain value objects"""
from dataclasses import dataclass
from typing import Optional


@dataclass(frozen=True)
class TradeGood:
    """
    Trade good value object

    Represents a single commodity available at a market.
    Prices follow the market's perspective:
    - purchase_price: What the market PAYS when buying from ships (market bids)
    - sell_price: What the market CHARGES when selling to ships (market asks)
    """
    symbol: str                      # Commodity symbol (e.g., "IRON_ORE")
    supply: Optional[str]            # SCARCE, LIMITED, MODERATE, HIGH, ABUNDANT
    activity: Optional[str]          # WEAK, GROWING, STRONG, RESTRICTED
    purchase_price: int              # What ship RECEIVES when selling to market
    sell_price: int                  # What ship PAYS when buying from market
    trade_volume: int                # Trading volume


@dataclass(frozen=True)
class Market:
    """
    Market snapshot value object

    Immutable snapshot of market data at a specific time.
    """
    waypoint_symbol: str                   # Waypoint identifier
    trade_goods: tuple[TradeGood, ...]     # Immutable tuple of trade goods
    last_updated: str                      # ISO timestamp


@dataclass(frozen=True)
class TourResult:
    """Result from executing one market tour"""
    markets_visited: int
    goods_updated: int
    duration_seconds: float


@dataclass(frozen=True)
class PollResult:
    """Result from executing one stationary poll"""
    goods_updated: int
    waypoint: str
