#!/usr/bin/env python3
"""Utility bridge for the TypeScript MCP server.

This script exposes a subset of the bot's database and market helpers as a
simple CLI that returns JSON payloads. It keeps the Python implementation as
the source of truth while allowing the new Node.js server to stay lean.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any, Dict, Optional

# Ensure bot/lib modules are importable
BOT_DIR = Path(__file__).parent
LIB_DIR = BOT_DIR / "lib"
if str(LIB_DIR) not in sys.path:
    sys.path.insert(0, str(LIB_DIR))

from database import get_database  # type: ignore  # noqa: E402
from market_data import (  # type: ignore  # noqa: E402
    find_markets_buying,
    find_markets_selling,
    get_recent_updates,
    get_stale_markets,
    get_waypoint_good,
    get_waypoint_goods,
    summarize_good,
)


def _json_response(data: Any, *, success: bool = True) -> int:
    payload = {"success": success, "data": data}
    json.dump(payload, sys.stdout)
    sys.stdout.write("\n")
    return 0 if success else 1


def _json_error(message: str) -> int:
    return _json_response({"error": message}, success=False)


def handle_players(args: argparse.Namespace) -> int:
    db = get_database()
    if args.action == "list":
        with db.connection() as conn:
            players = db.list_players(conn)
        return _json_response({"players": players})

    if args.action == "register":
        metadata: Dict[str, Any] = {}
        if args.metadata:
            metadata = json.loads(args.metadata)

        with db.transaction() as conn:
            player_id = db.create_player(conn, args.agent_symbol, args.token, metadata)
            player = db.get_player_by_id(conn, player_id)
        return _json_response({"player": player})

    if args.action == "info":
        with db.connection() as conn:
            player: Optional[Dict[str, Any]]
            if args.player_id is not None:
                player = db.get_player_by_id(conn, args.player_id)
            elif args.agent_symbol:
                player = db.get_player(conn, args.agent_symbol)
            else:
                return _json_error("Must provide player_id or agent_symbol")
        return _json_response({"player": player})

    return _json_error(f"Unknown players action: {args.action}")


def handle_market(args: argparse.Namespace) -> int:
    if args.action == "waypoint":
        if args.good_symbol:
            entry = get_waypoint_good(args.waypoint_symbol, args.good_symbol)
            return _json_response({"entry": entry})
        goods = get_waypoint_goods(args.waypoint_symbol)
        return _json_response({"goods": goods})

    if args.action == "find_sellers":
        markets = find_markets_selling(
            args.good_symbol,
            system=args.system,
            min_supply=args.min_supply,
            updated_within_hours=args.updated_within_hours,
            limit=args.limit,
        )
        return _json_response({"markets": markets})

    if args.action == "find_buyers":
        markets = find_markets_buying(
            args.good_symbol,
            system=args.system,
            min_activity=args.min_activity,
            updated_within_hours=args.updated_within_hours,
            limit=args.limit,
        )
        return _json_response({"markets": markets})

    if args.action == "recent_updates":
        updates = get_recent_updates(system=args.system, limit=args.limit)
        return _json_response({"updates": updates})

    if args.action == "stale":
        stale = get_stale_markets(args.max_age_hours, system=args.system)
        return _json_response({"stale": stale})

    if args.action == "summarize_good":
        summary = summarize_good(args.good_symbol, system=args.system)
        return _json_response({"summary": summary})

    return _json_error(f"Unknown market action: {args.action}")


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Bridge for MCP TypeScript server")
    subparsers = parser.add_subparsers(dest="category", required=True)

    players = subparsers.add_parser("players", help="Player database helpers")
    players_sub = players.add_subparsers(dest="action", required=True)

    players_sub.add_parser("list", help="List all players")

    register = players_sub.add_parser("register", help="Register or update a player")
    register.add_argument("--agent-symbol", required=True)
    register.add_argument("--token", required=True)
    register.add_argument("--metadata", help="Optional JSON metadata")

    info = players_sub.add_parser("info", help="Get player information")
    info.add_argument("--player-id", type=int)
    info.add_argument("--agent-symbol")

    market = subparsers.add_parser("market", help="Market data helpers")
    market_sub = market.add_subparsers(dest="action", required=True)

    waypoint = market_sub.add_parser("waypoint", help="Waypoint market data")
    waypoint.add_argument("--waypoint-symbol", required=True)
    waypoint.add_argument("--good-symbol")

    sellers = market_sub.add_parser("find_sellers", help="Find markets selling a good")
    sellers.add_argument("--good-symbol", required=True)
    sellers.add_argument("--system")
    sellers.add_argument("--min-supply")
    sellers.add_argument("--updated-within-hours", type=float)
    sellers.add_argument("--limit", type=int)

    buyers = market_sub.add_parser("find_buyers", help="Find markets buying a good")
    buyers.add_argument("--good-symbol", required=True)
    buyers.add_argument("--system")
    buyers.add_argument("--min-activity")
    buyers.add_argument("--updated-within-hours", type=float)
    buyers.add_argument("--limit", type=int)

    recent = market_sub.add_parser("recent_updates", help="Recent market updates")
    recent.add_argument("--system")
    recent.add_argument("--limit", type=int)

    stale = market_sub.add_parser("stale", help="Stale market intel")
    stale.add_argument("--max-age-hours", type=float, required=True)
    stale.add_argument("--system")

    summarize = market_sub.add_parser("summarize_good", help="Summarize market intel for a good")
    summarize.add_argument("--good-symbol", required=True)
    summarize.add_argument("--system")

    return parser


def main(argv: Optional[list[str]] = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)

    try:
        if args.category == "players":
            return handle_players(args)
        if args.category == "market":
            return handle_market(args)
        return _json_error(f"Unknown category: {args.category}")
    except Exception as exc:  # pylint: disable=broad-except
        return _json_error(str(exc))


if __name__ == "__main__":
    sys.exit(main())
