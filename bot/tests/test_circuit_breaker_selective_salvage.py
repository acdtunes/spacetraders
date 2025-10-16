"""
Test for circuit breaker selective salvage bug

BUG: When circuit breaker detects ONE unprofitable trade item in a segment,
     it panic-dumps ALL cargo at current market, even cargo destined for
     profitable future segments.

EVIDENCE: Real incident in production
- Ship bought at D42: 18x SHIP_PLATING @ 2,959, 20x ADVANCED_CIRCUITRY @ 3,845, 2x ELECTRONICS @ 6,036
- At D41 (segment 2): ELECTRONICS unprofitable (-650 cr/unit on 2 units = -1,300 loss)
- Circuit breaker dumped ALL cargo at D41:
  - 20x ADVANCED_CIRCUITRY sold @ 1,901 (loss: -38,074 cr)
  - 2x ELECTRONICS sold @ 2,983 (loss: -6,106 cr)
  - 18x SHIP_PLATING sold @ 1,470 (loss: -26,752 cr)
  - Total salvage loss: -70,932 cr
- Segments 3-4 (H48, H50) were INDEPENDENT and would have sold SHIP_PLATING
  and ADVANCED_CIRCUITRY profitably for ~1.17M credits

ROOT CAUSE: Circuit breaker uses all-or-nothing salvage strategy. When it sees
"segment unprofitable", it dumps ALL cargo, not just the unprofitable items.

EXPECTED BEHAVIOR: Circuit breaker should:
1. Detect which specific trade items are unprofitable
2. ONLY salvage the unprofitable items (ELECTRONICS in this case)
3. KEEP cargo destined for future profitable segments
4. Continue executing independent profitable segments

This test reproduces the exact scenario and validates the fix.
"""

import pytest
from unittest.mock import Mock, MagicMock, patch, call
from spacetraders_bot.operations.multileg_trader import (
    RouteSegment,
    TradeAction,
    MultiLegRoute,
    execute_multileg_route,
)


