"""
Step definitions for navigation domain BDD tests

These tests cover flight mode selection, checkpoint resume, and probe navigation.
All scenarios are marked as @xfail for future implementation.
"""

import pytest
from pytest_bdd import scenarios, given, when, then, parsers

# Load all navigation feature files
scenarios('../../features/navigation/flight_mode_selection.feature')
scenarios('../../features/navigation/checkpoint_resume.feature')
scenarios('../../features/navigation/probe_navigation.feature')

@pytest.fixture
def navigation_context():
    """Context for navigation domain tests"""
    return {}

# Placeholder steps for @xfail scenarios - no implementation needed yet
# These will be implemented when navigation system work begins
