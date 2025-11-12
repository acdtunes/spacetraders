import pytest
import os
from pathlib import Path


@pytest.fixture(autouse=True)
def use_test_database():
    """
    Override settings to use in-memory database for all tests.
    Each test gets a fresh, isolated database.
    """
    import importlib
    from configuration.settings import settings
    from configuration.container import reset_container, get_database, get_engine
    from adapters.secondary.persistence.models import metadata

    # Reload modules to clear any patches from previous tests
    import configuration.container
    import adapters.secondary.api.client
    importlib.reload(adapters.secondary.api.client)
    importlib.reload(configuration.container)

    # Save original database path and environment variables
    original_db_path = settings.db_path
    original_env_db_path = os.environ.get("SPACETRADERS_DB_PATH")
    original_database_url = os.environ.get("DATABASE_URL")

    # CRITICAL FIX: Unset DATABASE_URL to prevent tests from using PostgreSQL
    # Database class prioritizes DATABASE_URL over SPACETRADERS_DB_PATH
    if "DATABASE_URL" in os.environ:
        del os.environ["DATABASE_URL"]

    # CRITICAL FIX: Set environment variable so Database class uses in-memory database
    # Database class reads os.environ directly, not settings object
    os.environ["SPACETRADERS_DB_PATH"] = ":memory:"

    # Also update settings object for consistency
    settings.db_path = ":memory:"

    # Reset container to ensure it uses the test database
    reset_container()

    # Initialize SQLAlchemy schema for in-memory database
    engine = get_engine()
    metadata.create_all(engine)

    # Get fresh database instance (old Database class - still needed for some repos)
    db = get_database()

    yield

    # No cleanup needed - in-memory database will be disposed on engine.dispose()

    # Restore original settings and environment variables
    settings.db_path = original_db_path

    # Restore DATABASE_URL
    if original_database_url is None:
        os.environ.pop("DATABASE_URL", None)
    else:
        os.environ["DATABASE_URL"] = original_database_url

    # Restore SPACETRADERS_DB_PATH
    if original_env_db_path is None:
        # Remove the environment variable if it wasn't set originally
        os.environ.pop("SPACETRADERS_DB_PATH", None)
    else:
        os.environ["SPACETRADERS_DB_PATH"] = original_env_db_path

    # Reset container again to clean up test state (this closes the database connection)
    reset_container()


@pytest.fixture
def context():
    """Shared context for BDD steps"""
    return {}


@pytest.fixture
def mediator():
    """Get mediator instance for testing"""
    from configuration.container import get_mediator
    return get_mediator()
