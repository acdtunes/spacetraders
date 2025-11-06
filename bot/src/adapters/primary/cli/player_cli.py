import argparse
import json
import asyncio
from typing import Any, Dict

# TODO: get_mediator() will be implemented in Wave 5 (DI Container)
from configuration.container import get_mediator
from configuration.config import get_config
from application.player.commands.register_player import RegisterPlayerCommand
from application.player.queries.get_player import GetPlayerQuery, GetPlayerByAgentQuery
from application.player.queries.list_players import ListPlayersQuery
from domain.shared.exceptions import DuplicateAgentSymbolError, PlayerNotFoundError

def register_player_command(args: argparse.Namespace) -> int:
    """Handle player register command"""
    metadata: Dict[str, Any] = {}
    if args.metadata:
        metadata = json.loads(args.metadata)

    # TODO: get_mediator() will be implemented in Wave 5 (DI Container)
    mediator = get_mediator()
    command = RegisterPlayerCommand(
        agent_symbol=args.agent_symbol,
        token=args.token,
        metadata=metadata
    )

    try:
        # Send command via mediator
        player = asyncio.run(mediator.send_async(command))
        print(f"✅ Registered player {player.player_id}: {player.agent_symbol}")
        return 0
    except DuplicateAgentSymbolError as e:
        print(f"❌ Error: {e}")
        return 1

def list_players_command(args: argparse.Namespace) -> int:
    """Handle player list command"""
    # TODO: get_mediator() will be implemented in Wave 5 (DI Container)
    mediator = get_mediator()
    query = ListPlayersQuery()

    # Send query via mediator
    players = asyncio.run(mediator.send_async(query))

    if not players:
        print("No players registered")
        return 0

    print(f"Registered players ({len(players)}):")
    for player in players:
        active = "✓" if player.is_active_within(24) else "✗"
        print(f"  [{player.player_id}] {player.agent_symbol} {active}")

    return 0

def player_info_command(args: argparse.Namespace) -> int:
    """Handle player info command"""
    # TODO: get_mediator() will be implemented in Wave 5 (DI Container)
    mediator = get_mediator()

    try:
        # Determine player_id
        player_id = None
        if args.player_id:
            player_id = args.player_id
        elif args.agent_symbol:
            query = GetPlayerByAgentQuery(agent_symbol=args.agent_symbol)
            player = asyncio.run(mediator.send_async(query))
            player_id = player.player_id
        else:
            # No parameters provided - use default player from config
            config = get_config()
            if not config.default_player_id:
                print("❌ Error: No player specified and no default player configured")
                return 1
            player_id = config.default_player_id

        # Sync player data from API to get fresh credits and metadata
        from application.player.commands.sync_player import SyncPlayerCommand
        try:
            player = asyncio.run(mediator.send_async(SyncPlayerCommand(player_id=player_id)))
        except Exception as e:
            # If sync fails, fall back to database query
            print(f"⚠️  Warning: Failed to sync data from API: {e}")
            query = GetPlayerQuery(player_id=player_id)
            player = asyncio.run(mediator.send_async(query))

        # Display player info
        print(f"Player {player.player_id}:")
        print(f"  Agent: {player.agent_symbol}")
        print(f"  Credits: {player.credits:,}")
        print(f"  Created: {player.created_at.isoformat()}")
        print(f"  Last Active: {player.last_active.isoformat()}")
        if player.metadata:
            headquarters = player.metadata.get("headquarters", "Unknown")
            ship_count = player.metadata.get("shipCount", "Unknown")
            print(f"  Headquarters: {headquarters}")
            print(f"  Ships: {ship_count}")
            print(f"  Metadata: {json.dumps(player.metadata, indent=2)}")

        return 0
    except PlayerNotFoundError as e:
        print(f"❌ Error: {e}")
        return 1

def setup_player_commands(subparsers):
    """Setup player CLI commands"""
    player_parser = subparsers.add_parser("player", help="Player management")
    player_subparsers = player_parser.add_subparsers(dest="player_command")

    # Register command
    register_parser = player_subparsers.add_parser("register", help="Register new player")
    register_parser.add_argument("--agent", dest="agent_symbol", required=True)
    register_parser.add_argument("--token", required=True)
    register_parser.add_argument("--metadata", help="JSON metadata")
    register_parser.set_defaults(func=register_player_command)

    # List command
    list_parser = player_subparsers.add_parser("list", help="List all players")
    list_parser.set_defaults(func=list_players_command)

    # Info command
    info_parser = player_subparsers.add_parser("info", help="Get player info")
    info_parser.add_argument("--player-id", type=int)
    info_parser.add_argument("--agent", dest="agent_symbol")
    info_parser.set_defaults(func=player_info_command)
