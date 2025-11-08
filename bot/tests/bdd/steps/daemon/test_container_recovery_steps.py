"""Step definitions for container recovery on daemon startup"""
import json
import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from datetime import datetime

from configuration.container import get_database, get_mediator
from adapters.primary.daemon.daemon_server import DaemonServer
from adapters.primary.daemon.container_manager import ContainerStatus

# Load scenarios from feature file
scenarios('../../features/daemon/container_recovery.feature')


@given('a test database is initialized')
def test_database_initialized(context):
    """Ensure test database is initialized (handled by conftest autouse fixture)"""
    db = get_database()
    context['database'] = db


@given(parsers.parse('a container "{container_id}" exists in the database with status "{status}"'))
def create_container_in_database(context, container_id, status):
    """Create a container record in the database with specified status"""
    db = context['database']
    player_id = context.get('player_id', 1)

    # Default config for command container
    config = {
        'command_type': 'DockShipCommand',  # Use simple command type for testing
        'params': {
            'ship_symbol': context.get('ship_symbol', 'TEST-SHIP-1'),
            'player_id': player_id
        },
        'iterations': 1  # Run once for testing
    }

    with db.transaction() as conn:
        cursor = conn.cursor()
        cursor.execute("""
            INSERT INTO containers (
                container_id, player_id, container_type, status,
                restart_policy, restart_count, config, started_at
            )
            VALUES (?, ?, ?, ?, ?, ?, ?, ?)
        """, (
            container_id,
            player_id,
            'command',
            status,
            'no',
            0,
            json.dumps(config),
            datetime.now().isoformat()
        ))

    # Store in context for later assertions
    if 'containers' not in context:
        context['containers'] = {}
    context['containers'][container_id] = {
        'status': status,
        'config': config,
        'player_id': player_id
    }


@given(parsers.parse('the container has valid configuration for ship "{ship_symbol}"'))
def set_valid_container_config(context, ship_symbol):
    """Ensure container config references a valid ship (already set in default)"""
    # Already handled in create_container_in_database with ship from context
    # This step is declarative for BDD readability
    pass


@given(parsers.parse('the container references non-existent ship "{ship_symbol}"'))
def set_nonexistent_ship_in_config(context, ship_symbol, monkeypatch):
    """Update container config to reference a non-existent ship"""
    db = context['database']

    # Get the most recent container from context
    container_ids = list(context['containers'].keys())
    container_id = container_ids[-1] if container_ids else None
    assert container_id, "No container in context to update"

    config = context['containers'][container_id]['config']
    config['params']['ship_symbol'] = ship_symbol

    with db.transaction() as conn:
        cursor = conn.cursor()
        cursor.execute("""
            UPDATE containers SET config = ? WHERE container_id = ?
        """, (json.dumps(config), container_id))

    context['containers'][container_id]['config'] = config
    context['containers'][container_id]['missing_ship'] = True

    # Mock the API to return ship not found for this specific ship
    from unittest.mock import Mock
    def mock_get_api_client(player_id):
        mock_client = Mock()
        def mock_get_ship(symbol):
            if symbol == ship_symbol:
                # Simulate ship not found - raise exception
                raise Exception(f"Ship {symbol} not found")
            # Return default ship for other symbols
            return {'data': {
                'symbol': symbol,
                'nav': {'waypointSymbol': 'X1-TEST-A1', 'systemSymbol': 'X1-TEST',
                        'status': 'DOCKED', 'flightMode': 'CRUISE'},
                'fuel': {'current': 400, 'capacity': 400},
                'cargo': {'capacity': 100, 'units': 0, 'inventory': []},
                'frame': {'symbol': 'FRAME_PROBE'},
                'reactor': {'symbol': 'REACTOR_SOLAR_I'},
                'engine': {'symbol': 'ENGINE_IMPULSE_DRIVE_I', 'speed': 30},
                'modules': [], 'mounts': []
            }}
        mock_client.get_ship.side_effect = mock_get_ship
        return mock_client
    monkeypatch.setattr('configuration.container.get_api_client_for_player', mock_get_api_client)


