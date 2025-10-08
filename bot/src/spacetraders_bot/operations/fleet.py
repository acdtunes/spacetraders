#!/usr/bin/env python3
"""
Fleet operations: status checking and monitoring
"""

import logging
import time
from datetime import datetime
from typing import Iterable, List

from spacetraders_bot.core.utils import timestamp, timestamp_iso
from spacetraders_bot.operations.common import format_credits, get_api_client, setup_logging


def _resolve_ship_symbols(api, ships_arg: str) -> List[str]:
    if ships_arg:
        return [symbol.strip() for symbol in ships_arg.split(',') if symbol.strip()]

    all_ships = api.list_ships() or []
    return [ship['symbol'] for ship in all_ships]


def _print_agent_summary(agent: dict) -> None:
    if not agent:
        print("  ⚠️ Unable to retrieve agent information")
        return

    print(f"  Callsign: {agent['symbol']}")
    print(f"  Credits: {format_credits(agent['credits'])}")
    print(f"  HQ: {agent['headquarters']}")


def _print_ship_status(ship_data: dict) -> None:
    nav = ship_data['nav']
    fuel = ship_data['fuel']
    cargo = ship_data['cargo']

    symbol = ship_data.get('symbol', '<unknown>')
    print(f"\n  {symbol}:")
    print(f"    Location: {nav['waypointSymbol']}")
    print(f"    Status: {nav['status']}")
    print(f"    Flight Mode: {nav['flightMode']}")
    print(f"    Fuel: {fuel['current']}/{fuel['capacity']}")
    print(f"    Cargo: {cargo['units']}/{cargo['capacity']}")

    if nav['status'] == 'IN_TRANSIT':
        print(f"    ETA: {nav['route']['arrival']}")


def _print_monitor_ship_statuses(api, ship_symbols: Iterable[str]) -> None:
    for ship_symbol in ship_symbols:
        ship_data = api.get_ship(ship_symbol)
        if not ship_data:
            print(f"\n🚀 {ship_symbol}: unavailable")
            continue

        nav = ship_data['nav']
        fuel = ship_data['fuel']
        cargo = ship_data['cargo']

        print(f"\n🚀 {ship_symbol}:")
        print(f"   {nav['status']} at {nav['waypointSymbol']}")
        print(f"   Fuel: {fuel['current']}/{fuel['capacity']} | Cargo: {cargo['units']}/{cargo['capacity']}")

        if nav['status'] == 'IN_TRANSIT':
            print(f"   ETA: {nav['route']['arrival']}")


def _sleep_minutes(minutes: int) -> None:
    if minutes <= 0:
        return
    print(f"\n⏳ Next check in {minutes} minutes...")
    time.sleep(minutes * 60)


def status_operation(args):
    """Check status of agent and ships - replaces check_status.sh and similar"""
    log_file = setup_logging("status", "system", getattr(args, 'log_level', 'INFO'))

    print("=" * 70)
    print(f"SPACETRADERS STATUS - {timestamp_iso()}")
    print("=" * 70)

    api = get_api_client(args.player_id)

    # Get agent info
    print("\n💰 AGENT INFO:")
    _print_agent_summary(api.get_agent())

    # Get ships
    print(f"\n🚀 FLEET STATUS:")
    ships_to_check = _resolve_ship_symbols(api, args.ships or "")

    for ship_symbol in ships_to_check:
        ship_data = api.get_ship(ship_symbol)
        if ship_data:
            _print_ship_status(ship_data)

    print("\n" + "=" * 70)
    return 0


def monitor_operation(args):
    """Continuous monitoring of operations - replaces monitor_loop.sh and similar"""
    log_file = setup_logging("monitor", "system", getattr(args, 'log_level', 'INFO'))

    print("=" * 70)
    print("FLEET MONITORING")
    print("=" * 70)

    api = get_api_client(args.player_id)
    ships_to_monitor = _resolve_ship_symbols(api, args.ships or "")

    # Get starting credits
    agent = api.get_agent()
    starting_credits = agent['credits'] if agent else 0
    start_time = datetime.now()

    print(f"Starting Credits: {format_credits(starting_credits)}")
    print(f"Monitoring Interval: {args.interval} minutes")
    printable_ships = ', '.join(ships_to_monitor) if ships_to_monitor else 'All'
    print(f"Ships: {printable_ships}")
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

        _print_monitor_ship_statuses(api, ships_to_monitor)

        # Wait for next check
        if check_num < args.duration:
            _sleep_minutes(args.interval)

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
