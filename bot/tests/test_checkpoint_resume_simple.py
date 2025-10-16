"""
Simplified test to check flight mode selection in checkpoint resume

BUG REPORT:
- The SmartNavigator checkpoint resume path selects DRIFT when it should select CRUISE
- When the ship resumes from checkpoint and navigates to refuel waypoint, it should use CRUISE
"""
import pytest
from unittest.mock import Mock, patch
from spacetraders_bot.core.utils import select_flight_mode


def test_select_flight_mode_logic():
    """
    Test the core select_flight_mode() logic with the exact scenario from bug report

    Bug scenario:
    - Ship at B14, needs to go to B7
    - Distance: 11.7 units
    - Current fuel: 12
    - Fuel capacity: 400

    Expected: Should select DRIFT because 12 fuel is insufficient for CRUISE (needs ~13)
    But the bug report says the ship HAD enough fuel initially...

    Wait - let me re-read the bug. The log shows:
    - Step says "Navigate X1-TX46-B14 → X1-TX46-B7 (CRUISE, 12u, 12⛽)"
    - So route planned for CRUISE with 12 fuel cost
    - But execution selected DRIFT instead

    This means the ship had MORE than 12 fuel when the route was planned!
    """
    # Let's test with adequate fuel for CRUISE
    current_fuel = 20  # More than enough
    fuel_capacity = 400
    distance = 11.7

    # CRUISE needs distance * 1.1 = 11.7 * 1.1 = 12.87 ≈ 13 fuel
    mode = select_flight_mode(
        current_fuel,
        fuel_capacity,
        distance,
        require_return=False
    )

    assert mode == "CRUISE", f"With {current_fuel} fuel for {distance:.1f} units, expected CRUISE, got {mode}"


def test_actual_bug_scenario():
    """
    Reproduce the exact bug scenario

    From daemon log:
    - Route plan says: "Navigate X1-TX46-B14 → X1-TX46-B7 (CRUISE, 12u, 12⛽)"
    - Execution says: "Selected flight mode: DRIFT"

    This means:
    1. Route was planned with the assumption ship would use CRUISE
    2. When executing, ship selected DRIFT instead

    The bug is in the checkpoint resume path where it RE-SELECTS flight mode
    instead of using the mode from the route plan!
    """
    from spacetraders_bot.core.smart_navigator import SmartNavigator

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

    # Patch select_flight_mode to track calls
    with patch('spacetraders_bot.core.utils.select_flight_mode') as mock_select:
        mock_select.return_value = 'CRUISE'  # Should prefer CRUISE

        # Create mock ship controller
        ship_controller = Mock()
        ship_controller.get_status.return_value = {
            'symbol': 'STARHOPPER-8',
            'nav': {
                'waypointSymbol': 'X1-TX46-B14',
                'status': 'IN_ORBIT',
            },
            'fuel': {
                'current': 20,  # Plenty of fuel
                'capacity': 400,
            },
        }
        ship_controller.navigate.return_value = True

        # Mock _ensure_valid_state
        navigator._ensure_valid_state = Mock(return_value=True)

        # Create refuel step
        refuel_step = {
            'action': 'refuel',
            'waypoint': 'X1-TX46-B7',
            'fuel_added': 40,
        }

        # Try to execute step - it should call select_flight_mode
        try:
            navigator._execute_refuel_step(
                ship_controller,
                refuel_step,
                2,
                2,
                None
            )
        except:
            pass  # Ignore errors, we just want to check if select_flight_mode was called

        # Verify select_flight_mode was called with correct parameters
        assert mock_select.called, "select_flight_mode should be called during checkpoint resume"

        # Check the call args
        call_args = mock_select.call_args
        if call_args:
            fuel_current = call_args[0][0] if len(call_args[0]) > 0 else call_args[1].get('current_fuel')
            print(f"select_flight_mode called with fuel: {fuel_current}")


def test_direct_function_call():
    """
    Directly test what select_flight_mode returns for the bug scenario
    """
    # Scenario from bug report:
    # - Distance: 11.7 units
    # - Current fuel: 12 (this is what ship had AFTER extraction used some fuel)
    # - CRUISE needs: 11.7 * 1.1 = 12.87 ≈ 13 fuel

    current_fuel = 12
    fuel_capacity = 400
    distance = 11.7

    mode = select_flight_mode(current_fuel, fuel_capacity, distance, require_return=False)

    print(f"\nTest scenario:")
    print(f"  Current fuel: {current_fuel}")
    print(f"  Distance: {distance}")
    print(f"  CRUISE needs: ~{int(distance * 1.1)} fuel")
    print(f"  Selected mode: {mode}")

    # With 12 fuel and needing 13, it SHOULD select DRIFT
    # So the function is working correctly!
    # The bug must be that the ship actually had MORE fuel when route was planned

    # Let's test with the fuel the ship likely had when route was planned:
    current_fuel_before_extraction = 52  # Example: full fuel minus trip to asteroid
    mode_before = select_flight_mode(current_fuel_before_extraction, fuel_capacity, distance, require_return=False)

    print(f"\nWith {current_fuel_before_extraction} fuel (before extraction):")
    print(f"  Selected mode: {mode_before}")

    assert mode_before == "CRUISE", f"With {current_fuel_before_extraction} fuel, should select CRUISE, got {mode_before}"


if __name__ == '__main__':
    pytest.main([__file__, '-v', '-s'])