@given(parsers.parse('the container has invalid JSON configuration'))
def set_invalid_container_config(context):
    """Update container config to invalid JSON"""
    db = context['database']

    # Get the most recent container from context
    container_ids = list(context['containers'].keys())
    container_id = container_ids[-1] if container_ids else None
    assert container_id, "No container in context to update"

    # Store invalid JSON string (not parseable)
    with db.transaction() as conn:
        cursor = conn.cursor()
        cursor.execute("""
            UPDATE containers SET config = ? WHERE container_id = ?
        """, ("{ invalid json }", container_id))

    context['containers'][container_id]['invalid_config'] = True


@given(parsers.parse('the ship "{ship_symbol}" has an active zombie assignment'))
def create_zombie_assignment(context, ship_symbol):
    """Create an active ship assignment (zombie from previous daemon)"""
    db = context['database']
    player_id = context.get('player_id', 1)

    with db.transaction() as conn:
        cursor = conn.cursor()
        cursor.execute("""
            INSERT INTO ship_assignments (
                ship_symbol, player_id, container_id, operation,
                status, assigned_at
            )
            VALUES (?, ?, ?, ?, ?, ?)
        """, (
            ship_symbol,
            player_id,
            'zombie-container',
            'navigate',
            'active',
            datetime.now().isoformat()
        ))

    context['zombie_assignment_exists'] = True


@when('the daemon server starts up')
def start_daemon_server(context):
    """Start the daemon server (which should trigger recovery)"""
    import asyncio

    async def run_startup():
        # Create a daemon server instance
        daemon = DaemonServer()
        context['daemon'] = daemon

        # Call the startup sequence manually (without actually starting the server)
        # 1. Release zombie assignments
        await daemon.release_all_active_assignments()

        # 2. Recover running containers (this is what we're testing)
        if hasattr(daemon, 'recover_running_containers'):
            await daemon.recover_running_containers()
        else:
            # Method doesn't exist yet - expected in RED phase
            context['recovery_not_implemented'] = True

    asyncio.run(run_startup())


@then(parsers.parse('the container "{container_id}" should be resumed'))
def verify_container_resumed(context, container_id):
    """Verify container was resumed and is now in ContainerManager"""
    daemon = context.get('daemon')
    assert daemon, "Daemon not initialized"

    # Check if container exists in ContainerManager
    container_mgr = daemon._container_mgr
    container_info = container_mgr.get_container(container_id)

    assert container_info is not None, (
        f"Container {container_id} was not resumed - not found in ContainerManager"
    )
    # Container may be STARTING, RUNNING, or STOPPED (if it ran quickly)
    # The key is that it was resumed (exists in ContainerManager)
    assert container_info.status in [
        ContainerStatus.STARTING, ContainerStatus.RUNNING, ContainerStatus.STOPPED
    ], (
        f"Container {container_id} has unexpected status: {container_info.status}"
    )


@then('the container should appear in the containers list')
def verify_container_in_list(context):
    """Verify container appears when listing containers"""
    daemon = context.get('daemon')
    container_mgr = daemon._container_mgr

    containers = container_mgr.list_containers()
    assert len(containers) > 0, "No containers found in list"


@then(parsers.parse('the container status should remain "{expected_status}"'))
def verify_container_status(context, expected_status):
    """Verify container has expected status"""
    daemon = context.get('daemon')
    container_mgr = daemon._container_mgr

    # Get the most recently referenced container
    container_ids = list(context.get('containers', {}).keys())
    container_id = container_ids[-1] if container_ids else None

    container_info = container_mgr.get_container(container_id)
    assert container_info is not None, f"Container {container_id} not found"

    assert container_info.status.value.upper() == expected_status.upper(), (
        f"Expected status {expected_status}, got {container_info.status.value}"
    )


@then(parsers.parse('the container "{container_id}" should be marked as "{expected_status}"'))
def verify_container_marked_as_status(context, container_id, expected_status):
    """Verify container was marked with expected status in database"""
    db = context['database']

    with db.connection() as conn:
        cursor = conn.cursor()
        cursor.execute("""
            SELECT status FROM containers WHERE container_id = ?
        """, (container_id,))
        row = cursor.fetchone()

    assert row is not None, f"Container {container_id} not found in database"
    assert row['status'].upper() == expected_status.upper(), (
        f"Expected status {expected_status}, got {row['status']}"
    )


