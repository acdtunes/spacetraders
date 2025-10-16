#!/usr/bin/env python3
from __future__ import annotations

"""
Mining operations: autonomous resource extraction with intelligent routing
"""

import logging
from dataclasses import asdict, dataclass
from datetime import datetime, timezone
from typing import Callable, Dict, List, Optional, Tuple

from spacetraders_bot.core.api_client import APIClient
from spacetraders_bot.core.operation_controller import OperationController
from spacetraders_bot.core.ship_controller import ShipController
from spacetraders_bot.core.smart_navigator import SmartNavigator
from spacetraders_bot.operations.common import (
    get_api_client,
    get_captain_logger,
    get_operator_name,
    humanize_duration,
    log_captain_event,
    setup_logging,
)
from spacetraders_bot.operations.control import CircuitBreaker


SEPARATOR = "=" * 70


@dataclass
class MiningStats:
    cycles_completed: int = 0
    total_extracted: int = 0
    total_sold: int = 0
    total_revenue: int = 0


@dataclass
class MiningContext:
    args: any
    ship: ShipController
    navigator: SmartNavigator
    controller: Optional[OperationController]
    stats: MiningStats
    log_error: Callable[..., None]


@dataclass
class MiningCycle:
    """Executes a single mining cycle from asteroid extraction to market sale."""

    context: MiningContext
    total_cycles: int
    asteroid: str
    market: str

    def execute(self, cycle: int) -> bool:
        self._print_cycle_header(cycle)

        if not self._navigate_to_asteroid(cycle):
            return False

        self.context.ship.orbit()
        cargo = _mine_until_cargo_full(self.context)

        if not self._navigate_to_market(cycle):
            return False

        revenue = _sell_cargo(self.context, cargo)
        self._refuel_ship()

        self.context.stats.cycles_completed += 1
        _checkpoint_cycle(self.context, cycle, self.market)

        self._print_cycle_summary(cycle, revenue, cargo)
        return True

    def _navigate_to_asteroid(self, cycle: int) -> bool:
        return _navigate_with_retries(
            self.context,
            self.asteroid,
            cycle,
            f"\n1. Navigating to asteroid {self.asteroid}...",
            "Check fuel levels and route",
        )

    def _navigate_to_market(self, cycle: int) -> bool:
        return _navigate_with_retries(
            self.context,
            self.market,
            cycle,
            f"\n3. Navigating to market {self.market}...",
            "Verify route and refuel options",
        )

    def _refuel_ship(self) -> None:
        print("\n5. Refueling...")
        self.context.ship.refuel()

    def _print_cycle_header(self, cycle: int) -> None:
        print(f"\n{SEPARATOR}")
        print(f"CYCLE {cycle}/{self.total_cycles}")
        print(SEPARATOR)

    def _print_cycle_summary(self, cycle: int, revenue: int, cargo: Optional[Dict]) -> None:
        cargo_units = cargo.get('units', 0) if cargo else 0
        print(f"\n{SEPARATOR}")
        print(f"CYCLE {cycle} COMPLETE")
        print(f"Revenue this cycle: {revenue:,} credits")
        print(f"Cargo sold: {cargo_units} units")
        print(f"Total revenue: {self.context.stats.total_revenue:,} credits")
        print(f"Total extracted: {self.context.stats.total_extracted} units")
        print(SEPARATOR)


