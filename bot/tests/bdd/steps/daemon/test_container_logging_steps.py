"""Step definitions for container logging tests"""
import pytest
import asyncio
import logging
from dataclasses import dataclass
from pytest_bdd import scenarios, given, when, then, parsers

from configuration.container import (
    get_database,
    get_player_repository,
    get_ship_repository,
    get_mediator
)
from adapters.primary.daemon.container_manager import ContainerManager
from adapters.primary.daemon.command_container import CommandContainer
from pymediatr import Request, RequestHandler
from domain.shared.player import Player
from domain.shared.ship import Ship
from domain.shared.value_objects import Waypoint, Fuel
from datetime import datetime

# Load scenarios from feature file
scenarios('../../features/daemon/container_logging.feature')

# Test command that logs at multiple levels
logger = logging.getLogger(__name__)


@dataclass(frozen=True)
class MultiLevelLoggingCommand(Request[str]):
    """Command that logs at multiple levels for testing"""
    player_id: int
    log_info: bool = True
    log_warning: bool = True
    log_error: bool = True
    log_debug: bool = True


class MultiLevelLoggingCommandHandler(RequestHandler[MultiLevelLoggingCommand, str]):
    """Handler that logs at multiple levels"""

    async def handle(self, request: MultiLevelLoggingCommand) -> str:
        if request.log_info:
            logger.info("INFO level test message")
        if request.log_warning:
            logger.warning("WARNING level test message")
        if request.log_error:
            logger.error("ERROR level test message")
        if request.log_debug:
            logger.debug("DEBUG level test message")
        return "Logged at multiple levels"


# ============================================================================
# GIVEN STEPS
# ============================================================================

@given('the container manager is initialized')
def initialize_container_manager(context):
    """Initialize container manager with mediator and database"""
    mediator = get_mediator()
    database = get_database()

    # Register test command handler
    mediator.register_handler(
        MultiLevelLoggingCommand,
        lambda: MultiLevelLoggingCommandHandler()
    )

    context['container_manager'] = ContainerManager(mediator, database)
    context['database'] = database


@given(parsers.parse('a player with ID {player_id:d} exists'))
def create_player(context, player_id):
    """Create test player"""
    player_repo = get_player_repository()

    player = Player(
        player_id=None,
        agent_symbol=f"TEST-AGENT-{player_id}",
        token="test-token",
        created_at=datetime.now(),
        last_active=datetime.now(),
        metadata={},
        credits=100000
    )

    created_player = player_repo.create(player)

    # Update to specific ID if needed
    if player_id != created_player.player_id:
        db = get_database()
        with db.transaction() as conn:
            conn.execute("UPDATE players SET player_id = ? WHERE player_id = ?",
                        (player_id, created_player.player_id))

    context['player_id'] = player_id


@given(parsers.parse('a ship "{ship_symbol}" with cargo capacity {capacity:d}'))
@given(parsers.parse('a ship "{ship_symbol}" exists'))
def create_ship(context, ship_symbol, capacity=100):
    """Setup ship data for API mocking"""
    # Store ship data for API mocking
    if 'ships_data' not in context:
        context['ships_data'] = {}

    context['ships_data'][ship_symbol] = {
        'symbol': ship_symbol,
        'nav': {
            'waypointSymbol': 'X1-TEST-A1',
            'systemSymbol': 'X1-TEST',
            'status': 'DOCKED',
            'flightMode': 'CRUISE'
        },
        'fuel': {
            'current': 400,
            'capacity': 400
        },
        'cargo': {
            'capacity': capacity,
            'units': 0,
            'inventory': []
        },
        'frame': {
            'symbol': 'FRAME_PROBE'
        },
        'reactor': {
            'symbol': 'REACTOR_SOLAR_I'
        },
        'engine': {
            'symbol': 'ENGINE_IMPULSE_DRIVE_I',
            'speed': 30
        },
        'modules': [],
        'mounts': []
    }
    context['ship_symbol'] = ship_symbol


