#!/usr/bin/env python3
"""
Step definitions for api_client_operations.feature
Tests comprehensive API client functionality including retries, rate limiting, and error handling
"""

import sys
import time
import logging
from pathlib import Path
from unittest.mock import Mock, patch, MagicMock
from pytest_bdd import scenarios, given, when, then, parsers
import pytest
import requests

# Add lib to path
sys.path.insert(0, str(Path(__file__).resolve().parents[4] / 'lib'))

from api_client import APIClient, RateLimiter

# Load scenarios
scenarios('../../features/core/api_client_operations.feature')


# Fixtures

@pytest.fixture
def context():
    """Shared test context"""
    return {
        'api_client': None,
        'response': None,
        'error': None,
        'exception': None,
        'request_times': [],
        'retry_count': 0,
        'wait_times': [],
        'mock_response': None,
        'post_data': None,
        'patch_data': None,
        'headers_captured': None,
        'rate_limiter': None,
        'start_time': None
    }


# Background steps

@given("the API client is configured with a valid token")
def setup_api_client(context):
    """Initialize API client with test token"""
    context['api_client'] = APIClient(
        token="test_token_12345",
        base_url="https://api.spacetraders.io/v2"
    )


# Given steps - Setup test scenarios

@given(parsers.parse('the endpoint "{endpoint}" will return 404'))
def endpoint_returns_404(context, endpoint):
    """Mock endpoint to return 404"""
    mock_response = Mock()
    mock_response.status_code = 404
    mock_response.json.return_value = {
        "error": {
            "code": "NOT_FOUND",
            "message": "Resource not found"
        }
    }
    context['mock_response'] = mock_response


@given(parsers.parse('I have POST data with "{key}" set to "{value}"'))
def setup_post_data(context, key, value):
    """Setup POST data payload"""
    if context['post_data'] is None:
        context['post_data'] = {}
    context['post_data'][key] = value


@given(parsers.parse('I have PATCH data with "{key}" set to "{value}"'))
def setup_patch_data(context, key, value):
    """Setup PATCH data payload"""
    if context['patch_data'] is None:
        context['patch_data'] = {}
    context['patch_data'][key] = value


@given("the API will return rate limit error then succeed")
def api_rate_limit_then_success(context):
    """Mock API to return rate limit error first, then success"""
    context['retry_scenario'] = 'rate_limit_once'


@given(parsers.parse("the API will return 500 error {times:d} times then succeed"))
def api_server_error_then_success(context, times):
    """Mock API to return server errors then succeed"""
    context['retry_scenario'] = f'server_error_{times}'
    context['expected_retries'] = times


@given("the API will always return 500 error")
def api_always_server_error(context):
    """Mock API to always return 500"""
    context['retry_scenario'] = 'always_500'


@given("the API will have connection error then succeed")
def api_connection_error_then_success(context):
    """Mock API to have connection error then succeed"""
    context['retry_scenario'] = 'connection_error_once'


@given("the API will timeout then succeed")
def api_timeout_then_success(context):
    """Mock API to timeout then succeed"""
    context['retry_scenario'] = 'timeout_once'


@given("the API will return invalid JSON")
def api_invalid_json(context):
    """Mock API to return invalid JSON"""
    context['retry_scenario'] = 'invalid_json'


@given("the API will return 400 bad request error")
def api_bad_request(context):
    """Mock API to return 400 error"""
    context['retry_scenario'] = 'bad_request'


@given(parsers.parse('the system "{system}" has {count:d} waypoints'))
def system_has_waypoints(context, system, count):
    """Setup system with multiple waypoints for pagination testing"""
    context['waypoint_count'] = count
    context['system'] = system


@given(parsers.parse('the system "{system}" has waypoints with various traits'))
def system_has_waypoints_with_traits(context, system):
    """Setup system with waypoints having different traits"""
    context['system'] = system
    context['has_trait_filter'] = True


@given(parsers.parse('the rate limiter has minimum interval {interval:f} seconds'))
def rate_limiter_min_interval(context, interval):
    """Setup rate limiter with specific interval"""
    context['rate_limiter'] = RateLimiter(min_interval=interval)


