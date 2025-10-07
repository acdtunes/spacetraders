"""
SpaceTraders bot operations

Modular operation handlers for different bot tasks
"""

from .mining import mining_operation
from .multileg_trader import multileg_trade_operation, trade_plan_operation
from .purchasing import purchase_ship_operation
from .contracts import contract_operation, negotiate_operation
from .fleet import status_operation, monitor_operation
from .analysis import utilities_operation
from .routing import (
    graph_build_operation,
    route_plan_operation,
    scout_markets_operation,
)
from .daemon import (
    daemon_start_operation,
    daemon_stop_operation,
    daemon_status_operation,
    daemon_logs_operation,
    daemon_cleanup_operation,
)
from .assignments import (
    assignment_list_operation,
    assignment_assign_operation,
    assignment_release_operation,
    assignment_available_operation,
    assignment_find_operation,
    assignment_sync_operation,
    assignment_reassign_operation,
    assignment_status_operation,
    assignment_init_operation,
)
from .scout_coordination import (
    coordinator_start_operation,
    coordinator_add_ship_operation,
    coordinator_remove_ship_operation,
    coordinator_stop_operation,
    coordinator_status_operation,
)
from .captain_logging import captain_log_operation
from .navigation import navigate_ship

__all__ = [
    # Mining
    'mining_operation',

    # Trading
    'multileg_trade_operation',
    'trade_plan_operation',
    'purchase_ship_operation',

    # Contracts
    'contract_operation',
    'negotiate_operation',

    # Fleet management
    'status_operation',
    'monitor_operation',

    # Utilities
    'utilities_operation',

    # Routing
    'graph_build_operation',
    'route_plan_operation',
    'scout_markets_operation',

    # Daemon management
    'daemon_start_operation',
    'daemon_stop_operation',
    'daemon_status_operation',
    'daemon_logs_operation',
    'daemon_cleanup_operation',

    # Ship assignments
    'assignment_list_operation',
    'assignment_assign_operation',
    'assignment_release_operation',
    'assignment_available_operation',
    'assignment_find_operation',
    'assignment_sync_operation',
    'assignment_reassign_operation',
    'assignment_status_operation',
    'assignment_init_operation',

    # Scout coordination
    'coordinator_start_operation',
    'coordinator_add_ship_operation',
    'coordinator_remove_ship_operation',
    'coordinator_stop_operation',
    'coordinator_status_operation',

    # Captain logging
    'captain_log_operation',

    # Navigation
    'navigate_ship',
]
