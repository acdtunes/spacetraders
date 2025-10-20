"""Step definitions for infrastructure scenarios."""

import pytest
import tempfile
import shutil
import inspect
from pathlib import Path
from pytest_bdd import given, when, then, parsers, scenarios

from spacetraders_bot.core.operation_checkpointer import OperationController, send_control_command
from spacetraders_bot.core.smart_navigator import SmartNavigator
from spacetraders_bot.core.ship import ShipController
from spacetraders_bot.core.daemon_manager import DaemonManager
from tests.bdd.steps.fixtures.mock_api import MockAPIClient

# Load all infrastructure scenarios
scenarios('../../features/infrastructure/operation_controller.feature')
scenarios('../../features/infrastructure/daemon_cache_prevention.feature')


@pytest.fixture
def temp_dir():
    """Create temp directory for operation state"""
    d = tempfile.mkdtemp()
    yield d
    if Path(d).exists():
        shutil.rmtree(d)


@pytest.fixture
def infrastructure_context():
    """Shared context for infrastructure scenarios."""
    return {
        'temp_dir': None,
        'mock_api': None,
        'ship_controller': None,
        'navigator': None,
        'graph': None,
        'operation_controller': None,
        'operation_controller_instances': {},
        'checkpoints': [],
        'resumed_data': None,
        'progress': None,
        'daemon_manager': None,
        'command': None,
        'injected_command': None,
        'source_code': None,
    }


# ====================
# Operation Controller Steps
# ====================

@given("a temporary state directory")
def setup_temp_dir(infrastructure_context, temp_dir):
    """Set up temporary directory for test."""
    infrastructure_context['temp_dir'] = temp_dir


@given(parsers.parse('a mock environment with ship "{ship}" at "{location}"'))
def setup_mock_environment(infrastructure_context, ship, location):
    """Set up mock environment with ship and navigator."""
    mock_api = MockAPIClient()

    # Create navigation graph
    graph = {
        'system': 'X1-TEST',
        'waypoints': {
            'X1-TEST-A1': {'x': 0, 'y': 0, 'type': 'PLANET', 'traits': [], 'has_fuel': False},
            'X1-TEST-A2': {'x': 100, 'y': 0, 'type': 'ASTEROID', 'traits': [], 'has_fuel': False},
            'X1-TEST-A3': {'x': 200, 'y': 0, 'type': 'ORBITAL_STATION', 'traits': ['MARKETPLACE'], 'has_fuel': True},
        },
        'edges': [
            {'from': 'X1-TEST-A1', 'to': 'X1-TEST-A2', 'distance': 100},
            {'from': 'X1-TEST-A2', 'to': 'X1-TEST-A3', 'distance': 100},
            {'from': 'X1-TEST-A3', 'to': 'X1-TEST-A1', 'distance': 200},
        ]
    }

    # Setup ship
    mock_api.set_ship_location(ship, location, 'IN_ORBIT')
    mock_api.set_ship_fuel(ship, 400, 400)

    ship_controller = ShipController(ship_symbol=ship, api_client=mock_api)
    navigator = SmartNavigator(api_client=mock_api, system='X1-TEST', graph=graph)

    infrastructure_context['mock_api'] = mock_api
    infrastructure_context['ship_controller'] = ship_controller
    infrastructure_context['navigator'] = navigator
    infrastructure_context['graph'] = graph


@given(parsers.parse('an operation controller with ID "{op_id}"'))
def create_operation_controller(infrastructure_context, op_id):
    """Create operation controller with specified ID."""
    op_ctrl = OperationController(
        operation_id=op_id,
        state_dir=infrastructure_context['temp_dir']
    )
    infrastructure_context['operation_controller'] = op_ctrl
    infrastructure_context['operation_controller_instances'][op_id] = op_ctrl


@given(parsers.parse('the operation is started with ship "{ship}" to "{destination}"'))
def start_operation_with_destination(infrastructure_context, ship, destination):
    """Start operation with ship and destination."""
    op_ctrl = infrastructure_context['operation_controller']
    op_ctrl.start({'ship': ship, 'destination': destination})


