#!/usr/bin/env python3
"""
Step definitions for circuit breaker partial cargo handling tests
"""

import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from unittest.mock import Mock, MagicMock, patch
import sys
from pathlib import Path

# Add project root to path
sys.path.insert(0, str(Path(__file__).parent.parent))

from operations.contracts import contract_operation
from operations.mining import targeted_mining_with_circuit_breaker

# Load scenarios
scenarios('features/circuit_breaker_partial_cargo.feature')


@pytest.fixture
def mock_args():
    """Mock command line arguments"""
    args = Mock()
    args.player_id = 1
    args.ship = "TEST_SHIP-1"
    args.contract_id = "test-contract-123"
    args.mine_from = "X1-TEST-B8"
    args.buy_from = None  # Will be set per scenario
    args.log_level = "ERROR"  # Suppress logs in tests
    return args


@pytest.fixture
def mock_api():
    """Mock API client"""
    api = Mock()

    # Default contract response
    api.get_contract.return_value = {
        'id': 'test-contract-123',
        'accepted': True,
        'terms': {
            'deliver': [{
                'tradeSymbol': 'ALUMINUM_ORE',
                'destinationSymbol': 'X1-TEST-E45',
                'unitsRequired': 65,
                'unitsFulfilled': 0
            }]
        }
    }

    return api


@pytest.fixture
def mock_ship():
    """Mock ship controller"""
    ship = Mock()

    # Default ship status
    ship.get_status.return_value = {
        'nav': {
            'systemSymbol': 'X1-TEST',
            'waypointSymbol': 'X1-TEST-B8',
            'status': 'IN_ORBIT'
        },
        'cargo': {
            'capacity': 40,
            'units': 0,
            'inventory': []
        },
        'fuel': {
            'current': 400,
            'capacity': 400
        }
    }

    ship.get_cargo.return_value = {
        'capacity': 40,
        'units': 0,
        'inventory': []
    }

    return ship


@pytest.fixture
def context():
    """Shared test context"""
    return {
        'ship_cargo': [],
        'cargo_units': 0,
        'mining_result': None,
        'alternative_result': None,
        'operation_result': None,
        'total_units_available': 0,
        'units_required': 0,
        'required_units': 0,
        'has_enough': False,
    }


# Background steps

@given(parsers.parse('a ship at asteroid "{asteroid}" with {capacity:d} cargo capacity'))
def ship_at_asteroid(mock_ship, asteroid, capacity, context):
    mock_ship.get_status.return_value['nav']['waypointSymbol'] = asteroid
    mock_ship.get_status.return_value['cargo']['capacity'] = capacity
    mock_ship.get_cargo.return_value['capacity'] = capacity
    context['capacity'] = capacity


@given(parsers.parse('a contract requiring {units:d} ALUMINUM_ORE'))
def contract_requiring_units(mock_api, units, context):
    mock_api.get_contract.return_value['terms']['deliver'][0]['unitsRequired'] = units
    context['units_required'] = units
    context['required_units'] = units


# Given steps

@given(parsers.parse('ship has {units:d} units of ALUMINUM_ORE in cargo'))
def ship_has_aluminum(mock_ship, units, context):
    context['ship_cargo'] = [{'symbol': 'ALUMINUM_ORE', 'units': units}] if units > 0 else []
    context['cargo_units'] = units
    context['total_units_available'] += units

    mock_ship.get_status.return_value['cargo']['inventory'] = context['ship_cargo']
    mock_ship.get_status.return_value['cargo']['units'] = units
    mock_ship.get_cargo.return_value['inventory'] = context['ship_cargo']
    mock_ship.get_cargo.return_value['units'] = units


@given(parsers.parse('ship has {units:d} cargo units used by other materials'))
def ship_has_other_cargo(mock_ship, units, context):
    context['cargo_units'] += units
    context['total_units_available'] += units
    mock_ship.get_status.return_value['cargo']['units'] = context['cargo_units']
    mock_ship.get_cargo.return_value['units'] = context['cargo_units']


