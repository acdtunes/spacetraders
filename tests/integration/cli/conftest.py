"""
Shared fixtures for CLI integration tests.

These fixtures support TRUE integration testing by:
1. Using real container with test database (via root conftest.py)
2. Providing real repositories from container
3. Only mocking external dependencies (API clients)
4. Avoiding mock verification anti-patterns
"""
import pytest
from unittest.mock import Mock, AsyncMock

from spacetraders.configuration.container import (
    get_player_repository,
    get_ship_repository
)


@pytest.fixture
def player_repo():
    """
    Get real PlayerRepository from container.

    Uses test database configured by root conftest.py.
    Each test gets a fresh, isolated database.
    """
    return get_player_repository()


@pytest.fixture
def ship_repo():
    """
    Get real ShipRepository from container.

    Uses test database configured by root conftest.py.
    Each test gets a fresh, isolated database.
    """
    return get_ship_repository()


@pytest.fixture
def mock_api_client():
    """
    Create mock SpaceTraders API client for external dependency.

    This is the ONLY thing we should mock - external API calls.
    Internal dependencies (repos, mediator, handlers) use real implementations.

    Usage:
        @patch('spacetraders.adapters.secondary.api.client.SpaceTradersAPIClient')
        def test_something(mock_api_class, mock_api_client):
            mock_api_class.return_value = mock_api_client
            mock_api_client.get_my_ships = AsyncMock(return_value=[...])
    """
    mock_client = Mock()
    # Add async methods as needed
    mock_client.get_my_ships = AsyncMock(return_value=[])
    mock_client.get_waypoint = AsyncMock(return_value={})
    return mock_client
