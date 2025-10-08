#!/usr/bin/env python3
"""
Contract operations: contract fulfillment and negotiation
"""

import logging
import time
from datetime import datetime, timezone
from typing import Dict, List, Optional, Tuple

from spacetraders_bot.core.database import Database
from spacetraders_bot.core.ship_controller import ShipController
from spacetraders_bot.core.smart_navigator import SmartNavigator
from spacetraders_bot.operations.common import (
    setup_logging,
    format_credits,
    get_api_client,
    get_captain_logger,
    log_captain_event,
    humanize_duration,
    get_operator_name,
)


def contract_operation(
    args,
    *,
    api=None,
    ship=None,
    navigator=None,
    db=None,
    sleep_fn=time.sleep,
):
    """Contract fulfillment operation"""
    log_file = setup_logging("contract", args.ship, getattr(args, 'log_level', 'INFO'))

    print("=" * 70)
    print("CONTRACT FULFILLMENT OPERATION")
    print("=" * 70)

    api = api or get_api_client(args.player_id)
    ship = ship or ShipController(api, args.ship)

    operation_start = datetime.now(timezone.utc)
    captain_logger = get_captain_logger(args.player_id)
    local_db = db or Database()
    operator_name = get_operator_name(args)

    def log_error(error: str, cause: str, *, impact: Optional[Dict] = None,
                  resolution: str = "Manual follow-up", lesson: str = "Review contract flow",
                  escalate: bool = False, tags: Optional[List[str]] = None) -> None:
        log_captain_event(
            captain_logger,
            'CRITICAL_ERROR',
            operator=operator_name,
            ship=args.ship,
            error=error,
            cause=cause,
            impact=impact or {},
            resolution=resolution,
            lesson=lesson,
            escalate=escalate,
            tags=tags or ['contract']
        )

    def log_completion(stats_snapshot: dict, destination: str, trade_symbol: str) -> None:
        duration = humanize_duration(datetime.now(timezone.utc) - operation_start)
        results = {
            'Units Delivered': f"{stats_snapshot['units_delivered']}/{stats_snapshot['units_required']}",
            'Trips': stats_snapshot['trips'],
            'Purchased Units': stats_snapshot['purchased_units'],
            'Gross Payment': f"{stats_snapshot['payment']:,} cr",
            'Purchase Spend': f"{stats_snapshot['purchase_spent']:,} cr",
            'Net Profit': f"{stats_snapshot['payment'] - stats_snapshot['purchase_spent']:,} cr",
        }
        notes = f"Fulfilled contract {args.contract_id} delivering {trade_symbol} to {destination}."
        log_captain_event(
            captain_logger,
            'OPERATION_COMPLETED',
            operator=operator_name,
            ship=args.ship,
            duration=duration,
            results=results,
            notes=notes,
            tags=['contract', trade_symbol.lower(), destination.lower()]
        )

    def log_performance(stats_snapshot: dict) -> None:
        elapsed = datetime.now(timezone.utc) - operation_start
        hours = max(elapsed.total_seconds() / 3600, 0.0001)
        revenue = stats_snapshot['payment'] - stats_snapshot['purchase_spent']
        rate = int(revenue / hours) if revenue else 0
        log_captain_event(
            captain_logger,
            'PERFORMANCE_SUMMARY',
            summary_type='Contract Fulfillment',
            financials={
                'revenue': revenue,
                'cumulative': revenue,
                'rate': rate,
            },
            operations={'completed': 1, 'active': 0, 'success_rate': 100},
            fleet={'active': 1, 'total': 1},
            top_performers=[{
                'ship': args.ship,
                'profit': revenue,
                'operation': 'contract'
            }],
            tags=['contract', 'performance']
        )

    def ensure_market_availability(still_needed: int, cargo_available: int) -> bool:
        print(f"  Cargo space available: {cargo_available}/{cargo_capacity}")

        if args.buy_from:
            print(f"  ✅ Using specified market: {args.buy_from}")
            market_row = _fetch_market_listing(local_db, delivery['tradeSymbol'], args.buy_from)

            if market_row:
                total_cost = market_row[1] * still_needed
                print(f"     Price: {market_row[1]} cr/unit ({market_row[2]} supply)")
                print(f"     Total cost: {format_credits(total_cost)} for {still_needed} units")
            else:
                print(
                    f"  ⚠️  Warning: Market {args.buy_from} not found in database or doesn't sell {delivery['tradeSymbol']}"
                )
                print("  Will attempt purchase anyway (market may not be scouted yet)")
            return True

        print(f"\n  🔍 Searching for markets selling {delivery['tradeSymbol']}...")
        market_row = _find_lowest_price_market(local_db, delivery['tradeSymbol'], system)

        if market_row:
            args.buy_from = market_row[0]
            total_cost = market_row[1] * still_needed
            print(f"  ✅ Found market: {args.buy_from}")
            print(f"     Price: {market_row[1]} cr/unit ({market_row[2]} supply)")
            print(f"     Total cost: {format_credits(total_cost)} for {still_needed} units")
            return True

        print(f"  ❌ RESOURCE NOT AVAILABLE")
        print(f"  {delivery['tradeSymbol']} is not available in any discovered markets in {system}")
        print(f"\n  📋 RECOMMENDED ACTIONS:")
        print(f"     1. Deploy scout coordinator to system {system}")
        print(f"     2. Wait for scouts to discover markets selling {delivery['tradeSymbol']}")
        print(f"     3. Re-run contract with --buy-from <market> once discovered")
        print(f"\n  🔄 Contract operation will wait and periodically retry...")

        log_error(
            "Resource not available in markets",
            f"{delivery['tradeSymbol']} not found in any discovered markets",
            impact={'Resource': delivery['tradeSymbol'], 'System': system, 'Required': still_needed},
            resolution="Deploy scout coordinator to discover markets, then retry",
            escalate=True,
            tags=['contract', 'market_missing', delivery['tradeSymbol'].lower()]
        )

        max_retries = 12
        retry_interval = 300
        for retry_count in range(max_retries):
            print(
                f"\n  ⏳ Waiting {retry_interval // 60} minutes before retry {retry_count + 1}/{max_retries}..."
            )
            sleep_fn(retry_interval)
            print(f"  🔍 Retry {retry_count + 1}: Checking market database...")

            market_row = _find_lowest_price_market(local_db, delivery['tradeSymbol'], system)
            if market_row:
                print(f"  ✅ SUCCESS! Market discovered: {market_row[0]}")
                args.buy_from = market_row[0]
                total_cost = market_row[1] * still_needed
                print(f"     Price: {market_row[1]} cr/unit ({market_row[2]} supply)")
                print(f"     Total cost: {format_credits(total_cost)} for {still_needed} units")
                return True

            print(f"  ⚠️  Still not available (retry {retry_count + 1}/{max_retries})")

        print(
            f"\n  ❌ OPERATION FAILED: Resource never became available after {max_retries * retry_interval // 60} minutes"
        )
        print("  Manual intervention required - Flag Captain must deploy scout coordinator")
        log_error(
            "Contract operation timeout",
            f"Resource {delivery['tradeSymbol']} not discovered after waiting {max_retries * retry_interval // 60} minutes",
            impact={
                'Resource': delivery['tradeSymbol'],
                'System': system,
                'Waited': f"{max_retries * retry_interval // 60} min",
            },
            resolution="Flag Captain must manually deploy scouts or source resource elsewhere",
            escalate=True,
            tags=['contract', 'timeout', delivery['tradeSymbol'].lower()]
        )
        return False



    def _run_delivery_loop(
        ship,
        navigator,
        api,
        args,
        delivery,
        remaining,
        purchase_units_for_trip,
        stats,
        log_error,
        sleep_fn,
        fulfilled,
    ) -> tuple[int, bool]:
        total_delivered = 0
        trip = 1
        max_trips = 10

        while total_delivered < remaining and trip <= max_trips:
            ship_data = ship.get_status()
            current_cargo = ship_data['cargo']['inventory']

            to_deliver = next((item['units'] for item in current_cargo if item['symbol'] == delivery['tradeSymbol']), 0)

            if to_deliver == 0:
                still_need = remaining - total_delivered
                cargo_available = ship_data['cargo']['capacity'] - ship_data['cargo']['units']
                units_bought, continue_operation = purchase_units_for_trip(still_need, cargo_available, trip)

                if not continue_operation:
                    return total_delivered, False

                to_deliver = units_bought
                if units_bought == 0:
                    print(f"  ❌ Failed to purchase any units, skipping delivery")
                    break

            print(f"  Trip {trip}: Delivering {to_deliver} units...")
            navigator.execute_route(ship, delivery['destinationSymbol'], prefer_cruise=True)

            if not ship.dock():
                print(f"  ❌ Failed to dock at {delivery['destinationSymbol']}")
                log_error(
                    "Docking failure",
                    f"Unable to dock at delivery destination on trip {trip}",
                    impact={'Delivered': total_delivered, 'Remaining': remaining - total_delivered},
                    resolution="Check ship status and retry",
                    escalate=True,
                )
                return total_delivered, False

            if not _attempt_delivery(api, args, delivery, to_deliver, total_delivered, remaining, trip, log_error, sleep_fn):
                return total_delivered, False

            total_delivered += to_deliver
            stats['units_delivered'] = fulfilled + total_delivered
            stats['trips'] = trip
            trip += 1

        return total_delivered, total_delivered >= remaining

    def _attempt_delivery(api, args, delivery, units, delivered_so_far, remaining, trip, log_error, sleep_fn):
        max_delivery_retries = 3
        for retry in range(max_delivery_retries):
            result = api.post(f"/my/contracts/{args.contract_id}/deliver", {
                "shipSymbol": args.ship,
                "tradeSymbol": delivery['tradeSymbol'],
                "units": units
            })

            if result and 'data' in result:
                print(f"  ✅ Delivered {units} units (total: {delivered_so_far + units}/{remaining})")
                return True

            error_code = result.get('error', {}).get('code') if result else None
            error_msg = result.get('error', {}).get('message', 'Unknown error') if result else 'No response'

            if error_code == 4502 and retry < max_delivery_retries - 1:
                print(f"  ⚠️  Delivery failed (error {error_code}): {error_msg}")
                print(f"  🔄 Retry {retry + 1}/{max_delivery_retries - 1}...")
                sleep_fn(2)
                continue

            print(f"  ❌ Delivery failed: {error_msg} (Code: {error_code})")
            log_error(
                "Delivery API failure",
                f"/deliver call returned error {error_code}: {error_msg} on trip {trip}",
                impact={'Delivered': delivered_so_far, 'Remaining': remaining - delivered_so_far},
                resolution="Retry delivery manually or check contract requirements",
                escalate=True,
            )
            return False

        return False

    def purchase_units_for_trip(still_needed: int, cargo_available: int, trip_number: int) -> Tuple[int, bool]:
        if cargo_available <= 0:
            print("  ⚠️  No cargo space and nothing to deliver")
            return 0, True

        to_buy = min(still_needed, cargo_available)
        if to_buy <= 0:
            return 0, True

        print(f"\n  Trip {trip_number}: Buying {to_buy} more units from {args.buy_from}...")
        if not navigator.execute_route(ship, args.buy_from, prefer_cruise=True):
            log_error(
                "Navigation failure",
                f"Unable to navigate to market {args.buy_from}",
                impact={'Market': args.buy_from},
                resolution="Check fuel and route feasibility",
                escalate=True,
            )
            return 0, False

        ship.dock()

        total_bought = 0
        remaining_to_buy = to_buy

        while remaining_to_buy > 0:
            batch_size = remaining_to_buy
            transaction = ship.buy(delivery['tradeSymbol'], batch_size)

            if transaction:
                units_bought = transaction.get('units', batch_size)
                total_bought += units_bought
                remaining_to_buy -= units_bought
                stats['purchase_spent'] += transaction.get('totalPrice', 0)
                stats['purchased_units'] += units_bought

                if remaining_to_buy > 0:
                    print(f"  ✅ Bought {units_bought} units, {remaining_to_buy} more to go...")
                continue

            result = ship.api.request("GET", f"/my/ships/{ship.ship_symbol}")
            if result and batch_size > 20:
                print("  ⚠️  Transaction limit hit, reducing batch to 20 units...")
                batch_size = 20
                transaction = ship.buy(delivery['tradeSymbol'], batch_size)
                if transaction:
                    units_bought = transaction.get('units', batch_size)
                    total_bought += units_bought
                    remaining_to_buy -= units_bought
                    stats['purchase_spent'] += transaction.get('totalPrice', 0)
                    stats['purchased_units'] += units_bought
                    continue

            log_error(
                "Purchase failed",
                f"Unable to buy {remaining_to_buy} units from {args.buy_from} during trip {trip_number}",
                impact={'Bought': total_bought, 'Failed': remaining_to_buy, 'Remaining': still_needed},
                resolution="Check market supply and transaction limits",
                escalate=True,
            )
            break

        return total_bought, True

    # Initialize navigator
    ship_data = ship.get_status()
    if not ship_data:
        print("❌ Failed to get ship status")
        log_error(
            "Ship status unavailable",
            "API returned no ship data",
            resolution="Verify ship readiness",
            escalate=True
        )
        return 1

    system = ship_data['nav']['systemSymbol']
    navigator = navigator or SmartNavigator(api, system)

    # Get contract details
    print("1. Getting contract details...")
    contract = api.get_contract(args.contract_id)

    if not contract:
        print("❌ Failed to get contract")
        log_error(
            "Contract lookup failed",
            f"API returned no data for contract {args.contract_id}",
            resolution="Verify contract ID",
            escalate=True
        )
        return 1

    # Accept if not accepted
    if not contract['accepted']:
        print("2. Accepting contract...")
        result = api.post(f"/my/contracts/{args.contract_id}/accept")
        if result:
            contract = result['data']['contract']
            print(f"✅ Accepted! Payment: {contract['terms']['payment']['onAccepted']:,} credits")
        else:
            print("❌ Failed to accept")
            log_error(
                "Contract acceptance failed",
                "API call /accept returned error",
                resolution="Attempt manual acceptance",
                escalate=True
            )
            return 1

    # Get delivery requirements
    delivery = contract['terms']['deliver'][0]
    required = delivery['unitsRequired']
    fulfilled = delivery['unitsFulfilled']
    remaining = required - fulfilled

    print(f"\nDelivery Requirements:")
    print(f"  Resource: {delivery['tradeSymbol']}")
    print(f"  Required: {required}")
    print(f"  Fulfilled: {fulfilled}")
    print(f"  Remaining: {remaining}")
    print(f"  Destination: {delivery['destinationSymbol']}")

    if remaining == 0:
        print("\n✅ Contract already fulfilled!")
        return 0

    stats = {
        'units_required': required,
        'units_delivered': fulfilled,
        'trips': 0,
        'purchased_units': 0,
        'purchase_spent': 0,
        'payment': 0,
    }

    # Acquire resources (PURCHASE ONLY - mining removed for efficiency)
    print(f"\n3. Acquiring resources...")

    # Check current cargo first
    ship_data = ship.get_status()
    current_cargo = ship_data['cargo']['inventory']
    cargo_capacity = ship_data['cargo']['capacity']
    cargo_units = ship_data['cargo']['units']

    # Count how many of the required resource we already have
    already_have = 0
    for item in current_cargo:
        if item['symbol'] == delivery['tradeSymbol']:
            already_have = item['units']
            break

    still_need = remaining - already_have
    print(f"  Already have: {already_have} units")
    print(f"  Still need: {still_need} units")

    if still_need > 0:
        cargo_available = cargo_capacity - cargo_units
        if cargo_available <= 0:
            print("  ⚠️  No cargo space available to acquire additional goods")

        if not ensure_market_availability(still_need, cargo_available):
            return 1

        # Purchase from identified market
        if cargo_available < still_need:
            print(f"\n  ⚠️  Not enough cargo space! Will need multiple trips")
            # Buy what we can fit now
            to_buy = cargo_available
        else:
            to_buy = still_need

        if to_buy > 0:
            print(f"\n  💰 Purchasing {to_buy} units of {delivery['tradeSymbol']} from {args.buy_from}...")
            nav_success = navigator.execute_route(ship, args.buy_from, prefer_cruise=True)
            if not nav_success:
                print(f"  ❌ Navigation to {args.buy_from} failed")
                log_error(
                    "Navigation failure",
                    f"Unable to navigate to market {args.buy_from}",
                    impact={'Market': args.buy_from},
                    resolution="Check fuel and route feasibility",
                    escalate=True
                )
                return 1

            ship.dock()
            transaction = ship.buy(delivery['tradeSymbol'], to_buy)
            if transaction:
                stats['purchase_spent'] += transaction.get('totalPrice', 0)
                stats['purchased_units'] += transaction.get('units', to_buy)
                print(f"  ✅ Purchased {transaction.get('units', to_buy)} units for {format_credits(transaction.get('totalPrice', 0))}")
            else:
                print(f"  ❌ Purchase failed at {args.buy_from}")
                log_error(
                    "Purchase failed",
                    f"Unable to buy {to_buy} units from {args.buy_from}",
                    impact={'Units remaining': still_need},
                    resolution="Verify market availability and ship credits",
                    escalate=True
                )
                return 1

    total_delivered, success = _run_delivery_loop(
        ship=ship,
        navigator=navigator,
        api=api,
        args=args,
        delivery=delivery,
        remaining=remaining,
        purchase_units_for_trip=purchase_units_for_trip,
        stats=stats,
        log_error=log_error,
        sleep_fn=sleep_fn,
        fulfilled=fulfilled,
    )

    if not success:
        return 1

    if total_delivered >= remaining:
        # Fulfill contract
        print("\n5. Fulfilling contract...")
        result = api.post(f"/my/contracts/{args.contract_id}/fulfill")
        if result and 'data' in result and 'contract' in result['data']:
            payment = result['data']['contract']['terms']['payment']['onFulfilled']
            stats['payment'] = payment
            print(f"🎉 Contract fulfilled! Payment: {payment:,} credits")
            log_completion(stats, delivery['destinationSymbol'], delivery['tradeSymbol'])
            log_performance(stats)
            return 0
        else:
            error_msg = result.get('error', {}).get('message', 'Unknown error') if result else 'No response'
            error_code = result.get('error', {}).get('code', 'N/A') if result else 'N/A'
            print(f"❌ Failed to fulfill contract (delivery complete but fulfill failed)")
            print(f"   Error: {error_msg} (Code: {error_code})")
            log_error(
                "Fulfillment API failure",
                f"/fulfill call returned error: {error_msg} (Code: {error_code})",
                impact={'Delivered': total_delivered, 'Remaining': 0},
                resolution="Submit fulfill request manually",
                escalate=True
            )
            return 1
    else:
        print(f"❌ Failed to complete delivery ({total_delivered}/{remaining} delivered)")
        log_error(
            "Contract incomplete",
            "Insufficient units delivered",
            impact={'Delivered': total_delivered, 'Required': remaining},
            resolution="Acquire remaining goods and redeliver",
            escalate=True
        )
        return 1


