"""
Trading Domain Models

Core data structures for multi-leg trading operations.
"""

from dataclasses import dataclass, field
from typing import Dict, List


@dataclass
class TradeAction:
    """Represents a buy or sell action at a market"""
    waypoint: str
    good: str
    action: str  # 'BUY' or 'SELL'
    units: int
    price_per_unit: int
    total_value: int


@dataclass
class RouteSegment:
    """Represents one leg of a multi-stop trade route with dependency tracking"""
    from_waypoint: str
    to_waypoint: str
    distance: int
    fuel_cost: int
    actions_at_destination: List[TradeAction]
    cargo_after: Dict[str, int]
    credits_after: int
    cumulative_profit: int

    # Dependency tracking for smart skip logic
    depends_on_segments: List[int] = field(default_factory=list)
    independent_of_segments: List[int] = field(default_factory=list)
    goods_involved: set = field(default_factory=set)
    markets_involved: set = field(default_factory=set)
    required_cargo_from_prior: Dict[str, int] = field(default_factory=dict)
    can_skip_if_failed: bool = True


@dataclass
class MultiLegRoute:
    """Complete multi-leg trading route"""
    segments: List[RouteSegment]
    total_profit: int
    total_distance: int
    total_fuel_cost: int
    estimated_time_minutes: int


@dataclass
class MarketEvaluation:
    """Represents the result of evaluating actions at a market"""
    actions: List[TradeAction]
    cargo_after: Dict[str, int]
    credits_after: int
    net_profit: int


@dataclass
class SegmentDependency:
    """Captures dependencies between route segments for smart skip logic"""
    segment_index: int
    depends_on: List[int]  # Indices of prerequisite segments
    dependency_type: str  # 'CARGO', 'CREDIT', or 'NONE'
    required_cargo: Dict[str, int]  # {good: units} needed from prior segments
    required_credits: int  # Minimum credits needed to execute
    can_skip: bool  # True if segment can be skipped without breaking route
