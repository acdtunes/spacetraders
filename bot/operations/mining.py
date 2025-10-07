#!/usr/bin/env python3
"""
Mining operations: autonomous resource extraction with intelligent routing
"""

import sys
import logging
from datetime import datetime, timezone
from pathlib import Path

# Add lib directory to path
sys.path.insert(0, str(Path(__file__).parent.parent / "lib"))

from api_client import APIClient
from ship_controller import ShipController
from smart_navigator import SmartNavigator
from operation_controller import OperationController
from .common import (
    setup_logging,
    get_api_client,
    get_captain_logger,
    log_captain_event,
    humanize_duration,
    get_operator_name,
)
from typing import Optional, Tuple, List


def mining_operation(args):
    """
    Autonomous mining operation with intelligent routing and checkpoint/resume

    Features:
    - Smart navigation with fuel optimization
    - Automatic refuel stop insertion
    - Checkpoint/resume on crash
    - Route validation before execution
    """
    # Setup logging first
    log_file = setup_logging("mining", args.ship, getattr(args, 'log_level', 'INFO'))

    logging.info("Initializing mining operation")
    logging.info(f"Player ID: {args.player_id}")
    logging.info(f"Ship: {args.ship}")
    logging.info(f"Asteroid: {args.asteroid}")
    logging.info(f"Market: {args.market}")
    logging.info(f"Cycles: {args.cycles}")

    api = get_api_client(args.player_id)
    ship = ShipController(api, args.ship)

    operation_start = datetime.now(timezone.utc)
    captain_logger = get_captain_logger(args.player_id)
    operator_name = get_operator_name(args)

    def log_error(error: str, cause: str, *, impact: Optional[dict] = None,
                  resolution: str = "Manual follow-up", lesson: str = "Review mining plan",
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
            tags=tags or ['mining']
        )

    def log_completion(stats_snapshot: dict) -> None:
        duration = humanize_duration(datetime.now(timezone.utc) - operation_start)
        log_captain_event(
            captain_logger,
            'OPERATION_COMPLETED',
            operator=operator_name,
            ship=args.ship,
            duration=duration,
            results={
                'Cycles Completed': stats_snapshot.get('cycles_completed', 0),
                'Total Revenue': f"{stats_snapshot.get('total_revenue', 0):,} cr",
                'Units Extracted': stats_snapshot.get('total_extracted', 0),
                'Units Sold': stats_snapshot.get('total_sold', 0),
            },
            notes=f"Mined at {args.asteroid} and sold at {args.market}.",
            tags=['mining', args.asteroid.lower(), args.market.lower()]
        )

    def log_performance(stats_snapshot: dict) -> None:
        revenue = stats_snapshot.get('total_revenue', 0)
        elapsed = datetime.now(timezone.utc) - operation_start
        hours = max(elapsed.total_seconds() / 3600, 0.0001)
        rate = int(revenue / hours) if revenue else 0
        log_captain_event(
            captain_logger,
            'PERFORMANCE_SUMMARY',
            summary_type='Mining Operation',
            financials={
                'revenue': revenue,
                'cumulative': revenue,
                'rate': rate,
            },
            operations={
                'completed': stats_snapshot.get('cycles_completed', 0),
                'active': max(args.cycles - stats_snapshot.get('cycles_completed', 0), 0),
                'success_rate': 100 if stats_snapshot.get('cycles_completed', 0) == args.cycles else 0,
            },
            fleet={'active': 1, 'total': 1},
            top_performers=[{
                'ship': args.ship,
                'profit': revenue,
                'operation': 'mining'
            }],
            tags=['mining', 'performance']
        )

    # Get ship data and system
    ship_data = ship.get_status()
    if not ship_data:
        print("❌ Failed to get ship status")
        log_error(
            "Ship status unavailable",
            "API returned no ship data",
            resolution="Verify ship symbol and connection"
        )
        return 1

    system = ship_data['nav']['systemSymbol']

    # Initialize navigator
    print(f"📡 Initializing navigator for {system}...")
    navigator = SmartNavigator(api, system)

    # Initialize operation controller for checkpoint/resume
    op_id = f"mine_{args.ship}_{args.cycles}"
    controller = OperationController(op_id)

    # Pre-flight route validation
    print("\n🔍 Validating routes...")
    valid, reason = navigator.validate_route(ship_data, args.asteroid)
    if not valid:
        print(f"❌ Route to asteroid not feasible: {reason}")
        log_error(
            "Route validation failed",
            reason,
            impact={'Asteroid': args.asteroid},
            resolution="Adjust ship fuel or select nearer asteroid"
        )
        return 1
    print(f"  ✅ Asteroid route: {reason}")

    # Simulate fuel state after mining
    ship_data_after_mining = ship_data.copy()
    asteroid_fuel = navigator.get_fuel_estimate(ship_data, args.asteroid)
    if asteroid_fuel:
        ship_data_after_mining['fuel']['current'] = ship_data['fuel']['capacity'] - asteroid_fuel['total_fuel_cost']

    market_fuel = navigator.get_fuel_estimate(ship_data_after_mining, args.market)
    if not market_fuel:
        print(f"❌ Cannot route to market from asteroid (insufficient fuel)")
        log_error(
            "Market route infeasible",
            "Navigator could not find fuel-safe path",
            impact={'Asteroid': args.asteroid, 'Market': args.market},
            resolution="Insert refuel stop or pick closer market",
            escalate=True
        )
        return 1
    print(f"  ✅ Market route: {market_fuel['refuel_stops']} refuel stop(s)")

    # Check for existing operation to resume
    start_cycle = 1
    stats = {
        "cycles_completed": 0,
        "total_extracted": 0,
        "total_sold": 0,
        "total_revenue": 0
    }

    if controller.can_resume():
        checkpoint = controller.resume()
        start_cycle = checkpoint.get('cycle', 0) + 1
        stats = checkpoint.get('stats', stats)
        print(f"\n♻️  Resuming from cycle {start_cycle}/{args.cycles}")
        print(f"   Previous progress: {stats['cycles_completed']} cycles, {stats['total_revenue']:,} credits")
    else:
        controller.start({
            "ship": args.ship,
            "asteroid": args.asteroid,
            "market": args.market,
            "cycles": args.cycles,
            "system": system
        })

    # Mining loop with smart navigation
    for cycle in range(start_cycle, args.cycles + 1):
        # Check for control commands
        if controller.should_cancel():
            print("\n⚠️  Operation cancelled by external command")
            controller.cancel()
            log_error(
                "Mining operation cancelled",
                "External cancel command received",
                resolution="Restart operation when ready",
                escalate=False
            )
            return 1

        if controller.should_pause():
            print("\n⏸️  Operation paused by external command")
            controller.pause()
            return 0

        print(f"\n{'='*70}")
        print(f"CYCLE {cycle}/{args.cycles}")
        print('='*70)

        # Navigate to asteroid
        print(f"\n1. Navigating to asteroid {args.asteroid}...")
        success = navigator.execute_route(ship, args.asteroid, prefer_cruise=True, operation_controller=controller)
        if not success:
            print("❌ Navigation failed, aborting")
            controller.fail(f"Navigation to asteroid failed at cycle {cycle}")
            log_error(
                "Navigation failure",
                f"Unable to reach asteroid {args.asteroid}",
                impact={'Cycle': cycle},
                resolution="Check fuel levels and route",
                escalate=True
            )
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

        # Navigate to market
        print(f"\n3. Navigating to market {args.market}...")
        success = navigator.execute_route(ship, args.market, prefer_cruise=True, operation_controller=controller)
        if not success:
            print("❌ Navigation failed, aborting")
            controller.fail(f"Navigation to market failed at cycle {cycle}")
            log_error(
                "Navigation failure",
                f"Unable to reach market {args.market}",
                impact={'Cycle': cycle},
                resolution="Verify route and refuel options",
                escalate=True
            )
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

        stats['cycles_completed'] += 1

        # Save checkpoint after each cycle
        controller.checkpoint({
            'cycle': cycle,
            'stats': stats,
            'location': args.market
        })

        # Cycle summary
        print(f"\n{'='*70}")
        print(f"CYCLE {cycle} COMPLETE")
        print(f"Revenue this cycle: {revenue:,} credits")
        print(f"Total revenue: {stats['total_revenue']:,} credits")
        print(f"Total extracted: {stats['total_extracted']} units")
        print('='*70)

    # Final summary
    print(f"\n{'='*70}")
    print("MINING OPERATION COMPLETE")
    print('='*70)
    print(f"Cycles completed: {stats['cycles_completed']}")
    print(f"Total revenue: {stats['total_revenue']:,} credits")
    print(f"Total extracted: {stats['total_extracted']} units")
    print(f"Total sold: {stats['total_sold']} units")
    print(f"Average per cycle: {stats['total_revenue'] // stats['cycles_completed']:,} credits" if stats['cycles_completed'] > 0 else "N/A")
    print('='*70)

    # Mark operation as complete and cleanup
    controller.complete({
        "cycles": stats['cycles_completed'],
        "revenue": stats['total_revenue']
    })
    controller.cleanup()

    log_completion(stats)
    log_performance(stats)

    return 0


