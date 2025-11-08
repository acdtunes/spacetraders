"""BDD step definitions for daemon socket close speed"""
import asyncio
import time
import pytest
from pytest_bdd import given, when, then, scenario
from unittest.mock import AsyncMock, MagicMock, call
import json

from adapters.primary.daemon.daemon_server import DaemonServer

# Load scenarios individually to avoid unhashable type error
@scenario('../../features/daemon/socket_close_speed.feature',
          'Socket handler closes connection in under 100ms')
def test_socket_handler_closes_connection_in_under_100ms():
    """Test socket handler closes quickly"""
    pass


@scenario('../../features/daemon/socket_close_speed.feature',
          'Socket handler closes on error in under 100ms')
def test_socket_handler_closes_on_error_in_under_100ms():
    """Test socket handler closes quickly on error"""
    pass


@pytest.fixture
def context():
    """Shared test context"""
    return {}


@given("a mock StreamWriter and StreamReader are created")
def create_mock_streams(context):
    """Create mock StreamWriter and StreamReader"""
    from unittest.mock import MagicMock, AsyncMock

    reader = MagicMock()
    writer = MagicMock()

    # Configure reader to return a simple JSON-RPC request
    request = {
        "jsonrpc": "2.0",
        "method": "container.list",
        "params": {},
        "id": 1
    }
    request_bytes = json.dumps(request).encode('utf-8')

    async def mock_read(size):
        return request_bytes

    reader.read = mock_read

    # Configure writer drain to complete immediately
    async def mock_drain():
        pass

    writer.drain = mock_drain
    writer.write = MagicMock()

    # Track if wait_closed was called (THIS SHOULD NOT HAPPEN after fix)
    context['wait_closed_called'] = False

    async def track_wait_closed():
        context['wait_closed_called'] = True
        await asyncio.sleep(60)  # Simulate 60s delay

    writer.wait_closed = track_wait_closed
    writer.close = MagicMock()

    context['reader'] = reader
    context['writer'] = writer


@when("the daemon handles a successful request")
def handle_successful_request(context):
    """Test daemon handling a successful request"""
    async def _async_handler():
        server = DaemonServer()
        reader = context['reader']
        writer = context['writer']

        # Measure time for connection handler to complete
        start_time = time.time()

        try:
            await asyncio.wait_for(
                server._handle_connection(reader, writer),
                timeout=5.0  # Generous timeout
            )
        except asyncio.TimeoutError:
            context['handler_timeout'] = True
        else:
            context['handler_timeout'] = False

        elapsed = time.time() - start_time
        context['handler_elapsed'] = elapsed

    # Run in event loop
    asyncio.run(_async_handler())


@when("the daemon handles a request that raises an error")
def handle_error_request(context):
    """Test daemon handling a request that raises an error"""
    async def _async_handler():
        server = DaemonServer()
        reader = context['reader']
        writer = context['writer']

        # Configure reader to return invalid JSON
        reader.read = AsyncMock(return_value=b"invalid json{{{")

        # Measure time for connection handler to complete
        start_time = time.time()

        try:
            await asyncio.wait_for(
                server._handle_connection(reader, writer),
                timeout=5.0
            )
        except asyncio.TimeoutError:
            context['handler_timeout'] = True
        else:
            context['handler_timeout'] = False

        elapsed = time.time() - start_time
        context['handler_elapsed'] = elapsed

    # Run in event loop
    asyncio.run(_async_handler())


@then("the connection handler should complete in under 100ms")
def verify_handler_speed(context):
    """Verify connection handler completed quickly"""
    if context.get('handler_timeout', False):
        pytest.fail(
            "Handler timed out - likely due to wait_closed() blocking. "
            "Expected handler to complete in < 100ms"
        )

    elapsed = context['handler_elapsed']

    # THIS IS THE KEY ASSERTION - it will FAIL before the fix
    # With wait_closed(), this would timeout (5+ seconds)
    # Without wait_closed(), this should be < 100ms
    assert elapsed < 0.1, \
        f"Handler should complete in < 100ms, but took {elapsed:.3f}s. " \
        f"This indicates wait_closed() is blocking the handler."


@then("writer.close() should be called")
def verify_close_called(context):
    """Verify writer.close() was called"""
    writer = context['writer']
    assert writer.close.called, "writer.close() should have been called"


@then("writer.wait_closed() should NOT be called")
def verify_wait_closed_not_called(context):
    """Verify writer.wait_closed() was NOT called (the fix!)"""
    # This assertion will FAIL before the fix and PASS after
    assert not context['wait_closed_called'], \
        "writer.wait_closed() should NOT be called - it causes 60s delays! " \
        "writer.close() is sufficient for cleanup."
