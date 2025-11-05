"""BDD steps for daemon server lifecycle"""
import asyncio
import socket
import time
import signal
import threading
from pathlib import Path
from pytest_bdd import scenarios, given, when, then, parsers
from spacetraders.adapters.primary.daemon.daemon_server import DaemonServer

scenarios('../../features/daemon/daemon_server.feature')


@given("the daemon server is not running")
def daemon_not_running(context):
    """Ensure daemon is not running"""
    socket_path = Path("var/daemon.sock")
    if socket_path.exists():
        socket_path.unlink()
    context['daemon_server'] = None
    context['daemon_thread'] = None
    context['daemon_loop'] = None


@given("the daemon server is running")
def daemon_running(context):
    """Start daemon server for test"""
    socket_path = Path("var/daemon.sock")
    if socket_path.exists():
        socket_path.unlink()

    server = DaemonServer()
    context['daemon_server'] = server

    # Start server in background thread with its own event loop
    def run_server():
        loop = asyncio.new_event_loop()
        asyncio.set_event_loop(loop)
        context['daemon_loop'] = loop
        context['daemon_error'] = None
        try:
            loop.run_until_complete(server.start())
        except Exception as e:
            # Capture any errors for debugging
            context['daemon_error'] = str(e)
            import traceback
            context['daemon_traceback'] = traceback.format_exc()
        finally:
            loop.close()

    thread = threading.Thread(target=run_server, daemon=True)
    thread.start()
    context['daemon_thread'] = thread

    # Wait for socket to exist
    timeout = 5
    start = time.time()
    while not socket_path.exists() and time.time() - start < timeout:
        time.sleep(0.1)

    if context.get('daemon_error'):
        raise AssertionError(f"Daemon failed to start: {context['daemon_error']}\n{context.get('daemon_traceback', '')}")

    assert socket_path.exists(), "Daemon socket did not appear"


@when("I start the daemon server")
def start_daemon_server(context):
    """Start the daemon server"""
    socket_path = Path("var/daemon.sock")

    server = DaemonServer()
    context['daemon_server'] = server

    # Start server in background thread with its own event loop
    def run_server():
        loop = asyncio.new_event_loop()
        asyncio.set_event_loop(loop)
        context['daemon_loop'] = loop
        context['daemon_error'] = None
        try:
            loop.run_until_complete(server.start())
        except Exception as e:
            # Capture any errors for debugging
            context['daemon_error'] = str(e)
            import traceback
            context['daemon_traceback'] = traceback.format_exc()
        finally:
            loop.close()

    thread = threading.Thread(target=run_server, daemon=True)
    thread.start()
    context['daemon_thread'] = thread

    # Give it time to start
    time.sleep(0.5)

    if context.get('daemon_error'):
        raise AssertionError(f"Daemon failed to start: {context['daemon_error']}\n{context.get('daemon_traceback', '')}")


@when("a client connects to the Unix socket")
def client_connects(context):
    """Connect a client to daemon"""
    socket_path = Path("var/daemon.sock")
    sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    try:
        sock.connect(str(socket_path))
        context['client_socket'] = sock
        context['connection_successful'] = True
    except Exception as e:
        context['connection_successful'] = False
        context['connection_error'] = str(e)


@when("I send a stop signal to the daemon")
def send_stop_signal(context):
    """Stop daemon gracefully"""
    server = context['daemon_server']
    loop = context['daemon_loop']

    # Schedule stop on the daemon's event loop
    if loop and not loop.is_closed():
        asyncio.run_coroutine_threadsafe(server.stop(), loop)
        time.sleep(1)


@then("the daemon server should be running")
def daemon_should_be_running(context):
    """Verify daemon is running"""
    assert context['daemon_server'] is not None
    assert context['daemon_thread'] is not None
    assert context['daemon_thread'].is_alive(), "Daemon thread should still be running"


@then(parsers.parse('the Unix socket should exist at "{path}"'))
def socket_should_exist(context, path):
    """Verify socket file exists"""
    socket_path = Path(path)
    assert socket_path.exists(), f"Socket {path} does not exist"


@then("the connection should be accepted")
def connection_accepted(context):
    """Verify connection was successful"""
    assert context.get('connection_successful'), \
        f"Connection failed: {context.get('connection_error', 'Unknown error')}"


@then("the client can send JSON-RPC requests")
def can_send_jsonrpc(context):
    """Verify client can send requests"""
    import json

    sock = context['client_socket']

    # Send a simple list containers request
    request = {
        "jsonrpc": "2.0",
        "method": "container.list",
        "params": {},
        "id": 1
    }

    sock.sendall(json.dumps(request).encode())
    response_data = sock.recv(65536)
    response = json.loads(response_data.decode())

    assert "result" in response or "error" in response, "Invalid JSON-RPC response"
    sock.close()


@then("the daemon should stop gracefully")
def daemon_stopped(context):
    """Verify daemon stopped"""
    # The server should have stopped by now
    # Check if thread is still alive - it should be finishing up
    thread = context.get('daemon_thread')
    if thread:
        # Give it a moment to fully shut down
        thread.join(timeout=2)

    # Server should have marked itself as not running
    server = context.get('daemon_server')
    if server:
        assert not server._running, "Server should be marked as not running"


@then("the Unix socket should be cleaned up")
def socket_cleaned_up(context):
    """Verify socket was removed"""
    socket_path = Path("var/daemon.sock")
    assert not socket_path.exists(), "Socket should be cleaned up"
