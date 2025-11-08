"""BDD step definitions for immediate container stop behavior"""
import asyncio
import time
import pytest
from pytest_bdd import given, when, then, scenarios
from datetime import datetime

from adapters.primary.daemon.container_manager import ContainerManager
from adapters.primary.daemon.types import ContainerStatus
from adapters.secondary.persistence.database import Database

# Load scenarios
scenarios('../../features/daemon/container_stop.feature')


@pytest.fixture
def context():
    """Shared test context"""
    ctx = {}
    yield ctx

    # Cleanup: Close event loop and clean up pending tasks
    if 'loop' in ctx and ctx['loop']:
        loop = ctx['loop']

        # Cancel all pending tasks in the loop
        if not loop.is_closed():
            pending = asyncio.all_tasks(loop)
            for task in pending:
                task.cancel()

            # Run loop briefly to allow cancellations to complete
            if pending:
                loop.run_until_complete(asyncio.gather(*pending, return_exceptions=True))

            # Close the loop
            loop.close()


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
    db.close()


@given("a test database is initialized")
def test_database_initialized(test_database):
    """Mark database as initialized in context"""
    pass


@given("the daemon container manager is initialized")
def daemon_manager_initialized(context, test_database):
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
            """Simulate long-running operation that doesn't respect cancellation quickly"""
            wait_time = self.config.get('wait_time', 369)
            # Simulate operation that takes time to notice cancellation
            # This mimics real-world operations that do cleanup before exiting
            try:
                for i in range(int(wait_time)):
                    await asyncio.sleep(1)
            except asyncio.CancelledError:
                # Simulate cleanup that takes time
                await asyncio.sleep(0.5)
                raise

    manager = ContainerManager(MockMediator(), test_database)
    manager._container_types['command'] = LongRunningContainer

    context['manager'] = manager
    context['database'] = test_database
    context['loop'] = asyncio.new_event_loop()


