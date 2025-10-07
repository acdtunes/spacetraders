"""
SpaceTraders Bot Library
Consolidated core functionality for SpaceTraders automation
"""

from .api_client import APIClient, RateLimiter
from .ship_controller import ShipController
from .utils import (
    calculate_distance,
    estimate_fuel_cost,
    calculate_arrival_wait_time,
    parse_waypoint_symbol,
    format_credits,
    timestamp,
    timestamp_iso,
    resource_to_deposit_type,
    calculate_profit,
    select_flight_mode
)
from .market_data import (
    get_waypoint_goods,
    get_waypoint_good,
    find_markets_selling,
    find_markets_buying,
    get_recent_updates,
    get_stale_markets,
    summarize_good,
)

__all__ = [
    'APIClient',
    'RateLimiter',
    'ShipController',
    'calculate_distance',
    'estimate_fuel_cost',
    'calculate_arrival_wait_time',
    'parse_waypoint_symbol',
    'format_credits',
    'timestamp',
    'timestamp_iso',
    'resource_to_deposit_type',
    'calculate_profit',
    'select_flight_mode',
    'get_waypoint_goods',
    'get_waypoint_good',
    'find_markets_selling',
    'find_markets_buying',
    'get_recent_updates',
    'get_stale_markets',
    'summarize_good',
]
