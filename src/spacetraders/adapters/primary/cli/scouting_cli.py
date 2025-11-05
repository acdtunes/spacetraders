import argparse
import asyncio
from typing import List

from ....configuration.container import get_daemon_client, get_mediator
from ....application.scouting.commands.scout_markets import ScoutMarketsCommand
from .player_selector import get_player_id_from_args, PlayerSelectionError


def scout_markets_command(args: argparse.Namespace) -> int:
    """
    Handle scout markets command - partitions markets across multiple ships

    Creates background containers for each ship with optimally partitioned markets:
    - Uses VRP solver to partition markets across ships
    - Each market is assigned to exactly one ship (disjoint tours)
    - Creates N containers (one per ship with assigned markets)

    Args:
        args: Command arguments with ships, system, markets, iterations, etc.

    Returns:
        0 on success, 1 on error

    Example:
        spacetraders scout markets --ships SCOUT-1,SCOUT-2,SCOUT-3 --system X1-GZ7 --markets X1-GZ7-A1,X1-GZ7-B2,X1-GZ7-C3,X1-GZ7-D4 --iterations 10
    """
    try:
        # Determine player_id using intelligent selection
        player_id = get_player_id_from_args(args)

        # Parse ship and market lists
        ships = [s.strip() for s in args.ships.split(',')]
        markets = [m.strip() for m in args.markets.split(',')]

        if len(ships) == 0:
            print("❌ Error: At least one ship is required")
            return 1

        if len(markets) == 0:
            print("❌ Error: At least one market is required")
            return 1

        # Create ScoutMarketsCommand
        command = ScoutMarketsCommand(
            ship_symbols=ships,
            player_id=player_id,
            system=args.system,
            markets=markets,
            iterations=args.iterations if hasattr(args, 'iterations') else 1,
            return_to_start=args.return_to_start
        )

        # Execute via mediator
        mediator = get_mediator()
        result = asyncio.run(mediator.send_async(command))

        # Print results
        print(f"✅ Scout markets deployed: {len(result.container_ids)} containers created")
        print(f"   System: {args.system}")
        print(f"   Total markets: {len(markets)}")
        print(f"   Ships: {len(ships)}")
        print(f"   Iterations: {args.iterations if hasattr(args, 'iterations') else 1}")
        print(f"   Return to start: {args.return_to_start}")
        print()
        print("Market assignments:")
        for ship, assigned_markets in result.assignments.items():
            print(f"  {ship}: {len(assigned_markets)} markets - {', '.join(assigned_markets)}")
        print()
        print("Container IDs:")
        for container_id in result.container_ids:
            print(f"  {container_id}")
        print(f"\nUse 'spacetraders daemon logs <container_id>' to view progress")

        return 0

    except PlayerSelectionError as e:
        print(f"❌ {e}")
        return 1
    except Exception as e:
        print(f"❌ Error: {e}")
        import traceback
        traceback.print_exc()
        return 1


def setup_scouting_commands(subparsers):
    """
    Setup scouting CLI command structure

    Creates 'scout' command group with markets subcommand.

    Command structure:
    - spacetraders scout markets --ships SCOUT-1,SCOUT-2 --system X1-GZ7 --markets X1-GZ7-A1,X1-GZ7-B2,X1-GZ7-C3 [--iterations N] [--return-to-start]

    Player selection (same as other commands):
    - Uses default player if configured
    - --agent CHROMESAMURAI (by agent name)
    - --player-id 2 (by ID, explicit)

    Args:
        subparsers: Argparse subparsers to add commands to
    """
    # Scout command group
    scout_parser = subparsers.add_parser("scout", help="Market scouting commands")
    scout_subparsers = scout_parser.add_subparsers(dest="scout_command")

    # Scout markets command
    markets_parser = scout_subparsers.add_parser(
        "markets",
        help="Scout markets with VRP-optimized fleet distribution"
    )
    markets_parser.add_argument(
        "--ships",
        required=True,
        help="Comma-separated list of ship symbols (e.g., SCOUT-1,SCOUT-2,SCOUT-3)"
    )
    markets_parser.add_argument("--system", required=True, help="System symbol (e.g., X1-GZ7)")
    markets_parser.add_argument(
        "--markets",
        required=True,
        help="Comma-separated list of market waypoints (e.g., X1-GZ7-A1,X1-GZ7-B2,X1-GZ7-C3)"
    )
    markets_parser.add_argument(
        "--iterations",
        type=int,
        default=1,
        help="Number of complete tours to execute (default: 1, use -1 for infinite)"
    )
    markets_parser.add_argument(
        "--return-to-start",
        action="store_true",
        help="Return to starting waypoint after each tour"
    )
    markets_parser.add_argument("--player-id", type=int, help="Player ID (optional if default set)")
    markets_parser.add_argument("--agent", help="Agent symbol (e.g., CHROMESAMURAI)")
    markets_parser.set_defaults(func=scout_markets_command)