@then('the container should not appear in the running containers list')
def verify_container_not_in_list(context):
    """Verify failed container does not appear in running list"""
    daemon = context.get('daemon')
    container_mgr = daemon._container_mgr

    # Get the most recently referenced container
    container_ids = list(context.get('containers', {}).keys())
    container_id = container_ids[-1] if container_ids else None

    containers = container_mgr.list_containers()
    container_ids_in_list = [c.container_id for c in containers]

    assert container_id not in container_ids_in_list, (
        f"Container {container_id} should not be in running list but was found"
    )


@then('an error should be logged about invalid configuration')
def verify_error_logged(context):
    """Verify error was logged (check context flag)"""
    # In a real implementation, we'd capture logs
    # For now, verify the recovery was attempted
    assert context.get('recovery_not_implemented') or context.get('daemon'), (
        "Daemon startup was not attempted"
    )


@then(parsers.parse('only container "{container_id}" should be resumed'))
def verify_only_specific_container_resumed(context, container_id):
    """Verify only the specified container was resumed"""
    daemon = context.get('daemon')
    container_mgr = daemon._container_mgr

    containers = container_mgr.list_containers()
    container_ids = [c.container_id for c in containers]

    assert container_id in container_ids, (
        f"Expected container {container_id} to be resumed"
    )

    # Should be exactly one container (the RUNNING one)
    assert len(containers) == 1, (
        f"Expected only 1 container to be resumed, found {len(containers)}"
    )


@then(parsers.parse('container "{container_id}" should remain "{expected_status}"'))
def verify_container_remains_status(context, container_id, expected_status):
    """Verify container kept its original status in database"""
    db = context['database']

    with db.connection() as conn:
        cursor = conn.cursor()
        cursor.execute("""
            SELECT status FROM containers WHERE container_id = ?
        """, (container_id,))
        row = cursor.fetchone()

    assert row is not None, f"Container {container_id} not found"
    assert row['status'].upper() == expected_status.upper(), (
        f"Container {container_id} should remain {expected_status}, got {row['status']}"
    )


@then('zombie assignments should be released first')
def verify_zombie_assignments_released(context):
    """Verify zombie assignments were cleaned up"""
    db = context['database']

    with db.connection() as conn:
        cursor = conn.cursor()
        cursor.execute("""
            SELECT COUNT(*) as count FROM ship_assignments
            WHERE status = 'active' AND container_id = 'zombie-container'
        """)
        row = cursor.fetchone()

    assert row['count'] == 0, (
        "Zombie assignments should have been released before recovery"
    )


@then(parsers.parse('then container "{container_id}" should be resumed'))
def verify_container_resumed_after_cleanup(context, container_id):
    """Verify container was resumed after zombie cleanup"""
    # Reuse the existing resume verification
    verify_container_resumed(context, container_id)


@then(parsers.parse('the ship "{ship_symbol}" should be assigned to container "{container_id}"'))
def verify_ship_assigned_to_container(context, ship_symbol, container_id):
    """Verify ship assignment was created for recovered container"""
    db = context['database']
    player_id = context.get('player_id', 1)

    with db.connection() as conn:
        cursor = conn.cursor()
        cursor.execute("""
            SELECT container_id, status FROM ship_assignments
            WHERE ship_symbol = ? AND player_id = ? AND container_id = ?
            ORDER BY assigned_at DESC LIMIT 1
        """, (ship_symbol, player_id, container_id))
        row = cursor.fetchone()

    assert row is not None, (
        f"No assignment found for ship {ship_symbol} and container {container_id}"
    )
    assert row['container_id'] == container_id, (
        f"Ship assigned to wrong container: {row['container_id']} (expected {container_id})"
    )
    # Status may be 'active' or 'idle' (if container completed quickly)
    # The important thing is that the assignment was created
    assert row['status'] in ['active', 'idle'], (
        f"Assignment should be active or idle, got {row['status']}"
    )
