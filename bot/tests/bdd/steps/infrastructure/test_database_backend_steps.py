"""BDD step definitions for database backend selection tests"""
import os
import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from datetime import datetime
from pathlib import Path
import sqlite3

# Import the Database class we'll be testing
from adapters.secondary.persistence.database import Database

# Load all scenarios from the feature file
scenarios('../../features/infrastructure/database_backend.feature')


@pytest.fixture
def context():
    """Shared context for test steps"""
    return {
        'database': None,
        'database_url': None,
        'db_path': None,
        'player_id': None,
        'original_env': None
    }


@given("no DATABASE_URL environment variable is set")
def no_database_url_set(context):
    """Ensure DATABASE_URL is not set"""
    context['original_env'] = os.environ.get('DATABASE_URL')
    if 'DATABASE_URL' in os.environ:
        del os.environ['DATABASE_URL']


@given(parsers.parse('DATABASE_URL is set to "{url}"'))
def database_url_set(context, url):
    """Set DATABASE_URL environment variable"""
    context['original_env'] = os.environ.get('DATABASE_URL')
    os.environ['DATABASE_URL'] = url
    context['database_url'] = url


@when("I create a database instance with no DATABASE_URL")
def create_database_no_url(context):
    """Create database instance without DATABASE_URL"""
    # Use in-memory SQLite for test isolation
    context['database'] = Database(db_path=":memory:")


@when("I create a database instance")
def create_database(context):
    """Create database instance (will use DATABASE_URL if set)"""
    if context['database_url']:
        # For PostgreSQL, use the URL from environment
        context['database'] = Database()
    else:
        # For SQLite, use in-memory
        context['database'] = Database(db_path=":memory:")


@when(parsers.parse('I create a database instance with file path "{path}"'))
def create_database_with_path(context, path):
    """Create database instance with specific file path"""
    db_path = Path(path)
    context['db_path'] = db_path
    # Clean up if exists
    if db_path.exists():
        db_path.unlink()
    context['database'] = Database(db_path=str(db_path))


@when("I insert a player record")
def insert_player_record(context):
    """Insert a test player record"""
    db = context['database']

    # Generate unique agent symbol to avoid conflicts
    import uuid
    agent_symbol = f'TEST_AGENT_{uuid.uuid4().hex[:8]}'

    with db.transaction() as conn:
        cursor = conn.cursor()

        # Check if we're using PostgreSQL or SQLite
        if hasattr(db, 'backend') and db.backend == 'postgresql':
            # PostgreSQL uses RETURNING clause
            cursor.execute("""
                INSERT INTO players (agent_symbol, token, created_at)
                VALUES (%s, %s, %s)
                RETURNING player_id
            """, (agent_symbol, 'test_token_123', datetime.now()))
            # PostgreSQL with RealDictCursor returns dict
            row = cursor.fetchone()
            context['player_id'] = row['player_id']
        else:
            # SQLite uses lastrowid
            cursor.execute("""
                INSERT INTO players (agent_symbol, token, created_at)
                VALUES (?, ?, ?)
            """, (agent_symbol, 'test_token_123', datetime.now()))
            context['player_id'] = cursor.lastrowid


@then("the database should use SQLite backend")
def check_sqlite_backend(context):
    """Verify database is using SQLite"""
    db = context['database']
    assert hasattr(db, 'backend'), "Database should have backend attribute"
    assert db.backend == 'sqlite', f"Expected SQLite backend, got {db.backend}"


@then("the database should use PostgreSQL backend")
def check_postgresql_backend(context):
    """Verify database is using PostgreSQL"""
    db = context['database']
    assert hasattr(db, 'backend'), "Database should have backend attribute"
    assert db.backend == 'postgresql', f"Expected PostgreSQL backend, got {db.backend}"


