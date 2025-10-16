"""
Test for circuit breaker cargo cleanup bug

BUG: Trading operations leave cargo stranded on ships when circuit breakers trigger.

EVIDENCE: SILMARETH-D had stranded cargo (20x CLOTHING, 6x SHIP_PARTS) after circuit breaker.

ROOT CAUSE: Circuit breaker exits trading loop without selling cargo currently in ship's hold.

EXPECTED BEHAVIOR: Before any return/exit, ship should:
1. Check if cargo exists
2. Navigate to nearest market
3. Sell ALL cargo (accept any price to recover credits)
4. Log the cleanup with amounts/prices
5. Then return/exit

This test reproduces the bug and validates the fix.
"""

import pytest
from unittest.mock import Mock, MagicMock, patch, call
from spacetraders_bot.operations.multileg_trader import (
    RouteSegment,
    TradeAction,
    MultiLegRoute,
    execute_multileg_route,
)


def regression_circuit_breaker_sells_stranded_cargo_on_buy_price_spike():
    """
    Test that circuit breaker sells stranded cargo when buy price spikes

    Scenario:
    - Ship buys CLOTHING at market A
    - Navigates to market B
    - Discovers buy price for next good spiked >30% (circuit breaker triggers)
    - Circuit breaker should SELL stranded CLOTHING before exiting
    - Ship should end with EMPTY cargo, not stranded goods
    """
    # Create mock ship controller
    mock_ship = Mock()
    mock_ship.get_status = Mock(return_value={
        'nav': {
            'systemSymbol': 'X1-TEST',
            'waypointSymbol': 'X1-TEST-A1',
            'status': 'DOCKED'
        },
        'cargo': {
            'capacity': 40,
            'units': 0,
            'inventory': []
        },
        'fuel': {'current': 400, 'capacity': 400}
    })

    # Track cargo state changes
    cargo_state = {'units': 0, 'inventory': []}

    def update_cargo_after_buy(good, units):
        cargo_state['units'] = units
        cargo_state['inventory'] = [{'symbol': good, 'units': units}]
        mock_ship.get_status.return_value['cargo'] = {
            'capacity': 40,
            'units': units,
            'inventory': [{'symbol': good, 'units': units}]
        }
        return {'units': units, 'totalPrice': units * 1000}

    def update_cargo_after_sell(good, units, **kwargs):
        cargo_state['units'] = 0
        cargo_state['inventory'] = []
        mock_ship.get_status.return_value['cargo'] = {
            'capacity': 40,
            'units': 0,
            'inventory': []
        }
        return {'units': units, 'totalPrice': units * 800}

    mock_ship.buy = Mock(side_effect=update_cargo_after_buy)
    mock_ship.sell = Mock(side_effect=update_cargo_after_sell)
    mock_ship.dock = Mock(return_value=True)
    mock_ship.orbit = Mock(return_value=True)

    # Create mock API client
    mock_api = Mock()
    mock_api.get_agent = Mock(return_value={'credits': 100000})

    # First market check: Normal prices
    # Second market check: SPIKE in buy price (triggers circuit breaker)
    market_call_count = [0]

    def mock_get_market(system, waypoint):
        market_call_count[0] += 1

        # First call: Normal buy price at A1
        if market_call_count[0] == 1:
            return {
                'tradeGoods': [
                    {'symbol': 'CLOTHING', 'sellPrice': 1000, 'purchasePrice': 1200, 'tradeVolume': 50}
                ]
            }
        # Second call: SPIKED buy price at B7 (triggers circuit breaker)
        else:
            return {
                'tradeGoods': [
                    {'symbol': 'SHIP_PARTS', 'sellPrice': 2000, 'purchasePrice': 3000, 'tradeVolume': 50}  # 50% spike!
                ]
            }

    mock_api.get_market = Mock(side_effect=mock_get_market)

    # Create mock navigator
    mock_navigator = Mock()
    mock_navigator.execute_route = Mock(return_value=True)

    # Mock database
    mock_db = Mock()
    mock_conn = Mock()
    mock_cursor = Mock()
    mock_cursor.fetchone = Mock(return_value=None)
    mock_conn.cursor = Mock(return_value=mock_cursor)
    mock_db.connection = Mock(return_value=mock_conn)
    mock_conn.__enter__ = Mock(return_value=mock_conn)
    mock_conn.__exit__ = Mock(return_value=None)

    # Define route that will trigger circuit breaker:
    # Segment 1: A1 → B7, BUY CLOTHING at A1 (succeeds)
    # Segment 2: B7 → C5, try to BUY SHIP_PARTS but price spiked → circuit breaker triggers
    route = MultiLegRoute(
        segments=[
            RouteSegment(
                from_waypoint='X1-TEST-A1',
                to_waypoint='X1-TEST-B7',
                distance=50,
                fuel_cost=55,
                actions_at_destination=[
                    TradeAction('X1-TEST-B7', 'CLOTHING', 'BUY', 20, 1000, 20000)
                ],
                cargo_after={'CLOTHING': 20},
                credits_after=80000,
                cumulative_profit=0
            ),
            RouteSegment(
                from_waypoint='X1-TEST-B7',
                to_waypoint='X1-TEST-C5',
                distance=60,
                fuel_cost=66,
                actions_at_destination=[
                    TradeAction('X1-TEST-C5', 'SHIP_PARTS', 'BUY', 15, 2000, 30000)  # Will spike to 3000!
                ],
                cargo_after={'CLOTHING': 20, 'SHIP_PARTS': 15},
                credits_after=50000,
                cumulative_profit=0
            )
        ],
        total_profit=10000,
        total_distance=110,
        total_fuel_cost=121,
        estimated_time_minutes=60
    )

    # Patch SmartNavigator
    with patch('spacetraders_bot.operations.multileg_trader.SmartNavigator', return_value=mock_navigator):
        result = execute_multileg_route(route, mock_ship, mock_api, mock_db, player_id=1)

    # Verify execution failed (circuit breaker triggered)
    assert result == False, "Route should fail due to buy price spike"

    # CRITICAL: Verify cargo was sold before exit
    # Bug would leave cargo stranded, fix should sell it
    sell_calls = mock_ship.sell.call_args_list

    # Should have at least one sell call for cleanup
    assert len(sell_calls) > 0, "Circuit breaker should sell stranded cargo before exit"

    # Verify CLOTHING was sold (the stranded cargo)
    clothing_sold = False
    for call in sell_calls:
        if len(call[0]) >= 2 and call[0][0] == 'CLOTHING':
            clothing_sold = True
            break

    assert clothing_sold, "Circuit breaker should sell stranded CLOTHING before exit"

    # Verify final cargo state is EMPTY
    final_status = mock_ship.get_status()
    assert final_status['cargo']['units'] == 0, "Ship should have empty cargo after cleanup"
    assert len(final_status['cargo']['inventory']) == 0, "Ship inventory should be empty after cleanup"


