import pytest

# Database setup provided by root tests/conftest.py (autouse fixture)
# No need to duplicate here

@pytest.fixture
def context():
    """Provide shared context dictionary for test data"""
    return {}
