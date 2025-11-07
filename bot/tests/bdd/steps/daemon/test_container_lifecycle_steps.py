"""BDD step definitions for container lifecycle status transitions"""
import asyncio
import pytest
from pytest_bdd import given, when, then, parsers, scenarios
from datetime import datetime

from adapters.primary.daemon.container_manager import ContainerManager
from adapters.primary.daemon.types import ContainerStatus
from adapters.secondary.persistence.database import Database

# Load scenarios
scenarios('../../features/daemon/container_lifecycle.feature')


@pytest.fixture
def context():
    """Shared test context"""
    return {}


@pytest.fixture
def test_database():
    """Initialize test database"""
    db = Database(":memory:")
    yield db
    db.close()


@given("a test database is initialized")
def test_database_initialized(test_database):
    """Mark database as initialized in context"""
    pass  # Database is already initialized via fixture


@given("the daemon container manager is initialized")
def daemon_manager_initialized(context, test_database):
    """Initialize daemon container manager with mock mediator"""
    # Mock mediator that succeeds quickly
    class MockMediator:
        async def send_async(self, command):
            await asyncio.sleep(0.01)  # Simulate quick work
            return {"success": True}

    # Mock container type that completes successfully
    class MockCommandContainer:
        def __init__(self, container_id, player_id, config, mediator, database):
            self.container_id = container_id
            self.player_id = player_id
            self.config = config
            self.mediator = mediator
            self.database = database

        async def start(self):
            """Mock start that completes successfully or fails based on config"""
            await asyncio.sleep(0.01)  # Simulate work
            # Check if this container should fail
            if self.config.get('should_fail', False):
                raise RuntimeError("Container failed as configured")

    manager = ContainerManager(MockMediator(), test_database)
    # Override container type with mock
    manager._container_types['command'] = MockCommandContainer

    # Store mock class for test scenarios that need to simulate failures
    context['MockCommandContainer'] = MockCommandContainer

    context['manager'] = manager
    context['database'] = test_database
    # Create event loop for running async operations
    context['loop'] = asyncio.new_event_loop()


@given('a new daemon container is created with type "command"')
def create_container_command_type(context):
    """Create a command-type container"""
    manager = context['manager']
    loop = context['loop']

    container_id = f"test-container-{datetime.now().timestamp()}"
    config = {
        'command': 'test_command',
        'params': {},
        'should_fail': False
    }

    info = loop.run_until_complete(manager.create_container(
        container_id=container_id,
        player_id=1,
        container_type='command',
        config=config,
        restart_policy='no'
    ))

    context['container_id'] = container_id
    context['container_info'] = info


@given("a new daemon container is created that will fail")
def create_failing_container(context):
    """Create a container that will fail"""
    manager = context['manager']
    loop = context['loop']

    container_id = f"fail-container-{datetime.now().timestamp()}"
    config = {
        'command': 'test_command',
        'params': {},
        'should_fail': True  # This tells the mock to raise an exception
    }

    info = loop.run_until_complete(manager.create_container(
        container_id=container_id,
        player_id=1,
        container_type='command',
        config=config,
        restart_policy='no'
    ))

    context['container_id'] = container_id
    context['container_info'] = info


@given("a daemon container that runs for less than 1 second")
def create_quick_container(context):
    """Create a container that completes quickly"""
    manager = context['manager']
    loop = context['loop']

    container_id = f"quick-container-{datetime.now().timestamp()}"
    config = {
        'command': 'quick_test',
        'params': {}
    }

    info = loop.run_until_complete(manager.create_container(
        container_id=container_id,
        player_id=1,
        container_type='command',
        config=config,
        restart_policy='no'
    ))

    context['container_id'] = container_id
    context['container_info'] = info


@given("a container with exit code 0 and stop timestamp")
def container_with_exit_code_and_timestamp(context):
    """Create container that completes and has exit code and timestamp"""
    manager = context['manager']
    loop = context['loop']

    container_id = f"test-container-{datetime.now().timestamp()}"
    config = {
        'command': 'test_command',
        'params': {},
        'should_fail': False
    }

    info = loop.run_until_complete(manager.create_container(
        container_id=container_id,
        player_id=1,
        container_type='command',
        config=config,
        restart_policy='no'
    ))

    # Wait for container to complete so exit code and timestamp are set
    if info.task:
        try:
            loop.run_until_complete(asyncio.wait_for(info.task, timeout=2.0))
        except asyncio.TimeoutError:
            pytest.fail("Container did not complete within timeout")

    # Refresh to get updated info
    info = manager.get_container(container_id)

    context['container_id'] = container_id
    context['container_info'] = info


