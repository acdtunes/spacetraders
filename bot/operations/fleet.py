#!/usr/bin/env python3
"""
Fleet operations: status checking and monitoring
"""

import sys
import logging
import time
from datetime import datetime
from pathlib import Path

# Add lib directory to path
sys.path.insert(0, str(Path(__file__).parent.parent / "lib"))

from api_client import APIClient
from utils import timestamp, timestamp_iso
from .common import setup_logging, format_credits, get_api_client


def status_operation(args):
    """Check status of agent and ships - replaces check_status.sh and similar"""
    log_file = setup_logging("status", "system", getattr(args, 'log_level', 'INFO'))

    print("=" * 70)
    print(f"SPACETRADERS STATUS - {timestamp_iso()}")
    print("=" * 70)

    api = get_api_client(args.player_id)

    # Get agent info
    print("\n💰 AGENT INFO:")
    agent = api.get_agent()
    if agent:
        print(f"  Callsign: {agent['symbol']}")
        print(f"  Credits: {format_credits(agent['credits'])}")
        print(f"  HQ: {agent['headquarters']}")

    # Get ships
    print(f"\n🚀 FLEET STATUS:")
    ships_to_check = args.ships.split(',') if args.ships else []

    if not ships_to_check:
        # Get all ships
        all_ships = api.list_ships()
        if all_ships:
            ships_to_check = [s['symbol'] for s in all_ships]

    for ship_symbol in ships_to_check:
        ship_data = api.get_ship(ship_symbol.strip())
        if ship_data:
            nav = ship_data['nav']
            fuel = ship_data['fuel']
            cargo = ship_data['cargo']

            print(f"\n  {ship_symbol}:")
            print(f"    Location: {nav['waypointSymbol']}")
            print(f"    Status: {nav['status']}")
            print(f"    Flight Mode: {nav['flightMode']}")
            print(f"    Fuel: {fuel['current']}/{fuel['capacity']}")
            print(f"    Cargo: {cargo['units']}/{cargo['capacity']}")

            if nav['status'] == 'IN_TRANSIT':
                print(f"    ETA: {nav['route']['arrival']}")

    print("\n" + "=" * 70)
    return 0


def monitor_operation(args):
    """Continuous monitoring of operations - replaces monitor_loop.sh and similar"""
    log_file = setup_logging("monitor", "system", getattr(args, 'log_level', 'INFO'))

    print("=" * 70)
    print("FLEET MONITORING")
    print("=" * 70)

    api = get_api_client(args.player_id)
    ships_to_monitor = args.ships.split(',') if args.ships else []

    # Get starting credits
    agent = api.get_agent()
    starting_credits = agent['credits'] if agent else 0
    start_time = datetime.now()

    print(f"Starting Credits: {format_credits(starting_credits)}")
    print(f"Monitoring Interval: {args.interval} minutes")
    print(f"Ships: {', '.join(ships_to_monitor) if ships_to_monitor else 'All'}")
    print(f"Duration: {args.duration} checks\n")

    for check_num in range(1, args.duration + 1):
        print(f"\n{'='*70}")
        print(f"CHECK #{check_num} - {timestamp()}")
        print('='*70)

        # Current credits
        agent = api.get_agent()
        if agent:
            current_credits = agent['credits']
            profit = current_credits - starting_credits
            print(f"\n💰 Credits: {format_credits(current_credits)} (+{format_credits(profit)})")

        # Ship statuses
        for ship_symbol in ships_to_monitor:
            ship_data = api.get_ship(ship_symbol.strip())
            if ship_data:
                nav = ship_data['nav']
                fuel = ship_data['fuel']
                cargo = ship_data['cargo']

                print(f"\n🚀 {ship_symbol}:")
                print(f"   {nav['status']} at {nav['waypointSymbol']}")
                print(f"   Fuel: {fuel['current']}/{fuel['capacity']} | Cargo: {cargo['units']}/{cargo['capacity']}")

                if nav['status'] == 'IN_TRANSIT':
                    print(f"   ETA: {nav['route']['arrival']}")

        # Wait for next check
        if check_num < args.duration:
            wait_seconds = args.interval * 60
            print(f"\n⏳ Next check in {args.interval} minutes...")
            time.sleep(wait_seconds)

    # Final summary
    elapsed = datetime.now() - start_time
    agent = api.get_agent()
    final_credits = agent['credits'] if agent else 0
    total_profit = final_credits - starting_credits

    print(f"\n{'='*70}")
    print("MONITORING COMPLETE")
    print('='*70)
    print(f"Duration: {elapsed}")
    print(f"Starting Credits: {format_credits(starting_credits)}")
    print(f"Final Credits: {format_credits(final_credits)}")
    print(f"Total Profit: {format_credits(total_profit)}")
    print('='*70)

    return 0
