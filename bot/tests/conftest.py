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
    from configuration.container import reset_container, get_database

    # Reload modules to clear any patches from previous tests
    import configuration.container
    import adapters.secondary.api.client
    importlib.reload(adapters.secondary.api.client)
    importlib.reload(configuration.container)

    # Save original database path and environment variable
    original_db_path = settings.db_path
    original_env_db_path = os.environ.get("SPACETRADERS_DB_PATH")

    # CRITICAL FIX: Set environment variable so Database class uses in-memory database
    # Database class reads os.environ directly, not settings object
    os.environ["SPACETRADERS_DB_PATH"] = ":memory:"

    # Also update settings object for consistency
    settings.db_path = ":memory:"

    # Reset container to ensure it uses the test database
    reset_container()

    # Get fresh database instance and clear all tables
    db = get_database()
    with db.transaction() as conn:
        cursor = conn.cursor()
        # Clear all tables to ensure clean state between tests
        # Order matters due to foreign key constraints - delete children first
        cursor.execute("DELETE FROM ship_assignments")
        cursor.execute("DELETE FROM container_logs")
        cursor.execute("DELETE FROM containers")
        cursor.execute("DELETE FROM captain_logs")
        # Ships table removed - ship data is now fetched directly from API
        cursor.execute("DELETE FROM system_graphs")
        cursor.execute("DELETE FROM players")

    yield

    # Clean up tables BEFORE resetting container
    # This ensures we clean the same database instance the test used
    db = get_database()
    with db.transaction() as conn:
        cursor = conn.cursor()
        cursor.execute("DELETE FROM ship_assignments")
        cursor.execute("DELETE FROM container_logs")
        cursor.execute("DELETE FROM containers")
        cursor.execute("DELETE FROM captain_logs")
        # Ships table removed - ship data is now fetched directly from API
        cursor.execute("DELETE FROM system_graphs")
        cursor.execute("DELETE FROM players")

    # Restore original settings and environment variable
    settings.db_path = original_db_path
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
