"""
CLI integration test configuration.

CLI tests need file-based database (not :memory:) to share data between
the test process and subprocess CLI commands.
"""
import pytest
import os
import tempfile
from pathlib import Path


@pytest.fixture(scope="function", autouse=True)
def use_file_based_test_database():
    """
    Override root conftest to use file-based database for CLI tests.

    CLI tests spawn subprocess commands that need to see database writes
    from the test process. In-memory databases can't be shared between
    processes, so we use a temporary file-based database.
    """
    from configuration.container import reset_container, get_database
    from configuration.settings import settings

    # Create temporary database file
    temp_db = tempfile.NamedTemporaryFile(mode='w', suffix='.db', delete=False)
    temp_db_path = Path(temp_db.name)
    temp_db.close()

    # Save original values
    original_settings_path = settings.db_path
    original_env_db_path = os.environ.get("SPACETRADERS_DB_PATH")

    # Set environment variable to use temporary file database
    os.environ["SPACETRADERS_DB_PATH"] = str(temp_db_path)

    # Reset container to use the new database
    reset_container()

    # Initialize database and clear tables
    db = get_database()
    with db.transaction() as conn:
        cursor = conn.cursor()
        # Clear all tables in dependency order
        cursor.execute("DELETE FROM ship_assignments")
        cursor.execute("DELETE FROM container_logs")
        cursor.execute("DELETE FROM containers")
        cursor.execute("DELETE FROM captain_logs")
        cursor.execute("DELETE FROM contracts")
        cursor.execute("DELETE FROM market_data")
        cursor.execute("DELETE FROM waypoints")
        cursor.execute("DELETE FROM system_graphs")
        cursor.execute("DELETE FROM players")

    yield

    # Cleanup
    db = get_database()
    try:
        with db.transaction() as conn:
            cursor = conn.cursor()
            cursor.execute("DELETE FROM ship_assignments")
            cursor.execute("DELETE FROM container_logs")
            cursor.execute("DELETE FROM containers")
            cursor.execute("DELETE FROM captain_logs")
            cursor.execute("DELETE FROM contracts")
            cursor.execute("DELETE FROM market_data")
            cursor.execute("DELETE FROM waypoints")
            cursor.execute("DELETE FROM system_graphs")
            cursor.execute("DELETE FROM players")
    except Exception:
        pass

    # Reset container to clean up connections
    reset_container()

    # Restore original values
    settings.db_path = original_settings_path
    if original_env_db_path:
        os.environ["SPACETRADERS_DB_PATH"] = original_env_db_path
    else:
        os.environ.pop("SPACETRADERS_DB_PATH", None)

    # Delete temporary database file
    try:
        if temp_db_path.exists():
            temp_db_path.unlink()
    except Exception:
        pass
