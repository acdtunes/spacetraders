#!/usr/bin/env python3
"""
Multi-leg trading operations - CLI entry points

Core functionality has been refactored to operations._trading/
This file contains only the CLI operation entry points.
"""

import json
import logging
import time
from typing import Dict, List, Optional

from datetime import datetime, timedelta

from spacetraders_bot.core.api_client import APIClient
from spacetraders_bot.core.ship import ShipController
from spacetraders_bot.operations.control import CircuitBreaker

# Import refactored components
from spacetraders_bot.operations._trading import (
    MultiLegRoute,
    RouteExecutor,
    MultiLegTradeOptimizer,
    create_fixed_route,
    FleetTradeOptimizer,
)


def trade_plan_operation(args):
    """Analyze and propose a multi-leg trading route without executing it."""
    from .common import get_api_client, get_database

    player_id = getattr(args, "player_id", None)
    ship_symbol = getattr(args, "ship", None)

    if not player_id:
        print("❌ --player-id required")
        return 1

    if not ship_symbol:
        print("❌ --ship required")
        return 1

    max_stops = getattr(args, "max_stops", 4) or 4
    try:
        max_stops = int(max_stops)
    except (TypeError, ValueError):
        print("❌ --max-stops must be an integer")
        return 1

    if max_stops < 2:
        print("❌ --max-stops must be at least 2")
        return 1

    try:
        api = get_api_client(player_id)
    except Exception as exc:  # Surface errors directly
        print(f"❌ {exc}")
        return 1

    db = get_database()
    ship = ShipController(api, ship_symbol)

    ship_data = ship.get_status()
    if not ship_data:
        print("❌ Failed to get ship status")
        return 1

    system_override = getattr(args, "system", None)
    system = system_override or ship_data['nav']['systemSymbol']
    start_waypoint = ship_data['nav']['waypointSymbol']

    cargo_capacity = ship_data['cargo']['capacity']
    ship_speed = ship_data['engine']['speed']
    fuel_capacity = ship_data['fuel']['capacity']
    current_fuel = ship_data['fuel']['current']

    # CRITICAL FIX: Extract actual ship cargo (may have residual from previous operations)
    starting_cargo = {item['symbol']: item['units']
                     for item in ship_data['cargo']['inventory']}

    agent = api.get_agent()
    if not agent:
        print("❌ Failed to get agent data")
        return 1

    starting_credits = agent['credits']

    optimizer = MultiLegTradeOptimizer(api, db, player_id)
    route = optimizer.find_optimal_route(
        start_waypoint=start_waypoint,
        system=system,
        max_stops=max_stops,
        cargo_capacity=cargo_capacity,
        starting_credits=starting_credits,
        ship_speed=ship_speed,
        fuel_capacity=fuel_capacity,
        current_fuel=current_fuel,
        starting_cargo=starting_cargo,
    )

    if not route:
        print("❌ No profitable route found")
        return 1

    summary = {
        "ship": ship_symbol,
        "player_id": player_id,
        "system": system,
        "start_waypoint": start_waypoint,
        "max_stops": max_stops,
        "total_profit": route.total_profit,
        "total_distance": route.total_distance,
        "total_fuel_cost": route.total_fuel_cost,
        "estimated_time_minutes": route.estimated_time_minutes,
        "segment_count": len(route.segments),
        "segments": [],
    }

    for index, segment in enumerate(route.segments, start=1):
        actions = [
            {
                "type": action.action,
                "good": action.good,
                "units": action.units,
                "price_per_unit": action.price_per_unit,
                "total_value": action.total_value,
            }
            for action in segment.actions_at_destination
        ]

        summary["segments"].append(
            {
                "index": index,
                "from_waypoint": segment.from_waypoint,
                "to_waypoint": segment.to_waypoint,
                "distance": segment.distance,
                "fuel_cost": segment.fuel_cost,
                "actions": actions,
                "cargo_after": segment.cargo_after,
                "credits_after": segment.credits_after,
                "cumulative_profit": segment.cumulative_profit,
            }
        )

    print(json.dumps(summary, indent=2))
    return 0