@given(parsers.parse('a ship "{ship_symbol}" exists at waypoint "{waypoint_symbol}"'))
def create_ship_at_waypoint(context, ship_symbol, waypoint_symbol):
    """Setup ship data for API mocking at specific waypoint"""
    if 'ships_data' not in context:
        context['ships_data'] = {}

    system_symbol = waypoint_symbol.rsplit('-', 1)[0]

    context['ships_data'][ship_symbol] = {
        'symbol': ship_symbol,
        'nav': {
            'waypointSymbol': waypoint_symbol,
            'systemSymbol': system_symbol,
            'status': 'DOCKED',
            'flightMode': 'CRUISE'
        },
        'fuel': {
            'current': 400,
            'capacity': 400
        },
        'cargo': {
            'capacity': 100,
            'units': 0,
            'inventory': []
        },
        'frame': {
            'symbol': 'FRAME_PROBE'
        },
        'reactor': {
            'symbol': 'REACTOR_SOLAR_I'
        },
        'engine': {
            'symbol': 'ENGINE_IMPULSE_DRIVE_I',
            'speed': 30
        },
        'modules': [],
        'mounts': []
    }
    context['ship_symbol'] = ship_symbol


@given(parsers.parse('a ship "{ship_symbol}" in transit'))
def create_ship_in_transit(context, ship_symbol):
    """Setup ship data for API mocking in IN_TRANSIT state"""
    if 'ships_data' not in context:
        context['ships_data'] = {}

    context['ships_data'][ship_symbol] = {
        'symbol': ship_symbol,
        'nav': {
            'waypointSymbol': 'X1-TEST-A1',
            'systemSymbol': 'X1-TEST',
            'status': 'IN_TRANSIT',
            'flightMode': 'CRUISE'
        },
        'fuel': {
            'current': 400,
            'capacity': 400
        },
        'cargo': {
            'capacity': 100,
            'units': 0,
            'inventory': []
        },
        'frame': {
            'symbol': 'FRAME_PROBE'
        },
        'reactor': {
            'symbol': 'REACTOR_SOLAR_I'
        },
        'engine': {
            'symbol': 'ENGINE_IMPULSE_DRIVE_I',
            'speed': 30
        },
        'modules': [],
        'mounts': []
    }
    context['ship_symbol'] = ship_symbol


@given(parsers.parse('a ship "{ship_symbol}" in orbit without fuel station'))
def create_ship_no_fuel(context, ship_symbol):
    """Setup ship data for API mocking at waypoint without fuel"""
    if 'ships_data' not in context:
        context['ships_data'] = {}

    context['ships_data'][ship_symbol] = {
        'symbol': ship_symbol,
        'nav': {
            'waypointSymbol': 'X1-TEST-A1',
            'systemSymbol': 'X1-TEST',
            'status': 'DOCKED',
            'flightMode': 'CRUISE'
        },
        'fuel': {
            'current': 100,
            'capacity': 400
        },
        'cargo': {
            'capacity': 100,
            'units': 0,
            'inventory': []
        },
        'frame': {
            'symbol': 'FRAME_PROBE'
        },
        'reactor': {
            'symbol': 'REACTOR_SOLAR_I'
        },
        'engine': {
            'symbol': 'ENGINE_IMPULSE_DRIVE_I',
            'speed': 30
        },
        'modules': [],
        'mounts': []
    }
    context['ship_symbol'] = ship_symbol


@given('the database is empty of container logs')
def clear_container_logs(context):
    """Ensure no container logs exist"""
    db = get_database()
    with db.transaction() as conn:
        conn.execute("DELETE FROM container_logs")


# ============================================================================
# WHEN STEPS
# ============================================================================

