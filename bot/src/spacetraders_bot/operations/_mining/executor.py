"""
Mining Operation Executor

Orchestrates autonomous mining operations with intelligent routing and checkpoint/resume
"""

import logging
from datetime import datetime, timezone
from typing import Optional, List

from spacetraders_bot.core.operation_controller import OperationController
from spacetraders_bot.core.ship import ShipController
from spacetraders_bot.core.smart_navigator import SmartNavigator
from spacetraders_bot.operations.common import (
    get_api_client,
    get_captain_logger,
    get_operator_name,
    humanize_duration,
    log_captain_event,
)

from .mining_cycle import MiningCycle, MiningContext, MiningStats, stats_from_checkpoint


SEPARATOR = "=" * 70


class MiningOperationExecutor:
    """
    Executor for mining operations

    Handles initialization, route validation, checkpoint/resume, and execution loop.
    """

    def __init__(self, args, logger):
        """
        Initialize executor

        Args:
            args: Parsed command-line arguments
            logger: Python logger
        """
        self.args = args
        self.logger = logger

        # Will be initialized in setup()
        self.api = None
        self.ship = None
        self.navigator = None
        self.controller = None
        self.captain_logger = None
        self.operator_name = None
        self.operation_start = None
        self.stats = MiningStats()

    def setup(self) -> bool:
        """
        Initialize resources

        Returns:
            True if setup successful, False otherwise
        """
        # Initialize API and ship controller
        self.api = get_api_client(self.args.player_id)
        self.ship = ShipController(self.api, self.args.ship)

        self.operation_start = datetime.now(timezone.utc)
        self.captain_logger = get_captain_logger(self.args.player_id)
        self.operator_name = get_operator_name(self.args)

        # Get ship data and system
        ship_data = self.ship.get_status()
        if not ship_data:
            print("❌ Failed to get ship status")
            self._log_error(
                "Ship status unavailable",
                "API returned no ship data",
                resolution="Verify ship symbol and connection"
            )
            return False

        system = ship_data['nav']['systemSymbol']

        # Initialize navigator
        print(f"📡 Initializing navigator for {system}...")
        self.navigator = SmartNavigator(self.api, system)

        # Initialize operation controller for checkpoint/resume
        op_id = f"mine_{self.args.ship}_{self.args.cycles}"
        self.controller = OperationController(op_id)

        # Pre-flight route validation
        if not self._validate_routes(ship_data):
            return False

        # Check for existing operation to resume
        if not self._setup_checkpoint():
            return False

        return True

    def _validate_routes(self, ship_data: dict) -> bool:
        """
        Validate routes to asteroid and market

        Args:
            ship_data: Ship status dict

        Returns:
            True if routes valid, False otherwise
        """
        print("\n🔍 Validating routes...")

        # Validate asteroid route
        valid, reason = self.navigator.validate_route(ship_data, self.args.asteroid)
        if not valid:
            print(f"❌ Route to asteroid not feasible: {reason}")
            self._log_error(
                "Route validation failed",
                reason,
                impact={'Asteroid': self.args.asteroid},
                resolution="Adjust ship fuel or select nearer asteroid"
            )
            return False
        print(f"  ✅ Asteroid route: {reason}")

        # Simulate fuel state after mining
        ship_data_after_mining = ship_data.copy()
        asteroid_fuel = self.navigator.get_fuel_estimate(ship_data, self.args.asteroid)
        if asteroid_fuel:
            ship_data_after_mining['fuel']['current'] = ship_data['fuel']['capacity'] - asteroid_fuel['total_fuel_cost']

        # Validate market route
        market_fuel = self.navigator.get_fuel_estimate(ship_data_after_mining, self.args.market)
        if not market_fuel:
            print(f"❌ Cannot route to market from asteroid (insufficient fuel)")
            self._log_error(
                "Market route infeasible",
                "Navigator could not find fuel-safe path",
                impact={'Asteroid': self.args.asteroid, 'Market': self.args.market},
                resolution="Insert refuel stop or pick closer market",
                escalate=True
            )
            return False
        print(f"  ✅ Market route: {market_fuel['refuel_stops']} refuel stop(s)")

        return True

    def _setup_checkpoint(self) -> bool:
        """
        Setup checkpoint/resume

        Returns:
            True if setup successful
        """
        if self.controller.can_resume():
            checkpoint = self.controller.resume()
            start_cycle, self.stats = stats_from_checkpoint(checkpoint, self.stats)
            print(f"\n♻️  Resuming from cycle {start_cycle}/{self.args.cycles}")
            print(f"   Previous progress: {self.stats.cycles_completed} cycles, {self.stats.total_revenue:,} credits")
        else:
            ship_data = self.ship.get_status()
            system = ship_data['nav']['systemSymbol'] if ship_data else 'UNKNOWN'
            self.controller.start({
                "ship": self.args.ship,
                "asteroid": self.args.asteroid,
                "market": self.args.market,
                "cycles": self.args.cycles,
                "system": system
            })

        return True

    def run_mining_loop(self) -> bool:
        """
        Run main mining loop

        Returns:
            True if successful, False otherwise
        """
        # Create mining context
        context = MiningContext(
            args=self.args,
            ship=self.ship,
            navigator=self.navigator,
            controller=self.controller,
            stats=self.stats,
            log_error=self._log_error_method,
        )

        # Create cycle runner
        cycle_runner = MiningCycle(
            context=context,
            total_cycles=self.args.cycles,
            asteroid=self.args.asteroid,
            market=self.args.market,
        )

        # Determine start cycle
        start_cycle = self.stats.cycles_completed + 1

        # Mining loop
        for cycle in range(start_cycle, self.args.cycles + 1):
            # Check for control commands
            if self.controller.should_cancel():
                print("\n⚠️  Operation cancelled by external command")
                self.controller.cancel()
                self._log_error(
                    "Mining operation cancelled",
                    "External cancel command received",
                    resolution="Restart operation when ready",
                    escalate=False
                )
                return False

            if self.controller.should_pause():
                print("\n⏸️  Operation paused by external command")
                self.controller.pause()
                return True

            if not cycle_runner.execute(cycle):
                return False

        return True

    def finalize(self):
        """Print final summary and cleanup"""
        # Final summary
        print(f"\n{SEPARATOR}")
        print("MINING OPERATION COMPLETE")
        print(SEPARATOR)
        print(f"Cycles completed: {self.stats.cycles_completed}")
        print(f"Total revenue: {self.stats.total_revenue:,} credits")
        print(f"Total extracted: {self.stats.total_extracted} units")
        print(f"Total sold: {self.stats.total_sold} units")
        print(
            f"Average per cycle: {self.stats.total_revenue // self.stats.cycles_completed:,} credits"
            if self.stats.cycles_completed > 0
            else "N/A"
        )
        print('='*70)

        # Mark operation as complete and cleanup
        self.controller.complete({
            "cycles": self.stats.cycles_completed,
            "revenue": self.stats.total_revenue
        })
        self.controller.cleanup()

        self._log_completion()
        self._log_performance()

    def run(self) -> int:
        """
        Main execution entry point

        Returns:
            0 on success, 1 on failure
        """
        # Setup
        if not self.setup():
            return 1

        # Run mining loop
        if not self.run_mining_loop():
            return 1

        # Finalize
        self.finalize()

        return 0

    def _log_error_method(self, error: str, cause: str, *, impact: Optional[dict] = None,
                  resolution: str = "Manual follow-up", lesson: str = "Review mining plan",
                  escalate: bool = False, tags: Optional[List[str]] = None) -> None:
        """Log error to captain's log (method form for context)"""
        self._log_error(error, cause, impact=impact, resolution=resolution,
                       lesson=lesson, escalate=escalate, tags=tags)

    def _log_error(self, error: str, cause: str, *, impact: Optional[dict] = None,
                  resolution: str = "Manual follow-up", lesson: str = "Review mining plan",
                  escalate: bool = False, tags: Optional[List[str]] = None) -> None:
        """Log critical error to captain's log"""
        log_captain_event(
            self.captain_logger,
            'CRITICAL_ERROR',
            operator=self.operator_name,
            ship=self.args.ship,
            error=error,
            cause=cause,
            impact=impact or {},
            resolution=resolution,
            lesson=lesson,
            escalate=escalate,
            tags=tags or ['mining']
        )

    def _log_completion(self) -> None:
        """Log operation completion to captain's log"""
        duration = humanize_duration(datetime.now(timezone.utc) - self.operation_start)
        log_captain_event(
            self.captain_logger,
            'OPERATION_COMPLETED',
            operator=self.operator_name,
            ship=self.args.ship,
            duration=duration,
            results={
                'Cycles Completed': self.stats.cycles_completed,
                'Total Revenue': f"{self.stats.total_revenue:,} cr",
                'Units Extracted': self.stats.total_extracted,
                'Units Sold': self.stats.total_sold,
            },
            notes=f"Mined at {self.args.asteroid} and sold at {self.args.market}.",
            tags=['mining', self.args.asteroid.lower(), self.args.market.lower()]
        )

    def _log_performance(self) -> None:
        """Log performance summary to captain's log"""
        revenue = self.stats.total_revenue
        elapsed = datetime.now(timezone.utc) - self.operation_start
        hours = max(elapsed.total_seconds() / 3600, 0.0001)
        rate = int(revenue / hours) if revenue else 0
        log_captain_event(
            self.captain_logger,
            'PERFORMANCE_SUMMARY',
            summary_type='Mining Operation',
            financials={
                'revenue': revenue,
                'cumulative': revenue,
                'rate': rate,
            },
            operations={
                'completed': self.stats.cycles_completed,
                'active': max(self.args.cycles - self.stats.cycles_completed, 0),
                'success_rate': 100 if self.stats.cycles_completed == self.args.cycles else 0,
            },
            fleet={'active': 1, 'total': 1},
            top_performers=[{
                'ship': self.args.ship,
                'profit': revenue,
                'operation': 'mining'
            }],
            tags=['mining', 'performance']
        )
