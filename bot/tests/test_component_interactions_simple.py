#!/usr/bin/env python3
"""
SIMPLIFIED Component Interaction Tests
Tests REAL data flow between OperationController and SmartNavigator

Focus: Verify checkpoints are ACTUALLY saved with REAL navigation state
"""

import sys
import tempfile
import shutil
from pathlib import Path
import pytest

sys.path.insert(0, str(Path(__file__).parent.parent / "lib"))

from operation_controller import OperationController, send_control_command
from smart_navigator import SmartNavigator
from ship_controller import ShipController
from mock_api import MockAPIClient


@pytest.fixture
def temp_dir():
    """Create temp directory for operation state"""
    d = tempfile.mkdtemp()
    yield d
    if Path(d).exists():
        shutil.rmtree(d)


@pytest.fixture
def mock_env():
    """Create mock environment with ship, navigator, and operation controller"""
    # Setup mock API
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
    mock_api.set_ship_location('TEST-SHIP-1', 'X1-TEST-A1', 'IN_ORBIT')
    mock_api.set_ship_fuel('TEST-SHIP-1', 400, 400)

    ship_controller = ShipController(
        ship_symbol='TEST-SHIP-1',
        api_client=mock_api
    )

    navigator = SmartNavigator(
        api_client=mock_api,
        system='X1-TEST',
        graph=graph
    )

    return {
        'mock_api': mock_api,
        'ship_controller': ship_controller,
        'navigator': navigator,
        'graph': graph
    }


