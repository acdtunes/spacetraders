"""CLI commands for daemon operations"""
import argparse

from ..daemon.daemon_client import DaemonClient


def daemon_server_command(args: argparse.Namespace) -> int:
    """Start daemon server"""
    from ..daemon.daemon_server import main as daemon_main
    daemon_main()
    return 0


def daemon_list_command(args: argparse.Namespace) -> int:
    """List containers"""
    client = DaemonClient()
    try:
        result = client.list_containers()

        containers = result.get("containers", [])
        if not containers:
            print("No containers running")
            return 0

        print(f"Containers ({len(containers)}):")
        for c in containers:
            print(f"  [{c['container_id']}] {c['type']} - {c['status']}")
        return 0
    except Exception as e:
        print(f"❌ Error: {e}")
        return 1


def daemon_inspect_command(args: argparse.Namespace) -> int:
    """Inspect container"""
    import json
    client = DaemonClient()
    try:
        result = client.inspect_container(args.container_id)

        # If --json flag is set, output as JSON
        if hasattr(args, 'json') and args.json:
            print(json.dumps(result, indent=2, ensure_ascii=False))
            return 0

        # Otherwise, output human-readable format
        print(f"Container: {result['container_id']}")
        print(f"Status: {result['status']}")
        print(f"Type: {result['type']}")
        print(f"Player ID: {result['player_id']}")
        print(f"Iteration: {result.get('iteration', 0)}")
        print(f"Restart count: {result.get('restart_count', 0)}")
        print(f"Started: {result.get('started_at', 'N/A')}")
        if result.get('stopped_at'):
            print(f"Stopped: {result['stopped_at']}")
        if result.get('exit_code') is not None:
            print(f"Exit code: {result['exit_code']}")

        # Print logs if present
        if result.get('logs'):
            print(f"\nLogs ({len(result['logs'])} entries):")
            for log in result['logs'][:10]:  # Show first 10
                print(f"  [{log['level']}] {log['timestamp']}: {log['message'][:100]}")
            if len(result['logs']) > 10:
                print(f"  ... and {len(result['logs']) - 10} more")

        return 0
    except Exception as e:
        if hasattr(args, 'json') and args.json:
            print(json.dumps({"error": str(e)}, indent=2))
        else:
            print(f"❌ Error: {e}")
        return 1


def daemon_stop_command(args: argparse.Namespace) -> int:
    """Stop container"""
    client = DaemonClient()
    try:
        client.stop_container(args.container_id)
        print(f"✅ Stopped {args.container_id}")
        return 0
    except Exception as e:
        print(f"❌ Error: {e}")
        return 1


def daemon_remove_command(args: argparse.Namespace) -> int:
    """Remove container"""
    client = DaemonClient()
    try:
        client.remove_container(args.container_id)
        print(f"✅ Removed {args.container_id}")
        return 0
    except Exception as e:
        print(f"❌ Error: {e}")
        return 1


def daemon_logs_command(args: argparse.Namespace) -> int:
    """Get container logs from database"""
    import json
    client = DaemonClient()
    try:
        result = client.get_container_logs(
            container_id=args.container_id,
            player_id=args.player_id,
            limit=args.limit if hasattr(args, 'limit') else 100,
            level=args.level if hasattr(args, 'level') else None
        )

        # If --json flag is set, output as JSON
        if hasattr(args, 'json') and args.json:
            print(json.dumps(result, indent=2, ensure_ascii=False))
            return 0

        # Otherwise, output human-readable format
        logs = result.get('logs', [])
        if not logs:
            print("No logs found")
            return 0

        print(f"Logs for container {args.container_id} ({len(logs)} entries):")
        for log in logs:
            print(f"[{log['level']}] {log['timestamp']}: {log['message']}")

        return 0
    except Exception as e:
        if hasattr(args, 'json') and args.json:
            print(json.dumps({"error": str(e)}, indent=2))
        else:
            print(f"❌ Error: {e}")
        return 1


def setup_daemon_commands(subparsers):
    """Setup daemon CLI commands

    Args:
        subparsers: Subparsers from main argument parser
    """
    daemon = subparsers.add_parser("daemon", help="Daemon operations")
    daemon_sub = daemon.add_subparsers(dest="daemon_command")

    # Server command
    server = daemon_sub.add_parser("server", help="Start daemon server")
    server.set_defaults(func=daemon_server_command)

    # List command
    list_cmd = daemon_sub.add_parser("list", help="List containers")
    list_cmd.set_defaults(func=daemon_list_command)

    # Inspect command
    inspect = daemon_sub.add_parser("inspect", help="Inspect container")
    inspect.add_argument("--container-id", required=True, help="Container ID to inspect")
    inspect.add_argument("--json", action="store_true", help="Output as JSON")
    inspect.set_defaults(func=daemon_inspect_command)

    # Stop command
    stop = daemon_sub.add_parser("stop", help="Stop container")
    stop.add_argument("--container-id", required=True, help="Container ID to stop")
    stop.set_defaults(func=daemon_stop_command)

    # Remove command
    remove = daemon_sub.add_parser("remove", help="Remove stopped container")
    remove.add_argument("--container-id", required=True, help="Container ID to remove")
    remove.set_defaults(func=daemon_remove_command)

    # Logs command
    logs = daemon_sub.add_parser("logs", help="Get container logs")
    logs.add_argument("--container-id", required=True, help="Container ID")
    logs.add_argument("--player-id", type=int, required=True, help="Player ID")
    logs.add_argument("--limit", type=int, default=100, help="Maximum logs to retrieve (default 100)")
    logs.add_argument("--level", choices=["INFO", "WARNING", "ERROR", "DEBUG"], help="Filter by log level")
    logs.add_argument("--json", action="store_true", help="Output as JSON")
    logs.set_defaults(func=daemon_logs_command)