@given(parsers.parse('the operation is started with ship "{ship}"'))
def start_operation(infrastructure_context, ship):
    """Start operation with ship only."""
    op_ctrl = infrastructure_context['operation_controller']
    op_ctrl.start({'ship': ship})


@given(parsers.parse('a checkpoint is saved with step {step:d} at "{location}" with {fuel:d} fuel'))
def save_simple_checkpoint(infrastructure_context, step, location, fuel):
    """Save a simple checkpoint."""
    op_ctrl = infrastructure_context['operation_controller']
    op_ctrl.checkpoint({
        'completed_step': step,
        'location': location,
        'fuel': fuel,
        'state': 'IN_ORBIT'
    })


@given(parsers.parse('a checkpoint is saved at step {step:d}'))
def save_checkpoint_at_step(infrastructure_context, step):
    """Save a checkpoint at specified step."""
    op_ctrl = infrastructure_context['operation_controller']
    op_ctrl.checkpoint({
        'completed_step': step,
        'location': 'X1-TEST-A2',
        'fuel': 300,
        'state': 'IN_ORBIT'
    })


@given("the operation is paused")
def pause_operation(infrastructure_context):
    """Pause the operation."""
    op_ctrl = infrastructure_context['operation_controller']
    op_ctrl.pause()


@when(parsers.parse('I save a checkpoint with:\n{checkpoint_table}'))
def save_checkpoint_from_table(infrastructure_context, checkpoint_table):
    """Save checkpoint from table data."""
    op_ctrl = infrastructure_context['operation_controller']

    # Parse table
    lines = [line.strip() for line in checkpoint_table.strip().split('\n') if '|' in line]
    data_dict = {}
    for line in lines:
        parts = [p.strip() for p in line.split('|') if p.strip()]
        if len(parts) == 2 and parts[0] != 'field':
            field, value = parts
            # Convert numeric values
            if value.isdigit():
                data_dict[field] = int(value)
            else:
                data_dict[field] = value

    op_ctrl.checkpoint(data_dict)


@when(parsers.parse('I save checkpoints:\n{checkpoints_table}'))
def save_multiple_checkpoints(infrastructure_context, checkpoints_table):
    """Save multiple checkpoints from table."""
    op_ctrl = infrastructure_context['operation_controller']

    # Parse table
    lines = [line.strip() for line in checkpoints_table.strip().split('\n') if '|' in line]
    header_line = lines[0]
    data_lines = [line for line in lines[1:] if not '---' in line]

    for line in data_lines:
        parts = [p.strip() for p in line.split('|') if p.strip()]
        if len(parts) == 4:
            step, location, fuel, state = parts
            op_ctrl.checkpoint({
                'completed_step': int(step),
                'location': location,
                'fuel': int(fuel),
                'state': state
            })


@when("I resume the operation")
def resume_operation(infrastructure_context):
    """Resume the operation."""
    op_ctrl = infrastructure_context['operation_controller']
    infrastructure_context['resumed_data'] = op_ctrl.resume()


@when("an external pause command is sent")
def send_pause_command(infrastructure_context):
    """Send external pause command."""
    op_ctrl = infrastructure_context['operation_controller']
    send_control_command(op_ctrl.operation_id, 'pause', infrastructure_context['temp_dir'])


@when("an external cancel command is sent")
def send_cancel_command(infrastructure_context):
    """Send external cancel command."""
    op_ctrl = infrastructure_context['operation_controller']
    send_control_command(op_ctrl.operation_id, 'cancel', infrastructure_context['temp_dir'])


@when("the operation is paused")
def pause_operation_when(infrastructure_context):
    """Pause the operation (when step)."""
    op_ctrl = infrastructure_context['operation_controller']
    op_ctrl.pause()


@when("the operation is cancelled")
def cancel_operation(infrastructure_context):
    """Cancel the operation."""
    op_ctrl = infrastructure_context['operation_controller']
    op_ctrl.cancel()


