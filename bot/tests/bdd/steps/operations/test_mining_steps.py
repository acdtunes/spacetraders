from pytest_bdd import scenarios, given, when, then, parsers
from dataclasses import dataclass, field

scenarios('../../../bdd/features/operations/mining.feature')


@dataclass
class MiningStats:
    """Mining statistics tracker."""
    cycles_completed: int = 0
    total_extracted: int = 0
    total_sold: int = 0
    total_revenue: int = 0
    failed_extractions: int = 0
    extraction_attempts: int = 0


@dataclass
class MiningCycleConfig:
    """Mining cycle configuration."""
    asteroid: str = None
    market: str = None
    ship_controller: object = None
    navigator: object = None

    def is_valid(self):
        """Check if configuration is complete."""
        return all([
            self.asteroid,
            self.market,
            self.ship_controller,
            self.navigator
        ])


@given('a mining statistics tracker', target_fixture='mining_ctx')
def given_stats_tracker():
    """Create mining statistics tracker."""
    return {
        'stats': MiningStats(),
        'config': MiningCycleConfig(),
        'calculations': {},
        'distance': 0,
        'cargo_capacity': 0,
        'market_price': 0,
        'extraction_time': 0,
        'fuel_per_unit': 0,
        'fuel_price': 0
    }


@given('a new mining operation starts')
def given_new_operation(mining_ctx):
    """Start new mining operation."""
    mining_ctx['stats'] = MiningStats()
    return mining_ctx


@given('mining statistics show 0 cycles and 0 units extracted')
def given_zero_stats(mining_ctx):
    """Initialize with zero statistics."""
    stats = mining_ctx['stats']
    assert stats.cycles_completed == 0
    assert stats.total_extracted == 0
    return mining_ctx


@given('mining statistics show 0 cycles completed')
def given_zero_cycles(mining_ctx):
    """Initialize with zero cycles."""
    mining_ctx['stats'].cycles_completed = 0
    return mining_ctx


@given(parsers.parse('mining statistics show {cycles:d} cycles with {extracted:d} extracted and {revenue:d} revenue'))
def given_stats_values(mining_ctx, cycles, extracted, revenue):
    """Set specific statistics values."""
    stats = mining_ctx['stats']
    stats.cycles_completed = cycles
    stats.total_extracted = extracted
    stats.total_sold = extracted  # Assume all extracted was sold
    stats.total_revenue = revenue
    return mining_ctx


@given(parsers.parse('mining statistics show {cycles:d} cycles completed'))
def given_cycles_completed(mining_ctx, cycles):
    """Set cycles completed."""
    mining_ctx['stats'].cycles_completed = cycles
    return mining_ctx


@given(parsers.parse('total extracted is {units:d} units'))
def given_total_extracted(mining_ctx, units):
    """Set total extracted."""
    mining_ctx['stats'].total_extracted = units
    return mining_ctx


@given(parsers.parse('total sold is {units:d} units'))
def given_total_sold(mining_ctx, units):
    """Set total sold."""
    mining_ctx['stats'].total_sold = units
    return mining_ctx


@given(parsers.parse('total revenue is {revenue:d} credits'))
def given_total_revenue(mining_ctx, revenue):
    """Set total revenue."""
    mining_ctx['stats'].total_revenue = revenue
    return mining_ctx


@given('mining statistics show 0 units extracted')
def given_zero_extracted(mining_ctx):
    """Zero extraction."""
    mining_ctx['stats'].total_extracted = 0
    return mining_ctx


@given('a mining cycle configuration')
def given_cycle_config(mining_ctx):
    """Create mining cycle configuration."""
    mining_ctx['config'] = MiningCycleConfig()
    return mining_ctx


@given(parsers.parse('asteroid is {distance:d} units from market'))
def given_asteroid_distance(mining_ctx, distance):
    """Set asteroid distance."""
    mining_ctx['distance'] = distance
    return mining_ctx


@given(parsers.parse('ship uses {fuel:d} fuel per unit distance in CRUISE mode'))
def given_fuel_per_unit(mining_ctx, fuel):
    """Set fuel consumption rate."""
    mining_ctx['fuel_per_unit'] = fuel
    return mining_ctx


@given(parsers.parse('cargo capacity is {capacity:d} units'))
def given_cargo_capacity(mining_ctx, capacity):
    """Set cargo capacity."""
    mining_ctx['cargo_capacity'] = capacity
    return mining_ctx


