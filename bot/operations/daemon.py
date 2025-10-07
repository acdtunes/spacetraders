#!/usr/bin/env python3
"""
Daemon operations - Background process management
"""

import sys
from pathlib import Path

# Add lib directory to path
sys.path.insert(0, str(Path(__file__).parent.parent / "lib"))

from daemon_manager import DaemonManager
import json


def daemon_start_operation(args):
    """Start operation as background daemon"""
    if not hasattr(args, 'player_id') or not args.player_id:
        print("❌ --player-id required")
        return 1

    manager = DaemonManager(player_id=args.player_id)

    # Get operation name (support both daemon_operation and operation for backwards compatibility)
    operation_name = getattr(args, 'daemon_operation', getattr(args, 'operation', None))
    if not operation_name:
        print("❌ Operation name required")
        return 1

    # Build command from args with absolute paths
    import os
    import sys
    bot_script = os.path.abspath("spacetraders_bot.py")
    python_executable = sys.executable  # Use same Python as current process
    command = [python_executable, bot_script, operation_name]

    # Add operation-specific args from operation_args if provided
    if hasattr(args, 'operation_args') and args.operation_args:
        command.extend(args.operation_args)
    else:
        # Fallback to old behavior for backwards compatibility
        for arg, value in vars(args).items():
            if arg in ['operation', 'daemon_operation', 'daemon_action', 'daemon_id', 'player_id', 'operation_args']:
                continue

            if value is None or value is False:
                continue

            if value is True:
                command.append(f"--{arg.replace('_', '-')}")
            else:
                command.append(f"--{arg.replace('_', '-')}")
                command.append(str(value))

    # Add player-id
    if hasattr(args, 'player_id') and args.player_id:
        command.append('--player-id')
        command.append(str(args.player_id))

    # Try to extract ship from operation_args for validation and daemon_id generation
    ship = None
    if hasattr(args, 'operation_args') and args.operation_args:
        for i, arg in enumerate(args.operation_args):
            if arg == '--ship' and i + 1 < len(args.operation_args):
                ship = args.operation_args[i + 1]
                break

    # Check ship availability if ship is specified
    if ship:
        from assignment_manager import AssignmentManager
        assignment_manager = AssignmentManager(player_id=args.player_id)
        assignments = assignment_manager.list_all()

        for ship_symbol, assignment in assignments.items():
            if ship_symbol == ship and assignment.get('status') == 'active':
                print(f"❌ Ship {ship} is already assigned to {assignment.get('assigned_to')} (daemon: {assignment.get('daemon_id')})")
                print(f"   Release the ship first with: assignments release {ship}")
                return 1

    # Start daemon
    daemon_id = args.daemon_id or (f"{operation_name}_{ship}" if ship else f"{operation_name}")
    success = manager.start(daemon_id, command)

    # Register assignment if daemon started successfully and has a ship
    if success and ship:
        from assignment_manager import AssignmentManager
        assignment_manager = AssignmentManager(player_id=args.player_id)
        # Determine operator name based on operation
        operator_map = {
            'mine': 'mining_operator',
            'trade': 'trading_operator',
            'scout-markets': 'scout_coordinator',
            'contract': 'contract_operator',
        }
        operator = operator_map.get(operation_name, f'{operation_name}_operator')
        assignment_manager.assign(ship, operator, daemon_id, operation_name)
        print(f"✅ Registered assignment: {ship} → {operator} (daemon: {daemon_id})")

    return 0 if success else 1


def daemon_stop_operation(args):
    """Stop running daemon"""
    if not hasattr(args, 'player_id') or not args.player_id:
        print("❌ --player-id required")
        return 1

    manager = DaemonManager(player_id=args.player_id)

    # Get daemon info to find associated ship
    daemon_info = manager.status(args.daemon_id)

    success = manager.stop(args.daemon_id)

    # Release assignment if daemon stopped successfully and has a ship
    if success and daemon_info:
        # Extract ship symbol from command args if present
        command_args = daemon_info.get('command', [])
        ship = None
        if '--ship' in command_args:
            ship_index = command_args.index('--ship') + 1
            if ship_index < len(command_args):
                ship = command_args[ship_index]

        if ship:
            from assignment_manager import AssignmentManager
            assignment_manager = AssignmentManager(player_id=args.player_id)
            assignment_manager.release(ship, reason=f"Daemon {args.daemon_id} stopped")
            print(f"✅ Released assignment for {ship}")

    return 0 if success else 1


def daemon_status_operation(args):
    """Get daemon status"""
    if not hasattr(args, 'player_id') or not args.player_id:
        print("❌ --player-id required")
        return 1

    manager = DaemonManager(player_id=args.player_id)

    if hasattr(args, 'daemon_id') and args.daemon_id:
        status = manager.status(args.daemon_id)
        if status:
            # Pretty print status
            running = "✅ RUNNING" if status['is_running'] else "❌ STOPPED"
            print(f"\nDaemon: {status['daemon_id']}")
            print(f"Status: {running}")
            print(f"PID: {status['pid']}")
            print(f"Started: {status['started_at']}")

            if status['is_running']:
                print(f"Runtime: {status['runtime_seconds']:.0f}s")
                print(f"CPU: {status['cpu_percent']:.1f}%")
                print(f"Memory: {status['memory_mb']:.1f} MB")

            print(f"\nCommand: {' '.join(status['command'])}")
            print(f"Logs: {status['log_file']}")
            print(f"Errors: {status['err_file']}")
        else:
            print(f"Daemon {args.daemon_id} not found")
            return 1
    else:
        # List all daemons
        daemons = manager.list_all()
        if not daemons:
            print("No daemons running")
            return 0

        print(f"\n{'DAEMON ID':<30} {'STATUS':<10} {'PID':<8} {'CPU':<8} {'MEM':<10} {'RUNTIME':<10}")
        print("=" * 90)

        for daemon in daemons:
            status_icon = "✅" if daemon['is_running'] else "❌"
            status_text = "RUNNING" if daemon['is_running'] else "STOPPED"

            runtime_str = f"{daemon['runtime_seconds']:.0f}s" if daemon['runtime_seconds'] else "N/A"

            print(f"{daemon['daemon_id']:<30} {status_icon} {status_text:<8} "
                  f"{daemon['pid']:<8} {daemon['cpu_percent']:>6.1f}% "
                  f"{daemon['memory_mb']:>8.1f}MB {runtime_str:<10}")

    return 0


def daemon_logs_operation(args):
    """Show daemon logs"""
    if not hasattr(args, 'player_id') or not args.player_id:
        print("❌ --player-id required")
        return 1

    manager = DaemonManager(player_id=args.player_id)
    manager.tail_logs(args.daemon_id, args.lines)
    return 0


def daemon_cleanup_operation(args):
    """Clean up stopped daemons"""
    if not hasattr(args, 'player_id') or not args.player_id:
        print("❌ --player-id required")
        return 1

    manager = DaemonManager(player_id=args.player_id)
    manager.cleanup_stopped()
    return 0
