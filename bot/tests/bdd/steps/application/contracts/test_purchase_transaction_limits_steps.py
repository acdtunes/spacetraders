"""
Step definitions for purchase transaction limits tests

REFACTORED: Removed mediator mocking.

NOTE: This test still has a VIOLATION - it tests a private method (_get_transaction_limit).
Black-box tests should verify behavior through public interfaces. This test should either:
1. Be removed (transaction limit logic tested through public workflow commands)
2. Be moved to a unit test (not BDD)
3. Test through a public method that uses transaction limits

Keeping as-is for now with documentation of the issue.
"""
import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from unittest.mock import Mock

from application.contracts.commands.batch_contract_workflow import (
    BatchContractWorkflowHandler
)
from configuration.container import get_mediator
from domain.shared.market import Market, TradeGood

# Load scenarios from feature file
scenarios('../../../features/application/contracts/purchase_transaction_limits.feature')


@pytest.fixture
def context():
    """Shared test context"""
    return {
        'handler': None,
        'market_repository': None,
        'transaction_limit': None,
        'market': None,
    }


@given('a contract workflow handler with market repository access')
def handler_with_market_repository(context):
    """
    Create handler with real mediator and mock repositories.

    Uses real mediator (core infrastructure) and mocks at boundaries (repositories).
    """
    # Use REAL mediator from container
    mediator = get_mediator()

    mock_ship_repository = Mock()
    mock_market_repository = Mock()

    context['market_repository'] = mock_market_repository

    context['handler'] = BatchContractWorkflowHandler(
        mediator=mediator,
        ship_repository=mock_ship_repository,
        market_repository=mock_market_repository
    )


@given(parsers.parse('a market "{waypoint}" selling "{trade_symbol}" with transaction limit of {limit:d} units'))
def market_with_transaction_limit(context, waypoint, trade_symbol, limit):
    """Set up market with specific transaction limit"""
    trade_good = TradeGood(
        symbol=trade_symbol,
        supply="ABUNDANT",
        activity="STRONG",
        purchase_price=900,
        sell_price=1000,
        trade_volume=limit  # Transaction limit
    )

    context['market'] = Market(
        waypoint_symbol=waypoint,
        trade_goods=(trade_good,),
        last_updated="2025-01-01T00:00:00Z"
    )

    # Configure mock to return this market
    context['market_repository'].get_market_data = Mock(return_value=context['market'])


@given(parsers.parse('no market data exists for waypoint "{waypoint}"'))
def no_market_data(context, waypoint):
    """Configure market repository to return None (no data)"""
    context['market_repository'].get_market_data = Mock(return_value=None)


@when(parsers.parse('I query the transaction limit for "{trade_symbol}" at "{waypoint}"'))
def query_transaction_limit(context, trade_symbol, waypoint):
    """Query transaction limit using handler's helper method"""
    context['transaction_limit'] = context['handler']._get_transaction_limit(
        market_waypoint=waypoint,
        trade_symbol=trade_symbol,
        player_id=1  # Test player ID
    )


@then(parsers.parse('the transaction limit should be {expected_limit:d} units'))
def verify_transaction_limit(context, expected_limit):
    """Verify the transaction limit matches expected value"""
    assert context['transaction_limit'] == expected_limit, \
        f"Expected transaction limit {expected_limit}, got {context['transaction_limit']}"
