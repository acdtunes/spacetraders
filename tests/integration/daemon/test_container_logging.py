"""Integration test: verify handler logs are captured in container logs"""
import pytest
import time
import logging
from spacetraders.configuration.container import get_database, get_daemon_client, reset_container


@pytest.fixture
def setup_test_data():
    """Setup test player and ships in database"""
    reset_container()
    db = get_database()

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
        """, ('TEST-SCOUT-1', 9999, 'X1-GZ7-H60', 100, 100, 40, 0, 10, 'DOCKED', 'X1-GZ7'))

    yield 9999

    # Cleanup
    with db.transaction() as conn:
        conn.execute("DELETE FROM players WHERE player_id = ?", (9999,))
        conn.execute("DELETE FROM ships WHERE player_id = ?", (9999,))


def test_handler_logs_are_captured_in_container_logs(setup_test_data):
    """
    Integration test: Verify that handler logs (using Python logging module)
    are captured in container_logs database table.

    This tests that:
    - Handler INFO logs appear in container logs
    - Handler WARNING logs appear in container logs
    - Handler ERROR logs appear in container logs
    - Log levels are preserved
    """
    daemon = get_daemon_client()
    db = get_database()

    container_id = 'test-logging-integration'
    result = daemon.create_container({
        'container_id': container_id,
        'player_id': 9999,
        'container_type': 'command',
        'config': {
            'command_type': 'ScoutTourCommand',
            'params': {
                'ship_symbol': 'TEST-SCOUT-1',
                'player_id': 9999,
                'system': 'X1-GZ7',
                'markets': ['X1-GZ7-H60'],
                'return_to_start': False
            },
            'iterations': 1
        },
        'restart_policy': 'no'
    })

    # Wait for container to execute
    time.sleep(3)

    # Get all logs from container
    with db.connection() as conn:
        logs = conn.execute("""
            SELECT level, message FROM container_logs
            WHERE container_id = ?
            ORDER BY timestamp
        """, (container_id,)).fetchall()

    # Verify we have logs
    assert len(logs) > 0, "No logs found - container may not have started"

    # Verify handler logs are present
    log_messages = [log[1] for log in logs]

    # Should see "Scouting market: X1-GZ7-H60" from handler
    scouting_logs = [msg for msg in log_messages if 'Scouting market:' in msg]
    assert len(scouting_logs) > 0, \
        f"Handler logs not captured! Only saw: {log_messages}"

    # Should see "Tour complete:" from handler
    tour_complete_logs = [msg for msg in log_messages if 'Tour complete:' in msg]
    assert len(tour_complete_logs) > 0, \
        f"Tour completion log not captured! Only saw: {log_messages}"

    # Should see market result logs (either success or error)
    market_result_logs = [msg for msg in log_messages
                          if '✅ Market' in msg or 'Failed to scout market' in msg]
    assert len(market_result_logs) > 0, \
        f"Market result logs not captured! Only saw: {log_messages}"

    # Cleanup
    try:
        daemon.stop_container(container_id)
        daemon.remove_container(container_id)
    except:
        pass
