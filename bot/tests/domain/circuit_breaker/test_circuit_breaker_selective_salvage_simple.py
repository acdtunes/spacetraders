"""
Simplified test for circuit breaker selective salvage bug fix

CRITICAL BUG: When circuit breaker detects unprofitable BUY, it dumps ALL cargo,
even cargo destined for profitable future segments.

This test validates the fix: _cleanup_stranded_cargo now accepts unprofitable_item
parameter and only salvages that item, keeping other cargo intact.
"""

import pytest
from unittest.mock import Mock, patch
from spacetraders_bot.operations.multileg_trader import _cleanup_stranded_cargo


def test_cleanup_salvages_only_unprofitable_item():
    """
    Test that _cleanup_stranded_cargo with unprofitable_item parameter
    only salvages that specific item, keeping other cargo intact
    """
    # Mock ship with mixed cargo
    mock_ship = Mock()
    ship_data = {
        'symbol': 'TEST-SHIP',
        'nav': {
            'systemSymbol': 'X1-TEST',
            'waypointSymbol': 'X1-TEST-D41',
            'status': 'DOCKED'
        },
        'cargo': {
            'capacity': 40,
            'units': 40,
            'inventory': [
                {'symbol': 'ELECTRONICS', 'units': 2},         # Unprofitable
                {'symbol': 'SHIP_PLATING', 'units': 18},       # Profitable (segment 3)
                {'symbol': 'ADVANCED_CIRCUITRY', 'units': 20}  # Profitable (segment 4)
            ]
        },
        'fuel': {'current': 400, 'capacity': 400},
        'engine': {'speed': 10}
    }

    # Track sell calls
    sell_calls = []

    def mock_sell(good, units, **kwargs):
        """Track which items are sold"""
        sell_calls.append((good, units))

        # Remove sold item from cargo
        for item in ship_data['cargo']['inventory']:
            if item['symbol'] == good:
                item['units'] -= units
                if item['units'] <= 0:
                    ship_data['cargo']['inventory'].remove(item)
                break

        ship_data['cargo']['units'] = sum(item['units'] for item in ship_data['cargo']['inventory'])

        return {
            'units': units,
            'totalPrice': units * 1000,
            'pricePerUnit': 1000
        }

    mock_ship.get_status = Mock(return_value=ship_data)
    mock_ship.sell = Mock(side_effect=mock_sell)
    mock_ship.dock = Mock(return_value=True)

    # Mock API
    mock_api = Mock()
    mock_api.get_market = Mock(return_value={
        'tradeGoods': [
            {'symbol': 'ELECTRONICS', 'sellPrice': 2000, 'purchasePrice': 2100, 'tradeVolume': 50}
        ]
    })

    # Mock database
    mock_db = Mock()
    mock_conn = Mock()
    mock_conn.__enter__ = Mock(return_value=mock_conn)
    mock_conn.__exit__ = Mock(return_value=None)
    mock_db.connection = Mock(return_value=mock_conn)
    mock_db.get_market_data = Mock(return_value=[{'purchase_price': 2000}])

    # Mock logger
    mock_logger = Mock()

    # Mock route (not really used in this test since we're at current market)
    mock_route = None

    # CRITICAL TEST: Call cleanup with unprofitable_item='ELECTRONICS'
    # Should ONLY salvage ELECTRONICS, keep SHIP_PLATING and ADVANCED_CIRCUITRY
    result = _cleanup_stranded_cargo(
        ship=mock_ship,
        api=mock_api,
        db=mock_db,
        logger=mock_logger,
        route=mock_route,
        current_segment_index=None,
        unprofitable_item='ELECTRONICS'  # KEY FIX: selective salvage
    )

    # ASSERTIONS

    # 1. Function should succeed
    assert result == True, "Cleanup should succeed"

    # 2. ELECTRONICS should be sold (the unprofitable item)
    electronics_sold = any(good == 'ELECTRONICS' for good, units in sell_calls)
    assert electronics_sold, "Unprofitable item (ELECTRONICS) should be salvaged"

    # 3. SHIP_PLATING should NOT be sold (destined for profitable segment 3)
    ship_plating_sold = any(good == 'SHIP_PLATING' for good, units in sell_calls)
    assert not ship_plating_sold, "BUG: SHIP_PLATING should be KEPT for segment 3, not salvaged"

    # 4. ADVANCED_CIRCUITRY should NOT be sold (destined for profitable segment 4)
    advanced_circuitry_sold = any(good == 'ADVANCED_CIRCUITRY' for good, units in sell_calls)
    assert not advanced_circuitry_sold, "BUG: ADVANCED_CIRCUITRY should be KEPT for segment 4, not salvaged"

    # 5. Final cargo should have 38 units (40 - 2 ELECTRONICS)
    final_status = mock_ship.get_status()
    assert final_status['cargo']['units'] == 38, \
        f"Expected 38 units remaining (40 - 2 ELECTRONICS), got {final_status['cargo']['units']}"

    # 6. Final inventory should contain SHIP_PLATING and ADVANCED_CIRCUITRY only
    final_inventory_symbols = {item['symbol'] for item in final_status['cargo']['inventory']}
    assert 'ELECTRONICS' not in final_inventory_symbols, "ELECTRONICS should be gone after salvage"
    assert 'SHIP_PLATING' in final_inventory_symbols, "SHIP_PLATING should remain"
    assert 'ADVANCED_CIRCUITRY' in final_inventory_symbols, "ADVANCED_CIRCUITRY should remain"

    print("\n" + "="*70)
    print("✅ SELECTIVE SALVAGE FIX VALIDATED")
    print("="*70)
    print(f"Items sold: {sell_calls}")
    print(f"Final cargo: {final_status['cargo']['units']} units")
    print(f"Final inventory: {final_inventory_symbols}")
    print("="*70)


