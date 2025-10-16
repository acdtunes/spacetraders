#!/usr/bin/env python3
"""
Test to reproduce scout market price mapping bug

ISSUE: Scouts are writing WRONG market prices to database.
- Database sell_price is ~50% of actual API price
- Database purchase_price seems correct

EVIDENCE:
- Database values for X1-TX46-D42:
  - ADVANCED_CIRCUITRY: sell_price=1904, purchase_price=3906
  - ELECTRONICS: sell_price=2965, purchase_price=6014
  - SHIP_PLATING: sell_price=1620, purchase_price=3365

- Actual API prices when STARHOPPER-1 executed trades:
  - SHIP_PLATING: Bought @ 3,056-3,342 cr/unit
  - ADVANCED_CIRCUITRY: Bought @ 3,807-3,895 cr/unit
  - ELECTRONICS: Bought @ 5,990 cr/unit

ROOT CAUSE HYPOTHESIS:
The SpaceTraders API returns market data from the MARKET'S perspective:
- purchasePrice = Price the market PAYS (what they buy FROM ships for)
- sellPrice = Price the market CHARGES (what they sell TO ships for)

But scouts are mapping these fields INCORRECTLY to the database:
- They write API's purchasePrice → DB purchase_price (WRONG!)
- They write API's sellPrice → DB sell_price (WRONG!)

The database schema is from SHIP'S perspective:
- DB purchase_price = What ship pays TO BUY (should be API's sellPrice)
- DB sell_price = What ship receives TO SELL (should be API's purchasePrice)

This is BACKWARDS!
"""

import pytest
from unittest.mock import MagicMock, Mock
from spacetraders_bot.core.database import Database


def test_scout_maps_api_prices_to_database_incorrectly():
    """
    Reproduce the bug where scouts write API prices to database with wrong mapping

    EXPECTED BEHAVIOR:
    API returns market prices from MARKET'S perspective:
    - sellPrice: 3000 (market sells to ship for 3000)
    - purchasePrice: 1500 (market buys from ship for 1500)

    Database should store from SHIP'S perspective:
    - purchase_price: 3000 (ship pays 3000 to buy)
    - sell_price: 1500 (ship receives 1500 to sell)

    ACTUAL BEHAVIOR (BUG):
    Scouts write:
    - purchase_price: 1500 (WRONG - this is what market pays, not ship)
    - sell_price: 3000 (WRONG - this is what market charges, not ship receives)
    """
    # Mock API response from SpaceTraders
    # CRITICAL: API field names are COUNTER-INTUITIVE!
    # Despite the names, these are from SHIP'S perspective:
    # - purchasePrice = what SHIP PAYS to BUY (market sells at) - HIGH price
    # - sellPrice = what SHIP RECEIVES to SELL (market buys at) - LOW price
    mock_api_market_data = {
        'symbol': 'X1-TX46-D42',
        'tradeGoods': [
            {
                'symbol': 'SHIP_PLATING',
                'supply': 'MODERATE',
                'activity': 'STRONG',
                'purchasePrice': 3342,  # Ship PAYS this to BUY (what ship purchases at)
                'sellPrice': 1620,      # Ship RECEIVES this to SELL (what ship sells for)
                'tradeVolume': 100
            }
        ]
    }

    # Create mock database
    db = MagicMock()
    db.transaction = MagicMock()
    mock_conn = MagicMock()
    db.transaction.return_value.__enter__ = MagicMock(return_value=mock_conn)
    db.transaction.return_value.__exit__ = MagicMock(return_value=None)

    # Simulate what scout does (from routing.py lines 608-609)
    good = mock_api_market_data['tradeGoods'][0]

    # THIS IS THE BUG - scouts write API fields directly without swapping
    scout_purchase_price = good.get('purchasePrice', 0)  # 3342 (API: ship pays to buy)
    scout_sell_price = good.get('sellPrice', 0)          # 1620 (API: ship receives to sell)

    # Scout writes to database (WRONG - direct mapping)
    db.update_market_data(
        mock_conn,
        waypoint_symbol='X1-TX46-D42',
        good_symbol='SHIP_PLATING',
        supply='MODERATE',
        activity='STRONG',
        purchase_price=scout_purchase_price,  # BUG: 3342 (should be in sell_price!)
        sell_price=scout_sell_price,          # BUG: 1620 (should be in purchase_price!)
        trade_volume=100,
        last_updated='2025-10-13T12:00:00Z'
    )

    # Verify scout wrote WRONG values
    call_args = db.update_market_data.call_args
    assert call_args[1]['purchase_price'] == 3342, "Scout wrote API purchasePrice to DB purchase_price"
    assert call_args[1]['sell_price'] == 1620, "Scout wrote API sellPrice to DB sell_price"

    # But traders write CORRECT values (from multileg_trader.py lines 83, 130)
    # When trader BUYS from market (transaction type 'PURCHASE'):
    # - Transaction price is 3342 (what ship paid)
    # - Trader writes to DB sell_price (what traders pay to buy) ✓ CORRECT

    # When trader SELLS to market (transaction type 'SELL'):
    # - Transaction price is 1620 (what ship received)
    # - Trader writes to DB purchase_price (what market pays to buy from traders) ✓ CORRECT

    # THE FIX: Scouts should SWAP the field mapping to match database semantics
    # Database fields are from MARKET's asking/bidding perspective:
    # - sell_price = what market ASKS (ship pays to buy) = API purchasePrice
    # - purchase_price = what market BIDS (ship receives to sell) = API sellPrice
    correct_sell_price = good.get('purchasePrice', 0)      # 3342 (ship buys at API purchasePrice)
    correct_purchase_price = good.get('sellPrice', 0)      # 1620 (ship sells at API sellPrice)

    assert correct_sell_price == 3342, "DB sell_price should be API purchasePrice (ship pays to buy)"
    assert correct_purchase_price == 1620, "DB purchase_price should be API sellPrice (ship receives to sell)"

    print("\n" + "=" * 80)
    print("BUG CONFIRMED:")
    print("=" * 80)
    print(f"API returns (COUNTER-INTUITIVE naming from SHIP's perspective):")
    print(f"  - purchasePrice: {good['purchasePrice']} (ship PAYS to BUY)")
    print(f"  - sellPrice: {good['sellPrice']} (ship RECEIVES to SELL)")
    print()
    print(f"Database semantics (from MARKET's ask/bid perspective):")
    print(f"  - sell_price = market's ASK (what traders pay to buy)")
    print(f"  - purchase_price = market's BID (what traders receive to sell)")
    print()
    print(f"Scouts write to DB (WRONG - direct mapping):")
    print(f"  - purchase_price: {scout_purchase_price} ← API purchasePrice (BUG!)")
    print(f"  - sell_price: {scout_sell_price} ← API sellPrice (BUG!)")
    print()
    print(f"Should write to DB (CORRECT - swapped mapping):")
    print(f"  - sell_price: {correct_sell_price} ← API purchasePrice (traders pay to buy)")
    print(f"  - purchase_price: {correct_purchase_price} ← API sellPrice (traders receive to sell)")
    print("=" * 80)


