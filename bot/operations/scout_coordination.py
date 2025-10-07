#!/usr/bin/env python3
"""
Scout Coordinator operations: Multi-ship continuous market scouting
"""

import sys
import json
from pathlib import Path

# Add lib directory to path
sys.path.insert(0, str(Path(__file__).parent.parent / "lib"))

from scout_coordinator import ScoutCoordinator
from .common import setup_logging
from database import get_database


def coordinator_start_operation(args):
    """
    Start multi-ship continuous market scouting

    Partitions markets geographically and assigns non-overlapping subtours
    to each ship. Monitors and restarts daemons automatically.
    """
    setup_logging('SCOUT-COORDINATOR', 'coordinator', args.log_level)

    print("=" * 70)
    print("MULTI-SHIP SCOUT COORDINATOR - START")
    print("=" * 70)

    # Parse ships
    ships = [s.strip() for s in args.ships.split(',')]

    print(f"System: {args.system}")
    print(f"Ships: {len(ships)} - {', '.join(ships)}")
    print(f"Algorithm: {args.algorithm.upper()}")
    print()

    # Get token from database
    db = get_database()
    with db.connection() as conn:
        player = db.get_player_by_id(conn, args.player_id)
        if not player:
            print(f"❌ Player ID {args.player_id} not found")
            return 1
        token = player['token']

    try:
        # Initialize coordinator
        coordinator = ScoutCoordinator(
            system=args.system,
            ships=ships,
            token=token,
            player_id=args.player_id,
            algorithm=args.algorithm
        )

        # Save configuration
        coordinator.save_config()

        # Partition and start scouts
        coordinator.partition_and_start()

        # Monitor and restart (blocks until stopped)
        coordinator.monitor_and_restart()

        # Stop all on exit
        coordinator.stop_all()

        return 0

    except KeyboardInterrupt:
        print("\n⚠️  Interrupted by user")
        if 'coordinator' in locals():
            coordinator.stop_all()
        return 0
    except Exception as e:
        print(f"❌ Error: {e}")
        return 1


def coordinator_add_ship_operation(args):
    """
    Add ship to ongoing scout operation

    Triggers graceful reconfiguration: waits for current tours to complete,
    then repartitions markets and starts new subtours.
    """
    setup_logging('SCOUT-COORDINATOR', 'add-ship', args.log_level)

    print("=" * 70)
    print("SCOUT COORDINATOR - ADD SHIP")
    print("=" * 70)

    config_file = f"agents/scout_config_{args.system}.json"
    config_path = Path(config_file)

    if not config_path.exists():
        print(f"❌ No coordinator config found for {args.system}")
        print(f"   Start coordinator first with: scout-coordinator start")
        return 1

    try:
        # Load current config
        with open(config_path, 'r') as f:
            config = json.load(f)

        current_ships = set(config.get('ships', []))

        if args.ship in current_ships:
            print(f"⚠️  {args.ship} is already in the scout operation")
            return 1

        # Add ship and request reconfiguration
        current_ships.add(args.ship)
        config['ships'] = sorted(list(current_ships))
        config['reconfigure'] = True

        with open(config_path, 'w') as f:
            json.dump(config, f, indent=2)

        print(f"✅ Added {args.ship} to scout operation")
        print(f"   Ships now: {', '.join(config['ships'])}")
        print(f"   Coordinator will reconfigure on next check (~30s)")

        return 0

    except Exception as e:
        print(f"❌ Error: {e}")
        return 1


def coordinator_remove_ship_operation(args):
    """
    Remove ship from ongoing scout operation

    Triggers graceful reconfiguration: waits for current tours to complete,
    stops the removed ship's daemon, then repartitions remaining ships.
    """
    setup_logging('SCOUT-COORDINATOR', 'remove-ship', args.log_level)

    print("=" * 70)
    print("SCOUT COORDINATOR - REMOVE SHIP")
    print("=" * 70)

    config_file = f"agents/scout_config_{args.system}.json"
    config_path = Path(config_file)

    if not config_path.exists():
        print(f"❌ No coordinator config found for {args.system}")
        return 1

    try:
        # Load current config
        with open(config_path, 'r') as f:
            config = json.load(f)

        current_ships = set(config.get('ships', []))

        if args.ship not in current_ships:
            print(f"⚠️  {args.ship} is not in the scout operation")
            return 1

        # Remove ship and request reconfiguration
        current_ships.remove(args.ship)

        if not current_ships:
            print(f"❌ Cannot remove last ship from operation")
            print(f"   Use 'scout-coordinator stop' to stop the operation")
            return 1

        config['ships'] = sorted(list(current_ships))
        config['reconfigure'] = True

        with open(config_path, 'w') as f:
            json.dump(config, f, indent=2)

        print(f"✅ Removed {args.ship} from scout operation")
        print(f"   Ships now: {', '.join(config['ships'])}")
        print(f"   Coordinator will reconfigure on next check (~30s)")

        return 0

    except Exception as e:
        print(f"❌ Error: {e}")
        return 1


def coordinator_stop_operation(args):
    """
    Stop the scout coordinator and all scout daemons

    Stops monitoring and terminates all scout ship daemons.
    """
    setup_logging('SCOUT-COORDINATOR', 'stop', args.log_level)

    print("=" * 70)
    print("SCOUT COORDINATOR - STOP")
    print("=" * 70)

    config_file = f"agents/scout_config_{args.system}.json"
    config_path = Path(config_file)

    if not config_path.exists():
        print(f"⚠️  No coordinator running for {args.system}")
        return 0

    try:
        # Load config to get ship list
        with open(config_path, 'r') as f:
            config = json.load(f)

        ships = config.get('ships', [])

        # Stop all scout daemons
        from daemon_manager import DaemonManager
        daemon_manager = DaemonManager()

        print(f"Stopping {len(ships)} scout daemon(s)...")

        for ship in ships:
            daemon_id = f"scout-{ship.split('-')[-1]}"
            if daemon_manager.is_running(daemon_id):
                print(f"   Stopping {daemon_id}...")
                daemon_manager.stop(daemon_id)

        # Remove config file
        config_path.unlink()

        print(f"\n✅ Scout coordinator stopped")

        return 0

    except Exception as e:
        print(f"❌ Error: {e}")
        return 1


def coordinator_status_operation(args):
    """
    Show status of scout coordinator

    Displays current ships, daemon status, and configuration.
    """
    setup_logging('SCOUT-COORDINATOR', 'status', args.log_level)

    config_file = f"agents/scout_config_{args.system}.json"
    config_path = Path(config_file)

    if not config_path.exists():
        print(f"⚠️  No coordinator running for {args.system}")
        return 0

    try:
        # Load config
        with open(config_path, 'r') as f:
            config = json.load(f)

        ships = config.get('ships', [])
        algorithm = config.get('algorithm', 'greedy')

        print("=" * 70)
        print(f"SCOUT COORDINATOR STATUS - {args.system}")
        print("=" * 70)
        print(f"Algorithm: {algorithm.upper()}")
        print(f"Ships: {len(ships)}")
        print()

        # Check daemon status
        from daemon_manager import DaemonManager
        daemon_manager = DaemonManager()

        for ship in ships:
            daemon_id = f"scout-{ship.split('-')[-1]}"
            running = daemon_manager.is_running(daemon_id)
            status_str = "🟢 RUNNING" if running else "🔴 STOPPED"
            print(f"  {ship}: {status_str} (daemon: {daemon_id})")

        print()
        print(f"Config: {config_path}")

        return 0

    except Exception as e:
        print(f"❌ Error: {e}")
        return 1