def multileg_trade_operation(args):
    """Execute a multi-leg trading operation"""
    from .common import setup_logging, get_api_client, get_database
    from spacetraders_bot.core.ship import ShipController
    from datetime import datetime, timedelta

    log_file = setup_logging("multileg_trade", args.ship, getattr(args, 'log_level', 'INFO'), player_id=args.player_id)

    logging.info("="*70)
    logging.info("MULTI-LEG TRADING OPERATION")
    logging.info("="*70)

    api = get_api_client(args.player_id)
    db = get_database()
    ship = ShipController(api, args.ship)

    # Get ship status
    ship_data = ship.get_status()
    if not ship_data:
        logging.error("Failed to get ship status")
        return 1

    system = getattr(args, 'system', None) or ship_data['nav']['systemSymbol']
    current_waypoint = ship_data['nav']['waypointSymbol']
    cargo_capacity = getattr(args, 'cargo', None) or ship_data['cargo']['capacity']
    ship_speed = ship_data['engine']['speed']
    fuel_capacity = ship_data['fuel']['capacity']
    current_fuel = ship_data['fuel']['current']

    # Get current credits
    agent = api.get_agent()
    if not agent:
        logging.error("Failed to get agent data")
        return 1

    starting_credits = agent['credits']

    # Determine operation mode
    fixed_route_mode = bool(getattr(args, 'good', None) and
                            getattr(args, 'buy_from', None) and
                            getattr(args, 'sell_to', None))
    looping_mode = getattr(args, 'cycles', None) is not None or getattr(args, 'duration', None) is not None

    if fixed_route_mode:
        logging.info("Mode: FIXED-ROUTE (prescriptive trading)")
        logging.info(f"Route: {args.good} from {args.buy_from} → {args.sell_to}")
    else:
        logging.info("Mode: AUTONOMOUS (route optimization)")

    if looping_mode:
        cycles = getattr(args, 'cycles', None)
        duration = getattr(args, 'duration', None)
        if cycles is not None:
            if cycles == -1:
                logging.info("Looping: INFINITE")
            else:
                logging.info(f"Looping: {cycles} cycles")
        else:
            logging.info(f"Duration: {duration} hours")
    else:
        logging.info("Mode: ONE-SHOT")

    logging.info(f"Min profit threshold: {args.min_profit:,} credits")
    logging.info("="*70)

    # Initialize loop control
    if looping_mode:
        if getattr(args, 'duration', None):
            start_time = datetime.now()
            end_time = start_time + timedelta(hours=args.duration)
            cycles_remaining = float('inf')
        else:
            end_time = None
            cycles_remaining = args.cycles if args.cycles != -1 else float('inf')
    else:
        end_time = None
        cycles_remaining = 1

    cycle_num = 0
    total_profit = 0
    low_profit_breaker = CircuitBreaker(limit=3)

    # Main trading loop
    while cycles_remaining > 0:
        cycle_num += 1

        # Check time limit
        if end_time and datetime.now() >= end_time:
            logging.info("Duration limit reached")
            break

        if looping_mode:
            logging.info(f"\n{'='*70}")
            logging.info(f"CYCLE {cycle_num}")
            if cycles_remaining != float('inf'):
                logging.info(f"Remaining: {int(cycles_remaining)} cycles")
            logging.info('='*70)

        # Get current ship status
        ship_data = ship.get_status()
        if not ship_data:
            logging.error("Failed to get ship status")
            return 1

        current_waypoint = ship_data['nav']['waypointSymbol']
        current_fuel = ship_data['fuel']['current']

        # Get current credits
        agent = api.get_agent()
        if not agent:
            logging.error("Failed to get agent data")
            return 1

        cycle_start_credits = agent['credits']

        # Find or create route
        if fixed_route_mode:
            route = create_fixed_route(
                api, db, args.player_id,
                current_waypoint,
                args.buy_from,
                args.sell_to,
                args.good,
                cargo_capacity,
                cycle_start_credits,
                ship_speed,
                fuel_capacity,
                current_fuel
            )
        else:
            optimizer = MultiLegTradeOptimizer(api, db, args.player_id)
            route = optimizer.find_optimal_route(
                start_waypoint=current_waypoint,
                system=system,
                max_stops=args.max_stops,
                cargo_capacity=cargo_capacity,
                starting_credits=cycle_start_credits,
                ship_speed=ship_speed,
                fuel_capacity=fuel_capacity,
                current_fuel=current_fuel
            )

        if not route:
            logging.error("No profitable route found")
            if looping_mode:
                logging.warning("Breaking loop - no profitable routes available")
            return 1

        # Execute the route
        logging.info("\n" + "="*70)
        logging.info("EXECUTING MULTI-LEG ROUTE")
        logging.info("="*70)

        executor = RouteExecutor(ship, api, db, args.player_id, logging.getLogger(__name__))
        success = executor.execute_route(route)

        if not success:
            logging.error("\n" + "="*70)
            logging.error("❌ MULTI-LEG ROUTE FAILED")
            logging.error("="*70)
            return 1

        # Calculate cycle profit
        final_agent = api.get_agent()
        cycle_end_credits = final_agent['credits'] if final_agent else cycle_start_credits
        cycle_profit = cycle_end_credits - cycle_start_credits
        total_profit += cycle_profit

        logging.info("\n" + "="*70)
        if looping_mode:
            logging.info(f"CYCLE {cycle_num} COMPLETE")
        else:
            logging.info("✅ MULTI-LEG ROUTE COMPLETE")
        logging.info("="*70)
        logging.info(f"Cycle profit: {cycle_profit:,}")
        logging.info(f"Estimated profit: {route.total_profit:,}")
        logging.info(f"Accuracy: {(cycle_profit/route.total_profit*100) if route.total_profit > 0 else 0:.1f}%")

        if looping_mode:
            logging.info(f"Total profit: {total_profit:,}")
            logging.info(f"Average profit/cycle: {total_profit // cycle_num:,}")

        logging.info("="*70)

        # Circuit breaker: Check profitability for looping mode
        if looping_mode:
            if cycle_profit < args.min_profit:
                failures = low_profit_breaker.record_failure()
                logging.warning(
                    "Low profit (%s < %s)",
                    f"{cycle_profit:,}",
                    f"{args.min_profit:,}",
                )
                logging.warning("Consecutive low cycles: %s", failures)

                if low_profit_breaker.tripped():
                    logging.error(
                        "🚨 %s consecutive low-profit cycles", failures
                    )
                    logging.error("🛑 STOPPING")
                    break
            else:
                low_profit_breaker.record_success()

            if cycle_profit < 0:
                logging.error(f"🚨 CIRCUIT BREAKER: NEGATIVE PROFIT ({cycle_profit:,})")
                logging.error("🛑 STOPPING")
                break

        # Decrement cycles
        if cycles_remaining != float('inf'):
            cycles_remaining -= 1

        # Brief pause between cycles
        if looping_mode and cycles_remaining > 0:
            time.sleep(2)

    # Final summary
    final_agent = api.get_agent()
    final_credits = final_agent['credits'] if final_agent else starting_credits

    logging.info(f"\n{'='*70}")
    logging.info("OPERATION COMPLETE")
    logging.info('='*70)
    logging.info(f"Starting credits: {starting_credits:,}")
    logging.info(f"Final credits: {final_credits:,}")
    logging.info(f"Total profit: {total_profit:,}")
    logging.info(f"Cycles completed: {cycle_num}")
    if cycle_num > 0:
        logging.info(f"Average profit/cycle: {total_profit // cycle_num:,}")
    logging.info('='*70)

    return 0