@when('I create a container for a command that will log errors')
def create_container_with_error_logging(context, event_loop):
    """Create container that runs TestLoggingCommand with error logging"""
    container_manager = context['container_manager']
    player_id = context['player_id']

    container_id = "test-error-container"
    config = {
        'command_type': 'MultiLevelLoggingCommand',
        'params': {
            'player_id': player_id,
            'log_error': True,
            'log_info': False,
            'log_warning': False,
            'log_debug': False
        },
        'iterations': 1
    }

    # Run async operation in event loop
    info = event_loop.run_until_complete(
        container_manager.create_container(
            container_id=container_id,
            player_id=player_id,
            container_type='command',
            config=config,
            restart_policy='no'
        )
    )

    context['container_id'] = container_id
    context['container_info'] = info


@when('I create a container that logs at multiple levels')
def create_container_multi_level_logging(context, event_loop):
    """Create container that logs at all levels"""
    container_manager = context['container_manager']
    player_id = context['player_id']

    container_id = "test-multi-level-container"
    config = {
        'command_type': 'MultiLevelLoggingCommand',
        'params': {
            'player_id': player_id,
            'log_info': True,
            'log_warning': True,
            'log_error': True,
            'log_debug': True
        },
        'iterations': 1
    }

    info = event_loop.run_until_complete(
        container_manager.create_container(
            container_id=container_id,
            player_id=player_id,
            container_type='command',
            config=config,
            restart_policy='no'
        )
    )

    context['container_id'] = container_id
    context['container_info'] = info


@when(parsers.parse('I create a container to run batch contract workflow with {count:d} iteration'))
@when(parsers.parse('I create a container to run batch contract workflow with {count:d} iterations'))
def create_batch_contract_container(context, count, event_loop):
    """Create container that runs batch contract workflow"""
    container_manager = context['container_manager']
    player_id = context['player_id']
    ship_symbol = context['ship_symbol']

    container_id = "test-batch-contract-container"
    config = {
        'command_type': 'application.contracts.commands.batch_contract_workflow.BatchContractWorkflowCommand',
        'params': {
            'ship_symbol': ship_symbol,
            'iterations': count,
            'player_id': player_id
        },
        'iterations': 1  # Container runs the command once
    }

    info = event_loop.run_until_complete(
        container_manager.create_container(
            container_id=container_id,
            player_id=player_id,
            container_type='command',
            config=config,
            restart_policy='no'
        )
    )

    context['container_id'] = container_id
    context['container_info'] = info


@when(parsers.parse('I create a container to navigate ship "{ship_symbol}" to invalid waypoint "{destination}"'))
def create_navigate_container_with_error(context, ship_symbol, destination, event_loop):
    """Create container that will fail during navigation"""
    container_manager = context['container_manager']
    player_id = context['player_id']

    container_id = f"test-navigate-error-{ship_symbol}"
    config = {
        'command_type': 'NavigateShipCommand',
        'params': {
            'ship_symbol': ship_symbol,
            'destination_symbol': destination,
            'player_id': player_id
        },
        'iterations': 1
    }

    info = event_loop.run_until_complete(
        container_manager.create_container(
            container_id=container_id,
            player_id=player_id,
            container_type='command',
            config=config,
            restart_policy='no'
        )
    )

    context['container_id'] = container_id
    context['container_info'] = info


@when(parsers.parse('I create a container to dock ship "{ship_symbol}"'))
def create_dock_container(context, ship_symbol, event_loop):
    """Create container to dock ship (may fail if in wrong state)"""
    container_manager = context['container_manager']
    player_id = context['player_id']

    container_id = f"test-dock-{ship_symbol}"
    config = {
        'command_type': 'DockShipCommand',
        'params': {
            'ship_symbol': ship_symbol,
            'player_id': player_id
        },
        'iterations': 1
    }

    info = event_loop.run_until_complete(
        container_manager.create_container(
            container_id=container_id,
            player_id=player_id,
            container_type='command',
            config=config,
            restart_policy='no'
        )
    )

    context['container_id'] = container_id
    context['container_info'] = info


