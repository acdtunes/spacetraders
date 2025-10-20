"""
Mining module - SOLID architecture for autonomous resource extraction

Refactored from monolithic mining_operation into testable components.
"""

from .executor import MiningOperationExecutor
from .mining_cycle import MiningCycle, MiningContext, MiningStats
from .targeted_mining import TargetedMiningSession, targeted_mining_with_circuit_breaker
from .asteroid_finder import find_alternative_asteroids

__all__ = [
    'MiningOperationExecutor',
    'MiningCycle',
    'MiningContext',
    'MiningStats',
    'TargetedMiningSession',
    'targeted_mining_with_circuit_breaker',
    'find_alternative_asteroids',
]