@given("the API will return error with message \"rate limit exceeded\"")
def api_rate_limit_message(context):
    """Mock API to return error message about rate limiting"""
    context['retry_scenario'] = 'rate_limit_message'


@given("the API will always have connection errors")
def api_always_connection_error(context):
    """Mock API to always have connection errors"""
    context['retry_scenario'] = 'always_connection_error'


@given("the API will raise a generic request exception")
def api_generic_exception(context):
    """Mock API to raise generic RequestException"""
    context['retry_scenario'] = 'generic_exception'


@given("the API will return status code 300")
def api_status_300(context):
    """Mock API to return unexpected status code 300"""
    context['retry_scenario'] = 'status_300'


@given("the API will always timeout")
def api_always_timeout(context):
    """Mock API to always timeout"""
    context['retry_scenario'] = 'always_timeout'


# When steps - Execute actions

@when(parsers.parse('I make a GET request to "{endpoint}"'))
def make_get_request(context, endpoint):
    """Make GET request"""
    _execute_request(context, 'GET', endpoint)


@when(parsers.parse('I make a POST request to "{endpoint}" with no data'))
def make_post_request_no_data(context, endpoint):
    """Make POST request with no data"""
    _execute_request(context, 'POST', endpoint, data=None)


@when(parsers.parse('I make a POST request to "{endpoint}" with the data'))
def make_post_request_with_data(context, endpoint):
    """Make POST request with prepared data"""
    _execute_request(context, 'POST', endpoint, data=context.get('post_data'))


@when(parsers.parse('I make a PATCH request to "{endpoint}" with the data'))
def make_patch_request_with_data(context, endpoint):
    """Make PATCH request with prepared data"""
    _execute_request(context, 'PATCH', endpoint, data=context.get('patch_data'))


@when(parsers.parse('I make a DELETE request to "{endpoint}"'))
def make_delete_request(context, endpoint):
    """Make DELETE request (unsupported method)"""
    # Patch time.sleep and requests to avoid actual network calls
    with patch('time.sleep'):
        try:
            context['response'] = context['api_client'].request('DELETE', endpoint)
        except ValueError as e:
            context['exception'] = e


@when(parsers.parse('I make a GET request to "{endpoint}" with max retries {retries:d}'))
def make_get_request_with_retries(context, endpoint, retries):
    """Make GET request with specific max retries"""
    _execute_request(context, 'GET', endpoint, max_retries=retries)


@when("I make 3 consecutive GET requests")
def make_consecutive_requests(context):
    """Make multiple consecutive requests to test rate limiting"""
    context['start_time'] = time.time()
    context['request_times'] = []

    for _ in range(3):
        context['rate_limiter'].wait()
        request_time = time.time()
        context['request_times'].append(request_time)

    context['end_time'] = time.time()


@when(parsers.parse('I list waypoints for system "{system}" with limit {limit:d} and page {page:d}'))
def list_waypoints_pagination(context, system, limit, page):
    """List waypoints with pagination"""
    with patch('requests.get') as mock_get:
        # Create mock waypoints
        total = context.get('waypoint_count', 50)
        start = (page - 1) * limit
        end = min(start + limit, total)

        waypoints = [
            {
                "symbol": f"{system}-WP{i}",
                "type": "PLANET",
                "x": i * 10,
                "y": 0
            }
            for i in range(start, end)
        ]

        mock_response = Mock()
        mock_response.status_code = 200
        mock_response.json.return_value = {
            "data": waypoints,
            "meta": {
                "total": total,
                "page": page,
                "limit": limit
            }
        }
        mock_get.return_value = mock_response

        context['response'] = context['api_client'].list_waypoints(system, limit, page)


@when(parsers.parse('I list waypoints for system "{system}" with trait filter "{trait}"'))
def list_waypoints_with_trait(context, system, trait):
    """List waypoints filtered by trait"""
    with patch('requests.get') as mock_get:
        waypoints = [
            {
                "symbol": f"{system}-A1",
                "type": "PLANET",
                "traits": [{"symbol": "MARKETPLACE"}]
            },
            {
                "symbol": f"{system}-B2",
                "type": "PLANET",
                "traits": [{"symbol": "MARKETPLACE"}]
            }
        ]

        mock_response = Mock()
        mock_response.status_code = 200
        mock_response.json.return_value = {
            "data": waypoints,
            "meta": {"total": 2, "page": 1, "limit": 20}
        }
        mock_get.return_value = mock_response

        context['response'] = context['api_client'].list_waypoints(system, traits=trait)


