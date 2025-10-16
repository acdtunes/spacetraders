"""
Test for cargo cleanup selling at wrong market bug

BUG: Circuit breaker sells cargo at buy market instead of navigating to planned sell destination

EVIDENCE: STARGAZER-11 had 60x ALUMINUM after circuit breaker on segment 0.
Sold at E45 (buy market @ 70 cr/unit) instead of navigating to D42 (sell market @ 558 cr/unit).

ROOT CAUSE: _find_planned_sell_destination searches segments starting from current_segment_index + 1,
but after segment 0 completes with unprofitable BUY, ship is at E45 (segment.to_waypoint).
The SELL action for ALUMINUM is in segment 1 (E45→D42), which helper should find.

ACTUAL ROUTE STRUCTURE:
- Segment 0: D42 → E45, BUY 60x ALUMINUM @ 68 cr/unit (circuit breaker triggers after BUY)
- Segment 1: E45 → D42, SELL 60x ALUMINUM @ 558 cr/unit (never executed due to circuit breaker)

EXPECTED BEHAVIOR:
1. Circuit breaker triggers after segment 0 completes (segment unprofitable)
2. Ship is at E45 with 60x ALUMINUM cargo
3. _cleanup_stranded_cargo() calls _find_planned_sell_destination('ALUMINUM', route, 0)
4. Helper searches segments[1:] and finds SELL action for ALUMINUM at D42
5. Cleanup navigates E45 → D42
6. Sells ALUMINUM at D42 for 558 cr/unit (not 70!)

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


def test_cargo_cleanup_navigates_to_planned_sell_destination():
    """
    Test that cargo cleanup navigates to planned sell destination instead of selling at buy market

    Scenario:
    - Ship at D42, navigates to E45
    - Buys ALUMINUM at E45 for 68 cr/unit (60 units)
    - Segment 0 completes but is unprofitable (circuit breaker triggers)
    - Segment 1 has planned SELL action for ALUMINUM at D42 @ 558 cr/unit
    - Cleanup should navigate E45 → D42 and sell there (NOT sell at E45!)
    """
    # Create mock ship controller
    mock_ship = Mock()

    # Initial state: At D42, empty cargo
    initial_state = {
        'nav': {
            'systemSymbol': 'X1-JB26',
            'waypointSymbol': 'X1-JB26-D42',
            'status': 'DOCKED'
        },
        'cargo': {
            'capacity': 60,
            'units': 0,
            'inventory': []
        },
        'fuel': {'current': 400, 'capacity': 400}
    }

    mock_ship.get_status = Mock(return_value=initial_state.copy())

    # Track navigation calls
    navigation_log = []

    def mock_navigate(ship, destination):
        navigation_log.append(destination)
        # Update ship location after navigation
        mock_ship.get_status.return_value['nav']['waypointSymbol'] = destination
        return True

    # Track cargo state changes
    cargo_state = {'units': 0, 'inventory': []}

    def update_cargo_after_buy(good, units):
        # Simulate ship moving to E45 and buying ALUMINUM
        cargo_state['units'] = units
        cargo_state['inventory'] = [{'symbol': good, 'units': units}]
        mock_ship.get_status.return_value['nav']['waypointSymbol'] = 'X1-JB26-E45'
        mock_ship.get_status.return_value['cargo'] = {
            'capacity': 60,
            'units': units,
            'inventory': [{'symbol': good, 'units': units}]
        }
        return {'units': units, 'totalPrice': units * 68}  # 60 * 68 = 4,080

    sell_log = []  # Track where goods were sold

    def update_cargo_after_sell(good, units, check_market_prices=True):
        # Record where the sale happened
        current_location = mock_ship.get_status.return_value['nav']['waypointSymbol']

        # Determine price based on location
        if current_location == 'X1-JB26-E45':
            # BUG: Selling at buy market (low price)
            price_per_unit = 70
        elif current_location == 'X1-JB26-D42':
            # FIX: Selling at planned sell destination (high price)
            price_per_unit = 558
        else:
            price_per_unit = 100  # Default

        sell_log.append({
            'location': current_location,
            'good': good,
            'units': units,
            'price_per_unit': price_per_unit,
            'total': units * price_per_unit
        })

        cargo_state['units'] = 0
        cargo_state['inventory'] = []
        mock_ship.get_status.return_value['cargo'] = {
            'capacity': 60,
            'units': 0,
            'inventory': []
        }
        return {'units': units, 'totalPrice': units * price_per_unit}

    mock_ship.buy = Mock(side_effect=update_cargo_after_buy)
    mock_ship.sell = Mock(side_effect=update_cargo_after_sell)
    mock_ship.dock = Mock(return_value=True)
    mock_ship.orbit = Mock(return_value=True)

    # Create mock API client
    mock_api = Mock()

    # Agent credits tracking for segment profitability check
    agent_call_count = [0]

    def mock_get_agent():
        agent_call_count[0] += 1
        if agent_call_count[0] == 1:
            # Starting credits
            return {'credits': 100000}
        elif agent_call_count[0] == 2:
            # After segment 0: Spent 4,080 on ALUMINUM, lost money (unprofitable segment)
            # Simulate that segment cost more in fuel/time than it should have earned
            return {'credits': 91505}  # Lost 8,495 credits (segment unprofitable!)
        else:
            # After cleanup
            return {'credits': 95000}

    mock_api.get_agent = Mock(side_effect=mock_get_agent)

    # Market data: E45 buys ALUMINUM cheap, D42 sells expensive
    def mock_get_market(system, waypoint):
        if waypoint == 'X1-JB26-E45':
            return {
                'tradeGoods': [
                    {
                        'symbol': 'ALUMINUM',
                        'sellPrice': 70,  # E45 sells ALUMINUM at 70 (we can sell back for this)
                        'purchasePrice': 68,  # E45 buys ALUMINUM at 68 (we buy from here)
                        'tradeVolume': 100
                    }
                ]
            }
        elif waypoint == 'X1-JB26-D42':
            return {
                'tradeGoods': [
                    {
                        'symbol': 'ALUMINUM',
                        'sellPrice': 558,  # D42 buys ALUMINUM at 558 (PLANNED SELL DESTINATION!)
                        'purchasePrice': 600,
                        'tradeVolume': 100
                    }
                ]
            }
        return {'tradeGoods': []}

    mock_api.get_market = Mock(side_effect=mock_get_market)

    # Create mock navigator
    mock_navigator = Mock()
    mock_navigator.execute_route = Mock(side_effect=mock_navigate)

    # Mock database
    mock_db = Mock()
    mock_conn = Mock()
    mock_cursor = Mock()
    mock_cursor.fetchone = Mock(return_value=None)
    mock_conn.cursor = Mock(return_value=mock_cursor)
    mock_db.connection = Mock(return_value=mock_conn)
    mock_conn.__enter__ = Mock(return_value=mock_conn)
    mock_conn.__exit__ = Mock(return_value=None)

    # Define route matching the actual bug scenario:
    # Route: D42 → E45 → D42
    # Good: ALUMINUM
    # Buy price @ E45: 68 cr/unit
    # Sell price @ D42: 558 cr/unit
    route = MultiLegRoute(
        segments=[
            # Segment 0: D42 → E45, BUY ALUMINUM (circuit breaker triggers after this)
            RouteSegment(
                from_waypoint='X1-JB26-D42',
                to_waypoint='X1-JB26-E45',
                distance=50,
                fuel_cost=55,
                actions_at_destination=[
                    TradeAction('X1-JB26-E45', 'ALUMINUM', 'BUY', 60, 68, 4080)
                ],
                cargo_after={'ALUMINUM': 60},
                credits_after=95920,  # 100000 - 4080 = 95920
                cumulative_profit=-4080  # Segment is unprofitable (triggers circuit breaker)
            ),
            # Segment 1: E45 → D42, SELL ALUMINUM (never executed due to circuit breaker)
            RouteSegment(
                from_waypoint='X1-JB26-E45',
                to_waypoint='X1-JB26-D42',
                distance=50,
                fuel_cost=55,
                actions_at_destination=[
                    TradeAction('X1-JB26-D42', 'ALUMINUM', 'SELL', 60, 558, 33480)
                ],
                cargo_after={},
                credits_after=129400,  # 95920 + 33480 = 129400
                cumulative_profit=29400  # Would be profitable if executed
            )
        ],
        total_profit=29400,
        total_distance=100,
        total_fuel_cost=110,
        estimated_time_minutes=60
    )

    # Patch SmartNavigator creation
    with patch('spacetraders_bot.operations.multileg_trader.SmartNavigator', return_value=mock_navigator):
        result = execute_multileg_route(route, mock_ship, mock_api, mock_db, player_id=1)

    # Verify execution failed (circuit breaker triggered after segment 0)
    assert result == False, "Route should fail due to unprofitable segment 0"

    # CRITICAL: Verify cargo was sold at the PLANNED SELL DESTINATION (D42), not buy market (E45)
    assert len(sell_log) > 0, "Cargo cleanup should have sold ALUMINUM"

    # Find the ALUMINUM sale
    aluminum_sales = [sale for sale in sell_log if sale['good'] == 'ALUMINUM']
    assert len(aluminum_sales) > 0, "Should have sold ALUMINUM during cleanup"

    # BUG CHECK: Was ALUMINUM sold at E45 (buy market, 70 cr/unit)?
    # FIX CHECK: Was ALUMINUM sold at D42 (planned sell destination, 558 cr/unit)?
    aluminum_sale = aluminum_sales[0]

    # DEBUG: Print where it was sold
    print(f"\nALUMINUM sold at: {aluminum_sale['location']}")
    print(f"Price per unit: {aluminum_sale['price_per_unit']} cr")
    print(f"Total revenue: {aluminum_sale['total']} cr")
    print(f"Navigation log: {navigation_log}")

    # ASSERTION: Should navigate to D42 before selling
    assert 'X1-JB26-D42' in navigation_log, "Cleanup should navigate to planned sell destination (D42)"

    # ASSERTION: Should sell at D42, not E45
    assert aluminum_sale['location'] == 'X1-JB26-D42', \
        f"Should sell at planned destination D42, not {aluminum_sale['location']}"

    # ASSERTION: Should get full planned price (558 cr/unit), not buy market price (70 cr/unit)
    assert aluminum_sale['price_per_unit'] == 558, \
        f"Should sell at planned price 558 cr/unit, not {aluminum_sale['price_per_unit']} cr/unit"

    # ASSERTION: Total revenue should be 60 * 558 = 33,480 cr
    assert aluminum_sale['total'] == 33480, \
        f"Should recover 33,480 cr, not {aluminum_sale['total']} cr"

    # Verify final cargo is empty
    final_status = mock_ship.get_status()
    assert final_status['cargo']['units'] == 0, "Ship should have empty cargo after cleanup"
    assert len(final_status['cargo']['inventory']) == 0, "Ship inventory should be empty after cleanup"


if __name__ == '__main__':
    pytest.main([__file__, '-v', '-s'])
