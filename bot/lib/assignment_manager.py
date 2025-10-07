#!/usr/bin/env python3
"""
Assignment Manager - Ship allocation and coordination

Manages ship assignments to operations to prevent conflicts
and enable strategic reassignment.
"""

import sys
from pathlib import Path
from typing import Optional, Dict, List

# Import daemon manager to check daemon status
sys.path.insert(0, str(Path(__file__).parent))
from daemon_manager import DaemonManager
from database import get_database


class AssignmentManager:
    """
    Manages ship-to-operation assignments using SQLite database (multi-player)

    Usage:
        # Initialize with agent symbol
        manager = AssignmentManager(agent_symbol="CMDR_AC_2025", token="YOUR_TOKEN")

        # Or with player_id if already registered
        manager = AssignmentManager(player_id=1)

        # Assign ship
        manager.assign("SHIP-1", "trading_operator", "trader-ship1", "trade")

        # Check availability
        if manager.is_available("SHIP-1"):
            print("Ship available")

        # Release ship
        manager.release("SHIP-1")
    """

    def __init__(self, agent_symbol: Optional[str] = None, token: Optional[str] = None,
                 player_id: Optional[int] = None, db_path: str = "data/spacetraders.db"):
        """
        Initialize assignment manager for a specific player

        Args:
            agent_symbol: Agent symbol (e.g., "CMDR_AC_2025")
            token: API token (required if agent_symbol provided)
            player_id: Player ID (if already registered)
            db_path: Path to SQLite database

        Note: Must provide either (agent_symbol + token) OR player_id
        """
        self.db = get_database(db_path)

        if player_id is not None:
            # Use existing player - retrieve token from database
            with self.db.connection() as conn:
                player = self.db.get_player_by_id(conn, player_id)
                if not player:
                    raise ValueError(f"Player ID {player_id} not found")
                self.player_id = player_id
                self.agent_symbol = player['agent_symbol']
                self.token = player['token']  # Auto-retrieve token
        elif agent_symbol and token:
            # Create or get player
            with self.db.transaction() as conn:
                self.player_id = self.db.create_player(conn, agent_symbol, token)
                self.agent_symbol = agent_symbol
                self.token = token
        else:
            raise ValueError("Must provide either (agent_symbol + token) OR player_id")

        self.db_path = db_path
        self.daemon_manager = DaemonManager(player_id=self.player_id, db_path=db_path)
        self._api_client = None  # Lazy-loaded API client

    def get_api_client(self):
        """
        Get API client with stored token (lazy-loaded)

        Returns:
            APIClient instance configured with player's token

        Usage:
            api = manager.get_api_client()
            ship = ShipController(api, "SHIP-1")
        """
        if self._api_client is None:
            # Import here to avoid circular dependencies
            from api_client import APIClient
            self._api_client = APIClient(token=self.token)
        return self._api_client

    @property
    def api(self):
        """Convenient property access to API client"""
        return self.get_api_client()

    def assign(self, ship: str, operator: str, daemon_id: str, operation: str,
               metadata: Optional[Dict] = None) -> bool:
        """
        Assign ship to operation

        Args:
            ship: Ship symbol (e.g., "CMDR_AC_2025-1")
            operator: Operator name (e.g., "trading_operator")
            daemon_id: Associated daemon ID
            operation: Operation type (e.g., "trade", "mine")
            metadata: Optional additional data

        Returns:
            True if assigned successfully
        """
        with self.db.transaction() as conn:
            # Check if already assigned
            assignment = self.db.get_ship_assignment(conn, self.player_id, ship)

            if assignment and assignment.get('status') == 'active':
                current_operator = assignment.get('assigned_to')
                current_daemon = assignment.get('daemon_id')

                # Check if daemon actually running
                if self.daemon_manager.is_running(current_daemon):
                    print(f"❌ Ship {ship} already assigned to {current_operator} (daemon: {current_daemon})")
                    return False
                else:
                    # Daemon not running, clean up stale assignment
                    print(f"⚠️  Clearing stale assignment for {ship}")

            # Assign ship (atomic)
            success = self.db.assign_ship(conn, self.player_id, ship, operator, daemon_id, operation, metadata)

            if success:
                print(f"✅ Assigned {ship} to {operator} (operation: {operation}, daemon: {daemon_id})")

            return success

    def release(self, ship: str, reason: str = "operation_complete") -> bool:
        """
        Release ship from current assignment

        Args:
            ship: Ship symbol
            reason: Reason for release

        Returns:
            True if released successfully
        """
        with self.db.transaction() as conn:
            assignment = self.db.get_ship_assignment(conn, self.player_id, ship)

            if not assignment:
                print(f"Ship {ship} not in registry")
                return False

            old_operator = assignment.get('assigned_to')
            old_daemon = assignment.get('daemon_id')

            # Release ship
            success = self.db.release_ship(conn, self.player_id, ship, reason)

            if success:
                print(f"✅ Released {ship} from {old_operator} (daemon: {old_daemon})")

            return success

    def is_available(self, ship: str) -> bool:
        """Check if ship is available for assignment"""
        with self.db.connection() as conn:
            assignment = self.db.get_ship_assignment(conn, self.player_id, ship)

            if not assignment:
                return True  # Not in registry = available

            # Check if marked idle
            if assignment.get('status') == 'idle':
                return True

            # Check if daemon actually running
            daemon_id = assignment.get('daemon_id')
            if daemon_id and not self.daemon_manager.is_running(daemon_id):
                # Stale assignment, mark as available
                return True

            return False

    def get_assignment(self, ship: str) -> Optional[Dict]:
        """Get current assignment for ship"""
        with self.db.connection() as conn:
            return self.db.get_ship_assignment(conn, self.player_id, ship)

    def list_all(self, include_stale: bool = False) -> Dict:
        """
        List all ship assignments

        Args:
            include_stale: Include assignments with stopped daemons

        Returns:
            Dictionary of ship assignments
        """
        with self.db.connection() as conn:
            assignments = self.db.list_ship_assignments(conn, self.player_id)
            result_registry = {}

            # Always check daemon status and mark stale
            for assignment in assignments:
                ship = assignment['ship_symbol']
                daemon_id = assignment.get('daemon_id')

                if daemon_id:
                    if not self.daemon_manager.is_running(daemon_id):
                        # Mark stale
                        assignment['status'] = 'stale'
                        assignment['stale_note'] = 'Daemon not running'

                result_registry[ship] = assignment

            return result_registry

    def find_available(self, requirements: Optional[Dict] = None) -> List[str]:
        """
        Find available ships matching requirements

        Args:
            requirements: Optional dict with cargo_min, fuel_min, etc.

        Returns:
            List of available ship symbols
        """
        with self.db.connection() as conn:
            all_ships = [a['ship_symbol'] for a in self.db.list_ship_assignments(conn, self.player_id)]
            available = []

            for ship in all_ships:
                if self.is_available(ship):
                    # TODO: Check requirements against ship capabilities if provided
                    available.append(ship)

            return available

    def sync_with_daemons(self) -> Dict:
        """
        Synchronize registry with actual daemon status

        Returns:
            Summary of changes made
        """
        changes = {
            'released': [],
            'marked_stale': [],
            'still_active': []
        }

        with self.db.connection() as conn:
            assignments = self.db.list_ship_assignments(conn, self.player_id, status='active')

            for assignment in assignments:
                ship = assignment['ship_symbol']
                daemon_id = assignment.get('daemon_id')

                if not daemon_id:
                    continue

                # Check if daemon still running
                if not self.daemon_manager.is_running(daemon_id):
                    # Release ship
                    print(f"⚠️  Daemon {daemon_id} not running, releasing {ship}")
                    self.release(ship, reason="daemon_stopped")
                    changes['released'].append(ship)
                else:
                    changes['still_active'].append(ship)

        return changes

    def reassign_ships(self, ships: List[str], from_operation: str,
                      stop_daemons: bool = True, timeout: int = 10) -> bool:
        """
        Reassign ships from one operation (strategic shift)

        Args:
            ships: List of ship symbols to reassign
            from_operation: Operation to reassign from
            stop_daemons: Whether to stop associated daemons
            timeout: Timeout for daemon shutdown

        Returns:
            True if all reassignments successful
        """
        success = True

        for ship in ships:
            assignment = self.get_assignment(ship)

            if not assignment:
                print(f"⚠️  Ship {ship} not in registry, skipping")
                continue

            current_operation = assignment.get('operation')

            if current_operation != from_operation:
                print(f"⚠️  Ship {ship} not assigned to {from_operation} (current: {current_operation})")
                continue

            daemon_id = assignment.get('daemon_id')

            # Stop daemon if requested
            if stop_daemons and daemon_id:
                print(f"Stopping daemon {daemon_id} for {ship}...")
                if not self.daemon_manager.stop(daemon_id, timeout=timeout):
                    print(f"❌ Failed to stop daemon {daemon_id}")
                    success = False
                    continue

            # Release ship
            self.release(ship, reason=f"reassignment_from_{from_operation}")

        return success


