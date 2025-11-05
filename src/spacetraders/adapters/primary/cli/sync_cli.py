"""
CLI commands for syncing data from SpaceTraders API
"""
import argparse
import asyncio
import sys
from typing import Any

from ....configuration.container import get_mediator
from ....application.navigation.commands.sync_ships import SyncShipsCommand
from .player_selector import get_player_id_from_args, PlayerSelectionError


def sync_ships_command(args: argparse.Namespace) -> int:
    """
    Sync ships from SpaceTraders API to local database.

    Args:
        args: CLI arguments with optional player_id/agent

    Returns:
        Exit code (0 for success, 1 for error)
    """
    try:
        # Determine player_id using intelligent selection
        player_id = get_player_id_from_args(args)

        mediator = get_mediator()
        command = SyncShipsCommand(player_id=player_id)

        # Execute sync command
        ships = asyncio.run(mediator.send_async(command))

        print(f"✅ Successfully synced {len(ships)} ships")
        print("")
        print("Ships:")
        for ship in ships:
            fuel_pct = ship.fuel.percentage()
            print(f"  🚀 {ship.ship_symbol}")
            print(f"     Location: {ship.current_location.symbol}")
            print(f"     Fuel: {ship.fuel.current}/{ship.fuel.capacity} ({fuel_pct:.0f}%)")
            print(f"     Cargo: {ship.cargo_units}/{ship.cargo_capacity}")
            print(f"     Status: {ship.nav_status}")
            print("")

        return 0

    except PlayerSelectionError as e:
        print(f"❌ {e}")
        return 1

    except ValueError as e:
        print(f"❌ Configuration error: {e}")
        return 1

    except Exception as e:
        print(f"❌ Sync failed: {e}")
        return 1


def setup_sync_commands(subparsers: Any) -> None:
    """
    Setup sync CLI commands.

    Args:
        subparsers: argparse subparsers to add sync commands to
    """
    # Sync command group
    sync_parser = subparsers.add_parser("sync", help="Sync data from API")
    sync_subparsers = sync_parser.add_subparsers(dest="sync_command")

    # Sync ships command
    sync_ships_parser = sync_subparsers.add_parser(
        "ships",
        help="Sync ships from SpaceTraders API"
    )
    sync_ships_parser.add_argument(
        "--player-id",
        type=int,
        help="Player ID (optional if default set)"
    )
    sync_ships_parser.add_argument(
        "--agent",
        help="Agent symbol (e.g., CHROMESAMURAI)"
    )
    sync_ships_parser.set_defaults(func=sync_ships_command)
