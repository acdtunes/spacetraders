"""Fixtures for infrastructure BDD tests"""
import pytest
from unittest.mock import Mock, MagicMock


@pytest.fixture
def context():
    """Shared context dictionary for BDD scenarios"""
    return {}


@pytest.fixture
def mock_session():
    """Mock requests.Session for API client testing"""
    session = Mock()
    # Make headers a Mock object so we can track update calls
    session.headers = Mock()
    session.headers.update = Mock()
    session.request = Mock()
    return session


@pytest.fixture
def mock_rate_limiter():
    """Mock RateLimiter for testing"""
    limiter = Mock()
    limiter.acquire = Mock()
    return limiter
