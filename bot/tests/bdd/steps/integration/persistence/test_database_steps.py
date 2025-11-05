"""BDD step definitions for database integration tests"""
from pytest_bdd import scenario, given, when, then, parsers
import pytest
import sqlite3
import tempfile
import shutil
from pathlib import Path

from adapters.secondary.persistence.database import Database


# ==============================================================================
# Fixtures
# ==============================================================================
@pytest.fixture
def context():
    """Shared test context"""
    return {}


@pytest.fixture
def temp_db_path():
    """Create temporary database path"""
    with tempfile.TemporaryDirectory() as tmpdir:
        yield Path(tmpdir) / "test.db"


# ==============================================================================
# Scenario: Database initialization creates file and schema
# ==============================================================================
@scenario("../../../features/integration/persistence/database.feature",
          "Database initialization creates file and schema")
def test_database_initialization():
    pass


@when("I initialize a new database")
def initialize_database(context, temp_db_path):
    context["db_path"] = temp_db_path
    context["db"] = Database(temp_db_path)


@then("the database file should exist")
def check_file_exists(context):
    assert context["db_path"].exists()
    assert context["db_path"].is_file()


@then(parsers.parse('the "{table_name}" table should exist'))
def check_table_exists(context, table_name):
    with context["db"].connection() as conn:
        cursor = conn.cursor()
        cursor.execute("""
            SELECT name FROM sqlite_master
            WHERE type='table' AND name=?
        """, (table_name,))
        result = cursor.fetchone()
        assert result is not None


# ==============================================================================
# Scenario: Database creates parent directories
# ==============================================================================
@scenario("../../../features/integration/persistence/database.feature",
          "Database creates parent directories")
def test_database_creates_directories():
    pass


@when("I initialize a database in nested directories")
def initialize_nested_database(context):
    tmpdir = tempfile.mkdtemp()
    db_path = Path(tmpdir) / "nested" / "dir" / "test.db"
    context["db_path"] = db_path
    context["db"] = Database(db_path)
    context["tmpdir"] = tmpdir  # Keep reference


@then("the parent directories should exist")
def check_parent_dirs(context, request):
    assert context["db_path"].parent.exists()
    # Schedule cleanup after test completes
    def cleanup():
        if "tmpdir" in context and Path(context["tmpdir"]).exists():
            shutil.rmtree(context["tmpdir"], ignore_errors=True)
    request.addfinalizer(cleanup)


# ==============================================================================
# Scenario: Connection context manager works correctly
# ==============================================================================
@scenario("../../../features/integration/persistence/database.feature",
          "Connection context manager works correctly")
def test_connection_context_manager():
    pass


@given("an initialized database")
def initialized_database(context, temp_db_path):
    context["db"] = Database(temp_db_path)


@when("I open a connection")
def open_connection(context):
    context["conn_manager"] = context["db"].connection()
    context["conn"] = context["conn_manager"].__enter__()


@then("I should be able to execute queries")
def execute_query(context):
    cursor = context["conn"].cursor()
    cursor.execute("SELECT 1 as test_value")
    context["result"] = cursor.fetchone()


@then("the query should return results")
def check_results(context):
    assert context["result"][0] == 1
    context["conn_manager"].__exit__(None, None, None)


# ==============================================================================
# Scenario: Connection auto-closes after context exits
# ==============================================================================
@scenario("../../../features/integration/persistence/database.feature",
          "Connection auto-closes after context exits")
def test_connection_auto_closes():
    pass


@when("I open and close a connection")
def open_close_connection(context):
    with context["db"].connection() as conn:
        context["conn_ref"] = conn


@then("the connection should be closed")
def check_connection_closed(context):
    context["closed"] = True


@then("using the connection should raise an error")
def check_connection_error(context):
    with pytest.raises(sqlite3.ProgrammingError):
        context["conn_ref"].execute("SELECT 1")


# ==============================================================================
# Scenario: Transaction commits on success
# ==============================================================================
@scenario("../../../features/integration/persistence/database.feature",
          "Transaction commits on success")
def test_transaction_commits():
    pass


@when("I insert data in a transaction")
def insert_in_transaction(context):
    with context["db"].transaction() as conn:
        cursor = conn.cursor()
        cursor.execute("""
            INSERT INTO players (agent_symbol, token, created_at, last_active)
            VALUES (?, ?, ?, ?)
        """, ("TEST_AGENT", "token123", "2025-01-01T00:00:00", "2025-01-01T00:00:00"))


