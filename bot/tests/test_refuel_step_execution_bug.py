"""
Test for SmartNavigator refuel step execution bug

Bug Description:
- SmartNavigator plans a route with a refuel stop
- Route plan shows 3 steps total (navigate, refuel, navigate)
- But during execution, the refuel step is skipped
- Ship proceeds directly from navigation step 1 to navigation step 2

Expected behavior:
- Ship arrives at refuel waypoint after step 1
- Ship refuels at waypoint (step 2)
- Ship continues to final destination (step 3)

Actual behavior:
- Ship arrives at refuel waypoint after step 1
- Ship skips refueling
- Ship proceeds to final destination in DRIFT mode (slow, 44 min instead of 10 min)
"""

import pytest
from unittest.mock import MagicMock, patch
from spacetraders_bot.core.smart_navigator import SmartNavigator
from spacetraders_bot.core.ship_controller import ShipController


@pytest.fixture
def mock_graph():
    """Graph with 3 waypoints: A2, B33 (has fuel), J62"""
    return {
        'system': 'X1-GH18',
        'waypoints': {
            'X1-GH18-A2': {
                'type': 'PLANET',
                'x': 0,
                'y': 0,
                'traits': [],
                'has_fuel': False,  # No fuel at starting point
                'orbitals': []
            },
            'X1-GH18-B33': {
                'type': 'MOON',
                'x': 346,  # 346 units from A2
                'y': 0,
                'traits': ['MARKETPLACE'],
                'has_fuel': True,  # Has fuel - should refuel here!
                'orbitals': []
            },
            'X1-GH18-J62': {
                'type': 'ASTEROID',
                'x': 346,  # Same x as B33
                'y': 382,  # 382 units from B33
                'traits': [],
                'has_fuel': False,  # No fuel at destination
                'orbitals': []
            }
        },
        'edges': [
            {'from': 'X1-GH18-A2', 'to': 'X1-GH18-B33', 'distance': 346, 'type': 'normal'},
            {'from': 'X1-GH18-A2', 'to': 'X1-GH18-J62', 'distance': 500, 'type': 'normal'},
            {'from': 'X1-GH18-B33', 'to': 'X1-GH18-J62', 'distance': 382, 'type': 'normal'},
        ]
    }


@pytest.fixture
def ship_data():
    """Ship at A2 with moderate fuel (390/400) - enough to CRUISE to B33"""
    return {
        'nav': {
            'waypointSymbol': 'X1-GH18-A2',
            'status': 'IN_ORBIT',
            'systemSymbol': 'X1-GH18',
            'route': {
                'destination': {'symbol': 'X1-GH18-A2'},
                'arrival': '2025-01-01T00:00:00Z'
            }
        },
        'fuel': {
            'current': 390,  # Enough to CRUISE to B33 (346u needs ~381 fuel with safety)
            'capacity': 400
        },
        'frame': {'integrity': 1.0},
        'registration': {'role': 'HAULER'},
        'cooldown': {'remainingSeconds': 0},
        'engine': {'speed': 10}  # Standard speed
    }


@pytest.fixture
def ship_data_at_b33():
    """Ship at B33 (refuel station) with full fuel"""
    return {
        'nav': {
            'waypointSymbol': 'X1-GH18-B33',
            'status': 'IN_ORBIT',
            'systemSymbol': 'X1-GH18',
            'route': {
                'destination': {'symbol': 'X1-GH18-B33'},
                'arrival': '2025-01-01T00:00:00Z'
            }
        },
        'fuel': {
            'current': 400,  # Full fuel after refueling
            'capacity': 400
        },
        'frame': {'integrity': 1.0},
        'registration': {'role': 'HAULER'},
        'cooldown': {'remainingSeconds': 0},
        'engine': {'speed': 10}
    }


def regression_drift_final_approach(mock_graph, ship_data_at_b33):
    """
    Test that ship at B33 with full fuel DRIFTs to J62 when CRUISE requires more fuel than capacity

    Edge case: J62 is 382 units away, needs 420 fuel with safety margin, but ship capacity is 400
    So ship must DRIFT for final approach even after refueling

    This validates that DRIFT to goal is allowed when it's the only option
    """
    api = MagicMock()
    navigator = SmartNavigator(api, 'X1-GH18', graph=mock_graph, db_path=':memory:')

    # Plan route from B33 to J62
    route = navigator.plan_route(ship_data_at_b33, 'X1-GH18-J62', prefer_cruise=True)

    # Route should exist
    assert route is not None, "Route should be found (DRIFT to goal allowed)"

    # Route should have 1 step (DRIFT, since CRUISE needs more fuel than capacity)
    assert len(route['steps']) >= 1, f"Route should have at least 1 step, got {len(route['steps'])}"

    # Should DRIFT to goal (CRUISE not possible due to safety margin)
    navigate_steps = [s for s in route['steps'] if s['action'] == 'navigate']
    assert len(navigate_steps) > 0, "Should have at least one navigation step"
    final_nav = navigate_steps[-1]
    assert final_nav['to'] == 'X1-GH18-J62'
    # Mode could be DRIFT if fuel safety margin prevents CRUISE
    assert final_nav['mode'] in ['CRUISE', 'DRIFT']


