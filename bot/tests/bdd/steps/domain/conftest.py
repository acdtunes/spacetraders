"""
Fixtures for domain BDD tests.

Provides test waypoints and context for route entity testing.
"""
import pytest
from domain.shared.value_objects import Waypoint


@pytest.fixture
def waypoints():
    """
    Provide test waypoints dictionary.

    Returns a dictionary of waypoint symbols to Waypoint objects.
    """
    return {
        "X1-A1": Waypoint(symbol="X1-A1", x=0.0, y=0.0, system_symbol="X1"),
        "X1-B2": Waypoint(symbol="X1-B2", x=100.0, y=0.0, system_symbol="X1"),
        "X1-C3": Waypoint(symbol="X1-C3", x=200.0, y=0.0, system_symbol="X1"),
    }


@pytest.fixture
def context():
    """Provide shared context dictionary for test data"""
    return {}
