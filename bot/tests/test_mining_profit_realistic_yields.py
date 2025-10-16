#!/usr/bin/env python3
"""
Test cases demonstrating mining profit calculation bug with realistic yield modeling.

BUG: Current implementation assumes 100% yield of best-priced material.
REALITY: Random extraction yields mixed materials based on trait probabilities.

Expected behavior after fix:
- Use weighted average sell price based on material yield probabilities
- Account for mixed cargo containing common/uncommon/rare materials
- Show realistic profit estimates (10-20x lower than current calculation)
"""

import pytest


# ============================================================================
# Material Yield Probabilities (From Game Mechanics)
# ============================================================================

# Yield probabilities by deposit type
# Based on SpaceTraders game mechanics and empirical observation
YIELD_PROBABILITIES = {
    'RARE_METAL_DEPOSITS': {
        'MERITIUM_ORE': 0.05,      # 5% - very rare
        'URANITE_ORE': 0.05,       # 5% - very rare
        'SILICON_CRYSTALS': 0.25,  # 25% - common byproduct
        'QUARTZ_SAND': 0.35,       # 35% - common filler
        'ICE_WATER': 0.30,         # 30% - common filler
    },
    'PRECIOUS_METAL_DEPOSITS': {
        'GOLD_ORE': 0.10,          # 10% - rare
        'SILVER_ORE': 0.08,        # 8% - rare
        'PLATINUM_ORE': 0.07,      # 7% - rare
        'COPPER_ORE': 0.25,        # 25% - common byproduct
        'QUARTZ_SAND': 0.25,       # 25% - common filler
        'ICE_WATER': 0.25,         # 25% - common filler
    },
    'COMMON_METAL_DEPOSITS': {
        'ALUMINUM_ORE': 0.35,      # 35% - common
        'IRON_ORE': 0.30,          # 30% - common
        'COPPER_ORE': 0.20,        # 20% - common
        'QUARTZ_SAND': 0.10,       # 10% - filler
        'ICE_WATER': 0.05,         # 5% - filler
    },
    'MINERAL_DEPOSITS': {
        'SILICON_CRYSTALS': 0.40,  # 40% - common
        'QUARTZ_SAND': 0.35,       # 35% - common
        'ICE_WATER': 0.25,         # 25% - common
    },
}

# Market prices (credits per unit) - typical ranges
TYPICAL_MARKET_PRICES = {
    # Rare metals (high value)
    'MERITIUM_ORE': 1188,
    'URANITE_ORE': 980,

    # Precious metals (medium-high value)
    'GOLD_ORE': 117,
    'SILVER_ORE': 90,
    'PLATINUM_ORE': 140,

    # Common metals (medium value)
    'ALUMINUM_ORE': 55,
    'IRON_ORE': 45,
    'COPPER_ORE': 40,

    # Minerals (low value)
    'SILICON_CRYSTALS': 33,
    'QUARTZ_SAND': 18,
    'ICE_WATER': 12,
}


def calculate_weighted_average_price(deposit_type: str) -> float:
    """
    Calculate weighted average sell price based on yield probabilities.

    This is what the optimizer SHOULD be calculating instead of
    assuming 100% yield of the best material.
    """
    probabilities = YIELD_PROBABILITIES[deposit_type]
    weighted_price = 0.0

    for material, probability in probabilities.items():
        price = TYPICAL_MARKET_PRICES.get(material, 0)
        weighted_price += probability * price

    return weighted_price


def calculate_expected_cargo_value(deposit_type: str, cargo_capacity: int = 15) -> int:
    """
    Calculate expected cargo value based on realistic yield distribution.

    Args:
        deposit_type: Type of deposit (e.g., 'RARE_METAL_DEPOSITS')
        cargo_capacity: Ship cargo capacity (default 15 for probes)

    Returns:
        Expected cargo value in credits
    """
    probabilities = YIELD_PROBABILITIES[deposit_type]
    expected_units = {}

    # Calculate expected units of each material
    for material, probability in probabilities.items():
        expected_units[material] = cargo_capacity * probability

    # Calculate total value
    total_value = 0
    for material, units in expected_units.items():
        price = TYPICAL_MARKET_PRICES.get(material, 0)
        total_value += units * price

    return int(total_value)


# ============================================================================
# Test Cases - Expose Unrealistic Profit Calculations
# ============================================================================

