#!/usr/bin/env python3
"""
Smart Mining Operation - Uses intelligent routing

Demonstrates how to integrate SmartNavigator into operations
"""

import logging

from spacetraders_bot.core.routing import TimeCalculator
from spacetraders_bot.core.ship_controller import ShipController
from spacetraders_bot.core.smart_navigator import SmartNavigator
from spacetraders_bot.operations.common import get_api_client, setup_logging


def smart_mining_operation(args):
    """Autonomous mining with intelligent route planning"""
    log_file = setup_logging("smart-mining", args.ship, getattr(args, 'log_level', 'INFO'))

    logging.info("Initializing SMART mining operation")
    logging.info(f"Ship: {args.ship}")
    logging.info(f"Asteroid: {args.asteroid}")
    logging.info(f"Market: {args.market}")
    logging.info(f"Cycles: {args.cycles}")

    api = get_api_client(args.player_id)
    ship = ShipController(api, args.ship)

    # Initialize smart navigator
    ship_data = ship.get_status()
    if not ship_data:
        print("❌ Failed to get ship status")
        return 1

    # Extract system from ship location
    system = ship_data['nav']['systemSymbol']
    navigator = SmartNavigator(api, system)

    # Pre-flight validation
    print("\n🔍 Validating route feasibility...")

    # Check asteroid route
    valid, reason = navigator.validate_route(ship_data, args.asteroid)
    if not valid:
        print(f"❌ Route to asteroid is not feasible: {reason}")
        return 1
    print(f"  Asteroid route: {reason}")

    # Check market route (simulate full cargo)
    ship_data_full = ship_data.copy()
    ship_data_full['fuel']['current'] = ship_data['fuel']['capacity'] - \
        navigator.get_fuel_estimate(ship_data, args.asteroid)['total_fuel_cost']

    market_estimate = navigator.get_fuel_estimate(ship_data_full, args.market)
    if not market_estimate:
        print(f"❌ Cannot route to market from asteroid (fuel insufficient)")
        return 1

    print(f"  Market route: {market_estimate['refuel_stops']} refuel stop(s), "
          f"{market_estimate['total_time']}s travel time")

    # Calculate economics
    round_trip_time = navigator.get_fuel_estimate(ship_data, args.asteroid)['total_time'] + \
                      market_estimate['total_time']

    print(f"\n📊 Route Analysis:")
    print(f"  Round trip time: {TimeCalculator.format_time(round_trip_time)}")
    print(f"  Estimated cycles/hour: {3600 / round_trip_time:.1f}")

    input("\nPress ENTER to start mining or Ctrl+C to cancel...")

    # Mining loop with smart navigation
    stats = {
        "cycles_completed": 0,
        "total_extracted": 0,
        "total_sold": 0,
        "total_revenue": 0,
        "total_fuel_used": 0
    }

    for cycle in range(1, args.cycles + 1):
        print(f"\n{'='*70}")
        print(f"CYCLE {cycle}/{args.cycles}")
        print('='*70)

        # Get current fuel
        ship_data = ship.get_status()
        fuel_before = ship_data['fuel']['current']

        # Navigate to asteroid using smart router
        print(f"\n1. Smart navigation to asteroid {args.asteroid}...")
        success = navigator.execute_route(ship, args.asteroid, prefer_cruise=True)

        if not success:
            print("❌ Smart navigation failed, aborting")
            return 1

        ship.orbit()

        # Mine until cargo full
        print(f"\n2. Mining until cargo full...")
        cargo = ship.get_cargo()
        while cargo and cargo['units'] < cargo['capacity'] - 1:
            # Check cooldown
            ship_data = ship.get_status()
            if ship_data and ship_data['cooldown']['remainingSeconds'] > 0:
                ship.wait_for_cooldown(ship_data['cooldown']['remainingSeconds'])

            # Extract
            extraction = ship.extract()
            if extraction:
                stats['total_extracted'] += extraction['units']
                ship.wait_for_cooldown(extraction['cooldown'])

            cargo = ship.get_cargo()

        # Navigate to market using smart router
        print(f"\n3. Smart navigation to market {args.market}...")
        success = navigator.execute_route(ship, args.market, prefer_cruise=True)

        if not success:
            print("❌ Smart navigation failed, aborting")
            return 1

        # Dock and sell
        print(f"\n4. Selling cargo...")
        ship.dock()
        revenue = ship.sell_all()
        stats['total_revenue'] += revenue
        stats['total_sold'] += cargo['units'] if cargo else 0

        # Refuel
        print(f"\n5. Refueling...")
        ship.refuel()

        # Calculate fuel used this cycle
        ship_data = ship.get_status()
        fuel_after = ship_data['fuel']['current']
        fuel_used = fuel_before - fuel_after + ship_data['fuel']['capacity']  # Account for refuel
        stats['total_fuel_used'] += fuel_used

        stats['cycles_completed'] += 1

        # Cycle summary
        print(f"\n{'='*70}")
        print(f"CYCLE {cycle} COMPLETE")
        print(f"Revenue this cycle: {revenue:,} credits")
        print(f"Fuel used: {fuel_used} units")
        print(f"Total revenue: {stats['total_revenue']:,} credits")
        print('='*70)

    # Final summary
    print(f"\n{'='*70}")
    print("SMART MINING OPERATION COMPLETE")
    print('='*70)
    print(f"Cycles completed: {stats['cycles_completed']}")
    print(f"Total revenue: {stats['total_revenue']:,} credits")
    print(f"Total extracted: {stats['total_extracted']} units")
    print(f"Total sold: {stats['total_sold']} units")
    print(f"Total fuel used: {stats['total_fuel_used']} units")
    print(f"Average per cycle: {stats['total_revenue'] // stats['cycles_completed']:,} credits" if stats['cycles_completed'] > 0 else "N/A")
    print(f"Credits per fuel: {stats['total_revenue'] // stats['total_fuel_used']:.1f}" if stats['total_fuel_used'] > 0 else "N/A")
    print('='*70)

    return 0
