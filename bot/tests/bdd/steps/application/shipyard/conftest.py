"""Shared fixtures for shipyard tests"""
import pytest

from configuration.container import get_engine, reset_container
from adapters.secondary.persistence.models import metadata


@pytest.fixture(autouse=True)
def setup_test_environment():
    """Initialize database schema for shipyard tests"""
    # Reset container to ensure clean state
    reset_container()

    # Initialize SQLAlchemy schema for in-memory database
    engine = get_engine()
    metadata.create_all(engine)

    yield

    # Cleanup after test
    reset_container()
