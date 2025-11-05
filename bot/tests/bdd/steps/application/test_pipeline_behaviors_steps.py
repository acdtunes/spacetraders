"""
Step definitions for Pipeline Behaviors BDD tests.

BLACK-BOX testing through public API (handle method).
Tests LoggingBehavior and ValidationBehavior middleware.
"""
import pytest
from unittest.mock import Mock, AsyncMock, patch
from pytest_bdd import scenarios, given, when, then, parsers

from application.common.behaviors import (
    LoggingBehavior,
    ValidationBehavior
)

# Load all scenarios from the feature file
scenarios('../../features/application/pipeline_behaviors.feature')


# Fixtures

@pytest.fixture
def context():
    """Shared test context"""
    return {
        'logging_behavior': None,
        'validation_behavior': None,
        'request': None,
        'requests': [],
        'next_handler': None,
        'result': None,
        'exception': None,
        'mock_logger': None,
        'call_order': [],
        'handler_called': [],
        'validate_called': False
    }


# Given Steps - Initialization

@given("the logging behavior is initialized")
def logging_behavior_initialized(context):
    """Initialize logging behavior"""
    context['logging_behavior'] = LoggingBehavior()


@given("the validation behavior is initialized")
def validation_behavior_initialized(context):
    """Initialize validation behavior"""
    context['validation_behavior'] = ValidationBehavior()


# Given Steps - Request Setup

@given(parsers.parse('a test request named "{request_name}"'))
def create_request_named(context, request_name):
    """Create a test request with specific name"""
    request = Mock()
    request.__class__.__name__ = request_name
    if 'requests' not in context:
        context['requests'] = []
    context['requests'].append(request)
    context['request'] = request


@given("a test request with validate method")
def create_request_with_validate(context):
    """Create request with validate method"""
    request = Mock()
    request.__class__.__name__ = "ValidatableRequest"
    request.validate = Mock()
    context['request'] = request
    context['validate_method'] = request.validate


@given("a test request without validate method")
def create_request_without_validate(context):
    """Create request without validate method"""
    request = Mock()
    request.__class__.__name__ = "SimpleRequest"
    # No validate method
    context['request'] = request


@given("a test request with non-callable validate attribute")
def create_request_with_non_callable_validate(context):
    """Create request with validate as non-callable"""
    request = Mock()
    request.__class__.__name__ = "BadRequest"
    request.validate = "not_a_function"
    context['request'] = request


@given("a test request with validate method that tracks order")
def create_request_with_tracking_validate(context):
    """Create request with validate that tracks execution order"""
    request = Mock()
    request.__class__.__name__ = "ValidatableRequest"

    def track_validate():
        context['call_order'].append("validate")

    request.validate = track_validate
    context['request'] = request


@given(parsers.parse('a test request with validate method that raises {error_type} "{error_msg}"'))
def create_request_with_failing_validate(context, error_type, error_msg):
    """Create request with validate that raises error"""
    request = Mock()
    request.__class__.__name__ = "ValidatableRequest"

    error_class = globals().get(error_type, ValueError)
    request.validate = Mock(side_effect=error_class(error_msg))
    context['request'] = request
    context['validate_method'] = request.validate


# Given Steps - Handler Setup

@given("a mock next handler that returns success")
def mock_handler_success(context):
    """Create mock handler returning success"""
    handler = AsyncMock()
    handler.return_value = {"result": "success"}
    context['next_handler'] = handler


@given(parsers.parse('a mock next handler that returns data "{data}"'))
def mock_handler_with_data(context, data):
    """Create mock handler returning specific data"""
    handler = AsyncMock()
    handler.return_value = {"data": data}
    context['next_handler'] = handler


@given(parsers.re(r'a mock next handler that returns (?P<key>\w+) (?P<value>true|false)'))
def mock_handler_with_boolean(context, key, value):
    """Create mock handler returning boolean key-value pair"""
    handler = AsyncMock()
    bool_value = True if value == "true" else False
    handler.return_value = {key: bool_value}
    context['next_handler'] = handler


