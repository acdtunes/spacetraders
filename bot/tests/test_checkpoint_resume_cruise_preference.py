"""
Test that SmartNavigator checkpoint resume uses CRUISE mode correctly

BUG REPORT:
- STARHOPPER-8 uses DRIFT mode (353s) for return trip from asteroid to market
- Route plan says "CRUISE" but execution uses "DRIFT"
- The checkpoint resume path has different flight mode selection logic
"""
import pytest
from unittest.mock import Mock, MagicMock, patch
from spacetraders_bot.core.smart_navigator import SmartNavigator
from spacetraders_bot.core.utils import select_flight_mode


def test_select_flight_mode_prefers_cruise():
    """Verify that select_flight_mode() prefers CRUISE when fuel is adequate"""
    # Scenario: 12 fuel, 400 capacity, 11.7 unit distance
    # This is the exact scenario from the bug report
    current_fuel = 12
    fuel_capacity = 400
    distance = 11.7

    mode = select_flight_mode(
        current_fuel,
        fuel_capacity,
        distance,
        require_return=False
    )

    # With 12 fuel and 11.7 unit distance:
    # CRUISE needs ~13 fuel (11.7 * 1.1)
    # Since current_fuel (12) < cruise_fuel (13), it will fall back to DRIFT
    # But wait - this is the bug! We need to check if DRIFT is really needed

    # Actually let's calculate:
    # CRUISE fuel = distance * 1.1 = 11.7 * 1.1 = 12.87 ≈ 13
    # So with 12 fuel, we CAN'T actually do CRUISE safely

    # Let's test with MORE fuel
    current_fuel = 20  # Plenty of fuel
    mode = select_flight_mode(
        current_fuel,
        fuel_capacity,
        distance,
        require_return=False
    )

    assert mode == "CRUISE", f"Expected CRUISE with {current_fuel} fuel for {distance} units, got {mode}"


def test_checkpoint_resume_navigates_to_refuel_with_cruise():
    """
    Test that when resuming from checkpoint, navigation to refuel waypoint uses CRUISE

    Scenario:
    1. Ship extracts at asteroid B14
    2. Checkpoint saved after extraction
    3. Resume from checkpoint
    4. Navigate to market B7 (12 units away, 20 fuel available)
    5. Should use CRUISE mode
    """
    # Create mock API client
    api = Mock()

    # Create graph with waypoints
    graph = {
        'waypoints': {
            'X1-TX46-B14': {'x': 0, 'y': 0, 'type': 'ASTEROID', 'traits': []},
            'X1-TX46-B7': {'x': 11.7, 'y': 0, 'type': 'PLANET', 'traits': ['MARKETPLACE']},
        }
    }

    # Create SmartNavigator
    navigator = SmartNavigator(api, 'X1-TX46', graph=graph)

    # Create mock ship controller
    ship_controller = Mock()

    # Ship status: at asteroid B14, 20 fuel (plenty for CRUISE)
    ship_data_before = {
        'symbol': 'STARHOPPER-8',
        'nav': {
            'waypointSymbol': 'X1-TX46-B14',
            'status': 'IN_ORBIT',
        },
        'fuel': {
            'current': 20,
            'capacity': 400,
        },
    }

    ship_data_after = {
        'symbol': 'STARHOPPER-8',
        'nav': {
            'waypointSymbol': 'X1-TX46-B7',
            'status': 'IN_ORBIT',
        },
        'fuel': {
            'current': 8,
            'capacity': 400,
        },
    }

    # Mock get_status to return updated location after navigation
    ship_controller.get_status.side_effect = [ship_data_before, ship_data_after, ship_data_after]
    ship_controller.navigate.return_value = True
    ship_controller.refuel.return_value = True

    # Create mock operation controller with checkpoint
    operation_controller = Mock()
    operation_controller.should_cancel.return_value = False
    operation_controller.should_pause.return_value = False
    operation_controller.checkpoint = Mock()

    # Mock the _ensure_valid_state to always succeed
    navigator._ensure_valid_state = Mock(return_value=True)

    # Create a refuel step (simulating checkpoint resume scenario)
    refuel_step = {
        'action': 'refuel',
        'waypoint': 'X1-TX46-B7',
        'fuel_added': 40,
    }

    # Execute the refuel step
    success = navigator._execute_refuel_step(
        ship_controller,
        refuel_step,
        2,
        2,
        operation_controller
    )

    # Verify navigation was called
    assert ship_controller.navigate.called, "Navigate should have been called"

    # Extract the flight_mode argument
    call_args = ship_controller.navigate.call_args
    flight_mode_used = call_args[1].get('flight_mode') if len(call_args) > 1 else call_args[0][1] if len(call_args[0]) > 1 else None

    # Verify CRUISE was used (not DRIFT)
    assert flight_mode_used == 'CRUISE', \
        f"Expected CRUISE mode with 20 fuel for 11.7 units, but got {flight_mode_used}"

    assert success, "Refuel step should succeed"


def test_checkpoint_resume_insufficient_fuel_uses_drift():
    """
    Test that when resuming with insufficient fuel, DRIFT is used

    Scenario:
    1. Ship at asteroid B14
    2. Only 2 fuel remaining (emergency)
    3. Navigate to market B7 (12 units away)
    4. Should use DRIFT mode (only ~0.04 fuel needed)
    """
    # Create mock API client
    api = Mock()

    # Create graph with waypoints
    graph = {
        'waypoints': {
            'X1-TX46-B14': {'x': 0, 'y': 0, 'type': 'ASTEROID', 'traits': []},
            'X1-TX46-B7': {'x': 11.7, 'y': 0, 'type': 'PLANET', 'traits': ['MARKETPLACE']},
        }
    }

    # Create SmartNavigator
    navigator = SmartNavigator(api, 'X1-TX46', graph=graph)

    # Create mock ship controller
    ship_controller = Mock()

    # Ship status: at asteroid B14, only 2 fuel (emergency)
    ship_data = {
        'symbol': 'STARHOPPER-8',
        'nav': {
            'waypointSymbol': 'X1-TX46-B14',
            'status': 'IN_ORBIT',
        },
        'fuel': {
            'current': 2,
            'capacity': 400,
        },
    }

    ship_controller.get_status.return_value = ship_data
    ship_controller.navigate.return_value = True
    ship_controller.refuel.return_value = True

    # Create mock operation controller
    operation_controller = Mock()
    operation_controller.should_cancel.return_value = False
    operation_controller.should_pause.return_value = False
    operation_controller.checkpoint = Mock()

    # Mock the _ensure_valid_state to always succeed
    navigator._ensure_valid_state = Mock(return_value=True)

    # Create a refuel step
    refuel_step = {
        'action': 'refuel',
        'waypoint': 'X1-TX46-B7',
        'fuel_added': 40,
    }

    # Execute the refuel step
    success = navigator._execute_refuel_step(
        ship_controller,
        refuel_step,
        2,
        2,
        operation_controller
    )

    # Verify navigation was called
    assert ship_controller.navigate.called, "Navigate should have been called"

    # Extract the flight_mode argument
    call_args = ship_controller.navigate.call_args
    flight_mode_used = call_args[1].get('flight_mode') if len(call_args) > 1 else call_args[0][1] if len(call_args[0]) > 1 else None

    # Verify DRIFT was used (emergency low fuel)
    assert flight_mode_used == 'DRIFT', \
        f"Expected DRIFT mode with 2 fuel for 11.7 units, but got {flight_mode_used}"

    assert success, "Refuel step should succeed"


if __name__ == '__main__':
    pytest.main([__file__, '-v'])
