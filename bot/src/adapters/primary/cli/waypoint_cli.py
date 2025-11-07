"""
Waypoint CLI commands

Provides commands for querying waypoints with lazy-loading.
Automatically fetches from API if cache is empty or stale (when --agent or --player-id provided).
"""
import argparse
import asyncio

from configuration.container import get_mediator
from application.waypoints.queries.list_waypoints import ListWaypointsQuery
from .player_selector import get_player_id_from_args, PlayerSelectionError


def list_waypoints_command(args: argparse.Namespace) -> int:
    """
    Handle list waypoints command

    Displays waypoints for a system with optional filters.
    Lazy-loads from API if cache is stale and --agent or --player-id is provided.

    Args:
        args: Command arguments with system, optional trait filter, fuel filter, player_id/agent

    Returns:
        0 on success, 1 on error
    """
    try:
        mediator = get_mediator()

        # Determine player_id (optional - allows cache-only mode if not provided)
        player_id = None
        if hasattr(args, 'agent') or hasattr(args, 'player_id'):
            try:
                player_id = get_player_id_from_args(args)
            except PlayerSelectionError:
                # No player specified - cache-only mode
                pass

        # Build query with optional filters and player_id
        query = ListWaypointsQuery(
            system_symbol=args.system,
            trait_filter=args.trait if hasattr(args, 'trait') and args.trait else None,
            has_fuel=args.has_fuel if hasattr(args, 'has_fuel') and args.has_fuel else None,
            player_id=player_id
        )

        # Send query via mediator
        waypoints = asyncio.run(mediator.send_async(query))

        # Display results
        if not waypoints:
            print(f"No waypoints found in system {args.system}")
            if args.trait:
                print(f"  (with trait filter: {args.trait})")
            if args.has_fuel:
                print("  (with fuel filter)")
            if player_id is None:
                print("\nTip: Provide --agent or --player-id to fetch from API if cache is empty")
            return 0

        print(f"\nWaypoints in {args.system} ({len(waypoints)}):")
        print("=" * 80)

        for waypoint in waypoints:
            # Format traits
            traits_str = ", ".join(waypoint.traits) if waypoint.traits else "None"

            # Format type
            type_str = waypoint.waypoint_type if waypoint.waypoint_type else "Unknown"

            # Display waypoint
            print(f"\n  {waypoint.symbol}")
            print(f"    Type:   {type_str}")
            print(f"    Traits: {traits_str}")
            if waypoint.has_fuel:
                print("    Fuel:   Available")

        return 0

    except Exception as e:
        print(f"❌ Error: {e}")
        return 1


def setup_waypoint_commands(subparsers):
    """
    Setup waypoint CLI command structure

    Creates 'waypoint' command group with:
    - list: Query cached waypoints with optional filters

    Command structure:
    - spacetraders waypoint list --system X1-HZ85
    - spacetraders waypoint list --system X1-HZ85 --trait MARKETPLACE
    - spacetraders waypoint list --system X1-HZ85 --has-fuel

    Args:
        subparsers: Argparse subparsers to add commands to
    """
    # Waypoint command group
    waypoint_parser = subparsers.add_parser(
        "waypoint",
        help="Waypoint cache query commands"
    )
    waypoint_subparsers = waypoint_parser.add_subparsers(dest="waypoint_command")

    # List waypoints command
    list_parser = waypoint_subparsers.add_parser(
        "list",
        help="List waypoints in a system with lazy-loading"
    )
    list_parser.add_argument(
        "--system",
        required=True,
        help="System symbol (e.g., X1-HZ85)"
    )
    list_parser.add_argument(
        "--trait",
        help="Filter by trait (e.g., MARKETPLACE, SHIPYARD)"
    )
    list_parser.add_argument(
        "--has-fuel",
        action="store_true",
        help="Filter waypoints with fuel available"
    )
    list_parser.add_argument(
        "--agent",
        help="Agent symbol (for API lazy-loading)"
    )
    list_parser.add_argument(
        "--player-id",
        type=int,
        help="Player ID (for API lazy-loading)"
    )
    list_parser.set_defaults(func=list_waypoints_command)
