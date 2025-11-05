"""
Shipyard CLI commands for ship purchasing operations.

Commands:
- spacetraders shipyard list --waypoint X1-A1-B2 --agent CHROMESAMURAI
- spacetraders shipyard purchase --ship SHIP-1 --type SHIP_MINING_DRONE --agent CHROMESAMURAI
  (auto-discovers nearest shipyard that sells the ship type)
- spacetraders shipyard purchase --ship SHIP-1 --type SHIP_MINING_DRONE --shipyard X1-A1-B2 --agent CHROMESAMURAI
  (specify shipyard explicitly for power users)
- spacetraders shipyard batch --ship SHIP-1 --type SHIP_MINING_DRONE --quantity 5 --max-budget 500000 --agent CHROMESAMURAI
  (auto-discovers nearest shipyard)
"""
import argparse
import asyncio
import uuid
from typing import List

from configuration.container import get_mediator, get_daemon_client
from application.shipyard.queries.get_shipyard_listings import GetShipyardListingsQuery
from application.shipyard.commands.purchase_ship import PurchaseShipCommand
from application.shipyard.commands.batch_purchase_ships import BatchPurchaseShipsCommand
from domain.shared.exceptions import ShipyardNotFoundError
from .player_selector import get_player_id_from_args, PlayerSelectionError


def list_shipyard_command(args: argparse.Namespace) -> int:
    """
    List available ships at a shipyard.

    Displays:
    - Ship type
    - Name
    - Purchase price (formatted with commas)
    - Frame/Reactor/Engine details

    Args:
        args: Command arguments with waypoint, optional player_id/agent

    Returns:
        0 on success, 1 on error
    """
    try:
        # Determine player_id using intelligent selection
        player_id = get_player_id_from_args(args)

        # Extract system symbol from waypoint (e.g., "X1-GZ7" from "X1-GZ7-AB12")
        system_symbol = '-'.join(args.waypoint.split('-')[:2])

        # Get mediator
        mediator = get_mediator()

        # Query shipyard listings
        query = GetShipyardListingsQuery(
            system_symbol=system_symbol,
            waypoint_symbol=args.waypoint,
            player_id=player_id
        )

        shipyard = asyncio.run(mediator.send_async(query))

        # Display shipyard information
        print(f"\nShipyard at {shipyard.symbol}")
        print(f"Modification Fee: {_format_credits(shipyard.modification_fee)}")
        print(f"\nAvailable Ships ({len(shipyard.listings)}):")
        print("-" * 80)

        if not shipyard.listings:
            print("No ships currently available at this shipyard.")
        else:
            for listing in shipyard.listings:
                print(f"\n{listing.ship_type}")
                print(f"  Name: {listing.name}")
                print(f"  Price: {_format_credits(listing.purchase_price)}")
                if listing.frame:
                    print(f"  Frame: {listing.frame.get('symbol', 'N/A')}")
                if listing.reactor:
                    print(f"  Reactor: {listing.reactor.get('symbol', 'N/A')}")
                if listing.engine:
                    print(f"  Engine: {listing.engine.get('symbol', 'N/A')}")
                if listing.description:
                    print(f"  Description: {listing.description}")

        print("-" * 80)
        return 0

    except PlayerSelectionError as e:
        print(f"Error: {e}")
        return 1
    except ShipyardNotFoundError as e:
        print(f"Error: {e}")
        return 1
    except Exception as e:
        print(f"Error: {e}")
        return 1


def purchase_ship_command(args: argparse.Namespace) -> int:
    """
    Purchase ship from shipyard - runs in daemon container.

    The command will:
    1. Auto-discover nearest shipyard (if not specified) that sells the desired ship type
    2. Navigate to the shipyard if not already there
    3. Dock if in orbit
    4. Purchase the specified ship type
    5. Update player credits
    6. Save the new ship

    Args:
        args: Command arguments with ship, type, optional shipyard, optional player_id/agent

    Returns:
        0 on success, 1 on error
    """
    try:
        # Determine player_id using intelligent selection
        player_id = get_player_id_from_args(args)

        # Generate unique container ID
        container_id = f"purchase-{args.ship.lower()}-{uuid.uuid4().hex[:8]}"

        # Get daemon client
        daemon = get_daemon_client()

        # Build params dict
        params = {
            'purchasing_ship_symbol': args.ship,
            'ship_type': args.type,
            'player_id': player_id
        }

        # Add shipyard_waypoint only if provided (for backward compatibility)
        if hasattr(args, 'shipyard') and args.shipyard is not None:
            params['shipyard_waypoint'] = args.shipyard

        # Create container for purchase operation
        result = daemon.create_container({
            'container_id': container_id,
            'player_id': player_id,
            'container_type': 'command',
            'config': {
                'command_type': 'PurchaseShipCommand',
                'params': params
            },
            'restart_policy': 'no'
        })

        if args.shipyard is None:
            print(f"Auto-discovering nearest shipyard that sells {args.type}...")

        print(f"Container created: {container_id}")
        print(f"Monitor with: spacetraders daemon logs {container_id}")

        return 0

    except PlayerSelectionError as e:
        print(f"{e}")
        return 1
    except Exception as e:
        print(f"Error: {e}")
        return 1