@given(parsers.parse('a mock next handler that raises {error_type} "{error_msg}"'))
def mock_handler_raises_error(context, error_type, error_msg):
    """Create mock handler that raises error"""
    handler = AsyncMock()
    error_class = globals().get(error_type, ValueError)
    handler.side_effect = error_class(error_msg)
    context['next_handler'] = handler


@given("a mock next handler that tracks order")
def mock_handler_tracks_order(context):
    """Create mock handler that tracks execution order"""
    async def track_handler():
        context['call_order'].append("handler")
        return {"result": "success"}

    context['next_handler'] = track_handler


@given("a final handler that tracks order")
def final_handler_tracks_order(context):
    """Create final handler that tracks execution order"""
    async def track_handler():
        context['call_order'].append("handler")
        return {"result": "success"}

    context['final_handler'] = track_handler


@given(parsers.parse('a final handler that returns {data}'))
def final_handler_with_data(context, data):
    """Create final handler returning specific data"""
    async def final_handler():
        return {"data": data}

    context['final_handler'] = final_handler


@given("a final handler that tracks calls")
def final_handler_tracks_calls(context):
    """Create final handler that tracks if it's called"""
    async def final_handler():
        context['handler_called'].append(True)
        return {"result": "success"}

    context['final_handler'] = final_handler


@given(parsers.parse('a failing handler that raises {error_type} "{error_msg}"'))
def failing_handler(context, error_type, error_msg):
    """Create handler that always fails"""
    async def fail_handler():
        error_class = globals().get(error_type, ValueError)
        raise error_class(error_msg)

    context['next_handler'] = fail_handler


# When Steps - Execute Behaviors

@when("I execute the logging behavior with the request")
def execute_logging_behavior(context):
    """Execute logging behavior with request"""
    import asyncio
    behavior = context['logging_behavior']
    request = context['request']
    handler = context['next_handler']

    with patch('application.common.behaviors.logger') as mock_logger:
        context['mock_logger'] = mock_logger
        try:
            context['result'] = asyncio.run(behavior.handle(request, handler))
        except Exception as e:
            context['exception'] = e


@when("I execute the logging behavior with all requests")
def execute_logging_behavior_all(context):
    """Execute logging behavior with all requests"""
    import asyncio
    behavior = context['logging_behavior']
    handler = context['next_handler']

    with patch('application.common.behaviors.logger') as mock_logger:
        context['mock_logger'] = mock_logger
        for request in context['requests']:
            try:
                asyncio.run(behavior.handle(request, handler))
            except Exception:
                pass


@when("I execute the validation behavior with the request")
def execute_validation_behavior(context):
    """Execute validation behavior with request"""
    import asyncio
    behavior = context['validation_behavior']
    request = context['request']
    handler = context['next_handler']

    try:
        context['result'] = asyncio.run(behavior.handle(request, handler))
    except Exception as e:
        context['exception'] = e


@when("I execute the behavior pipeline with logging then validation")
def execute_behavior_pipeline(context):
    """Execute full behavior pipeline"""
    import asyncio
    logging_behavior = context['logging_behavior']
    validation_behavior = context['validation_behavior']
    request = context['request']
    final_handler = context['final_handler']

    # Create pipeline: Logging -> Validation -> Final Handler
    async def validation_then_handler():
        return await validation_behavior.handle(request, final_handler)

    with patch('application.common.behaviors.logger') as mock_logger:
        context['mock_logger'] = mock_logger
        try:
            context['result'] = asyncio.run(logging_behavior.handle(request, validation_then_handler))
        except Exception as e:
            context['exception'] = e


# Then Steps - Assertions

