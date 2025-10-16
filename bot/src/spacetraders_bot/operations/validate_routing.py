"""Routing validation CLI operation."""

import logging
from typing import Optional

from spacetraders_bot.core.api_client import APIClient
from spacetraders_bot.core.database import get_database
from spacetraders_bot.core.routing_validator import RoutingValidator
from spacetraders_bot.core.routing_pause import get_pause_details, is_paused
from spacetraders_bot.operations.common import setup_logging


def validate_routing_operation(args) -> bool:
    """Entry point for the routing validation CLI command."""

    log_file = setup_logging("validate-routing", args.ship, getattr(args, "log_level", "INFO"))
    logger = logging.getLogger(__name__)
    logger.info("Routing validation log: %s", log_file)

    print("=" * 70)
    print("ROUTING VALIDATION")
    print("=" * 70)

    db = get_database()
    with db.connection() as conn:
        player = db.get_player_by_id(conn, args.player_id)

    if not player:
        print(f"❌ Player ID {args.player_id} not found")
        return False

    api = APIClient(token=player["token"])

    ship_status = api.get_ship(args.ship)
    if not ship_status:
        print(f"❌ Failed to load ship {args.ship}")
        return False

    current_location = ship_status["nav"]["waypointSymbol"]
    system_symbol = ship_status["nav"]["systemSymbol"]
    destination = args.destination or current_location

    if destination == current_location and not args.dry_run:
        print("⚠️  Ship already at destination; specify a different destination for validation")
        return False

    validator = RoutingValidator(api, system_symbol)
    result = validator.validate_route(
        ship_symbol=args.ship,
        destination=destination,
        execute=not args.dry_run,
    )

    if not result:
        print("❌ Routing validation could not be completed")
        return False

    print(f"Ship: {result.ship_symbol}")
    print(f"Start: {result.start_waypoint}")
    print(f"Destination: {result.destination}")
    print("-")
    print("Predicted:")
    print(f"  Time: {result.predicted_time:.1f} s")
    print(f"  Fuel: {result.predicted_fuel_cost} units")

    if args.dry_run:
        print("(dry-run) Navigation not executed; no actual metrics available")
        return True

    print("Actual:")
    print(f"  Time: {result.actual_time:.1f} s")
    print(f"  Fuel: {result.actual_fuel_cost} units")

    print("Deviation:")
    print(f"  Time: {result.time_deviation_pct:.2f}%")
    print(f"  Fuel: {result.fuel_deviation_pct:.2f}%")

    if result.passed:
        print("✅ VALIDATION PASSED (within configured thresholds)")
        if is_paused():
            print("⚠️  Routing remains paused; investigate lingering pause file.")
        return True

    print("❌ VALIDATION FAILED (exceeds deviation threshold)")
    if validator.pause_on_failure:
        details = get_pause_details() or {}
        print(f"⚠️  Routing operations paused: {details.get('reason', 'Validation failure')}")
    return False
