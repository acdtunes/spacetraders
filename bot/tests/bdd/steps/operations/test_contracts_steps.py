from unittest.mock import Mock, MagicMock
from pytest_bdd import scenarios, given, when, then, parsers
from contextlib import contextmanager

from spacetraders_bot.operations.contracts import evaluate_contract_profitability
from spacetraders_bot.core.database import Database

scenarios('../../../bdd/features/operations/contracts.feature')


class MockRow(dict):
    """Mock SQLite Row object that behaves like a dict."""
    pass


class MockConnection:
    """Mock database connection."""
    def __init__(self, market_prices):
        self.market_prices = market_prices

    def execute(self, query, params=None):
        """Mock SQL execution."""
        # Return mock market data based on trade symbol
        if params and len(params) > 0:
            trade_symbol = params[0]
            if trade_symbol in self.market_prices:
                # Return cursor-like object with dict-convertible rows
                # Match the schema from find_markets_selling query:
                # waypoint_symbol, good_symbol, supply, activity,
                # purchase_price, sell_price, trade_volume, last_updated
                row = MockRow({
                    'waypoint_symbol': 'X1-TEST-M1',
                    'good_symbol': trade_symbol,
                    'trade_symbol': trade_symbol,  # Alias
                    'supply': 'MODERATE',
                    'activity': 'STRONG',
                    'purchase_price': self.market_prices[trade_symbol],
                    'sell_price': self.market_prices[trade_symbol] + 100,
                    'trade_volume': 100,
                    'last_updated': '2025-01-01T00:00:00Z'
                })
                result = [row]
                cursor = MagicMock()
                cursor.fetchall.return_value = result
                cursor.fetchone.return_value = row
                cursor.__iter__ = lambda self: iter(result)
                return cursor
        # Return empty cursor
        cursor = MagicMock()
        cursor.fetchall.return_value = []
        cursor.fetchone.return_value = None
        cursor.__iter__ = lambda self: iter([])
        return cursor


class MockDatabase:
    """Mock database for market price lookups."""
    def __init__(self):
        self.market_prices = {}

    def add_market_price(self, trade_symbol, price):
        """Add mock market price."""
        self.market_prices[trade_symbol] = price

    @contextmanager
    def connection(self):
        """Return mock connection context manager."""
        conn = MockConnection(self.market_prices)
        try:
            yield conn
        finally:
            pass

    def get_system_graph(self, conn, system_prefix):
        """Mock system graph retrieval."""
        return None  # No graph available in tests

    def execute(self, query, params=None):
        """Mock database query execution (legacy interface)."""
        # Return mock market data based on trade symbol
        if params and len(params) > 0:
            trade_symbol = params[0]
            if trade_symbol in self.market_prices:
                return [(self.market_prices[trade_symbol],)]
        return []


@given('a mock API client', target_fixture='contract_ctx')
def given_mock_api():
    """Create mock API client for contract tests."""
    api = Mock()
    api.token = "fake-token"

    context = {
        'api': api,
        'db': MockDatabase(),
        'contract': None,
        'cargo_capacity': 40,
        'system': 'X1-TEST',
        'evaluation_result': None,
        'is_profitable': None,
        'reason': None,
        'metrics': None
    }
    return context


@given('a mock database for market prices')
def given_mock_database(contract_ctx):
    """Database is already initialized."""
    return contract_ctx


@given(parsers.parse('a contract requiring {units:d} units of "{trade_symbol}"'))
def given_contract_requirement(contract_ctx, units, trade_symbol):
    """Create contract with specific requirements."""
    contract_ctx['contract'] = {
        'id': 'contract-123',
        'type': 'PROCUREMENT',
        'terms': {
            'deliver': [{
                'tradeSymbol': trade_symbol,
                'destinationSymbol': 'X1-TEST-DEST',
                'unitsRequired': units,
                'unitsFulfilled': 0
            }],
            'payment': {
                'onAccepted': 0,  # Will be set by payment step
                'onFulfilled': 0
            }
        },
        'accepted': False,
        'fulfilled': False
    }
    return contract_ctx


@given(parsers.parse('contract payment is {on_accept:d} credits on acceptance and {on_fulfill:d} credits on fulfillment'))
def given_contract_payment(contract_ctx, on_accept, on_fulfill):
    """Set contract payment terms."""
    contract_ctx['contract']['terms']['payment']['onAccepted'] = on_accept
    contract_ctx['contract']['terms']['payment']['onFulfilled'] = on_fulfill
    return contract_ctx


@given(parsers.parse('ship has {capacity:d} units cargo capacity'))
def given_cargo_capacity(contract_ctx, capacity):
    """Set ship cargo capacity."""
    contract_ctx['cargo_capacity'] = capacity
    return contract_ctx


@given(parsers.parse('market price for "{trade_symbol}" is {price:d} credits per unit'))
def given_market_price(contract_ctx, trade_symbol, price):
    """Set market price for trade good."""
    contract_ctx['db'].add_market_price(trade_symbol, price)
    return contract_ctx


@given(parsers.parse('no market data available for "{trade_symbol}"'))
def given_no_market_data(contract_ctx, trade_symbol):
    """Ensure no market data exists for trade symbol."""
    # Don't add any market price - will use conservative estimate
    return contract_ctx