def regression_circuit_breaker_sells_stranded_cargo_on_sell_price_crash():
    """
    Test that circuit breaker sells stranded cargo when sell price crashes

    Scenario:
    - Ship buys goods at market A
    - Navigates to market B to sell
    - Discovers sell price crashed >30% (circuit breaker triggers)
    - Circuit breaker should sell at current (crashed) price to recover what it can
    - Ship should end with EMPTY cargo
    """
    mock_ship = Mock()
    mock_ship.get_status = Mock(return_value={
        'nav': {
            'systemSymbol': 'X1-TEST',
            'waypointSymbol': 'X1-TEST-A1',
            'status': 'DOCKED'
        },
        'cargo': {
            'capacity': 40,
            'units': 30,
            'inventory': [{'symbol': 'IRON_ORE', 'units': 30}]
        },
        'fuel': {'current': 400, 'capacity': 400}
    })

    cargo_state = {'units': 30, 'inventory': [{'symbol': 'IRON_ORE', 'units': 30}]}

    def update_cargo_after_buy(good, units):
        cargo_state['units'] = units
        cargo_state['inventory'] = [{'symbol': good, 'units': units}]
        mock_ship.get_status.return_value['cargo'] = {
            'capacity': 40,
            'units': units,
            'inventory': [{'symbol': good, 'units': units}]
        }
        return {'units': units, 'totalPrice': units * 1000}

    def update_cargo_after_sell(good, units, **kwargs):
        cargo_state['units'] -= units
        if cargo_state['units'] == 0:
            cargo_state['inventory'] = []
        mock_ship.get_status.return_value['cargo'] = {
            'capacity': 40,
            'units': cargo_state['units'],
            'inventory': cargo_state['inventory']
        }
        # Accept whatever price for cleanup
        return {'units': units, 'totalPrice': units * 500}  # Crashed price

    mock_ship.buy = Mock(side_effect=update_cargo_after_buy)
    mock_ship.sell = Mock(side_effect=update_cargo_after_sell)
    mock_ship.dock = Mock(return_value=True)

    mock_api = Mock()
    mock_api.get_agent = Mock(return_value={'credits': 100000})

    market_call_count = [0]

    def mock_get_market(system, waypoint):
        # CRASHED sell price (triggers circuit breaker immediately)
        return {
            'tradeGoods': [
                {'symbol': 'IRON_ORE', 'sellPrice': 800, 'purchasePrice': 900, 'tradeVolume': 50}  # 40% crash from expected 1500!
            ]
        }

    mock_api.get_market = Mock(side_effect=mock_get_market)

    mock_navigator = Mock()
    mock_navigator.execute_route = Mock(return_value=True)

    mock_db = Mock()
    mock_conn = Mock()
    mock_cursor = Mock()
    mock_cursor.fetchone = Mock(return_value=None)
    mock_conn.cursor = Mock(return_value=mock_cursor)
    mock_db.connection = Mock(return_value=mock_conn)
    mock_conn.__enter__ = Mock(return_value=mock_conn)
    mock_conn.__exit__ = Mock(return_value=None)

    route = MultiLegRoute(
        segments=[
            RouteSegment(
                from_waypoint='X1-TEST-A1',
                to_waypoint='X1-TEST-B7',
                distance=50,
                fuel_cost=55,
                actions_at_destination=[
                    TradeAction('X1-TEST-B7', 'IRON_ORE', 'SELL', 30, 1500, 45000)  # Will crash to 900!
                ],
                cargo_after={},
                credits_after=145000,
                cumulative_profit=45000
            )
        ],
        total_profit=45000,
        total_distance=50,
        total_fuel_cost=55,
        estimated_time_minutes=30
    )

    with patch('spacetraders_bot.operations.multileg_trader.SmartNavigator', return_value=mock_navigator):
        result = execute_multileg_route(route, mock_ship, mock_api, mock_db, player_id=1)

    # Verify circuit breaker triggered
    assert result == False, "Route should fail due to sell price crash"

    # Verify cargo was sold (even at crashed price to recover what we can)
    sell_calls = mock_ship.sell.call_args_list
    assert len(sell_calls) > 0, "Circuit breaker should sell stranded cargo at crashed price"

    # Verify final cargo is empty
    final_status = mock_ship.get_status()
    assert final_status['cargo']['units'] == 0, "Ship should have empty cargo after cleanup"