@then("the database should create tables successfully")
def check_tables_created(context):
    """Verify tables were created"""
    db = context['database']
    with db.connection() as conn:
        cursor = conn.cursor()

        if hasattr(db, 'backend') and db.backend == 'postgresql':
            # PostgreSQL query to check tables
            cursor.execute("""
                SELECT table_name FROM information_schema.tables
                WHERE table_schema = 'public'
            """)
            # PostgreSQL with RealDictCursor returns dicts
            tables = {row['table_name'] for row in cursor.fetchall()}
        else:
            # SQLite query to check tables
            cursor.execute("""
                SELECT name FROM sqlite_master WHERE type='table'
            """)
            tables = {row[0] for row in cursor.fetchall()}

        expected_tables = {
            'players', 'system_graphs', 'ship_assignments',
            'containers', 'container_logs', 'market_data', 'contracts',
            'waypoints', 'captain_logs'
        }

        assert expected_tables.issubset(tables), \
            f"Missing tables: {expected_tables - tables}"


@then("the player_id should auto-increment using SQLite syntax")
def check_sqlite_autoincrement(context):
    """Verify SQLite auto-increment worked"""
    assert context['player_id'] is not None, "Player ID should be set"
    assert isinstance(context['player_id'], int), "Player ID should be integer"
    assert context['player_id'] > 0, "Player ID should be positive"


@then("the player_id should auto-increment using PostgreSQL syntax")
def check_postgresql_autoincrement(context):
    """Verify PostgreSQL auto-increment worked"""
    assert context['player_id'] is not None, "Player ID should be set"
    assert isinstance(context['player_id'], int), "Player ID should be integer"
    assert context['player_id'] > 0, "Player ID should be positive"


@then("the database should have WAL mode enabled")
def check_wal_mode(context):
    """Verify SQLite WAL mode is enabled"""
    db = context['database']
    # Only check WAL for file-based SQLite databases
    if hasattr(db, 'backend') and db.backend == 'sqlite' and db.db_path != ":memory:":
        with db.connection() as conn:
            cursor = conn.cursor()
            cursor.execute("PRAGMA journal_mode")
            mode = cursor.fetchone()[0]
            assert mode.upper() == 'WAL', f"Expected WAL mode, got {mode}"


@then("the database should support concurrent connections")
def check_concurrent_connections(context):
    """Verify database supports multiple concurrent connections"""
    db = context['database']

    # Try to get multiple connections
    conn1 = db._get_connection()
    conn2 = db._get_connection()

    # Both connections should work
    cursor1 = conn1.cursor()
    cursor2 = conn2.cursor()

    if hasattr(db, 'backend') and db.backend == 'postgresql':
        cursor1.execute("SELECT 1 AS value")
        cursor2.execute("SELECT 2 AS value")

        # PostgreSQL with RealDictCursor returns dict
        assert cursor1.fetchone()['value'] == 1
        assert cursor2.fetchone()['value'] == 2

        # Clean up PostgreSQL connections
        conn1.close()
        conn2.close()
    else:
        # SQLite also supports concurrent reads
        cursor1.execute("SELECT 1")
        cursor2.execute("SELECT 2")

        assert cursor1.fetchone()[0] == 1
        assert cursor2.fetchone()[0] == 2


@pytest.fixture(autouse=True)
def cleanup(context):
    """Cleanup after each test"""
    yield

    # Close database if created
    if context.get('database'):
        try:
            if hasattr(context['database'], 'backend') and context['database'].backend == 'postgresql':
                # Drop all tables in PostgreSQL test database
                with context['database'].transaction() as conn:
                    cursor = conn.cursor()
                    cursor.execute("""
                        SELECT table_name FROM information_schema.tables
                        WHERE table_schema = 'public'
                    """)
                    tables = [row[0] for row in cursor.fetchall()]
                    for table in tables:
                        cursor.execute(f"DROP TABLE IF EXISTS {table} CASCADE")

            context['database'].close()
        except Exception:
            pass

    # Restore original DATABASE_URL
    if context.get('original_env') is not None:
        os.environ['DATABASE_URL'] = context['original_env']
    elif 'DATABASE_URL' in os.environ:
        del os.environ['DATABASE_URL']

    # Clean up test database file if created
    if context.get('db_path'):
        try:
            if context['db_path'].exists():
                context['db_path'].unlink()
        except Exception:
            pass
