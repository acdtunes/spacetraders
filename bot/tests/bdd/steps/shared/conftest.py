import pytest

# Use real repositories from production code - no mocks!
# All repositories backed by in-memory SQLite (configured in root conftest.py)

@pytest.fixture
def player_repo():
    """Get real PlayerRepository (SQLAlchemy + in-memory SQLite)"""
    from configuration.container import get_player_repository
    return get_player_repository()

@pytest.fixture
def context():
    """Provide shared context dictionary for test data"""
    return {}
