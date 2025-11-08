"""Step definitions for database placeholder conversion tests"""
import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from adapters.secondary.persistence.database import Database

# Load scenarios
scenarios('../../features/infrastructure/database_placeholder_conversion.feature')


@pytest.fixture
def context():
    """Shared context for test steps"""
    return {}


@given("a PostgreSQL database backend")
def postgresql_backend(context, monkeypatch):
    """Set up a PostgreSQL database backend for testing"""
    # Mock the environment to simulate PostgreSQL
    monkeypatch.setenv("DATABASE_URL", "postgresql://localhost/test")
    context['backend'] = 'postgresql'


@given("a SQLite database backend")
def sqlite_backend(context, monkeypatch):
    """Set up a SQLite database backend for testing"""
    # Clear any DATABASE_URL to ensure SQLite mode
    monkeypatch.delenv("DATABASE_URL", raising=False)
    context['backend'] = 'sqlite'


@when(parsers.parse('I convert the SQL "{sql}"'))
def convert_sql(context, sql):
    """Convert SQL placeholders using the database's conversion method"""
    # Create a Database instance (will detect backend from environment)
    # We need to mock the initialization to avoid actual DB connection
    db = Database.__new__(Database)
    db.backend = context['backend']

    # Call the placeholder conversion method
    context['converted_sql'] = db._convert_placeholders(sql)


@then(parsers.parse('the converted SQL should be "{expected_sql}"'))
def verify_converted_sql(context, expected_sql):
    """Verify the converted SQL matches expected output"""
    assert context['converted_sql'] == expected_sql, \
        f"Expected: {expected_sql}\nActual: {context['converted_sql']}"
