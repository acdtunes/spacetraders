#!/usr/bin/env python3
from __future__ import annotations

"""
Mining operations: autonomous resource extraction with intelligent routing
"""

import logging
from typing import Tuple

from spacetraders_bot.core.api_client import APIClient
from spacetraders_bot.core.ship import ShipController
from spacetraders_bot.core.smart_navigator import SmartNavigator
from spacetraders_bot.operations.common import setup_logging


# All classes and helpers have been moved to operations/_mining/ module
# This file now contains thin wrappers that delegate to the module


def mining_operation(args):
    """
    Autonomous mining operation with intelligent routing and checkpoint/resume

    REFACTORED: Now delegates to MiningOperationExecutor for better testability.

    Features:
    - Smart navigation with fuel optimization
    - Automatic refuel stop insertion
    - Checkpoint/resume on crash
    - Route validation before execution
    """
    # Setup logging first (agent-scoped by player_id)
    log_file = setup_logging("mining", args.ship, getattr(args, 'log_level', 'INFO'), player_id=args.player_id)
    logger = logging.getLogger(__name__ + '.mining')

    logger.info("Initializing mining operation")
    logger.info(f"Player ID: {args.player_id}")
    logger.info(f"Ship: {args.ship}")
    logger.info(f"Asteroid: {args.asteroid}")
    logger.info(f"Market: {args.market}")
    logger.info(f"Cycles: {args.cycles}")

    # Delegate to executor (new SOLID architecture)
    from ._mining import MiningOperationExecutor
    executor = MiningOperationExecutor(args, logger)
    return executor.run()



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

    REFACTORED: Delegates to mining module.

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
    from ._mining import targeted_mining_with_circuit_breaker as _targeted_mining
    return _targeted_mining(
        ship, navigator, asteroid, target_resource, units_needed, max_consecutive_failures
    )

def find_alternative_asteroids(
    api: APIClient,
    system: str,
    current_asteroid: str,
    target_resource: str
) -> list:
    """
    Find alternative asteroids in the system that may contain the target resource

    REFACTORED: Delegates to mining module.

    Args:
        api: API client instance
        system: System symbol (e.g., "X1-HU87")
        current_asteroid: Current asteroid that's not working
        target_resource: Resource we're looking for (e.g., "ALUMINUM_ORE")

    Returns:
        List of alternative asteroid waypoint symbols
    """
    from ._mining import find_alternative_asteroids as _find_asteroids
    return _find_asteroids(api, system, current_asteroid, target_resource)