@when(parsers.parse('I create a container to orbit ship "{ship_symbol}"'))
def create_orbit_container(context, ship_symbol, event_loop):
    """Create container to orbit ship (may fail if in wrong state)"""
    container_manager = context['container_manager']
    player_id = context['player_id']

    container_id = f"test-orbit-{ship_symbol}"
    config = {
        'command_type': 'OrbitShipCommand',
        'params': {
            'ship_symbol': ship_symbol,
            'player_id': player_id
        },
        'iterations': 1
    }

    info = event_loop.run_until_complete(
        container_manager.create_container(
            container_id=container_id,
            player_id=player_id,
            container_type='command',
            config=config,
            restart_policy='no'
        )
    )

    context['container_id'] = container_id
    context['container_info'] = info


@when(parsers.parse('I create a container to refuel ship "{ship_symbol}"'))
def create_refuel_container(context, ship_symbol, event_loop):
    """Create container to refuel ship (may fail if no fuel available)"""
    container_manager = context['container_manager']
    player_id = context['player_id']

    container_id = f"test-refuel-{ship_symbol}"
    config = {
        'command_type': 'RefuelShipCommand',
        'params': {
            'ship_symbol': ship_symbol,
            'player_id': player_id
        },
        'iterations': 1
    }

    info = event_loop.run_until_complete(
        container_manager.create_container(
            container_id=container_id,
            player_id=player_id,
            container_type='command',
            config=config,
            restart_policy='no'
        )
    )

    context['container_id'] = container_id
    context['container_info'] = info


@when('I create a container to run scout tour with invalid markets')
def create_scout_tour_container_with_error(context, event_loop):
    """Create container that will fail during scout tour"""
    container_manager = context['container_manager']
    player_id = context['player_id']
    ship_symbol = context['ship_symbol']

    container_id = f"test-scout-tour-error"
    config = {
        'command_type': 'application.scouting.commands.scout_tour.ScoutTourCommand',
        'params': {
            'ship_symbol': ship_symbol,
            'player_id': player_id,
            'system': 'X1-TEST',
            'markets': ['X1-TEST-INVALID'],  # Invalid market
            'return_to_start': False
        },
        'iterations': 1
    }

    info = event_loop.run_until_complete(
        container_manager.create_container(
            container_id=container_id,
            player_id=player_id,
            container_type='command',
            config=config,
            restart_policy='no'
        )
    )

    context['container_id'] = container_id
    context['container_info'] = info


@when('I wait for the container to execute')
@when('I wait for the container to complete')
@when('I wait for the container to fail')
def wait_for_container(context, event_loop):
    """Wait for container task to complete"""
    container_info = context['container_info']

    if container_info.task:
        try:
            event_loop.run_until_complete(asyncio.wait_for(container_info.task, timeout=5.0))
        except asyncio.TimeoutError:
            pytest.fail("Container did not complete within timeout")
        except Exception as e:
            # Container might fail, which is expected in some tests
            context['container_error'] = str(e)


# ============================================================================
# THEN STEPS
# ============================================================================

@then('the container logs should contain ERROR level messages')
def verify_error_logs_exist(context):
    """Verify ERROR level logs exist in database"""
    db = context['database']
    container_id = context['container_id']
    player_id = context['player_id']

    logs = db.get_container_logs(
        container_id=container_id,
        player_id=player_id,
        level='ERROR',
        limit=100
    )

    assert len(logs) > 0, "Expected ERROR level logs but found none"
    context['error_logs'] = logs


@then('the errors should be retrievable via get_container_logs')
def verify_logs_retrievable(context):
    """Verify logs can be retrieved"""
    db = context['database']
    container_id = context['container_id']
    player_id = context['player_id']

    logs = db.get_container_logs(
        container_id=container_id,
        player_id=player_id,
        limit=100
    )

    assert len(logs) > 0, "Expected logs but found none"

    # Verify log structure
    for log in logs:
        assert 'log_id' in log
        assert 'container_id' in log
        assert 'player_id' in log
        assert 'timestamp' in log
        assert 'level' in log
        assert 'message' in log


