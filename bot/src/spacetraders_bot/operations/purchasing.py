#!/usr/bin/env python3
"""Ship purchasing operation."""

import logging
from dataclasses import dataclass
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


@dataclass
class PurchasePlan:
    """Represents how many ships can be purchased and the financial limits."""

    price: int
    requested_quantity: int
    purchasable_quantity: int
    max_budget: int
    available_credits: int


def _validate_purchase_args(args):
    required_args = ["player_id", "ship", "shipyard", "ship_type", "max_budget"]
    for arg_name in required_args:
        if not hasattr(args, arg_name) or getattr(args, arg_name) in (None, ""):
            print(f"❌ Missing required argument: {arg_name}")
            return None

    try:
        quantity = int(getattr(args, "quantity", 1))
    except (TypeError, ValueError):
        print("❌ Quantity must be an integer")
        return None
    if quantity <= 0:
        print("❌ Quantity must be greater than zero")
        return None

    try:
        max_budget = int(getattr(args, "max_budget"))
    except (TypeError, ValueError):
        print("❌ max_budget must be an integer value in credits")
        return None
    if max_budget <= 0:
        print("❌ max_budget must be greater than zero")
        return None

    return quantity, max_budget


def _ensure_ship_ready(ship: ShipController, shipyard_symbol: str, log_error) -> bool:
    ship_status = ship.get_status()
    if not ship_status:
        print("❌ Failed to retrieve ship status")
        log_error("Ship status unavailable", "API returned no data")
        return False

    current_waypoint = ship_status["nav"]["waypointSymbol"]
    if current_waypoint != shipyard_symbol:
        logging.info("Navigating %s to %s", ship.ship_symbol, shipyard_symbol)
        print(f"🚀 Navigating {ship.ship_symbol} to {shipyard_symbol}...")
        if not ship.navigate(shipyard_symbol):
            print("❌ Failed to navigate to shipyard")
            log_error("Navigation failed", f"Unable to reach {shipyard_symbol}")
            return False

    print("🛬 Docking purchasing ship...")
    if not ship.dock():
        print("❌ Failed to dock at shipyard")
        log_error("Dock failed", "Shipyard docking endpoint returned error")
        return False

    return True


def _fetch_shipyard_listing(api, shipyard_symbol: str, ship_type: str, log_error):
    system_symbol, _ = parse_waypoint_symbol(shipyard_symbol)
    logging.info("Retrieving shipyard listings from %s", shipyard_symbol)
    shipyard_response = api.get(f"/systems/{system_symbol}/waypoints/{shipyard_symbol}/shipyard")
    if not shipyard_response or "data" not in shipyard_response:
        print("❌ Failed to load shipyard data")
        log_error("Shipyard unavailable", "API response missing data")
        return None, None

    shipyard_data = shipyard_response["data"]
    listings = shipyard_data.get("ships") or []
    listing = next((s for s in listings if s.get("type") == ship_type), None)
    if not listing:
        print(f"❌ Ship type {ship_type} not available at {shipyard_symbol}")
        log_error("Ship type unavailable", f"No listing for {ship_type}")
        return None, None

    price = listing.get("purchasePrice")
    if price is None:
        print("❌ Ship listing missing purchase price")
        log_error("Missing price", "Shipyard listing lacked purchasePrice")
        return None, None

    return listing, price


def _load_agent_credits(api, log_error) -> Optional[int]:
    agent = api.get_agent()
    if not agent:
        print("❌ Failed to load agent data")
        log_error("Agent data unavailable", "API returned no agent info")
        return None

    return int(agent.get("credits", 0))


def _calculate_purchase_plan(quantity: int, price: int, credits: int, max_budget: int, log_error) -> Optional[PurchasePlan]:
    max_by_budget = max_budget // price if price else 0
    max_by_credits = credits // price if price else 0
    purchasable_quantity = min(quantity, max_by_budget, max_by_credits)

    if purchasable_quantity <= 0:
        print(
            "❌ Not enough budget or credits to purchase even one ship. "
            f"Price per ship: {format_credits(price)}"
        )
        log_error(
            "Insufficient funds",
            f"Credits: {credits:,}, Budget: {max_budget:,}, Price: {price:,}",
            escalate=False,
        )
        return None

    return PurchasePlan(
        price=price,
        requested_quantity=quantity,
        purchasable_quantity=purchasable_quantity,
        max_budget=max_budget,
        available_credits=credits,
    )


