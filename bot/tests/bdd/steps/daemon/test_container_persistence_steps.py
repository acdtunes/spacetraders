"""Step definitions for container persistence tests"""
import pytest
import uuid
import json
from datetime import datetime
from pytest_bdd import scenarios, given, when, then, parsers

from adapters.secondary.persistence.database import Database
from adapters.primary.daemon.container_manager import ContainerManager
from adapters.primary.daemon.types import ContainerStatus
from configuration.container import get_mediator

# Load scenarios
scenarios('../../features/daemon/container_persistence.feature')


@pytest.fixture
def context():
    """Shared context for steps"""
    return {}


@given("a test database")
def create_test_database(context):
    """Create test database"""
    db = Database(":memory:")
    context['database'] = db


@given("a test player exists")
def create_test_player_record(context):
    """Create test player"""
    db = context['database']
    with db.transaction() as conn:
        conn.execute("""
            INSERT INTO players (agent_symbol, token, created_at)
            VALUES (?, ?, ?)
        """, ("TEST_AGENT", "test-token", datetime.now().isoformat()))
        cursor = conn.execute("SELECT player_id FROM players WHERE agent_symbol = ?", ("TEST_AGENT",))
        player_id = cursor.fetchone()['player_id']

    context['player_id'] = player_id


@when('I create a container record in database with status "STARTING"')
def create_container_record(context):
    """Create a container record directly via Database methods"""
    db = context['database']
    container_id = f"test-container-{uuid.uuid4().hex[:8]}"
    player_id = context['player_id']

    # Use the database method we just created
    db.insert_container(
        container_id=container_id,
        player_id=player_id,
        container_type='command',
        status='STARTING',
        restart_policy='no',
        config=json.dumps({'test': 'config'}),
        started_at=datetime.now().isoformat()
    )

    context['container_id'] = container_id


@given('a container exists in database with status "STARTING"')
def container_exists_starting(context):
    """Create container directly in database with STARTING status"""
    db = context['database']
    container_id = f"test-container-{uuid.uuid4().hex[:8]}"
    player_id = context['player_id']

    with db.transaction() as conn:
        conn.execute("""
            INSERT INTO containers (
                container_id, player_id, container_type, status,
                restart_policy, config, started_at
            ) VALUES (?, ?, ?, ?, ?, ?, ?)
        """, (
            container_id,
            player_id,
            'command',
            'STARTING',
            'no',
            json.dumps({'test': 'config'}),
            datetime.now().isoformat()
        ))

    context['container_id'] = container_id


@given('a container exists in database with status "RUNNING"')
def container_exists_running(context):
    """Create container directly in database with RUNNING status"""
    db = context['database']
    container_id = f"test-container-{uuid.uuid4().hex[:8]}"
    player_id = context['player_id']

    with db.transaction() as conn:
        conn.execute("""
            INSERT INTO containers (
                container_id, player_id, container_type, status,
                restart_policy, config, started_at
            ) VALUES (?, ?, ?, ?, ?, ?, ?)
        """, (
            container_id,
            player_id,
            'command',
            'RUNNING',
            'no',
            json.dumps({'test': 'config'}),
            datetime.now().isoformat()
        ))

    context['container_id'] = container_id


@when('the container status changes to "RUNNING"')
def container_status_to_running(context):
    """Update container status to RUNNING"""
    db = context['database']
    container_id = context['container_id']
    player_id = context['player_id']

    # This simulates what BaseContainer.start() should do
    with db.transaction() as conn:
        conn.execute("""
            UPDATE containers
            SET status = ?
            WHERE container_id = ? AND player_id = ?
        """, ('RUNNING', container_id, player_id))


@when("the container completes successfully")
def container_completes_successfully(context):
    """Update container to completed state"""
    db = context['database']
    container_id = context['container_id']
    player_id = context['player_id']

    with db.transaction() as conn:
        conn.execute("""
            UPDATE containers
            SET status = ?, stopped_at = ?, exit_code = ?
            WHERE container_id = ? AND player_id = ?
        """, ('STOPPED', datetime.now().isoformat(), 0, container_id, player_id))