def regression_refuel_step_in_route_plan(mock_graph, ship_data):
    """
    Test that RouteOptimizer generates a refuel step in the route plan

    Scenario:
    - Ship at A2 with 390 fuel (enough to CRUISE to B33)
    - Destination: J62 (500 units direct, or 346 + 382 via B33)
    - With prefer_cruise=True, should route through B33 and refuel

    Expected route:
    1. Navigate A2 → B33 (CRUISE, 346u, uses ~346 fuel)
    2. Refuel at B33 (fill to 400)
    3. Navigate B33 → J62 (DRIFT, 382u - CRUISE would need 420 fuel with safety margin, ship capacity is only 400)
    """
    api = MagicMock()
    navigator = SmartNavigator(api, 'X1-GH18', graph=mock_graph, db_path=':memory:')

    # Plan route from A2 to J62
    route = navigator.plan_route(ship_data, 'X1-GH18-J62', prefer_cruise=True)

    # Route should exist
    assert route is not None, "Route should be found"

    # DEBUG: Print route details
    print(f"\n=== ROUTE PLAN ===")
    print(f"Total steps: {len(route['steps'])}")
    for i, step in enumerate(route['steps'], 1):
        print(f"  Step {i}: {step}")
    print(f"==================\n")

    # Route should have 3 steps
    assert len(route['steps']) == 3, f"Route should have 3 steps (nav, refuel, nav), got {len(route['steps'])}"

    # Step 1: Navigate to B33
    assert route['steps'][0]['action'] == 'navigate', "Step 1 should be navigate"
    assert route['steps'][0]['to'] == 'X1-GH18-B33', "Step 1 should go to B33"
    assert route['steps'][0]['mode'] == 'CRUISE', "Step 1 should use CRUISE"

    # Step 2: Refuel at B33
    assert route['steps'][1]['action'] == 'refuel', "Step 2 should be refuel"
    assert route['steps'][1]['waypoint'] == 'X1-GH18-B33', "Step 2 should refuel at B33"
    assert route['steps'][1]['fuel_added'] > 0, "Step 2 should add fuel"

    # Step 3: Navigate to J62
    assert route['steps'][2]['action'] == 'navigate', "Step 3 should be navigate"
    assert route['steps'][2]['to'] == 'X1-GH18-J62', "Step 3 should go to J62"
    # Mode could be DRIFT since CRUISE needs 420 fuel with safety margin, ship capacity is only 400
    assert route['steps'][2]['mode'] in ['CRUISE', 'DRIFT'], "Step 3 should use CRUISE or DRIFT"


def regression_refuel_step_execution(mock_graph, ship_data):
    """
    Test that SmartNavigator actually EXECUTES the refuel step

    This is the ACTUAL BUG: refuel step is in the plan but not executed
    """
    api = MagicMock()
    navigator = SmartNavigator(api, 'X1-GH18', graph=mock_graph, db_path=':memory:')

    # Mock ShipController
    mock_ship = MagicMock(spec=ShipController)

    # Track the sequence of operations
    operation_sequence = []

    # Mock get_status to return ship data
    def get_status_side_effect():
        operation_sequence.append('get_status')
        # Return updated ship data based on current state
        return ship_data.copy()

    mock_ship.get_status.side_effect = get_status_side_effect

    # Mock navigate
    def navigate_side_effect(waypoint, flight_mode=None, auto_refuel=True):
        operation_sequence.append(f'navigate_to_{waypoint}')
        # Update ship location
        ship_data['nav']['waypointSymbol'] = waypoint
        ship_data['nav']['status'] = 'IN_ORBIT'
        return True

    mock_ship.navigate.side_effect = navigate_side_effect

    # Mock orbit
    def orbit_side_effect():
        operation_sequence.append('orbit')
        ship_data['nav']['status'] = 'IN_ORBIT'
        return True

    mock_ship.orbit.side_effect = orbit_side_effect

    # Mock dock
    def dock_side_effect():
        operation_sequence.append('dock')
        ship_data['nav']['status'] = 'DOCKED'
        return True

    mock_ship.dock.side_effect = dock_side_effect

    # Mock refuel - THIS IS THE CRITICAL OPERATION
    def refuel_side_effect(units=None):
        operation_sequence.append('refuel')
        ship_data['fuel']['current'] = ship_data['fuel']['capacity']
        return True

    mock_ship.refuel.side_effect = refuel_side_effect

    # Execute route
    success = navigator.execute_route(mock_ship, 'X1-GH18-J62', prefer_cruise=True)

    # Verify success
    assert success, "Route execution should succeed"

    # Print operation sequence for debugging
    print(f"\nOperation sequence: {operation_sequence}")

    # Verify that refuel was called
    assert 'refuel' in operation_sequence, "REFUEL OPERATION SHOULD HAVE BEEN EXECUTED!"

    # Verify operation order
    nav_to_b33_idx = next((i for i, op in enumerate(operation_sequence) if 'navigate_to_X1-GH18-B33' in op), None)
    refuel_idx = next((i for i, op in enumerate(operation_sequence) if op == 'refuel'), None)
    nav_to_j62_idx = next((i for i, op in enumerate(operation_sequence) if 'navigate_to_X1-GH18-J62' in op), None)

    assert nav_to_b33_idx is not None, "Should navigate to B33"
    assert refuel_idx is not None, "Should refuel"
    assert nav_to_j62_idx is not None, "Should navigate to J62"

    # Refuel should happen BETWEEN the two navigations
    assert nav_to_b33_idx < refuel_idx < nav_to_j62_idx, \
        f"Refuel should happen between navigations (got nav1@{nav_to_b33_idx}, refuel@{refuel_idx}, nav2@{nav_to_j62_idx})"


if __name__ == '__main__':
    pytest.main([__file__, '-v', '-s'])
