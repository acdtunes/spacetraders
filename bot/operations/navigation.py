"""Smart navigation operation using SmartNavigator."""

from lib.api_client import APIClient
from lib.ship_controller import ShipController
from lib.smart_navigator import SmartNavigator
from lib.database import get_database


def navigate_ship(args, api: APIClient, logger):
    """Navigate a ship to a destination using SmartNavigator with fuel awareness.

    Args:
        args: Namespace containing:
            - player_id: Player ID
            - ship: Ship symbol
            - destination: Destination waypoint
            - system: System symbol (optional, auto-detected if in same system)
        api: API client
        logger: Logger instance

    Returns:
        bool: True if navigation successful, False otherwise
    """
    db = get_database()

    # Get player token
    with db.connection() as conn:
        player = db.get_player_by_id(conn, args.player_id)

    if not player:
        logger.error(f"Player ID {args.player_id} not found in database")
        return False

    # Create API client with player's token
    api = APIClient(token=player["token"])
    ship = ShipController(api, args.ship)

    # Get current ship status
    status = ship.get_status()
    current_location = status["nav"]["waypointSymbol"]
    system = status["nav"]["systemSymbol"]

    logger.info(f"Ship {args.ship} currently at {current_location}")
    logger.info(f"Navigating to {args.destination}")

    # Verify destination is in same system
    dest_system = args.destination.split("-")[0] + "-" + args.destination.split("-")[1]
    if dest_system != system:
        logger.error(f"Cross-system navigation not supported. Ship in {system}, destination in {dest_system}")
        return False

    # Check if already at destination
    if current_location == args.destination:
        logger.info(f"Ship already at destination {args.destination}")
        return True

    # Create SmartNavigator for this system
    navigator = SmartNavigator(api, system)

    # Validate route
    logger.info("Validating route...")
    valid, reason = navigator.validate_route(status, args.destination)

    if not valid:
        logger.error(f"Route validation failed: {reason}")
        return False

    logger.info(f"Route validated successfully")

    # Execute navigation with fuel awareness
    logger.info("Executing navigation with SmartNavigator (automatic refuel stops if needed)...")
    success = navigator.execute_route(ship, args.destination)

    if success:
        final_status = ship.get_status()
        final_location = final_status["nav"]["waypointSymbol"]
        fuel = final_status["fuel"]
        logger.info(f"✅ Navigation complete!")
        logger.info(f"Final location: {final_location}")
        logger.info(f"Fuel remaining: {fuel['current']}/{fuel['capacity']}")
        return True
    else:
        logger.error("❌ Navigation failed")
        return False
