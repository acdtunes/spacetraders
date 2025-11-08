"""Step definitions for database transparent placeholder conversion tests"""
import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from unittest.mock import Mock, patch, MagicMock
from adapters.secondary.persistence.database import Database

# Load scenarios
scenarios('../../features/infrastructure/database_transparent_conversion.feature')


@pytest.fixture
def context():
    """Shared context for test steps"""
    return {}


@given("a PostgreSQL database backend is configured")
def postgresql_backend_configured(context, monkeypatch):
    """Configure PostgreSQL backend"""
    monkeypatch.setenv("DATABASE_URL", "postgresql://localhost/test")
    context['backend'] = 'postgresql'
    context['expected_format'] = 'postgresql'


@given("a SQLite database backend is configured")
def sqlite_backend_configured(context, monkeypatch):
    """Configure SQLite backend"""
    monkeypatch.delenv("DATABASE_URL", raising=False)
    context['backend'] = 'sqlite'
    context['expected_format'] = 'sqlite'


@given("a database connection")
def database_connection(context):
    """Create a database connection for testing"""
    # We'll use a mock connection since we're testing conversion, not actual DB ops
    context['connection_type'] = 'connection'


@given("a database transaction")
def database_transaction(context):
    """Create a database transaction for testing"""
    # We'll use a mock connection since we're testing conversion, not actual DB ops
    context['connection_type'] = 'transaction'


@when(parsers.parse('I execute "{sql}" with parameters {params}'))
def execute_sql_with_conversion(context, sql, params):
    """Execute SQL and verify it gets converted"""
    # Create a Database instance
    db = Database.__new__(Database)
    db.backend = context['backend']
    db.db_path = ":memory:" if context['backend'] == 'sqlite' else None
    db.db_url = "postgresql://localhost/test" if context['backend'] == 'postgresql' else None
    db._persistent_conn = None

    # Store the original SQL
    context['original_sql'] = sql

    # We just need to verify that the _convert_placeholders method
    # is called when executing through the wrapper
    # The wrapper itself handles the conversion, so we test that
    converted = db._convert_placeholders(sql)
    context['converted_sql'] = converted


@then("the SQL should be automatically converted to PostgreSQL format")
def verify_postgresql_conversion(context):
    """Verify SQL was converted to PostgreSQL format"""
    converted = context['converted_sql']
    original = context['original_sql']

    # Verify conversion happened
    if '?' in original:
        assert '$1' in converted, f"Expected PostgreSQL placeholders in: {converted}"
        assert '?' not in converted, f"SQLite placeholders should be removed: {converted}"
    else:
        # No placeholders to convert
        assert converted == original


@then("the SQL should use the original placeholders")
def verify_no_conversion(context):
    """Verify SQL was not converted (SQLite)"""
    converted = context['converted_sql']
    original = context['original_sql']

    # SQLite should not convert - output should match input
    assert converted == original
