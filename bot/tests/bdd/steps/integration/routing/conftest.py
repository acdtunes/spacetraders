"""Shared fixtures for routing integration tests."""
import pytest


@pytest.fixture
def datatable(request):
    """Parse BDD table data."""
    # This fixture is used by pytest-bdd to parse Gherkin tables
    # The table data is automatically injected by pytest-bdd
    return request.getfixturevalue('step_data')