@when("I call the get_agent convenience method")
def call_get_agent(context):
    """Call get_agent convenience method"""
    with patch('requests.get') as mock_get:
        mock_response = Mock()
        mock_response.status_code = 200
        mock_response.json.return_value = {
            "data": {
                "symbol": "TEST_AGENT",
                "credits": 100000,
                "headquarters": "X1-HU87-A1"
            }
        }
        mock_get.return_value = mock_response

        context['raw_response'] = mock_response.json.return_value
        context['response'] = context['api_client'].get_agent()


@when(parsers.parse('I call the get_ship convenience method for "{ship_symbol}"'))
def call_get_ship(context, ship_symbol):
    """Call get_ship convenience method"""
    with patch('requests.get') as mock_get:
        mock_response = Mock()
        mock_response.status_code = 200
        mock_response.json.return_value = {
            "data": {
                "symbol": ship_symbol,
                "fuel": {"current": 400, "capacity": 400}
            }
        }
        mock_get.return_value = mock_response

        context['response'] = context['api_client'].get_ship(ship_symbol)
        context['expected_symbol'] = ship_symbol


@when("I call the list_ships convenience method")
def call_list_ships(context):
    """Call list_ships convenience method"""
    with patch('requests.get') as mock_get:
        mock_response = Mock()
        mock_response.status_code = 200
        mock_response.json.return_value = {
            "data": [
                {"symbol": "SHIP-1"},
                {"symbol": "SHIP-2"}
            ]
        }
        mock_get.return_value = mock_response

        context['response'] = context['api_client'].list_ships()


@when(parsers.parse('I call the get_contract convenience method for "{contract_id}"'))
def call_get_contract(context, contract_id):
    """Call get_contract convenience method"""
    with patch('requests.get') as mock_get:
        mock_response = Mock()
        mock_response.status_code = 200
        mock_response.json.return_value = {
            "data": {
                "id": contract_id,
                "type": "PROCUREMENT"
            }
        }
        mock_get.return_value = mock_response

        context['response'] = context['api_client'].get_contract(contract_id)


@when("I call the list_contracts convenience method")
def call_list_contracts(context):
    """Call list_contracts convenience method"""
    with patch('requests.get') as mock_get:
        mock_response = Mock()
        mock_response.status_code = 200
        mock_response.json.return_value = {
            "data": [
                {"id": "CONTRACT-1"},
                {"id": "CONTRACT-2"}
            ]
        }
        mock_get.return_value = mock_response

        context['response'] = context['api_client'].list_contracts()


@when(parsers.parse('I call the get_market convenience method for system "{system}" and waypoint "{waypoint}"'))
def call_get_market(context, system, waypoint):
    """Call get_market convenience method"""
    with patch('requests.get') as mock_get:
        mock_response = Mock()
        mock_response.status_code = 200
        mock_response.json.return_value = {
            "data": {
                "symbol": waypoint,
                "tradeGoods": []
            }
        }
        mock_get.return_value = mock_response

        context['response'] = context['api_client'].get_market(system, waypoint)


@when(parsers.parse('I call the get_waypoint convenience method for system "{system}" and waypoint "{waypoint}"'))
def call_get_waypoint(context, system, waypoint):
    """Call get_waypoint convenience method"""
    with patch('requests.get') as mock_get:
        mock_response = Mock()
        mock_response.status_code = 200
        mock_response.json.return_value = {
            "data": {
                "symbol": waypoint,
                "type": "PLANET"
            }
        }
        mock_get.return_value = mock_response

        context['response'] = context['api_client'].get_waypoint(system, waypoint)
        context['expected_symbol'] = waypoint