def negotiate_operation(args, *, api=None):
    """Negotiate a new contract - replaces negotiate_contract.sh"""
    log_file = setup_logging("negotiate", args.ship, getattr(args, 'log_level', 'INFO'))

    print("=" * 70)
    print("NEGOTIATE CONTRACT")
    print("=" * 70)

    api = api or get_api_client(args.player_id)

    print(f"Negotiating contract with ship {args.ship}...\n")

    result = api.post(f"/my/ships/{args.ship}/negotiate/contract")

    if result and 'data' in result:
        contract = result['data']['contract']

        print("✅ Contract negotiated successfully!\n")
        print(f"Contract ID: {contract['id']}")
        print(f"Type: {contract['type']}")
        print(f"Faction: {contract['factionSymbol']}")

        terms = contract['terms']
        print(f"\nPayment:")
        print(f"  On Accept: {format_credits(terms['payment']['onAccepted'])}")
        print(f"  On Fulfill: {format_credits(terms['payment']['onFulfilled'])}")

        if terms.get('deliver'):
            print(f"\nDelivery Requirements:")
            for delivery in terms['deliver']:
                print(f"  - {delivery['unitsRequired']} x {delivery['tradeSymbol']}")
                print(f"    Destination: {delivery['destinationSymbol']}")

        print(f"\nDeadline to Accept: {contract['deadlineToAccept']}")
        print(f"Deadline to Fulfill: {terms['deadline']}")

        print("\n" + "=" * 70)
        return 0
    else:
        print("❌ Failed to negotiate contract")
        return 1
def _fetch_market_listing(db: Database, trade_symbol: str, waypoint_symbol: str) -> Optional[Tuple[str, int, str]]:
    """Return market listing tuple for specific waypoint or None."""
    with db.connection() as conn:
        cursor = conn.execute(
            """
            SELECT waypoint_symbol, sell_price, supply
            FROM market_data
            WHERE good_symbol = ?
              AND waypoint_symbol = ?
              AND sell_price IS NOT NULL
            """,
            (trade_symbol, waypoint_symbol),
        )
        return cursor.fetchone()


def _find_lowest_price_market(db: Database, trade_symbol: str, system_prefix: str) -> Optional[Tuple[str, int, str]]:
    """Return the cheapest market tuple within given system prefix."""
    pattern = f"{system_prefix}%"
    with db.connection() as conn:
        cursor = conn.execute(
            """
            SELECT waypoint_symbol, sell_price, supply
            FROM market_data
            WHERE good_symbol = ?
              AND sell_price IS NOT NULL
              AND waypoint_symbol LIKE ?
            ORDER BY sell_price ASC
            LIMIT 1
            """,
            (trade_symbol, pattern),
        )
        return cursor.fetchone()
