#!/usr/bin/env python3
"""
Assignment operations - Ship allocation management
"""

from spacetraders_bot.core.ship_assignment_repository import AssignmentManager


def assignment_list_operation(args):
    """List all ship assignments"""
    if not hasattr(args, 'player_id') or not args.player_id:
        print("❌ --player-id required")
        return 1

    manager = AssignmentManager(player_id=args.player_id)
    assignments = manager.list_all(include_stale=getattr(args, 'include_stale', False))

    if not assignments:
        print("\n📋 No ship assignments")
        return 0

    print(f"\n{'='*100}")
    print(f"{'SHIP ASSIGNMENTS':<100}")
    print(f"{'='*100}")
    print(f"{'SHIP':<20} {'STATUS':<12} {'OPERATOR':<25} {'DAEMON':<25} {'OPERATION':<15}")
    print("-" * 100)

    for ship, data in sorted(assignments.items()):
        status = data.get('status', 'unknown')
        operator = data.get('assigned_to', 'none')
        daemon_id = data.get('daemon_id', 'none')
        operation = data.get('operation', 'none')

        # Status icon
        if status == 'active':
            status_icon = "✅"
        elif status == 'idle':
            status_icon = "⚪"
        elif status == 'stale':
            status_icon = "⚠️"
        else:
            status_icon = "❓"

        print(f"{ship:<20} {status_icon} {status:<10} {operator or 'none':<25} "
              f"{daemon_id or 'none':<25} {operation or 'none':<15}")

    print("=" * 100)
    return 0


def assignment_assign_operation(args):
    """Assign ship to operation"""
    if not hasattr(args, 'player_id') or not args.player_id:
        print("❌ --player-id required")
        return 1

    manager = AssignmentManager(player_id=args.player_id)

    metadata = {}
    if hasattr(args, 'duration') and args.duration:
        metadata['duration'] = args.duration

    success = manager.assign(
        args.ship,
        args.operator,
        args.daemon_id,
        args.operation_type,
        metadata=metadata
    )

    return 0 if success else 1


def assignment_release_operation(args):
    """Release ship from assignment"""
    if not hasattr(args, 'player_id') or not args.player_id:
        print("❌ --player-id required")
        return 1

    manager = AssignmentManager(player_id=args.player_id)

    reason = getattr(args, 'reason', 'manual_release')
    success = manager.release(args.ship, reason=reason)

    return 0 if success else 1


def assignment_available_operation(args):
    """Check if ship is available"""
    if not hasattr(args, 'player_id') or not args.player_id:
        print("❌ --player-id required")
        return 1

    manager = AssignmentManager(player_id=args.player_id)

    if manager.is_available(args.ship):
        print(f"✅ {args.ship} is available")
        return 0
    else:
        assignment = manager.get_assignment(args.ship)
        if assignment:
            operator = assignment.get('assigned_to', 'unknown')
            daemon_id = assignment.get('daemon_id', 'unknown')
            operation = assignment.get('operation', 'unknown')

            print(f"❌ {args.ship} is currently assigned:")
            print(f"   Operator: {operator}")
            print(f"   Daemon: {daemon_id}")
            print(f"   Operation: {operation}")
            print(f"   Assigned at: {assignment.get('assigned_at', 'unknown')}")
        else:
            print(f"❓ {args.ship} status unknown")

        return 1


def assignment_find_operation(args):
    """Find available ships"""
    if not hasattr(args, 'player_id') or not args.player_id:
        print("❌ --player-id required")
        return 1

    manager = AssignmentManager(player_id=args.player_id)

    requirements = {}
    if hasattr(args, 'cargo_min') and args.cargo_min:
        requirements['cargo_min'] = args.cargo_min
    if hasattr(args, 'fuel_min') and args.fuel_min:
        requirements['fuel_min'] = args.fuel_min

    available = manager.find_available(requirements if requirements else None)

    if available:
        print(f"\n📡 Available ships ({len(available)}):")
        for ship in available:
            print(f"  • {ship}")
        print()
    else:
        print("\n❌ No ships available")

    return 0


def assignment_sync_operation(args):
    """Sync registry with daemon status"""
    if not hasattr(args, 'player_id') or not args.player_id:
        print("❌ --player-id required")
        return 1

    manager = AssignmentManager(player_id=args.player_id)

    print("\n🔄 Synchronizing ship assignments with daemon status...")
    changes = manager.sync_with_daemons()

    print(f"\n✅ Sync complete:")
    print(f"   Released (daemon stopped): {len(changes['released'])} ships")
    print(f"   Still active: {len(changes['still_active'])} ships")

    if changes['released']:
        print(f"\n   Released ships:")
        for ship in changes['released']:
            print(f"     • {ship}")

    if changes['still_active']:
        print(f"\n   Active ships:")
        for ship in changes['still_active']:
            print(f"     • {ship}")

    print()
    return 0