@dataclass
class TargetedMiningSession:
    """Encapsulates targeted mining with circuit-breaker protection."""

    context: MiningContext
    target_resource: str
    units_needed: int
    breaker: CircuitBreaker
    units_collected: int = 0
    total_extractions: int = 0

    def run(self) -> Tuple[bool, int, str]:
        while self.units_collected < self.units_needed:
            if self.breaker.tripped():
                return self._breaker_failure()

            self._wait_if_cooldown_active()
            self._process_extraction()

            if self.breaker.tripped():
                return self._breaker_failure()

            self._manage_cargo()

        return self._success()

    def _wait_if_cooldown_active(self) -> None:
        ship_data = self.context.ship.get_status()
        cooldown = 0
        if ship_data:
            cooldown = ship_data.get('cooldown', {}).get('remainingSeconds', 0)
        if cooldown > 0:
            self.context.ship.wait_for_cooldown(cooldown)

    def _process_extraction(self) -> None:
        extraction = self.context.ship.extract()
        self.total_extractions += 1

        if not extraction:
            failure_count = self.breaker.record_failure()
            print(f"❌ Extraction failed (failure #{failure_count})")
            return

        symbol = extraction['symbol']
        units = extraction['units']
        self.context.stats.total_extracted += units

        if symbol == self.target_resource:
            self.units_collected += units
            self.breaker.record_success()
            print(
                f"✅ Got {units} x {self.target_resource} "
                f"(total: {self.units_collected}/{self.units_needed})"
            )
        else:
            failure_count = self.breaker.record_failure()
            print(
                f"⚠️  Got {units} x {symbol} instead "
                f"(failure #{failure_count})"
            )

        self.context.ship.jettison_wrong_cargo(self.target_resource, cargo_threshold=0.8)
        self.context.ship.wait_for_cooldown(extraction['cooldown'])

    def _manage_cargo(self) -> None:
        cargo = self.context.ship.get_cargo()
        if not cargo:
            return

        if cargo['units'] < cargo['capacity'] - 1:
            return

        has_target = any(
            item['symbol'] == self.target_resource for item in cargo.get('inventory', [])
        )
        if not has_target:
            print("⚠️  Cargo full of wrong materials, jettisoning all...")
            self.context.ship.jettison_wrong_cargo(
                self.target_resource,
                cargo_threshold=0.0,
            )

    def _breaker_failure(self) -> Tuple[bool, int, str]:
        print("\n🛑 CIRCUIT BREAKER TRIGGERED!")
        print(
            f"   {self.breaker.failures} "
            f"consecutive extractions without {self.target_resource}"
        )
        print(f"   This asteroid may not contain {self.target_resource}")
        print("   Recommend: Switch to different asteroid or buy from market")
        return False, self.units_collected, (
            f"Circuit breaker: {self.breaker.failures} consecutive failures"
        )

    def _success(self) -> Tuple[bool, int, str]:
        print(
            f"\n✅ Collected {self.units_collected} units of {self.target_resource}"
        )
        print(f"   Total extractions: {self.total_extractions}")
        if self.total_extractions > 0:
            success_rate = (self.units_collected / self.total_extractions) * 100
            print(f"   Success rate: {success_rate:.1f}%")
        else:
            print("   Success rate: N/A")
        return True, self.units_collected, "Success"


def _stats_from_checkpoint(checkpoint: dict, default: MiningStats) -> Tuple[int, MiningStats]:
    start_cycle = checkpoint.get('cycle', 0) + 1
    stats_payload = checkpoint.get('stats') or asdict(default)
    stats = MiningStats(**stats_payload)
    return start_cycle, stats


def _navigate_with_retries(
    context: MiningContext,
    destination: str,
    cycle: int,
    description: str,
    resolution: str,
) -> bool:
    print(description)
    success = context.navigator.execute_route(
        context.ship,
        destination,
        operation_controller=context.controller,
    )

    if success:
        return True

    print("❌ Navigation failed, aborting")
    if context.controller:
        context.controller.fail(f"Navigation to {destination} failed at cycle {cycle}")
    context.log_error(
        "Navigation failure",
        f"Unable to reach {destination}",
        impact={'Cycle': cycle},
        resolution=resolution,
        escalate=True,
    )
    return False


def _mine_until_cargo_full(context: MiningContext) -> Dict:
    print(f"\n2. Mining until cargo full...")
    cargo = context.ship.get_cargo()

    while cargo and cargo['units'] < cargo['capacity'] - 1:
        ship_status = context.ship.get_status()
        remaining = ship_status['cooldown']['remainingSeconds'] if ship_status else 0
        if remaining > 0:
            context.ship.wait_for_cooldown(remaining)

        extraction = context.ship.extract()
        if extraction:
            context.stats.total_extracted += extraction['units']
            context.ship.wait_for_cooldown(extraction['cooldown'])

        cargo = context.ship.get_cargo()

    return cargo or {'units': 0}


def _sell_cargo(context: MiningContext, cargo: Dict) -> int:
    print(f"\n4. Selling cargo...")
    context.ship.dock()
    revenue = context.ship.sell_all()
    context.stats.total_revenue += revenue
    context.stats.total_sold += cargo.get('units', 0)
    return revenue