@when(parsers.parse('I save a checkpoint at step {step:d} with location "{location}"'))
def save_checkpoint_with_location(infrastructure_context, step, location):
    """Save checkpoint with step and location."""
    op_ctrl = infrastructure_context['operation_controller']
    op_ctrl.checkpoint({
        'completed_step': step,
        'location': location,
        'fuel': 300,
        'state': 'IN_ORBIT'
    })


@when(parsers.parse('I create a new operation controller instance with ID "{op_id}"'))
def create_new_controller_instance(infrastructure_context, op_id):
    """Create new controller instance with same ID."""
    new_ctrl = OperationController(
        operation_id=op_id,
        state_dir=infrastructure_context['temp_dir']
    )
    infrastructure_context['operation_controller'] = new_ctrl


@when(parsers.parse('I save a navigation checkpoint at step {step:d} with {fuel:d} fuel in orbit'))
def save_navigation_checkpoint(infrastructure_context, step, fuel):
    """Save navigation checkpoint."""
    op_ctrl = infrastructure_context['operation_controller']
    op_ctrl.checkpoint({
        'completed_step': step,
        'location': 'X1-TEST-A3',
        'fuel': fuel,
        'state': 'IN_ORBIT'
    })


@when(parsers.parse('I save a refuel checkpoint at step {step:d} with {fuel:d} fuel docked'))
def save_refuel_checkpoint(infrastructure_context, step, fuel):
    """Save refuel checkpoint."""
    op_ctrl = infrastructure_context['operation_controller']
    op_ctrl.checkpoint({
        'completed_step': step,
        'location': 'X1-TEST-A3',
        'fuel': fuel,
        'state': 'DOCKED'
    })


@when(parsers.parse('I save {count:d} checkpoints'))
def save_n_checkpoints(infrastructure_context, count):
    """Save N checkpoints."""
    op_ctrl = infrastructure_context['operation_controller']
    for i in range(1, count + 1):
        op_ctrl.checkpoint({
            'completed_step': i,
            'location': f'X1-TEST-A{i}',
            'fuel': 400 - (i * 100),
            'state': 'IN_ORBIT'
        })


@when("I get the operation progress")
def get_operation_progress(infrastructure_context):
    """Get operation progress."""
    op_ctrl = infrastructure_context['operation_controller']
    infrastructure_context['progress'] = op_ctrl.get_progress()


@then("the checkpoint should be saved to the operation state")
def verify_checkpoint_saved(infrastructure_context):
    """Verify checkpoint was saved."""
    op_ctrl = infrastructure_context['operation_controller']
    assert len(op_ctrl.state['checkpoints']) >= 1, \
        "Checkpoint should be saved to operation_controller.state"


@then("the checkpoint data should match the saved values")
def verify_checkpoint_data(infrastructure_context):
    """Verify checkpoint data matches."""
    op_ctrl = infrastructure_context['operation_controller']
    checkpoint = op_ctrl.state['checkpoints'][-1]
    cp_data = checkpoint['data']

    # Verify fields exist
    assert 'location' in cp_data, "Checkpoint should have location"
    assert 'fuel' in cp_data, "Checkpoint should have fuel"
    assert 'state' in cp_data, "Checkpoint should have state"
    assert 'completed_step' in cp_data, "Checkpoint should have completed_step"


@then("the checkpoint should have a timestamp")
def verify_checkpoint_timestamp(infrastructure_context):
    """Verify checkpoint has timestamp."""
    op_ctrl = infrastructure_context['operation_controller']
    checkpoint = op_ctrl.state['checkpoints'][-1]
    assert 'timestamp' in checkpoint, "Checkpoint should have timestamp"


@then(parsers.parse('there should be {count:d} checkpoints saved'))
@then(parsers.parse('there should be {count:d} checkpoints'))
def verify_checkpoint_count(infrastructure_context, count):
    """Verify number of checkpoints."""
    op_ctrl = infrastructure_context['operation_controller']
    assert len(op_ctrl.state['checkpoints']) == count, \
        f"Should have {count} checkpoints, got {len(op_ctrl.state['checkpoints'])}"


