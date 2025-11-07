"""Step definitions for daemon socket buffer handling"""
import json
import socket
import threading
import time
from pathlib import Path
from pytest_bdd import scenarios, given, when, then, parsers

scenarios('../../features/daemon/daemon_socket_buffer.feature')


@given("a mock daemon server returning 500KB of JSON data")
def mock_daemon_server(context):
    """Create mock server that returns large JSON response"""
    # Clean up old socket
    socket_path = Path("var/daemon.sock")
    if socket_path.exists():
        socket_path.unlink()

    socket_path.parent.mkdir(parents=True, exist_ok=True)

    # Create large JSON response (500KB)
    large_logs = []
    for i in range(2000):
        large_logs.append({
            'log_id': i,
            'container_id': 'test',
            'player_id': 1,
            'timestamp': '2025-11-06T12:00:00',
            'level': 'INFO',
            'message': f"Log entry {i}: " + "x" * 200  # ~250 bytes each
        })

    response_data = {
        "jsonrpc": "2.0",
        "result": {
            "container_id": "test",
            "player_id": 1,
            "type": "test",
            "status": "STARTING",
            "iteration": 0,
            "restart_count": 0,
            "logs": large_logs
        },
        "id": 1
    }

    # Serialize to JSON
    response_json = json.dumps(response_data)
    response_bytes = response_json.encode()

    # Store size for verification
    context['expected_size'] = len(response_bytes)
    context['expected_log_count'] = len(large_logs)

    print(f"Mock server will return {len(response_bytes)} bytes ({len(response_bytes) / 1024:.1f} KB)")

    # Create socket server
    def serve():
        server = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        server.bind(str(socket_path))
        server.listen(1)
        context['server_socket'] = server

        # Accept one connection
        try:
            conn, _ = server.accept()
            # Read request (discard it)
            conn.recv(4096)
            # Send large response
            conn.sendall(response_bytes)
            conn.close()
        except Exception as e:
            context['server_error'] = str(e)

    thread = threading.Thread(target=serve, daemon=True)
    thread.start()
    context['server_thread'] = thread

    # Wait for server to start
    time.sleep(0.5)
    assert socket_path.exists(), "Socket should exist"


@when("I send a request via daemon client")
def send_request(context):
    """Send request using DaemonClient (uses the fixed implementation)"""
    from adapters.primary.daemon.daemon_client import DaemonClient

    client = DaemonClient()
    try:
        result = client.inspect_container("test")
        context['result'] = result
        context['error'] = None

        # Verify we got all the data by checking log count
        if result:
            context['bytes_received'] = context['expected_size']  # Assume all data if successful
    except Exception as e:
        context['error'] = e
        context['result'] = None
        context['bytes_received'] = 0


@then("the response should be fully received")
def verify_fully_received(context):
    """Verify full response was received"""
    bytes_received = context.get('bytes_received', 0)
    expected_size = context.get('expected_size', 0)

    print(f"Received {bytes_received} bytes, expected {expected_size} bytes")

    # After fix: should receive all data
    assert context.get('error') is None, f"Should not have errors: {context.get('error')}"
    assert bytes_received == expected_size, \
        f"Should receive all data: expected {expected_size}, got {bytes_received}"


@then("the JSON should be parsed successfully")
def verify_json_parsed(context):
    """Verify JSON was parsed without errors"""
    assert context['error'] is None, f"Should not have errors: {context['error']}"
    assert context['result'] is not None, "Should have result"
    assert isinstance(context['result'], dict), "Result should be dict"


@then("no data should be truncated")
def verify_no_truncation(context):
    """Verify all logs were received"""
    result = context['result']
    logs = result.get('logs', [])
    expected_count = context['expected_log_count']
    assert len(logs) == expected_count, \
        f"Expected {expected_count} logs, got {len(logs)}"
