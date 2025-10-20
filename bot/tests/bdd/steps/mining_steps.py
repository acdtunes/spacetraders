"""Step definitions for mining profit calculation scenarios."""

import pytest
from pytest_bdd import given, when, then, parsers, scenarios

# Load all mining scenarios
scenarios('../../features/mining/profit_calculation.feature')

# Constants from test file
YIELD_PROBABILITIES = {
    'RARE_METAL_DEPOSITS': {
        'MERITIUM_ORE': 0.05, 'URANITE_ORE': 0.05, 'SILICON_CRYSTALS': 0.25,
        'QUARTZ_SAND': 0.35, 'ICE_WATER': 0.30,
    },
    'PRECIOUS_METAL_DEPOSITS': {
        'GOLD_ORE': 0.10, 'SILVER_ORE': 0.08, 'PLATINUM_ORE': 0.07,
        'COPPER_ORE': 0.25, 'QUARTZ_SAND': 0.25, 'ICE_WATER': 0.25,
    },
    'COMMON_METAL_DEPOSITS': {
        'ALUMINUM_ORE': 0.35, 'IRON_ORE': 0.30, 'COPPER_ORE': 0.20,
        'QUARTZ_SAND': 0.10, 'ICE_WATER': 0.05,
    },
    'MINERAL_DEPOSITS': {
        'SILICON_CRYSTALS': 0.40, 'QUARTZ_SAND': 0.35, 'ICE_WATER': 0.25,
    },
}

TYPICAL_MARKET_PRICES = {
    'MERITIUM_ORE': 1188, 'URANITE_ORE': 980, 'GOLD_ORE': 117, 'SILVER_ORE': 90,
    'PLATINUM_ORE': 140, 'ALUMINUM_ORE': 55, 'IRON_ORE': 45, 'COPPER_ORE': 40,
    'SILICON_CRYSTALS': 33, 'QUARTZ_SAND': 18, 'ICE_WATER': 12,
}


@pytest.fixture
def mining_context():
    """Shared context for mining scenarios."""
    return {
        'deposit_type': None,
        'cargo_capacity': None,
        'cycle_minutes': None,
        'weighted_price': None,
        'cargo_value': None,
        'profit_per_hour': None,
        'opportunities': {},
    }


def calculate_weighted_average_price(deposit_type: str) -> float:
    """Calculate weighted average sell price based on yield probabilities."""
    probabilities = YIELD_PROBABILITIES[deposit_type]
    weighted_price = 0.0
    for material, probability in probabilities.items():
        price = TYPICAL_MARKET_PRICES.get(material, 0)
        weighted_price += probability * price
    return weighted_price


def calculate_expected_cargo_value(deposit_type: str, cargo_capacity: int = 15) -> int:
    """Calculate expected cargo value based on realistic yield distribution."""
    probabilities = YIELD_PROBABILITIES[deposit_type]
    total_value = 0
    for material, probability in probabilities.items():
        expected_units = cargo_capacity * probability
        price = TYPICAL_MARKET_PRICES.get(material, 0)
        total_value += expected_units * price
    return int(total_value)


@given("realistic yield probabilities for deposit types")
@given("typical market prices for materials")
def setup_constants(mining_context):
    """Yield probabilities and prices are available as constants."""
    pass


@given(parsers.parse('a deposit type of "{deposit_type}"'))
def set_deposit_type(mining_context, deposit_type):
    """Set the deposit type for mining calculations."""
    mining_context['deposit_type'] = deposit_type


@given(parsers.parse('a cargo capacity of {capacity:d} units'))
def set_cargo_capacity(mining_context, capacity):
    """Set cargo capacity."""
    mining_context['cargo_capacity'] = capacity


@given(parsers.parse('a cycle time of {cycle_minutes:f} minutes'))
def set_cycle_time(mining_context, cycle_minutes):
    """Set cycle time."""
    mining_context['cycle_minutes'] = cycle_minutes


@given(parsers.re(r'a (?P<deposit_type_words>\w+_?\w+) opportunity with (?P<distance>\d+) unit distance and (?P<cycle>[\d.]+) minute cycle'))
def set_opportunity(mining_context, deposit_type_words, distance, cycle):
    """Set an opportunity for comparison."""
    # Convert "COMMON_METAL" or "RARE_METAL" to deposit type key
    deposit_type = deposit_type_words.upper()
    if not deposit_type.endswith('_DEPOSITS'):
        deposit_type = deposit_type + '_DEPOSITS'

    cargo = mining_context.get('cargo_capacity', 15)
    revenue = calculate_expected_cargo_value(deposit_type, cargo)
    profit_per_hour = int((revenue * 60) / float(cycle))
    mining_context['opportunities'][deposit_type] = profit_per_hour


@when("I calculate the weighted average price")
def calculate_avg_price(mining_context):
    """Calculate weighted average price."""
    deposit_type = mining_context['deposit_type']
    mining_context['weighted_price'] = calculate_weighted_average_price(deposit_type)


@when("I calculate the expected cargo value")
def calculate_cargo_value(mining_context):
    """Calculate expected cargo value."""
    deposit_type = mining_context['deposit_type']
    capacity = mining_context.get('cargo_capacity', 15)
    mining_context['cargo_value'] = calculate_expected_cargo_value(deposit_type, capacity)