def targeted_mining_with_circuit_breaker(
    ship: ShipController,
    navigator: SmartNavigator,
    asteroid: str,
    target_resource: str,
    units_needed: int,
    max_consecutive_failures: int = 10
) -> Tuple[bool, int, str]:
    """
    Mine a specific resource with circuit breaker for wrong cargo

    Features:
    - Tracks consecutive failures (didn't get target resource)
    - Jettisons wrong cargo when cargo fills up
    - Circuit breaker: stops after max consecutive failures
    - Returns success status, units collected, and failure reason

    Args:
        ship: Ship controller instance
        navigator: Smart navigator instance
        asteroid: Asteroid waypoint to mine at
        target_resource: Specific resource to mine (e.g., "ALUMINUM_ORE")
        units_needed: How many units of target resource needed
        max_consecutive_failures: Stop after this many failed extractions (default 10)

    Returns:
        Tuple of (success: bool, units_collected: int, reason: str)
    """
    print(f"\n🎯 Targeted mining: {target_resource} (need {units_needed} units)")
    print(f"   Circuit breaker: Will stop after {max_consecutive_failures} consecutive failures")

    # Navigate to asteroid
    print(f"\n1. Navigating to asteroid {asteroid}...")
    success = navigator.execute_route(ship, asteroid, prefer_cruise=True)
    if not success:
        return False, 0, "Navigation to asteroid failed"

    ship.orbit()

    # Mining loop with circuit breaker
    consecutive_failures = 0
    units_collected = 0
    total_extractions = 0

    while units_collected < units_needed:
        # Check circuit breaker
        if consecutive_failures >= max_consecutive_failures:
            print(f"\n🛑 CIRCUIT BREAKER TRIGGERED!")
            print(f"   {consecutive_failures} consecutive extractions without {target_resource}")
            print(f"   This asteroid may not contain {target_resource}")
            print(f"   Recommend: Switch to different asteroid or buy from market")
            return False, units_collected, f"Circuit breaker: {consecutive_failures} consecutive failures"

        # Check cooldown
        ship_data = ship.get_status()
        if ship_data and ship_data.get('cooldown') and ship_data['cooldown'].get('remainingSeconds', 0) > 0:
            ship.wait_for_cooldown(ship_data['cooldown']['remainingSeconds'])

        # Extract
        extraction = ship.extract()
        total_extractions += 1

        if extraction:
            extracted_symbol = extraction['symbol']
            extracted_units = extraction['units']

            # Check if we got the target resource
            if extracted_symbol == target_resource:
                units_collected += extracted_units
                consecutive_failures = 0  # Reset circuit breaker
                print(f"✅ Got {extracted_units} x {target_resource} (total: {units_collected}/{units_needed})")
            else:
                consecutive_failures += 1
                print(f"⚠️  Got {extracted_units} x {extracted_symbol} instead (failure #{consecutive_failures})")

            # Jettison wrong cargo if getting full
            ship.jettison_wrong_cargo(target_resource, cargo_threshold=0.8)

            ship.wait_for_cooldown(extraction['cooldown'])
        else:
            consecutive_failures += 1
            print(f"❌ Extraction failed (failure #{consecutive_failures})")

        # Safety check: If cargo completely full of wrong materials, jettison and continue
        cargo = ship.get_cargo()
        if cargo and cargo['units'] >= cargo['capacity'] - 1:
            has_target = any(item['symbol'] == target_resource for item in cargo['inventory'])
            if not has_target:
                print(f"⚠️  Cargo full of wrong materials, jettisoning all...")
                ship.jettison_wrong_cargo(target_resource, cargo_threshold=0.0)

    # Success
    print(f"\n✅ Collected {units_collected} units of {target_resource}")
    print(f"   Total extractions: {total_extractions}")
    print(f"   Success rate: {(units_collected/total_extractions)*100:.1f}%" if total_extractions > 0 else "   Success rate: N/A")

    return True, units_collected, "Success"