@then(parsers.parse('the container logs should mention "{expected_text}"'))
def verify_logs_contain_text(context, expected_text):
    """Verify logs contain expected text (supports OR with ' or ')"""
    db = context['database']
    container_id = context['container_id']
    player_id = context['player_id']

    all_logs = db.get_container_logs(
        container_id=container_id,
        player_id=player_id,
        limit=100
    )

    # Support "word1" or "word2" or "word3" syntax
    search_terms = [term.strip(' "\'') for term in expected_text.split(' or ')]

    found = False
    for log in all_logs:
        message_lower = log['message'].lower()
        for term in search_terms:
            if term.lower() in message_lower:
                found = True
                break
        if found:
            break

    if not found:
        # Debug output
        print(f"\nSearching for any of: {search_terms}")
        print(f"In {len(all_logs)} log messages:")
        for log in all_logs:
            print(f"  [{log['level']}] {log['message']}")

    assert found, f"Expected log message containing any of {search_terms} but found none in {len(all_logs)} logs"


@then('the container logs should contain INFO messages')
def verify_info_logs(context):
    """Verify INFO logs exist"""
    db = context['database']
    container_id = context['container_id']
    player_id = context['player_id']

    logs = db.get_container_logs(
        container_id=container_id,
        player_id=player_id,
        level='INFO',
        limit=100
    )

    assert len(logs) > 0, "Expected INFO level logs but found none"


@then('the container logs should contain WARNING messages')
def verify_warning_logs(context):
    """Verify WARNING logs exist"""
    db = context['database']
    container_id = context['container_id']
    player_id = context['player_id']

    logs = db.get_container_logs(
        container_id=container_id,
        player_id=player_id,
        level='WARNING',
        limit=100
    )

    assert len(logs) > 0, "Expected WARNING level logs but found none"


@then('the container logs should contain DEBUG messages')
def verify_debug_logs(context):
    """Verify DEBUG logs exist"""
    db = context['database']
    container_id = context['container_id']
    player_id = context['player_id']

    logs = db.get_container_logs(
        container_id=container_id,
        player_id=player_id,
        level='DEBUG',
        limit=100
    )

    assert len(logs) > 0, "Expected DEBUG level logs but found none"


# ============================================================================
# JSON SERIALIZATION TEST STEPS
# ============================================================================

@when(parsers.parse('I create a test container with ID "{container_id}"'))
def create_test_container_with_id(context, container_id):
    """Create minimal test container entry in database"""
    db = context['database']
    player_id = context['player_id']

    # Create minimal container entry directly in database
    with db.transaction() as conn:
        conn.execute("""
            INSERT INTO containers (container_id, player_id, container_type, status, started_at)
            VALUES (?, ?, 'test', 'STARTING', datetime('now'))
        """, (container_id, player_id))

    context['test_container_id'] = container_id
    context['added_logs'] = []


@when(parsers.parse('I add a log with message containing double quotes: {message}'))
@when(parsers.parse('I add a log with message containing newlines: {message}'))
@when(parsers.parse('I add a log with message containing backslashes: {message}'))
@when(parsers.parse('I add a log with message containing unicode: {message}'))
@when(parsers.parse('I add a log with message containing JSON-like content: {message}'))
@when(parsers.parse('I add a log with message containing all special characters: {message}'))
def add_log_with_special_characters(context, message):
    """Add log with special characters directly to database"""
    db = context['database']
    container_id = context['test_container_id']
    player_id = context['player_id']

    # Remove surrounding quotes from message if present
    if message.startswith("'") and message.endswith("'"):
        message = message[1:-1]
    elif message.startswith('"') and message.endswith('"'):
        message = message[1:-1]

    # Unescape literal \n, \t, etc. to actual characters
    message = message.replace('\\n', '\n').replace('\\t', '\t').replace('\\r', '\r')
    message = message.replace('\\b', '\b').replace('\\f', '\f').replace('\\\\', '\\')

    # Add log to database
    with db.transaction() as conn:
        conn.execute("""
            INSERT INTO container_logs (container_id, player_id, timestamp, level, message)
            VALUES (?, ?, datetime('now'), 'INFO', ?)
        """, (container_id, player_id, message))

    context['added_logs'].append(message)


