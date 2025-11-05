from typing import Optional
from ..shared.value_objects import Fuel, FlightMode, Waypoint

class FlightModeSelector:
    """
    Domain service for selecting optimal flight mode

    Business rules:
    - ALWAYS prioritize speed (BURN > CRUISE > DRIFT)
    - Maintain absolute safety margin (default: 4 fuel units)
    - Only use slower modes when fuel constraints require it
    """

    SAFETY_MARGIN = 4  # Absolute fuel units to keep in reserve

    @staticmethod
    def select_for_distance(
        fuel: Fuel,
        distance: float,
        require_return: bool = False,
        safety_margin: int = SAFETY_MARGIN
    ) -> FlightMode:
        """
        Select fastest flight mode that maintains safety margin.

        Strategy: Try modes in speed order (BURN > CRUISE > DRIFT) and pick
        the fastest one that leaves enough fuel.

        Args:
            fuel: Current fuel state
            distance: Distance to travel
            require_return: If True, ensure enough fuel for return trip
            safety_margin: Minimum fuel units to keep in reserve

        Returns:
            Fastest FlightMode that works for the journey
        """
        multiplier = 2 if require_return else 1
        total_distance = distance * multiplier

        # Try BURN first (fastest)
        burn_cost = FlightMode.BURN.fuel_cost(total_distance)
        if fuel.current >= burn_cost + safety_margin:
            return FlightMode.BURN

        # Try CRUISE next (standard speed)
        cruise_cost = FlightMode.CRUISE.fuel_cost(total_distance)
        if fuel.current >= cruise_cost + safety_margin:
            return FlightMode.CRUISE

        # Fall back to DRIFT (slowest but most fuel efficient)
        drift_cost = FlightMode.DRIFT.fuel_cost(total_distance)
        if fuel.current >= drift_cost + safety_margin:
            return FlightMode.DRIFT

        # Not enough fuel even for DRIFT - return DRIFT and let caller handle refuel
        return FlightMode.DRIFT

class RefuelPlanner:
    """
    Domain service for planning refuel stops

    Business rules:
    - Use absolute safety margin (4 fuel units)
    - Refuel only when necessary to reach destination
    - Prioritize speed over fuel efficiency
    """

    SAFETY_MARGIN = 4  # Absolute fuel units to keep in reserve

    @staticmethod
    def should_refuel(
        fuel: Fuel,
        at_marketplace: bool,
        next_leg_distance: float = 0,
        safety_margin: int = SAFETY_MARGIN
    ) -> bool:
        """
        Determine if ship should refuel at current location.

        Only refuel if:
        1. Location has fuel available (marketplace)
        2. Current fuel insufficient for next leg + safety margin

        Args:
            fuel: Current fuel state
            at_marketplace: Whether current location has fuel
            next_leg_distance: Distance to next waypoint (0 = unknown)
            safety_margin: Minimum fuel units to keep in reserve

        Returns:
            True if should refuel now
        """
        if not at_marketplace:
            return False

        # If we know the next leg distance, check if we have enough fuel
        if next_leg_distance > 0:
            # Check if we can do next leg in BURN mode (fastest)
            burn_cost = FlightMode.BURN.fuel_cost(next_leg_distance)
            return fuel.current < burn_cost + safety_margin

        # Unknown next leg - refuel if below safety margin
        return fuel.current < safety_margin

    @staticmethod
    def needs_refuel_stop(
        fuel: Fuel,
        distance_to_destination: float,
        distance_to_refuel_point: float,
        mode: FlightMode,
        safety_margin: int = SAFETY_MARGIN
    ) -> bool:
        """
        Determine if refuel stop needed to reach destination.

        Args:
            fuel: Current fuel
            distance_to_destination: Direct distance to destination
            distance_to_refuel_point: Distance to nearest refuel point
            mode: Intended flight mode
            safety_margin: Minimum fuel units to keep in reserve

        Returns:
            True if refuel stop needed
        """
        fuel_to_destination = mode.fuel_cost(distance_to_destination)

        # Can we reach destination directly with safety margin?
        if fuel.current >= fuel_to_destination + safety_margin:
            return False

        # Can we reach refuel point?
        fuel_to_refuel = mode.fuel_cost(distance_to_refuel_point)
        return fuel.current >= fuel_to_refuel + safety_margin

class RouteValidator:
    """
    Domain service for validating route feasibility

    Business rules:
    - All segments must be connected
    - Fuel requirements must not exceed ship capacity
    - Route must have at least one segment
    """

    @staticmethod
    def validate_segments_connected(segments) -> bool:
        """Check if segments form connected path"""
        for i in range(len(segments) - 1):
            if segments[i].to_waypoint.symbol != segments[i + 1].from_waypoint.symbol:
                return False
        return True

    @staticmethod
    def validate_fuel_capacity(segments, ship_fuel_capacity: int) -> bool:
        """Check if any segment exceeds ship fuel capacity"""
        return all(seg.fuel_required <= ship_fuel_capacity for seg in segments)
