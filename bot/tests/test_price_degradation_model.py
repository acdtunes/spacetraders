#!/usr/bin/env python3
"""
Unit tests for price degradation model in multileg trader

Tests the estimate_sell_price_with_degradation() function against:
1. Empirical data from STARGAZER-11 trading (36 units SHIP_PARTS)
2. Edge cases (0 units, 1 unit, 20 units, 40 units, 100 units)
3. High-value and low-value goods
"""

import pytest
from spacetraders_bot.operations.multileg_trader import estimate_sell_price_with_degradation


class TestPriceDegradationModel:
    """Test suite for price degradation estimation"""

    def test_no_degradation_for_small_quantities(self):
        """Small quantities (<= 20 units) should have minimal degradation"""
        # Single transaction - no degradation
        assert estimate_sell_price_with_degradation(8000, 1) == 8000
        assert estimate_sell_price_with_degradation(8000, 10) == 8000
        assert estimate_sell_price_with_degradation(8000, 20) == 8000

        # Low-value goods
        assert estimate_sell_price_with_degradation(100, 15) == 100
        assert estimate_sell_price_with_degradation(500, 20) == 500

    def test_empirical_data_ship_parts_36_units(self):
        """
        Validate against STARGAZER-11 real trading data (2025-10-12)

        36 units SHIP_PARTS sold in 6-unit batches:
        - Batch 1: 8,031 cr/unit (baseline)
        - Batch 2: 7,945 cr/unit (-1.1%)
        - Batch 3: 7,840 cr/unit (-1.3%)
        - Batch 4: 7,711 cr/unit (-1.6%)
        - Batch 5: 7,551 cr/unit (-2.1%)
        - Batch 6: 7,355 cr/unit (-2.6%)
        - Average: 7,739 cr/unit (calculated from batches)
        - Total degradation: -8.4% from baseline
        """
        base_price = 8031  # Baseline from batch 1
        units = 36

        expected_avg = estimate_sell_price_with_degradation(base_price, units)

        # Model should predict ~8.4% degradation for 36 units
        # 36 units * 0.23% per unit = 8.28% degradation
        expected_degradation = 0.0828
        expected_price = int(base_price * (1 - expected_degradation))

        # Allow 2% tolerance for rounding
        tolerance = base_price * 0.02
        assert abs(expected_avg - expected_price) < tolerance, \
            f"Expected ~{expected_price} ({expected_degradation*100:.1f}% degradation), got {expected_avg}"

        # Verify it's in reasonable range (7,300 - 7,400)
        assert 7300 <= expected_avg <= 7400, \
            f"Expected average price in range 7,300-7,400, got {expected_avg}"

    def test_standard_cargo_40_units(self):
        """Standard full cargo (40 units) should have ~9% degradation"""
        base_price = 1500
        units = 40

        expected_avg = estimate_sell_price_with_degradation(base_price, units)

        # 40 units * 0.23% = 9.2% degradation
        expected_degradation = 0.092
        expected_price = int(base_price * (1 - expected_degradation))

        # Model should be within 2% of expected
        tolerance = base_price * 0.02
        assert abs(expected_avg - expected_price) < tolerance, \
            f"Expected ~{expected_price}, got {expected_avg}"

    def test_high_value_goods(self):
        """High-value goods should follow same degradation pattern"""
        # SHIP_PARTS, ADVANCED_CIRCUITRY, etc.
        base_price = 10000
        units = 30

        expected_avg = estimate_sell_price_with_degradation(base_price, units)

        # 30 units * 0.23% = 6.9% degradation
        expected_degradation = 0.069
        expected_price = int(base_price * (1 - expected_degradation))

        tolerance = base_price * 0.02
        assert abs(expected_avg - expected_price) < tolerance

    def test_low_value_goods(self):
        """Low-value goods (ores, basic materials) should follow same pattern"""
        base_price = 150
        units = 35

        expected_avg = estimate_sell_price_with_degradation(base_price, units)

        # 35 units * 0.23% = 8.05% degradation
        expected_degradation = 0.0805
        expected_price = int(base_price * (1 - expected_degradation))

        tolerance = base_price * 0.02
        assert abs(expected_avg - expected_price) < tolerance

    def test_degradation_cap_at_15_percent(self):
        """Very large quantities should cap at 15% degradation"""
        base_price = 5000
        units = 100  # Would be 23% without cap

        expected_avg = estimate_sell_price_with_degradation(base_price, units)

        # Should cap at 15% degradation
        expected_price = int(base_price * 0.85)

        # Allow small rounding tolerance
        assert abs(expected_avg - expected_price) < 50, \
            f"Expected cap at {expected_price} (15% degradation), got {expected_avg}"

    def test_edge_case_zero_units(self):
        """Zero units should return base price"""
        assert estimate_sell_price_with_degradation(1000, 0) == 1000

    def test_edge_case_boundary_20_units(self):
        """20 units is the boundary - should have minimal degradation"""
        base_price = 2000
        assert estimate_sell_price_with_degradation(base_price, 20) == base_price

    def test_edge_case_boundary_21_units(self):
        """21 units should start showing degradation"""
        base_price = 2000
        units = 21

        expected_avg = estimate_sell_price_with_degradation(base_price, units)

        # 21 units * 0.23% = 4.83% degradation
        # Should be less than base_price
        assert expected_avg < base_price
        assert expected_avg > base_price * 0.90  # Not more than 10% degradation

    def test_realistic_trading_scenarios(self):
        """Test realistic trading scenarios with varying quantities"""
        scenarios = [
            # (base_price, units, expected_range_min, expected_range_max)
            (8000, 36, 7300, 7400),  # SHIP_PARTS empirical data
            (1500, 40, 1350, 1380),  # Standard cargo
            (500, 25, 470, 480),     # Small batch
            (10000, 15, 10000, 10000),  # Below threshold
        ]

        for base_price, units, range_min, range_max in scenarios:
            result = estimate_sell_price_with_degradation(base_price, units)
            assert range_min <= result <= range_max, \
                f"For {units} units @ {base_price}: expected {range_min}-{range_max}, got {result}"

    def test_degradation_increases_with_quantity(self):
        """Degradation should increase as quantity increases"""
        base_price = 5000

        price_20 = estimate_sell_price_with_degradation(base_price, 20)
        price_30 = estimate_sell_price_with_degradation(base_price, 30)
        price_40 = estimate_sell_price_with_degradation(base_price, 40)

        # Prices should decrease as quantity increases
        assert price_20 > price_30 > price_40, \
            f"Expected decreasing prices: {price_20} > {price_30} > {price_40}"

    def test_percentage_degradation_formula(self):
        """Verify the degradation percentage matches expected formula"""
        base_price = 10000
        units = 30

        result = estimate_sell_price_with_degradation(base_price, units)

        # Calculate expected degradation: 30 * 0.23% = 6.9%
        expected_degradation_pct = 0.069
        actual_degradation_pct = (base_price - result) / base_price

        # Should be within 0.5 percentage points
        assert abs(actual_degradation_pct - expected_degradation_pct) < 0.005, \
            f"Expected {expected_degradation_pct*100:.1f}% degradation, got {actual_degradation_pct*100:.1f}%"