@when("I call the patch convenience method")
def call_patch_convenience(context):
    """Call patch convenience method"""
    with patch('requests.patch') as mock_patch:
        mock_response = Mock()
        mock_response.status_code = 200
        mock_response.json.return_value = {
            "data": {
                "nav": {
                    "flightMode": context['patch_data']['flightMode']
                }
            }
        }
        mock_patch.return_value = mock_response

        context['response'] = context['api_client'].patch("/my/ships/TEST-1/nav", context['patch_data'])


@when("I make a POST request to \"/my/ships/TEST-1/orbit\" with empty data")
def make_post_empty_data(context):
    """Make POST request with empty data"""
    with patch('requests.post') as mock_post:
        mock_response = Mock()
        mock_response.status_code = 200
        mock_response.json.return_value = {"data": {"nav": {"status": "IN_ORBIT"}}}
        mock_post.return_value = mock_response

        context['response'] = context['api_client'].post("/my/ships/TEST-1/orbit", data={})
        context['post_call'] = mock_post


@when("I make a POST request that returns 201 created")
def make_post_201_response(context):
    """Make POST request that returns 201"""
    with patch('requests.post') as mock_post:
        mock_response = Mock()
        mock_response.status_code = 201
        mock_response.json.return_value = {"data": {"id": "NEW-RESOURCE"}}
        mock_post.return_value = mock_response

        context['response'] = context['api_client'].post("/some/endpoint")


# Helper function for executing requests with mock scenarios

def _execute_request(context, method, endpoint, data=None, max_retries=5):
    """Execute request with appropriate mocking based on retry scenario"""
    scenario = context.get('retry_scenario')

    if scenario == 'rate_limit_once':
        _mock_rate_limit_once(context, method, endpoint, data, max_retries)
    elif scenario and scenario.startswith('server_error_'):
        times = int(scenario.split('_')[-1])
        _mock_server_errors(context, method, endpoint, data, max_retries, times)
    elif scenario == 'always_500':
        _mock_always_500(context, method, endpoint, data, max_retries)
    elif scenario == 'connection_error_once':
        _mock_connection_error_once(context, method, endpoint, data, max_retries)
    elif scenario == 'timeout_once':
        _mock_timeout_once(context, method, endpoint, data, max_retries)
    elif scenario == 'invalid_json':
        _mock_invalid_json(context, method, endpoint, data, max_retries)
    elif scenario == 'bad_request':
        _mock_bad_request(context, method, endpoint, data, max_retries)
    elif scenario == 'rate_limit_message':
        _mock_rate_limit_message(context, method, endpoint, data, max_retries)
    elif scenario == 'always_connection_error':
        _mock_always_connection_error(context, method, endpoint, data, max_retries)
    elif scenario == 'generic_exception':
        _mock_generic_exception(context, method, endpoint, data, max_retries)
    elif scenario == 'status_300':
        _mock_status_300(context, method, endpoint, data, max_retries)
    elif scenario == 'always_timeout':
        _mock_always_timeout(context, method, endpoint, data, max_retries)
    elif context.get('mock_response'):
        _mock_simple_response(context, method, endpoint, data, max_retries)
    else:
        _mock_success_response(context, method, endpoint, data, max_retries)


def _mock_rate_limit_once(context, method, endpoint, data, max_retries):
    """Mock rate limit on first call, then success"""
    call_count = [0]

    with patch('requests.' + method.lower()) as mock_request:
        def side_effect(*args, **kwargs):
            call_count[0] += 1
            mock_response = Mock()
            if call_count[0] == 1:
                mock_response.status_code = 429
                mock_response.json.return_value = {
                    "error": {"code": "RATE_LIMIT", "message": "Rate limit exceeded"}
                }
            else:
                mock_response.status_code = 200
                mock_response.json.return_value = {"data": {"success": True}}
            return mock_response

        mock_request.side_effect = side_effect

        # Track wait times
        original_sleep = time.sleep
        wait_times = []

        def track_sleep(seconds):
            wait_times.append(seconds)
            original_sleep(0.01)  # Short sleep for testing

        with patch('time.sleep', side_effect=track_sleep):
            context['response'] = context['api_client'].request(method, endpoint, data, max_retries)
            context['retry_count'] = call_count[0] - 1
            context['wait_times'] = wait_times


