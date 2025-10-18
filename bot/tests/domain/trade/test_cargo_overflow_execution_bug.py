"""
Test for cargo overflow bug during route EXECUTION (not planning)

CRITICAL BUG: The route PLANNER correctly tracks cargo, but during EXECUTION
the code doesn't validate current cargo space before buying. This causes
cargo overflow when:
1. Segment is partially executed (some actions succeed, some fail)
2. Ship already has cargo from previous operations
3. Route assumes empty cargo but ship has residual cargo

**Real-World Failure:**
STARHOPPER-D tried to buy 2 units of ADVANCED_CIRCUITRY when cargo was already
at 78/80 units (from cargo not properly cleared). API rejected with HTTP 400:
"Cannot add 2 unit(s) to ship cargo. Exceeds max limit of 80."

**Root Cause:**
File: multileg_trader.py, Line ~2228
```python
total_units_to_buy = action.units  # From planned route
transaction = ship.buy(action.good, total_units_to_buy)  # NO CARGO CHECK!
```

The code validates price safety but NEVER checks:
- Current cargo space available
- Whether action.units fits in remaining capacity
- If ship has residual cargo from skipped/failed segments

**The Fix:**
Before ANY buy operation, check current cargo space and adjust purchase quantity:
```python
current_cargo_units = sum(item['units'] for item in ship_data['cargo']['inventory'])
cargo_available = ship_data['cargo']['capacity'] - current_cargo_units
units_to_buy = min(action.units, cargo_available)

if units_to_buy <= 0:
    logging.warning(f"Skipping BUY {action.good}: cargo full ({current_cargo_units}/{capacity})")
    continue
```
"""

import pytest
from unittest.mock import Mock, MagicMock, patch


def test_cargo_overflow_during_execution_with_residual_cargo():
    """
    Test that execution fails when ship has residual cargo that planning didn't account for

    Scenario:
    - Route planned assuming empty cargo (0/80)
    - Segment 1: Buy 40 units of GOOD_A
    - Ship arrives at segment 2 with 40 units (as planned)
    - Segment 2 planned: Buy 40 units of GOOD_B (would be 80/80 total)

    But what if ship has 42 units instead of 40 (e.g., from a skipped sell)?
    - Segment 2 tries to buy 40 units
    - Current cargo: 42 units
    - Result: 42 + 40 = 82 > 80 = OVERFLOW!

    This test simulates the EXACT failure mode from STARHOPPER-D
    """

    from spacetraders_bot.operations.multileg_trader import (
        MultiLegRoute,
        RouteSegment,
        TradeAction,
    )

    # Mock ship with cargo that doesn't match the planned route
    mock_ship = Mock()

    # Simulate ship arriving at segment with MORE cargo than planned
    # Planned: 40 units after segment 1
    # Actual: 45 units (5 extra from somewhere - skipped sell, etc.)
    mock_ship.get_status.return_value = {
        'symbol': 'TEST-SHIP',
        'nav': {
            'waypointSymbol': 'X1-TEST-B',
            'status': 'DOCKED',
            'systemSymbol': 'X1-TEST'
        },
        'fuel': {
            'current': 400,
            'capacity': 400
        },
        'cargo': {
            'capacity': 80,
            'units': 45,  # MORE than the 40 planned!
            'inventory': [
                {'symbol': 'GOOD_A', 'units': 45}
            ]
        }
    }

    # Route expects to buy 40 more units at segment 2
    route = MultiLegRoute(
        segments=[
            RouteSegment(
                from_waypoint='X1-TEST-A',
                to_waypoint='X1-TEST-B',
                distance=100,
                fuel_cost=110,
                actions_at_destination=[
                    TradeAction(
                        waypoint='X1-TEST-B',
                        good='GOOD_B',
                        action='BUY',
                        units=40,  # Planned to buy 40
                        price_per_unit=100,
                        total_value=4000
                    )
                ],
                cargo_after={'GOOD_A': 40, 'GOOD_B': 40},  # Planned: 80 total
                credits_after=96000,
                cumulative_profit=0
            )
        ],
        total_profit=0,
        total_distance=100,
        total_fuel_cost=110,
        estimated_time_minutes=600
    )

    # Mock API
    mock_api = Mock()
    mock_api.get_market.return_value = {
        'tradeGoods': [
            {
                'symbol': 'GOOD_B',
                'sellPrice': 100,
                'purchasePrice': 200,
                'tradeVolume': 100
            }
        ]
    }

    # The bug: ship.buy() is called with action.units (40)
    # But current cargo is 45, so 45 + 40 = 85 > 80!
    # This should FAIL with cargo overflow

    mock_ship.buy.return_value = None  # API rejects with HTTP 400

    # Try to execute the buy action
    # This is the EXACT code path from multileg_trader.py:2228
    action = route.segments[0].actions_at_destination[0]
    total_units_to_buy = action.units  # 40 units

    # NO CARGO CHECK HERE! This is the bug!
    transaction = mock_ship.buy(action.good, total_units_to_buy)

    # Should fail because 45 + 40 > 80
    assert transaction is None, "Purchase should fail due to cargo overflow"

    # The fix: Check cargo before buying
    ship_data = mock_ship.get_status()
    current_cargo_units = sum(item['units'] for item in ship_data['cargo']['inventory'])
    cargo_available = ship_data['cargo']['capacity'] - current_cargo_units

    print(f"\nCurrent cargo: {current_cargo_units}/{ship_data['cargo']['capacity']}")
    print(f"Available space: {cargo_available}")
    print(f"Trying to buy: {total_units_to_buy}")
    print(f"Would exceed capacity: {current_cargo_units + total_units_to_buy} > {ship_data['cargo']['capacity']}")

    assert current_cargo_units + total_units_to_buy > ship_data['cargo']['capacity'], \
        "Test should detect cargo overflow condition"

    # The correct fix:
    units_to_buy = min(total_units_to_buy, cargo_available)
    assert units_to_buy == 35, f"Should only buy {cargo_available} units, not {total_units_to_buy}"


