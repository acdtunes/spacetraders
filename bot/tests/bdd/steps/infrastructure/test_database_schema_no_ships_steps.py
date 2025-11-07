"""Step definitions for database schema verification (no ships table)"""
from pytest_bdd import scenarios, given, when, then

from adapters.secondary.persistence.database import Database

scenarios('../../features/infrastructure/database_schema_no_ships.feature')


@given('a fresh database instance')
def fresh_database(context):
    """Create a fresh in-memory database"""
    context['database'] = Database(":memory:")
    return context


@when('I inspect the database schema')
def inspect_schema(context):
    """Get list of tables in the database"""
    with context['database'].connection() as conn:
        cursor = conn.cursor()
        cursor.execute("""
            SELECT name FROM sqlite_master
            WHERE type='table'
            ORDER BY name
        """)
        context['tables'] = [row[0] for row in cursor.fetchall()]


@then('the ships table should not exist')
def ships_table_should_not_exist(context):
    """Verify ships table does not exist"""
    assert 'ships' not in context['tables'], \
        f"Database should not have 'ships' table, but it exists. Tables: {context['tables']}"


@then('the ship_assignments table should exist')
def ship_assignments_table_should_exist(context):
    """Verify ship_assignments table exists"""
    assert 'ship_assignments' in context['tables'], \
        f"Database should have 'ship_assignments' table. Tables: {context['tables']}"


@then('the routes table should exist')
def routes_table_should_exist(context):
    """Verify routes table exists"""
    assert 'routes' in context['tables'], \
        f"Database should have 'routes' table. Tables: {context['tables']}"
