#!/usr/bin/env python3
"""
SpaceTraders Unified Bot - Consolidated automation system

This unified program replaces all scattered Python scripts with a single,
well-organized bot that can perform all SpaceTraders operations.

Usage:
    # Mining operation
    spacetraders-bot mine --player-id 7 --ship SHIP-1 --asteroid X1-HU87-B9 --market X1-HU87-B7 --cycles 30

    # Market scouting
    spacetraders-bot scout-markets --player-id PLAYER_ID --ship SHIP-2 --system X1-HU87 --markets 20

    # Trading operation
    spacetraders-bot trade --player-id PLAYER_ID --ship SHIP-3 --cycles 10

    # Contract fulfillment
    spacetraders-bot contract --player-id PLAYER_ID --ship SHIP-1 --contract-id ID

    # Market analysis
    # (analyze command removed)
"""

import argparse
import json
import logging
import os
import sys
from datetime import datetime
from pathlib import Path

from ..core import APIClient, ShipController, timestamp, timestamp_iso
from ..operations import (
    mining_operation,
    mining_optimize_operation,
    multileg_trade_operation,
    trade_plan_operation,
    fleet_trade_optimize_operation,
    purchase_ship_operation,
    contract_operation,
    negotiate_operation,
    status_operation,
    monitor_operation,
    utilities_operation,
    waypoint_query_operation,
    graph_build_operation,
    route_plan_operation,
    daemon_start_operation,
    daemon_stop_operation,
    daemon_status_operation,
    daemon_logs_operation,
    daemon_cleanup_operation,
    assignment_list_operation,
    assignment_assign_operation,
    assignment_release_operation,
    assignment_available_operation,
    assignment_find_operation,
    assignment_sync_operation,
    assignment_reassign_operation,
    assignment_status_operation,
    assignment_init_operation,
    coordinator_start_operation,
    coordinator_add_ship_operation,
    coordinator_remove_ship_operation,
    coordinator_stop_operation,
    coordinator_status_operation,
    captain_log_operation,
    validate_routing_operation,
)


