"""
Mining Cycle - Single cycle execution with extraction and sales

Handles:
- Navigation to asteroid and market
- Extraction until cargo full
- Cargo sales and refueling
- Cycle checkpointing
"""

from dataclasses import asdict, dataclass
from typing import Callable, Dict, Optional

from spacetraders_bot.core.operation_controller import OperationController
from spacetraders_bot.core.ship_controller import ShipController
from spacetraders_bot.core.smart_navigator import SmartNavigator


SEPARATOR = "=" * 70


@dataclass
class MiningStats:
    """Mining operation statistics"""
    cycles_completed: int = 0
    total_extracted: int = 0
    total_sold: int = 0
    total_revenue: int = 0


@dataclass
class MiningContext:
    """Context for mining operations"""
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
        """
        Execute a complete mining cycle

        Args:
            cycle: Cycle number

        Returns:
            True if successful, False otherwise
        """
        self._print_cycle_header(cycle)

        if not self._navigate_to_asteroid(cycle):
            return False

        self.context.ship.orbit()
        cargo = mine_until_cargo_full(self.context)

        if not self._navigate_to_market(cycle):
            return False

        revenue = sell_cargo(self.context, cargo)
        self._refuel_ship()

        self.context.stats.cycles_completed += 1
        checkpoint_cycle(self.context, cycle, self.market)

        self._print_cycle_summary(cycle, revenue, cargo)
        return True

    def _navigate_to_asteroid(self, cycle: int) -> bool:
        """Navigate to asteroid with retry logic"""
        return navigate_with_retries(
            self.context,
            self.asteroid,
            cycle,
            f"\n1. Navigating to asteroid {self.asteroid}...",
            "Check fuel levels and route",
        )

    def _navigate_to_market(self, cycle: int) -> bool:
        """Navigate to market with retry logic"""
        return navigate_with_retries(
            self.context,
            self.market,
            cycle,
            f"\n3. Navigating to market {self.market}...",
            "Verify route and refuel options",
        )

    def _refuel_ship(self) -> None:
        """Refuel ship at market"""
        print("\n5. Refueling...")
        self.context.ship.refuel()

    def _print_cycle_header(self, cycle: int) -> None:
        """Print cycle start header"""
        print(f"\n{SEPARATOR}")
        print(f"CYCLE {cycle}/{self.total_cycles}")
        print(SEPARATOR)

    def _print_cycle_summary(self, cycle: int, revenue: int, cargo: Optional[Dict]) -> None:
        """Print cycle completion summary"""
        cargo_units = cargo.get('units', 0) if cargo else 0
        print(f"\n{SEPARATOR}")
        print(f"CYCLE {cycle} COMPLETE")
        print(f"Revenue this cycle: {revenue:,} credits")
        print(f"Cargo sold: {cargo_units} units")
        print(f"Total revenue: {self.context.stats.total_revenue:,} credits")
        print(f"Total extracted: {self.context.stats.total_extracted} units")
        print(SEPARATOR)


def stats_from_checkpoint(checkpoint: dict, default: MiningStats) -> tuple:
    """
    Extract stats from checkpoint data

    Args:
        checkpoint: Checkpoint data dict
        default: Default stats if checkpoint missing data

    Returns:
        Tuple of (start_cycle, stats)
    """
    start_cycle = checkpoint.get('cycle', 0) + 1
    stats_payload = checkpoint.get('stats') or asdict(default)
    stats = MiningStats(**stats_payload)
    return start_cycle, stats


def navigate_with_retries(
    context: MiningContext,
    destination: str,
    cycle: int,
    description: str,
    resolution: str,
) -> bool:
    """
    Navigate to destination with automatic retries

    Args:
        context: Mining context with ship and navigator
        destination: Target waypoint
        cycle: Current cycle number
        description: Description to print
        resolution: Resolution advice for failures

    Returns:
        True if navigation successful, False otherwise
    """
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


def mine_until_cargo_full(context: MiningContext) -> Dict:
    """
    Mine until cargo is full

    Args:
        context: Mining context with ship

    Returns:
        Cargo dict with units
    """
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


def sell_cargo(context: MiningContext, cargo: Dict) -> int:
    """
    Sell all cargo at market

    Args:
        context: Mining context with ship
        cargo: Cargo dict

    Returns:
        Revenue from sales in credits
    """
    print(f"\n4. Selling cargo...")
    context.ship.dock()
    revenue = context.ship.sell_all()
    context.stats.total_revenue += revenue
    context.stats.total_sold += cargo.get('units', 0)
    return revenue


def checkpoint_cycle(context: MiningContext, cycle: int, location: str) -> None:
    """
    Checkpoint current cycle state

    Args:
        context: Mining context with controller
        cycle: Current cycle number
        location: Current location
    """
    if not context.controller:
        return

    context.controller.checkpoint({
        'cycle': cycle,
        'stats': asdict(context.stats),
        'location': location,
    })