class TestMiningProfitRealisticYields:
    """Test cases demonstrating the profit calculation bug."""

    def test_rare_metal_deposits_weighted_price(self):
        """
        RARE_METAL_DEPOSITS should show realistic weighted average price.

        Bug: Current implementation uses best material price (MERITIUM = 1188 cr)
        Fix: Should use weighted average based on yield probability

        Expected weighted price:
        (0.05 × 1188) + (0.05 × 980) + (0.25 × 33) + (0.35 × 18) + (0.30 × 12)
        = 59.4 + 49.0 + 8.25 + 6.3 + 3.6
        = 126.55 cr/unit (NOT 1188!)
        """
        weighted_price = calculate_weighted_average_price('RARE_METAL_DEPOSITS')

        # Should be around 127 cr/unit (10x lower than assuming pure MERITIUM)
        assert 120 <= weighted_price <= 135, \
            f"Expected weighted price ~127 cr/unit, got {weighted_price}"

        # Current bug: optimizer would use 1188 cr/unit
        best_material_price = TYPICAL_MARKET_PRICES['MERITIUM_ORE']
        assert weighted_price < best_material_price * 0.15, \
            f"Weighted price should be <15% of best material price"

    def test_precious_metal_deposits_weighted_price(self):
        """
        PRECIOUS_METAL_DEPOSITS weighted average price.

        Expected weighted price:
        (0.10 × 117) + (0.08 × 90) + (0.07 × 140) + (0.25 × 40) + (0.25 × 18) + (0.25 × 12)
        = 11.7 + 7.2 + 9.8 + 10.0 + 4.5 + 3.0
        = 46.2 cr/unit (NOT 140!)
        """
        weighted_price = calculate_weighted_average_price('PRECIOUS_METAL_DEPOSITS')

        assert 40 <= weighted_price <= 55, \
            f"Expected weighted price ~46 cr/unit, got {weighted_price}"

        # Should be dramatically lower than assuming pure PLATINUM
        best_material_price = TYPICAL_MARKET_PRICES['PLATINUM_ORE']
        assert weighted_price < best_material_price * 0.4, \
            f"Weighted price should be <40% of best material price"

    def test_common_metal_deposits_weighted_price(self):
        """
        COMMON_METAL_DEPOSITS weighted average price.

        Expected weighted price:
        (0.35 × 55) + (0.30 × 45) + (0.20 × 40) + (0.10 × 18) + (0.05 × 12)
        = 19.25 + 13.5 + 8.0 + 1.8 + 0.6
        = 43.15 cr/unit
        """
        weighted_price = calculate_weighted_average_price('COMMON_METAL_DEPOSITS')

        assert 40 <= weighted_price <= 48, \
            f"Expected weighted price ~43 cr/unit, got {weighted_price}"

    def test_mineral_deposits_weighted_price(self):
        """
        MINERAL_DEPOSITS weighted average price.

        Expected weighted price:
        (0.40 × 33) + (0.35 × 18) + (0.25 × 12)
        = 13.2 + 6.3 + 3.0
        = 22.5 cr/unit
        """
        weighted_price = calculate_weighted_average_price('MINERAL_DEPOSITS')

        assert 20 <= weighted_price <= 26, \
            f"Expected weighted price ~22.5 cr/unit, got {weighted_price}"

    def test_rare_metal_cargo_value_15_capacity(self):
        """
        Test expected cargo value for RARE_METAL_DEPOSITS with 15-unit probe.

        Bug: Current implementation calculates 15 × 1188 = 17,820 credits
        Fix: Should calculate ~15 × 127 = 1,905 credits (9x lower!)

        Example realistic cargo from RARE_METAL_DEPOSITS:
        - 0-1 units MERITIUM_ORE (5% × 15 = 0.75 units)
        - 0-1 units URANITE_ORE (5% × 15 = 0.75 units)
        - 3-4 units SILICON_CRYSTALS (25% × 15 = 3.75 units)
        - 5-6 units QUARTZ_SAND (35% × 15 = 5.25 units)
        - 4-5 units ICE_WATER (30% × 15 = 4.5 units)

        Total value: (1 × 1188) + (1 × 980) + (4 × 33) + (5 × 18) + (4 × 12)
                   = 1188 + 980 + 132 + 90 + 48
                   = 2,438 credits (NOT 17,820!)
        """
        expected_value = calculate_expected_cargo_value('RARE_METAL_DEPOSITS', cargo_capacity=15)

        # Expected value should be around 1,900 credits
        assert 1800 <= expected_value <= 2100, \
            f"Expected cargo value ~1,900 cr, got {expected_value}"

        # Current bug: optimizer calculates 15 × 1188 = 17,820 credits
        buggy_calculation = 15 * TYPICAL_MARKET_PRICES['MERITIUM_ORE']
        assert expected_value < buggy_calculation * 0.15, \
            f"Realistic cargo value should be <15% of buggy calculation"

    def test_profit_per_hour_rare_metal_long_distance(self):
        """
        Test profit/hour calculation for long-distance RARE_METAL route.

        Scenario from bug report:
        - Asteroid: X1-JB26-J80 (RARE_METAL_DEPOSITS)
        - Market: X1-JB26-B7 (800 units away)
        - Ship: Probe (15 cargo, speed 9)
        - DRIFT mode: ~378 minutes travel (one way)

        Cycle time: 5 min mining + 378 min travel + 1 min sell = 384 minutes

        Current bug calculation:
        - Revenue: 15 × 1188 = 17,820 credits
        - Cycles/hour: 60 / 384 = 0.156
        - Profit/hour: 17,820 × 0.156 = 2,779 cr/hr

        Realistic calculation:
        - Revenue: ~1,900 credits (weighted cargo value)
        - Cycles/hour: 60 / 384 = 0.156
        - Profit/hour: 1,900 × 0.156 = 296 cr/hr (9x lower!)
        """
        cargo_capacity = 15
        cycle_time_minutes = 384.6
        distance = 800

        # Buggy calculation (current implementation)
        buggy_revenue = cargo_capacity * TYPICAL_MARKET_PRICES['MERITIUM_ORE']
        buggy_cycles_per_hour = 60 / cycle_time_minutes
        buggy_profit_per_hour = int(buggy_revenue * buggy_cycles_per_hour)

        # Realistic calculation (after fix)
        realistic_revenue = calculate_expected_cargo_value('RARE_METAL_DEPOSITS', cargo_capacity)
        realistic_cycles_per_hour = 60 / cycle_time_minutes
        realistic_profit_per_hour = int(realistic_revenue * realistic_cycles_per_hour)

        # The bug overestimates profit by ~9-10x
        assert buggy_profit_per_hour > realistic_profit_per_hour * 8, \
            f"Bug should overestimate by >8x: buggy={buggy_profit_per_hour}, realistic={realistic_profit_per_hour}"

        # Realistic profit for 800-unit DRIFT route should be very low
        assert realistic_profit_per_hour < 400, \
            f"Long-distance RARE_METAL route should show <400 cr/hr, got {realistic_profit_per_hour}"

    def test_profit_per_hour_precious_metal_short_distance(self):
        """
        Test profit/hour for short-distance PRECIOUS_METAL route.

        Scenario:
        - Asteroid: PRECIOUS_METAL_DEPOSITS
        - Market: 50 units away
        - Ship: Probe (15 cargo, speed 9)
        - DRIFT mode: ~4.6 minutes travel (one way)

        Cycle time: 5 min mining + 9.2 min travel + 1 min sell = 15.2 minutes

        Current bug calculation:
        - Revenue: 15 × 140 (PLATINUM) = 2,100 credits
        - Cycles/hour: 60 / 15.2 = 3.95
        - Profit/hour: 2,100 × 3.95 = 8,295 cr/hr

        Realistic calculation:
        - Revenue: 15 × 46 (weighted) = 690 credits
        - Cycles/hour: 60 / 15.2 = 3.95
        - Profit/hour: 690 × 3.95 = 2,726 cr/hr (3x lower)
        """
        cargo_capacity = 15
        cycle_time_minutes = 15.2

        # Buggy calculation
        buggy_revenue = cargo_capacity * TYPICAL_MARKET_PRICES['PLATINUM_ORE']
        buggy_cycles_per_hour = 60 / cycle_time_minutes
        buggy_profit_per_hour = int(buggy_revenue * buggy_cycles_per_hour)

        # Realistic calculation
        realistic_revenue = calculate_expected_cargo_value('PRECIOUS_METAL_DEPOSITS', cargo_capacity)
        realistic_cycles_per_hour = 60 / cycle_time_minutes
        realistic_profit_per_hour = int(realistic_revenue * realistic_cycles_per_hour)

        # The bug overestimates profit by ~3x for PRECIOUS_METAL
        assert buggy_profit_per_hour > realistic_profit_per_hour * 2.5, \
            f"Bug should overestimate by >2.5x: buggy={buggy_profit_per_hour}, realistic={realistic_profit_per_hour}"

        # Realistic profit for short-distance PRECIOUS_METAL should be moderate
        assert 2500 <= realistic_profit_per_hour <= 3500, \
            f"Short-distance PRECIOUS_METAL route should show 2,500-3,500 cr/hr, got {realistic_profit_per_hour}"

    def test_profit_per_hour_common_metal_medium_distance(self):
        """
        Test profit/hour for medium-distance COMMON_METAL route.

        Scenario:
        - Asteroid: COMMON_METAL_DEPOSITS
        - Market: 150 units away
        - Ship: Probe (15 cargo, speed 9)

        Cycle time: ~5 min mining + 27.8 min travel + 1 min = 33.8 minutes

        Current bug calculation:
        - Revenue: 15 × 55 (ALUMINUM) = 825 credits
        - Cycles/hour: 60 / 33.8 = 1.78
        - Profit/hour: 825 × 1.78 = 1,469 cr/hr

        Realistic calculation:
        - Revenue: 15 × 43 (weighted) = 645 credits
        - Cycles/hour: 60 / 33.8 = 1.78
        - Profit/hour: 645 × 1.78 = 1,148 cr/hr (1.3x lower)
        """
        cargo_capacity = 15
        cycle_time_minutes = 33.8

        # Buggy calculation
        buggy_revenue = cargo_capacity * TYPICAL_MARKET_PRICES['ALUMINUM_ORE']
        buggy_cycles_per_hour = 60 / cycle_time_minutes
        buggy_profit_per_hour = int(buggy_revenue * buggy_cycles_per_hour)

        # Realistic calculation
        realistic_revenue = calculate_expected_cargo_value('COMMON_METAL_DEPOSITS', cargo_capacity)
        realistic_cycles_per_hour = 60 / cycle_time_minutes
        realistic_profit_per_hour = int(realistic_revenue * realistic_cycles_per_hour)

        # The bug overestimates profit by ~1.3x for COMMON_METAL
        assert buggy_profit_per_hour > realistic_profit_per_hour * 1.2, \
            f"Bug should overestimate by >1.2x: buggy={buggy_profit_per_hour}, realistic={realistic_profit_per_hour}"

        # Realistic profit for medium-distance COMMON_METAL should be moderate
        assert 1000 <= realistic_profit_per_hour <= 1400, \
            f"Medium-distance COMMON_METAL route should show 1,000-1,400 cr/hr, got {realistic_profit_per_hour}"