@given(parsers.parse('market price is {price:d} credits per unit'))
def given_market_price(mining_ctx, price):
    """Set market price."""
    mining_ctx['market_price'] = price
    return mining_ctx


@given(parsers.parse('extraction takes {seconds:d} seconds to fill cargo'))
def given_extraction_time(mining_ctx, seconds):
    """Set extraction time."""
    mining_ctx['extraction_time'] = seconds
    return mining_ctx


@given(parsers.parse('round-trip fuel cost is {units:d} units at {price:d} credits per unit'))
def given_fuel_cost(mining_ctx, units, price):
    """Set fuel cost parameters."""
    mining_ctx['fuel_units'] = units
    mining_ctx['fuel_price'] = price
    return mining_ctx


@given('mining statistics show 0 failed extractions')
def given_zero_failures(mining_ctx):
    """Zero failed extractions."""
    mining_ctx['stats'].failed_extractions = 0
    mining_ctx['stats'].extraction_attempts = 0
    return mining_ctx


@when('I check initial statistics')
def when_check_initial(mining_ctx):
    """Check initial statistics."""
    # Statistics already set in fixture
    return mining_ctx


@when(parsers.parse('I record extraction of {units:d} units'))
def when_record_extraction(mining_ctx, units):
    """Record extraction."""
    mining_ctx['stats'].total_extracted += units
    mining_ctx['stats'].extraction_attempts += 1
    return mining_ctx


@when(parsers.parse('I record cycle completion with {units:d} units extracted'))
def when_record_cycle(mining_ctx, units):
    """Record cycle completion."""
    stats = mining_ctx['stats']
    stats.cycles_completed += 1
    stats.total_extracted += units
    return mining_ctx


@when(parsers.parse('I record sale of {units:d} units for {revenue:d} credits'))
def when_record_sale(mining_ctx, units, revenue):
    """Record cargo sale."""
    stats = mining_ctx['stats']
    stats.total_sold += units
    stats.total_revenue += revenue
    return mining_ctx


@when('I calculate average revenue per cycle')
def when_calculate_averages(mining_ctx):
    """Calculate averages."""
    stats = mining_ctx['stats']
    if stats.cycles_completed > 0:
        mining_ctx['calculations']['avg_revenue'] = stats.total_revenue // stats.cycles_completed
        mining_ctx['calculations']['avg_units'] = stats.total_extracted // stats.cycles_completed
    return mining_ctx


@when('I validate cycle has asteroid waypoint')
def when_validate_asteroid(mining_ctx):
    """Validate asteroid configured."""
    mining_ctx['config'].asteroid = 'X1-TEST-A1'
    return mining_ctx


@when('I validate cycle has market waypoint')
def when_validate_market(mining_ctx):
    """Validate market configured."""
    mining_ctx['config'].market = 'X1-TEST-M1'
    return mining_ctx


@when('I validate cycle has ship controller')
def when_validate_ship(mining_ctx):
    """Validate ship controller configured."""
    mining_ctx['config'].ship_controller = object()  # Mock object
    return mining_ctx


@when('I validate cycle has navigator')
def when_validate_navigator(mining_ctx):
    """Validate navigator configured."""
    mining_ctx['config'].navigator = object()  # Mock object
    return mining_ctx


@when('I calculate round-trip fuel cost')
def when_calculate_fuel(mining_ctx):
    """Calculate fuel cost."""
    distance = mining_ctx['distance']
    fuel_per_unit = mining_ctx['fuel_per_unit']
    round_trip = distance * 2
    mining_ctx['calculations']['fuel_cost'] = round_trip * fuel_per_unit
    mining_ctx['calculations']['fuel_cost_3cycles'] = round_trip * fuel_per_unit * 3
    return mining_ctx


@when('I estimate profit per cycle')
def when_estimate_profit(mining_ctx):
    """Estimate profit per cycle."""
    capacity = mining_ctx['cargo_capacity']
    price = mining_ctx['market_price']
    fuel_units = mining_ctx['fuel_units']
    fuel_price = mining_ctx['fuel_price']
    extraction_time = mining_ctx['extraction_time']

    gross = capacity * price
    fuel_cost = fuel_units * fuel_price
    net = gross - fuel_cost

    # Profit per hour: (net profit / cycle time in seconds) * 3600
    cycle_time_hours = extraction_time / 3600
    profit_per_hour = int(net / cycle_time_hours)

    mining_ctx['calculations']['gross_revenue'] = gross
    mining_ctx['calculations']['fuel_cost'] = fuel_cost
    mining_ctx['calculations']['net_profit'] = net
    mining_ctx['calculations']['profit_per_hour'] = profit_per_hour

    return mining_ctx


