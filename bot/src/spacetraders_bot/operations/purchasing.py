#!/usr/bin/env python3
"""Ship purchasing operation."""

import logging
from typing import List, Optional

from spacetraders_bot.core.ship_controller import ShipController
from spacetraders_bot.core.utils import format_credits, parse_waypoint_symbol

from spacetraders_bot.operations.common import (
    get_api_client,
    get_captain_logger,
    get_operator_name,
    log_captain_event,
    setup_logging,
)


def purchase_ship_operation(args, *, api=None, ship=None, captain_logger=None):
    """Purchase one or more ships from a shipyard using the supplied hauler."""

    log_file = setup_logging(
        "purchase_ship",
        getattr(args, "ship", "purchasing_ship"),
        getattr(args, "log_level", "INFO"),
    )
    logging.info("Purchase ship operation initialized")

    required_args = ["player_id", "ship", "shipyard", "ship_type", "max_budget"]
    for arg_name in required_args:
        if not hasattr(args, arg_name) or getattr(args, arg_name) in (None, ""):
            print(f"❌ Missing required argument: {arg_name}")
            return 1

    try:
        quantity = int(getattr(args, "quantity", 1))
    except (TypeError, ValueError):
        print("❌ Quantity must be an integer")
        return 1
    if quantity <= 0:
        print("❌ Quantity must be greater than zero")
        return 1

    try:
        max_budget = int(getattr(args, "max_budget"))
    except (TypeError, ValueError):
        print("❌ max_budget must be an integer value in credits")
        return 1
    if max_budget <= 0:
        print("❌ max_budget must be greater than zero")
        return 1

    ship_symbol = args.ship
    shipyard_symbol = args.shipyard
    ship_type = args.ship_type
    operator_name = get_operator_name(args)

    api = api or get_api_client(args.player_id)
    ship = ship or ShipController(api, ship_symbol)
    captain_logger = captain_logger or get_captain_logger(args.player_id)

    def log_error(error: str, cause: str, *, escalate: bool = False, extra_tags: Optional[List[str]] = None):
        log_captain_event(
            captain_logger,
            "CRITICAL_ERROR",
            operator=operator_name,
            ship=ship_symbol,
            error=error,
            cause=cause,
            resolution="Review shipyard plan and retry",
            tags=["purchasing", ship_type.lower(), shipyard_symbol.lower()] + (extra_tags or []),
            escalate=escalate,
        )

    # Ensure purchasing ship can reach/dock at the shipyard
    ship_status = ship.get_status()
    if not ship_status:
        print("❌ Failed to retrieve ship status")
        log_error("Ship status unavailable", "API returned no data")
        return 1

    current_waypoint = ship_status["nav"]["waypointSymbol"]
    if current_waypoint != shipyard_symbol:
        logging.info("Navigating %s to %s", ship_symbol, shipyard_symbol)
        print(f"🚀 Navigating {ship_symbol} to {shipyard_symbol}...")
        if not ship.navigate(shipyard_symbol):
            print("❌ Failed to navigate to shipyard")
            log_error("Navigation failed", f"Unable to reach {shipyard_symbol}")
            return 1

    # Ensure docked before interacting with shipyard
    print("🛬 Docking purchasing ship...")
    if not ship.dock():
        print("❌ Failed to dock at shipyard")
        log_error("Dock failed", "Shipyard docking endpoint returned error")
        return 1

    # Fetch shipyard listings
    system_symbol, _ = parse_waypoint_symbol(shipyard_symbol)
    logging.info("Retrieving shipyard listings from %s", shipyard_symbol)
    shipyard_response = api.get(f"/systems/{system_symbol}/waypoints/{shipyard_symbol}/shipyard")
    if not shipyard_response or "data" not in shipyard_response:
        print("❌ Failed to load shipyard data")
        log_error("Shipyard unavailable", "API response missing data")
        return 1

    shipyard_data = shipyard_response["data"]
    listings = shipyard_data.get("ships") or []
    listing = next((s for s in listings if s.get("type") == ship_type), None)
    if not listing:
        print(f"❌ Ship type {ship_type} not available at {shipyard_symbol}")
        log_error("Ship type unavailable", f"No listing for {ship_type}")
        return 1

    price = listing.get("purchasePrice")
    if price is None:
        print("❌ Ship listing missing purchase price")
        log_error("Missing price", "Shipyard listing lacked purchasePrice")
        return 1

    agent = api.get_agent()
    if not agent:
        print("❌ Failed to load agent data")
        log_error("Agent data unavailable", "API returned no agent info")
        return 1

    available_credits = int(agent.get("credits", 0))
    max_by_budget = max_budget // price
    max_by_credits = available_credits // price
    purchasable_quantity = min(quantity, max_by_budget, max_by_credits)

    if purchasable_quantity <= 0:
        print(
            "❌ Not enough budget or credits to purchase even one ship. "
            f"Price per ship: {format_credits(price)}"
        )
        log_error(
            "Insufficient funds",
            f"Credits: {available_credits:,}, Budget: {max_budget:,}, Price: {price:,}",
            escalate=False,
        )
        return 1

    logging.info(
        "Purchasing up to %d ships (requested=%d, price=%d, credits=%d, budget=%d)",
        purchasable_quantity,
        quantity,
        price,
        available_credits,
        max_budget,
    )

    purchased_symbols: List[str] = []
    total_spent = 0

    for idx in range(purchasable_quantity):
        remaining_budget = max_budget - total_spent
        if remaining_budget < price:
            logging.info("Budget exhausted before purchase %d", idx + 1)
            break

        if available_credits < price:
            logging.info("Credits exhausted before purchase %d", idx + 1)
            break

        print(f"🛒 Purchasing {ship_type} #{idx + 1}...")
        payload = {
            "shipType": ship_type,
            "waypointSymbol": shipyard_symbol,
        }
        response = api.post("/my/ships", payload)
        if not response or "data" not in response:
            print("❌ Purchase failed - see logs for details")
            log_error("Purchase failed", f"API response: {response}", escalate=False)
            break

        data = response["data"]
        new_ship = data.get("ship")
        transaction = data.get("transaction", {})
        agent = data.get("agent") or agent
        available_credits = int(agent.get("credits", available_credits - price))
        spent = int(transaction.get("totalPrice", price))
        total_spent += spent

        new_symbol = new_ship.get("symbol") if new_ship else None
        if new_symbol:
            purchased_symbols.append(new_symbol)

        logging.info(
            "Purchased %s for %s credits (remaining credits: %s)",
            new_symbol or "<unknown>",
            format_credits(spent),
            format_credits(available_credits),
        )

        log_captain_event(
            captain_logger,
            "OPERATION_COMPLETED",
            operator=operator_name,
            ship=ship_symbol,
            results={
                "purchased_ship": new_symbol,
                "ship_type": ship_type,
                "price": format_credits(spent),
                "shipyard": shipyard_symbol,
            },
            notes=f"Purchased {ship_type} at {shipyard_symbol}",
            tags=["purchasing", ship_type.lower(), shipyard_symbol.lower()],
        )

    if not purchased_symbols:
        print("⚠️ No ships were purchased. Credits or budget may have been exhausted.")
        return 1

    print("\n✅ Purchase summary:")
    print(f"  Ships bought: {len(purchased_symbols)} / {quantity} requested")
    print(f"  Total spent: {format_credits(total_spent)} (budget cap: {format_credits(max_budget)})")
    print(f"  Remaining credits: {format_credits(available_credits)}")
    print("  New ship symbols:")
    for symbol in purchased_symbols:
        print(f"   • {symbol}")

    return 0
