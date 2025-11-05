"""
Configuration CLI commands.

Allows users to manage their SpaceTraders CLI configuration like default player.
"""
import argparse
import asyncio

from ....configuration.config import get_config
from ....configuration.container import get_mediator
from ....application.player.queries.get_player import GetPlayerByAgentQuery
from ....domain.shared.exceptions import PlayerNotFoundError


def set_player_command(args: argparse.Namespace) -> int:
    """
    Set default player by agent symbol.

    Args:
        args: Command arguments with agent_symbol

    Returns:
        0 on success, 1 on error
    """
    try:
        # Lookup player by agent symbol
        mediator = get_mediator()
        query = GetPlayerByAgentQuery(agent_symbol=args.agent_symbol)
        player = asyncio.run(mediator.send_async(query))

        # Set as default
        config = get_config()
        config.set_default_player(player.player_id, player.agent_symbol)

        print(f"✅ Set default player to {player.agent_symbol} (ID: {player.player_id})")
        print(f"\nYou can now run commands without --player-id:")
        print(f"  spacetraders ship list")
        print(f"  spacetraders navigate --ship SHIP-1 --destination X1-A1-B2")
        return 0

    except PlayerNotFoundError:
        print(f"❌ Player with agent '{args.agent_symbol}' not found")
        print(f"\nRegister this player first:")
        print(f"  spacetraders player register --agent {args.agent_symbol} --token YOUR_TOKEN")
        return 1
    except Exception as e:
        print(f"❌ Error: {e}")
        return 1


def clear_player_command(args: argparse.Namespace) -> int:
    """
    Clear default player setting.

    Args:
        args: Command arguments (unused)

    Returns:
        0 on success, 1 on error
    """
    try:
        config = get_config()
        old_agent = config.default_agent

        config.clear_default_player()

        if old_agent:
            print(f"✅ Cleared default player (was: {old_agent})")
        else:
            print(f"✅ No default player was set")

        print(f"\nYou'll need to specify player on each command:")
        print(f"  spacetraders ship list --player-id <id>")
        print(f"  spacetraders ship list --agent <symbol>")
        return 0

    except Exception as e:
        print(f"❌ Error: {e}")
        return 1


def show_config_command(args: argparse.Namespace) -> int:
    """
    Show current configuration.

    Args:
        args: Command arguments (unused)

    Returns:
        0 on success
    """
    try:
        config = get_config()

        print("SpaceTraders Configuration")
        print("=" * 40)
        print(f"Config file: {config.config_path}")
        print()

        if config.default_player_id:
            print(f"Default Player:")
            print(f"  ID:    {config.default_player_id}")
            print(f"  Agent: {config.default_agent}")
        else:
            print("Default Player: (not set)")
            print()
            print("Set a default player:")
            print("  spacetraders config set-player <agent_symbol>")

        return 0

    except Exception as e:
        print(f"❌ Error: {e}")
        return 1


def setup_config_commands(subparsers):
    """
    Setup configuration CLI commands.

    Command structure:
    - spacetraders config show
    - spacetraders config set-player <agent_symbol>
    - spacetraders config clear-player

    Args:
        subparsers: Argparse subparsers to add commands to
    """
    config_parser = subparsers.add_parser(
        "config",
        help="Manage SpaceTraders CLI configuration"
    )
    config_subparsers = config_parser.add_subparsers(dest="config_command")

    # Show config command
    show_parser = config_subparsers.add_parser(
        "show",
        help="Show current configuration"
    )
    show_parser.set_defaults(func=show_config_command)

    # Set default player command
    set_player_parser = config_subparsers.add_parser(
        "set-player",
        help="Set default player by agent symbol"
    )
    set_player_parser.add_argument(
        "agent_symbol",
        help="Agent symbol to set as default (e.g., CHROMESAMURAI)"
    )
    set_player_parser.set_defaults(func=set_player_command)

    # Clear default player command
    clear_player_parser = config_subparsers.add_parser(
        "clear-player",
        help="Clear default player setting"
    )
    clear_player_parser.set_defaults(func=clear_player_command)