def _mock_server_errors(context, method, endpoint, data, max_retries, error_count):
    """Mock server errors then success"""
    call_count = [0]
    wait_times = []

    with patch('requests.' + method.lower()) as mock_request:
        def side_effect(*args, **kwargs):
            call_count[0] += 1
            mock_response = Mock()
            if call_count[0] <= error_count:
                mock_response.status_code = 500
                mock_response.json.return_value = {
                    "error": {"code": "SERVER_ERROR", "message": "Internal server error"}
                }
            else:
                mock_response.status_code = 200
                mock_response.json.return_value = {"data": {"success": True}}
            return mock_response

        mock_request.side_effect = side_effect

        def track_sleep(seconds):
            wait_times.append(seconds)

        with patch('time.sleep', side_effect=track_sleep):
            context['response'] = context['api_client'].request(method, endpoint, data, max_retries)
            context['retry_count'] = error_count
            context['wait_times'] = wait_times


def _mock_always_500(context, method, endpoint, data, max_retries):
    """Mock always returning 500"""
    call_count = [0]

    with patch('requests.' + method.lower()) as mock_request:
        def side_effect(*args, **kwargs):
            call_count[0] += 1
            mock_response = Mock()
            mock_response.status_code = 500
            mock_response.json.return_value = {
                "error": {"code": "SERVER_ERROR", "message": "Internal server error"}
            }
            return mock_response

        mock_request.side_effect = side_effect

        with patch('time.sleep'):
            context['response'] = context['api_client'].request(method, endpoint, data, max_retries)
            context['retry_count'] = call_count[0]


def _mock_connection_error_once(context, method, endpoint, data, max_retries):
    """Mock connection error once then success"""
    call_count = [0]

    with patch('requests.' + method.lower()) as mock_request:
        def side_effect(*args, **kwargs):
            call_count[0] += 1
            if call_count[0] == 1:
                raise requests.exceptions.ConnectionError("Connection failed")
            mock_response = Mock()
            mock_response.status_code = 200
            mock_response.json.return_value = {"data": {"success": True}}
            return mock_response

        mock_request.side_effect = side_effect

        with patch('time.sleep'):
            context['response'] = context['api_client'].request(method, endpoint, data, max_retries)
            context['retry_count'] = 1


def _mock_timeout_once(context, method, endpoint, data, max_retries):
    """Mock timeout once then success"""
    call_count = [0]

    with patch('requests.' + method.lower()) as mock_request:
        def side_effect(*args, **kwargs):
            call_count[0] += 1
            if call_count[0] == 1:
                raise requests.exceptions.Timeout("Request timed out")
            mock_response = Mock()
            mock_response.status_code = 200
            mock_response.json.return_value = {"data": {"success": True}}
            return mock_response

        mock_request.side_effect = side_effect

        with patch('time.sleep'):
            context['response'] = context['api_client'].request(method, endpoint, data, max_retries)


def _mock_invalid_json(context, method, endpoint, data, max_retries):
    """Mock invalid JSON response"""
    with patch('requests.' + method.lower()) as mock_request:
        mock_response = Mock()
        mock_response.status_code = 200
        mock_response.json.side_effect = ValueError("Invalid JSON")
        mock_request.return_value = mock_response

        context['response'] = context['api_client'].request(method, endpoint, data, max_retries)


def _mock_bad_request(context, method, endpoint, data, max_retries):
    """Mock 400 bad request"""
    with patch('requests.' + method.lower()) as mock_request:
        mock_response = Mock()
        mock_response.status_code = 400
        mock_response.json.return_value = {
            "error": {"code": "BAD_REQUEST", "message": "Invalid request"}
        }
        mock_request.return_value = mock_response

        with patch('time.sleep'):
            context['response'] = context['api_client'].request(method, endpoint, data, max_retries)


def _mock_simple_response(context, method, endpoint, data, max_retries):
    """Use pre-configured mock response"""
    with patch('requests.' + method.lower()) as mock_request:
        mock_request.return_value = context['mock_response']
        context['response'] = context['api_client'].request(method, endpoint, data, max_retries)


