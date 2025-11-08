"""Fixtures for infrastructure BDD tests"""
import pytest
from unittest.mock import Mock, MagicMock


class Context:
    """Context object for sharing state between steps"""

    def __setitem__(self, key, value):
        """Support dict-style assignment: context['key'] = value"""
        setattr(self, key, value)

    def __getitem__(self, key):
        """Support dict-style access: value = context['key']"""
        return getattr(self, key)

    def __contains__(self, key):
        """Support 'in' operator: 'key' in context"""
        return hasattr(self, key)

    def get(self, key, default=None):
        """Support .get() method: context.get('key', default)"""
        return getattr(self, key, default)


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