@given("a container is running with a long-running operation")
def create_long_running_container(context):
    """Create a container with a long-running operation"""
    manager = context['manager']
    loop = context['loop']

    container_id = f"long-running-{datetime.now().timestamp()}"
    config = {
        'command': 'long_operation',
        'wait_time': 10  # 10 seconds is enough to demonstrate the problem in tests
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


@given("a container exists but task is None")
def create_container_with_no_task(context):
    """Create container info with None task"""
    manager = context['manager']
    loop = context['loop']

    container_id = f"no-task-{datetime.now().timestamp()}"
    config = {'command': 'test'}

    info = loop.run_until_complete(manager.create_container(
        container_id=container_id,
        player_id=1,
        container_type='command',
        config=config,
        restart_policy='no'
    ))

    # Manually set task to None to simulate edge case
    info.task = None

    context['container_id'] = container_id
    context['container_info'] = info


@given("a container has already completed")
def create_completed_container(context):
    """Create container that has already completed"""
    manager = context['manager']
    loop = context['loop']

    container_id = f"completed-{datetime.now().timestamp()}"
    config = {
        'command': 'quick_test',
        'wait_time': 0.01
    }

    info = loop.run_until_complete(manager.create_container(
        container_id=container_id,
        player_id=1,
        container_type='command',
        config=config,
        restart_policy='no'
    ))

    # Wait for completion
    if info.task:
        loop.run_until_complete(asyncio.wait_for(info.task, timeout=2.0))

    # Refresh to get updated status
    info = manager.get_container(container_id)

    context['container_id'] = container_id
    context['container_info'] = info


@given("a container is running")
def create_running_container(context):
    """Create a running container"""
    manager = context['manager']
    loop = context['loop']

    container_id = f"running-{datetime.now().timestamp()}"
    config = {
        'command': 'operation',
        'wait_time': 100
    }

    info = loop.run_until_complete(manager.create_container(
        container_id=container_id,
        player_id=1,
        container_type='command',
        config=config,
        restart_policy='no'
    ))

    # Give it time to start
    loop.run_until_complete(asyncio.sleep(0.1))

    context['container_id'] = container_id
    context['container_info'] = info


@given("a container is waiting for ship navigation with 369 seconds remaining")
def create_navigation_waiting_container(context):
    """Create container simulating ship navigation wait"""
    manager = context['manager']
    loop = context['loop']

    container_id = f"nav-wait-{datetime.now().timestamp()}"
    config = {
        'command': 'navigate_ship',
        'wait_time': 10  # Use 10 seconds for testing instead of 369
    }

    info = loop.run_until_complete(manager.create_container(
        container_id=container_id,
        player_id=1,
        container_type='command',
        config=config,
        restart_policy='no'
    ))

    # Give it time to start
    loop.run_until_complete(asyncio.sleep(0.1))

    context['container_id'] = container_id
    context['container_info'] = info


@when("I issue a stop command")
def issue_stop_command(context):
    """Issue stop command and measure response time"""
    manager = context['manager']
    container_id = context['container_id']
    loop = context['loop']

    start_time = time.time()
    loop.run_until_complete(manager.stop_container(container_id))
    elapsed = time.time() - start_time

    context['stop_elapsed_time'] = elapsed
    context['container_info'] = manager.get_container(container_id)

    # Also track if we waited for the long operation
    context['waited_for_operation'] = elapsed > 2.0


@when("I issue another stop command immediately")
def issue_second_stop_command(context):
    """Issue a second stop command"""
    manager = context['manager']
    container_id = context['container_id']
    loop = context['loop']

    try:
        loop.run_until_complete(manager.stop_container(container_id))
        context['second_stop_success'] = True
        context['second_stop_error'] = None
    except Exception as e:
        context['second_stop_success'] = False
        context['second_stop_error'] = str(e)

    context['container_info'] = manager.get_container(container_id)


@when("I issue a stop command at time T")
def issue_stop_at_time_t(context):
    """Issue stop command and record exact timing"""
    manager = context['manager']
    container_id = context['container_id']
    loop = context['loop']

    context['stop_start_time'] = time.time()
    loop.run_until_complete(manager.stop_container(container_id))
    context['stop_end_time'] = time.time()

    context['container_info'] = manager.get_container(container_id)


@then("the container should stop immediately within 2 seconds")
def verify_immediate_stop(context):
    """Verify stop completed within 2 seconds"""
    elapsed = context['stop_elapsed_time']
    print(f"DEBUG: Stop elapsed time: {elapsed:.3f}s")

    # With the current graceful shutdown (await task), it takes ~0.5s for cleanup
    # Without await (immediate stop), it should be < 0.1s
    assert elapsed < 0.2, \
        f"Stop should be immediate (< 0.2s), but took {elapsed:.2f}s - this means we waited for cleanup"

    # Additional check: we should NOT have waited for the long operation
    waited = context.get('waited_for_operation', False)
    assert not waited, \
        "Stop should not wait for long-running operation to complete"


@then('the container status should be "STOPPED"')
def verify_status_stopped(context):
    """Verify container status is STOPPED"""
    info = context['container_info']
    assert info is not None, "Container info should exist"
    assert info.status == ContainerStatus.STOPPED, \
        f"Expected status STOPPED, got {info.status.value}"


@then("the stop timestamp should be set")
def verify_stop_timestamp(context):
    """Verify stop timestamp is set"""
    info = context['container_info']
    assert info is not None, "Container info should exist"
    assert info.stopped_at is not None, \
        "Stop timestamp should be set"
    assert isinstance(info.stopped_at, datetime), \
        "Stop timestamp should be a datetime object"


@then("the database should reflect the stopped status")
def verify_database_status(context):
    """Verify database has STOPPED status"""
    database = context['database']
    container_id = context['container_id']

    # Query database for container status
    with database.transaction() as conn:
        cursor = conn.execute("""
            SELECT status FROM containers WHERE container_id = ?
        """, (container_id,))
        row = cursor.fetchone()

    assert row is not None, "Container should exist in database"
    assert row[0] == ContainerStatus.STOPPED.value, \
        f"Database status should be STOPPED, got {row[0]}"


@then("the stop should succeed without error")
def verify_stop_success(context):
    """Verify stop command succeeded"""
    # If we got here, stop_container didn't raise an exception
    assert True


@then('the container status should remain "STOPPED"')
def verify_status_remains_stopped(context):
    """Verify status is still STOPPED"""
    info = context['container_info']
    assert info is not None, "Container info should exist"
    assert info.status == ContainerStatus.STOPPED, \
        f"Status should remain STOPPED, got {info.status.value}"


@then("both stops should succeed")
def verify_both_stops_succeeded(context):
    """Verify both stop commands succeeded"""
    assert context.get('second_stop_success', False), \
        f"Second stop failed: {context.get('second_stop_error', 'Unknown error')}"


@then("the stop should complete by time T + 2 seconds")
def verify_stop_timing(context):
    """Verify stop completed within 2 seconds of start"""
    start = context['stop_start_time']
    end = context['stop_end_time']
    elapsed = end - start

    assert elapsed < 2.0, \
        f"Stop should complete within 2 seconds of T, but took {elapsed:.2f}s"


@then("the container task should be cancelled")
def verify_task_cancelled(context):
    """Verify the asyncio task was cancelled"""
    info = context['container_info']
    assert info is not None, "Container info should exist"

    # Task should have cancel() called on it
    # With immediate stop, it may still be in "cancelling" state (not done yet)
    # This is expected - we don't wait for it to finish cancelling
    if info.task is not None:
        # Task should either be done or in the process of cancelling
        assert info.task.done() or info.task.cancelling(), \
            "Task should be cancelled or in cancelling state after stop"


@then('the status should be "STOPPED" immediately')
def verify_immediate_stopped_status(context):
    """Verify status is STOPPED immediately after stop"""
    info = context['container_info']
    assert info is not None, "Container info should exist"
    assert info.status == ContainerStatus.STOPPED, \
        f"Status should be STOPPED immediately, got {info.status.value}"