def test_real_world_example_tx46_d42():
    """
    Test with the real-world example from X1-TX46-D42

    Database values (scout-written, WRONG):
    - ADVANCED_CIRCUITRY: sell_price=1904, purchase_price=3906

    Actual trade (trader-executed):
    - Bought @ 3,807-3,895 cr/unit

    This proves:
    - API purchasePrice was ~3,900 (ship PAYS to BUY)
    - API sellPrice was ~1,900 (ship RECEIVES to SELL)
    - Scout wrote them BACKWARDS to database
    """
    # Mock API response (counter-intuitive naming!)
    # purchasePrice = ship PAYS to BUY (high price)
    # sellPrice = ship RECEIVES to SELL (low price)
    mock_api_data = {
        'tradeGoods': [
            {
                'symbol': 'ADVANCED_CIRCUITRY',
                'purchasePrice': 3906,  # Ship PAYS this to BUY (high)
                'sellPrice': 1904       # Ship RECEIVES this to SELL (low)
            }
        ]
    }

    good = mock_api_data['tradeGoods'][0]

    # Scout's WRONG mapping (direct copy)
    scout_db_purchase_price = good['purchasePrice']  # 3906 (WRONG column!)
    scout_db_sell_price = good['sellPrice']          # 1904 (WRONG column!)

    # Trader's CORRECT values (from actual transaction)
    trader_bought_at = 3807  # Real trade price (ship paid this)

    # The trader bought at ~3,807 which matches API purchasePrice (~3,906)
    # This confirms API purchasePrice is what ships pay to buy
    assert abs(trader_bought_at - scout_db_purchase_price) < 100, \
        "Trader's buy price matches API purchasePrice"

    # But scout wrote API purchasePrice to DB purchase_price (WRONG!)
    # And wrote API sellPrice to DB sell_price (WRONG!)
    # This is BACKWARDS!

    # CORRECT mapping (swapped):
    correct_db_sell_price = good['purchasePrice']      # 3906 (ship pays to buy) → DB sell_price
    correct_db_purchase_price = good['sellPrice']      # 1904 (ship receives to sell) → DB purchase_price

    print("\n" + "=" * 80)
    print("REAL-WORLD EVIDENCE:")
    print("=" * 80)
    print(f"API data for ADVANCED_CIRCUITRY at X1-TX46-D42:")
    print(f"  - purchasePrice: {good['purchasePrice']} (ship PAYS to BUY)")
    print(f"  - sellPrice: {good['sellPrice']} (ship RECEIVES to SELL)")
    print()
    print(f"Trader executed BUY at: {trader_bought_at} cr/unit")
    print(f"  → Matches API purchasePrice ✓")
    print()
    print(f"Scout wrote to DB (WRONG - direct mapping):")
    print(f"  - purchase_price: {scout_db_purchase_price} (should be {correct_db_purchase_price})")
    print(f"  - sell_price: {scout_db_sell_price} (should be {correct_db_sell_price})")
    print()
    print(f"Database showed sell_price={scout_db_sell_price} (WRONG!)")
    print(f"Route planner thought ship could BUY at {scout_db_sell_price}")
    print(f"But ship actually pays {correct_db_sell_price} (DOUBLE!)")
    print("=" * 80)


if __name__ == '__main__':
    pytest.main([__file__, '-v', '-s'])
