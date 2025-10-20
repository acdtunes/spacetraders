"""
Targeted Mining - Mine specific resources with circuit breaker protection

Handles:
- Targeting specific resource types
- Circuit breaker for consecutive failures
- Cargo management with wrong material jettisoning
"""

from dataclasses import dataclass
from typing import Tuple

from spacetraders_bot.core.ship_controller import ShipController
from spacetraders_bot.core.smart_navigator import SmartNavigator
from spacetraders_bot.operations.control import CircuitBreaker

from .mining_cycle import MiningContext, MiningStats, navigate_with_retries


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
        """
        Execute targeted mining session

        Returns:
            Tuple of (success, units_collected, reason)
        """
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
        """Wait for cooldown if active"""
        ship_data = self.context.ship.get_status()
        cooldown = 0
        if ship_data:
            cooldown = ship_data.get('cooldown', {}).get('remainingSeconds', 0)
        if cooldown > 0:
            self.context.ship.wait_for_cooldown(cooldown)

    def _process_extraction(self) -> None:
        """Process a single extraction attempt"""
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
        """Manage cargo when nearly full"""
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
        """Handle circuit breaker trip"""
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
        """Handle successful completion"""
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

    if not navigate_with_retries(
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