def _execute_purchases(
    api,
    shipyard_symbol: str,
    ship_type: str,
    purchasable_quantity: int,
    price: int,
    available_credits: int,
    max_budget: int,
    captain_logger,
    operator_name: str,
    ship_symbol: str,
    log_error,
):
    purchased_symbols: List[str] = []
    total_spent = 0
    agent_credits = available_credits

    for idx in range(purchasable_quantity):
        remaining_budget = max_budget - total_spent
        if remaining_budget < price:
            logging.info("Budget exhausted before purchase %d", idx + 1)
            break

        if agent_credits < price:
            logging.info("Credits exhausted before purchase %d", idx + 1)
            break

        print(f"🛒 Purchasing {ship_type} #{idx + 1}...")
        payload = {"shipType": ship_type, "waypointSymbol": shipyard_symbol}
        response = api.post("/my/ships", payload)
        if not response or "data" not in response:
            print("❌ Purchase failed - see logs for details")
            log_error("Purchase failed", f"API response: {response}", escalate=False)
            break

        data = response["data"]
        new_ship = data.get("ship")
        transaction = data.get("transaction", {})
        agent = data.get("agent")
        if agent:
            agent_credits = int(agent.get("credits", agent_credits - price))
        else:
            agent_credits -= price

        spent = int(transaction.get("totalPrice", price))
        total_spent += spent

        new_symbol = new_ship.get("symbol") if new_ship else None
        if new_symbol:
            purchased_symbols.append(new_symbol)

        logging.info(
            "Purchased %s for %s credits (remaining credits: %s)",
            new_symbol or "<unknown>",
            format_credits(spent),
            format_credits(agent_credits),
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

    return purchased_symbols, total_spent, agent_credits


def purchase_ship_operation(args, *, api=None, ship=None, captain_logger=None):
    """Purchase one or more ships from a shipyard using the supplied hauler."""

    log_file = setup_logging(
        "purchase_ship",
        getattr(args, "ship", "purchasing_ship"),
        getattr(args, "log_level", "INFO"),
    )
    logging.info("Purchase ship operation initialized")

    parsed_args = _validate_purchase_args(args)
    if not parsed_args:
        return 1

    quantity, max_budget = parsed_args

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
    if not _ensure_ship_ready(ship, shipyard_symbol, log_error):
        return 1

    _, price = _fetch_shipyard_listing(api, shipyard_symbol, ship_type, log_error)
    if price is None:
        return 1

    available_credits = _load_agent_credits(api, log_error)
    if available_credits is None:
        return 1

    plan = _calculate_purchase_plan(quantity, price, available_credits, max_budget, log_error)
    if not plan:
        return 1

    logging.info(
        "Purchasing up to %d ships (requested=%d, price=%d, credits=%d, budget=%d)",
        plan.purchasable_quantity,
        plan.requested_quantity,
        plan.price,
        plan.available_credits,
        plan.max_budget,
    )

    purchased_symbols, total_spent, available_credits = _execute_purchases(
        api=api,
        shipyard_symbol=shipyard_symbol,
        ship_type=ship_type,
        purchasable_quantity=plan.purchasable_quantity,
        price=plan.price,
        available_credits=plan.available_credits,
        max_budget=plan.max_budget,
        captain_logger=captain_logger,
        operator_name=operator_name,
        ship_symbol=ship_symbol,
        log_error=log_error,
    )

    if not purchased_symbols:
        print("⚠️ No ships were purchased. Credits or budget may have been exhausted.")
        return 1

    print("\n✅ Purchase summary:")
    print(f"  Ships bought: {len(purchased_symbols)} / {plan.requested_quantity} requested")
    print(f"  Total spent: {format_credits(total_spent)} (budget cap: {format_credits(plan.max_budget)})")
    print(f"  Remaining credits: {format_credits(available_credits)}")
    print("  New ship symbols:")
    for symbol in purchased_symbols:
        print(f"   • {symbol}")

    return 0
