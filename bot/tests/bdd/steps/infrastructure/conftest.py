"""Fixtures for infrastructure BDD tests"""
import pytest
from unittest.mock import Mock, MagicMock


class Context:
    """Context object for sharing state between steps"""
    def __init__(self):
        self._data = {}

    def __setitem__(self, key, value):
        self._data[key] = value

    def __getitem__(self, key):
        return self._data[key]

    def get(self, key, default=None):
        return self._data.get(key, default)


@pytest.fixture
def context():
    """Shared context object for BDD scenarios"""
    return Context()


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