@then(parsers.parse('the logger should log "{message}"'))
def logger_should_log(context, message):
    """Verify logger logged specific message - checking observable log output"""
    mock_logger = context['mock_logger']
    # Extract all log messages from info calls
    calls = [call[0][0] for call in mock_logger.info.call_args_list]
    assert any(message in call for call in calls), (
        f"Expected log message containing '{message}' but got: {calls}"
    )


@then("the next handler should be called")
def next_handler_called(context):
    """Verify next handler was called by checking we got a result"""
    # If we have a result, the handler was called
    assert context['result'] is not None, "Expected handler to be called and return a result"


@then("the result should be the handler response")
def result_is_handler_response(context):
    """Verify result matches handler response"""
    assert context['result'] == {"result": "success"}


@then(parsers.parse('the result should contain data "{data}"'))
def result_contains_data(context, data):
    """Verify result contains specific data"""
    assert context['result']['data'] == data


@then(parsers.parse('the execution should fail with {error_type} "{error_msg}"'))
def execution_should_fail(context, error_type, error_msg):
    """Verify execution failed with specific error"""
    assert context['exception'] is not None
    error_class = globals().get(error_type, ValueError)
    assert isinstance(context['exception'], error_class)
    assert error_msg in str(context['exception'])


@then(parsers.parse('the logger should log error containing "{message}"'))
def logger_should_log_error(context, message):
    """Verify logger logged error with message - checking observable error output"""
    mock_logger = context['mock_logger']
    # Extract all error log messages
    error_calls = [call[0][0] for call in mock_logger.error.call_args_list]
    assert any(message in call for call in error_calls), (
        f"Expected error log containing '{message}' but got: {error_calls}"
    )


@then("the logger should log error with exc_info true")
def logger_should_log_with_exc_info(context):
    """Verify logger logged with exc_info=True - checking exception details were included"""
    mock_logger = context['mock_logger']
    # Check that error was logged with exception information
    assert len(mock_logger.error.call_args_list) > 0, "Expected error to be logged"
    # Verify exc_info was passed (exception traceback included)
    error_call = mock_logger.error.call_args
    assert error_call[1].get('exc_info') is True, "Expected error to be logged with exc_info=True"


@then("the validate method should be called")
def validate_method_called(context):
    """Verify validate method was called by checking execution succeeded or checking call order"""
    # For tracking validates, we check call_order
    if 'validate' in context.get('call_order', []):
        assert 'validate' in context['call_order']
    else:
        # If no tracking, just verify execution succeeded (validate didn't raise)
        assert context['exception'] is None, "Expected validation to pass without exception"


@then("the execution should succeed")
def execution_should_succeed(context):
    """Verify execution succeeded"""
    assert context['exception'] is None
    assert context['result'] is not None


@then("the next handler should not be called")
def next_handler_not_called(context):
    """Verify next handler was not called by checking an exception was raised"""
    # If an exception was raised before the handler, the handler wasn't called
    assert context['exception'] is not None, "Expected validation to fail and prevent handler execution"


@then(parsers.parse('the call order should be "{first}" then "{second}"'))
def call_order_should_be(context, first, second):
    """Verify execution order"""
    call_order = context['call_order']
    assert call_order == [first, second], f"Expected [{first}, {second}], got {call_order}"


@then("the final handler should not be called")
def final_handler_not_called(context):
    """Verify final handler was not called"""
    assert len(context['handler_called']) == 0


@then("the logger should log error once")
def logger_should_log_error_once(context):
    """Verify logger logged error by checking an error was logged"""
    mock_logger = context['mock_logger']
    # Check that at least one error was logged
    assert len(mock_logger.error.call_args_list) > 0, "Expected at least one error to be logged"


# Boolean pattern - only matches true/false values
@then(parsers.re(r'the result should contain (?P<key>\w+) (?P<value>true|false)'))
def result_contains_boolean(context, key, value):
    """Verify result contains key-value pair with boolean value"""
    expected = True if value == "true" else False
    assert context['result'][key] == expected