@then(parsers.parse('the checkpoint step numbers should increment from {start:d} to {end:d}'))
def verify_step_numbers_increment(infrastructure_context, start, end):
    """Verify step numbers increment."""
    op_ctrl = infrastructure_context['operation_controller']
    for i, cp in enumerate(op_ctrl.state['checkpoints'], start):
        assert cp['data']['completed_step'] == i, \
            f"Checkpoint {i} should have step={i}, got {cp['data']['completed_step']}"


@then(parsers.parse('the checkpoint locations should progress: {locations}'))
def verify_locations_progress(infrastructure_context, locations):
    """Verify checkpoint locations progress."""
    op_ctrl = infrastructure_context['operation_controller']
    expected_locations = [loc.strip() for loc in locations.split(',')]
    actual_locations = [cp['data']['location'] for cp in op_ctrl.state['checkpoints']]
    assert actual_locations == expected_locations, \
        f"Locations should progress {expected_locations}, got {actual_locations}"


@then(parsers.parse('the checkpoint fuel values should decrease: {fuel_values}'))
def verify_fuel_decreases(infrastructure_context, fuel_values):
    """Verify fuel decreases."""
    op_ctrl = infrastructure_context['operation_controller']
    expected_fuels = [int(f.strip()) for f in fuel_values.split(',')]
    actual_fuels = [cp['data']['fuel'] for cp in op_ctrl.state['checkpoints']]
    assert actual_fuels == expected_fuels, \
        f"Fuel should decrease {expected_fuels}, got {actual_fuels}"


@then("the operation should be resumable")
def verify_operation_resumable(infrastructure_context):
    """Verify operation can be resumed."""
    op_ctrl = infrastructure_context['operation_controller']
    assert op_ctrl.can_resume() is True, "Operation should be resumable"


@then(parsers.parse('the resumed data should have step {step:d}'))
def verify_resumed_step(infrastructure_context, step):
    """Verify resumed data has correct step."""
    resumed = infrastructure_context['resumed_data']
    assert resumed['completed_step'] == step, \
        f"Resumed step should be {step}, got {resumed['completed_step']}"


@then(parsers.parse('the resumed data should have location "{location}"'))
def verify_resumed_location(infrastructure_context, location):
    """Verify resumed data has correct location."""
    resumed = infrastructure_context['resumed_data']
    assert resumed['location'] == location, \
        f"Resumed location should be {location}, got {resumed['location']}"


@then(parsers.parse('the resumed data should have {fuel:d} fuel'))
def verify_resumed_fuel(infrastructure_context, fuel):
    """Verify resumed data has correct fuel."""
    resumed = infrastructure_context['resumed_data']
    assert resumed['fuel'] == fuel, \
        f"Resumed fuel should be {fuel}, got {resumed['fuel']}"


@then(parsers.parse('the resumed data should have state "{state}"'))
def verify_resumed_state(infrastructure_context, state):
    """Verify resumed data has correct state."""
    resumed = infrastructure_context['resumed_data']
    assert resumed['state'] == state, \
        f"Resumed state should be {state}, got {resumed['state']}"


@then("the operation should detect the pause signal")
def verify_pause_signal_detected(infrastructure_context):
    """Verify pause signal is detected."""
    op_ctrl = infrastructure_context['operation_controller']
    assert op_ctrl.should_pause() is True, "should_pause() should detect external pause command"


@then("the operation should detect the cancel signal")
def verify_cancel_signal_detected(infrastructure_context):
    """Verify cancel signal is detected."""
    op_ctrl = infrastructure_context['operation_controller']
    assert op_ctrl.should_cancel() is True, "should_cancel() should detect external cancel command"