@when(parsers.parse('I add {count:d} logs with varying content including special characters'))
def add_multiple_logs_with_special_chars(context, count):
    """Add multiple logs with varying special characters"""
    db = context['database']
    container_id = context['test_container_id']
    player_id = context['player_id']

    test_messages = []
    for i in range(count):
        # Generate varied messages with special characters
        if i % 5 == 0:
            msg = f'Log {i}: Ship "TEST-{i}" navigating'
        elif i % 5 == 1:
            msg = f'Log {i}: Path\\nLine 1\\nLine 2'
        elif i % 5 == 2:
            msg = f'Log {i}: C:\\\\Users\\\\test\\\\file{i}.txt'
        elif i % 5 == 3:
            msg = f'Log {i}: Status ✅ {{"index": {i}}}'
        else:
            msg = f'Log {i}: Complex\\ttab\\nNewline"Quote\\Backslash'

        # Unescape for actual insertion
        msg = msg.replace('\\n', '\n').replace('\\t', '\t').replace('\\\\', '\\')
        test_messages.append(msg)

    # Batch insert
    with db.transaction() as conn:
        for msg in test_messages:
            conn.execute("""
                INSERT INTO container_logs (container_id, player_id, timestamp, level, message)
                VALUES (?, ?, datetime('now'), 'INFO', ?)
            """, (container_id, player_id, msg))

    context['added_logs'] = test_messages


@when(parsers.parse('I call daemon_inspect for container "{container_id}"'))
def call_daemon_inspect(context, container_id):
    """Simulate daemon_inspect by getting logs and serializing to JSON"""
    import json
    db = context['database']
    player_id = context['player_id']

    # Get logs from database (same as _inspect_container does)
    logs = db.get_container_logs(
        container_id=container_id,
        player_id=player_id,
        limit=200  # Use higher limit to capture all test logs
    )

    # Build result dict similar to _inspect_container
    result = {
        "container_id": container_id,
        "player_id": player_id,
        "type": "test",
        "status": "STARTING",
        "iteration": 0,
        "restart_count": 0,
        "started_at": "2024-01-01T00:00:00",
        "stopped_at": None,
        "exit_code": None,
        "logs": logs
    }

    # Serialize to JSON to test serialization (this is where bug would occur)
    try:
        json_response = json.dumps(result)
        context['daemon_inspect_response'] = json_response
        context['daemon_inspect_parsed'] = json.loads(json_response)
    except Exception as e:
        context['daemon_inspect_error'] = str(e)
        context['daemon_inspect_response'] = None
        context['daemon_inspect_exception'] = e


@when(parsers.parse('I call daemon_logs for container "{container_id}" and player {player_id:d}'))
def call_daemon_logs(context, container_id, player_id):
    """Simulate daemon_logs by getting logs and serializing to JSON"""
    import json
    db = context['database']

    # Get logs from database
    logs = db.get_container_logs(
        container_id=container_id,
        player_id=player_id,
        limit=100
    )

    # Build result dict similar to _get_container_logs
    result = {
        "container_id": container_id,
        "player_id": player_id,
        "logs": logs
    }

    # Serialize to JSON
    try:
        json_response = json.dumps(result)
        context['daemon_logs_response'] = json_response
        context['daemon_logs_parsed'] = json.loads(json_response)
    except Exception as e:
        context['daemon_logs_error'] = str(e)
        context['daemon_logs_response'] = None
        context['daemon_logs_exception'] = e


@then('the daemon_inspect response should be valid JSON')
def verify_daemon_inspect_valid_json(context):
    """Verify daemon_inspect returned valid JSON"""
    assert 'daemon_inspect_error' not in context, \
        f"JSON serialization failed: {context.get('daemon_inspect_error')}"
    assert context['daemon_inspect_response'] is not None, \
        "daemon_inspect response was None"
    assert context['daemon_inspect_parsed'] is not None, \
        "Failed to parse daemon_inspect JSON response"


