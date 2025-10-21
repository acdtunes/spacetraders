"""
BDD Step Definitions for Dependency Analyzer - Route segment dependencies

All step definitions are now in conftest.py (shared across all _trading_module features).
This file only loads the feature scenarios.
"""

from pytest_bdd import scenarios


# Load feature file
scenarios('../../../../bdd/features/trading/_trading_module/dependency_analyzer.feature')