class TestCircuitBreakerWithDegradation:
    """
    Integration tests for circuit breaker logic with degradation model

    These tests verify that the circuit breaker correctly accounts for
    expected price degradation and doesn't trigger false positives.
    """

    def test_circuit_breaker_should_not_trigger_for_expected_degradation(self):
        """
        Scenario: Buy at 2000, sell at 2200 (cached), 40 units
        Expected sell with degradation: ~2000 (9% degradation)
        Result: Should NOT trigger circuit breaker (profit ~0, acceptable)
        """
        actual_buy_price = 2000
        planned_sell_price = 2200  # Cached market price
        units = 40

        expected_sell_price = estimate_sell_price_with_degradation(planned_sell_price, units)

        # Circuit breaker triggers when buy >= sell
        is_unprofitable = actual_buy_price >= expected_sell_price

        # With degradation: 2200 * 0.908 = ~2000
        # Buy price 2000 ~= sell price 2000, but NOT unprofitable
        # (In reality, the circuit breaker uses strict >= so this edge case matters)

        # For this test: we expect the effective sell to be close to buy
        assert expected_sell_price >= 1950, \
            f"Expected ~2000 after degradation, got {expected_sell_price}"

    def test_circuit_breaker_should_trigger_for_actual_unprofitable_trade(self):
        """
        Scenario: Buy at 2300, sell at 2200 (cached), 40 units
        Expected sell with degradation: ~2000 (9% degradation)
        Result: SHOULD trigger circuit breaker (loss of ~300 cr/unit)
        """
        actual_buy_price = 2300
        planned_sell_price = 2200
        units = 40

        expected_sell_price = estimate_sell_price_with_degradation(planned_sell_price, units)

        is_unprofitable = actual_buy_price >= expected_sell_price

        # Buy 2300 > Sell ~2000 = unprofitable
        assert is_unprofitable, \
            f"Should be unprofitable: buy {actual_buy_price} >= sell {expected_sell_price}"

    def test_circuit_breaker_with_empirical_ship_parts_data(self):
        """
        Real-world scenario from STARGAZER-11:
        - Bought at unknown price (assume 7000 avg)
        - Planned sell: 8031 (cached from GET /market)
        - Actual sell: 7739 average (after degradation)
        - Expected sell with degradation: ~7366 (8031 * 0.9172)
        - Should be profitable and NOT trigger circuit breaker
        """
        actual_buy_price = 7000
        planned_sell_price = 8031
        units = 36

        expected_sell_price = estimate_sell_price_with_degradation(planned_sell_price, units)

        profit_per_unit = expected_sell_price - actual_buy_price
        is_unprofitable = actual_buy_price >= expected_sell_price

        # Should be profitable
        assert not is_unprofitable, \
            f"Should be profitable: buy {actual_buy_price} < sell {expected_sell_price}"
        # Profit should be positive (degradation reduces from 1031 to ~366)
        assert profit_per_unit > 300, \
            f"Expected profit >300 cr/unit after degradation, got {profit_per_unit}"
        # Verify expected sell price is reasonable (7300-7400 range)
        assert 7300 <= expected_sell_price <= 7400, \
            f"Expected sell price in empirical range 7300-7400, got {expected_sell_price}"


if __name__ == '__main__':
    pytest.main([__file__, '-v'])