@then("the data should be persisted")
def check_data_persisted(context):
    with context["db"].connection() as conn:
        cursor = conn.cursor()
        cursor.execute("SELECT agent_symbol FROM players WHERE agent_symbol = ?",
                      ("TEST_AGENT",))
        context["result"] = cursor.fetchone()


@then("the data should be retrievable")
def check_data_retrievable(context):
    assert context["result"] is not None
    assert context["result"][0] == "TEST_AGENT"


# ==============================================================================
# Scenario: Transaction rolls back on error
# ==============================================================================
@scenario("../../../features/integration/persistence/database.feature",
          "Transaction rolls back on error")
def test_transaction_rollback():
    pass


@when("I insert data and raise an error in a transaction")
def insert_and_error(context):
    try:
        with context["db"].transaction() as conn:
            cursor = conn.cursor()
            cursor.execute("""
                INSERT INTO players (agent_symbol, token, created_at, last_active)
                VALUES (?, ?, ?, ?)
            """, ("TEST_AGENT", "token123", "2025-01-01T00:00:00", "2025-01-01T00:00:00"))
            raise ValueError("Simulated error")
    except ValueError:
        pass


@then("the data should not be persisted")
def check_no_data(context):
    with context["db"].connection() as conn:
        cursor = conn.cursor()
        cursor.execute("SELECT COUNT(*) FROM players WHERE agent_symbol = ?",
                      ("TEST_AGENT",))
        context["count"] = cursor.fetchone()[0]


@then("the table should be empty")
def check_table_empty(context):
    assert context["count"] == 0


# ==============================================================================
# Scenario: Transaction connection auto-closes
# ==============================================================================
@scenario("../../../features/integration/persistence/database.feature",
          "Transaction connection auto-closes")
def test_transaction_auto_closes():
    pass


@when("I open and close a transaction")
def open_close_transaction(context):
    with context["db"].transaction() as conn:
        context["conn_ref"] = conn


# ==============================================================================
# Scenario: WAL mode is enabled
# ==============================================================================
@scenario("../../../features/integration/persistence/database.feature",
          "WAL mode is enabled")
def test_wal_mode():
    pass


@when("I check the journal mode")
def check_journal_mode(context):
    with context["db"].connection() as conn:
        cursor = conn.cursor()
        cursor.execute("PRAGMA journal_mode")
        context["journal_mode"] = cursor.fetchone()[0]


@then(parsers.parse('the journal mode should be "{expected_mode}"'))
def verify_journal_mode(context, expected_mode):
    assert context["journal_mode"].upper() == expected_mode


# ==============================================================================
# Scenario: Foreign keys are enabled
# ==============================================================================
@scenario("../../../features/integration/persistence/database.feature",
          "Foreign keys are enabled")
def test_foreign_keys():
    pass


@when("I check foreign keys setting")
def check_foreign_keys(context):
    with context["db"].connection() as conn:
        cursor = conn.cursor()
        cursor.execute("PRAGMA foreign_keys")
        context["foreign_keys"] = cursor.fetchone()[0]


@then("foreign keys should be enabled")
def verify_foreign_keys(context):
    assert context["foreign_keys"] == 1


# ==============================================================================
# Scenario: Row factory returns dict-like rows
# ==============================================================================
@scenario("../../../features/integration/persistence/database.feature",
          "Row factory returns dict-like rows")
def test_row_factory():
    pass


@when("I execute a query with named columns")
def execute_named_query(context):
    with context["db"].connection() as conn:
        cursor = conn.cursor()
        cursor.execute("SELECT 1 as test_col")
        context["row"] = cursor.fetchone()


@then("I should access results by column name")
def check_column_access(context):
    assert context["row"]["test_col"] == 1


# ==============================================================================
# Table Schema Scenarios
# ==============================================================================
@scenario("../../../features/integration/persistence/database.feature",
          "Players table has correct schema")
def test_players_schema():
    pass


@scenario("../../../features/integration/persistence/database.feature",
          "Ships table has correct schema")
def test_ships_schema():
    pass


@scenario("../../../features/integration/persistence/database.feature",
          "Routes table has correct schema")
def test_routes_schema():
    pass


@scenario("../../../features/integration/persistence/database.feature",
          "System graphs table has correct schema")
def test_system_graphs_schema():
    pass


@when(parsers.parse('I check the {table_name} table schema'))
def check_table_schema(context, table_name):
    with context["db"].connection() as conn:
        cursor = conn.cursor()
        cursor.execute(f"PRAGMA table_info({table_name})")
        columns = {row[1]: row[2] for row in cursor.fetchall()}
        context["columns"] = columns


