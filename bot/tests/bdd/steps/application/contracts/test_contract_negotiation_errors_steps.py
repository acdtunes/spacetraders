"""Step definitions for contract negotiation error visibility"""
import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from unittest.mock import Mock, patch
import requests

from domain.shared.exceptions import DomainException
from application.contracts.commands.negotiate_contract import NegotiateContractCommand

# Load scenarios
scenarios("../../../features/application/contracts/contract_negotiation_errors.feature")


@given("a registered player with agent symbol \"TEST-AGENT\" and player_id 1")
def mock_player(context):
    """Mock player setup"""
    context['player_id'] = 1
    context['agent_symbol'] = "TEST-AGENT"


@given("a ship \"TEST-AGENT-1\" exists for player 1")
def mock_ship(context):
    """Mock ship setup"""
    context['ship_symbol'] = "TEST-AGENT-1"


@given(parsers.parse('the API will return error {error_code:d} "{error_message}"'))
def mock_api_error(context, error_code, error_message):
    """Mock API client to return specific error"""
    # Create mock response
    mock_response = Mock()
    mock_response.status_code = 400
    mock_response.ok = False
    mock_response.json.return_value = {
        "error": {
            "code": error_code,
            "message": error_message
        }
    }

    # Create HTTPError with response attached
    http_error = requests.exceptions.HTTPError()
    http_error.response = mock_response

    # Mock API client factory
    mock_api_client = Mock()
    mock_api_client.negotiate_contract.side_effect = http_error

    # Store in context
    context['mock_api_client_factory'] = lambda player_id: mock_api_client
    context['expected_error_code'] = error_code
    context['expected_error_message'] = error_message


@given("the API returns a 429 rate limit error")
def mock_rate_limit_error(context):
    """Mock API client to return rate limit error"""
    mock_response = Mock()
    mock_response.status_code = 429
    mock_response.ok = False
    mock_response.json.return_value = {
        "error": {
            "code": 429,
            "message": "Rate limit exceeded"
        }
    }

    http_error = requests.exceptions.HTTPError()
    http_error.response = mock_response

    mock_api_client = Mock()
    mock_api_client.negotiate_contract.side_effect = http_error

    context['mock_api_client_factory'] = lambda player_id: mock_api_client
    context['expected_error_message'] = "rate limit"


@given("the API returns a 500 server error")
def mock_server_error(context):
    """Mock API client to return server error"""
    mock_response = Mock()
    mock_response.status_code = 500
    mock_response.ok = False
    mock_response.text = "Internal Server Error"
    mock_response.json.side_effect = ValueError("Not JSON")

    http_error = requests.exceptions.HTTPError()
    http_error.response = mock_response

    mock_api_client = Mock()
    mock_api_client.negotiate_contract.side_effect = http_error

    context['mock_api_client_factory'] = lambda player_id: mock_api_client


@when('I attempt to negotiate a contract with ship "TEST-AGENT-1"')
def attempt_negotiate(context, mediator):
    """Attempt to negotiate contract and capture exception"""
    # Create command
    command = NegotiateContractCommand(
        ship_symbol=context['ship_symbol'],
        player_id=context['player_id']
    )

    # Execute and capture exception
    # Patch the API client factory to return our mock
    with patch('configuration.container.get_api_client_for_player', context['mock_api_client_factory']):
        try:
            import asyncio
            asyncio.run(mediator.send_async(command))
            context['exception'] = None
        except Exception as e:
            context['exception'] = e


@then("the negotiation should fail with a ContractNegotiationError")
def verify_contract_negotiation_error(context):
    """Verify ContractNegotiationError was raised"""
    assert context['exception'] is not None, "Expected exception but none was raised"

    # Import the exception class we're expecting
    from domain.shared.exceptions import ContractNegotiationError

    assert isinstance(context['exception'], ContractNegotiationError), (
        f"Expected ContractNegotiationError but got {type(context['exception']).__name__}: {context['exception']}"
    )


@then("the negotiation should fail with a RateLimitError")
def verify_rate_limit_error(context):
    """Verify RateLimitError was raised"""
    assert context['exception'] is not None, "Expected exception but none was raised"

    # Import the exception class we're expecting
    from domain.shared.exceptions import RateLimitError

    assert isinstance(context['exception'], RateLimitError), (
        f"Expected RateLimitError but got {type(context['exception']).__name__}: {context['exception']}"
    )


@then(parsers.parse('the error message should contain "{expected_text}"'))
def verify_error_message_contains(context, expected_text):
    """Verify error message contains expected text (case-insensitive)"""
    assert context['exception'] is not None, "No exception was raised"

    error_message = str(context['exception']).lower()
    expected_text_lower = expected_text.lower()
    assert expected_text_lower in error_message, (
        f"Expected error message to contain '{expected_text}' but got: {context['exception']}"
    )