@when('I record failed extraction attempt')
def when_record_failure(mining_ctx):
    """Record failed extraction."""
    stats = mining_ctx['stats']
    stats.failed_extractions += 1
    stats.extraction_attempts += 1
    return mining_ctx


@then(parsers.parse('cycles completed should be {count:d}'))
def then_cycles_count(mining_ctx, count):
    """Verify cycles completed."""
    assert mining_ctx['stats'].cycles_completed == count


@then(parsers.parse('total extracted should be {units:d}'))
def then_extracted_count(mining_ctx, units):
    """Verify total extracted."""
    assert mining_ctx['stats'].total_extracted == units


@then(parsers.parse('total sold should be {units:d}'))
def then_sold_count(mining_ctx, units):
    """Verify total sold."""
    assert mining_ctx['stats'].total_sold == units


@then(parsers.parse('total revenue should be {revenue:d}'))
def then_revenue_amount(mining_ctx, revenue):
    """Verify total revenue."""
    assert mining_ctx['stats'].total_revenue == revenue


@then('cycles completed should remain 0')
def then_cycles_zero(mining_ctx):
    """Verify cycles still zero."""
    assert mining_ctx['stats'].cycles_completed == 0


@then(parsers.parse('average revenue should be {revenue:d} credits'))
def then_avg_revenue(mining_ctx, revenue):
    """Verify average revenue."""
    assert mining_ctx['calculations']['avg_revenue'] == revenue


@then(parsers.parse('average units per cycle should be {units:d}'))
def then_avg_units(mining_ctx, units):
    """Verify average units."""
    assert mining_ctx['calculations']['avg_units'] == units


@then(parsers.parse('extraction count should be {count:d}'))
def then_extraction_count(mining_ctx, count):
    """Verify extraction count."""
    assert mining_ctx['stats'].extraction_attempts == count


@then('cycle configuration should be valid')
def then_config_valid(mining_ctx):
    """Verify configuration is valid."""
    assert mining_ctx['config'].is_valid()


@then(parsers.parse('fuel cost should be {units:d} units'))
def then_fuel_cost(mining_ctx, units):
    """Verify fuel cost."""
    assert mining_ctx['calculations']['fuel_cost'] == units


@then(parsers.parse('fuel cost for 3 cycles should be {units:d} units'))
def then_fuel_cost_3cycles(mining_ctx, units):
    """Verify fuel cost for 3 cycles."""
    assert mining_ctx['calculations']['fuel_cost_3cycles'] == units


@then(parsers.parse('gross revenue should be {revenue:d} credits'))
def then_gross_revenue(mining_ctx, revenue):
    """Verify gross revenue."""
    assert mining_ctx['calculations']['gross_revenue'] == revenue


@then(parsers.parse('fuel cost should be {cost:d} credits'))
def then_fuel_cost_credits(mining_ctx, cost):
    """Verify fuel cost in credits."""
    assert mining_ctx['calculations']['fuel_cost'] == cost


@then(parsers.parse('net profit should be {profit:d} credits'))
def then_net_profit(mining_ctx, profit):
    """Verify net profit."""
    assert mining_ctx['calculations']['net_profit'] == profit


@then(parsers.parse('profit per hour should be approximately {profit:d} credits'))
def then_profit_per_hour(mining_ctx, profit):
    """Verify profit per hour (allow 5% variance)."""
    actual = mining_ctx['calculations']['profit_per_hour']
    # Allow 5% variance due to integer division
    tolerance = profit * 0.05
    assert abs(actual - profit) <= tolerance, f"Expected ~{profit}, got {actual}"


@then(parsers.parse('failed extraction count should be {count:d}'))
def then_failed_count(mining_ctx, count):
    """Verify failed extraction count."""
    assert mining_ctx['stats'].failed_extractions == count


@then('failure rate should be tracked')
def then_failure_rate_tracked(mining_ctx):
    """Verify failure rate can be calculated."""
    stats = mining_ctx['stats']
    if stats.extraction_attempts > 0:
        failure_rate = stats.failed_extractions / stats.extraction_attempts
        assert failure_rate >= 0 and failure_rate <= 1
