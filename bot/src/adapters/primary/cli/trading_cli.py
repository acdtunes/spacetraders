import argparse
import uuid

from configuration.container import get_daemon_client
from .player_selector import get_player_id_from_args, PlayerSelectionError


def sell_command(args: argparse.Namespace) -> int:
    """
    Handle cargo sell command - ALWAYS runs in container mode

    Creates a background container that executes the sell cargo operation.

    Args:
        args: Command arguments with ship, good, units, optional player_id/agent

    Returns:
        0 on success, 1 on error
    """
    try:
        # Determine player_id using intelligent selection
        player_id = get_player_id_from_args(args)

        # Generate unique container ID
        container_id = f"sell-{args.ship.lower()}-{uuid.uuid4().hex[:8]}"

        # Get daemon client
        daemon = get_daemon_client()

        # Build params
        params = {
            'ship_symbol': args.ship,
            'trade_symbol': args.good,
            'units': args.units,
            'player_id': player_id
        }

        # Create container for sell operation
        result = daemon.create_container({
            'container_id': container_id,
            'player_id': player_id,
            'container_type': 'command',
            'config': {
                'command_type': 'SellCargoCommand',
                'params': params
            },
            'restart_policy': 'no'
        })

        return 0

    except PlayerSelectionError as e:
        print(f"❌ {e}")
        return 1
    except Exception as e:
        print(f"❌ Error: {e}")
        return 1


def setup_trading_commands(subparsers):
    """
    Setup trading CLI command structure

    Command structure (player selection is smart):
    - spacetraders sell --ship SHIP-1 --good IRON_ORE --units 10              # Uses default/auto
    - spacetraders sell --ship SHIP-1 --good IRON_ORE --units 10 --agent TEST  # By agent name
    - spacetraders sell --ship SHIP-1 --good IRON_ORE --units 10 --player-id 2 # By ID (explicit)

    Args:
        subparsers: Argparse subparsers to add commands to
    """
    # Sell command
    sell_parser = subparsers.add_parser("sell", help="Sell cargo from ship at market")
    sell_parser.add_argument("--ship", required=True, help="Ship symbol")
    sell_parser.add_argument("--good", required=True, help="Good symbol to sell (e.g., IRON_ORE)")
    sell_parser.add_argument("--units", type=int, required=True, help="Number of units to sell")
    sell_parser.add_argument("--player-id", type=int, help="Player ID (optional if default set)")
    sell_parser.add_argument("--agent", help="Agent symbol (e.g., CHROMESAMURAI)")
    sell_parser.set_defaults(func=sell_command)