@when("I calculate profit per hour")
def calculate_profit_per_hour(mining_context):
    """Calculate profit per hour."""
    deposit_type = mining_context['deposit_type']
    capacity = mining_context.get('cargo_capacity', 15)
    cycle = mining_context['cycle_minutes']
    revenue = calculate_expected_cargo_value(deposit_type, capacity)
    mining_context['profit_per_hour'] = int((revenue * 60) / cycle)


@when("I calculate profit per hour for both opportunities")
def calculate_both_profits(mining_context):
    """Profit calculations already done in Given steps."""
    pass


@then(parsers.parse('the weighted price should be approximately {expected:d} credits per unit'))
def verify_weighted_price(mining_context, expected):
    """Verify weighted price is approximately correct."""
    actual = mining_context['weighted_price']
    tolerance = max(5, expected * 0.05)  # 5% tolerance
    assert abs(actual - expected) <= tolerance, \
        f"Expected weighted price ~{expected}, got {actual:.2f}"


@then(parsers.parse('the weighted price should be less than {percent:d}% of the best material price'))
def verify_price_vs_best(mining_context, percent):
    """Verify weighted price is significantly less than best material price."""
    deposit_type = mining_context['deposit_type']
    weighted = mining_context['weighted_price']
    best_price = max(TYPICAL_MARKET_PRICES[mat] for mat in YIELD_PROBABILITIES[deposit_type].keys())
    threshold = best_price * (percent / 100.0)
    assert weighted < threshold, \
        f"Weighted price {weighted:.2f} should be <{percent}% of best price {best_price}"


@then(parsers.parse('the cargo value should be approximately {expected:d} credits'))
def verify_cargo_value(mining_context, expected):
    """Verify cargo value is approximately correct."""
    actual = mining_context['cargo_value']
    tolerance = max(100, expected * 0.1)  # 10% tolerance
    assert abs(actual - expected) <= tolerance, \
        f"Expected cargo value ~{expected}, got {actual}"


@then(parsers.parse('the cargo value should be less than {percent:d}% of the buggy calculation'))
def verify_vs_buggy(mining_context, percent):
    """Verify cargo value is much less than buggy calculation."""
    deposit_type = mining_context['deposit_type']
    capacity = mining_context.get('cargo_capacity', 15)
    actual = mining_context['cargo_value']

    # Buggy calculation assumes 100% best material
    best_price = max(TYPICAL_MARKET_PRICES[mat] for mat in YIELD_PROBABILITIES[deposit_type].keys())
    buggy = capacity * best_price
    threshold = buggy * (percent / 100.0)

    assert actual < threshold, \
        f"Realistic value {actual} should be <{percent}% of buggy calculation {buggy}"


@then(parsers.parse('the profit per hour should be between {min_profit:d} and {max_profit:d} credits'))
def verify_profit_range(mining_context, min_profit, max_profit):
    """Verify profit per hour is in expected range."""
    actual = mining_context['profit_per_hour']
    assert min_profit <= actual <= max_profit, \
        f"Profit {actual} should be between {min_profit} and {max_profit}"


@then(parsers.parse('the buggy calculation should overestimate by more than {factor:f}x'))
def verify_overestimate_factor(mining_context, factor):
    """Verify buggy calculation overestimates by expected factor."""
    deposit_type = mining_context['deposit_type']
    capacity = mining_context.get('cargo_capacity', 15)
    cycle = mining_context['cycle_minutes']
    realistic = mining_context['profit_per_hour']

    # Buggy calculation
    best_price = max(TYPICAL_MARKET_PRICES[mat] for mat in YIELD_PROBABILITIES[deposit_type].keys())
    buggy_revenue = capacity * best_price
    buggy_profit = int((buggy_revenue * 60) / cycle)

    actual_factor = buggy_profit / realistic if realistic > 0 else 0
    assert actual_factor > factor, \
        f"Buggy overestimate factor {actual_factor:.1f}x should be >{factor}x"


@then(parsers.re(r'the (?P<deposit1_words>\w+_?\w+) opportunity should have profit at least (?P<factor>\d+)x higher than (?P<deposit2_words>\w+_?\w+)'))
def verify_opportunity_comparison(mining_context, deposit1_words, deposit2_words, factor):
    """Verify one opportunity is significantly more profitable."""
    # Convert words to keys
    deposit1 = deposit1_words.upper()
    if not deposit1.endswith('_DEPOSITS'):
        deposit1 = deposit1 + '_DEPOSITS'
    deposit2 = deposit2_words.upper()
    if not deposit2.endswith('_DEPOSITS'):
        deposit2 = deposit2 + '_DEPOSITS'

    profit1 = mining_context['opportunities'][deposit1]
    profit2 = mining_context['opportunities'][deposit2]
    assert profit1 > profit2 * int(factor), \
        f"{deposit1} profit {profit1} should be >{factor}x {deposit2} profit {profit2}"


@then(parsers.re(r'(?P<deposit_type_words>\w+_?\w+) profit should be between (?P<min_profit>\d+) and (?P<max_profit>\d+) credits per hour'))
def verify_specific_profit_range(mining_context, deposit_type_words, min_profit, max_profit):
    """Verify specific opportunity profit is in expected range."""
    deposit_type = deposit_type_words.upper()
    if not deposit_type.endswith('_DEPOSITS'):
        deposit_type = deposit_type + '_DEPOSITS'

    actual = mining_context['opportunities'][deposit_type]
    assert int(min_profit) <= actual <= int(max_profit), \
        f"{deposit_type} profit {actual} should be between {min_profit} and {max_profit}"
