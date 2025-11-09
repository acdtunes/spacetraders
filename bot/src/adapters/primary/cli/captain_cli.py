"""Captain CLI commands for logging narrative entries"""
import argparse
import asyncio
import json
import sys
from typing import Dict, Any, List, Optional

from configuration.container import get_mediator
from application.captain.commands.log_captain_entry import LogCaptainEntryCommand, VALID_ENTRY_TYPES
from application.captain.queries.get_captain_logs import GetCaptainLogsQuery
from adapters.primary.cli.player_selector import get_player_id_from_args, PlayerSelectionError
from domain.shared.exceptions import PlayerNotFoundError


def log_captain_entry_command(args: argparse.Namespace) -> int:
    """Handle captain log command"""
    try:
        # 1. Resolve player_id
        player_id = get_player_id_from_args(args)

        # 2. Parse optional JSON fields
        event_data: Optional[Dict[str, Any]] = None
        if args.event_data:
            try:
                event_data = json.loads(args.event_data)
            except json.JSONDecodeError as e:
                print(f"❌ Error: Invalid JSON in --event-data: {e}")
                return 1

        fleet_snapshot: Optional[Dict[str, Any]] = None
        if args.fleet_snapshot:
            try:
                fleet_snapshot = json.loads(args.fleet_snapshot)
            except json.JSONDecodeError as e:
                print(f"❌ Error: Invalid JSON in --fleet-snapshot: {e}")
                return 1

        # 3. Parse tags (comma-separated string to list)
        tags: Optional[List[str]] = None
        if args.tags:
            tags = [tag.strip() for tag in args.tags.split(',')]

        # 4. Create and send command
        mediator = get_mediator()
        command = LogCaptainEntryCommand(
            player_id=player_id,
            entry_type=args.type,
            narrative=args.narrative,
            event_data=event_data,
            tags=tags,
            fleet_snapshot=fleet_snapshot
        )

        log_id = asyncio.run(mediator.send_async(command))

        print(f"✅ Captain log entry #{log_id} created successfully")
        return 0

    except PlayerSelectionError as e:
        print(f"❌ {e}")
        return 1
    except PlayerNotFoundError as e:
        print(f"❌ Error: {e}")
        return 1
    except ValueError as e:
        print(f"❌ Error: {e}")
        return 1
    except Exception as e:
        print(f"❌ Unexpected error: {e}")
        return 1


def get_captain_logs_command(args: argparse.Namespace) -> int:
    """Handle captain logs command"""
    try:
        # 1. Resolve player_id
        player_id = get_player_id_from_args(args)

        # 2. Parse tags (comma-separated string to list)
        tags: Optional[List[str]] = None
        if args.tags:
            tags = [tag.strip() for tag in args.tags.split(',')]

        # 3. Create and send query
        mediator = get_mediator()
        query = GetCaptainLogsQuery(
            player_id=player_id,
            limit=args.limit,
            entry_type=args.type if hasattr(args, 'type') and args.type else None,
            since=args.since if hasattr(args, 'since') and args.since else None,
            tags=tags
        )

        logs = asyncio.run(mediator.send_async(query))

        # 4. Display results
        if not logs:
            print("No captain logs found")
            return 0

        print(f"\n{'=' * 80}")
        print(f"Captain Logs ({len(logs)} entries)")
        print(f"{'=' * 80}\n")

        for log in logs:
            print(f"Timestamp: {log['timestamp']}")
            print(f"Entry Type: {log['entry_type']}")

            if log.get('tags'):
                try:
                    tags_list = json.loads(log['tags'])
                    if tags_list:
                        print(f"Tags: {', '.join(tags_list)}")
                except (json.JSONDecodeError, TypeError):
                    pass

            print(f"\nNarrative:")
            print(f"  {log['narrative']}\n")

            if log.get('event_data'):
                try:
                    event_data = json.loads(log['event_data'])
                    print(f"Event Data:")
                    print(json.dumps(event_data, indent=2))
                    print()
                except (json.JSONDecodeError, TypeError):
                    pass

            if log.get('fleet_snapshot'):
                try:
                    fleet_snapshot = json.loads(log['fleet_snapshot'])
                    print(f"Fleet Snapshot:")
                    print(json.dumps(fleet_snapshot, indent=2))
                    print()
                except (json.JSONDecodeError, TypeError):
                    pass

            print(f"{'-' * 80}\n")

        return 0

    except PlayerSelectionError as e:
        print(f"❌ {e}")
        return 1
    except PlayerNotFoundError as e:
        print(f"❌ Error: {e}")
        return 1
    except Exception as e:
        print(f"❌ Unexpected error: {e}")
        return 1


def setup_captain_commands(subparsers):
    """Setup captain CLI commands"""
    captain_parser = subparsers.add_parser(
        "captain",
        help="Captain narrative logging for mission continuity"
    )
    captain_subparsers = captain_parser.add_subparsers(dest="captain_command")

    # Log command
    log_parser = captain_subparsers.add_parser(
        "log",
        help="Log a captain narrative entry"
    )
    log_parser.add_argument("--player-id", type=int, help="Player ID")
    log_parser.add_argument("--agent", help="Agent symbol (alternative to player-id)")
    log_parser.add_argument(
        "--type",
        required=True,
        choices=sorted(VALID_ENTRY_TYPES),
        help="Entry type"
    )
    log_parser.add_argument(
        "--narrative",
        required=True,
        help="The narrative prose from captain"
    )
    log_parser.add_argument(
        "--event-data",
        help="Optional JSON string of event data"
    )
    log_parser.add_argument(
        "--tags",
        help="Comma-separated tags (e.g., 'mining,iron_ore')"
    )
    log_parser.add_argument(
        "--fleet-snapshot",
        help="Optional JSON string of fleet state"
    )
    log_parser.set_defaults(func=log_captain_entry_command)

    # Logs command
    logs_parser = captain_subparsers.add_parser(
        "logs",
        help="Retrieve captain narrative logs"
    )
    logs_parser.add_argument("--player-id", type=int, help="Player ID")
    logs_parser.add_argument("--agent", help="Agent symbol (alternative to player-id)")
    logs_parser.add_argument(
        "--limit",
        type=int,
        default=100,
        help="Maximum number of entries to return (default: 100)"
    )
    logs_parser.add_argument(
        "--type",
        choices=sorted(VALID_ENTRY_TYPES),
        help="Filter by entry type"
    )
    logs_parser.add_argument(
        "--since",
        help="ISO timestamp - only logs after this time (e.g., '2025-11-08T00:00:00')"
    )
    logs_parser.add_argument(
        "--tags",
        help="Comma-separated tags to filter by (e.g., 'mining,iron_ore')"
    )
    logs_parser.set_defaults(func=get_captain_logs_command)
