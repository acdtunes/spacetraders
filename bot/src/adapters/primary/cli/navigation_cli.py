import argparse
import json
import asyncio
import uuid
from typing import Any, Dict

from configuration.container import get_mediator, get_daemon_client
from application.navigation.commands.navigate_ship import NavigateShipCommand
from application.navigation.commands.dock_ship import DockShipCommand
from application.navigation.commands.orbit_ship import OrbitShipCommand
from application.navigation.commands.refuel_ship import RefuelShipCommand
from application.navigation.queries.get_ship_location import GetShipLocationQuery
from application.navigation.queries.list_ships import ListShipsQuery
from application.navigation.queries.plan_route import PlanRouteQuery
from domain.shared.exceptions import ShipNotFoundError
from domain.navigation.exceptions import NoRouteFoundError
from domain.shared.ship import InvalidNavStatusError, InsufficientFuelError
from .player_selector import get_player_id_from_args, PlayerSelectionError


def navigate_command(args: argparse.Namespace) -> int:
    """
    Handle ship navigation command - ALWAYS runs in container mode

    Creates a background container that executes navigation including:
    - Route planning with fuel constraints
    - Automatic refueling stops
    - State transitions (orbit/dock)
    - Route execution

    Args:
        args: Command arguments with ship, destination, optional player_id/agent

    Returns:
        0 on success, 1 on error
    """
    try:
        # Determine player_id using intelligent selection
        player_id = get_player_id_from_args(args)

        # Generate unique container ID
        container_id = f"nav-{args.ship.lower()}-{uuid.uuid4().hex[:8]}"

        # Get daemon client
        daemon = get_daemon_client()

        # Create container for navigation
        result = daemon.create_container({
            'container_id': container_id,
            'player_id': player_id,
            'container_type': 'command',
            'config': {
                'command_type': 'NavigateShipCommand',
                'params': {
                    'ship_symbol': args.ship,
                    'destination_symbol': args.destination,
                    'player_id': player_id
                }
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


def dock_command(args: argparse.Namespace) -> int:
    """
    Handle ship dock command - ALWAYS runs in container mode

    Creates a background container that docks the ship at its current location.

    Args:
        args: Command arguments with ship, optional player_id/agent

    Returns:
        0 on success, 1 on error
    """
    try:
        player_id = get_player_id_from_args(args)

        # Generate unique container ID
        container_id = f"dock-{args.ship.lower()}-{uuid.uuid4().hex[:8]}"

        # Get daemon client
        daemon = get_daemon_client()

        # Create container for docking
        result = daemon.create_container({
            'container_id': container_id,
            'player_id': player_id,
            'container_type': 'command',
            'config': {
                'command_type': 'DockShipCommand',
                'params': {
                    'ship_symbol': args.ship,
                    'player_id': player_id
                }
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


def orbit_command(args: argparse.Namespace) -> int:
    """
    Handle ship orbit command - ALWAYS runs in container mode

    Creates a background container that puts the ship into orbit.

    Args:
        args: Command arguments with ship, optional player_id/agent

    Returns:
        0 on success, 1 on error
    """
    try:
        player_id = get_player_id_from_args(args)

        # Generate unique container ID
        container_id = f"orbit-{args.ship.lower()}-{uuid.uuid4().hex[:8]}"

        # Get daemon client
        daemon = get_daemon_client()

        # Create container for orbit
        result = daemon.create_container({
            'container_id': container_id,
            'player_id': player_id,
            'container_type': 'command',
            'config': {
                'command_type': 'OrbitShipCommand',
                'params': {
                    'ship_symbol': args.ship,
                    'player_id': player_id
                }
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


def refuel_command(args: argparse.Namespace) -> int:
    """
    Handle ship refuel command - ALWAYS runs in container mode

    Creates a background container that refuels the ship at its current location.

    Args:
        args: Command arguments with ship, optional player_id/agent, units (optional)

    Returns:
        0 on success, 1 on error
    """
    try:
        player_id = get_player_id_from_args(args)

        # Generate unique container ID
        container_id = f"refuel-{args.ship.lower()}-{uuid.uuid4().hex[:8]}"

        # Get daemon client
        daemon = get_daemon_client()

        # Build params
        params = {
            'ship_symbol': args.ship,
            'player_id': player_id
        }
        if hasattr(args, 'units') and args.units is not None:
            params['units'] = args.units

        # Create container for refuel
        result = daemon.create_container({
            'container_id': container_id,
            'player_id': player_id,
            'container_type': 'command',
            'config': {
                'command_type': 'RefuelShipCommand',
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


def ship_info_command(args: argparse.Namespace) -> int:
    """
    Handle ship info command

    Displays detailed information about a ship including:
    - Current location
    - Navigation status
    - Fuel levels
    - Cargo capacity

    Args:
        args: Command arguments with ship, optional player_id/agent

    Returns:
        0 on success, 1 on error
    """
    try:
        player_id = get_player_id_from_args(args)

        mediator = get_mediator()
        query = GetShipLocationQuery(
            ship_symbol=args.ship,
            player_id=player_id
        )

        # Send query via mediator - note this only returns Waypoint
        # We need to get the full ship for complete info
        # For now, we'll use a workaround by getting ship from ListShips
        list_query = ListShipsQuery(player_id=player_id)
        ships = asyncio.run(mediator.send_async(list_query))

        # Find the specific ship
        ship = next((s for s in ships if s.ship_symbol == args.ship), None)
        if not ship:
            raise ShipNotFoundError(f"Ship '{args.ship}' not found for player {player_id}")

        # Display ship information
        print(f"\n{ship.ship_symbol}")
        print("=" * 80)
        print(f"Location:       {ship.current_location.symbol}")
        print(f"System:         {ship.current_location.system_symbol or 'Unknown'}")
        print(f"Status:         {ship.nav_status}")
        print(f"\nFuel:           {ship.fuel.current}/{ship.fuel.capacity} ({ship.fuel.percentage():.0f}%)")
        print(f"Cargo:          {ship.cargo_units}/{ship.cargo_capacity}")
        print(f"Engine Speed:   {ship.engine_speed}")

        if ship.current_location.waypoint_type:
            print(f"\nWaypoint Type:  {ship.current_location.waypoint_type}")
        if ship.current_location.traits:
            print(f"Traits:         {', '.join(ship.current_location.traits)}")

        return 0

    except PlayerSelectionError as e:
        print(f"❌ {e}")
        return 1
    except Exception as e:
        print(f"❌ Error: {e}")
        return 1


def list_ships_command(args: argparse.Namespace) -> int:
    """
    Handle list ships command

    Lists all ships for a player in table format with:
    - Ship symbol
    - Current location
    - Navigation status
    - Fuel percentage
    - Cargo usage

    Auto-syncs from API if database is empty.

    Args:
        args: Command arguments with optional player_id/agent

    Returns:
        0 on success, 1 on error
    """
    try:
        player_id = get_player_id_from_args(args)

        mediator = get_mediator()
        query = ListShipsQuery(player_id=player_id)

        # Send query via mediator
        ships = asyncio.run(mediator.send_async(query))

        # Auto-sync if database is empty
        if not ships:
            print("📡 No ships in database, syncing from API...")
            from application.navigation.commands.sync_ships import SyncShipsCommand
            sync_command = SyncShipsCommand(player_id=player_id)
            ships = asyncio.run(mediator.send_async(sync_command))
            print(f"✅ Synced {len(ships)} ships\n")

        # Display ship list
        if not ships:
            print("No ships found")
            return 0

        print(f"Ships ({len(ships)}):")
        print("-" * 80)
        for ship in ships:
            fuel_pct = ship.fuel.percentage()
            cargo_used = f"{ship.cargo_units}/{ship.cargo_capacity}"
            print(f"  {ship.ship_symbol}")
            print(f"    Location: {ship.current_location.symbol}")
            print(f"    Status: {ship.nav_status}")
            print(f"    Fuel: {fuel_pct:.0f}% ({ship.fuel.current}/{ship.fuel.capacity})")
            print(f"    Cargo: {cargo_used}")
            print()

        return 0

    except PlayerSelectionError as e:
        print(f"❌ {e}")
        return 1
    except Exception as e:
        print(f"❌ Error: {e}")
        return 1


def plan_route_command(args: argparse.Namespace) -> int:
    """
    Handle route planning command

    Plans a route without executing it, showing:
    - Route segments
    - Fuel requirements
    - Travel time estimates
    - Refueling stops needed

    Args:
        args: Command arguments with ship, destination, optional player_id/agent

    Returns:
        0 on success, 1 on error
    """
    try:
        player_id = get_player_id_from_args(args)

        mediator = get_mediator()
        query = PlanRouteQuery(
            ship_symbol=args.ship,
            destination_symbol=args.destination,
            player_id=player_id,
            prefer_cruise=True
        )

        # Send query via mediator
        route = asyncio.run(mediator.send_async(query))

        return 0

    except PlayerSelectionError as e:
        print(f"❌ {e}")
        return 1
    except Exception as e:
        print(f"❌ Error: {e}")
        return 1


def setup_navigation_commands(subparsers):
    """
    Setup navigation CLI command structure

    Creates two main command groups:
    1. 'ship' - Ship management commands (list, info)
    2. Navigation commands - Direct navigation operations

    Command structure (player selection is now smart):
    - spacetraders ship list                                    # Uses default/auto
    - spacetraders ship list --agent CHROMESAMURAI              # By agent name
    - spacetraders ship list --player-id 2                      # By ID (explicit)
    - spacetraders navigate --ship SHIP-1 --destination X1-A1-B2
    - spacetraders dock --ship SHIP-1
    - spacetraders orbit --ship SHIP-1
    - spacetraders refuel --ship SHIP-1 [--units 100]
    - spacetraders plan --ship SHIP-1 --destination X1-A1-B2

    Args:
        subparsers: Argparse subparsers to add commands to
    """
    # Ship command group
    ship_parser = subparsers.add_parser("ship", help="Ship management commands")
    ship_subparsers = ship_parser.add_subparsers(dest="ship_command")

    # Ship list command
    list_parser = ship_subparsers.add_parser("list", help="List all ships for a player")
    list_parser.add_argument("--player-id", type=int, help="Player ID (optional if default set)")
    list_parser.add_argument("--agent", help="Agent symbol (e.g., CHROMESAMURAI)")
    list_parser.set_defaults(func=list_ships_command)

    # Ship info command
    info_parser = ship_subparsers.add_parser("info", help="Get detailed ship information")
    info_parser.add_argument("--ship", required=True, help="Ship symbol")
    info_parser.add_argument("--player-id", type=int, help="Player ID (optional if default set)")
    info_parser.add_argument("--agent", help="Agent symbol (e.g., CHROMESAMURAI)")
    info_parser.set_defaults(func=ship_info_command)

    # Navigate command
    navigate_parser = subparsers.add_parser("navigate", help="Navigate ship to destination")
    navigate_parser.add_argument("--ship", required=True, help="Ship symbol")
    navigate_parser.add_argument("--destination", required=True, help="Destination waypoint symbol")
    navigate_parser.add_argument("--player-id", type=int, help="Player ID (optional if default set)")
    navigate_parser.add_argument("--agent", help="Agent symbol (e.g., CHROMESAMURAI)")
    navigate_parser.set_defaults(func=navigate_command)

    # Dock command
    dock_parser = subparsers.add_parser("dock", help="Dock ship at current location")
    dock_parser.add_argument("--ship", required=True, help="Ship symbol")
    dock_parser.add_argument("--player-id", type=int, help="Player ID (optional if default set)")
    dock_parser.add_argument("--agent", help="Agent symbol (e.g., CHROMESAMURAI)")
    dock_parser.set_defaults(func=dock_command)

    # Orbit command
    orbit_parser = subparsers.add_parser("orbit", help="Put ship into orbit")
    orbit_parser.add_argument("--ship", required=True, help="Ship symbol")
    orbit_parser.add_argument("--player-id", type=int, help="Player ID (optional if default set)")
    orbit_parser.add_argument("--agent", help="Agent symbol (e.g., CHROMESAMURAI)")
    orbit_parser.set_defaults(func=orbit_command)

    # Refuel command
    refuel_parser = subparsers.add_parser("refuel", help="Refuel ship at current location")
    refuel_parser.add_argument("--ship", required=True, help="Ship symbol")
    refuel_parser.add_argument("--player-id", type=int, help="Player ID (optional if default set)")
    refuel_parser.add_argument("--agent", help="Agent symbol (e.g., CHROMESAMURAI)")
    refuel_parser.add_argument("--units", type=int, help="Fuel units to add (optional, defaults to full)")
    refuel_parser.set_defaults(func=refuel_command)

    # Plan route command
    plan_parser = subparsers.add_parser("plan", help="Plan route without executing")
    plan_parser.add_argument("--ship", required=True, help="Ship symbol")
    plan_parser.add_argument("--destination", required=True, help="Destination waypoint symbol")
    plan_parser.add_argument("--player-id", type=int, help="Player ID (optional if default set)")
    plan_parser.add_argument("--agent", help="Agent symbol (e.g., CHROMESAMURAI)")
    plan_parser.set_defaults(func=plan_route_command)