def batch_purchase_command(args: argparse.Namespace) -> int:
    """
    Batch purchase ships from shipyard - runs in daemon container.

    Purchases as many ships as possible within constraints:
    - Quantity requested
    - Maximum budget allocated
    - Player's available credits

    If shipyard is not specified, will auto-discover nearest shipyard that sells the ship type.

    Args:
        args: Command arguments with ship, type, quantity, max_budget, optional shipyard, optional player_id/agent

    Returns:
        0 on success, 1 on error
    """
    try:
        # Determine player_id using intelligent selection
        player_id = get_player_id_from_args(args)

        # Generate unique container ID
        container_id = f"batch-purchase-{uuid.uuid4().hex[:8]}"

        # Get daemon client
        daemon = get_daemon_client()

        # Build params dict
        params = {
            'purchasing_ship_symbol': args.ship,
            'ship_type': args.type,
            'quantity': args.quantity,
            'max_budget': args.max_budget,
            'player_id': player_id
        }

        # Add shipyard_waypoint only if provided (for backward compatibility)
        if hasattr(args, 'shipyard') and args.shipyard is not None:
            params['shipyard_waypoint'] = args.shipyard

        # Create container for batch purchase operation
        result = daemon.create_container({
            'container_id': container_id,
            'player_id': player_id,
            'container_type': 'command',
            'config': {
                'command_type': 'BatchPurchaseShipsCommand',
                'params': params
            },
            'restart_policy': 'no'
        })

        if args.shipyard is None:
            print(f"Auto-discovering nearest shipyard that sells {args.type}...")

        print(f"Batch purchase container created: {container_id}")
        print(f"Purchasing up to {args.quantity} ships with max budget {_format_credits(args.max_budget)}")
        print(f"Monitor with: spacetraders daemon logs {container_id}")

        return 0

    except PlayerSelectionError as e:
        print(f"{e}")
        return 1
    except Exception as e:
        print(f"Error: {e}")
        return 1


def setup_shipyard_commands(subparsers):
    """
    Setup shipyard CLI command structure.

    Command structure:
    - spacetraders shipyard list --waypoint X1-A1-B2 [--player-id N | --agent AGENT]
    - spacetraders shipyard purchase --ship SHIP-1 --type SHIP_MINING_DRONE [--shipyard X1-A1-B2] [--player-id N | --agent AGENT]
      (auto-discovers nearest shipyard if --shipyard not provided)
    - spacetraders shipyard batch --ship SHIP-1 --type SHIP_MINING_DRONE --quantity 5 --max-budget 500000 [--shipyard X1-A1-B2] [--player-id N | --agent AGENT]
      (auto-discovers nearest shipyard if --shipyard not provided)

    Args:
        subparsers: Argparse subparsers to add commands to
    """
    # Shipyard command group
    shipyard_parser = subparsers.add_parser("shipyard", help="Ship purchasing commands")
    shipyard_subparsers = shipyard_parser.add_subparsers(dest="shipyard_command")

    # List command
    list_parser = shipyard_subparsers.add_parser("list", help="List available ships at shipyard")
    list_parser.add_argument("--waypoint", required=True, help="Shipyard waypoint symbol (e.g., X1-GZ7-AB12)")
    list_parser.add_argument("--player-id", type=int, help="Player ID (optional if default set)")
    list_parser.add_argument("--agent", help="Agent symbol (e.g., CHROMESAMURAI)")
    list_parser.set_defaults(func=list_shipyard_command)

    # Purchase command
    purchase_parser = shipyard_subparsers.add_parser("purchase", help="Purchase ship from shipyard (auto-discovers nearest shipyard)")
    purchase_parser.add_argument("--ship", required=True, help="Ship symbol to use for purchase")
    purchase_parser.add_argument("--type", required=True, help="Ship type to purchase (e.g., SHIP_MINING_DRONE)")
    purchase_parser.add_argument("--shipyard", help="Shipyard waypoint symbol (optional - will auto-discover if not provided)")
    purchase_parser.add_argument("--player-id", type=int, help="Player ID (optional if default set)")
    purchase_parser.add_argument("--agent", help="Agent symbol (e.g., CHROMESAMURAI)")
    purchase_parser.set_defaults(func=purchase_ship_command)

    # Batch purchase command
    batch_parser = shipyard_subparsers.add_parser("batch", help="Batch purchase ships from shipyard (auto-discovers nearest shipyard)")
    batch_parser.add_argument("--ship", required=True, help="Ship symbol to use for purchase")
    batch_parser.add_argument("--type", required=True, help="Ship type to purchase (e.g., SHIP_MINING_DRONE)")
    batch_parser.add_argument("--quantity", type=int, required=True, help="Maximum number of ships to purchase")
    batch_parser.add_argument("--max-budget", type=int, required=True, help="Maximum total credits to spend")
    batch_parser.add_argument("--shipyard", help="Shipyard waypoint symbol (optional - will auto-discover if not provided)")
    batch_parser.add_argument("--player-id", type=int, help="Player ID (optional if default set)")
    batch_parser.add_argument("--agent", help="Agent symbol (e.g., CHROMESAMURAI)")
    batch_parser.set_defaults(func=batch_purchase_command)


def _format_credits(amount: int) -> str:
    """
    Format credit amount with commas for readability.

    Args:
        amount: Credit amount to format

    Returns:
        Formatted string (e.g., "150,000")
    """
    return f"{amount:,}"