def regression_circuit_breaker_sells_stranded_cargo_on_segment_unprofitable():
    """
    Test that circuit breaker sells stranded cargo when segment is unprofitable

    Scenario:
    - Ship executes segment 1 successfully (has cargo from BUY)
    - Segment completes but profit < 0 (circuit breaker triggers)
    - Circuit breaker should sell any remaining cargo before exit
    """
    mock_ship = Mock()
    mock_ship.get_status = Mock(return_value={
        'nav': {
            'systemSymbol': 'X1-TEST',
            'waypointSymbol': 'X1-TEST-A1',
            'status': 'DOCKED'
        },
        'cargo': {
            'capacity': 40,
            'units': 10,  # Has stranded cargo
            'inventory': [{'symbol': 'MEDICINE', 'units': 10}]
        },
        'fuel': {'current': 400, 'capacity': 400}
    })

    def mock_sell(good, units, **kwargs):
        mock_ship.get_status.return_value['cargo'] = {
            'capacity': 40,
            'units': 0,
            'inventory': []
        }
        return {'units': units, 'totalPrice': units * 500}

    mock_ship.sell = Mock(side_effect=mock_sell)
    mock_ship.buy = Mock(return_value={'units': 10, 'totalPrice': 8000})
    mock_ship.dock = Mock(return_value=True)

    mock_api = Mock()
    # Simulate unprofitable segment: Started with 10000, spent 8000, earned 6000 → lost 2000
    mock_api.get_agent = Mock(side_effect=[
        {'credits': 10000},  # Starting credits
        {'credits': 8000}    # After unprofitable segment
    ])

    mock_api.get_market = Mock(return_value={
        'tradeGoods': [
            {'symbol': 'MEDICINE', 'sellPrice': 800, 'purchasePrice': 600, 'tradeVolume': 50}
        ]
    })

    mock_navigator = Mock()
    mock_navigator.execute_route = Mock(return_value=True)

    mock_db = Mock()
    mock_conn = Mock()
    mock_cursor = Mock()
    mock_cursor.fetchone = Mock(return_value=None)
    mock_conn.cursor = Mock(return_value=mock_cursor)
    mock_db.connection = Mock(return_value=mock_conn)
    mock_conn.__enter__ = Mock(return_value=mock_conn)
    mock_conn.__exit__ = Mock(return_value=None)

    # Segment that results in loss
    route = MultiLegRoute(
        segments=[
            RouteSegment(
                from_waypoint='X1-TEST-A1',
                to_waypoint='X1-TEST-B7',
                distance=50,
                fuel_cost=55,
                actions_at_destination=[
                    TradeAction('X1-TEST-B7', 'MEDICINE', 'BUY', 10, 800, 8000),
                    TradeAction('X1-TEST-B7', 'MEDICINE', 'SELL', 10, 600, 6000)
                ],
                cargo_after={},
                credits_after=8000,
                cumulative_profit=-2000  # LOSS!
            )
        ],
        total_profit=-2000,
        total_distance=50,
        total_fuel_cost=55,
        estimated_time_minutes=30
    )

    with patch('spacetraders_bot.operations.multileg_trader.SmartNavigator', return_value=mock_navigator):
        result = execute_multileg_route(route, mock_ship, mock_api, mock_db, player_id=1)

    # Verify circuit breaker triggered
    assert result == False, "Route should fail due to unprofitable segment"

    # Verify stranded cargo was sold
    sell_calls = mock_ship.sell.call_args_list
    assert len(sell_calls) > 0, "Circuit breaker should sell stranded cargo before exit"

    # Verify final cargo is empty
    final_status = mock_ship.get_status()
    assert final_status['cargo']['units'] == 0, "Ship should have empty cargo after cleanup"


