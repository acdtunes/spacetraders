"""Integration test for scout tour - end-to-end daemon execution"""
import pytest
import asyncio
import time
from spacetraders.configuration.container import get_database, get_daemon_client, reset_container
from spacetraders.domain.shared.player import Player
from spacetraders.domain.shared.ship import Ship


@pytest.fixture
def setup_test_data():
    """Setup test player and ships in database"""
    reset_container()
    db = get_database()

    # Insert test player and ship
    with db.transaction() as conn:
        conn.execute("""
            INSERT OR REPLACE INTO players (player_id, agent_symbol, token, created_at)
            VALUES (?, ?, ?, datetime('now'))
        """, (9999, "TEST_INTEGRATION", "test-token-123"))

        conn.execute("""
            INSERT OR REPLACE INTO ships
            (ship_symbol, player_id, current_location_symbol, fuel_current, fuel_capacity,
             cargo_capacity, cargo_units, engine_speed, nav_status, system_symbol, synced_at)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))
        """, ('TEST-SCOUT-1', 9999, 'X1-TEST-A1', 100, 100, 40, 0, 10, 'DOCKED', 'X1-TEST'))

    yield 9999

    # Cleanup
    with db.transaction() as conn:
        conn.execute("DELETE FROM players WHERE player_id = ?", (9999,))
        conn.execute("DELETE FROM ships WHERE player_id = ?", (9999,))


def test_scout_tour_container_executes_successfully(setup_test_data):
    """
    Integration test: Verify daemon can execute ScoutTourCommand end-to-end

    This tests:
    - Daemon can discover ScoutTourCommand from scouting.commands module
    - Container starts and runs without crashing
    - Command executes (even if navigation fails due to missing graph data)
    """
    daemon = get_daemon_client()
    db = get_database()

    # Create scout tour container
    container_id = 'test-scout-integration'
    result = daemon.create_container({
        'container_id': container_id,
        'player_id': 9999,
        'container_type': 'command',
        'config': {
            'command_type': 'ScoutTourCommand',
            'params': {
                'ship_symbol': 'TEST-SCOUT-1',
                'player_id': 9999,
                'system': 'X1-TEST',
                'markets': ['X1-TEST-A1'],
                'return_to_start': False
            },
            'iterations': 1
        },
        'restart_policy': 'no'
    })

    # Wait for container to start and attempt execution
    time.sleep(2)

    # Check container logs - it should have started (not failed to find command)
    with db.connection() as conn:
        logs = conn.execute("""
            SELECT level, message FROM container_logs
            WHERE container_id = ?
            ORDER BY timestamp
        """, (container_id,)).fetchall()

    # Verify logs exist
    assert len(logs) > 0, "No logs found - container may not have started"

    # Check for command discovery errors
    error_logs = [log for log in logs if log[0] == 'ERROR']
    command_not_found_errors = [
        log for log in error_logs
        if 'not found in standard locations' in log[1]
    ]

    assert len(command_not_found_errors) == 0, \
        f"Daemon failed to discover ScoutTourCommand: {command_not_found_errors}"

    # Verify container started iteration
    starting_logs = [log for log in logs if 'Starting' in log[1] and 'ScoutTourCommand' in log[1]]
    assert len(starting_logs) > 0, "Container did not start executing ScoutTourCommand"

    # Cleanup
    try:
        daemon.stop_container(container_id)
        daemon.remove_container(container_id)
    except:
        pass


def test_scout_stationary_poll_container_executes_successfully(setup_test_data):
    """
    Integration test: Verify daemon can execute ScoutStationaryPollCommand
    """
    daemon = get_daemon_client()
    db = get_database()

    container_id = 'test-poll-integration'
    result = daemon.create_container({
        'container_id': container_id,
        'player_id': 9999,
        'container_type': 'command',
        'config': {
            'command_type': 'ScoutStationaryPollCommand',
            'params': {
                'ship_symbol': 'TEST-SCOUT-1',
                'player_id': 9999,
                'system': 'X1-TEST',
                'market': 'X1-TEST-A1'
            },
            'iterations': 1
        },
        'restart_policy': 'no'
    })

    time.sleep(2)

    with db.connection() as conn:
        logs = conn.execute("""
            SELECT level, message FROM container_logs
            WHERE container_id = ?
            ORDER BY timestamp
        """, (container_id,)).fetchall()

    assert len(logs) > 0, "No logs found"

    command_not_found_errors = [
        log for log in logs
        if log[0] == 'ERROR' and 'not found in standard locations' in log[1]
    ]

    assert len(command_not_found_errors) == 0, \
        f"Daemon failed to discover ScoutStationaryPollCommand: {command_not_found_errors}"

    # Cleanup
    try:
        daemon.stop_container(container_id)
        daemon.remove_container(container_id)
    except:
        pass
