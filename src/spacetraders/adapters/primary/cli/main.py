#!/usr/bin/env python3
import argparse
import sys
from .player_cli import setup_player_commands
from .navigation_cli import setup_navigation_commands
from .sync_cli import setup_sync_commands
from .daemon_cli import setup_daemon_commands
from .config_cli import setup_config_commands
from .shipyard_cli import setup_shipyard_commands
from .scouting_cli import setup_scouting_commands
from .contract_cli import setup_contract_commands

def main():
    parser = argparse.ArgumentParser(description="SpaceTraders V2")
    subparsers = parser.add_subparsers(dest="command")

    # Setup subcommands
    setup_player_commands(subparsers)
    setup_navigation_commands(subparsers)
    setup_sync_commands(subparsers)
    setup_daemon_commands(subparsers)
    setup_config_commands(subparsers)
    setup_shipyard_commands(subparsers)
    setup_scouting_commands(subparsers)
    setup_contract_commands(subparsers)

    args = parser.parse_args()

    # Use func attribute set by set_defaults
    if hasattr(args, "func"):
        sys.exit(args.func(args))
    else:
        parser.print_help()
        sys.exit(1)

if __name__ == "__main__":
    main()
