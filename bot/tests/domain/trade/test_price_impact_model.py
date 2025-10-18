"""
Test price impact model for batch trading

Based on real-world trade execution data from 2025-10-14:
- SHIP_PLATING purchase at D42 (tradeVolume=6, supply=LIMITED, 18 units)
  - Batch 1: 3,941 cr → Batch 3: 4,580 cr (+16.2% total)
- ASSAULT_RIFLES sale at J55 (WEAK activity, 21 units)
  - Batch 1: 4,554 cr → Batch 2: 4,531 cr (-0.5%)
"""

import pytest


def test_calculate_batch_purchase_cost_ship_plating():
    """
    Test batch purchase price escalation with LIMITED supply

    Real-world data from X1-TX46-D42:
    - Base price: 3,941 cr
    - tradeVolume: 6
    - supply: LIMITED
    - Units to buy: 18

    Expected behavior:
    - Batch 1 (6 units): 3,941 cr/unit
    - Batch 2 (6 units): 4,227 cr/unit (+7.3%)
    - Batch 3 (6 units): 4,580 cr/unit (+16.2%)
    - Total: 75,696 cr (vs 70,938 cr static estimate)
    """
    from spacetraders_bot.core.market_data import calculate_batch_purchase_cost

    base_price = 3941
    units = 18
    trade_volume = 6
    supply = "LIMITED"

    total_cost, breakdown = calculate_batch_purchase_cost(
        base_price=base_price,
        units=units,
        trade_volume=trade_volume,
        supply=supply
    )

    # Expected costs from real data
    expected_total = 75696
    static_estimate = base_price * units  # 70,938

    # Allow 5% tolerance for model accuracy
    tolerance = expected_total * 0.05
    assert abs(total_cost - expected_total) < tolerance, (
        f"Total cost {total_cost:,} should be within 5% of {expected_total:,} "
        f"(actual real-world cost), but got difference of {abs(total_cost - expected_total):,}"
    )

    # Verify it's significantly higher than static estimate
    assert total_cost > static_estimate * 1.05, (
        f"Price impact should increase cost by >5% from static {static_estimate:,} to ~{expected_total:,}"
    )

    # Verify breakdown structure
    assert 'batches' in breakdown
    assert 'price_escalation_pct' in breakdown
    assert len(breakdown['batches']) == 3  # 18 units / 6 per batch


def test_calculate_batch_sale_revenue_assault_rifles():
    """
    Test batch sale price degradation with WEAK activity

    Real-world data from X1-TX46-J55:
    - Base price: 4,554 cr
    - tradeVolume: ~10 (matched batch size)
    - activity: WEAK
    - Units to sell: 21

    Expected behavior:
    - Batch 1 (10 units): 4,554 cr/unit
    - Batch 2 (11 units): 4,531 cr/unit (-0.5%)
    - Minimal degradation due to WEAK activity + matching tradeVolume
    """
    from spacetraders_bot.core.market_data import calculate_batch_sale_revenue

    base_price = 4554
    units = 21
    trade_volume = 10
    activity = "WEAK"

    total_revenue, breakdown = calculate_batch_sale_revenue(
        base_price=base_price,
        units=units,
        trade_volume=trade_volume,
        activity=activity
    )

    # Real data showed minimal degradation, but model accounts for volume excess
    # 21 units / 10 tradeVolume = 2.1x ratio → expect some degradation
    static_estimate = base_price * units  # 95,634

    # Model should show modest degradation (5-15% for 2.1x volume with WEAK activity)
    degradation_pct = ((static_estimate - total_revenue) / static_estimate) * 100
    assert 5 <= degradation_pct <= 15, (
        f"WEAK activity with 2.1x volume excess should show 5-15% degradation, got {degradation_pct:.2f}%"
    )

    # Revenue should be 85-95% of static estimate
    assert 0.85 * static_estimate <= total_revenue <= 0.95 * static_estimate


def test_calculate_batch_sale_revenue_weak_activity_large_volume():
    """
    Test severe price degradation with WEAK activity and large volume

    Hypothetical scenario: Selling SHIP_PLATING at H49
    - Base price: 16,008 cr
    - tradeVolume: 6
    - activity: WEAK (few buyers, prices collapse quickly)
    - Units to sell: 18

    Expected behavior:
    - WEAK activity causes ~2x multiplier on degradation
    - Each batch of 6 units increases supply pressure
    - Total revenue should be ~33% lower than static estimate
    """
    from spacetraders_bot.core.market_data import calculate_batch_sale_revenue

    base_price = 16008
    units = 18
    trade_volume = 6
    activity = "WEAK"

    total_revenue, breakdown = calculate_batch_sale_revenue(
        base_price=base_price,
        units=units,
        trade_volume=trade_volume,
        activity=activity
    )

    static_estimate = base_price * units  # 288,144

    # Model should show severe degradation (30-45% for 3x volume with WEAK activity)
    degradation_pct = ((static_estimate - total_revenue) / static_estimate) * 100
    assert 30 <= degradation_pct <= 45, (
        f"WEAK activity with 3x volume excess should show 30-45% degradation, got {degradation_pct:.2f}%"
    )

    # Revenue should be 55-70% of static estimate (reflecting market flooding)
    assert 0.55 * static_estimate <= total_revenue <= 0.70 * static_estimate