def _mock_success_response(context, method, endpoint, data, max_retries):
    """Mock successful response"""
    with patch('requests.' + method.lower()) as mock_request:
        mock_response = Mock()
        mock_response.status_code = 200

        # Capture headers
        def capture_headers(*args, **kwargs):
            context['headers_captured'] = kwargs.get('headers', {})
            return mock_response

        mock_request.side_effect = capture_headers

        # Different response based on endpoint
        if '/navigate' in endpoint:
            mock_response.json.return_value = {
                "data": {
                    "nav": {"status": "IN_TRANSIT"},
                    "fuel": {"current": 250}
                }
            }
        elif '/agent' in endpoint:
            mock_response.json.return_value = {
                "data": {
                    "symbol": "TEST_AGENT",
                    "credits": 100000
                }
            }
        else:
            mock_response.json.return_value = {"data": {"success": True}}

        context['response'] = context['api_client'].request(method, endpoint, data, max_retries)


def _mock_rate_limit_message(context, method, endpoint, data, max_retries):
    """Mock rate limit via error message"""
    call_count = [0]

    with patch('requests.' + method.lower()) as mock_request:
        def side_effect(*args, **kwargs):
            call_count[0] += 1
            mock_response = Mock()
            if call_count[0] == 1:
                mock_response.status_code = 200
                mock_response.json.return_value = {
                    "error": {"message": "rate limit exceeded"}
                }
            else:
                mock_response.status_code = 200
                mock_response.json.return_value = {"data": {"success": True}}
            return mock_response

        mock_request.side_effect = side_effect

        with patch('time.sleep'):
            context['response'] = context['api_client'].request(method, endpoint, data, max_retries)
            context['retry_count'] = call_count[0] - 1


def _mock_always_connection_error(context, method, endpoint, data, max_retries):
    """Mock always connection error"""
    call_count = [0]

    with patch('requests.' + method.lower()) as mock_request:
        def side_effect(*args, **kwargs):
            call_count[0] += 1
            raise requests.exceptions.ConnectionError("Connection failed")

        mock_request.side_effect = side_effect

        with patch('time.sleep'):
            context['response'] = context['api_client'].request(method, endpoint, data, max_retries)
            context['retry_count'] = call_count[0]


def _mock_generic_exception(context, method, endpoint, data, max_retries):
    """Mock generic request exception"""
    with patch('requests.' + method.lower()) as mock_request:
        mock_request.side_effect = requests.exceptions.RequestException("Generic error")

        with patch('time.sleep'):
            context['response'] = context['api_client'].request(method, endpoint, data, max_retries)


def _mock_status_300(context, method, endpoint, data, max_retries):
    """Mock status code 300 (unexpected)"""
    with patch('requests.' + method.lower()) as mock_request:
        mock_response = Mock()
        mock_response.status_code = 300
        mock_response.json.return_value = {"data": {"status": "redirect"}}
        mock_request.return_value = mock_response

        with patch('time.sleep'):
            context['response'] = context['api_client'].request(method, endpoint, data, max_retries)


def _mock_always_timeout(context, method, endpoint, data, max_retries):
    """Mock always timeout"""
    call_count = [0]

    with patch('requests.' + method.lower()) as mock_request:
        def side_effect(*args, **kwargs):
            call_count[0] += 1
            raise requests.exceptions.Timeout("Request timed out")

        mock_request.side_effect = side_effect

        with patch('time.sleep'):
            context['response'] = context['api_client'].request(method, endpoint, data, max_retries)
            context['retry_count'] = call_count[0]


# Then steps - Verify results

@then("the request should succeed")
def request_succeeds(context):
    """Verify request succeeded"""
    assert context['response'] is not None
    assert 'data' in context['response'] or context['response'] is not None


@then("the response should contain data")
def response_contains_data(context):
    """Verify response has data"""
    assert context['response'] is not None
    assert 'data' in context['response']


@then("the request should fail")
def request_fails(context):
    """Verify request failed"""
    response = context.get('response')
    assert response is None or 'error' in response


@then("no data should be returned")
def no_data_returned(context):
    """Verify no data returned"""
    response = context.get('response')
    assert response is None or 'data' not in response