def assignment_reassign_operation(args):
    """Reassign ships from one operation to idle (for strategic shifts)"""
    if not hasattr(args, 'player_id') or not args.player_id:
        print("❌ --player-id required")
        return 1

    manager = AssignmentManager(player_id=args.player_id)

    ships = args.ships.split(',') if hasattr(args, 'ships') and args.ships else []

    if not ships:
        print("❌ No ships specified")
        return 1

    print(f"\n🔄 Reassigning {len(ships)} ship(s) from {args.from_operation}...")

    stop_daemons = not getattr(args, 'no_stop', False)
    timeout = getattr(args, 'timeout', 10)

    success = manager.reassign_ships(
        ships,
        args.from_operation,
        stop_daemons=stop_daemons,
        timeout=timeout
    )

    if success:
        print(f"\n✅ Reassignment complete - ships now idle and available")
        return 0
    else:
        print(f"\n⚠️  Reassignment completed with some errors")
        return 1


def assignment_status_operation(args):
    """Get detailed status for specific ship"""
    if not hasattr(args, 'player_id') or not args.player_id:
        print("❌ --player-id required")
        return 1

    manager = AssignmentManager(player_id=args.player_id)

    assignment = manager.get_assignment(args.ship)

    if not assignment:
        print(f"\n📋 {args.ship}: Not in registry (available)")
        return 0

    print(f"\n{'='*70}")
    print(f"SHIP ASSIGNMENT STATUS: {args.ship}")
    print(f"{'='*70}")

    status = assignment.get('status', 'unknown')
    operator = assignment.get('assigned_to', 'none')
    daemon_id = assignment.get('daemon_id', 'none')
    operation = assignment.get('operation', 'none')

    # Status icon
    status_icon = {
        'active': '✅ ACTIVE',
        'idle': '⚪ IDLE',
        'stale': '⚠️ STALE (daemon stopped)'
    }.get(status, '❓ UNKNOWN')

    print(f"\nStatus: {status_icon}")
    print(f"Operator: {operator or 'none'}")
    print(f"Daemon: {daemon_id or 'none'}")
    print(f"Operation: {operation or 'none'}")

    if assignment.get('assigned_at'):
        print(f"Assigned at: {assignment['assigned_at']}")

    if assignment.get('released_at'):
        print(f"Released at: {assignment['released_at']}")
        print(f"Release reason: {assignment.get('release_reason', 'unknown')}")

    metadata = assignment.get('metadata', {})
    if metadata:
        print(f"\nMetadata:")
        for key, value in metadata.items():
            print(f"  {key}: {value}")

    # Check daemon status if assigned
    if daemon_id and daemon_id != 'none':
        from daemon_manager import DaemonManager
        daemon_mgr = DaemonManager(player_id=args.player_id)

        if daemon_mgr.is_running(daemon_id):
            daemon_status = daemon_mgr.status(daemon_id)
            if daemon_status:
                print(f"\nDaemon Status:")
                print(f"  Running: ✅ Yes")
                print(f"  PID: {daemon_status['pid']}")
                print(f"  Runtime: {daemon_status['runtime_seconds']:.0f}s")
                print(f"  CPU: {daemon_status['cpu_percent']:.1f}%")
                print(f"  Memory: {daemon_status['memory_mb']:.1f} MB")
        else:
            print(f"\nDaemon Status: ❌ Not running (stale assignment)")

    print(f"{'='*70}\n")
    return 0


def assignment_init_operation(args):
    """Initialize registry with all ships from API"""
    if not hasattr(args, 'player_id') or not args.player_id:
        print("❌ --player-id required to fetch ships from API")
        return 1

    manager = AssignmentManager(player_id=args.player_id)

    print("\n🔄 Initializing ship registry from API...")

    api = manager.get_api_client()
    ships = api.list_ships()

    if not ships:
        print("❌ No ships found")
        return 1

    print(f"Found {len(ships)} ships:")

    registry = manager._load_registry()

    for ship_data in ships:
        ship_symbol = ship_data['symbol']
        print(f"  • {ship_symbol}")

        # Only add if not already in registry
        if ship_symbol not in registry:
            registry[ship_symbol] = {
                'assigned_to': None,
                'daemon_id': None,
                'operation': None,
                'status': 'idle',
                'metadata': {
                    'frame': ship_data.get('frame', {}).get('symbol'),
                    'cargo_capacity': ship_data.get('cargo', {}).get('capacity'),
                    'fuel_capacity': ship_data.get('fuel', {}).get('capacity')
                }
            }

    manager._save_registry(registry)

    print(f"\n✅ Registry initialized with {len(ships)} ships")
    return 0
