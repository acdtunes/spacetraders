"""Operations available to the SpaceTraders bot CLI."""

from .analysis import utilities_operation
from .assignments import (
    assignment_assign_operation,
    assignment_available_operation,
    assignment_find_operation,
    assignment_init_operation,
    assignment_list_operation,
    assignment_reassign_operation,
    assignment_release_operation,
    assignment_status_operation,
    assignment_sync_operation,
)
from .captain_logging import captain_log_operation
from .contracts import contract_operation, negotiate_operation, batch_contract_operation
from .waypoint_query import waypoint_query_operation
from .daemon import (
    daemon_cleanup_operation,
    daemon_logs_operation,
    daemon_start_operation,
    daemon_status_operation,
    daemon_stop_operation,
)
from .fleet import monitor_operation, status_operation
from .mining import mining_operation
from .mining_optimizer import mining_optimize_operation
from .multileg_trader import multileg_trade_operation, trade_plan_operation, fleet_trade_optimize_operation
from .navigation import navigate_ship
from .purchasing import purchase_ship_operation
from .routing import (
    graph_build_operation,
    route_plan_operation,
    scout_markets_operation,
)
from .validate_routing import validate_routing_operation
from .scout_coordination import (
    coordinator_add_ship_operation,
    coordinator_remove_ship_operation,
    coordinator_start_operation,
    coordinator_status_operation,
    coordinator_stop_operation,
)

__all__ = [
    # Mining
    "mining_operation",
    "mining_optimize_operation",

    # Trading
    "multileg_trade_operation",
    "trade_plan_operation",
    "fleet_trade_optimize_operation",
    "purchase_ship_operation",

    # Contracts
    "contract_operation",
    "negotiate_operation",
    "batch_contract_operation",

    # Fleet management
    "status_operation",
    "monitor_operation",

    # Utilities
    "utilities_operation",

    # Routing
    "graph_build_operation",
    "route_plan_operation",
    "scout_markets_operation",
    "validate_routing_operation",

    # Daemon management
    "daemon_start_operation",
    "daemon_stop_operation",
    "daemon_status_operation",
    "daemon_logs_operation",
    "daemon_cleanup_operation",

    # Ship assignments
    "assignment_list_operation",
    "assignment_assign_operation",
    "assignment_release_operation",
    "assignment_available_operation",
    "assignment_find_operation",
    "assignment_sync_operation",
    "assignment_reassign_operation",
    "assignment_status_operation",
    "assignment_init_operation",

    # Scout coordination
    "coordinator_start_operation",
    "coordinator_add_ship_operation",
    "coordinator_remove_ship_operation",
    "coordinator_stop_operation",
    "coordinator_status_operation",

    # Captain logging
    "captain_log_operation",

    # Navigation
    "navigate_ship",

    # Waypoint query
    "waypoint_query_operation",
]
