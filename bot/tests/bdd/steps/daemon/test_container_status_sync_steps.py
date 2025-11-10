"""Step definitions for container status synchronization tests"""
import asyncio
import pytest
from pytest_bdd import given, when, then, scenarios, parsers
from datetime import datetime

from adapters.primary.daemon.container_manager import ContainerManager, ContainerStatus
from adapters.secondary.persistence.database import Database

scenarios('../../features/daemon/container_status_sync.feature')


@pytest.fixture
def context():
    """Test context for sharing state between steps"""
    return {}


@given("a test database")
def setup_database(context):
    """Setup test database"""
    # Create a test player using the repository (will use global test database from conftest)
    from configuration.container import get_player_repository
    from domain.shared.player import Player

    player_repo = get_player_repository()
    test_player = Player(
        player_id=None,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=datetime.now(),
        last_active=datetime.now(),
        metadata={},
        credits=100000
    )
    created_player = player_repo.create(test_player)
    context['player_id'] = created_player.player_id


@given("a mediator instance")
def setup_mediator(context):
    """Setup mediator"""
    # Mock mediator that succeeds quickly
    class MockMediator:
        async def send_async(self, command):
            await asyncio.sleep(0.5)  # Simulate work
            return {"success": True}

    context['mediator'] = MockMediator()


@given("a container manager")
def setup_container_manager(context):
    """Setup container manager with mock container and repositories"""
    from unittest.mock import Mock

    # Mock container type that completes successfully
    class MockCommandContainer:
        def __init__(self, container_id, player_id, config, mediator, container_log_repo, container_info=None):
            from adapters.primary.daemon.types import ContainerStatus
            self.container_id = container_id
            self.player_id = player_id
            self.config = config
            self.mediator = mediator
            self.container_log_repo = container_log_repo
            self.container_info = container_info
            self.cancel_event = asyncio.Event()
            self.status = ContainerStatus.STARTING
            self.iteration = 0
            self.last_result = None

        async def start(self):
            """Mock start that runs like BaseContainer"""
            from adapters.primary.daemon.types import ContainerStatus
            try:
                # This is where BaseContainer sets RUNNING status
                self.status = ContainerStatus.RUNNING
                # Sync status to ContainerInfo if provided (this is the fix!)
                if self.container_info:
                    self.container_info.status = ContainerStatus.RUNNING
                # Simulate work
                wait_time = self.config.get('params', {}).get('wait_time', 0.5)
                await asyncio.sleep(wait_time)
                self.status = ContainerStatus.STOPPED
            except asyncio.CancelledError:
                self.status = ContainerStatus.STOPPED
                raise
            except Exception:
                self.status = ContainerStatus.FAILED
                raise
            finally:
                await self.cleanup()

        async def cleanup(self):
            pass

    # Create real repositories using configuration container (uses test database from conftest)
    from configuration.container import get_container_repository, get_container_log_repository

    container_repo = get_container_repository()
    container_log_repo = get_container_log_repository()

    manager = ContainerManager(context['mediator'], container_repo, container_log_repo)
    # Override container type with mock
    manager._container_types['command'] = MockCommandContainer

    context['container_manager'] = manager
    context['loop'] = asyncio.new_event_loop()


@given(parsers.parse('I create a command container with ID "{container_id}"'))
def create_container(context, container_id):
    """Create a command container"""
    context['container_id'] = container_id
    # player_id should already be set by setup_database step
    if 'player_id' not in context:
        context['player_id'] = 1  # Fallback for tests that don't use setup_database
    context['pending_container_id'] = container_id


@given("the container is configured to run a simple command")
def configure_simple_command(context):
    """Configure container with a quick command"""
    container_id = context.pop('pending_container_id')
    config = {
        'command_type': 'TestCommand',
        'params': {
            'player_id': context['player_id'],
            'wait_time': 2.0  # Long enough to check status while running
        },
        'iterations': 1
    }

    context['config'] = config
    context['container_id'] = container_id


@given("the container is configured to run a long command")
def configure_long_command(context):
    """Configure container with a slow command"""
    container_id = context.pop('pending_container_id')
    config = {
        'command_type': 'TestCommand',
        'params': {
            'player_id': context['player_id'],
            'wait_time': 5.0  # Long enough to inspect while running
        },
        'iterations': 1
    }

    context['config'] = config
    context['container_id'] = container_id


@given(parsers.parse('I create command container "{container_id}" with a quick command'))
def create_quick_container(context, container_id):
    """Create a container with quick command"""
    if 'containers' not in context:
        context['containers'] = {}

    # Get player_id from context (set by setup_database)
    player_id = context.get('player_id', 1)

    config = {
        'command_type': 'TestCommand',
        'params': {
            'player_id': player_id,
            'wait_time': 2.0  # Long enough to check while running
        },
        'iterations': 1
    }

    context['containers'][container_id] = {
        'config': config,
        'player_id': player_id
    }


@given(parsers.parse('I create command container "{container_id}" with a slow command'))
def create_slow_container(context, container_id):
    """Create a container with slow command"""
    if 'containers' not in context:
        context['containers'] = {}

    # Get player_id from context (set by setup_database)
    player_id = context.get('player_id', 1)

    config = {
        'command_type': 'TestCommand',
        'params': {
            'player_id': player_id,
            'wait_time': 5.0
        },
        'iterations': 1
    }

    context['containers'][container_id] = {
        'config': config,
        'player_id': player_id
    }


@when("the container task starts executing")
def start_container(context):
    """Start the container task"""
    manager = context['container_manager']
    container_id = context['container_id']
    player_id = context['player_id']
    config = context['config']
    loop = context['loop']

    # Create and start container using event loop
    info = loop.run_until_complete(
        manager.create_container(
            container_id=container_id,
            player_id=player_id,
            container_type='command',
            config=config,
            restart_policy='no'
        )
    )

    context['container_info'] = info