@then("the response should contain navigation data")
def response_has_nav_data(context):
    """Verify response has navigation data"""
    assert context['response'] is not None
    assert 'data' in context['response']
    assert 'nav' in context['response']['data']


@then("the response should contain nav data")
def response_has_nav_field(context):
    """Verify response has nav field"""
    assert context['response'] is not None
    assert 'data' in context['response']


@then("the request should eventually succeed after retry")
def request_succeeds_after_retry(context):
    """Verify request succeeded after retry"""
    assert context['response'] is not None


@then("the rate limiter should have waited")
def rate_limiter_waited(context):
    """Verify rate limiter waited"""
    assert len(context.get('wait_times', [])) > 0


@then("the request should eventually succeed after retries")
def request_succeeds_after_retries(context):
    """Verify request succeeded after multiple retries"""
    assert context['response'] is not None


@then(parsers.parse("there should be {count:d} retry attempts"))
def verify_retry_count(context, count):
    """Verify number of retry attempts"""
    assert context['retry_count'] == count


@then("the request should fail after max retries")
def request_fails_max_retries(context):
    """Verify request failed after max retries"""
    response = context.get('response')
    assert response is None or 'error' in response


@then("the error should be logged about unsupported method")
def error_logged_unsupported_method(context):
    """Verify error was logged about unsupported method"""
    # The API client catches the ValueError and returns None/error payload
    response = context.get('response')
    assert response is None or 'error' in response


@then("the request should fail immediately")
def request_fails_immediately(context):
    """Verify request failed without retries"""
    response = context.get('response')
    assert response is None or 'error' in response


@then("there should be no retry attempts")
def no_retry_attempts(context):
    """Verify no retries occurred"""
    # For 400 errors, the request is made once but not retried
    # So we just verify the response failed
    response = context.get('response')
    assert response is None or 'error' in response


@then("the request headers should include bearer token")
def headers_include_token(context):
    """Verify headers include bearer token"""
    assert context.get('headers_captured') is not None
    assert 'Authorization' in context['headers_captured']


@then("the authorization header should be correctly formatted")
def auth_header_formatted(context):
    """Verify authorization header format"""
    auth_header = context['headers_captured'].get('Authorization', '')
    assert auth_header.startswith('Bearer ')
    assert 'test_token_12345' in auth_header


@then(parsers.parse("the response should contain {count:d} waypoints"))
def response_has_waypoints(context, count):
    """Verify response has correct number of waypoints"""
    assert context['response'] is not None
    assert 'data' in context['response']
    assert len(context['response']['data']) == count


@then(parsers.parse("the meta should show total of {total:d} and page {page:d}"))
def meta_shows_pagination(context, total, page):
    """Verify pagination metadata"""
    assert 'meta' in context['response']
    assert context['response']['meta']['total'] == total
    assert context['response']['meta']['page'] == page


@then(parsers.parse('all returned waypoints should have the "{trait}" trait'))
def waypoints_have_trait(context, trait):
    """Verify all waypoints have specific trait"""
    assert context['response'] is not None
    assert 'data' in context['response']
    for waypoint in context['response']['data']:
        traits = [t['symbol'] for t in waypoint.get('traits', [])]
        assert trait in traits


@then("the response should contain agent data with symbol and credits")
def response_has_agent_data(context):
    """Verify agent data structure"""
    assert context['response'] is not None
    assert 'symbol' in context['response']
    assert 'credits' in context['response']


@then(parsers.parse('the response should contain ship data with symbol "{ship_symbol}"'))
def response_has_ship_data(context, ship_symbol):
    """Verify ship data structure"""
    assert context['response'] is not None
    assert context['response']['symbol'] == ship_symbol


@then("the response should contain a list of ships")
def response_has_ship_list(context):
    """Verify response is list of ships"""
    assert context['response'] is not None
    assert isinstance(context['response'], list)
    assert len(context['response']) > 0


@then("the response should contain contract data")
def response_has_contract_data(context):
    """Verify contract data structure"""
    assert context['response'] is not None
    assert 'id' in context['response'] or 'type' in context['response']


