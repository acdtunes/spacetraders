"""
BDD Step Definitions for Route Executor - Multi-segment route execution

All step definitions are now in conftest.py (shared across all _trading_module features).
This file only loads the feature scenarios.
"""

from pytest_bdd import scenarios


# Load feature file
scenarios('../../../../bdd/features/trading/_trading_module/route_executor.feature')