if __name__ == "__main__":  # pragma: no cover
    # CLI for assignment management
    import argparse

    parser = argparse.ArgumentParser(description="Ship Assignment Manager CLI")
    subparsers = parser.add_subparsers(dest='action', help='Action to perform')

    # Assign ship
    assign_parser = subparsers.add_parser('assign', help='Assign ship to operation')
    assign_parser.add_argument('ship', help='Ship symbol')
    assign_parser.add_argument('operator', help='Operator name')
    assign_parser.add_argument('daemon_id', help='Daemon ID')
    assign_parser.add_argument('operation', help='Operation type')

    # Release ship
    release_parser = subparsers.add_parser('release', help='Release ship')
    release_parser.add_argument('ship', help='Ship symbol')
    release_parser.add_argument('--reason', default='manual_release', help='Release reason')

    # Check availability
    available_parser = subparsers.add_parser('available', help='Check if ship available')
    available_parser.add_argument('ship', help='Ship symbol')

    # List all
    list_parser = subparsers.add_parser('list', help='List all assignments')
    list_parser.add_argument('--include-stale', action='store_true', help='Include stale assignments')

    # Sync with daemons
    sync_parser = subparsers.add_parser('sync', help='Sync registry with daemon status')

    # Find available ships
    find_parser = subparsers.add_parser('find', help='Find available ships')

    args = parser.parse_args()

    manager = AssignmentManager()

    if args.action == 'assign':
        manager.assign(args.ship, args.operator, args.daemon_id, args.operation)

    elif args.action == 'release':
        manager.release(args.ship, args.reason)

    elif args.action == 'available':
        if manager.is_available(args.ship):
            print(f"✅ {args.ship} is available")
        else:
            assignment = manager.get_assignment(args.ship)
            print(f"❌ {args.ship} is assigned to {assignment.get('assigned_to')} (daemon: {assignment.get('daemon_id')})")

    elif args.action == 'list':
        assignments = manager.list_all(include_stale=args.include_stale)

        if not assignments:
            print("No ship assignments")
        else:
            print(f"\n{'SHIP':<20} {'STATUS':<10} {'OPERATOR':<25} {'DAEMON':<25} {'OPERATION':<15}")
            print("=" * 100)

            for ship, data in assignments.items():
                status = data.get('status', 'unknown')
                operator = data.get('assigned_to', 'none')
                daemon_id = data.get('daemon_id', 'none')
                operation = data.get('operation', 'none')

                status_icon = "✅" if status == 'active' else "⚪" if status == 'idle' else "⚠️"

                print(f"{ship:<20} {status_icon} {status:<8} {operator or 'none':<25} "
                      f"{daemon_id or 'none':<25} {operation or 'none':<15}")

    elif args.action == 'sync':
        changes = manager.sync_with_daemons()
        print(f"\nSync complete:")
        print(f"  Released: {len(changes['released'])} ships")
        print(f"  Still active: {len(changes['still_active'])} ships")
        if changes['released']:
            print(f"  Released ships: {', '.join(changes['released'])}")

    elif args.action == 'find':
        available = manager.find_available()
        if available:
            print(f"Available ships: {', '.join(available)}")
        else:
            print("No ships available")

    else:
        parser.print_help()