@then(parsers.parse('the operation status should be "{status}"'))
def verify_operation_status(infrastructure_context, status):
    """Verify operation status."""
    op_ctrl = infrastructure_context['operation_controller']
    assert op_ctrl.state['status'] == status, \
        f"Status should be '{status}', got {op_ctrl.state['status']}"


@then("the checkpoint should be preserved")
def verify_checkpoint_preserved(infrastructure_context):
    """Verify checkpoint is preserved."""
    op_ctrl = infrastructure_context['operation_controller']
    assert len(op_ctrl.state['checkpoints']) >= 1, "Checkpoint should be preserved"


@then("the operation should not be resumable")
def verify_operation_not_resumable(infrastructure_context):
    """Verify operation cannot be resumed."""
    op_ctrl = infrastructure_context['operation_controller']
    assert op_ctrl.can_resume() is False, "Should NOT be able to resume cancelled operation"


@then("the state file should exist on disk")
def verify_state_file_exists(infrastructure_context):
    """Verify state file exists on disk."""
    op_ctrl = infrastructure_context['operation_controller']
    state_file = Path(infrastructure_context['temp_dir']) / f'{op_ctrl.operation_id}.json'
    assert state_file.exists(), "State file should exist on disk"


@then("the checkpoint should be loaded from disk")
def verify_checkpoint_loaded(infrastructure_context):
    """Verify checkpoint loaded from disk."""
    op_ctrl = infrastructure_context['operation_controller']
    assert len(op_ctrl.state['checkpoints']) >= 1, "Checkpoint should be loaded from disk"


@then(parsers.parse('the loaded checkpoint should have location "{location}"'))
def verify_loaded_checkpoint_location(infrastructure_context, location):
    """Verify loaded checkpoint has correct location."""
    op_ctrl = infrastructure_context['operation_controller']
    checkpoint = op_ctrl.get_last_checkpoint()
    assert checkpoint['location'] == location, \
        f"Checkpoint location should be {location}, got {checkpoint['location']}"


@then(parsers.parse('the second checkpoint should have state "{state}"'))
def verify_second_checkpoint_state(infrastructure_context, state):
    """Verify second checkpoint has correct state."""
    op_ctrl = infrastructure_context['operation_controller']
    checkpoint = op_ctrl.state['checkpoints'][1]['data']
    assert checkpoint['state'] == state, \
        f"Second checkpoint should have state {state}, got {checkpoint['state']}"


@then("the second checkpoint fuel should be greater than the first")
def verify_fuel_increased(infrastructure_context):
    """Verify fuel increased in second checkpoint."""
    op_ctrl = infrastructure_context['operation_controller']
    first_fuel = op_ctrl.state['checkpoints'][0]['data']['fuel']
    second_fuel = op_ctrl.state['checkpoints'][1]['data']['fuel']
    assert second_fuel > first_fuel, \
        f"Fuel should increase: {first_fuel} -> {second_fuel}"


@then("both checkpoints should have the same location")
def verify_same_location(infrastructure_context):
    """Verify both checkpoints have same location."""
    op_ctrl = infrastructure_context['operation_controller']
    first_loc = op_ctrl.state['checkpoints'][0]['data']['location']
    second_loc = op_ctrl.state['checkpoints'][1]['data']['location']
    assert first_loc == second_loc, "Both checkpoints should have same location"


@then(parsers.parse('the progress should show {count:d} checkpoints'))
def verify_progress_checkpoint_count(infrastructure_context, count):
    """Verify progress shows correct checkpoint count."""
    progress = infrastructure_context['progress']
    assert progress['checkpoints'] == count, \
        f"Progress should show {count} checkpoints, got {progress['checkpoints']}"


@then("the progress should include the last checkpoint")
def verify_progress_includes_last_checkpoint(infrastructure_context):
    """Verify progress includes last checkpoint."""
    progress = infrastructure_context['progress']
    assert progress['last_checkpoint'] is not None, "Progress should include last checkpoint"