@when("the container process completes successfully")
def container_completes_successfully(context):
    """Wait for container to complete successfully"""
    info = context['container_info']
    loop = context['loop']

    # Wait for container task to complete
    if info.task:
        try:
            loop.run_until_complete(asyncio.wait_for(info.task, timeout=5.0))
        except asyncio.TimeoutError:
            pytest.fail("Container did not complete within timeout")

    # Refresh container info from manager
    manager = context['manager']
    context['container_info'] = manager.get_container(context['container_id'])


@when("the container completes")
@when("the container process completes")
def container_completes(context):
    """Wait for container to complete (success or failure)"""
    info = context['container_info']
    loop = context['loop']

    # Wait for completion
    if info.task:
        try:
            loop.run_until_complete(asyncio.wait_for(info.task, timeout=2.0))
        except asyncio.TimeoutError:
            pytest.fail("Container did not complete within timeout")

    # Refresh container info
    manager = context['manager']
    context['container_info'] = manager.get_container(context['container_id'])


@when("I query the container status")
def query_container_status(context):
    """Query current container status"""
    manager = context['manager']
    context['container_info'] = manager.get_container(context['container_id'])


@when("I list all containers")
def list_all_containers(context):
    """List all containers"""
    manager = context['manager']
    context['container_list'] = manager.list_containers()


@then('the container status should be "STOPPED"')
def verify_status_stopped(context):
    """Verify container status is STOPPED"""
    info = context['container_info']
    assert info is not None, "Container info should exist"
    assert info.status == ContainerStatus.STOPPED, \
        f"Expected status STOPPED, got {info.status.value}"


@then('the status should be "STOPPED" not "STARTING"')
def verify_status_not_starting(context):
    """Verify status is STOPPED, not STARTING"""
    info = context['container_info']
    assert info is not None, "Container info should exist"
    assert info.status != ContainerStatus.STARTING, \
        "Container status should not be STARTING after completion"
    assert info.status == ContainerStatus.STOPPED, \
        f"Expected status STOPPED, got {info.status.value}"


@then('the status must be "STOPPED" not "STARTING"')
def verify_status_must_be_stopped(context):
    """Enforce status MUST be STOPPED when exit code and timestamp set"""
    info = context['container_info']
    assert info is not None, "Container info should exist"

    # If exit code is set and stopped timestamp exists, status MUST be STOPPED
    if info.exit_code is not None and info.stopped_at is not None:
        assert info.status == ContainerStatus.STOPPED, \
            f"Container with exit_code={info.exit_code} and stopped_at={info.stopped_at} " \
            f"MUST have status=STOPPED, but got {info.status.value}"


@then(parsers.parse("the exit code should be {expected_code:d}"))
def verify_exit_code(context, expected_code):
    """Verify exit code"""
    info = context['container_info']
    assert info is not None, "Container info should exist"
    assert info.exit_code == expected_code, \
        f"Expected exit code {expected_code}, got {info.exit_code}"


@then("the stop timestamp should be set")
def verify_stop_timestamp(context):
    """Verify stop timestamp is set"""
    info = context['container_info']
    assert info is not None, "Container info should exist"
    assert info.stopped_at is not None, \
        "Stop timestamp should be set"
    assert isinstance(info.stopped_at, datetime), \
        "Stop timestamp should be a datetime object"


@then('the container should appear with status "STOPPED"')
def verify_container_list_status(context):
    """Verify container appears with STOPPED status in list"""
    container_list = context['container_list']
    container_id = context['container_id']

    matching = [c for c in container_list if c.container_id == container_id]
    assert len(matching) == 1, f"Expected 1 container with ID {container_id}, found {len(matching)}"

    container = matching[0]
    assert container.status == ContainerStatus.STOPPED, \
        f"Container in list should have status STOPPED, got {container.status.value}"


@then("I should be able to remove the container directly")
def remove_container_directly(context):
    """Attempt to remove container without stopping first"""
    manager = context['manager']
    container_id = context['container_id']
    loop = context['loop']

    # This should succeed if status is properly STOPPED
    try:
        loop.run_until_complete(manager.remove_container(container_id))
        context['removal_success'] = True
    except ValueError as e:
        context['removal_success'] = False
        context['removal_error'] = str(e)


@then("removal should not require daemon_stop first")
def verify_removal_no_stop_needed(context):
    """Verify removal succeeded without explicit stop"""
    assert context.get('removal_success', False), \
        f"Container removal failed: {context.get('removal_error', 'Unknown error')}"