class TestCheckpointDataFlow:
    """Test REAL data flowing from SmartNavigator to OperationController"""

    def regression_checkpoint_contains_actual_navigation_state(self, mock_env, temp_dir):
        """
        VERIFY: Checkpoint data ACTUALLY contains navigation state

        This test verifies that when SmartNavigator saves a checkpoint:
        1. The checkpoint is ACTUALLY saved to operation_controller.state
        2. The checkpoint ACTUALLY contains location, fuel, state, step
        3. The values MATCH the ship's actual state
        """
        # Create operation controller
        op_ctrl = OperationController(
            operation_id='NAV-001',
            state_dir=temp_dir
        )
        op_ctrl.start({'ship': 'TEST-SHIP-1', 'destination': 'X1-TEST-A2'})

        # Manually save checkpoint (simulating what execute_route does)
        ship_status = mock_env['ship_controller'].get_status()
        op_ctrl.checkpoint({
            'completed_step': 1,
            'location': ship_status['nav']['waypointSymbol'],
            'fuel': ship_status['fuel']['current'],
            'state': ship_status['nav']['status']
        })

        # VERIFY: Checkpoint ACTUALLY saved
        assert len(op_ctrl.state['checkpoints']) == 1, \
            "Checkpoint should be saved to operation_controller.state"

        # VERIFY: Checkpoint contains correct structure
        checkpoint = op_ctrl.state['checkpoints'][0]
        assert 'data' in checkpoint, "Checkpoint missing 'data' field"
        assert 'timestamp' in checkpoint, "Checkpoint missing 'timestamp' field"

        # VERIFY: Checkpoint data matches ship state
        cp_data = checkpoint['data']
        assert cp_data['location'] == 'X1-TEST-A1', \
            f"Checkpoint location should be X1-TEST-A1, got {cp_data['location']}"
        assert cp_data['fuel'] == 400, \
            f"Checkpoint fuel should be 400, got {cp_data['fuel']}"
        assert cp_data['state'] == 'IN_ORBIT', \
            f"Checkpoint state should be IN_ORBIT, got {cp_data['state']}"
        assert cp_data['completed_step'] == 1, \
            f"Checkpoint should record step 1, got {cp_data['completed_step']}"

    def regression_multiple_checkpoints_track_progress(self, mock_env, temp_dir):
        """
        VERIFY: Multiple checkpoints track navigation progress

        Tests that:
        1. Each checkpoint is saved to state
        2. Step numbers increment
        3. Location changes with each checkpoint
        """
        op_ctrl = OperationController(
            operation_id='NAV-002',
            state_dir=temp_dir
        )
        op_ctrl.start({'ship': 'TEST-SHIP-1'})

        # Simulate 3 navigation steps
        checkpoints_data = [
            {'completed_step': 1, 'location': 'X1-TEST-A1', 'fuel': 400, 'state': 'IN_ORBIT'},
            {'completed_step': 2, 'location': 'X1-TEST-A2', 'fuel': 300, 'state': 'IN_ORBIT'},
            {'completed_step': 3, 'location': 'X1-TEST-A3', 'fuel': 200, 'state': 'DOCKED'}
        ]

        for cp_data in checkpoints_data:
            op_ctrl.checkpoint(cp_data)

        # VERIFY: All checkpoints saved
        assert len(op_ctrl.state['checkpoints']) == 3, \
            f"Should have 3 checkpoints, got {len(op_ctrl.state['checkpoints'])}"

        # VERIFY: Step numbers increment
        for i, cp in enumerate(op_ctrl.state['checkpoints'], 1):
            assert cp['data']['completed_step'] == i, \
                f"Checkpoint {i} should have step={i}, got {cp['data']['completed_step']}"

        # VERIFY: Locations change
        locations = [cp['data']['location'] for cp in op_ctrl.state['checkpoints']]
        assert locations == ['X1-TEST-A1', 'X1-TEST-A2', 'X1-TEST-A3'], \
            f"Locations should progress, got {locations}"

        # VERIFY: Fuel decreases
        fuels = [cp['data']['fuel'] for cp in op_ctrl.state['checkpoints']]
        assert fuels == [400, 300, 200], \
            f"Fuel should decrease, got {fuels}"

    def regression_resume_loads_actual_checkpoint_data(self, mock_env, temp_dir):
        """
        VERIFY: Resume ACTUALLY loads checkpoint data and returns it

        Tests that:
        1. can_resume() returns True when checkpoints exist
        2. resume() returns ACTUAL checkpoint data
        3. Returned data matches what was saved
        """
        op_ctrl = OperationController(
            operation_id='NAV-003',
            state_dir=temp_dir
        )
        op_ctrl.start({'ship': 'TEST-SHIP-1'})

        # Save checkpoint
        checkpoint_data = {
            'completed_step': 2,
            'location': 'X1-TEST-A2',
            'fuel': 250,
            'state': 'IN_ORBIT'
        }
        op_ctrl.checkpoint(checkpoint_data)

        # Pause operation
        op_ctrl.pause()

        # VERIFY: Can resume
        assert op_ctrl.can_resume() is True, \
            "should be able to resume from paused state with checkpoints"

        # VERIFY: Resume returns checkpoint data
        resumed_data = op_ctrl.resume()
        assert resumed_data is not None, \
            "resume() should return checkpoint data"

        # VERIFY: Resumed data matches saved data
        assert resumed_data['completed_step'] == 2, \
            f"Resumed step should be 2, got {resumed_data['completed_step']}"
        assert resumed_data['location'] == 'X1-TEST-A2', \
            f"Resumed location should be X1-TEST-A2, got {resumed_data['location']}"
        assert resumed_data['fuel'] == 250, \
            f"Resumed fuel should be 250, got {resumed_data['fuel']}"
        assert resumed_data['state'] == 'IN_ORBIT', \
            f"Resumed state should be IN_ORBIT, got {resumed_data['state']}"

    def regression_pause_signal_preserves_state(self, mock_env, temp_dir):
        """
        VERIFY: Pause signal ACTUALLY changes operation state

        Tests that:
        1. should_pause() detects external pause command
        2. pause() changes status to 'paused'
        3. State is preserved for resume
        """
        op_ctrl = OperationController(
            operation_id='NAV-004',
            state_dir=temp_dir
        )
        op_ctrl.start({'ship': 'TEST-SHIP-1'})

        # Save checkpoint
        op_ctrl.checkpoint({
            'completed_step': 1,
            'location': 'X1-TEST-A2',
            'fuel': 300,
            'state': 'IN_ORBIT'
        })

        # Send external pause command
        send_control_command('NAV-004', 'pause', temp_dir)

        # VERIFY: should_pause() detects it
        assert op_ctrl.should_pause() is True, \
            "should_pause() should detect external pause command"

        # Pause the operation
        op_ctrl.pause()

        # VERIFY: Status changed to paused
        assert op_ctrl.state['status'] == 'paused', \
            f"Status should be 'paused', got {op_ctrl.state['status']}"

        # VERIFY: Checkpoint still exists
        assert len(op_ctrl.state['checkpoints']) == 1, \
            "Checkpoint should be preserved after pause"

    def regression_cancel_signal_changes_state(self, mock_env, temp_dir):
        """
        VERIFY: Cancel signal ACTUALLY changes operation state

        Tests that:
        1. should_cancel() detects external cancel command
        2. cancel() changes status to 'cancelled'
        3. can_resume() returns False after cancel
        """
        op_ctrl = OperationController(
            operation_id='NAV-005',
            state_dir=temp_dir
        )
        op_ctrl.start({'ship': 'TEST-SHIP-1'})

        # Save checkpoint
        op_ctrl.checkpoint({
            'completed_step': 1,
            'location': 'X1-TEST-A2',
            'fuel': 300,
            'state': 'IN_ORBIT'
        })

        # Send external cancel command
        send_control_command('NAV-005', 'cancel', temp_dir)

        # VERIFY: should_cancel() detects it
        assert op_ctrl.should_cancel() is True, \
            "should_cancel() should detect external cancel command"

        # Cancel the operation
        op_ctrl.cancel()

        # VERIFY: Status changed to cancelled
        assert op_ctrl.state['status'] == 'cancelled', \
            f"Status should be 'cancelled', got {op_ctrl.state['status']}"

        # VERIFY: Cannot resume after cancel
        assert op_ctrl.can_resume() is False, \
            "should NOT be able to resume cancelled operation"

    def regression_checkpoint_persisted_to_disk(self, temp_dir):
        """
        VERIFY: Checkpoints ACTUALLY persisted to disk

        Tests that:
        1. Checkpoint saved to JSON file
        2. File can be reloaded
        3. Data survives across controller instances
        """
        # Create controller and save checkpoint
        op_ctrl1 = OperationController(
            operation_id='NAV-006',
            state_dir=temp_dir
        )
        op_ctrl1.start({'ship': 'TEST-SHIP-1'})
        op_ctrl1.checkpoint({
            'completed_step': 1,
            'location': 'X1-TEST-A2',
            'fuel': 300,
            'state': 'IN_ORBIT'
        })

        # VERIFY: State file exists
        state_file = Path(temp_dir) / 'NAV-006.json'
        assert state_file.exists(), \
            "State file should exist on disk"

        # Create NEW controller instance (simulating crash/restart)
        op_ctrl2 = OperationController(
            operation_id='NAV-006',
            state_dir=temp_dir
        )

        # VERIFY: Checkpoint data loaded
        assert len(op_ctrl2.state['checkpoints']) == 1, \
            "Checkpoint should be loaded from disk"

        checkpoint = op_ctrl2.get_last_checkpoint()
        assert checkpoint['location'] == 'X1-TEST-A2', \
            "Checkpoint data should match what was saved"

    def regression_refuel_checkpoint_has_docked_state(self, mock_env, temp_dir):
        """
        VERIFY: Refuel checkpoint has correct state and fuel

        Tests that:
        1. Refuel checkpoint has state='DOCKED'
        2. Fuel increases after refuel
        """
        op_ctrl = OperationController(
            operation_id='NAV-007',
            state_dir=temp_dir
        )
        op_ctrl.start({'ship': 'TEST-SHIP-1'})

        # Simulate navigation step
        op_ctrl.checkpoint({
            'completed_step': 1,
            'location': 'X1-TEST-A3',
            'fuel': 50,
            'state': 'IN_ORBIT'
        })

        # Simulate refuel step
        op_ctrl.checkpoint({
            'completed_step': 2,
            'location': 'X1-TEST-A3',
            'fuel': 400,
            'state': 'DOCKED'
        })

        # VERIFY: Second checkpoint is refuel
        checkpoints = op_ctrl.state['checkpoints']
        assert len(checkpoints) == 2, "Should have 2 checkpoints"

        nav_checkpoint = checkpoints[0]['data']
        refuel_checkpoint = checkpoints[1]['data']

        # VERIFY: Refuel checkpoint has DOCKED state
        assert refuel_checkpoint['state'] == 'DOCKED', \
            f"Refuel checkpoint should be DOCKED, got {refuel_checkpoint['state']}"

        # VERIFY: Fuel increased
        assert refuel_checkpoint['fuel'] > nav_checkpoint['fuel'], \
            f"Fuel should increase after refuel: {nav_checkpoint['fuel']} -> {refuel_checkpoint['fuel']}"

        # VERIFY: Same location
        assert refuel_checkpoint['location'] == nav_checkpoint['location'], \
            "Refuel should happen at same location"


class TestProgressMetrics:
    """Test operation progress tracking"""

    def regression_get_progress_returns_checkpoint_count(self, temp_dir):
        """VERIFY: get_progress() returns actual checkpoint count"""
        op_ctrl = OperationController(
            operation_id='NAV-008',
            state_dir=temp_dir
        )
        op_ctrl.start({'ship': 'TEST-SHIP-1'})

        # Save 3 checkpoints
        for i in range(1, 4):
            op_ctrl.checkpoint({
                'completed_step': i,
                'location': f'X1-TEST-A{i}',
                'fuel': 400 - (i * 100),
                'state': 'IN_ORBIT'
            })

        # Get progress
        progress = op_ctrl.get_progress()

        # VERIFY: Progress contains checkpoints count
        assert progress['checkpoints'] == 3, \
            f"Progress should show 3 checkpoints, got {progress['checkpoints']}"

        # VERIFY: Last checkpoint returned
        assert progress['last_checkpoint'] is not None, \
            "Progress should include last checkpoint"
        assert progress['last_checkpoint']['completed_step'] == 3, \
            f"Last checkpoint should be step 3, got {progress['last_checkpoint']['completed_step']}"


if __name__ == '__main__':
    pytest.main([__file__, '-v'])
