"""Step definitions for ConnectionWrapper psycopg2 support tests"""
import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from unittest.mock import Mock
from adapters.secondary.persistence.database import ConnectionWrapper, CursorWrapper

# Load scenarios
scenarios('../../features/infrastructure/connection_wrapper_psycopg2.feature')


@pytest.fixture
def context():
    """Shared context for test steps"""
    return {}


def _postgresql_converter(sql: str) -> str:
    """Convert SQLite ? placeholders to PostgreSQL $1, $2, etc."""
    result = []
    placeholder_count = 0
    for char in sql:
        if char == '?':
            placeholder_count += 1
            result.append(f'${placeholder_count}')
        else:
            result.append(char)
    return ''.join(result)


def _sqlite_converter(sql: str) -> str:
    """SQLite converter - no conversion needed"""
    return sql


@given("a ConnectionWrapper for PostgreSQL backend")
def postgresql_connection_wrapper(context):
    """Create a ConnectionWrapper for PostgreSQL"""
    # Create a mock connection that behaves like psycopg2
    # Use spec_set to control what attributes exist
    mock_cursor = Mock()

    # Setup cursor mock
    mock_cursor.execute = Mock(return_value=None)
    mock_cursor.fetchone = Mock(return_value=None)
    mock_cursor.fetchall = Mock(return_value=[])
    mock_cursor.rowcount = 0
    mock_cursor.lastrowid = None

    # Create connection mock WITHOUT execute method
    # psycopg2 connections only have: cursor, commit, rollback, close
    mock_conn = Mock(spec=['cursor', 'commit', 'rollback', 'close'])
    mock_conn.cursor = Mock(return_value=mock_cursor)
    mock_conn.commit = Mock()
    mock_conn.rollback = Mock()
    mock_conn.close = Mock()

    context['connection'] = mock_conn
    context['mock_cursor'] = mock_cursor
    context['wrapper'] = ConnectionWrapper(mock_conn, _postgresql_converter)
    context['backend'] = 'postgresql'


@given("a ConnectionWrapper for SQLite backend")
def sqlite_connection_wrapper(context):
    """Create a ConnectionWrapper for SQLite"""
    # Create a mock connection that behaves like SQLite
    mock_conn = Mock()
    mock_cursor = Mock()

    # Setup cursor mock
    mock_cursor.execute = Mock(return_value=None)
    mock_cursor.fetchone = Mock(return_value=None)
    mock_cursor.fetchall = Mock(return_value=[])
    mock_cursor.rowcount = 0
    mock_cursor.lastrowid = None

    # SQLite connections CAN execute directly
    mock_conn.execute = Mock(return_value=mock_cursor)
    mock_conn.cursor = Mock(return_value=mock_cursor)
    mock_conn.commit = Mock()
    mock_conn.rollback = Mock()
    mock_conn.close = Mock()

    context['connection'] = mock_conn
    context['mock_cursor'] = mock_cursor
    context['wrapper'] = ConnectionWrapper(mock_conn, _sqlite_converter)
    context['backend'] = 'sqlite'


@when(parsers.parse('I call execute with "{sql}"'))
def call_execute_on_wrapper(context, sql):
    """Call execute on the wrapper"""
    context['sql'] = sql
    try:
        context['result'] = context['wrapper'].execute(sql, (1,))
        context['error'] = None
    except Exception as e:
        context['error'] = e


@when("I get a cursor from the wrapper")
def get_cursor_from_wrapper(context):
    """Get a cursor from the wrapper"""
    context['cursor'] = context['wrapper'].cursor()


@when(parsers.parse('I execute "{sql}" on the cursor'))
def execute_on_cursor(context, sql):
    """Execute SQL on the cursor"""
    context['sql'] = sql
    context['cursor'].execute(sql, (1,))


@then("the wrapper should create a cursor and convert SQL to PostgreSQL format")
def verify_psycopg2_cursor_used(context):
    """Verify wrapper used cursor() and converted SQL"""
    # For psycopg2, the wrapper should have called cursor()
    mock_conn = context['connection']
    mock_cursor = context['mock_cursor']

    # Verify cursor() was called
    mock_conn.cursor.assert_called()

    # Verify execute was called on the cursor with converted SQL
    assert mock_cursor.execute.called, "Cursor execute should be called"

    call_args = mock_cursor.execute.call_args
    executed_sql = call_args[0][0]

    # SQL should be converted to PostgreSQL format
    assert '$1' in executed_sql, f"Expected PostgreSQL placeholder $1 in: {executed_sql}"
    assert '?' not in executed_sql, f"SQLite placeholders should be removed: {executed_sql}"


@then("the wrapper should execute directly with no conversion")
def verify_sqlite_execute_used(context):
    """Verify wrapper used direct execute"""
    # For SQLite, the wrapper can call execute directly on connection
    mock_conn = context['connection']

    # Verify execute was called on the connection
    mock_conn.execute.assert_called_once()

    call_args = mock_conn.execute.call_args
    executed_sql = call_args[0][0]

    # SQL should remain unchanged
    assert '?' in executed_sql, f"SQLite placeholders should remain: {executed_sql}"


@then("the cursor should convert SQL to PostgreSQL format")
def verify_cursor_converts_sql(context):
    """Verify cursor converted SQL to PostgreSQL format"""
    mock_cursor = context['mock_cursor']

    call_args = mock_cursor.execute.call_args
    executed_sql = call_args[0][0]

    # SQL should be converted
    assert '$1' in executed_sql, f"Expected PostgreSQL placeholder in: {executed_sql}"
    assert '?' not in executed_sql, f"SQLite placeholders should be removed: {executed_sql}"
