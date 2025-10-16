#!/usr/bin/env python3
"""
Utility Functions - Consolidated common calculations and helpers
"""

import math
from typing import Tuple, Dict, Any
from datetime import datetime, timezone
from dateutil import parser as dateparser


def calculate_distance(coord1: Dict[str, int], coord2: Dict[str, int]) -> float:
    """
    Calculate Euclidean distance between two coordinates

    Args:
        coord1: {'x': int, 'y': int}
        coord2: {'x': int, 'y': int}

    Returns:
        Distance as float
    """
    x1, y1 = coord1['x'], coord1['y']
    x2, y2 = coord2['x'], coord2['y']
    return math.sqrt((x2 - x1) ** 2 + (y2 - y1) ** 2)


def estimate_fuel_cost(distance: float, flight_mode: str = "CRUISE") -> int:
    """
    Estimate fuel consumption for a journey

    Args:
        distance: Distance in units
        flight_mode: CRUISE, DRIFT, or BURN

    Returns:
        Estimated fuel units needed
    """
    if flight_mode == "CRUISE":
        return int(distance * 1.1)  # ~1 fuel/unit + 10% buffer
    elif flight_mode == "DRIFT":
        return max(1, int(distance / 300 * 1.1))  # ~1 fuel/300 units + 10% buffer
    elif flight_mode == "BURN":
        return int(distance * 2)  # ~2 fuel/unit (estimate)
    return int(distance)


def calculate_arrival_wait_time(arrival_time_str: str) -> int:
    """
    Calculate seconds to wait until arrival

    Args:
        arrival_time_str: ISO format arrival time from API

    Returns:
        Seconds to wait (minimum 0)
    """
    arrival_time = dateparser.isoparse(arrival_time_str.replace('Z', '+00:00'))
    now = datetime.now(timezone.utc)
    wait_seconds = (arrival_time - now).total_seconds()
    return max(0, int(wait_seconds))


def parse_waypoint_symbol(waypoint_symbol: str) -> Tuple[str, str]:
    """
    Parse waypoint symbol into system and waypoint

    Args:
        waypoint_symbol: Full waypoint (e.g., 'X1-HU87-A1')

    Returns:
        Tuple of (system, waypoint)
        e.g., ('X1-HU87', 'X1-HU87-A1')
    """
    parts = waypoint_symbol.split('-')
    system = f"{parts[0]}-{parts[1]}"
    return system, waypoint_symbol


def format_credits(credits: int) -> str:
    """Format credits with thousand separators"""
    return f"{credits:,}"


def timestamp() -> str:
    """Get current local timestamp string (HH:MM:SS) with timezone support"""
    return datetime.now().astimezone().strftime("%H:%M:%S")


def timestamp_iso() -> str:
    """Get current timestamp in ISO format"""
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def resource_to_deposit_type(resource_symbol: str) -> list:
    """
    Map resource symbols to asteroid deposit trait types

    Args:
        resource_symbol: Resource like 'IRON_ORE', 'SILICON_CRYSTALS'

    Returns:
        List of deposit traits that may contain this resource
    """
    resource_map = {
        "IRON_ORE": ["COMMON_METAL_DEPOSITS"],
        "COPPER_ORE": ["COMMON_METAL_DEPOSITS"],
        "ALUMINUM_ORE": ["COMMON_METAL_DEPOSITS"],
        "SILVER_ORE": ["PRECIOUS_METAL_DEPOSITS"],
        "GOLD_ORE": ["PRECIOUS_METAL_DEPOSITS"],
        "PLATINUM_ORE": ["PRECIOUS_METAL_DEPOSITS"],
        "URANITE_ORE": ["RARE_METAL_DEPOSITS"],
        "MERITIUM_ORE": ["RARE_METAL_DEPOSITS"],
        "SILICON_CRYSTALS": ["MINERAL_DEPOSITS"],
        "QUARTZ_SAND": ["MINERAL_DEPOSITS"],
        "ICE_WATER": ["MINERAL_DEPOSITS"],
    }
    return resource_map.get(resource_symbol, ["COMMON_METAL_DEPOSITS"])


def calculate_profit(
    buy_price: int,
    sell_price: int,
    units: int,
    distance: float = 0,
    fuel_cost_per_unit: float = 0.7
) -> Dict[str, float]:
    """
    Calculate profit metrics for a trade

    Args:
        buy_price: Purchase price per unit
        sell_price: Sell price per unit
        units: Number of units
        distance: Distance to travel
        fuel_cost_per_unit: Cost per fuel unit

    Returns:
        Dict with profit metrics
    """
    margin = sell_price - buy_price
    gross_profit = margin * units

    fuel_cost = distance * fuel_cost_per_unit
    net_profit = gross_profit - fuel_cost

    total_cost = buy_price * units
    roi = (net_profit / total_cost * 100) if total_cost > 0 else 0

    return {
        "margin": margin,
        "gross_profit": round(gross_profit, 2),
        "fuel_cost": round(fuel_cost, 2),
        "net_profit": round(net_profit, 2),
        "roi": round(roi, 2)
    }


def select_flight_mode(
    current_fuel: int,
    fuel_capacity: int,
    distance: float,
    require_return: bool = True
) -> str:
    """
    Intelligently select flight mode based on fuel status

    CRITICAL: Always prefers CRUISE when fuel is adequate.
    This function should ONLY be used as a fallback when SmartNavigator
    is unavailable. SmartNavigator's OR-Tools routing is preferred.

    Args:
        current_fuel: Current fuel amount
        fuel_capacity: Max fuel capacity
        distance: Distance to travel
        require_return: Whether to reserve fuel for return trip

    Returns:
        Flight mode: 'CRUISE', 'DRIFT', or 'BURN'
    """
    # Estimate fuel needed for CRUISE
    cruise_fuel = estimate_fuel_cost(distance, "CRUISE")
    if require_return:
        cruise_fuel *= 2

    # ALWAYS prefer CRUISE when fuel is adequate (no percentage threshold)
    # CRUISE is 8-10x faster than DRIFT for most trips
    # Only use DRIFT in true emergencies
    if current_fuel >= cruise_fuel:
        return "CRUISE"

    # Insufficient fuel for CRUISE - check DRIFT feasibility
    drift_fuel = estimate_fuel_cost(distance, "DRIFT")
    if require_return:
        drift_fuel *= 2

    if current_fuel >= drift_fuel:
        return "DRIFT"

    # Emergency: not enough fuel even for DRIFT
    # This should never happen if routes are validated properly
    return "DRIFT"  # Default to safest option (will likely fail)