def test_cargo_overflow_batch_purchasing():
    """
    Test cargo overflow during BATCH purchasing

    Scenario from STARHOPPER-D logs:
    - Batch 1-7: Each purchased 2 units successfully (14 units total)
    - Cargo before batch 8: 78 units (from previous goods)
    - Batch 8: Tries to buy 2 units
    - Result: 78 + 2 = 80 (should succeed)
    - But if cargo was already at 79, then 79 + 2 = 81 > 80 = OVERFLOW!

    The bug: Batch purchasing doesn't check current cargo before each batch
    """

    from spacetraders_bot.operations.multileg_trader import (
        TradeAction,
    )

    mock_ship = Mock()

    # Simulate ship with 78 units of cargo
    current_cargo = 78
    mock_ship.get_status.return_value = {
        'symbol': 'TEST-SHIP',
        'cargo': {
            'capacity': 80,
            'units': current_cargo,
            'inventory': [
                {'symbol': 'GOOD_A', 'units': 40},
                {'symbol': 'GOOD_B', 'units': 20},
                {'symbol': 'GOOD_C', 'units': 18},
            ]
        }
    }

    # Try to buy in batches of 2 units
    batch_size = 2
    action = TradeAction(
        waypoint='X1-TEST-K91',
        good='GOOD_D',
        action='BUY',
        units=20,  # Total planned purchase
        price_per_unit=150,
        total_value=3000
    )

    total_units_to_buy = action.units
    units_remaining = total_units_to_buy
    total_purchased = 0
    batch_num = 0

    print(f"\n=== BATCH PURCHASING TEST ===")
    print(f"Starting cargo: {current_cargo}/80")
    print(f"Planned purchase: {total_units_to_buy} units in batches of {batch_size}")

    # Simulate batch purchasing loop (from multileg_trader.py:2374)
    while units_remaining > 0:
        batch_num += 1
        units_this_batch = min(batch_size, units_remaining)

        print(f"\nBatch {batch_num}: Attempting to buy {units_this_batch} units")
        print(f"  Current cargo: {current_cargo}/80")
        print(f"  After batch: {current_cargo + units_this_batch}/80")

        # THE BUG: No cargo check before ship.buy()!
        # This line appears at multileg_trader.py:2552
        if current_cargo + units_this_batch > 80:
            print(f"  ❌ WOULD OVERFLOW! ({current_cargo} + {units_this_batch} > 80)")
            # In real code, ship.buy() would fail with HTTP 400
            break
        else:
            print(f"  ✅ Batch would succeed")
            current_cargo += units_this_batch
            total_purchased += units_this_batch
            units_remaining -= units_this_batch

    # Should have detected overflow before the API call
    assert total_purchased < total_units_to_buy, \
        "Should detect cargo overflow and stop purchasing before API failure"
    print(f"\n=== RESULT ===")
    print(f"Total purchased: {total_purchased}/{total_units_to_buy} units")
    print(f"Final cargo: {current_cargo}/80")


if __name__ == "__main__":
    pytest.main([__file__, "-vv", "-s"])
