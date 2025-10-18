"""
Test circuit breaker behavior with 100%+ buy-side price spikes

Reproduces STARGAZER-11 scenario where SHIP_PLATING experienced 100%+ price spike.
"""
import pytest
from unittest.mock import Mock, patch
from spacetraders_bot.operations.multileg_trader import (
    execute_multileg_route,
    MultiLegRoute,
    RouteSegment,
    TradeAction,
)


def test_circuit_breaker_100_percent_buy_spike_should_trigger():
    """
    SHIP_PLATING scenario: Planned 1,751 cr → Actual 3,563 cr (+103%)

    Expected: Circuit breaker TRIGGERS before purchase (>30% threshold)
    """
    # Mocks
    api = Mock()
    db = Mock()
    navigator = Mock()
    ship = Mock()

    # Ship status
    ship.get_status.return_value = {
        'symbol': 'STARGAZER-11',
        'cargo': {'capacity': 60, 'units': 0, 'inventory': []},
        'fuel': {'current': 400, 'capacity': 400},
        'nav': {
            'systemSymbol': 'X1-TEST',
            'waypointSymbol': 'X1-TEST-A1',
            'status': 'DOCKED'
        }
    }

    # Agent with credits
    api.get_agent.return_value = {'credits': 500000}

    # Market with 103% price spike
    api.get_market.return_value = {
        'tradeGoods': [{
            'symbol': 'SHIP_PLATING',
            'sellPrice': 3563,  # +103% from planned 1,751
            'purchasePrice': 3000,
            'tradeVolume': 100,
            'supply': 'LIMITED'
        }]
    }

    # Database mocks
    mock_db_conn = Mock()
    mock_db_cursor = Mock()
    mock_db_cursor.fetchone = Mock(return_value=None)
    mock_db_cursor.fetchall = Mock(return_value=[])
    mock_db_conn.cursor = Mock(return_value=mock_db_cursor)
    db.connection = Mock(return_value=mock_db_conn)
    mock_db_conn.__enter__ = Mock(return_value=mock_db_conn)
    mock_db_conn.__exit__ = Mock(return_value=None)

    # Navigator mock
    navigator.execute_route = Mock(return_value=True)

    # Route with PLANNED price from stale cache
    route = MultiLegRoute(
        segments=[
            RouteSegment(
                from_waypoint='X1-TEST-START',
                to_waypoint='X1-TEST-A1',
                distance=50,
                fuel_cost=50,
                actions_at_destination=[
                    TradeAction(
                        waypoint='X1-TEST-A1',
                        good='SHIP_PLATING',
                        action='BUY',
                        units=45,
                        price_per_unit=1751,  # PLANNED (stale)
                        total_value=78795
                    )
                ],
                cargo_after={'SHIP_PLATING': 45},
                credits_after=421205,
                cumulative_profit=0
            )
        ],
        total_profit=50000,
        total_distance=50,
        total_fuel_cost=50,
        estimated_time_minutes=10
    )

    # Execute
    with patch('spacetraders_bot.operations.multileg_trader.SmartNavigator', return_value=navigator):
        result = execute_multileg_route(route, ship, api, db, player_id=1)

    # Assertions
    print(f"\n🔍 Test result: {result}")
    print(f"🔍 ship.buy called: {ship.buy.called}")
    if ship.buy.called:
        print(f"🔍 ship.buy call_args: {ship.buy.call_args_list}")

    # CRITICAL: Circuit breaker should abort on 103% spike
    assert result is False, (
        "Circuit breaker MUST abort on 103% price spike "
        f"(1,751 → 3,563 cr). Result: {result}"
    )

    # Ship.buy() should NEVER be called
    assert not ship.buy.called, (
        f"ship.buy() should NOT be called when price spiked 103%. "
        f"Calls: {ship.buy.call_args_list}"
    )


if __name__ == '__main__':
    pytest.main([__file__, '-xvs'])