@then('the daemon_logs response should be valid JSON')
def verify_daemon_logs_valid_json(context):
    """Verify daemon_logs returned valid JSON"""
    assert 'daemon_logs_error' not in context, \
        f"JSON serialization failed: {context.get('daemon_logs_error')}"
    assert context['daemon_logs_response'] is not None, \
        "daemon_logs response was None"
    assert context['daemon_logs_parsed'] is not None, \
        "Failed to parse daemon_logs JSON response"


@then(parsers.parse('the daemon_inspect response should contain all {expected_count:d} log messages'))
def verify_daemon_inspect_log_count(context, expected_count):
    """Verify daemon_inspect response contains expected number of logs"""
    parsed = context['daemon_inspect_parsed']
    logs = parsed.get('logs', [])
    assert len(logs) == expected_count, \
        f"Expected {expected_count} logs but got {len(logs)}"


@then(parsers.parse('the daemon_logs response should contain all {expected_count:d} log messages'))
def verify_daemon_logs_log_count(context, expected_count):
    """Verify daemon_logs response contains expected number of logs"""
    parsed = context['daemon_logs_parsed']
    logs = parsed.get('logs', [])
    assert len(logs) == expected_count, \
        f"Expected {expected_count} logs but got {len(logs)}"


@then('the log messages should preserve special characters correctly')
def verify_special_chars_preserved(context):
    """Verify special characters in logs are correctly preserved"""
    parsed = context['daemon_inspect_parsed']
    logs = parsed.get('logs', [])
    added_logs = context['added_logs']

    # Extract messages from response
    response_messages = [log['message'] for log in logs]

    # Verify all added messages appear in response
    for expected_msg in added_logs:
        assert expected_msg in response_messages, \
            f"Expected message not found in response: {expected_msg!r}"


@then('the daemon_logs response should contain the log message with special characters preserved')
def verify_daemon_logs_special_chars(context):
    """Verify daemon_logs preserves special characters"""
    parsed = context['daemon_logs_parsed']
    logs = parsed.get('logs', [])
    added_logs = context['added_logs']

    response_messages = [log['message'] for log in logs]

    for expected_msg in added_logs:
        assert expected_msg in response_messages, \
            f"Expected message not found in response: {expected_msg!r}"


@then(parsers.parse('the total response size should exceed {min_size:d} characters'))
def verify_response_size(context, min_size):
    """Verify response size exceeds threshold"""
    response = context['daemon_inspect_response']
    actual_size = len(response)
    assert actual_size > min_size, \
        f"Expected response size > {min_size} but got {actual_size}"


# ============================================================================
# CLI OUTPUT TEST STEPS
# ============================================================================

@when(parsers.parse('I call daemon_inspect CLI command for container "{container_id}"'))
def call_daemon_inspect_cli(context, container_id):
    """Call daemon_inspect CLI command function directly with mocked client"""
    import json
    from io import StringIO
    import sys
    from unittest.mock import Mock
    from argparse import Namespace
    from adapters.primary.cli.daemon_cli import daemon_inspect_command

    # Get logs from database to build mock response
    db = context['database']
    player_id = context['player_id']
    logs = db.get_container_logs(
        container_id=container_id,
        player_id=player_id,
        limit=200
    )

    # Build mock response matching daemon server format
    mock_result = {
        "container_id": container_id,
        "player_id": player_id,
        "type": "test",
        "status": "STARTING",
        "iteration": 0,
        "restart_count": 0,
        "started_at": "2024-01-01T00:00:00",
        "stopped_at": None,
        "exit_code": None,
        "logs": logs
    }

    # Mock DaemonClient
    mock_client = Mock()
    mock_client.inspect_container.return_value = mock_result

    # Capture stdout
    captured_output = StringIO()
    original_stdout = sys.stdout
    sys.stdout = captured_output

    try:
        # Patch DaemonClient in the module
        import adapters.primary.cli.daemon_cli as daemon_cli_module
        original_client_class = daemon_cli_module.DaemonClient
        daemon_cli_module.DaemonClient = lambda: mock_client

        # Call command with --json flag
        args = Namespace(container_id=container_id, json=True)
        returncode = daemon_inspect_command(args)

        # Restore
        daemon_cli_module.DaemonClient = original_client_class
    finally:
        sys.stdout = original_stdout

    context['cli_stdout'] = captured_output.getvalue()
    context['cli_returncode'] = returncode