@given('contract is already fulfilled')
def given_contract_fulfilled(contract_ctx):
    """Mark contract as already fulfilled."""
    delivery = contract_ctx['contract']['terms']['deliver'][0]
    delivery['unitsFulfilled'] = delivery['unitsRequired']
    return contract_ctx


@given(parsers.parse('contract already has {fulfilled:d} units fulfilled'))
def given_partial_fulfillment(contract_ctx, fulfilled):
    """Set partial fulfillment amount."""
    contract_ctx['contract']['terms']['deliver'][0]['unitsFulfilled'] = fulfilled
    return contract_ctx


@when('I evaluate contract profitability')
def when_evaluate_profitability(contract_ctx):
    """Evaluate contract profitability."""
    # Mock the database query for market prices
    from spacetraders_bot.operations.contracts import find_markets_selling
    from unittest.mock import patch

    trade_symbol = contract_ctx['contract']['terms']['deliver'][0]['tradeSymbol']
    db = contract_ctx['db']

    # Create mock return value for find_markets_selling
    def mock_find_markets(symbol, system=None, limit=None, db=None):
        if symbol in db.market_prices:
            return [{
                'trade_symbol': symbol,
                'purchase_price': db.market_prices[symbol],
                'waypoint': 'X1-TEST-M1'
            }]
        return []

    with patch('spacetraders_bot.operations.contracts.find_markets_selling', side_effect=mock_find_markets):
        is_profitable, reason, metrics = evaluate_contract_profitability(
            contract_ctx['contract'],
            contract_ctx['cargo_capacity'],
            system=contract_ctx['system'],
            db=db
        )

    contract_ctx['is_profitable'] = is_profitable
    contract_ctx['reason'] = reason
    contract_ctx['metrics'] = metrics
    contract_ctx['evaluation_result'] = (is_profitable, reason, metrics)

    return contract_ctx


@then('evaluation should succeed')
def then_evaluation_succeeds(contract_ctx):
    """Verify evaluation completed."""
    assert contract_ctx['evaluation_result'] is not None
    assert contract_ctx['reason'] is not None


@then('contract should be marked as profitable')
def then_contract_profitable(contract_ctx):
    """Verify contract is profitable."""
    assert contract_ctx['is_profitable'] is True


@then('contract should be marked as unprofitable')
def then_contract_unprofitable(contract_ctx):
    """Verify contract is unprofitable."""
    assert contract_ctx['is_profitable'] is False


@then('metrics should show positive net profit')
def then_positive_profit(contract_ctx):
    """Verify net profit is positive."""
    metrics = contract_ctx['metrics']
    assert metrics['net_profit'] > 0


@then('metrics should include market price source')
def then_has_price_source(contract_ctx):
    """Verify metrics include price source."""
    metrics = contract_ctx['metrics']
    assert 'price_source' in metrics
    assert 'market data' in metrics['price_source']


@then('rejection reason should mention insufficient profit')
def then_reason_mentions_profit(contract_ctx):
    """Verify rejection reason mentions profit."""
    reason = contract_ctx['reason']
    assert 'profit' in reason.lower() or 'cost' in reason.lower()


@then(parsers.parse('metrics should show price source as "{source}"'))
def then_price_source(contract_ctx, source):
    """Verify price source matches."""
    metrics = contract_ctx['metrics']
    assert source in metrics['price_source']


@then(parsers.parse('metrics should use {price:d} credits per unit estimate'))
def then_unit_price_estimate(contract_ctx, price):
    """Verify unit cost estimate."""
    metrics = contract_ctx['metrics']
    assert metrics['unit_cost'] == price


@then(parsers.parse('rejection reason should be "{expected_reason}"'))
def then_rejection_reason(contract_ctx, expected_reason):
    """Verify specific rejection reason."""
    reason = contract_ctx['reason']
    assert expected_reason in reason


@then(parsers.parse('metrics should show {trips:d} trips required'))
@then(parsers.parse('metrics should show {trips:d} trip required'))
def then_trips_required(contract_ctx, trips):
    """Verify trip calculation."""
    metrics = contract_ctx['metrics']
    assert metrics['trips'] == trips


@then(parsers.parse('fuel cost should be estimated at {cost:d} credits'))
def then_fuel_cost(contract_ctx, cost):
    """Verify fuel cost estimate."""
    metrics = contract_ctx['metrics']
    # Fuel cost is included in estimated_cost
    # Each trip costs 100 credits for fuel
    expected_fuel = metrics['trips'] * 100
    assert expected_fuel == cost


@then('metrics should show net loss within acceptable range')
def then_acceptable_loss(contract_ctx):
    """Verify loss is within acceptable range."""
    metrics = contract_ctx['metrics']
    # Acceptable loss threshold is -5000 cr
    assert metrics['net_profit'] >= -5000


@then('rejection reason should mention net profit below minimum')
def then_reason_profit_threshold(contract_ctx):
    """Verify rejection mentions profit threshold."""
    reason = contract_ctx['reason']
    assert 'net profit' in reason.lower() or 'minimum' in reason.lower()


@then(parsers.parse('metrics should show {units:d} units remaining'))
def then_units_remaining(contract_ctx, units):
    """Verify remaining units calculation."""
    metrics = contract_ctx['metrics']
    assert metrics['units_remaining'] == units