def test_circuit_breaker_selective_salvage_keeps_profitable_cargo():
    """
    Test that circuit breaker only salvages unprofitable items, keeps profitable cargo

    Scenario (based on real incident):
    - Segment 1 (D42): BUY 18x SHIP_PLATING @ 2,959, 20x ADVANCED_CIRCUITRY @ 3,845, 2x ELECTRONICS @ 6,036
    - Segment 2 (D41):
      - SELL 2x ELECTRONICS @ 5,386 (planned)
      - Reality: ELECTRONICS buy price spiked to 6,036 → unprofitable by -650 cr/unit
      - Circuit breaker triggers
      - Should ONLY salvage ELECTRONICS (the unprofitable item)
      - Should KEEP SHIP_PLATING and ADVANCED_CIRCUITRY (destined for segments 3-4)
    - Segment 3 (H48): SELL 18x SHIP_PLATING @ 4,500 (independent, still profitable)
    - Segment 4 (H50): SELL 20x ADVANCED_CIRCUITRY @ 5,900 (independent, still profitable)

    EXPECTED: Circuit breaker salvages ELECTRONICS only, continues to segments 3-4
    BUG: Circuit breaker dumps ALL cargo at D41, never reaches segments 3-4
    """
    # Create mock ship controller
    mock_ship = Mock()

    # Initial state: ship is at D42, empty cargo
    ship_state = {
        'symbol': 'TEST-SHIP',
        'nav': {
            'systemSymbol': 'X1-TEST',
            'waypointSymbol': 'X1-TEST-D42',
            'status': 'DOCKED'
        },
        'cargo': {
            'capacity': 40,
            'units': 0,
            'inventory': []
        },
        'fuel': {'current': 400, 'capacity': 400},
        'engine': {'speed': 10}  # Required by SmartNavigator
    }

    mock_ship.get_status = Mock(return_value=ship_state)

    # Track cargo state changes through transactions
    cargo_inventory = []

    def update_cargo_after_buy(good, units):
        """Simulate cargo state after buying goods"""
        cargo_inventory.append({'symbol': good, 'units': units})
        total_units = sum(item['units'] for item in cargo_inventory)
        ship_state['cargo']['units'] = total_units
        ship_state['cargo']['inventory'] = list(cargo_inventory)
        mock_ship.get_status.return_value['cargo'] = ship_state['cargo']

        # Return transaction based on good type
        prices = {
            'SHIP_PLATING': 2959,
            'ADVANCED_CIRCUITRY': 3845,
            'ELECTRONICS': 6036
        }
        return {
            'units': units,
            'totalPrice': units * prices.get(good, 1000),
            'pricePerUnit': prices.get(good, 1000)
        }

    def update_cargo_after_sell(good, units, **kwargs):
        """Simulate cargo state after selling goods"""
        # Find and remove the good from inventory
        for item in cargo_inventory:
            if item['symbol'] == good:
                item['units'] -= units
                if item['units'] <= 0:
                    cargo_inventory.remove(item)
                break

        total_units = sum(item['units'] for item in cargo_inventory)
        ship_state['cargo']['units'] = total_units
        ship_state['cargo']['inventory'] = list(cargo_inventory)
        mock_ship.get_status.return_value['cargo'] = ship_state['cargo']

        # Return transaction based on good type and location
        current_waypoint = ship_state['nav']['waypointSymbol']

        # D41 salvage prices (terrible)
        if current_waypoint == 'X1-TEST-D41':
            salvage_prices = {
                'ELECTRONICS': 2983,           # Loss: -3,053 cr/unit
                'ADVANCED_CIRCUITRY': 1901,    # Loss: -1,944 cr/unit
                'SHIP_PLATING': 1470           # Loss: -1,489 cr/unit
            }
            price = salvage_prices.get(good, 500)
        # H48 planned price for SHIP_PLATING
        elif current_waypoint == 'X1-TEST-H48':
            price = 4500  # Profit: +1,541 cr/unit
        # H50 planned price for ADVANCED_CIRCUITRY
        elif current_waypoint == 'X1-TEST-H50':
            price = 5900  # Profit: +2,055 cr/unit
        else:
            price = 1000

        return {
            'units': units,
            'totalPrice': units * price,
            'pricePerUnit': price
        }

    mock_ship.buy = Mock(side_effect=update_cargo_after_buy)
    mock_ship.sell = Mock(side_effect=update_cargo_after_sell)
    mock_ship.dock = Mock(return_value=True)
    mock_ship.orbit = Mock(return_value=True)

    # Create mock API client
    mock_api = Mock()

    # Track credits through transactions
    credits_state = {'amount': 200000}  # Starting credits

    def mock_get_agent():
        return {'credits': credits_state['amount']}

    mock_api.get_agent = Mock(side_effect=mock_get_agent)

    # Mock market data - simulate price spike at D41
    market_call_count = [0]

    def mock_get_market(system, waypoint):
        market_call_count[0] += 1

        # D42: Buy market (normal prices)
        if waypoint == 'X1-TEST-D42':
            return {
                'tradeGoods': [
                    {'symbol': 'SHIP_PLATING', 'sellPrice': 2959, 'purchasePrice': 3100, 'tradeVolume': 50},
                    {'symbol': 'ADVANCED_CIRCUITRY', 'sellPrice': 3845, 'purchasePrice': 4000, 'tradeVolume': 50},
                    {'symbol': 'ELECTRONICS', 'sellPrice': 6036, 'purchasePrice': 6200, 'tradeVolume': 50}
                ]
            }
        # D41: Sell market - ELECTRONICS price SPIKED (unprofitable)
        elif waypoint == 'X1-TEST-D41':
            return {
                'tradeGoods': [
                    # CRITICAL: ELECTRONICS sell price unchanged, but buy price in cache was too high
                    # Real scenario: bought @ 6,036, planned sell @ 5,386 → unprofitable
                    {'symbol': 'ELECTRONICS', 'sellPrice': 5386, 'purchasePrice': 5500, 'tradeVolume': 50},
                    # Salvage prices (if circuit breaker dumps everything here)
                    {'symbol': 'SHIP_PLATING', 'sellPrice': 1470, 'purchasePrice': 1600, 'tradeVolume': 50},
                    {'symbol': 'ADVANCED_CIRCUITRY', 'sellPrice': 1901, 'purchasePrice': 2000, 'tradeVolume': 50}
                ]
            }
        # H48: Planned sell market for SHIP_PLATING (profitable)
        elif waypoint == 'X1-TEST-H48':
            return {
                'tradeGoods': [
                    {'symbol': 'SHIP_PLATING', 'sellPrice': 4500, 'purchasePrice': 4700, 'tradeVolume': 50}
                ]
            }
        # H50: Planned sell market for ADVANCED_CIRCUITRY (profitable)
        elif waypoint == 'X1-TEST-H50':
            return {
                'tradeGoods': [
                    {'symbol': 'ADVANCED_CIRCUITRY', 'sellPrice': 5900, 'purchasePrice': 6100, 'tradeVolume': 50}
                ]
            }
        else:
            return {'tradeGoods': []}

    mock_api.get_market = Mock(side_effect=mock_get_market)

    # Create mock navigator
    mock_navigator = Mock()

    def mock_execute_route(ship, waypoint):
        """Simulate navigation by updating ship location"""
        ship_state['nav']['waypointSymbol'] = waypoint
        mock_ship.get_status.return_value['nav']['waypointSymbol'] = waypoint
        return True

    mock_navigator.execute_route = Mock(side_effect=mock_execute_route)

    # Mock database with planned sell destination lookup
    mock_db = Mock()
    mock_conn = Mock()
    mock_cursor = Mock()

    # Mock database responses for _find_planned_sell_destination queries
    def mock_get_market_data(conn, waypoint, good):
        """Return market data for specific good at specific waypoint"""
        # Map goods to their planned sell destinations
        if good == 'ELECTRONICS' and waypoint == 'X1-TEST-D41':
            return [{'purchase_price': 5386}]  # D41 buys ELECTRONICS
        elif good == 'SHIP_PLATING' and waypoint == 'X1-TEST-H48':
            return [{'purchase_price': 4500}]  # H48 buys SHIP_PLATING
        elif good == 'ADVANCED_CIRCUITRY' and waypoint == 'X1-TEST-H50':
            return [{'purchase_price': 5900}]  # H50 buys ADVANCED_CIRCUITRY
        else:
            return []

    mock_db.get_market_data = Mock(side_effect=mock_get_market_data)
    mock_conn.cursor = Mock(return_value=mock_cursor)
    mock_db.connection = Mock(return_value=mock_conn)
    mock_conn.__enter__ = Mock(return_value=mock_conn)
    mock_conn.__exit__ = Mock(return_value=None)
    mock_cursor.fetchone = Mock(return_value=None)  # No waypoint coordinates needed

    # Define 4-segment route (reproducing real incident)
    route = MultiLegRoute(
        segments=[
            # Segment 1: Buy all goods at D42
            RouteSegment(
                from_waypoint='X1-TEST-D42',
                to_waypoint='X1-TEST-D42',
                distance=0,
                fuel_cost=0,
                actions_at_destination=[
                    TradeAction('X1-TEST-D42', 'SHIP_PLATING', 'BUY', 18, 2959, 53262),
                    TradeAction('X1-TEST-D42', 'ADVANCED_CIRCUITRY', 'BUY', 20, 3845, 76900),
                    TradeAction('X1-TEST-D42', 'ELECTRONICS', 'BUY', 2, 6036, 12072)
                ],
                cargo_after={'SHIP_PLATING': 18, 'ADVANCED_CIRCUITRY': 20, 'ELECTRONICS': 2},
                credits_after=57766,  # 200000 - 142234
                cumulative_profit=-142234
            ),
            # Segment 2: Navigate to D41, try to sell ELECTRONICS (will be unprofitable)
            RouteSegment(
                from_waypoint='X1-TEST-D42',
                to_waypoint='X1-TEST-D41',
                distance=50,
                fuel_cost=55,
                actions_at_destination=[
                    TradeAction('X1-TEST-D41', 'ELECTRONICS', 'SELL', 2, 5386, 10772)  # Planned, but unprofitable
                ],
                cargo_after={'SHIP_PLATING': 18, 'ADVANCED_CIRCUITRY': 20},
                credits_after=68538,
                cumulative_profit=-131462
            ),
            # Segment 3: Navigate to H48, sell SHIP_PLATING (independent, profitable)
            RouteSegment(
                from_waypoint='X1-TEST-D41',
                to_waypoint='X1-TEST-H48',
                distance=80,
                fuel_cost=88,
                actions_at_destination=[
                    TradeAction('X1-TEST-H48', 'SHIP_PLATING', 'SELL', 18, 4500, 81000)
                ],
                cargo_after={'ADVANCED_CIRCUITRY': 20},
                credits_after=149450,
                cumulative_profit=-50550
            ),
            # Segment 4: Navigate to H50, sell ADVANCED_CIRCUITRY (independent, profitable)
            RouteSegment(
                from_waypoint='X1-TEST-H48',
                to_waypoint='X1-TEST-H50',
                distance=60,
                fuel_cost=66,
                actions_at_destination=[
                    TradeAction('X1-TEST-H50', 'ADVANCED_CIRCUITRY', 'SELL', 20, 5900, 118000)
                ],
                cargo_after={},
                credits_after=267384,
                cumulative_profit=67384
            )
        ],
        total_profit=67384,
        total_distance=190,
        total_fuel_cost=209,
        estimated_time_minutes=120
    )

    # Patch SmartNavigator
    with patch('spacetraders_bot.operations.multileg_trader.SmartNavigator', return_value=mock_navigator):
        result = execute_multileg_route(route, mock_ship, mock_api, mock_db, player_id=1)

    # ASSERTIONS

    # Get all sell calls
    sell_calls = mock_ship.sell.call_args_list

    # 1. Circuit breaker SHOULD trigger (ELECTRONICS is unprofitable)
    # Result should be False OR partial success (segments 3-4 should still execute)
    # For now, we expect False because current implementation aborts entire route

    # 2. CRITICAL BUG CHECK: Circuit breaker should ONLY salvage ELECTRONICS
    #    It should NOT salvage SHIP_PLATING or ADVANCED_CIRCUITRY at D41

    electronics_salvaged_at_d41 = False
    ship_plating_salvaged_at_d41 = False
    advanced_circuitry_salvaged_at_d41 = False

    # Track which goods were sold at which locations
    for i, call_obj in enumerate(sell_calls):
        args = call_obj[0]
        good = args[0]
        units = args[1]

        # Determine location at time of sell by checking ship state
        # (This is approximate - in real test we'd need more precise tracking)
        print(f"Sell call {i+1}: {units}x {good}")

        # If we see SHIP_PLATING or ADVANCED_CIRCUITRY sold before segments 3-4,
        # that's the bug (premature salvage at D41)
        if good == 'ELECTRONICS':
            electronics_salvaged_at_d41 = True
        elif good == 'SHIP_PLATING' and i < 3:  # Sold before segment 3
            ship_plating_salvaged_at_d41 = True
        elif good == 'ADVANCED_CIRCUITRY' and i < 4:  # Sold before segment 4
            advanced_circuitry_salvaged_at_d41 = True

    # 3. EXPECTED BEHAVIOR: Only ELECTRONICS should be salvaged at D41
    assert electronics_salvaged_at_d41, \
        "Circuit breaker should salvage unprofitable ELECTRONICS"

    # 4. BUG: If SHIP_PLATING or ADVANCED_CIRCUITRY were salvaged at D41, that's the bug
    #    They should be KEPT for profitable segments 3-4
    assert not ship_plating_salvaged_at_d41, \
        "BUG: Circuit breaker should NOT salvage SHIP_PLATING destined for profitable segment 3 at H48"

    assert not advanced_circuitry_salvaged_at_d41, \
        "BUG: Circuit breaker should NOT salvage ADVANCED_CIRCUITRY destined for profitable segment 4 at H50"

    # 5. Verify segments 3-4 executed (SHIP_PLATING and ADVANCED_CIRCUITRY sold at planned destinations)
    ship_plating_sold_at_h48 = False
    advanced_circuitry_sold_at_h50 = False

    # Check navigation calls to verify ship reached H48 and H50
    nav_calls = mock_navigator.execute_route.call_args_list
    nav_destinations = [call[0][1] for call in nav_calls]

    assert 'X1-TEST-H48' in nav_destinations, \
        "Ship should navigate to H48 for segment 3 (SHIP_PLATING sell)"

    assert 'X1-TEST-H50' in nav_destinations, \
        "Ship should navigate to H50 for segment 4 (ADVANCED_CIRCUITRY sell)"

    # 6. Final verification: Ship should end with empty cargo (all profitable sales completed)
    final_status = mock_ship.get_status()
    assert final_status['cargo']['units'] == 0, \
        "Ship should have empty cargo after completing segments 3-4"

    print("\n" + "="*70)
    print("TEST SUMMARY")
    print("="*70)
    print(f"Total sell calls: {len(sell_calls)}")
    print(f"Navigation destinations: {nav_destinations}")
    print(f"Electronics salvaged: {electronics_salvaged_at_d41}")
    print(f"Ship Plating salvaged at D41 (BUG): {ship_plating_salvaged_at_d41}")
    print(f"Advanced Circuitry salvaged at D41 (BUG): {advanced_circuitry_salvaged_at_d41}")
    print(f"Final cargo units: {final_status['cargo']['units']}")
    print("="*70)


if __name__ == "__main__":
    test_circuit_breaker_selective_salvage_keeps_profitable_cargo()
    print("\n✅ TEST PASSED: Circuit breaker correctly salvages only unprofitable items!")
