from dataclasses import dataclass
from typing import List, Optional
from enum import Enum

from ..shared.value_objects import Waypoint, FlightMode, Fuel

class RouteStatus(Enum):
    """Route execution status"""
    PLANNED = "PLANNED"
    EXECUTING = "EXECUTING"
    COMPLETED = "COMPLETED"
    FAILED = "FAILED"
    ABORTED = "ABORTED"

@dataclass(frozen=True)
class RouteSegment:
    """Immutable route segment value object"""
    from_waypoint: Waypoint
    to_waypoint: Waypoint
    distance: float
    fuel_required: int
    travel_time: int
    flight_mode: FlightMode
    requires_refuel: bool = False

    def __repr__(self) -> str:
        mode = self.flight_mode.mode_name
        refuel = " [REFUEL]" if self.requires_refuel else ""
        return f"{self.from_waypoint.symbol} → {self.to_waypoint.symbol} ({self.distance:.1f}u, {self.fuel_required}⛽, {mode}){refuel}"

class Route:
    """
    Route aggregate root - represents a complete navigation plan

    Invariants:
    - Segments form connected path (segment[i].to == segment[i+1].from)
    - Total fuel required does not exceed ship capacity
    - Route can only be executed from PLANNED status
    """

    def __init__(
        self,
        route_id: str,
        ship_symbol: str,
        player_id: int,
        segments: List[RouteSegment],
        ship_fuel_capacity: int,
        refuel_before_departure: bool = False
    ):
        if not segments:
            raise ValueError("Route must have at least one segment")

        self._route_id = route_id
        self._ship_symbol = ship_symbol
        self._player_id = player_id
        self._segments = segments
        self._ship_fuel_capacity = ship_fuel_capacity
        self._refuel_before_departure = refuel_before_departure
        self._status = RouteStatus.PLANNED
        self._current_segment_index = 0

        self._validate()

    @property
    def route_id(self) -> str:
        return self._route_id

    @property
    def ship_symbol(self) -> str:
        return self._ship_symbol

    @property
    def player_id(self) -> int:
        return self._player_id

    @property
    def segments(self) -> List[RouteSegment]:
        return self._segments.copy()

    @property
    def status(self) -> RouteStatus:
        return self._status

    @property
    def current_segment_index(self) -> int:
        return self._current_segment_index

    @property
    def refuel_before_departure(self) -> bool:
        return self._refuel_before_departure

    def get_current_segment_index(self) -> int:
        """
        Get the current segment index in the route.

        Returns:
            int: Zero-based index of the current segment being executed.
                 Returns the total number of segments if route is completed.
        """
        return self._current_segment_index

    def _validate(self) -> None:
        """Validate route invariants"""
        # Check segments form connected path
        for i in range(len(self._segments) - 1):
            current = self._segments[i]
            next_seg = self._segments[i + 1]
            if current.to_waypoint.symbol != next_seg.from_waypoint.symbol:
                raise ValueError(
                    f"Segments not connected: {current.to_waypoint.symbol} → {next_seg.from_waypoint.symbol}"
                )

        # Check fuel requirements don't exceed capacity
        max_fuel_needed = max(seg.fuel_required for seg in self._segments)
        if max_fuel_needed > self._ship_fuel_capacity:
            raise ValueError(
                f"Segment requires {max_fuel_needed} fuel but ship capacity is {self._ship_fuel_capacity}"
            )

    def start_execution(self) -> None:
        """Begin route execution"""
        if self._status != RouteStatus.PLANNED:
            raise ValueError(f"Cannot start route in status {self._status.value}")
        self._status = RouteStatus.EXECUTING

    def complete_segment(self) -> None:
        """Mark current segment as complete and advance"""
        if self._status != RouteStatus.EXECUTING:
            raise ValueError(f"Cannot complete segment when route status is {self._status.value}")

        self._current_segment_index += 1

        # Check if route complete
        if self._current_segment_index >= len(self._segments):
            self._status = RouteStatus.COMPLETED

    def fail_route(self, reason: str) -> None:
        """Mark route as failed"""
        self._status = RouteStatus.FAILED

    def abort_route(self, reason: str) -> None:
        """Abort route execution"""
        self._status = RouteStatus.ABORTED

    def total_distance(self) -> float:
        """Calculate total distance of route"""
        return sum(seg.distance for seg in self._segments)

    def total_fuel_required(self) -> int:
        """Calculate total fuel required (assuming refuels at stops)"""
        return sum(seg.fuel_required for seg in self._segments)

    def total_travel_time(self) -> int:
        """Calculate total travel time in seconds"""
        return sum(seg.travel_time for seg in self._segments)

    def current_segment(self) -> Optional[RouteSegment]:
        """Get current segment being executed"""
        if self._current_segment_index < len(self._segments):
            return self._segments[self._current_segment_index]
        return None

    def remaining_segments(self) -> List[RouteSegment]:
        """Get remaining segments to execute"""
        return self._segments[self._current_segment_index:]

    def __repr__(self) -> str:
        return (
            f"Route(id={self.route_id}, ship={self.ship_symbol}, "
            f"segments={len(self._segments)}, status={self._status.value})"
        )
