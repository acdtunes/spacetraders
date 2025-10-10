#!/usr/bin/env python3
"""
Contract operations: contract fulfillment and negotiation
"""

import logging
import time
from datetime import datetime, timezone
from dataclasses import dataclass
from typing import Callable, Dict, List, Optional, Tuple

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
from spacetraders_bot.operations.control import CircuitBreaker


def evaluate_contract_profitability(contract: Dict, cargo_capacity: int) -> Tuple[bool, str, Dict]:
    """
    Evaluate if a contract is profitable based on ROI and net profit criteria.

    Returns:
        (is_profitable, reason, metrics)
    """
    terms = contract['terms']
    delivery = terms['deliver'][0] if terms.get('deliver') else None

    if not delivery:
        return False, "No delivery requirements", {}

    # Calculate payment
    on_accepted = terms['payment']['onAccepted']
    on_fulfilled = terms['payment']['onFulfilled']
    total_payment = on_accepted + on_fulfilled

    # Calculate units and trips
    units_required = delivery['unitsRequired']
    units_fulfilled = delivery['unitsFulfilled']
    units_remaining = units_required - units_fulfilled

    if units_remaining <= 0:
        return False, "Contract already fulfilled", {}

    trips = (units_remaining + cargo_capacity - 1) // cargo_capacity  # Ceiling division

    # Estimate costs (conservative - assumes purchase required)
    # Use 1500 cr/unit as conservative estimate for raw materials
    estimated_unit_cost = 1500
    estimated_purchase_cost = units_remaining * estimated_unit_cost

    # Estimate fuel cost (conservative - 100 cr per trip for fuel)
    estimated_fuel_cost = trips * 100

    total_estimated_cost = estimated_purchase_cost + estimated_fuel_cost

    # Calculate profit and ROI
    net_profit = total_payment - total_estimated_cost
    roi = (net_profit / total_estimated_cost * 100) if total_estimated_cost > 0 else 0

    # Profitability criteria
    min_profit = 5000
    min_roi = 5.0

    metrics = {
        'total_payment': total_payment,
        'estimated_cost': total_estimated_cost,
        'net_profit': net_profit,
        'roi': roi,
        'units_remaining': units_remaining,
        'trips': trips,
    }

    # Check profitability
    if net_profit < min_profit:
        return False, f"Net profit {net_profit:,} cr < {min_profit:,} cr minimum", metrics

    if roi < min_roi:
        return False, f"ROI {roi:.1f}% < {min_roi}% minimum", metrics

    return True, "Contract meets profitability criteria", metrics