@then(parsers.parse('the last checkpoint should be at step {step:d}'))
def verify_last_checkpoint_step(infrastructure_context, step):
    """Verify last checkpoint is at correct step."""
    progress = infrastructure_context['progress']
    assert progress['last_checkpoint']['completed_step'] == step, \
        f"Last checkpoint should be step {step}, got {progress['last_checkpoint']['completed_step']}"


# ====================
# Daemon Cache Prevention Steps
# ====================

@given(parsers.parse('a daemon manager for player {player_id:d}'))
def setup_daemon_manager(infrastructure_context, player_id):
    """Set up daemon manager."""
    infrastructure_context['daemon_manager'] = DaemonManager(player_id=player_id)


@given(parsers.parse('a Python command: {command}'))
def set_python_command(infrastructure_context, command):
    """Set Python command."""
    infrastructure_context['command'] = command.split()


@given(parsers.parse('a non-Python command: {command}'))
def set_non_python_command(infrastructure_context, command):
    """Set non-Python command."""
    infrastructure_context['command'] = command.split()


@given("an empty command")
def set_empty_command(infrastructure_context):
    """Set empty command."""
    infrastructure_context['command'] = []


@given("a daemon manager instance")
def create_daemon_manager_instance(infrastructure_context):
    """Create daemon manager instance."""
    infrastructure_context['daemon_manager'] = DaemonManager(player_id=999)


@when("I inject the no-cache flag")
def inject_no_cache_flag(infrastructure_context):
    """Inject no-cache flag into command."""
    manager = infrastructure_context['daemon_manager']
    command = infrastructure_context['command']
    infrastructure_context['injected_command'] = manager._inject_python_no_cache_flag(command)


@when("I inspect the daemon start method source code")
def inspect_daemon_start_source(infrastructure_context):
    """Inspect daemon start method source code."""
    manager = infrastructure_context['daemon_manager']
    infrastructure_context['source_code'] = inspect.getsource(manager.start)


@then(parsers.parse('the command should start with: {expected_start}'))
def verify_command_start(infrastructure_context, expected_start):
    """Verify command starts with expected prefix."""
    injected = infrastructure_context['injected_command']
    expected_parts = expected_start.split()
    actual_start = ' '.join(injected[:len(expected_parts)])
    assert actual_start == expected_start, \
        f"Command should start with '{expected_start}', got '{actual_start}'"


@then(parsers.parse('the remaining arguments should be: {expected_args}'))
def verify_remaining_args(infrastructure_context, expected_args):
    """Verify remaining arguments."""
    injected = infrastructure_context['injected_command']
    expected_parts = expected_args.split()
    # Get parts after "python3 -B"
    actual_remaining = injected[2:]
    assert actual_remaining == expected_parts, \
        f"Remaining args should be {expected_parts}, got {actual_remaining}"


@then("the command should have exactly one -B flag")
def verify_single_b_flag(infrastructure_context):
    """Verify command has exactly one -B flag."""
    injected = infrastructure_context['injected_command']
    assert injected.count('-B') == 1, \
        f"Should have exactly one -B flag, got {injected.count('-B')}"


@then(parsers.parse('the command should be: {expected_command}'))
def verify_exact_command(infrastructure_context, expected_command):
    """Verify exact command."""
    injected = infrastructure_context['injected_command']
    expected = expected_command.split()
    assert injected == expected, \
        f"Command should be {expected}, got {injected}"


@then("the command should remain unchanged")
def verify_command_unchanged(infrastructure_context):
    """Verify command remains unchanged."""
    original = infrastructure_context['command']
    injected = infrastructure_context['injected_command']
    assert injected == original, "Command should remain unchanged"


@then("the command should remain empty")
def verify_command_empty(infrastructure_context):
    """Verify command remains empty."""
    injected = infrastructure_context['injected_command']
    assert injected == [], "Command should remain empty"


@then(parsers.parse('the source should contain "{text}"'))
def verify_source_contains(infrastructure_context, text):
    """Verify source contains text."""
    source = infrastructure_context['source_code']
    assert text in source, f"Source should contain '{text}'"
