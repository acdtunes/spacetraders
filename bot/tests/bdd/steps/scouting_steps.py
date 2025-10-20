"""
Step definitions for scouting domain BDD tests

These tests cover scout coordinator functionality including partitioning,
market exclusion, market dropping prevention, price mapping, and stationary scout balancing.
All scenarios are marked as @xfail for future implementation.
"""

import pytest
from pytest_bdd import scenarios, given, when, then, parsers

# Load all scouting feature files
scenarios('../../features/scouting/partitioning.feature')
scenarios('../../features/scouting/market_exclusion.feature')
scenarios('../../features/scouting/market_dropping.feature')
scenarios('../../features/scouting/price_mapping.feature')
scenarios('../../features/scouting/stationary_scouts.feature')
scenarios('../../features/scouting/coordinator_bugs.feature')

@pytest.fixture
def scouting_context():
    """Context for scouting domain tests"""
    return {}

# Placeholder steps for @xfail scenarios - no implementation needed yet
# These will be implemented when scout coordinator refactoring work begins