@then(parsers.parse('it should have column "{column_name}"'))
def verify_column_exists(context, column_name):
    assert column_name in context["columns"]


# ==============================================================================
# Scenario: Player unique constraint prevents duplicates
# ==============================================================================
@scenario("../../../features/integration/persistence/database.feature",
          "Player unique constraint prevents duplicates")
def test_unique_constraint():
    pass


@when(parsers.parse('I insert a player with agent_symbol "{agent}"'))
def insert_player(context, agent):
    with context["db"].transaction() as conn:
        cursor = conn.cursor()
        cursor.execute("""
            INSERT INTO players (agent_symbol, token, created_at, last_active)
            VALUES (?, ?, ?, ?)
        """, (agent, "token123", "2025-01-01T00:00:00", "2025-01-01T00:00:00"))
    context["agent"] = agent


@when(parsers.parse('I attempt to insert another player with agent_symbol "{agent}"'))
def insert_duplicate_player(context, agent):
    context["error"] = None
    try:
        with context["db"].transaction() as conn:
            cursor = conn.cursor()
            cursor.execute("""
                INSERT INTO players (agent_symbol, token, created_at, last_active)
                VALUES (?, ?, ?, ?)
            """, (agent, "token456", "2025-01-01T00:00:00", "2025-01-01T00:00:00"))
    except sqlite3.IntegrityError as e:
        context["error"] = e


@then("the second insert should fail with IntegrityError")
def check_integrity_error(context):
    assert isinstance(context["error"], sqlite3.IntegrityError)


# ==============================================================================
# Scenario: Foreign key cascade delete
# ==============================================================================
@scenario("../../../features/integration/persistence/database.feature",
          "Foreign key cascade delete from players to ships")
def test_cascade_delete():
    pass


@when("I create a player and a ship")
def create_player_and_ship(context):
    with context["db"].transaction() as conn:
        cursor = conn.cursor()
        cursor.execute("""
            INSERT INTO players (agent_symbol, token, created_at, last_active)
            VALUES (?, ?, ?, ?)
        """, ("TEST_AGENT", "token123", "2025-01-01T00:00:00", "2025-01-01T00:00:00"))
        context["player_id"] = cursor.lastrowid

        cursor.execute("""
            INSERT INTO ships (
                ship_symbol, player_id, current_location_symbol,
                fuel_current, fuel_capacity, cargo_capacity,
                cargo_units, engine_speed, nav_status, system_symbol
            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        """, ("SHIP-1", context["player_id"], "X1-A1", 100, 200, 50, 0, 30, "IN_ORBIT", "X1"))


@when("I delete the player")
def delete_player(context):
    with context["db"].transaction() as conn:
        cursor = conn.cursor()
        cursor.execute("DELETE FROM players WHERE player_id = ?", (context["player_id"],))


@then("the ship should be cascade deleted")
def verify_cascade_delete(context):
    with context["db"].connection() as conn:
        cursor = conn.cursor()
        cursor.execute("SELECT COUNT(*) FROM ships WHERE player_id = ?",
                      (context["player_id"],))
        count = cursor.fetchone()[0]
        assert count == 0


# ==============================================================================
# Scenario: Multiple connections
# ==============================================================================
@scenario("../../../features/integration/persistence/database.feature",
          "Multiple connections can be opened simultaneously")
def test_multiple_connections():
    pass


@when("I open two connections simultaneously")
def open_two_connections(context):
    with context["db"].connection() as conn1:
        with context["db"].connection() as conn2:
            cursor1 = conn1.cursor()
            cursor2 = conn2.cursor()

            cursor1.execute("SELECT 1")
            cursor2.execute("SELECT 2")

            context["result1"] = cursor1.fetchone()[0]
            context["result2"] = cursor2.fetchone()[0]


@then("both connections should work independently")
def verify_both_connections(context):
    assert context["result1"] == 1
    assert context["result2"] == 2


# ==============================================================================
# Scenario: Indexes exist
# ==============================================================================
@scenario("../../../features/integration/persistence/database.feature",
          "Indexes are created for performance")
def test_indexes():
    pass


@when("I check database indexes")
def check_indexes(context):
    with context["db"].connection() as conn:
        cursor = conn.cursor()
        cursor.execute("""
            SELECT name FROM sqlite_master
            WHERE type='index' AND name NOT LIKE 'sqlite_%'
            ORDER BY name
        """)
        context["indexes"] = {row[0] for row in cursor.fetchall()}


@then(parsers.parse('index "{index_name}" should exist'))
def verify_index_exists(context, index_name):
    assert index_name in context["indexes"]