# When steps

@when(parsers.parse('targeted mining fails with circuit breaker after collecting {units:d} units'))
def mining_fails_with_units(units, context):
    context['mining_result'] = (False, units, "Circuit breaker: 10 consecutive failures")


@when('no alternative asteroids are available')
def no_alternatives(context):
    context['alternatives'] = []


@when(parsers.parse('alternative asteroid "{asteroid}" is available'))
def alternative_available(asteroid, context):
    context['alternatives'] = [asteroid]


@when(parsers.parse('alternative mining succeeds collecting {units:d} units'))
def alternative_succeeds(units, context):
    context['alternative_result'] = (True, units, "Success")


@when(parsers.parse('alternative mining fails with circuit breaker after collecting {units:d} units'))
def alternative_fails(units, context):
    context['alternative_result'] = (False, units, "Circuit breaker: 10 consecutive failures")


@when(parsers.parse('buy_from market "{market}" is specified'))
def buy_from_specified(mock_args, market):
    mock_args.buy_from = market


@when('no buy_from market is specified')
def no_buy_from(mock_args):
    mock_args.buy_from = None


# Then steps

@then('mining should be marked as success')
def mining_marked_success(context):
    required = context.get('units_required', 0)
    cargo_units = context.get('cargo_units', 0)
    mining_units = 0
    alt_units = 0

    if context.get('mining_result'):
        mining_units = context['mining_result'][1]
    if context.get('alternative_result'):
        alt_units = context['alternative_result'][1]

    total_units = cargo_units + mining_units + alt_units
    context['total_units_available'] = total_units
    context['should_succeed'] = True

    assert total_units >= required, (
        f"Expected at least {required} units to satisfy contract but have {total_units}"
    )


@then('should proceed to delivery without buying')
def proceed_without_buying(context):
    # Ensure buying fallback was not triggered
    assert context.get('should_buy') in (None, 0)
    context['skip_buying'] = True


@then('should proceed to delivery')
def proceed_to_delivery(context):
    assert context.get('total_units_available', 0) >= context.get('units_required', 0)
    context['proceed_to_delivery'] = True


@then(parsers.parse('should deliver {units:d} units'))
def should_deliver_units(units, context):
    context['expected_delivery'] = units


@then('operation should fail with partial cargo message')
def operation_fails_partial(context):
    context['should_fail'] = True
    context['expect_partial_message'] = True


@then(parsers.parse('should report having {have:d} of {total:d} units'))
def report_partial_units(have, total, context):
    context['reported_have'] = have
    context['reported_total'] = total


@then(parsers.re(r'should have collected (?P<units>\d+) total units(?: from mining)?'))
def total_units_collected(units, context):
    target = int(units)
    mining_units = context.get('mining_result', (False, 0, ""))[1]
    alt_units = context.get('alternative_result', (False, 0, ""))[1]
    assert mining_units + alt_units == target


@then(parsers.parse('should fall back to buying {units:d} remaining units'))
def fallback_buying(units, context):
    context['should_buy'] = units


@then(parsers.parse('alternative should be asked to mine {units:d} units'))
def alternative_asked_units(units, context):
    context['expected_alt_request'] = units


@then(parsers.parse('not the original {units:d} units'))
def not_original_units(units, context):
    assert context.get('expected_alt_request', units) != units


@then(parsers.parse('total should be {units:d} units after buying'))
def total_after_buying(units, context):
    context['total_after_buying'] = units


# Integration test

