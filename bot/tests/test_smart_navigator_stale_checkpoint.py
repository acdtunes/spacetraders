"""
Test that SmartNavigator properly validates stale checkpoints

BUG SCENARIO:
1. Mining operation: Navigate B7→B14 (checkpoint saved: location=B14, completed_step=1)
2. Extract resources at B14
3. Mining operation: Navigate B14→B7 (new route)
4. SmartNavigator sees checkpoint from previous navigation (location=B14, completed_step=1)
5. BUG: It tries to resume from step 2, but ship is at B14, not at the expected location
6. FIX: Validate checkpoint location matches ship's current location before resuming
"""
import pytest
from unittest.mock import Mock, MagicMock
from spacetraders_bot.core.smart_navigator import SmartNavigator


def test_stale_checkpoint_location_mismatch():
    """
    Test that stale checkpoints with location mismatch are rejected

    Scenario:
    - Checkpoint says location=X1-TX46-B7 (from previous navigation)
    - Ship is actually at X1-TX46-B14 (current location)
    - Checkpoint should be rejected and route should start from step 1
    """
    # Create mock API
    api = Mock()

    # Create graph
    graph = {
        'waypoints': {
            'X1-TX46-B14': {'x': 0, 'y': 0, 'type': 'ASTEROID', 'traits': []},
            'X1-TX46-B7': {'x': 11.7, 'y': 0, 'type': 'PLANET', 'traits': ['MARKETPLACE']},
        }
    }

    # Create navigator
    navigator = SmartNavigator(api, 'X1-TX46', graph=graph)

    # Create mock ship controller
    ship_controller = Mock()
    ship_controller.get_status.return_value = {
        'symbol': 'STARHOPPER-8',
        'nav': {
            'waypointSymbol': 'X1-TX46-B14',  # Ship is at B14
            'status': 'IN_ORBIT',
        },
        'fuel': {
            'current': 20,
            'capacity': 400,
        },
        'frame': {
            'integrity': 1.0,
        },
        'cooldown': {
            'remainingSeconds': 0,
        },
        'registration': {
            'role': 'EXCAVATOR',
        },
    }
    ship_controller.navigate.return_value = True
    ship_controller.refuel.return_value = True
    ship_controller.orbit.return_value = True
    ship_controller.dock.return_value = True

    # Create mock operation controller with STALE checkpoint
    operation_controller = Mock()
    operation_controller.can_resume.return_value = True
    operation_controller.get_last_checkpoint.return_value = {
        'completed_step': 1,
        'location': 'X1-TX46-B7',  # Checkpoint says ship is at B7 (WRONG!)
        'fuel': 50,
        'state': 'IN_ORBIT',
    }
    operation_controller.should_cancel.return_value = False
    operation_controller.should_pause.return_value = False
    operation_controller.checkpoint = Mock()

    # Mock plan_route to return a 2-step route
    def mock_plan_route(ship_data, destination, prefer_cruise=True):
        return {
            'steps': [
                {
                    'action': 'navigate',
                    'from': 'X1-TX46-B14',
                    'to': 'X1-TX46-B7',
                    'mode': 'CRUISE',
                    'distance': 11.7,
                    'fuel_cost': 12,
                },
                {
                    'action': 'refuel',
                    'waypoint': 'X1-TX46-B7',
                    'fuel_added': 40,
                },
            ],
            'total_time': 45,
            'final_fuel': 48,
        }

    navigator.plan_route = mock_plan_route

    # Execute route - should REJECT stale checkpoint and start from step 1
    success = navigator.execute_route(
        ship_controller,
        'X1-TX46-B7',
        operation_controller=operation_controller
    )

    # Verify navigate was called (step 1 was executed, not skipped)
    assert ship_controller.navigate.called, "Navigate should have been called (step 1 not skipped)"

    # Verify checkpoint was NOT used to skip step 1
    # (The bug was that it would skip to step 2 due to stale checkpoint)
    navigate_calls = ship_controller.navigate.call_count
    assert navigate_calls >= 1, f"Expected at least 1 navigate call, got {navigate_calls}"


