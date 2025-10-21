"""
BDD Step Definitions for Circuit Breaker - Profitability validation and batch sizing

All step definitions are now in conftest.py (shared across all _trading_module features).
This file only loads the feature scenarios.
"""

from pytest_bdd import scenarios


# Load feature file
scenarios('../../../../bdd/features/trading/_trading_module/circuit_breaker.feature')