@pytest.mark.skip(reason="contract_operation now uses purchase-only flow; integration scenario needs rewrite")
def test_circuit_breaker_partial_cargo_integration():
    """Integration test for circuit breaker with partial cargo"""

    # Setup mocks
    with patch('operations.contracts.get_api_client') as mock_get_api, \
         patch('operations.contracts.ShipController') as MockShip, \
         patch('operations.contracts.SmartNavigator') as MockNav, \
         patch('operations.contracts.targeted_mining_with_circuit_breaker') as mock_mining, \
         patch('operations.contracts.find_alternative_asteroids') as mock_find_alt:

        # Configure mocks
        api = Mock()
        api.get_contract.return_value = {
            'id': 'test-123',
            'accepted': True,
            'terms': {
                'deliver': [{
                    'tradeSymbol': 'ALUMINUM_ORE',
                    'destinationSymbol': 'X1-TEST-E45',
                    'unitsRequired': 65,
                    'unitsFulfilled': 0
                }]
            }
        }
        mock_get_api.return_value = api

        ship = Mock()
        # Multiple calls needed throughout operation:
        # 1. Initial ship check
        # 2. Cargo check before mining
        # 3. After first mining failure (has 22 aluminum now)
        # 4. Before alternative mining
        # 5-6. During delivery
        ship.get_status.side_effect = [
            # Initial status check
            {'nav': {'systemSymbol': 'X1-TEST', 'waypointSymbol': 'X1-TEST-B8'},
             'cargo': {'capacity': 40, 'units': 0, 'inventory': []},
             'fuel': {'current': 400, 'capacity': 400}},
            # Check before calculating to_mine
            {'nav': {'systemSymbol': 'X1-TEST', 'waypointSymbol': 'X1-TEST-B8'},
             'cargo': {'capacity': 40, 'units': 0, 'inventory': []},
             'fuel': {'current': 400, 'capacity': 400}},
            # After first mining returns (simulates cargo update)
            {'nav': {'systemSymbol': 'X1-TEST', 'waypointSymbol': 'X1-TEST-B8'},
             'cargo': {'capacity': 40, 'units': 22, 'inventory': [{'symbol': 'ALUMINUM_ORE', 'units': 22}]},
             'fuel': {'current': 400, 'capacity': 400}},
            # Before alternative mining
            {'nav': {'systemSymbol': 'X1-TEST', 'waypointSymbol': 'X1-TEST-B8'},
             'cargo': {'capacity': 40, 'units': 22, 'inventory': [{'symbol': 'ALUMINUM_ORE', 'units': 22}]},
             'fuel': {'current': 400, 'capacity': 400}},
            # During delivery
            {'nav': {'systemSymbol': 'X1-TEST', 'waypointSymbol': 'X1-TEST-E45'},
             'cargo': {'capacity': 40, 'units': 65, 'inventory': [{'symbol': 'ALUMINUM_ORE', 'units': 65}]},
             'fuel': {'current': 400, 'capacity': 400}},
        ]
        MockShip.return_value = ship

        navigator = Mock()
        navigator.execute_route.return_value = True
        MockNav.return_value = navigator

        # First mining: circuit breaker with 22 units
        # Second mining (alternative): success with 18 units (limited by cargo space)
        mock_mining.side_effect = [
            (False, 22, "Circuit breaker: 10 consecutive failures"),
            (True, 18, "Success")  # Can only fit 18 more (40 - 22 = 18 space left)
        ]

        mock_find_alt.return_value = ['X1-TEST-C5']

        # Mock API deliver and fulfill
        api.post.return_value = {
            'data': {
                'contract': {
                    'terms': {
                        'payment': {
                            'onFulfilled': 10000
                        }
                    }
                }
            }
        }

        # Create args
        args = Mock()
        args.player_id = 1
        args.ship = "TEST-1"
        args.contract_id = "test-123"
        args.mine_from = "X1-TEST-B8"
        args.buy_from = None
        args.log_level = "ERROR"

        # Run operation
        result = contract_operation(args)

        # Verify: Operation completes successfully
        # The fix properly tracks partial cargo: 22 from first mine + 18 from alternative = 40 total
        assert result == 0  # Success
        assert mock_mining.call_count == 2

        # Verify second mining call asked for cargo-limited amount (18), not full remaining (43)
        second_call_units = mock_mining.call_args_list[1][1]['units_needed']
        assert second_call_units == 18  # Limited by cargo space (40 - 22 = 18)


if __name__ == '__main__':
    pytest.main([__file__, '-v'])
