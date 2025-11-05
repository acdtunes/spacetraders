"""
Player selection helper for CLI commands.

Implements intelligent player selection with priority:
1. Explicit --player-id flag
2. Explicit --agent flag (lookup in DB)
3. Default from ~/.spacetraders/config.json
4. Auto-select if only one player in DB
5. Error if ambiguous
"""
import asyncio
import argparse
from typing import Optional

from configuration.container import get_mediator
from configuration.config import get_config
from application.player.queries.get_player import GetPlayerByAgentQuery
from application.player.queries.list_players import ListPlayersQuery
from domain.shared.exceptions import PlayerNotFoundError


class PlayerSelectionError(Exception):
    """Error when player cannot be determined"""
    pass


def get_player_id_from_args(args: argparse.Namespace) -> int:
    """
    Determine player_id from command line arguments with intelligent defaults.

    Priority order:
    1. Explicit --player-id flag
    2. Explicit --agent flag (lookup agent symbol in database)
    3. Default player from config file (~/.spacetraders/config.json)
    4. Auto-select if only one player exists in database
    5. Error if multiple players exist and none specified

    Args:
        args: Command line arguments namespace

    Returns:
        int: Player ID to use

    Raises:
        PlayerSelectionError: If player cannot be determined
    """
    # Priority 1: Explicit --player-id
    if hasattr(args, 'player_id') and args.player_id is not None:
        return args.player_id

    # Priority 2: Explicit --agent flag
    if hasattr(args, 'agent') and args.agent:
        try:
            mediator = get_mediator()
            query = GetPlayerByAgentQuery(agent_symbol=args.agent)
            player = asyncio.run(mediator.send_async(query))
            return player.player_id
        except PlayerNotFoundError:
            raise PlayerSelectionError(
                f"No player found with agent symbol '{args.agent}'. "
                f"Register with: spacetraders player register --agent {args.agent} --token YOUR_TOKEN"
            )

    # Priority 3: Default from config
    config = get_config()
    if config.default_player_id is not None:
        return config.default_player_id

    # Priority 4 & 5: Auto-select or error
    try:
        mediator = get_mediator()
        query = ListPlayersQuery()
        players = asyncio.run(mediator.send_async(query))

        if len(players) == 0:
            raise PlayerSelectionError(
                "No players registered. Register a player first:\n"
                "  spacetraders player register --agent YOUR_AGENT --token YOUR_TOKEN"
            )
        elif len(players) == 1:
            # Auto-select single player
            return players[0].player_id
        else:
            # Multiple players, need to specify
            agents = ', '.join(p.agent_symbol for p in players)
            raise PlayerSelectionError(
                f"Multiple players found. Specify which one to use:\n"
                f"  --player-id <id>  or  --agent <symbol>\n"
                f"  Available: {agents}\n"
                f"\n"
                f"Or set a default:\n"
                f"  spacetraders config set-player <agent_symbol>"
            )
    except Exception as e:
        if isinstance(e, PlayerSelectionError):
            raise
        raise PlayerSelectionError(f"Error determining player: {e}")


def format_player_selection_help() -> str:
    """
    Format helpful message about player selection.

    Returns:
        str: Help message
    """
    return """
Player Selection:
  Specify player using one of these methods:
    --player-id <id>        Use specific player ID
    --agent <symbol>        Use player by agent symbol

  Or set a default player:
    spacetraders config set-player <agent_symbol>

  List available players:
    spacetraders player list
"""