@when(parsers.parse('I call daemon_logs CLI command for container "{container_id}" and player {player_id:d}'))
def call_daemon_logs_cli(context, container_id, player_id):
    """Call daemon_logs CLI command function directly with mocked client"""
    import json
    from io import StringIO
    import sys
    from unittest.mock import Mock
    from argparse import Namespace
    from adapters.primary.cli.daemon_cli import daemon_logs_command

    # Get logs from database
    db = context['database']
    logs = db.get_container_logs(
        container_id=container_id,
        player_id=player_id,
        limit=100
    )

    # Build mock response
    mock_result = {
        "container_id": container_id,
        "player_id": player_id,
        "logs": logs
    }

    # Mock DaemonClient
    mock_client = Mock()
    mock_client.get_container_logs.return_value = mock_result

    # Capture stdout
    captured_output = StringIO()
    original_stdout = sys.stdout
    sys.stdout = captured_output

    try:
        # Patch DaemonClient in the module
        import adapters.primary.cli.daemon_cli as daemon_cli_module
        original_client_class = daemon_cli_module.DaemonClient
        daemon_cli_module.DaemonClient = lambda: mock_client

        # Call command with --json flag
        args = Namespace(container_id=container_id, player_id=player_id, limit=100, level=None, json=True)
        returncode = daemon_logs_command(args)

        # Restore
        daemon_cli_module.DaemonClient = original_client_class
    finally:
        sys.stdout = original_stdout

    context['cli_stdout'] = captured_output.getvalue()
    context['cli_returncode'] = returncode


@then('the CLI output should be valid JSON')
def verify_cli_output_is_json(context):
    """Verify CLI output is valid JSON"""
    import json

    stdout = context['cli_stdout']

    # Debug output if test fails
    if not stdout.strip():
        print(f"CLI stdout is empty. stderr: {context['cli_stderr']}")

    try:
        parsed = json.loads(stdout)
        context['cli_json_output'] = parsed
    except json.JSONDecodeError as e:
        pytest.fail(f"CLI output is not valid JSON: {e}\nOutput: {stdout[:500]}")


@then('the CLI JSON output should contain container metadata')
def verify_cli_json_has_metadata(context):
    """Verify CLI JSON contains container metadata fields"""
    parsed = context['cli_json_output']

    required_fields = ['container_id', 'player_id', 'type', 'status']
    for field in required_fields:
        assert field in parsed, f"Missing required field: {field}"


@then('the CLI JSON output should contain the logs with special characters preserved')
def verify_cli_json_logs_preserved(context):
    """Verify logs in CLI JSON output preserve special characters"""
    parsed = context['cli_json_output']
    added_logs = context['added_logs']

    assert 'logs' in parsed, "CLI JSON output missing 'logs' field"
    logs = parsed['logs']

    response_messages = [log['message'] for log in logs]

    for expected_msg in added_logs:
        assert expected_msg in response_messages, \
            f"Expected message not found in CLI output: {expected_msg!r}"


@then('the CLI JSON output should contain the log with special characters')
def verify_cli_json_log_special_chars(context):
    """Verify log in CLI JSON output preserves special characters"""
    parsed = context['cli_json_output']
    added_logs = context['added_logs']

    assert 'logs' in parsed, "CLI JSON output missing 'logs' field"
    logs = parsed['logs']

    response_messages = [log['message'] for log in logs]

    for expected_msg in added_logs:
        assert expected_msg in response_messages, \
            f"Expected message not found in CLI output: {expected_msg!r}"