@when(parsers.parse('the container fails with error "{error_message}"'))
def container_fails(context, error_message):
    """Update container to failed state"""
    db = context['database']
    container_id = context['container_id']
    player_id = context['player_id']

    with db.transaction() as conn:
        conn.execute("""
            UPDATE containers
            SET status = ?, stopped_at = ?, exit_code = ?, exit_reason = ?
            WHERE container_id = ? AND player_id = ?
        """, ('FAILED', datetime.now().isoformat(), 1, error_message, container_id, player_id))


@then("the container should exist in the database")
def container_exists_in_db(context):
    """Verify container exists in database"""
    db = context['database']
    container_id = context['container_id']
    player_id = context['player_id']

    with db.connection() as conn:
        cursor = conn.execute("""
            SELECT container_id FROM containers
            WHERE container_id = ? AND player_id = ?
        """, (container_id, player_id))
        row = cursor.fetchone()

    assert row is not None, f"Container {container_id} not found in database"


@then(parsers.parse('the container status in database should be "{expected_status}"'))
def container_status_in_db(context, expected_status):
    """Verify container status in database"""
    db = context['database']
    container_id = context['container_id']
    player_id = context['player_id']

    with db.connection() as conn:
        cursor = conn.execute("""
            SELECT status FROM containers
            WHERE container_id = ? AND player_id = ?
        """, (container_id, player_id))
        row = cursor.fetchone()

    assert row is not None, f"Container {container_id} not found in database"
    actual_status = row['status']
    assert actual_status == expected_status, \
        f"Expected status '{expected_status}', got '{actual_status}'"


@then("the container started_at timestamp should be set")
def container_started_at_set(context):
    """Verify started_at timestamp is set"""
    db = context['database']
    container_id = context['container_id']
    player_id = context['player_id']

    with db.connection() as conn:
        cursor = conn.execute("""
            SELECT started_at FROM containers
            WHERE container_id = ? AND player_id = ?
        """, (container_id, player_id))
        row = cursor.fetchone()

    assert row is not None, f"Container {container_id} not found in database"
    assert row['started_at'] is not None, "started_at should be set"


@then("the container stopped_at timestamp should be set")
def container_stopped_at_set(context):
    """Verify stopped_at timestamp is set"""
    db = context['database']
    container_id = context['container_id']
    player_id = context['player_id']

    with db.connection() as conn:
        cursor = conn.execute("""
            SELECT stopped_at FROM containers
            WHERE container_id = ? AND player_id = ?
        """, (container_id, player_id))
        row = cursor.fetchone()

    assert row is not None, f"Container {container_id} not found in database"
    assert row['stopped_at'] is not None, "stopped_at should be set"


@then(parsers.parse("the container exit_code should be {expected_code:d}"))
def container_exit_code(context, expected_code):
    """Verify container exit code"""
    db = context['database']
    container_id = context['container_id']
    player_id = context['player_id']

    with db.connection() as conn:
        cursor = conn.execute("""
            SELECT exit_code FROM containers
            WHERE container_id = ? AND player_id = ?
        """, (container_id, player_id))
        row = cursor.fetchone()

    assert row is not None, f"Container {container_id} not found in database"
    actual_code = row['exit_code']
    assert actual_code == expected_code, \
        f"Expected exit_code {expected_code}, got {actual_code}"


@then(parsers.parse('the container exit_reason should be "{expected_reason}"'))
def container_exit_reason(context, expected_reason):
    """Verify container exit reason"""
    db = context['database']
    container_id = context['container_id']
    player_id = context['player_id']

    with db.connection() as conn:
        cursor = conn.execute("""
            SELECT exit_reason FROM containers
            WHERE container_id = ? AND player_id = ?
        """, (container_id, player_id))
        row = cursor.fetchone()

    assert row is not None, f"Container {container_id} not found in database"
    actual_reason = row['exit_reason']
    assert actual_reason == expected_reason, \
        f"Expected exit_reason '{expected_reason}', got '{actual_reason}'"