def fleet_trade_optimize_operation(args):
    """
    Fleet trade route optimization operation - finds conflict-free profitable routes for multiple ships.

    Args:
        args: CLI arguments with player_id, ships (comma-separated), system, max_stops

    Returns:
        0 on success, 1 on failure
    """
    from .common import setup_logging, get_api_client
    from ..core.database import Database
    from ..core.ship_controller import ShipController

    log_file = setup_logging("fleet_trade_optimize", "fleet", getattr(args, 'log_level', 'INFO'), player_id=args.player_id)

    print("=" * 70)
    print("FLEET TRADE ROUTE OPTIMIZATION")
    print("=" * 70)
    print(f"System: {args.system}")
    print(f"Max Stops: {args.max_stops}")
    print("=" * 70)

    # Parse ship list
    ship_symbols = [s.strip() for s in args.ships.split(',')]
    print(f"\nOptimizing routes for {len(ship_symbols)} ships:")
    for ship_symbol in ship_symbols:
        print(f"  - {ship_symbol}")
    print()

    # Initialize components
    api = get_api_client(args.player_id)
    db = Database()

    # Get agent starting credits
    agent = api.get_agent()
    if not agent:
        print("❌ Failed to get agent data")
        return 1
    starting_credits = agent['credits']

    # Get ship data for all ships
    ships = []
    for ship_symbol in ship_symbols:
        ship_data = api.get(f"/my/ships/{ship_symbol}")
        if not ship_data or 'data' not in ship_data:
            print(f"❌ Failed to get data for ship {ship_symbol}")
            return 1
        ships.append(ship_data['data'])

    # Initialize optimizer
    print("Initializing fleet optimizer...")
    optimizer = FleetTradeOptimizer(api, db, player_id=args.player_id)

    # Run optimization
    print(f"\nOptimizing fleet routes in {args.system}...\n")
    try:
        result = optimizer.optimize_fleet(
            ships=ships,
            system=args.system,
            max_stops=args.max_stops,
            starting_credits=starting_credits,
        )
    except Exception as e:
        print(f"\n❌ Optimization failed: {e}")
        import traceback
        traceback.print_exc()
        return 1

    if not result or not result.get('ship_routes'):
        print("\n❌ No routes found for fleet")
        return 1

    # Display results
    print("\n" + "=" * 70)
    print("FLEET OPTIMIZATION RESULTS")
    print("=" * 70)
    print(f"Total Fleet Profit: {result['total_fleet_profit']:,} credits\n")

    # Create ship lookup
    ship_lookup = {s['symbol']: s for s in ships}

    for i, (ship_symbol, route) in enumerate(result['ship_routes'].items(), 1):
        print(f"\n{'='*70}")
        print(f"SHIP {i}/{len(result['ship_routes'])}: {ship_symbol}")
        print('='*70)

        if not route or not route.segments:
            print("  No profitable route found")
            continue

        # Get ship's current location
        ship = ship_lookup[ship_symbol]
        start_waypoint = ship['nav']['waypointSymbol']

        # Show route summary
        waypoints = [start_waypoint] + [seg.to_waypoint for seg in route.segments]
        route_str = " → ".join(waypoints)
        print(f"Route: {route_str}")
        print(f"Estimated Profit: {route.total_profit:,} credits")
        print(f"Segments: {len(route.segments)}")
        print(f"Total Distance: {route.total_distance:.0f} units")
        print(f"Est. Duration: {route.estimated_time_minutes:.0f} minutes")

        # Show BUY actions (the ones that matter for conflicts)
        print(f"\nBUY Actions (reserved for conflict avoidance):")
        for j, segment in enumerate(route.segments, 1):
            for action in segment.actions_at_destination:
                if action.action == 'BUY':
                    print(f"  {j}. {segment.to_waypoint}: BUY {action.units}x {action.good}")

    # Show conflict status
    print(f"\n{'='*70}")
    print("CONFLICT ANALYSIS")
    print('='*70)
    reserved = result.get('reserved_pairs', set())
    print(f"Total Reserved (Resource, Waypoint) Pairs: {len(reserved)}")
    print(f"Conflicts Detected: {result.get('conflicts', 0)}")

    if result.get('conflicts', 0) == 0:
        print("✅ All routes are conflict-free!")
    else:
        print("⚠️  WARNING: Conflicts detected between routes")

    print("\n" + "=" * 70)
    print("\nUse these parameters to start daemons:")
    print("-" * 70)

    for ship_symbol, route in result['ship_routes'].items():
        if not route or not route.segments:
            continue

        # Get ship's current location
        ship = ship_lookup[ship_symbol]
        start_waypoint = ship['nav']['waypointSymbol']

        # Build daemon command
        waypoints = [start_waypoint] + [seg.to_waypoint for seg in route.segments]

        # For simplicity, recommend multileg-trade with system parameter
        print(f"\n# {ship_symbol}")
        print(f"spacetraders-bot daemon start trade \\")
        print(f"  --player-id {args.player_id} \\")
        print(f"  --ship {ship_symbol} \\")
        print(f"  --system {args.system} \\")
        print(f"  --max-stops {args.max_stops} \\")
        print(f"  --cycles 10")

    print("\n" + "=" * 70)

    return 0
