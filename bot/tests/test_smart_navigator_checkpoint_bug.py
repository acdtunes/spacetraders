#!/usr/bin/env python3
"""
Test for SmartNavigator checkpoint resume bug

BUG: SmartNavigator hardcodes DRIFT mode when navigating to refuel waypoints
during checkpoint resume, bypassing the fixed select_flight_mode() logic.

EXPECTED: Should use CRUISE when fuel is adequate (same as non-checkpoint path)
ACTUAL: Always uses DRIFT for refuel waypoint navigation during resume
"""

import pytest
from unittest.mock import Mock, MagicMock, patch
from datetime import datetime, timezone, timedelta


@pytest.fixture
def mock_api():
    """Mock API client"""
    api = Mock()
    api.get_waypoint = Mock(return_value={'x': 0, 'y': 0, 'type': 'FUEL_STATION', 'traits': ['MARKETPLACE']})
    return api


@pytest.fixture
def mock_ship_controller():
    """Mock ship controller"""
    controller = Mock()
    controller.ship_symbol = "STARHOPPER-8"

    # Ship status with adequate fuel for CRUISE
    controller.get_status = Mock(return_value={
        'symbol': 'STARHOPPER-8',
        'nav': {
            'waypointSymbol': 'X1-TX46-B14',
            'status': 'IN_ORBIT',
            'systemSymbol': 'X1-TX46'
        },
        'fuel': {
            'current': 52,  # Adequate for CRUISE (12 units needed)
            'capacity': 80
        },
        'frame': {
            'integrity': 1.0
        },
        'registration': {
            'role': 'EXCAVATOR'
        },
        'cooldown': {
            'remainingSeconds': 0
        }
    })

    controller.navigate = Mock(return_value=True)
    controller.dock = Mock(return_value=True)
    controller.refuel = Mock(return_value=True)
    controller._wait_for_arrival = Mock()

    return controller


@pytest.fixture
def mock_operation_controller():
    """Mock operation controller with checkpoint"""
    controller = Mock()
    controller.can_resume = Mock(return_value=True)
    controller.get_last_checkpoint = Mock(return_value={
        'completed_step': 1,  # Resume from step 2 (refuel)
        'location': 'X1-TX46-B14',
        'fuel': 52,
        'state': 'IN_ORBIT'
    })
    controller.should_cancel = Mock(return_value=False)
    controller.should_pause = Mock(return_value=False)
    controller.checkpoint = Mock()
    return controller


@pytest.fixture
def mock_graph():
    """Mock system graph with asteroid and market"""
    return {
        'waypoints': {
            'X1-TX46-B14': {
                'symbol': 'X1-TX46-B14',
                'type': 'ASTEROID',
                'x': 0,
                'y': 0,
                'traits': []
            },
            'X1-TX46-B7': {
                'symbol': 'X1-TX46-B7',
                'type': 'PLANET',
                'x': 12,  # 12 units away
                'y': 0,
                'traits': ['MARKETPLACE']
            }
        }
    }


def test_checkpoint_resume_uses_drift_instead_of_cruise(mock_api, mock_ship_controller, mock_operation_controller, mock_graph):
    """
    Reproduce bug: Checkpoint resume path uses DRIFT instead of CRUISE

    Scenario:
    - Ship at asteroid B14 with 52/80 fuel (65% full)
    - Needs to navigate to market B7 (12 units away)
    - CRUISE needs 12 fuel (have 52) → should use CRUISE
    - Route plan says "CRUISE"
    - But checkpoint resume hardcodes DRIFT mode

    Expected: navigate() called with flight_mode='CRUISE'
    Actual: navigate() called with flight_mode='DRIFT'
    """
    from spacetraders_bot.core.smart_navigator import SmartNavigator
    from spacetraders_bot.core.routing_config import RoutingConfig

    # Create SmartNavigator with mocked graph
    navigator = SmartNavigator(mock_api, system="X1-TX46", graph=mock_graph)

    # Mock the plan_route to return a route with CRUISE mode (what OR-Tools would select)
    mock_route = {
        'steps': [
            {
                'action': 'navigate',
                'from': 'X1-TX46-B14',
                'to': 'X1-TX46-B7',
                'mode': 'CRUISE',  # Route plan says CRUISE
                'distance': 12,
                'fuel_cost': 12
            },
            {
                'action': 'refuel',
                'waypoint': 'X1-TX46-B7',
                'fuel_added': 40
            }
        ],
        'total_time': 45,
        'final_fuel': 80
    }

    with patch.object(navigator, 'plan_route', return_value=mock_route):
        # Execute route with checkpoint resume (will skip step 1, execute step 2)
        result = navigator.execute_route(
            mock_ship_controller,
            destination='X1-TX46-B7',
            operation_controller=mock_operation_controller
        )

    # Verify navigation was called (for refuel waypoint during step 2 resume)
    assert mock_ship_controller.navigate.called, "navigate() should have been called"

    # Get the call arguments
    call_args = mock_ship_controller.navigate.call_args

    # CRITICAL CHECK: What flight mode was used?
    actual_flight_mode = call_args[1].get('flight_mode') if len(call_args) > 1 else call_args[0][1] if len(call_args[0]) > 1 else None

    # BUG: This will fail because SmartNavigator hardcodes 'DRIFT' at line 508
    assert actual_flight_mode == 'CRUISE', (
        f"BUG CONFIRMED: Checkpoint resume used flight_mode='{actual_flight_mode}' "
        f"instead of 'CRUISE' (ship has 52/80 fuel, needs 12 for 12-unit trip). "
        f"Line 508 in smart_navigator.py hardcodes DRIFT instead of using select_flight_mode()."
    )


def test_normal_path_uses_cruise_correctly(mock_api, mock_ship_controller, mock_graph):
    """
    Verify that the normal (non-checkpoint) path correctly uses CRUISE

    This test should PASS, demonstrating the discrepancy between the two code paths.
    """
    from spacetraders_bot.core.smart_navigator import SmartNavigator

    # Create SmartNavigator with mocked graph
    navigator = SmartNavigator(mock_api, system="X1-TX46", graph=mock_graph)

    # Mock the plan_route to return a simple CRUISE route
    mock_route = {
        'steps': [
            {
                'action': 'navigate',
                'from': 'X1-TX46-B14',
                'to': 'X1-TX46-B7',
                'mode': 'CRUISE',  # Route plan says CRUISE
                'distance': 12,
                'fuel_cost': 12
            }
        ],
        'total_time': 40,
        'final_fuel': 40
    }

    with patch.object(navigator, 'plan_route', return_value=mock_route):
        # Execute route WITHOUT checkpoint (fresh operation)
        result = navigator.execute_route(
            mock_ship_controller,
            destination='X1-TX46-B7',
            operation_controller=None  # No checkpoint
        )

    # Verify navigation was called with CRUISE
    assert mock_ship_controller.navigate.called
    call_args = mock_ship_controller.navigate.call_args

    # This should PASS because the normal path uses the route plan's mode correctly
    actual_flight_mode = call_args[1].get('flight_mode')
    assert actual_flight_mode == 'CRUISE', (
        f"Normal path should use CRUISE from route plan, got '{actual_flight_mode}'"
    )


if __name__ == '__main__':
    pytest.main([__file__, '-v', '-s'])
