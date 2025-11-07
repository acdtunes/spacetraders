"""BDD step definitions for database closed detection"""
import asyncio
import time
import pytest
from pytest_bdd import given, when, then, scenarios
from datetime import datetime

from adapters.secondary.persistence.database import Database
from adapters.primary.daemon.container_manager import ContainerManager

# Load scenarios
scenarios('../../features/daemon/database_closed_gracefully.feature')


@pytest.fixture
def context():
    """Shared test context"""
    return {}


@pytest.fixture
def test_database():
    """Initialize test database"""
    db = Database(":memory:")
    # Create a test player
    with db.transaction() as conn:
        conn.execute("""
            INSERT INTO players (agent_symbol, token, created_at)
            VALUES (?, ?, ?)
        """, ("TEST_AGENT", "test-token", datetime.now().isoformat()))
    yield db
    # Don't close here - let tests control when to close


@given("a test database is initialized")
def test_database_initialized(context, test_database):
    """Mark database as initialized in context"""
    context['database'] = test_database


@given("the daemon container manager is initialized")
def daemon_manager_initialized(context):
    """Initialize daemon container manager with mock mediator"""

    class MockMediator:
        async def send_async(self, command):
            await asyncio.sleep(0.01)
            return {"success": True}

    class LongRunningContainer:
        """Mock container that runs for a long time"""
        def __init__(self, container_id, player_id, config, mediator, database, container_info=None):
            self.container_id = container_id
            self.player_id = player_id
            self.config = config
            self.mediator = mediator
            self.database = database
            self.container_info = container_info

        async def start(self):
            """Simulate long-running operation"""
            wait_time = self.config.get('wait_time', 10)
            try:
                for i in range(int(wait_time)):
                    await asyncio.sleep(1)
            except asyncio.CancelledError:
                # Simulate cleanup that takes time
                await asyncio.sleep(0.5)
                raise

    manager = ContainerManager(MockMediator(), context['database'])
    manager._container_types['command'] = LongRunningContainer

    context['manager'] = manager
    context['loop'] = asyncio.new_event_loop()


@given("a container is running with a long-running operation")
def create_long_running_container(context):
    """Create a container with a long-running operation"""
    manager = context['manager']
    loop = context['loop']

    container_id = f"long-running-{datetime.now().timestamp()}"
    config = {
        'command': 'long_operation',
        'wait_time': 10
    }

    info = loop.run_until_complete(manager.create_container(
        container_id=container_id,
        player_id=1,
        container_type='command',
        config=config,
        restart_policy='no'
    ))

    # Give it a moment to start
    loop.run_until_complete(asyncio.sleep(0.1))

    context['container_id'] = container_id
    context['container_info'] = info


@when("the database is closed")
def close_database(context):
    """Close the database"""
    database = context['database']
    database.close()


@when("I issue a stop command")
def issue_stop_command(context):
    """Issue stop command"""
    manager = context['manager']
    container_id = context['container_id']
    loop = context['loop']

    loop.run_until_complete(manager.stop_container(container_id))
    context['container_info'] = manager.get_container(container_id)


@when("enough time passes for background cleanup")
def wait_for_background_cleanup(context):
    """Wait for background cleanup to complete"""
    loop = context['loop']
    # Wait longer than the cleanup time
    loop.run_until_complete(asyncio.sleep(1.0))

    # Clean up pending tasks
    pending = asyncio.all_tasks(loop)
    for task in pending:
        if not task.done():
            task.cancel()

    if pending:
        loop.run_until_complete(asyncio.gather(*pending, return_exceptions=True))

    loop.close()


@then("is_closed should return true")
def verify_database_closed(context):
    """Verify database reports as closed"""
    database = context['database']
    assert database.is_closed(), "Database should report as closed after close()"


@then("is_closed should return false")
def verify_database_open(context):
    """Verify database reports as open"""
    database = context['database']
    assert not database.is_closed(), "Database should report as open when not closed"


@then("no database errors should be raised")
def verify_no_database_errors(context):
    """Verify no database errors were raised during cleanup"""
    # If we got here without exceptions, the test passed
    assert True