def test_supply_multipliers():
    """Test that supply levels correctly affect price escalation"""
    from spacetraders_bot.core.market_data import calculate_batch_purchase_cost

    base_price = 1000
    units = 30
    trade_volume = 10

    # SCARCE supply should have highest escalation
    scarce_cost, _ = calculate_batch_purchase_cost(base_price, units, trade_volume, "SCARCE")

    # LIMITED supply should be medium
    limited_cost, _ = calculate_batch_purchase_cost(base_price, units, trade_volume, "LIMITED")

    # ABUNDANT supply should be lowest
    abundant_cost, _ = calculate_batch_purchase_cost(base_price, units, trade_volume, "ABUNDANT")

    # Verify ordering
    assert scarce_cost > limited_cost > abundant_cost, (
        f"Price escalation should decrease with supply: SCARCE({scarce_cost}) > LIMITED({limited_cost}) > ABUNDANT({abundant_cost})"
    )

    # SCARCE should be at least 5% more expensive than ABUNDANT
    assert scarce_cost > abundant_cost * 1.05, (
        f"SCARCE({scarce_cost}) should be >5% more expensive than ABUNDANT({abundant_cost})"
    )


def test_activity_multipliers():
    """Test that activity levels correctly affect price degradation"""
    from spacetraders_bot.core.market_data import calculate_batch_sale_revenue

    base_price = 5000
    units = 30
    trade_volume = 10

    # RESTRICTED activity should have steepest degradation
    restricted_revenue, _ = calculate_batch_sale_revenue(base_price, units, trade_volume, "RESTRICTED")

    # WEAK activity should be medium
    weak_revenue, _ = calculate_batch_sale_revenue(base_price, units, trade_volume, "WEAK")

    # STRONG activity should be most stable
    strong_revenue, _ = calculate_batch_sale_revenue(base_price, units, trade_volume, "STRONG")

    # Verify ordering
    assert restricted_revenue < weak_revenue < strong_revenue, (
        f"Price stability should increase with activity: RESTRICTED({restricted_revenue}) < WEAK({weak_revenue}) < STRONG({strong_revenue})"
    )

    # RESTRICTED should lose at least 30% vs STRONG
    assert restricted_revenue < strong_revenue * 0.70


def test_trade_volume_impact():
    """Test that tradeVolume affects batch size and price impact"""
    from spacetraders_bot.core.market_data import calculate_batch_purchase_cost

    base_price = 1000
    units = 20
    supply = "MODERATE"

    # Small tradeVolume = many small batches = higher escalation
    small_volume_cost, small_breakdown = calculate_batch_purchase_cost(
        base_price, units, trade_volume=5, supply=supply
    )

    # Large tradeVolume = fewer batches = lower escalation
    large_volume_cost, large_breakdown = calculate_batch_purchase_cost(
        base_price, units, trade_volume=20, supply=supply
    )

    # Smaller tradeVolume should result in more batches and higher cost
    assert len(small_breakdown['batches']) > len(large_breakdown['batches'])
    assert small_volume_cost > large_volume_cost

    # Single-batch purchase (tradeVolume >= units) should have minimal escalation
    assert large_volume_cost < base_price * units * 1.10, (
        f"Single batch purchase should have <10% escalation"
    )


def test_price_impact_breakdown_structure():
    """Test that breakdown contains expected diagnostic information"""
    from spacetraders_bot.core.market_data import calculate_batch_purchase_cost

    base_price = 1000
    units = 15
    trade_volume = 5
    supply = "LIMITED"

    total_cost, breakdown = calculate_batch_purchase_cost(
        base_price, units, trade_volume, supply
    )

    # Verify structure
    assert 'batches' in breakdown
    assert 'price_escalation_pct' in breakdown
    assert 'avg_price_per_unit' in breakdown

    # Verify batch details
    for batch in breakdown['batches']:
        assert 'batch_num' in batch
        assert 'units' in batch
        assert 'price_per_unit' in batch
        assert 'total_cost' in batch

    # Verify math consistency
    batch_total = sum(b['total_cost'] for b in breakdown['batches'])
    assert batch_total == total_cost

    avg_price = total_cost / units
    assert abs(breakdown['avg_price_per_unit'] - avg_price) < 1  # Allow rounding error


def test_zero_edge_cases():
    """Test edge cases with zero or invalid values"""
    from spacetraders_bot.core.market_data import calculate_batch_purchase_cost

    # Zero units
    cost, _ = calculate_batch_purchase_cost(1000, 0, 10, "MODERATE")
    assert cost == 0

    # Zero base price
    cost, _ = calculate_batch_purchase_cost(0, 10, 10, "MODERATE")
    assert cost == 0

    # Units less than tradeVolume (single batch)
    cost, breakdown = calculate_batch_purchase_cost(1000, 5, 10, "MODERATE")
    assert len(breakdown['batches']) == 1
    assert cost == 5000  # Minimal escalation for single batch


if __name__ == '__main__':
    pytest.main([__file__, '-v'])
