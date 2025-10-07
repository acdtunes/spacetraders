#!/usr/bin/env python3
"""
Contract operations: contract fulfillment and negotiation
"""

import sys
import logging
from datetime import datetime, timezone
from pathlib import Path
from typing import Optional, List, Dict

# Add lib directory to path
sys.path.insert(0, str(Path(__file__).parent.parent / "lib"))

from api_client import APIClient
from ship_controller import ShipController
from smart_navigator import SmartNavigator
from database import Database
from .common import (
    setup_logging,
    format_credits,
    get_api_client,
    get_captain_logger,
    log_captain_event,
    humanize_duration,
    get_operator_name,
)
from .mining import targeted_mining_with_circuit_breaker, find_alternative_asteroids


def contract_operation(args):
    """Contract fulfillment operation"""
    log_file = setup_logging("contract", args.ship, getattr(args, 'log_level', 'INFO'))

    print("=" * 70)
    print("CONTRACT FULFILLMENT OPERATION")
    print("=" * 70)

    api = get_api_client(args.player_id)
    ship = ShipController(api, args.ship)

    operation_start = datetime.now(timezone.utc)
    captain_logger = get_captain_logger(args.player_id)
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
            'Mined Units': stats_snapshot['mined_units'],
            'Purchased Units': stats_snapshot['purchased_units'],
            'Gross Payment': f"{stats_snapshot['payment']:,} cr",
            'Purchase Spend': f"{stats_snapshot['purchase_spent']:,} cr",
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
    navigator = SmartNavigator(api, system)

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
        'mined_units': 0,
        'purchased_units': 0,
        'purchase_spent': 0,
        'payment': 0,
    }

    # Acquire resources (buy or mine with circuit breaker)
    if args.buy_from or hasattr(args, 'mine_from'):
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
            # Check cargo space
            cargo_available = cargo_capacity - cargo_units
            print(f"  Cargo space available: {cargo_available}/{cargo_capacity}")

            # Check market data FIRST to make intelligent decision
            print(f"\n  🔍 Checking if {delivery['tradeSymbol']} is available for purchase...")
            db = Database()
            with db.connection() as conn:
                cursor = conn.execute("""
                    SELECT waypoint_symbol, sell_price, supply
                    FROM market_data
                    WHERE good_symbol = ?
                      AND sell_price IS NOT NULL
                      AND waypoint_symbol LIKE ?
                    ORDER BY sell_price ASC
                    LIMIT 1
                """, (delivery['tradeSymbol'], f"{system}%"))
                market_row = cursor.fetchone()

            purchase_option = None
            should_buy_instead = False

            if market_row:
                purchase_option = {
                    'waypoint': market_row[0],
                    'price': market_row[1],
                    'supply': market_row[2]
                }
                total_cost = purchase_option['price'] * still_need
                print(f"  ✅ Found market: {purchase_option['waypoint']}")
                print(f"     Price: {purchase_option['price']} cr/unit ({purchase_option['supply']} supply)")
                print(f"     Total cost: {format_credits(total_cost)} for {still_need} units")

                # Calculate opportunity cost of mining
                time_to_mine_hours = (still_need / 4) * 80 / 3600  # Assume 4 units/extraction avg
                opportunity_cost = time_to_mine_hours * 50000  # 50k cr/hr conservative estimate

                print(f"     Mining time estimate: ~{time_to_mine_hours:.1f} hours")
                print(f"     Mining opportunity cost: {format_credits(int(opportunity_cost))}")

                # Check procurement mode override
                if hasattr(args, 'procurement_mode') and args.procurement_mode:
                    if args.procurement_mode == 'procure':
                        print(f"  💡 OVERRIDE: Procurement mode set to PROCURE (forced buying)")
                        should_buy_instead = True
                        args.buy_from = purchase_option['waypoint']
                    else:  # mine
                        print(f"  💡 OVERRIDE: Procurement mode set to MINE (forced mining)")
                        should_buy_instead = False
                else:
                    # Decision: buy if cheaper than opportunity cost
                    if total_cost < opportunity_cost:
                        print(f"  💡 DECISION: BUY from market (cheaper than mining)")
                        should_buy_instead = True
                        args.buy_from = purchase_option['waypoint']
                    else:
                        print(f"  💡 DECISION: Mine (cheaper than purchase)")
            else:
                print(f"  ℹ️  No markets selling {delivery['tradeSymbol']} found in database")

            # Initialize success flag
            success = False

            # Strategy 1: Mining with continuous market monitoring (if mine_from specified and buying not preferred)
            if hasattr(args, 'mine_from') and args.mine_from and not should_buy_instead:
                print(f"\n  🎯 Mining strategy: Extract {delivery['tradeSymbol']} from {args.mine_from}")
                print(f"     Continuous market monitoring enabled - will switch to buying if better opportunity found")

                to_mine = min(still_need, cargo_available) if cargo_available < still_need else still_need

                # Mining loop with continuous market monitoring
                units_mined = 0
                success = False
                reason = ""

                # Navigate to asteroid first
                print(f"\n  Navigating to asteroid {args.mine_from}...")
                nav_success = navigator.execute_route(ship, args.mine_from, prefer_cruise=True)
                if not nav_success:
                    success = False
                    units_mined = 0
                    reason = "Navigation to asteroid failed"
                    log_error(
                        "Navigation failure",
                        reason,
                        impact={'Asteroid': args.mine_from},
                        resolution="Review route or fuel plan",
                        tags=['contract', 'mining'],
                        escalate=True
                    )
                else:
                    ship.orbit()

                    consecutive_failures = 0
                    max_consecutive_failures = 10

                    print(f"\n  ⛏️  Mining {delivery['tradeSymbol']} (need {to_mine} units)")
                    print(f"     Circuit breaker: Will stop after {max_consecutive_failures} consecutive failures")

                    while units_mined < to_mine:
                        # CHECK MARKET DATA BEFORE EACH EXTRACTION
                        db = Database()
                        with db.connection() as conn:
                            cursor = conn.execute("""
                                SELECT waypoint_symbol, sell_price, supply
                                FROM market_data
                                WHERE good_symbol = ?
                                  AND sell_price IS NOT NULL
                                  AND waypoint_symbol LIKE ?
                                ORDER BY sell_price ASC
                                LIMIT 1
                            """, (delivery['tradeSymbol'], f"{system}%"))
                            market_row = cursor.fetchone()

                        if market_row:
                            units_remaining = to_mine - units_mined
                            purchase_cost = market_row[1] * units_remaining
                            mining_time_hours = (units_remaining / 4) * 80 / 3600
                            opportunity_cost = mining_time_hours * 50000

                            if purchase_cost < opportunity_cost:
                                print(f"\n  💡 MARKET INTELLIGENCE UPDATE!")
                                print(f"     Found: {market_row[0]} selling {delivery['tradeSymbol']} @ {market_row[1]} cr/unit")
                                print(f"     Purchase: {purchase_cost} cr vs Mining opportunity cost: {int(opportunity_cost)} cr")
                                print(f"     💡 DECISION: Stop mining, buy remaining {units_remaining} units from market")
                                print(f"     📦 Mined {units_mined} units before switching")
                                args.buy_from = market_row[0]
                                success = False
                                reason = f"Market opportunity: {market_row[0]} @ {market_row[1]} cr/unit"
                                break

                        # Check circuit breaker
                        if consecutive_failures >= max_consecutive_failures:
                            print(f"\n  🛑 CIRCUIT BREAKER TRIGGERED!")
                            print(f"     {consecutive_failures} consecutive failures")
                            success = False
                            reason = f"Circuit breaker: {consecutive_failures} consecutive failures"
                            log_error(
                                "Mining circuit breaker",
                                reason,
                                impact={'Units mined': units_mined, 'Target': to_mine},
                                resolution="Switching to market purchase",
                                tags=['contract', 'mining']
                            )
                            break

                        # Extract
                        extraction = ship.extract()
                        if extraction:
                            extracted_symbol = extraction['symbol']
                            extracted_units = extraction['units']

                            if extracted_symbol == delivery['tradeSymbol']:
                                units_mined += extracted_units
                                stats['mined_units'] += extracted_units
                                consecutive_failures = 0
                                print(f"     ✅ Got {extracted_units} x {delivery['tradeSymbol']} (total: {units_mined}/{to_mine})")
                            else:
                                consecutive_failures += 1
                                print(f"     ⚠️  Got {extracted_units} x {extracted_symbol} (failure #{consecutive_failures})")

                            ship.jettison_wrong_cargo(delivery['tradeSymbol'], cargo_threshold=0.8)
                            ship.wait_for_cooldown(extraction['cooldown'])
                        else:
                            consecutive_failures += 1

                    if units_mined >= to_mine:
                        success = True
                        reason = "Success"

                if not success:
                    print(f"\n  🛑 Mining failed: {reason}")
                    print(f"  📦 Collected {units_mined} units before failure")

                    if not reason.startswith("Market opportunity"):
                        log_error(
                            "Mining phase failed",
                            reason or "Unknown reason",
                            impact={'Mined': units_mined, 'Needed': still_need + units_mined},
                            resolution="Fallback to alternate acquisition strategy",
                            tags=['contract', 'mining']
                        )

                    # Update cargo count with what we mined
                    already_have += units_mined
                    still_need = remaining - already_have

                    if still_need <= 0:
                        print(f"  ✅ Actually have enough! ({already_have}/{remaining} collected)")
                        success = True  # Mark as success to skip buying
                    else:
                        print(f"  ⚠️  Still need {still_need} more units")

                        # Check market data for purchase option
                        print(f"\n  🔍 Checking if {delivery['tradeSymbol']} is available for purchase...")
                        db = Database()
                        with db.connection() as conn:
                            cursor = conn.execute("""
                                SELECT waypoint_symbol, sell_price, supply
                                FROM market_data
                                WHERE good_symbol = ?
                                  AND sell_price IS NOT NULL
                                  AND waypoint_symbol LIKE ?
                                ORDER BY sell_price ASC
                                LIMIT 1
                            """, (delivery['tradeSymbol'], f"{system}%"))
                            market_row = cursor.fetchone()

                        purchase_option = None
                        if market_row:
                            purchase_option = {
                                'waypoint': market_row[0],
                                'price': market_row[1],
                                'supply': market_row[2]
                            }
                            total_cost = purchase_option['price'] * still_need
                            print(f"  ✅ Found market: {purchase_option['waypoint']}")
                            print(f"     Price: {purchase_option['price']} cr/unit ({purchase_option['supply']} supply)")
                            print(f"     Total cost: {format_credits(total_cost)} for {still_need} units")

                            # Calculate opportunity cost
                            time_to_mine_hours = (still_need / 4) * 80 / 3600  # Assume 4 units/extraction avg
                            opportunity_cost = time_to_mine_hours * 50000  # 50k cr/hr conservative estimate

                            print(f"     Mining time: ~{time_to_mine_hours:.1f} hours (opportunity cost: {format_credits(int(opportunity_cost))})")

                            # Check procurement mode override for fallback decision
                            if hasattr(args, 'procurement_mode') and args.procurement_mode == 'procure':
                                print(f"  💡 OVERRIDE: Procurement mode set to PROCURE (forced buying)")
                                args.buy_from = purchase_option['waypoint']
                                success = False  # Force purchase path
                                alternatives = []
                            elif hasattr(args, 'procurement_mode') and args.procurement_mode == 'mine':
                                print(f"  💡 OVERRIDE: Procurement mode set to MINE (continue mining)")
                                # Try alternative asteroids
                                alternatives = find_alternative_asteroids(
                                    api=api,
                                    system=system,
                                    current_asteroid=args.mine_from,
                                    target_resource=delivery['tradeSymbol']
                                )
                            else:
                                # Decision: buy if cheaper than opportunity cost or circuit breaker triggered
                                if total_cost < opportunity_cost or "Circuit breaker" in reason:
                                    print(f"  💡 DECISION: BUY from market (cheaper/faster than mining)")
                                    # Override to use purchase strategy
                                    args.buy_from = purchase_option['waypoint']
                                    success = False  # Force purchase path
                                    # Skip alternative asteroids
                                    alternatives = []
                                else:
                                    print(f"  💡 DECISION: Continue mining (cheaper than purchase)")
                                    # Try alternative asteroids
                                    alternatives = find_alternative_asteroids(
                                        api=api,
                                        system=system,
                                        current_asteroid=args.mine_from,
                                        target_resource=delivery['tradeSymbol']
                                    )
                        else:
                            print(f"  ⚠️  No markets selling {delivery['tradeSymbol']} found in database")
                            # Try alternative asteroids with REMAINING amount needed
                            alternatives = find_alternative_asteroids(
                                api=api,
                                system=system,
                                current_asteroid=args.mine_from,
                                target_resource=delivery['tradeSymbol']
                            )

                        if alternatives:
                            # Calculate remaining cargo space
                            ship_data = ship.get_status()
                            cargo_units = ship_data['cargo']['units']
                            cargo_available = cargo_capacity - cargo_units
                            to_mine_alt = min(still_need, cargo_available)

                            print(f"\n  🔄 Trying alternative asteroid: {alternatives[0]}")
                            print(f"  🎯 Need {to_mine_alt} more units")

                            alt_success, alt_units_mined, alt_reason = targeted_mining_with_circuit_breaker(
                                ship=ship,
                                navigator=navigator,
                                asteroid=alternatives[0],
                                target_resource=delivery['tradeSymbol'],
                                units_needed=to_mine_alt,
                                max_consecutive_failures=10
                            )

                            if alt_success:
                                success = True
                                units_mined += alt_units_mined
                                already_have += alt_units_mined
                                still_need = remaining - already_have
                                stats['mined_units'] += alt_units_mined
                            else:
                                # Alternative also failed, update totals
                                units_mined += alt_units_mined
                                already_have += alt_units_mined
                                still_need = remaining - already_have
                                stats['mined_units'] += alt_units_mined

                        # Final check after alternatives
                        if not success and still_need > 0:
                            print(f"\n  ❌ Mining strategy failed completely")
                            print(f"  📦 Total collected from mining: {units_mined} units")
                            if args.buy_from:
                                print(f"  💡 Falling back to purchasing remaining {still_need} units from {args.buy_from}")
                            else:
                                print(f"  ❌ No buy_from specified")
                                if already_have >= remaining:
                                    print(f"  ✅ But we have enough cargo! Proceeding to delivery...")
                                    success = True
                                else:
                                    print(f"  ❌ Cannot complete contract (have {already_have}/{remaining})")
                                    log_error(
                                        "Unable to acquire resources",
                                        "Mining failed and no purchase option available",
                                        impact={'Collected': already_have, 'Required': remaining},
                                        resolution="Assign scout to refresh markets or specify buy_from",
                                        escalate=True
                                    )
                                    return 1

            # Strategy 2: Purchase from market (if buy_from specified or mining failed)
            if args.buy_from and (not hasattr(args, 'mine_from') or not args.mine_from or not success):
                if cargo_available < still_need:
                    print(f"  ⚠️  Not enough cargo space! Will need multiple trips")
                    # Buy what we can fit now
                    to_buy = cargo_available
                else:
                    to_buy = still_need

                if to_buy > 0:
                    print(f"  💰 Buying {to_buy} units from {args.buy_from}...")
                    navigator.execute_route(ship, args.buy_from, prefer_cruise=True)
                    ship.dock()
                    transaction = ship.buy(delivery['tradeSymbol'], to_buy)
                    if transaction:
                        stats['purchase_spent'] += transaction.get('totalPrice', 0)
                        stats['purchased_units'] += transaction.get('units', to_buy)
                    else:
                        log_error(
                            "Purchase failed",
                            f"Unable to buy {to_buy} units from {args.buy_from}",
                            impact={'Units remaining': still_need},
                            resolution="Verify market availability",
                            escalate=True
                        )

    # Deliver (may need multiple trips)
    print(f"\n4. Delivering to {delivery['destinationSymbol']}...")

    total_delivered = 0
    trip = 1
    max_trips = 10  # Safety limit

    while total_delivered < remaining and trip <= max_trips:
        # Check current cargo
        ship_data = ship.get_status()
        current_cargo = ship_data['cargo']['inventory']

        # Count how many units we have to deliver
        to_deliver = 0
        for item in current_cargo:
            if item['symbol'] == delivery['tradeSymbol']:
                to_deliver = item['units']
                break

        if to_deliver == 0:
            # No more cargo, need to buy more
            if args.buy_from:
                still_need = remaining - total_delivered
                cargo_available = ship_data['cargo']['capacity'] - ship_data['cargo']['units']
                to_buy = min(still_need, cargo_available)

                if to_buy > 0:
                    print(f"\n  Trip {trip}: Buying {to_buy} more units from {args.buy_from}...")
                    navigator.execute_route(ship, args.buy_from, prefer_cruise=True)
                    ship.dock()

                    # Handle transaction limits by buying in batches
                    total_bought = 0
                    remaining_to_buy = to_buy

                    while remaining_to_buy > 0:
                        # Try to buy all remaining, but handle transaction limit errors
                        batch_size = remaining_to_buy
                        transaction = ship.buy(delivery['tradeSymbol'], batch_size)

                        if transaction:
                            # Success
                            units_bought = transaction.get('units', batch_size)
                            total_bought += units_bought
                            remaining_to_buy -= units_bought
                            stats['purchase_spent'] += transaction.get('totalPrice', 0)
                            stats['purchased_units'] += units_bought

                            if remaining_to_buy > 0:
                                print(f"  ✅ Bought {units_bought} units, {remaining_to_buy} more to go...")
                        else:
                            # Purchase failed - check if it's a transaction limit error
                            # Get the last API response to check error code
                            result = ship.api.request("GET", f"/my/ships/{ship.ship_symbol}")
                            if result:
                                # Try buying in smaller batch (20 units max is common limit)
                                if batch_size > 20:
                                    print(f"  ⚠️  Transaction limit hit, reducing batch to 20 units...")
                                    batch_size = 20
                                    transaction = ship.buy(delivery['tradeSymbol'], batch_size)
                                    if transaction:
                                        units_bought = transaction.get('units', batch_size)
                                        total_bought += units_bought
                                        remaining_to_buy -= units_bought
                                        stats['purchase_spent'] += transaction.get('totalPrice', 0)
                                        stats['purchased_units'] += units_bought
                                        continue

                            # If still failing, log and break
                            log_error(
                                "Purchase failed",
                                f"Unable to buy {remaining_to_buy} units from {args.buy_from} during trip {trip}",
                                impact={'Bought': total_bought, 'Failed': remaining_to_buy, 'Remaining': still_need},
                                resolution="Check market supply and transaction limits",
                                escalate=True
                            )
                            break

                    to_deliver = total_bought
                    if total_bought == 0:
                        print(f"  ❌ Failed to purchase any units, skipping delivery")
                        break
                else:
                    print("  ⚠️  No cargo space and nothing to deliver")
                    break
            else:
                print("  ❌ No cargo to deliver and no buy_from specified")
                break

        # Navigate and deliver
        print(f"  Trip {trip}: Delivering {to_deliver} units...")
        navigator.execute_route(ship, delivery['destinationSymbol'], prefer_cruise=True)

        # Ensure ship docks properly (ship.dock() now handles IN_TRANSIT wait)
        if not ship.dock():
            print(f"  ❌ Failed to dock at {delivery['destinationSymbol']}")
            log_error(
                "Docking failure",
                f"Unable to dock at delivery destination on trip {trip}",
                impact={'Delivered': total_delivered, 'Remaining': remaining - total_delivered},
                resolution="Check ship status and retry",
                escalate=True
            )
            return 1

        # Attempt delivery with retry logic for API errors
        max_delivery_retries = 3
        delivery_success = False

        for retry in range(max_delivery_retries):
            result = api.post(f"/my/contracts/{args.contract_id}/deliver", {
                "shipSymbol": args.ship,
                "tradeSymbol": delivery['tradeSymbol'],
                "units": to_deliver
            })

            if result and 'data' in result:
                # Success
                total_delivered += to_deliver
                stats['units_delivered'] = fulfilled + total_delivered
                stats['trips'] = trip
                print(f"  ✅ Delivered {to_deliver} units (total: {total_delivered}/{remaining})")
                delivery_success = True
                break
            else:
                # Check error details
                error_code = result.get('error', {}).get('code') if result else None
                error_msg = result.get('error', {}).get('message', 'Unknown error') if result else 'No response'

                # Error 4502 = contract terms not met - retry with brief pause
                if error_code == 4502 and retry < max_delivery_retries - 1:
                    print(f"  ⚠️  Delivery failed (error {error_code}): {error_msg}")
                    print(f"  🔄 Retry {retry + 1}/{max_delivery_retries - 1}...")
                    time.sleep(2)  # Brief pause before retry
                    continue
                else:
                    print(f"  ❌ Delivery failed: {error_msg} (Code: {error_code})")
                    log_error(
                        "Delivery API failure",
                        f"/deliver call returned error {error_code}: {error_msg} on trip {trip}",
                        impact={'Delivered': total_delivered, 'Remaining': remaining - total_delivered},
                        resolution="Retry delivery manually or check contract requirements",
                        escalate=True
                    )
                    break

        if not delivery_success:
            return 1

        trip += 1

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


def negotiate_operation(args):
    """Negotiate a new contract - replaces negotiate_contract.sh"""
    log_file = setup_logging("negotiate", args.ship, getattr(args, 'log_level', 'INFO'))

    print("=" * 70)
    print("NEGOTIATE CONTRACT")
    print("=" * 70)

    api = get_api_client(args.player_id)

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