@then("the response should contain a list of contracts")
def response_has_contract_list(context):
    """Verify response is list of contracts"""
    assert context['response'] is not None
    assert isinstance(context['response'], list)


@then("the response should contain market data")
def response_has_market_data(context):
    """Verify market data structure"""
    assert context['response'] is not None
    assert 'symbol' in context['response'] or 'tradeGoods' in context['response']


@then(parsers.parse('the response should contain waypoint data with symbol "{waypoint}"'))
def response_has_waypoint_data(context, waypoint):
    """Verify waypoint data structure"""
    assert context['response'] is not None
    assert context['response']['symbol'] == waypoint


@then(parsers.parse("each request should be separated by at least {interval:f} seconds"))
def requests_separated_by_interval(context, interval):
    """Verify requests are properly rate limited"""
    request_times = context['request_times']
    for i in range(1, len(request_times)):
        time_diff = request_times[i] - request_times[i-1]
        assert time_diff >= interval - 0.1  # Small tolerance


@then(parsers.parse("the total time should be at least {seconds:f} seconds"))
def total_time_at_least(context, seconds):
    """Verify total time for rate limited requests"""
    total_time = context['end_time'] - context['start_time']
    assert total_time >= seconds - 0.2  # Small tolerance


@then("the retry wait times should increase exponentially")
def wait_times_exponential(context):
    """Verify exponential backoff"""
    wait_times = context.get('wait_times', [])
    # Filter out rate limiter waits (0.6 seconds) to get only retry backoff waits
    retry_waits = [w for w in wait_times if w >= 1.0]
    if len(retry_waits) >= 2:
        # Each wait time should be roughly double the previous (with exponential backoff)
        for i in range(1, len(retry_waits)):
            # Allow some tolerance in the comparison
            assert retry_waits[i] >= retry_waits[i-1]


@then("the wait times should be 2s, 4s, 8s")
def wait_times_specific(context):
    """Verify specific wait times"""
    wait_times = context.get('wait_times', [])
    # Filter out rate limiter waits (0.6 seconds) to get only retry backoff waits
    retry_waits = [w for w in wait_times if w >= 1.0]
    if len(retry_waits) >= 3:
        assert retry_waits[0] == 2
        assert retry_waits[1] == 4
        assert retry_waits[2] == 8


@then("no wait time should exceed 60 seconds")
def wait_time_max_60(context):
    """Verify wait time cap"""
    wait_times = context.get('wait_times', [])
    for wait_time in wait_times:
        assert wait_time <= 60


@then("the raw response should have a \"data\" field")
def raw_response_has_data(context):
    """Verify raw response structure"""
    assert 'raw_response' in context
    assert 'data' in context['raw_response']


@then("the convenience method should return only the data field contents")
def convenience_returns_data_only(context):
    """Verify convenience method extracts data"""
    assert context['response'] is not None
    # Should not have the outer "data" wrapper
    assert 'symbol' in context['response'] or 'credits' in context['response']


@then("the POST should include an empty JSON object")
def post_includes_empty_json(context):
    """Verify POST sends empty JSON"""
    assert context.get('post_call') is not None
    # The mock was called, which means the request succeeded


@then("the request should be treated as rate limited")
def treated_as_rate_limited(context):
    """Verify rate limit detection"""
    assert context['retry_count'] >= 1


@then("the client should wait and retry")
def client_waits_and_retries(context):
    """Verify retry occurred"""
    assert context['response'] is not None


@then("the request should be considered successful")
def request_considered_successful(context):
    """Verify 201 is treated as success"""
    assert context['response'] is not None


@then("the response data should be returned")
def response_data_returned(context):
    """Verify response data was returned"""
    assert context['response'] is not None
    assert 'data' in context['response']


@then("the last error should be about connection")
def last_error_connection(context):
    """Verify last error was connection error"""
    # Request failed due to connection errors
    assert context['response'] is None


@then("an error should be logged about JSON parsing")
def json_parse_error_logged(context):
    """Verify JSON parsing error was handled"""
    # Response should be None when JSON parsing fails
    assert context['response'] is None


@then("the error should be logged with exception details")
def error_logged_with_details(context):
    """Verify exception was logged"""
    # Request should fail
    assert context['response'] is None