def find_alternative_asteroids(
    api: APIClient,
    system: str,
    current_asteroid: str,
    target_resource: str
) -> list:
    """
    Find alternative asteroids in the system that may contain the target resource

    Args:
        api: API client instance
        system: System symbol (e.g., "X1-HU87")
        current_asteroid: Current asteroid that's not working
        target_resource: Resource we're looking for (e.g., "ALUMINUM_ORE")

    Returns:
        List of alternative asteroid waypoint symbols
    """
    print(f"\n🔍 Searching for alternative asteroids with {target_resource}...")

    # Map resources to asteroid traits
    resource_to_traits = {
        "ALUMINUM_ORE": ["COMMON_METAL_DEPOSITS", "MINERAL_DEPOSITS"],
        "IRON_ORE": ["COMMON_METAL_DEPOSITS"],
        "COPPER_ORE": ["COMMON_METAL_DEPOSITS"],
        "QUARTZ_SAND": ["MINERAL_DEPOSITS", "COMMON_METAL_DEPOSITS"],
        "SILICON_CRYSTALS": ["MINERAL_DEPOSITS", "CRYSTALLINE_STRUCTURES"],
        "PRECIOUS_METAL_DEPOSITS": ["PRECIOUS_METAL_DEPOSITS"],
        "GOLD_ORE": ["PRECIOUS_METAL_DEPOSITS"],
        "SILVER_ORE": ["PRECIOUS_METAL_DEPOSITS"],
        "PLATINUM_ORE": ["PRECIOUS_METAL_DEPOSITS"]
    }

    target_traits = resource_to_traits.get(target_resource, ["COMMON_METAL_DEPOSITS"])
    alternatives = []

    # Get all waypoints in system (paginated)
    page = 1
    while True:
        result = api.list_waypoints(system, limit=20, page=page)
        if not result or 'data' not in result:
            break

        waypoints = result['data']
        if not waypoints:
            break

        for wp in waypoints:
            # Skip current asteroid
            if wp['symbol'] == current_asteroid:
                continue

            # Check if it's an asteroid
            if wp['type'] != 'ASTEROID':
                continue

            # Check traits
            wp_traits = [trait['symbol'] for trait in wp.get('traits', [])]

            # Skip stripped asteroids
            if 'STRIPPED' in wp_traits:
                continue

            # Check if has target traits
            has_target_trait = any(trait in wp_traits for trait in target_traits)
            if has_target_trait:
                alternatives.append(wp['symbol'])
                print(f"   Found: {wp['symbol']} with traits {wp_traits}")

        # Check if there are more pages
        meta = result.get('meta', {})
        if page >= meta.get('total', 1):
            break

        page += 1

    print(f"\n📍 Found {len(alternatives)} alternative asteroids")
    return alternatives
