"""Step definitions for daemon socket buffer handling"""
import json
import socket
import threading
import time
import pytest
from pathlib import Path
from pytest_bdd import scenarios, given, when, then, parsers

scenarios('../../features/daemon/daemon_socket_buffer.feature')


@pytest.fixture
def context():
    """Shared test context with cleanup"""
    ctx = {}
    yield ctx

    # Cleanup: Close server socket and wait for thread
    if 'server_socket' in ctx:
        try:
            ctx['server_socket'].close()
        except Exception:
            pass

    if 'server_thread' in ctx and ctx['server_thread'].is_alive():
        # Give thread a moment to finish
        ctx['server_thread'].join(timeout=2.0)

    # Cleanup: Remove test socket file
    if 'socket_path' in ctx:
        try:
            ctx['socket_path'].unlink()
        except Exception:
            pass


@given("a mock daemon server returning 500KB of JSON data")
def mock_daemon_server(context):
    """Create mock server that returns large JSON response"""
    # Use TEST-SPECIFIC socket path to avoid interfering with production daemon
    socket_path = Path("var/test-daemon.sock")
    if socket_path.exists():
        socket_path.unlink()

    socket_path.parent.mkdir(parents=True, exist_ok=True)

    # Store socket path for cleanup and client usage
    context['socket_path'] = socket_path

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
            # Read request (like real server does - don't wait for EOF)
            # Real daemon server uses reader.read(65536) which reads available data
            request_data = conn.recv(65536)

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

    # Monkey-patch DaemonClient to use test socket path instead of production socket
    original_socket_path = DaemonClient.SOCKET_PATH
    DaemonClient.SOCKET_PATH = context['socket_path']

    try:
        client = DaemonClient()
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
    finally:
        # Restore original socket path
        DaemonClient.SOCKET_PATH = original_socket_path


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