# ============================================================================
# Integration Test - Full Opportunity Ranking
# ============================================================================

class TestMiningOpportunityRanking:
    """Test that opportunities are ranked correctly with realistic yields."""

    def test_short_common_beats_long_rare(self):
        """
        Short-distance COMMON_METAL should rank higher than long-distance RARE_METAL.

        Opportunity A: COMMON_METAL, 50 units away
        - Weighted price: 43 cr/unit
        - Cycle time: ~14 minutes
        - Profit/hour: ~2,757 cr/hr

        Opportunity B: RARE_METAL, 800 units away
        - Weighted price: 127 cr/unit
        - Cycle time: ~385 minutes
        - Profit/hour: ~296 cr/hr

        With realistic yield modeling, Opportunity A should rank MUCH higher.
        Current bug would rank B higher (due to 1188 cr/unit MERITIUM assumption).
        """
        # Opportunity A: Short COMMON_METAL
        cargo_a = 15
        cycle_a = 14.0
        revenue_a = calculate_expected_cargo_value('COMMON_METAL_DEPOSITS', cargo_a)
        profit_a = int((revenue_a * 60) / cycle_a)

        # Opportunity B: Long RARE_METAL
        cargo_b = 15
        cycle_b = 384.6
        revenue_b = calculate_expected_cargo_value('RARE_METAL_DEPOSITS', cargo_b)
        profit_b = int((revenue_b * 60) / cycle_b)

        # Opportunity A should have MUCH higher profit/hour
        assert profit_a > profit_b * 5, \
            f"Short COMMON should beat long RARE by >5x: A={profit_a}, B={profit_b}"

        # Expected values
        assert 2500 <= profit_a <= 3000, f"Opportunity A profit/hr: {profit_a}"
        assert 250 <= profit_b <= 350, f"Opportunity B profit/hr: {profit_b}"


if __name__ == '__main__':
    pytest.main([__file__, '-v'])