def main():
    parser = argparse.ArgumentParser(
        description="SpaceTraders Unified Bot - All operations in one program",
        formatter_class=argparse.RawDescriptionHelpFormatter
    )

    # Add global log level argument
    parser.add_argument('--log-level', default='INFO',
                       choices=['INFO', 'WARNING', 'ERROR'],
                       help='Logging level (default: INFO)')

    subparsers = parser.add_subparsers(dest='operation', help='Operation to perform')

    # Mining operation
    mine_parser = subparsers.add_parser('mine', help='Autonomous mining operation')
    mine_parser.add_argument('--player-id', type=int, required=True, help='Player ID')
    mine_parser.add_argument('--ship', required=True, help='Ship symbol')
    mine_parser.add_argument('--asteroid', required=True, help='Asteroid waypoint')
    mine_parser.add_argument('--market', required=True, help='Market waypoint')
    mine_parser.add_argument('--cycles', type=int, default=30, help='Number of cycles')

    # Mining fleet optimization
    mining_optimize_parser = subparsers.add_parser('mining-optimize', help='Optimize mining fleet assignments using OR-Tools')
    mining_optimize_parser.add_argument('--player-id', type=int, required=True, help='Player ID')
    mining_optimize_parser.add_argument('--system', required=True, help='System symbol (e.g., X1-HU87)')
    mining_optimize_parser.add_argument('--ships', help='Comma-separated ship symbols (default: all EXCAVATOR ships)')
    mining_optimize_parser.add_argument('--algorithm', default='ortools', choices=['ortools', 'greedy'], help='Optimization algorithm (default: ortools)')
    mining_optimize_parser.add_argument('--output', help='Save results to JSON file')

    # Contract operation
    contract_parser = subparsers.add_parser('contract', help='Contract fulfillment')
    contract_parser.add_argument('--player-id', type=int, required=True, help='Player ID')
    contract_parser.add_argument('--ship', required=True, help='Ship symbol')
    contract_parser.add_argument('--contract-id', help='Contract ID (required for single contract mode)')
    contract_parser.add_argument('--contract-count', type=int, default=1, help='Number of contracts to negotiate and fulfill in sequence (default: 1)')
    contract_parser.add_argument('--buy-from', help='Waypoint to buy from (optional - will auto-discover if omitted)')

    # Trade operation (unified multi-leg and single-leg trading)
    trade_parser = subparsers.add_parser('trade', help='Unified trading operation (autonomous or fixed-route)')
    trade_parser.add_argument('--player-id', type=int, required=True, help='Player ID')
    trade_parser.add_argument('--ship', required=True, help='Ship symbol')
    trade_parser.add_argument('--system', help='System symbol (e.g., X1-JB26) - optional, defaults to ship system')
    trade_parser.add_argument('--max-stops', type=int, default=4, help='Maximum stops for autonomous mode (default: 4)')

    # Looping parameters (mutually exclusive)
    loop_group = trade_parser.add_mutually_exclusive_group()
    loop_group.add_argument('--cycles', type=int, help='Number of cycles to repeat (-1 for infinite)')
    loop_group.add_argument('--duration', type=float, help='Run for N hours')

    # Fixed-route mode (prescriptive trading)
    trade_parser.add_argument('--good', help='Trade good for fixed-route mode')
    trade_parser.add_argument('--buy-from', help='Buy market for fixed-route mode')
    trade_parser.add_argument('--sell-to', help='Sell market for fixed-route mode')
    trade_parser.add_argument('--min-profit', type=int, default=5000, help='Min profit per cycle for looping (default: 5000)')
    trade_parser.add_argument('--cargo', type=int, help='Override cargo capacity for fixed-route mode')

    trade_plan_parser = subparsers.add_parser(
        'trade-plan',
        help='Propose a multi-leg trading route without executing it'
    )
    trade_plan_parser.add_argument('--player-id', type=int, required=True, help='Player ID')
    trade_plan_parser.add_argument('--ship', required=True, help='Ship symbol')
    trade_plan_parser.add_argument('--max-stops', type=int, default=4, help='Maximum number of stops to evaluate (default: 4)')
    trade_plan_parser.add_argument('--system', help="Optional system override (defaults to ship's current system)")

    # Fleet trade optimization - Multi-ship conflict-aware route planning
    fleet_trade_parser = subparsers.add_parser(
        'fleet-trade-optimize',
        help='Optimize trade routes for multiple ships with conflict avoidance'
    )
    fleet_trade_parser.add_argument('--player-id', type=int, required=True, help='Player ID')
    fleet_trade_parser.add_argument('--ships', required=True, help='Comma-separated ship symbols (e.g., SHIP-1,SHIP-2)')
    fleet_trade_parser.add_argument('--system', required=True, help='System symbol (e.g., X1-TX46)')
    fleet_trade_parser.add_argument('--max-stops', type=int, default=4, help='Maximum stops per route (default: 4)')

    purchase_ship_parser = subparsers.add_parser('purchase-ship', help='Purchase ships from a shipyard')
    purchase_ship_parser.add_argument('--player-id', type=int, required=True, help='Player ID')
    purchase_ship_parser.add_argument('--ship', required=True, help='Ship symbol that will perform the purchase')
    purchase_ship_parser.add_argument('--shipyard', required=True, help='Shipyard waypoint symbol (e.g., X1-HU87-A1)')
    purchase_ship_parser.add_argument('--ship-type', required=True, help='Ship type to purchase (e.g., SHIP_EXPLORER)')
    purchase_ship_parser.add_argument('--quantity', type=int, default=1, help='Number of ships to purchase (default: 1)')
    purchase_ship_parser.add_argument('--max-budget', type=int, required=True, help='Maximum total credits to spend')

    # Analyze operation

    # Status operation (replaces check_status.sh, check_ship*.sh, etc.)
    status_parser = subparsers.add_parser('status', help='Check agent and ship status')
    status_parser.add_argument('--player-id', type=int, required=True, help='Player ID')
    status_parser.add_argument('--ships', help='Comma-separated ship symbols (default: all)')

    # Monitor operation (replaces monitor_loop.sh, fleet_monitor.sh, etc.)
    monitor_parser = subparsers.add_parser('monitor', help='Monitor fleet continuously')
    monitor_parser.add_argument('--player-id', type=int, required=True, help='Player ID')
    monitor_parser.add_argument('--ships', required=True, help='Comma-separated ship symbols')
    monitor_parser.add_argument('--interval', type=int, default=5, help='Check interval in minutes (default: 5)')
    monitor_parser.add_argument('--duration', type=int, default=12, help='Number of checks (default: 12)')

    # Negotiate operation (replaces negotiate_contract.sh)
    negotiate_parser = subparsers.add_parser('negotiate', help='Negotiate new contract')
    negotiate_parser.add_argument('--player-id', type=int, required=True, help='Player ID')
    negotiate_parser.add_argument('--ship', required=True, help='Ship symbol')

    # Utilities operation (replaces find_nearest_fuel.sh, etc.)
    util_parser = subparsers.add_parser('util', help='Utility operations')
    util_parser.add_argument('--player-id', type=int, required=True, help='Player ID')
    util_parser.add_argument('--type', dest='util_type', required=True,
                            choices=['find-fuel', 'distance', 'find-mining'],
                            help='Utility type')
    util_parser.add_argument('--ship', help='Ship symbol (for find-fuel and find-mining optimization)')
    util_parser.add_argument('--waypoint1', help='First waypoint (for distance)')
    util_parser.add_argument('--waypoint2', help='Second waypoint (for distance)')
    util_parser.add_argument('--system', help='System symbol (for find-mining)')

    # Graph building operation
    graph_parser = subparsers.add_parser('graph-build', help='Build system navigation graph')
    graph_parser.add_argument('--player-id', type=int, required=True, help='Player ID')
    graph_parser.add_argument('--system', required=True, help='System symbol (e.g., X1-HU87)')

    # Route planning operation
    route_parser = subparsers.add_parser('route-plan', help='Plan optimal route with fuel awareness')
    route_parser.add_argument('--player-id', type=int, required=True, help='Player ID')
    route_parser.add_argument('--ship', required=True, help='Ship symbol')
    route_parser.add_argument('--system', required=True, help='System symbol')
    route_parser.add_argument('--start', required=True, help='Starting waypoint')
    route_parser.add_argument('--goal', required=True, help='Destination waypoint')
    route_parser.add_argument('--output', help='Save route to JSON file')

    # Smart navigation operation
    navigate_parser = subparsers.add_parser('navigate', help='Navigate ship using SmartNavigator with fuel awareness')
    navigate_parser.add_argument('--player-id', type=int, required=True, help='Player ID')
    navigate_parser.add_argument('--ship', required=True, help='Ship symbol')
    navigate_parser.add_argument('--destination', required=True, help='Destination waypoint')

    # Market route planning (PLANNING ONLY - does not navigate!)
    plan_route_parser = subparsers.add_parser('plan-market-route', help='Plan optimized market tour route (PLANNING ONLY - does not navigate or collect data)')
    plan_route_parser.add_argument('--player-id', type=int, required=True, help='Player ID')
    plan_route_parser.add_argument('--ship', required=True, help='Ship symbol')
    plan_route_parser.add_argument('--system', required=True, help='System symbol (e.g., X1-HU87)')
    plan_route_parser.add_argument('--return-to-start', action='store_true', help='Return to starting waypoint')
    plan_route_parser.add_argument('--continuous', action='store_true', help='Continuous mode: restart immediately after completing tour')
    plan_route_parser.add_argument('--output', help='Save tour plan to JSON file')

    # Market scouting operation (NAVIGATES AND COLLECTS DATA)
    scout_markets_parser = subparsers.add_parser('scout-markets', help='Scout markets by NAVIGATING and COLLECTING market data')
    scout_markets_parser.add_argument('--player-id', type=int, required=True, help='Player ID')
    scout_markets_parser.add_argument('--ship', required=True, help='Ship symbol')
    scout_markets_parser.add_argument('--system', required=True, help='System to scout')
    scout_markets_parser.add_argument('--markets', type=int, default=20, help='Number of markets (ignored if --markets-list provided)')
    scout_markets_parser.add_argument('--markets-list', type=str, help='Comma-separated list of specific markets to visit (e.g., X1-JB26-A1,X1-JB26-B7)')
    scout_markets_parser.add_argument('--return-to-start', action='store_true', help='Return to starting waypoint')
    scout_markets_parser.add_argument('--continuous', action='store_true', help='Continuously loop the tour (restart after completion)')
    scout_markets_parser.add_argument('--interval', type=int, default=60, help='Polling interval in seconds for stationary scouts (default: 60)')
    scout_markets_parser.add_argument('--output', help='Save tour plan to JSON file')

    # Routing validation operation
    validate_parser = subparsers.add_parser('validate-routing', help='Validate routing predictions against live navigation')
    validate_parser.add_argument('--player-id', type=int, required=True, help='Player ID')
    validate_parser.add_argument('--ship', required=True, help='Ship symbol')
    validate_parser.add_argument('--destination', required=True, help='Destination waypoint symbol')
    validate_parser.add_argument('--dry-run', action='store_true', help='Plan only without executing navigation')

    # Daemon management operations
    daemon_parser = subparsers.add_parser('daemon', help='Background daemon management')
    daemon_subparsers = daemon_parser.add_subparsers(dest='daemon_action', help='Daemon action')

    # Start daemon (special handling - runs ANY operation in background)
    daemon_start_parser = daemon_subparsers.add_parser('start', help='Start operation as background daemon')
    daemon_start_parser.add_argument('daemon_operation', help='Operation to run (mine, trade, etc.)')
    daemon_start_parser.add_argument('--daemon-id', help='Daemon ID (auto-generated if not specified)')
    daemon_start_parser.add_argument('--player-id', type=int, required=True, help='Player ID')
    daemon_start_parser.add_argument('operation_args', nargs='*', help='Arguments for the operation')
    # Operation args will be parsed dynamically

    # Stop daemon
    daemon_stop_parser = daemon_subparsers.add_parser('stop', help='Stop running daemon')
    daemon_stop_parser.add_argument('daemon_id', help='Daemon ID')
    daemon_stop_parser.add_argument('--player-id', type=int, required=True, help='Player ID')

    # Daemon status
    daemon_status_parser = daemon_subparsers.add_parser('status', help='Show daemon status')
    daemon_status_parser.add_argument('daemon_id', nargs='?', help='Daemon ID (omit to list all)')
    daemon_status_parser.add_argument('--player-id', type=int, required=True, help='Player ID')

    # Daemon logs
    daemon_logs_parser = daemon_subparsers.add_parser('logs', help='Show daemon logs')
    daemon_logs_parser.add_argument('daemon_id', help='Daemon ID')
    daemon_logs_parser.add_argument('--lines', type=int, default=20, help='Number of lines to show')
    daemon_logs_parser.add_argument('--player-id', type=int, required=True, help='Player ID')

    # Cleanup stopped daemons
    daemon_cleanup_parser = daemon_subparsers.add_parser('cleanup', help='Clean up stopped daemons')
    daemon_cleanup_parser.add_argument('--player-id', type=int, required=True, help='Player ID')

    # Ship assignment management operations
    assignment_parser = subparsers.add_parser('assignments', help='Ship assignment management')
    assignment_subparsers = assignment_parser.add_subparsers(dest='assignment_action', help='Assignment action')

    # List assignments
    assignment_list_parser = assignment_subparsers.add_parser('list', help='List all ship assignments')
    assignment_list_parser.add_argument('--include-stale', action='store_true', help='Include stale assignments')
    assignment_list_parser.add_argument('--player-id', type=int, required=True, help='Player ID')

    # Assign ship
    assignment_assign_parser = assignment_subparsers.add_parser('assign', help='Assign ship to operation')
    assignment_assign_parser.add_argument('--ship', required=True, help='Ship symbol')
    assignment_assign_parser.add_argument('--operator', required=True, help='Operator name (e.g., trading_operator)')
    assignment_assign_parser.add_argument('--daemon-id', required=True, help='Associated daemon ID')
    assignment_assign_parser.add_argument('--op-type', dest='operation_type', required=True, help='Operation type (e.g., trade, mine, scout-markets)')
    assignment_assign_parser.add_argument('--duration', type=float, help='Expected duration in hours')
    assignment_assign_parser.add_argument('--player-id', type=int, required=True, help='Player ID')

    # Release ship
    assignment_release_parser = assignment_subparsers.add_parser('release', help='Release ship from assignment')
    assignment_release_parser.add_argument('ship', help='Ship symbol')
    assignment_release_parser.add_argument('--reason', default='manual_release', help='Release reason')
    assignment_release_parser.add_argument('--player-id', type=int, required=True, help='Player ID')

    # Check availability
    assignment_available_parser = assignment_subparsers.add_parser('available', help='Check if ship is available')
    assignment_available_parser.add_argument('ship', help='Ship symbol')
    assignment_available_parser.add_argument('--player-id', type=int, required=True, help='Player ID')

    # Find available ships
    assignment_find_parser = assignment_subparsers.add_parser('find', help='Find available ships')
    assignment_find_parser.add_argument('--cargo-min', type=int, help='Minimum cargo capacity')
    assignment_find_parser.add_argument('--fuel-min', type=int, help='Minimum fuel capacity')
    assignment_find_parser.add_argument('--player-id', type=int, required=True, help='Player ID')

    # Sync with daemons
    assignment_sync_parser = assignment_subparsers.add_parser('sync', help='Sync registry with daemon status')
    assignment_sync_parser.add_argument('--player-id', type=int, required=True, help='Player ID')

    # Reassign ships
    assignment_reassign_parser = assignment_subparsers.add_parser('reassign', help='Reassign ships from operation')
    assignment_reassign_parser.add_argument('--ships', required=True, help='Comma-separated ship symbols')
    assignment_reassign_parser.add_argument('--from-operation', required=True, help='Operation to reassign from')
    assignment_reassign_parser.add_argument('--no-stop', action='store_true', help='Do not stop daemons')
    assignment_reassign_parser.add_argument('--timeout', type=int, default=10, help='Daemon stop timeout (default: 10s)')
    assignment_reassign_parser.add_argument('--player-id', type=int, required=True, help='Player ID')

    # Ship status
    assignment_status_parser = assignment_subparsers.add_parser('status', help='Get detailed ship assignment status')
    assignment_status_parser.add_argument('ship', help='Ship symbol')
    assignment_status_parser.add_argument('--player-id', type=int, required=True, help='Player ID')

    # Initialize registry
    assignment_init_parser = assignment_subparsers.add_parser('init', help='Initialize registry from API')
    assignment_init_parser.add_argument('--player-id', type=int, required=True, help='Player ID')

    # Scout coordinator - Multi-ship continuous market scouting
    coordinator_parser = subparsers.add_parser('scout-coordinator', help='Multi-ship continuous market scouting')
    coordinator_subparsers = coordinator_parser.add_subparsers(dest='coordinator_action', help='Coordinator action')

    # Start coordinator
    coordinator_start_parser = coordinator_subparsers.add_parser('start', help='Start multi-ship continuous scouting')
    coordinator_start_parser.add_argument('--player-id', type=int, required=True, help='Player ID')
    coordinator_start_parser.add_argument('--system', required=True, help='System symbol (e.g., X1-HU87)')
    coordinator_start_parser.add_argument('--ships', required=True, help='Comma-separated ship symbols')
    coordinator_start_parser.add_argument('--exclude-markets', help='Comma-separated list of markets to exclude from auto-discovery (e.g., X1-TX46-I52,X1-TX46-J55 for stationary scouts)')

    # Add ship to coordinator
    coordinator_add_parser = coordinator_subparsers.add_parser('add-ship', help='Add ship to ongoing operation')
    coordinator_add_parser.add_argument('--system', required=True, help='System symbol')
    coordinator_add_parser.add_argument('--ship', required=True, help='Ship symbol to add')

    # Remove ship from coordinator
    coordinator_remove_parser = coordinator_subparsers.add_parser('remove-ship', help='Remove ship from ongoing operation')
    coordinator_remove_parser.add_argument('--system', required=True, help='System symbol')
    coordinator_remove_parser.add_argument('--ship', required=True, help='Ship symbol to remove')

    # Stop coordinator
    coordinator_stop_parser = coordinator_subparsers.add_parser('stop', help='Stop coordinator and all scouts')
    coordinator_stop_parser.add_argument('--system', required=True, help='System symbol')
    coordinator_stop_parser.add_argument('--player-id', type=int, required=True, help='Player ID for daemon cleanup')

    # Coordinator status
    coordinator_status_parser = coordinator_subparsers.add_parser('status', help='Show coordinator status')
    coordinator_status_parser.add_argument('--system', required=True, help='System symbol')
    coordinator_status_parser.add_argument('--player-id', type=int, required=True, help='Player ID for daemon status checks')

    # Captain log writer - Automated mission logging
    captain_log_parser = subparsers.add_parser('captain-log', help='Automated mission logging')
    captain_log_subparsers = captain_log_parser.add_subparsers(dest='log_action', help='Log action')

    # Initialize log
    log_init_parser = captain_log_subparsers.add_parser('init', help='Initialize new captain log')
    log_init_parser.add_argument('--agent', required=True, help='Agent callsign')
    log_init_parser.add_argument('--token', help='Agent token (optional)')

    # Start session
    log_session_start_parser = captain_log_subparsers.add_parser('session-start', help='Start new session')
    log_session_start_parser.add_argument('--agent', required=True, help='Agent callsign')
    log_session_start_parser.add_argument('--token', help='Agent token (optional)')
    log_session_start_parser.add_argument('--objective', required=True, help='Mission objective')
    log_session_start_parser.add_argument('--operator', default='AI First Mate', help='Operator name')
    log_session_start_parser.add_argument('--narrative', help='First-person briefing narrative')

    # End session
    log_session_end_parser = captain_log_subparsers.add_parser('session-end', help='End current session')
    log_session_end_parser.add_argument('--agent', required=True, help='Agent callsign')
    log_session_end_parser.add_argument('--token', help='Agent token (optional)')

    # Create log entry
    log_entry_parser = captain_log_subparsers.add_parser('entry', help='Create log entry')
    log_entry_parser.add_argument('--agent', required=True, help='Agent callsign')
    log_entry_parser.add_argument('--token', help='Agent token (optional)')
    log_entry_parser.add_argument('--type', dest='entry_type', required=True,
                                  choices=['SESSION_START', 'OPERATION_STARTED', 'OPERATION_COMPLETED',
                                          'CRITICAL_ERROR', 'STRATEGIC_DECISION', 'PERFORMANCE_SUMMARY'],
                                  help='Entry type')
    log_entry_parser.add_argument('--operator', help='Operator/specialist name')
    log_entry_parser.add_argument('--ship', help='Ship symbol')
    log_entry_parser.add_argument('--daemon-id', help='Daemon ID')
    log_entry_parser.add_argument('--op-type', help='Operation type (for OPERATION_STARTED)')
    log_entry_parser.add_argument('--narrative', help='First-person narrative describing what was done and why (REQUIRED for specialist agents)')
    log_entry_parser.add_argument('--insights', help='Strategic insights learned (for OPERATION_COMPLETED)')
    log_entry_parser.add_argument('--recommendations', help='Forward-looking recommendations (for OPERATION_COMPLETED)')
    log_entry_parser.add_argument('--error', help='Error description (for CRITICAL_ERROR)')
    log_entry_parser.add_argument('--resolution', help='Resolution (for CRITICAL_ERROR)')

    # Search logs
    log_search_parser = captain_log_subparsers.add_parser('search', help='Search logs')
    log_search_parser.add_argument('--agent', required=True, help='Agent callsign')
    log_search_parser.add_argument('--tag', help='Tag to search for')
    log_search_parser.add_argument('--timeframe', type=int, help='Hours to look back')

    # Generate report
    log_report_parser = captain_log_subparsers.add_parser('report', help='Generate executive report')
    log_report_parser.add_argument('--agent', required=True, help='Agent callsign')
    log_report_parser.add_argument('--token', help='Agent token (optional)')
    log_report_parser.add_argument('--duration', type=int, default=24, help='Hours to summarize (default: 24)')

    # Waypoint query operation
    waypoint_query_parser = subparsers.add_parser('waypoint-query', help='Query and filter waypoints from database')
    waypoint_query_parser.add_argument('--player-id', type=int, required=True, help='Player ID')
    waypoint_query_parser.add_argument('--system', required=True, help='System symbol (e.g., X1-JB26)')
    waypoint_query_parser.add_argument('--type', dest='waypoint_type', help='Waypoint type (PLANET, ASTEROID, MOON, etc.)')
    waypoint_query_parser.add_argument('--trait', help='Required trait (SHIPYARD, MARKETPLACE, COMMON_METAL_DEPOSITS, etc.)')
    waypoint_query_parser.add_argument('--exclude', help='Exclude waypoints with these traits (comma-separated: RADIOACTIVE,EXPLOSIVE_GASES)')
    waypoint_query_parser.add_argument('--has-fuel', action='store_true', help='Only show waypoints with fuel')

    # Special handling for daemon start - use parse_known_args to capture operation-specific args
    if len(sys.argv) > 2 and sys.argv[1] == 'daemon' and sys.argv[2] == 'start':
        args, unknown_args = parser.parse_known_args()
        if unknown_args:
            # Store unknown args as operation_args
            args.operation_args = unknown_args
    else:
        args = parser.parse_args()

    if not args.operation:
        parser.print_help()
        return 1

    # Dispatch to appropriate operation
    if args.operation == 'mine':
        return mining_operation(args)
    elif args.operation == 'mining-optimize':
        return mining_optimize_operation(args)
    elif args.operation == 'trade':
        return multileg_trade_operation(args)
    elif args.operation == 'trade-plan':
        return trade_plan_operation(args)
    elif args.operation == 'fleet-trade-optimize':
        return fleet_trade_optimize_operation(args)
    elif args.operation == 'purchase-ship':
        return purchase_ship_operation(args)
    elif args.operation == 'contract':
        # Determine if batch or single contract mode
        if args.contract_count > 1:
            # Batch mode: negotiate and fulfill multiple contracts
            from spacetraders_bot.operations.contracts import batch_contract_operation
            return batch_contract_operation(args)
        else:
            # Single contract mode: fulfill specific contract
            if not args.contract_id:
                print("❌ Error: --contract-id is required for single contract mode")
                print("   Use --contract-count N for batch mode (auto-negotiates N contracts)")
                return 1
            return contract_operation(args)
    elif args.operation == 'status':
        return status_operation(args)
    elif args.operation == 'monitor':
        return monitor_operation(args)
    elif args.operation == 'negotiate':
        return negotiate_operation(args)
    elif args.operation == 'util':
        return utilities_operation(args)
    elif args.operation == 'graph-build':
        return graph_build_operation(args)
    elif args.operation == 'route-plan':
        return route_plan_operation(args)
    elif args.operation == 'navigate':
        from spacetraders_bot.operations.navigation import navigate_operation
        return navigate_operation(args)
    elif args.operation == 'plan-market-route':
        from spacetraders_bot.operations.routing import scout_markets_operation as plan_route_operation
        return plan_route_operation(args)
    elif args.operation == 'scout-markets':
        from spacetraders_bot.operations.routing import scout_markets_operation
        return scout_markets_operation(args)
    elif args.operation == 'validate-routing':
        return validate_routing_operation(args)
    elif args.operation == 'daemon':
        if args.daemon_action == 'start':
            return daemon_start_operation(args)
        elif args.daemon_action == 'stop':
            return daemon_stop_operation(args)
        elif args.daemon_action == 'status':
            return daemon_status_operation(args)
        elif args.daemon_action == 'logs':
            return daemon_logs_operation(args)
        elif args.daemon_action == 'cleanup':
            return daemon_cleanup_operation(args)
    elif args.operation == 'assignments':
        if not hasattr(args, 'assignment_action') or not args.assignment_action:
            assignment_parser.print_help()
            return 1
        elif args.assignment_action == 'list':
            return assignment_list_operation(args)
        elif args.assignment_action == 'assign':
            return assignment_assign_operation(args)
        elif args.assignment_action == 'release':
            return assignment_release_operation(args)
        elif args.assignment_action == 'available':
            return assignment_available_operation(args)
        elif args.assignment_action == 'find':
            return assignment_find_operation(args)
        elif args.assignment_action == 'sync':
            return assignment_sync_operation(args)
        elif args.assignment_action == 'reassign':
            return assignment_reassign_operation(args)
        elif args.assignment_action == 'status':
            return assignment_status_operation(args)
        elif args.assignment_action == 'init':
            return assignment_init_operation(args)
    elif args.operation == 'scout-coordinator':
        if args.coordinator_action == 'start':
            return coordinator_start_operation(args)
        elif args.coordinator_action == 'add-ship':
            return coordinator_add_ship_operation(args)
        elif args.coordinator_action == 'remove-ship':
            return coordinator_remove_ship_operation(args)
        elif args.coordinator_action == 'stop':
            return coordinator_stop_operation(args)
        elif args.coordinator_action == 'status':
            return coordinator_status_operation(args)
    elif args.operation == 'captain-log':
        args.action = args.log_action  # Rename for consistency
        return captain_log_operation(args)
    elif args.operation == 'waypoint-query':
        return waypoint_query_operation(args)

    return 1


if __name__ == "__main__":
    try:
        sys.exit(main())
    except KeyboardInterrupt:
        print("\n\n⚠️  Operation interrupted by user")
        sys.exit(130)
    except Exception as e:
        print(f"\n\n❌ Fatal error: {e}")
        import traceback
        traceback.print_exc()
        sys.exit(1)