def _checkpoint_cycle(context: MiningContext, cycle: int, location: str) -> None:
    if not context.controller:
        return

    context.controller.checkpoint({
        'cycle': cycle,
        'stats': asdict(context.stats),
        'location': location,
    })


def mining_operation(args):
    """
    Autonomous mining operation with intelligent routing and checkpoint/resume

    Features:
    - Smart navigation with fuel optimization
    - Automatic refuel stop insertion
    - Checkpoint/resume on crash
    - Route validation before execution
    """
    # Setup logging first (agent-scoped by player_id)
    log_file = setup_logging("mining", args.ship, getattr(args, 'log_level', 'INFO'), player_id=args.player_id)

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

    def log_completion(stats_snapshot: MiningStats) -> None:
        duration = humanize_duration(datetime.now(timezone.utc) - operation_start)
        log_captain_event(
            captain_logger,
            'OPERATION_COMPLETED',
            operator=operator_name,
            ship=args.ship,
            duration=duration,
            results={
                'Cycles Completed': stats_snapshot.cycles_completed,
                'Total Revenue': f"{stats_snapshot.total_revenue:,} cr",
                'Units Extracted': stats_snapshot.total_extracted,
                'Units Sold': stats_snapshot.total_sold,
            },
            notes=f"Mined at {args.asteroid} and sold at {args.market}.",
            tags=['mining', args.asteroid.lower(), args.market.lower()]
        )

    def log_performance(stats_snapshot: MiningStats) -> None:
        revenue = stats_snapshot.total_revenue
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
                'completed': stats_snapshot.cycles_completed,
                'active': max(args.cycles - stats_snapshot.cycles_completed, 0),
                'success_rate': 100 if stats_snapshot.cycles_completed == args.cycles else 0,
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
    stats = MiningStats()

    if controller.can_resume():
        checkpoint = controller.resume()
        start_cycle, stats = _stats_from_checkpoint(checkpoint, stats)
        print(f"\n♻️  Resuming from cycle {start_cycle}/{args.cycles}")
        print(f"   Previous progress: {stats.cycles_completed} cycles, {stats.total_revenue:,} credits")
    else:
        controller.start({
            "ship": args.ship,
            "asteroid": args.asteroid,
            "market": args.market,
            "cycles": args.cycles,
            "system": system
        })

    context = MiningContext(
        args=args,
        ship=ship,
        navigator=navigator,
        controller=controller,
        stats=stats,
        log_error=log_error,
    )

    cycle_runner = MiningCycle(
        context=context,
        total_cycles=args.cycles,
        asteroid=args.asteroid,
        market=args.market,
    )

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

        if not cycle_runner.execute(cycle):
            return 1

    # Final summary
    print(f"\n{SEPARATOR}")
    print("MINING OPERATION COMPLETE")
    print(SEPARATOR)
    print(f"Cycles completed: {context.stats.cycles_completed}")
    print(f"Total revenue: {context.stats.total_revenue:,} credits")
    print(f"Total extracted: {context.stats.total_extracted} units")
    print(f"Total sold: {context.stats.total_sold} units")
    print(
        f"Average per cycle: {context.stats.total_revenue // context.stats.cycles_completed:,} credits"
        if context.stats.cycles_completed > 0
        else "N/A"
    )
    print('='*70)

    # Mark operation as complete and cleanup
    controller.complete({
        "cycles": context.stats.cycles_completed,
        "revenue": context.stats.total_revenue
    })
    controller.cleanup()

    log_completion(context.stats)
    log_performance(context.stats)

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

    context = MiningContext(
        args=None,
        ship=ship,
        navigator=navigator,
        controller=None,
        stats=MiningStats(),
        log_error=lambda *_, **__: None,
    )

    if not _navigate_with_retries(
        context,
        asteroid,
        cycle=0,
        description=f"\n1. Navigating to asteroid {asteroid}...",
        resolution="Verify route and refuel options",
    ):
        return False, 0, "Navigation to asteroid failed"

    ship.orbit()

    session = TargetedMiningSession(
        context=context,
        target_resource=target_resource,
        units_needed=units_needed,
        breaker=CircuitBreaker(limit=max_consecutive_failures),
    )

    return session.run()

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