@when("both container tasks start executing")
def start_multiple_containers(context):
    """Start multiple container tasks"""
    manager = context['container_manager']
    loop = context['loop']

    async def start_all():
        for container_id, data in context['containers'].items():
            info = await manager.create_container(
                container_id=container_id,
                player_id=data['player_id'],
                container_type='command',
                config=data['config'],
                restart_policy='no'
            )
            context['containers'][container_id]['info'] = info

    loop.run_until_complete(start_all())


@when("I wait for the container to begin running")
def wait_for_running(context):
    """Wait for container to transition to RUNNING status"""
    from adapters.primary.daemon.types import ContainerStatus
    loop = context['loop']
    manager = context['container_manager']
    container_id = context['container_id']

    async def poll_status():
        """Poll container status until RUNNING or timeout"""
        max_attempts = 20  # 2 seconds total (20 * 0.1s)
        for _ in range(max_attempts):
            info = manager.get_container(container_id)
            if info and info.status == ContainerStatus.RUNNING:
                return
            await asyncio.sleep(0.1)
        # Timeout - let test assertions handle the failure

    loop.run_until_complete(poll_status())


@when("I wait for both containers to begin running")
def wait_for_multiple_running(context):
    """Wait for multiple containers to transition to RUNNING status"""
    from adapters.primary.daemon.types import ContainerStatus
    loop = context['loop']
    manager = context['container_manager']

    async def poll_all_status():
        """Poll all container statuses until RUNNING or timeout"""
        max_attempts = 20  # 2 seconds total
        for _ in range(max_attempts):
            all_running = True
            for container_id in context['containers'].keys():
                info = manager.get_container(container_id)
                if not info or info.status != ContainerStatus.RUNNING:
                    all_running = False
                    break
            if all_running:
                return
            await asyncio.sleep(0.1)
        # Timeout - let test assertions handle the failure

    loop.run_until_complete(poll_all_status())


@when("I list containers via the container manager")
def list_containers(context):
    """List containers"""
    manager = context['container_manager']
    context['listed_containers'] = manager.list_containers()


@then(parsers.parse('the container status in memory should be "{expected_status}"'))
def check_memory_status(context, expected_status):
    """Verify in-memory status"""
    manager = context['container_manager']
    container_id = context['container_id']

    info = manager.get_container(container_id)
    assert info is not None, f"Container {container_id} not found in memory"
    assert info.status.value == expected_status, \
        f"Expected status {expected_status}, got {info.status.value}"


@then(parsers.parse('the container status in database should be "{expected_status}"'))
def check_database_status(context, expected_status):
    """Verify database status"""
    from configuration.container import get_container_repository

    container_repo = get_container_repository()
    container_id = context['container_id']
    player_id = context['player_id']

    # Use repository to get container data
    container_data = container_repo.get(container_id, player_id)

    assert container_data is not None, f"Container {container_id} not found in database"
    assert container_data['status'] == expected_status, \
        f"Expected database status {expected_status}, got {container_data['status']}"


@then(parsers.parse('listing containers should show status "{expected_status}"'))
def check_list_status(context, expected_status):
    """Verify list_containers returns correct status"""
    manager = context['container_manager']
    containers = manager.list_containers()

    assert len(containers) > 0, "No containers found in list"

    container_id = context['container_id']
    container = next((c for c in containers if c.container_id == container_id), None)

    assert container is not None, f"Container {container_id} not in list"
    assert container.status.value == expected_status, \
        f"Expected listed status {expected_status}, got {container.status.value}"


@then(parsers.parse('all listed containers should show "{expected_status}" status'))
def check_all_list_status(context, expected_status):
    """Verify all listed containers have expected status"""
    listed = context['listed_containers']

    for container in listed:
        assert container.status.value == expected_status, \
            f"Container {container.container_id} has status {container.status.value}, expected {expected_status}"


@then("the in-memory status should match the database status")
def check_status_sync(context):
    """Verify in-memory and database status are synchronized"""
    from configuration.container import get_container_repository

    container_repo = get_container_repository()
    manager = context['container_manager']
    container_id = context['container_id']
    player_id = context['player_id']

    # Get in-memory status
    info = manager.get_container(container_id)
    memory_status = info.status.value

    # Get database status
    container_data = container_repo.get(container_id, player_id)
    db_status = container_data['status']

    assert memory_status == db_status, \
        f"Status mismatch: memory={memory_status}, database={db_status}"


@then(parsers.parse('container "{container_id}" should show "{expected_status}" in list'))
def check_specific_container_list_status(context, container_id, expected_status):
    """Verify specific container has expected status in list"""
    manager = context['container_manager']
    containers = manager.list_containers()

    container = next((c for c in containers if c.container_id == container_id), None)
    assert container is not None, f"Container {container_id} not found in list"
    assert container.status.value == expected_status, \
        f"Container {container_id} has status {container.status.value}, expected {expected_status}"


@then(parsers.parse('both containers should have "{expected_status}" in database'))
def check_multiple_database_status(context, expected_status):
    """Verify all containers have expected status in database"""
    from configuration.container import get_container_repository

    container_repo = get_container_repository()

    for container_id, data in context['containers'].items():
        player_id = data['player_id']
        container_data = container_repo.get(container_id, player_id)

        assert container_data is not None, f"Container {container_id} not found in database"
        assert container_data['status'] == expected_status, \
            f"Container {container_id} database status is {container_data['status']}, expected {expected_status}"