def test_valid_checkpoint_location_match():
    """
    Test that valid checkpoints with matching location are accepted

    Scenario:
    - Checkpoint says location=X1-TX46-B7, completed_step=1
    - Ship is actually at X1-TX46-B7 (location matches)
    - Checkpoint should be accepted and route should resume from step 2
    """
    # Create mock API
    api = Mock()

    # Create graph
    graph = {
        'waypoints': {
            'X1-TX46-B7': {'x': 11.7, 'y': 0, 'type': 'PLANET', 'traits': ['MARKETPLACE']},
            'X1-TX46-B14': {'x': 0, 'y': 0, 'type': 'ASTEROID', 'traits': []},
        }
    }

    # Create navigator
    navigator = SmartNavigator(api, 'X1-TX46', graph=graph)

    # Create mock ship controller
    ship_controller = Mock()
    ship_controller.get_status.return_value = {
        'symbol': 'STARHOPPER-8',
        'nav': {
            'waypointSymbol': 'X1-TX46-B7',  # Ship is at B7 (matches checkpoint)
            'status': 'IN_ORBIT',
        },
        'fuel': {
            'current': 50,
            'capacity': 400,
        },
        'frame': {
            'integrity': 1.0,
        },
        'cooldown': {
            'remainingSeconds': 0,
        },
        'registration': {
            'role': 'EXCAVATOR',
        },
    }
    ship_controller.navigate.return_value = True
    ship_controller.refuel.return_value = True
    ship_controller.dock.return_value = True

    # Create mock operation controller with VALID checkpoint
    operation_controller = Mock()
    operation_controller.can_resume.return_value = True
    operation_controller.get_last_checkpoint.return_value = {
        'completed_step': 1,
        'location': 'X1-TX46-B7',  # Checkpoint says ship is at B7 (CORRECT!)
        'fuel': 50,
        'state': 'IN_ORBIT',
    }
    operation_controller.should_cancel.return_value = False
    operation_controller.should_pause.return_value = False
    operation_controller.checkpoint = Mock()

    # Mock plan_route to return a 2-step route
    def mock_plan_route(ship_data, destination, prefer_cruise=True):
        return {
            'steps': [
                {
                    'action': 'navigate',
                    'from': 'X1-TX46-B7',
                    'to': 'X1-TX46-B14',
                    'mode': 'CRUISE',
                    'distance': 11.7,
                    'fuel_cost': 12,
                },
                {
                    'action': 'refuel',
                    'waypoint': 'X1-TX46-B14',
                    'fuel_added': 40,
                },
            ],
            'total_time': 45,
            'final_fuel': 88,
        }

    navigator.plan_route = mock_plan_route

    # Execute route - should ACCEPT valid checkpoint and resume from step 2
    success = navigator.execute_route(
        ship_controller,
        'X1-TX46-B14',
        operation_controller=operation_controller
    )

    # Verify navigate was NOT called (step 1 was skipped because checkpoint is valid)
    assert not ship_controller.navigate.called, "Navigate should NOT have been called (step 1 should be skipped)"

    # Verify refuel was called (step 2 should execute)
    assert ship_controller.refuel.called or ship_controller.dock.called, \
        "Refuel or dock should have been called (step 2 executed)"


def test_stale_checkpoint_destination_mismatch():
    """
    Test that checkpoints with destination mismatch are rejected

    Scenario (THE ACTUAL BUG):
    - Previous route: B7→B14, checkpoint saved with destination=B14
    - Ship completes route, is at B14
    - New route: B14→B7 (different destination!)
    - Checkpoint says destination=B14, but new route goes to B7
    - Checkpoint should be rejected even though location matches
    """
    # Create mock API
    api = Mock()

    # Create graph
    graph = {
        'waypoints': {
            'X1-TX46-B14': {'x': 0, 'y': 0, 'type': 'ASTEROID', 'traits': []},
            'X1-TX46-B7': {'x': 11.7, 'y': 0, 'type': 'PLANET', 'traits': ['MARKETPLACE']},
        }
    }

    # Create navigator
    navigator = SmartNavigator(api, 'X1-TX46', graph=graph)

    # Create mock ship controller
    ship_controller = Mock()
    ship_controller.get_status.return_value = {
        'symbol': 'STARHOPPER-8',
        'nav': {
            'waypointSymbol': 'X1-TX46-B14',  # Ship is at B14 (matches checkpoint location)
            'status': 'IN_ORBIT',
        },
        'fuel': {
            'current': 20,
            'capacity': 400,
        },
        'frame': {
            'integrity': 1.0,
        },
        'cooldown': {
            'remainingSeconds': 0,
        },
        'registration': {
            'role': 'EXCAVATOR',
        },
    }
    ship_controller.navigate.return_value = True
    ship_controller.refuel.return_value = True
    ship_controller.orbit.return_value = True
    ship_controller.dock.return_value = True

    # Create mock operation controller with checkpoint from PREVIOUS route
    operation_controller = Mock()
    operation_controller.can_resume.return_value = True
    operation_controller.get_last_checkpoint.return_value = {
        'completed_step': 1,
        'location': 'X1-TX46-B14',  # Location matches current ship location
        'destination': 'X1-TX46-B14',  # But destination was B14 (previous route B7→B14)
        'fuel': 50,
        'state': 'IN_ORBIT',
    }
    operation_controller.should_cancel.return_value = False
    operation_controller.should_pause.return_value = False
    operation_controller.checkpoint = Mock()

    # Mock plan_route to return a route B14→B7 (NEW route, different destination)
    def mock_plan_route(ship_data, destination, prefer_cruise=True):
        return {
            'steps': [
                {
                    'action': 'navigate',
                    'from': 'X1-TX46-B14',
                    'to': 'X1-TX46-B7',  # Going to B7, not B14!
                    'mode': 'CRUISE',
                    'distance': 11.7,
                    'fuel_cost': 12,
                },
                {
                    'action': 'refuel',
                    'waypoint': 'X1-TX46-B7',
                    'fuel_added': 40,
                },
            ],
            'total_time': 45,
            'final_fuel': 58,
        }

    navigator.plan_route = mock_plan_route

    # Execute route - should REJECT checkpoint due to destination mismatch
    success = navigator.execute_route(
        ship_controller,
        'X1-TX46-B7',  # Destination is B7
        operation_controller=operation_controller
    )

    # Verify navigate was called (step 1 was executed, not skipped)
    assert ship_controller.navigate.called, "Navigate should have been called (step 1 not skipped)"

    # This is the KEY fix: even though location matches (B14 == B14),
    # the destination mismatch (B14 != B7) should cause checkpoint to be rejected


if __name__ == '__main__':
    pytest.main([__file__, '-xvs'])