def batch_contract_operation(args, *, api=None):
    """
    Batch contract negotiation and fulfillment operation.

    Negotiates and fulfills multiple contracts in sequence, filtering by profitability.
    """
    log_file = setup_logging("batch_contract", args.ship, getattr(args, 'log_level', 'INFO'))

    print("=" * 70)
    print("BATCH CONTRACT OPERATION")
    print("=" * 70)
    print(f"Target: {args.contract_count} contracts")
    print("=" * 70)

    api = api or get_api_client(args.player_id)
    ship = ShipController(api, args.ship)

    # Get ship data for profitability evaluation
    ship_data = ship.get_status()
    if not ship_data:
        print("❌ Failed to get ship status")
        return 1

    cargo_capacity = ship_data['cargo']['capacity']

    captain_logger = get_captain_logger(args.player_id)
    operator_name = get_operator_name(args)

    # Statistics tracking
    batch_stats = {
        'total_negotiated': 0,
        'accepted': 0,
        'fulfilled': 0,
        'failed': 0,
        'total_profit': 0,
        'contracts': [],
    }

    print(f"\nShip: {args.ship}")
    print(f"Cargo Capacity: {cargo_capacity}")
    print(f"\nStarting batch contract operations...\n")

    for i in range(args.contract_count):
        contract_num = i + 1
        print("\n" + "=" * 70)
        print(f"CONTRACT {contract_num}/{args.contract_count}")
        print("=" * 70)

        # Step 1: Negotiate new contract
        print(f"\n{contract_num}.1 Negotiating contract...")
        result = api.post(f"/my/ships/{args.ship}/negotiate/contract")

        if not result or 'data' not in result:
            print(f"❌ Failed to negotiate contract {contract_num}")
            batch_stats['failed'] += 1

            # Log error but continue to next contract
            log_captain_event(
                captain_logger,
                'CRITICAL_ERROR',
                operator=operator_name,
                ship=args.ship,
                error=f"Contract negotiation failed (contract {contract_num}/{args.contract_count})",
                cause="API returned no data for negotiate request",
                impact={'Contract Number': contract_num},
                resolution="Continuing to next contract",
                lesson="Monitor API reliability",
                escalate=False,
                tags=['contract', 'batch', 'negotiation_failed']
            )
            continue

        contract = result['data']['contract']
        contract_id = contract['id']
        batch_stats['total_negotiated'] += 1

        # Display contract details
        terms = contract['terms']
        delivery = terms['deliver'][0] if terms.get('deliver') else None

        print(f"\n✅ Contract {contract_id} negotiated")
        print(f"   Type: {contract['type']}")
        print(f"   Faction: {contract['factionSymbol']}")

        if delivery:
            print(f"   Delivery: {delivery['unitsRequired']} x {delivery['tradeSymbol']}")
            print(f"   Destination: {delivery['destinationSymbol']}")

        print(f"   Payment on Accept: {format_credits(terms['payment']['onAccepted'])}")
        print(f"   Payment on Fulfill: {format_credits(terms['payment']['onFulfilled'])}")

        # Step 2: Calculate estimated profitability (for informational purposes only)
        # NOTE: We ALWAYS accept and fulfill contracts, regardless of profitability.
        # Rationale: Opportunity cost of waiting for contract expiration is worse than small loss.
        print(f"\n{contract_num}.2 Calculating profitability metrics...")
        is_profitable, reason, metrics = evaluate_contract_profitability(contract, cargo_capacity)

        print(f"   Estimated Cost: {format_credits(metrics.get('estimated_cost', 0))}")
        print(f"   Net Profit: {format_credits(metrics.get('net_profit', 0))}")
        print(f"   ROI: {metrics.get('roi', 0):.1f}%")
        print(f"   Trips Required: {metrics.get('trips', 0)}")

        if not is_profitable:
            print(f"\n⚠️  WARNING: {reason}")
            print(f"   However, proceeding with contract anyway (avoiding opportunity cost of waiting)")
        else:
            print(f"\n✅ PROFITABLE: {reason}")

        # ALWAYS accept contracts
        batch_stats['accepted'] += 1

        # Step 3: Fulfill contract
        print(f"\n{contract_num}.3 Fulfilling contract...")

        # Create args for single contract fulfillment
        contract_args = type('obj', (object,), {
            'player_id': args.player_id,
            'ship': args.ship,
            'contract_id': contract_id,
            'buy_from': getattr(args, 'buy_from', None),
            'log_level': getattr(args, 'log_level', 'INFO'),
        })()

        # Execute contract fulfillment
        result = contract_operation(contract_args, api=api, ship=ship)

        if result == 0:
            print(f"\n✅ Contract {contract_id} fulfilled successfully!")
            batch_stats['fulfilled'] += 1
            batch_stats['total_profit'] += metrics.get('net_profit', 0)
            batch_stats['contracts'].append({
                'contract_id': contract_id,
                'status': 'fulfilled',
                'metrics': metrics,
            })
        else:
            print(f"\n❌ Contract {contract_id} fulfillment failed")
            batch_stats['failed'] += 1
            batch_stats['contracts'].append({
                'contract_id': contract_id,
                'status': 'failed',
                'metrics': metrics,
            })

            # Log failure but continue
            log_captain_event(
                captain_logger,
                'CRITICAL_ERROR',
                operator=operator_name,
                ship=args.ship,
                error=f"Contract fulfillment failed (contract {contract_num}/{args.contract_count})",
                cause=f"Contract operation returned error code {result}",
                impact={'Contract ID': contract_id, 'Contract Number': contract_num},
                resolution="Continuing to next contract",
                lesson="Review contract fulfillment error logs",
                escalate=False,
                tags=['contract', 'batch', 'fulfillment_failed', contract_id]
            )

    # Print batch summary
    print("\n" + "=" * 70)
    print("BATCH CONTRACT SUMMARY")
    print("=" * 70)
    print(f"Total Negotiated: {batch_stats['total_negotiated']}")
    print(f"Accepted: {batch_stats['accepted']}")
    print(f"Fulfilled: {batch_stats['fulfilled']}")
    print(f"Failed: {batch_stats['failed']}")
    print(f"Total Estimated Profit: {format_credits(batch_stats['total_profit'])}")
    print("=" * 70)

    # Log batch completion
    log_captain_event(
        captain_logger,
        'PERFORMANCE_SUMMARY',
        summary_type='Batch Contract Operation',
        financials={
            'revenue': batch_stats['total_profit'],
            'cumulative': batch_stats['total_profit'],
            'rate': 0,  # Not time-based
        },
        operations={
            'completed': batch_stats['fulfilled'],
            'active': 0,
            'success_rate': (batch_stats['fulfilled'] / batch_stats['accepted'] * 100) if batch_stats['accepted'] > 0 else 0
        },
        fleet={'active': 1, 'total': 1},
        top_performers=[{
            'ship': args.ship,
            'profit': batch_stats['total_profit'],
            'operation': 'batch_contract'
        }],
        tags=['contract', 'batch', 'summary']
    )

    # Return success if at least one contract was fulfilled
    return 0 if batch_stats['fulfilled'] > 0 else 1


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
        net_profit = stats_snapshot['payment'] - stats_snapshot['purchase_spent']
        results = {
            'Units Delivered': f"{stats_snapshot['units_delivered']}/{stats_snapshot['units_required']}",
            'Trips': stats_snapshot['trips'],
            'Purchased Units': stats_snapshot['purchased_units'],
            'Gross Payment': f"{stats_snapshot['payment']:,} cr",
            'Purchase Spend': f"{stats_snapshot['purchase_spent']:,} cr",
            'Net Profit': f"{net_profit:,} cr",
        }
        notes = f"Fulfilled contract {args.contract_id} delivering {trade_symbol} to {destination}."

        # Generate narrative for captain's log
        narrative = f"""Contract fulfillment complete. I coordinated {stats_snapshot['trips']} delivery trip{'s' if stats_snapshot['trips'] > 1 else ''} to transport {stats_snapshot['units_delivered']} units of {trade_symbol} to {destination}. All {stats_snapshot['purchased_units']} units were acquired through market purchases. The operation took {duration} and generated {net_profit:,} credits net profit after accounting for {stats_snapshot['purchase_spent']:,} credits in procurement costs."""

        log_captain_event(
            captain_logger,
            'OPERATION_COMPLETED',
            operator=operator_name,
            ship=args.ship,
            duration=duration,
            results=results,
            narrative=narrative,
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

    def _purchase_initial_cargo(
        ship,
        navigator,
        market: str,
        trade_symbol: str,
        quantity: int,
        stats: dict,
        log_error,
    ) -> Tuple[int, bool]:
        if quantity <= 0:
            return 0, True

        print(f"\n  💰 Purchasing {quantity} units of {trade_symbol} from {market}...")
        if not navigator.execute_route(ship, market):
            print(f"  ❌ Navigation to {market} failed")
            log_error(
                "Navigation failure",
                f"Unable to navigate to market {market}",
                impact={'Market': market},
                resolution="Check fuel and route feasibility",
                escalate=True,
            )
            return 0, False

        ship.dock()
        # ShipController.buy() now handles transaction limits automatically
        transaction = ship.buy(trade_symbol, quantity)
        if transaction:
            units = transaction.get('units', quantity)
            stats['purchase_spent'] += transaction.get('totalPrice', 0)
            stats['purchased_units'] += units
            print(
                f"  ✅ Purchased {units} units for {format_credits(transaction.get('totalPrice', 0))}"
            )
            return units, True

        print(f"  ❌ Purchase failed at {market}")
        log_error(
            "Purchase failed",
            f"Unable to buy {quantity} units from {market}",
            impact={'Units remaining': quantity},
            resolution="Verify market availability and ship credits",
            escalate=True,
        )
        return 0, False

    def _acquire_initial_resources(
        delivery: Dict,
        remaining_units: int,
    ) -> bool:
        ship_data = ship.get_status()
        if not ship_data:
            print("❌ Failed to get ship status")
            log_error(
                "Ship status unavailable",
                "API returned no ship data",
                resolution="Verify ship readiness",
                escalate=True,
            )
            return False

        current_cargo = ship_data['cargo']['inventory']
        cargo_capacity = ship_data['cargo']['capacity']
        cargo_units = ship_data['cargo']['units']

        already_have = next(
            (item['units'] for item in current_cargo if item['symbol'] == delivery['tradeSymbol']),
            0,
        )

        still_need = remaining_units - already_have
        print(f"  Already have: {already_have} units")
        print(f"  Still need: {still_need} units")

        if still_need <= 0:
            return True

        cargo_available = cargo_capacity - cargo_units
        if cargo_available <= 0:
            print("  ⚠️  No cargo space available to acquire additional goods")

        acquisition_strategy = ResourceAcquisitionStrategy(
            trade_symbol=delivery['tradeSymbol'],
            system=system,
            database=local_db,
            log_error=log_error,
            sleep_fn=sleep_fn,
            print_fn=print,
            delivery_waypoint=delivery['destinationSymbol'],  # NEW: Enable distance filtering
            max_distance=400,  # NEW: Limit to markets within 400 units of delivery point
        )

        if not acquisition_strategy.ensure_availability(
            still_needed=still_need,
            cargo_available=cargo_available,
            cargo_capacity=cargo_capacity,
            preferred_market=getattr(args, 'buy_from', None),
            update_preferred_market=lambda new_market: setattr(args, 'buy_from', new_market),
        ):
            return False

        quantity = min(still_need, cargo_available)
        _, purchase_success = _purchase_initial_cargo(
            ship=ship,
            navigator=navigator,
            market=args.buy_from,
            trade_symbol=delivery['tradeSymbol'],
            quantity=quantity,
            stats=stats,
            log_error=log_error,
        )

        return purchase_success


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
            navigator.execute_route(ship, delivery['destinationSymbol'])

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
        breaker = CircuitBreaker(limit=3)

        while not breaker.tripped():
            result = api.post(
                f"/my/contracts/{args.contract_id}/deliver",
                {
                    "shipSymbol": args.ship,
                    "tradeSymbol": delivery['tradeSymbol'],
                    "units": units,
                },
            )

            if result and 'data' in result:
                print(
                    f"  ✅ Delivered {units} units (total: {delivered_so_far + units}/{remaining})"
                )
                breaker.record_success()
                return True

            error_code = result.get('error', {}).get('code') if result else None
            error_msg = result.get('error', {}).get('message', 'Unknown error') if result else 'No response'
            failure_count = breaker.record_failure()

            if error_code == 4502 and failure_count < breaker.limit:
                print(f"  ⚠️  Delivery failed (error {error_code}): {error_msg}")
                print(f"  🔄 Retry {failure_count}/{breaker.limit - 1}...")
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
        if not navigator.execute_route(ship, args.buy_from):
            log_error(
                "Navigation failure",
                f"Unable to navigate to market {args.buy_from}",
                impact={'Market': args.buy_from},
                resolution="Check fuel and route feasibility",
                escalate=True,
            )
            return 0, False

        ship.dock()

        # ShipController.buy() now handles transaction limits automatically
        transaction = ship.buy(delivery['tradeSymbol'], to_buy)

        if transaction:
            units_bought = transaction.get('units', to_buy)
            stats['purchase_spent'] += transaction.get('totalPrice', 0)
            stats['purchased_units'] += units_bought
            print(f"  ✅ Bought {units_bought} units for {format_credits(transaction.get('totalPrice', 0))}")
            return units_bought, True

        log_error(
            "Purchase failed",
            f"Unable to buy {to_buy} units from {args.buy_from} during trip {trip_number}",
            impact={'Failed to purchase': to_buy, 'Remaining': still_needed},
            resolution="Check market supply and agent credits",
            escalate=True,
        )
        return 0, False

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
    if not _acquire_initial_resources(delivery, remaining):
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


def _find_lowest_price_market(db: Database, trade_symbol: str, system_prefix: str, delivery_waypoint: Optional[str] = None, max_distance: Optional[float] = None) -> Optional[Tuple[str, int, str]]:
    """
    Return the cheapest market tuple within given system prefix.

    Args:
        db: Database instance
        trade_symbol: Good to search for
        system_prefix: System to search in (e.g., 'X1-GH18')
        delivery_waypoint: Optional delivery destination for distance filtering
        max_distance: Optional maximum distance from delivery waypoint (units)

    Returns:
        Tuple of (waypoint_symbol, sell_price, supply) or None

    If delivery_waypoint and max_distance are provided, only markets within
    max_distance of the delivery point are considered. This prevents selecting
    distant markets that cause navigation failures.
    """
    pattern = f"{system_prefix}%"

    if delivery_waypoint and max_distance:
        # Distance-aware selection: filter by proximity to delivery waypoint
        # Load system graph to calculate distances
        with db.connection() as conn:
            system_graph = db.get_system_graph(conn, system_prefix)

        if not system_graph:
            # Fallback to price-only if graph unavailable
            return _find_lowest_price_market_price_only(db, trade_symbol, pattern)

        delivery_wp_data = system_graph['waypoints'].get(delivery_waypoint)
        if not delivery_wp_data:
            # Fallback if delivery waypoint not in graph
            return _find_lowest_price_market_price_only(db, trade_symbol, pattern)

        # Get all markets selling this good
        with db.connection() as conn:
            cursor = conn.execute(
                """
                SELECT waypoint_symbol, sell_price, supply
                FROM market_data
                WHERE good_symbol = ?
                  AND sell_price IS NOT NULL
                  AND waypoint_symbol LIKE ?
                ORDER BY sell_price ASC
                """,
                (trade_symbol, pattern),
            )
            all_markets = cursor.fetchall()

        # Filter by distance and select cheapest among nearby markets
        import math
        nearby_markets = []
        for market_symbol, price, supply in all_markets:
            market_wp_data = system_graph['waypoints'].get(market_symbol)
            if not market_wp_data:
                continue

            # Calculate Euclidean distance
            distance = math.sqrt(
                (delivery_wp_data['x'] - market_wp_data['x']) ** 2 +
                (delivery_wp_data['y'] - market_wp_data['y']) ** 2
            )

            if distance <= max_distance:
                nearby_markets.append((market_symbol, price, supply, distance))

        if nearby_markets:
            # Return cheapest market among nearby options
            # Sort by price (already sorted from query, but re-sort with distance info)
            nearby_markets.sort(key=lambda x: (x[1], x[3]))  # price, then distance
            best = nearby_markets[0]
            return (best[0], best[1], best[2])  # Return original tuple format

        # No markets within max_distance - return None to trigger waiting logic
        return None

    # Default: price-only optimization (original behavior)
    return _find_lowest_price_market_price_only(db, trade_symbol, pattern)


def _find_lowest_price_market_price_only(db: Database, trade_symbol: str, pattern: str) -> Optional[Tuple[str, int, str]]:
    """Original price-only market selection (no distance filtering)."""
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

@dataclass
class ResourceAcquisitionStrategy:
    """Encapsulates market lookup and waiting logic for contract resources."""

    trade_symbol: str
    system: str
    database: Database
    log_error: Callable[..., None]
    sleep_fn: Callable[[int], None]
    print_fn: Callable[[str], None]
    delivery_waypoint: Optional[str] = None  # NEW: For distance-aware market selection
    max_distance: Optional[float] = None  # NEW: Maximum distance from delivery point
    max_retries: int = 12
    retry_interval_seconds: int = 300

    def ensure_availability(
        self,
        *,
        still_needed: int,
        cargo_available: int,
        cargo_capacity: int,
        preferred_market: Optional[str],
        update_preferred_market: Callable[[str], None],
    ) -> bool:
        self.print_fn(f"  Cargo space available: {cargo_available}/{cargo_capacity}")

        if preferred_market:
            return self._verify_preferred_market(still_needed, preferred_market)

        market_row = self._find_market()
        if market_row:
            update_preferred_market(market_row[0])
            self._announce_market(market_row, still_needed)
            return True

        return self._wait_for_market(still_needed, update_preferred_market)

    def _verify_preferred_market(self, still_needed: int, market: str) -> bool:
        self.print_fn(f"  ✅ Using specified market: {market}")
        market_row = _fetch_market_listing(self.database, self.trade_symbol, market)

        if market_row:
            self._announce_market(market_row, still_needed)
            return True

        self.print_fn(
            f"  ⚠️  Warning: Market {market} not found in database or doesn't sell {self.trade_symbol}"
        )
        self.print_fn("  Will attempt purchase anyway (market may not be scouted yet)")
        return True

    def _find_market(self) -> Optional[Tuple[str, int, str]]:
        self.print_fn(f"\n  🔍 Searching for markets selling {self.trade_symbol}...")

        # Use distance-aware selection if delivery waypoint provided
        if self.delivery_waypoint and self.max_distance:
            self.print_fn(f"     Distance filter: max {self.max_distance:.0f} units from {self.delivery_waypoint}")

        return _find_lowest_price_market(
            self.database,
            self.trade_symbol,
            self.system,
            delivery_waypoint=self.delivery_waypoint,
            max_distance=self.max_distance
        )

    def _wait_for_market(
        self,
        still_needed: int,
        update_preferred_market: Callable[[str], None],
    ) -> bool:
        self._log_missing_market()

        breaker = CircuitBreaker(limit=self.max_retries)

        while not breaker.tripped():
            attempt = breaker.failures + 1
            self.print_fn(
                f"\n  ⏳ Waiting {self.retry_interval_seconds // 60} minutes before retry {attempt}/{self.max_retries}..."
            )
            self.sleep_fn(self.retry_interval_seconds)
            self.print_fn(f"  🔍 Retry {attempt}: Checking market database...")

            market_row = self._find_market()
            if market_row:
                self.print_fn(f"  ✅ SUCCESS! Market discovered: {market_row[0]}")
                update_preferred_market(market_row[0])
                self._announce_market(market_row, still_needed)
                breaker.record_success()
                return True

            failure_count = breaker.record_failure()
            self.print_fn(
                f"  ⚠️  Still not available (retry {failure_count}/{self.max_retries})"
            )

        self._log_timeout()
        return False

    def _announce_market(self, market_row: Tuple[str, int, str], units: int) -> None:
        market, price, supply = market_row
        total_cost = price * units
        self.print_fn(f"  ✅ Found market: {market}")
        self.print_fn(f"     Price: {price} cr/unit ({supply} supply)")
        self.print_fn(f"     Total cost: {format_credits(total_cost)} for {units} units")

    def _log_missing_market(self) -> None:
        self.print_fn(f"  ❌ RESOURCE NOT AVAILABLE")
        self.print_fn(
            f"  {self.trade_symbol} is not available in any discovered markets in {self.system}"
        )
        self.print_fn(f"\n  📋 RECOMMENDED ACTIONS:")
        self.print_fn(f"     1. Deploy scout coordinator to system {self.system}")
        self.print_fn(f"     2. Wait for scouts to discover markets selling {self.trade_symbol}")
        self.print_fn(f"     3. Re-run contract with --buy-from <market> once discovered")
        self.print_fn(f"\n  🔄 Contract operation will wait and periodically retry...")

        self.log_error(
            "Resource not available in markets",
            f"{self.trade_symbol} not found in any discovered markets",
            impact={'Resource': self.trade_symbol, 'System': self.system},
            resolution="Deploy scout coordinator to discover markets, then retry",
            escalate=True,
            tags=['contract', 'market_missing', self.trade_symbol.lower()],
        )

    def _log_timeout(self) -> None:
        minutes_waited = (self.max_retries * self.retry_interval_seconds) // 60
        self.print_fn(
            f"\n  ❌ OPERATION FAILED: Resource never became available after {minutes_waited} minutes"
        )
        self.print_fn("  Manual intervention required - Flag Captain must deploy scout coordinator")
        self.log_error(
            "Contract operation timeout",
            f"Resource {self.trade_symbol} not discovered after waiting {minutes_waited} minutes",
            impact={'Resource': self.trade_symbol, 'System': self.system, 'Waited': f"{minutes_waited} min"},
            resolution="Flag Captain must manually deploy scouts or source resource elsewhere",
            escalate=True,
            tags=['contract', 'timeout', self.trade_symbol.lower()],
        )