def test_cleanup_without_unprofitable_item_salvages_all():
    """
    Test backward compatibility: _cleanup_stranded_cargo without unprofitable_item
    parameter salvages ALL cargo (legacy behavior)
    """
    # Mock ship with mixed cargo
    mock_ship = Mock()
    ship_data = {
        'symbol': 'TEST-SHIP',
        'nav': {
            'systemSymbol': 'X1-TEST',
            'waypointSymbol': 'X1-TEST-D41',
            'status': 'DOCKED'
        },
        'cargo': {
            'capacity': 40,
            'units': 40,
            'inventory': [
                {'symbol': 'ELECTRONICS', 'units': 2},
                {'symbol': 'SHIP_PLATING', 'units': 18},
                {'symbol': 'ADVANCED_CIRCUITRY', 'units': 20}
            ]
        },
        'fuel': {'current': 400, 'capacity': 400},
        'engine': {'speed': 10}
    }

    # Track sell calls
    sell_calls = []

    def mock_sell(good, units, **kwargs):
        """Track which items are sold"""
        sell_calls.append((good, units))

        # Remove sold item from cargo
        for item in ship_data['cargo']['inventory']:
            if item['symbol'] == good:
                item['units'] -= units
                if item['units'] <= 0:
                    ship_data['cargo']['inventory'].remove(item)
                break

        ship_data['cargo']['units'] = sum(item['units'] for item in ship_data['cargo']['inventory'])

        return {
            'units': units,
            'totalPrice': units * 1000,
            'pricePerUnit': 1000
        }

    mock_ship.get_status = Mock(return_value=ship_data)
    mock_ship.sell = Mock(side_effect=mock_sell)
    mock_ship.dock = Mock(return_value=True)

    # Mock API - current market buys everything
    def mock_get_market(system, waypoint):
        return {
            'tradeGoods': [
                {'symbol': 'ELECTRONICS', 'sellPrice': 2000, 'purchasePrice': 2100, 'tradeVolume': 50},
                {'symbol': 'SHIP_PLATING', 'sellPrice': 1500, 'purchasePrice': 1600, 'tradeVolume': 50},
                {'symbol': 'ADVANCED_CIRCUITRY', 'sellPrice': 2000, 'purchasePrice': 2100, 'tradeVolume': 50}
            ]
        }

    mock_api = Mock()
    mock_api.get_market = Mock(side_effect=mock_get_market)

    # Mock database
    mock_db = Mock()
    mock_conn = Mock()
    mock_conn.__enter__ = Mock(return_value=mock_conn)
    mock_conn.__exit__ = Mock(return_value=None)
    mock_db.connection = Mock(return_value=mock_conn)

    def mock_get_market_data(conn, waypoint, good):
        # All goods can be sold at current market
        return [{'purchase_price': 1500}]

    mock_db.get_market_data = Mock(side_effect=mock_get_market_data)

    # Mock logger
    mock_logger = Mock()

    # Call cleanup WITHOUT unprofitable_item (legacy behavior)
    # Should salvage ALL cargo
    result = _cleanup_stranded_cargo(
        ship=mock_ship,
        api=mock_api,
        db=mock_db,
        logger=mock_logger,
        route=None,
        current_segment_index=None
        # NO unprofitable_item parameter = salvage ALL
    )

    # ASSERTIONS

    # 1. Function should succeed
    assert result == True, "Cleanup should succeed"

    # 2. ALL items should be sold (legacy behavior)
    assert len(sell_calls) == 3, f"Expected 3 sell calls (all cargo), got {len(sell_calls)}"

    # 3. Final cargo should be empty
    final_status = mock_ship.get_status()
    assert final_status['cargo']['units'] == 0, \
        f"Expected 0 units remaining (all salvaged), got {final_status['cargo']['units']}"

    print("\n" + "="*70)
    print("✅ BACKWARD COMPATIBILITY VALIDATED")
    print("="*70)
    print(f"Items sold: {sell_calls}")
    print(f"Final cargo: {final_status['cargo']['units']} units (all salvaged as expected)")
    print("="*70)


if __name__ == "__main__":
    test_cleanup_salvages_only_unprofitable_item()
    test_cleanup_without_unprofitable_item_salvages_all()
    print("\n✅ ALL TESTS PASSED: Selective salvage fix validated!")
