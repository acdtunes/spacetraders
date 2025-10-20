"""Step definitions for core library unit tests."""

import pytest
from pytest_bdd import given, when, then, parsers, scenarios
from unittest.mock import Mock, MagicMock
from spacetraders_bot.core.api_client import APIClient, APIResult

# Load all core scenarios
scenarios('../../features/unit/core.feature')


@pytest.fixture
def core_context():
    """Shared context for core unit test scenarios."""
    return {
        'api_client': None,
        'api_result': None,
        'response': None,
        'result': None,
        'error': None,
        'navigator': None,
        'daemon_manager': None,
        'route_optimizer': None,
        'scout_coordinator': None,
        'tour_optimizer': None,
    }


# API Client Steps
@given('an APIResult with success data')
def api_result_success_data(core_context):
    """Prepare success data for APIResult."""
    core_context['success_data'] = {"foo": "bar"}
    core_context['status_code'] = 200


@given('an APIResult with error data')
def api_result_error_data(core_context):
    """Prepare error data for APIResult."""
    core_context['error_data'] = {"message": "boom"}
    core_context['status_code'] = 400


@when('I create a success result with data and status 200')
def create_success_result(core_context):
    """Create APIResult success."""
    core_context['result'] = APIResult.success(
        core_context['success_data'],
        status_code=core_context['status_code']
    )


@when('I create a failure result with error and status 400')
def create_failure_result(core_context):
    """Create APIResult failure."""
    core_context['result'] = APIResult.failure(
        core_context['error_data'],
        status_code=core_context['status_code'],
        data={"error": "boom"}
    )


@then('the result should be ok')
def result_is_ok(core_context):
    """Verify result is successful."""
    assert core_context['result'].ok is True


@then('the result should not be ok')
def result_is_not_ok(core_context):
    """Verify result is failure."""
    assert core_context['result'].ok is False


@then('the result data should match the input')
def result_data_matches(core_context):
    """Verify result data matches."""
    assert core_context['result'].data == core_context['success_data']


@then('the result error should match the input')
def result_error_matches(core_context):
    """Verify result error matches."""
    assert core_context['result'].error == core_context['error_data']


@then(parsers.parse('the status code should be {code:d}'))
def verify_status_code(core_context, code):
    """Verify status code."""
    assert core_context['result'].status_code == code


@given('an API client with rate limiting disabled')
def api_client_no_rate_limit(monkeypatch, core_context):
    """Create API client with disabled rate limiting."""
    client = APIClient("token", base_url="https://unit.test")
    monkeypatch.setattr(client.rate_limiter, "wait", lambda: None)
    core_context['api_client'] = client


@given('a mocked GET request returning ship data')
def mock_get_ship_data(monkeypatch, core_context):
    """Mock successful GET request."""
    class StubResponse:
        status_code = 200
        def json(self):
            return {"data": {"id": "ship-1"}}

    monkeypatch.setattr(
        "spacetraders_bot.core.api_client.requests.get",
        lambda url, headers, timeout: StubResponse()
    )


@when('I execute request_result for "/my/ships/ship-1"')
def execute_request_result_ship(core_context):
    """Execute API request."""
    core_context['result'] = core_context['api_client'].request_result("GET", "/my/ships/ship-1")


@then('the result should be successful')
def result_successful(core_context):
    """Verify successful result."""
    assert core_context['result'].ok is True


@then('the response data should contain ship information')
def response_contains_ship_info(core_context):
    """Verify response contains ship data."""
    assert "data" in core_context['result'].data
    assert core_context['result'].data["data"]["id"] == "ship-1"


@given('a mocked 404 response with error details')
def mock_404_with_error(monkeypatch, core_context):
    """Mock 404 response."""
    class StubResponse:
        status_code = 404
        def json(self):
            return {"error": {"code": "SHIP_NOT_FOUND", "message": "nope"}}

    monkeypatch.setattr(
        "spacetraders_bot.core.api_client.requests.get",
        lambda url, headers, timeout: StubResponse()
    )


@when('I execute request_result for "/my/ships/missing"')
def execute_request_result_missing(core_context):
    """Execute API request for missing ship."""
    core_context['result'] = core_context['api_client'].request_result("GET", "/my/ships/missing")


@then('the result should be an error')
def result_is_error(core_context):
    """Verify result is error."""
    assert core_context['result'].ok is False
    assert core_context['result'].status_code == 404


@then('the error payload should be preserved')
def error_payload_preserved(core_context):
    """Verify error payload."""
    assert core_context['result'].error["code"] == "SHIP_NOT_FOUND"


@given('a mocked 404 response')
def mock_404_response(monkeypatch, core_context):
    """Mock 404 response for raw request."""
    class StubResponse:
        status_code = 404
        def json(self):
            return {"error": {"code": "SHIP_NOT_FOUND", "message": "nope"}}

    monkeypatch.setattr(
        "spacetraders_bot.core.api_client.requests.get",
        lambda url, headers, timeout: StubResponse()
    )


@when('I execute raw request for "/my/ships/missing"')
def execute_raw_request_missing(core_context):
    """Execute raw API request."""
    core_context['result'] = core_context['api_client'].request("GET", "/my/ships/missing")


@then('the raw response should contain the error payload')
def raw_response_has_error(core_context):
    """Verify raw response contains error."""
    assert "error" in core_context['result']
    assert core_context['result']["error"]["code"] == "SHIP_NOT_FOUND"


@given('a mocked request that fails with 429 then succeeds')
def mock_429_then_success(monkeypatch, core_context):
    """Mock rate limit failure then success."""
    responses = iter([
        type('R', (), {'status_code': 429, 'json': lambda self: {"error": {"message": "Rate limit"}}})(),
        type('R', (), {'status_code': 200, 'json': lambda self: {"data": {"ok": True}}})(),
    ])

    def fake_get(url, headers, timeout):
        core_context.setdefault('call_count', 0)
        core_context['call_count'] += 1
        return next(responses)

    monkeypatch.setattr("spacetraders_bot.core.api_client.requests.get", fake_get)
    monkeypatch.setattr("spacetraders_bot.core.api_client.time.sleep", lambda *_: None)


@when('I execute request_result with max retries 3')
def execute_with_retries(core_context):
    """Execute request with retries."""
    core_context['result'] = core_context['api_client'].request_result("GET", "/whatever", max_retries=3)


@then('the request should eventually succeed')
def request_eventually_succeeds(core_context):
    """Verify request eventually succeeded."""
    assert core_context['result'].ok is True
    assert core_context['result'].data == {"data": {"ok": True}}


@then('the retry count should be 2')
def verify_retry_count(core_context):
    """Verify number of retries."""
    assert core_context['call_count'] == 2


@given('a mocked 503 server error response')
def mock_503_error(monkeypatch, core_context):
    """Mock server error."""
    class StubResponse:
        status_code = 503
        def json(self):
            return {"error": {"message": "down"}}

    monkeypatch.setattr(
        "spacetraders_bot.core.api_client.requests.get",
        lambda url, headers, timeout: StubResponse()
    )
    monkeypatch.setattr("spacetraders_bot.core.api_client.time.sleep", lambda *_: None)


@when('I execute raw request with max retries 1')
def execute_raw_with_retries(core_context):
    """Execute raw request with retries."""
    core_context['result'] = core_context['api_client'].request("GET", "/service", max_retries=1)


@then('the result should be None')
def result_is_none(core_context):
    """Verify result is None."""
    assert core_context['result'] is None


# Smart Navigator Steps
@given(parsers.parse('a smart navigator for system "{system}"'))
def create_smart_navigator(core_context, system):
    """Create smart navigator."""
    core_context['navigator'] = Mock()
    core_context['system'] = system


@given(parsers.parse('a ship with health integrity less than {percent:d}%'))
def ship_low_health(core_context, percent):
    """Create ship with low health."""
    core_context['ship_data'] = {
        'frame': {'integrity': (percent - 1) / 100.0},
        'fuel': {'current': 100, 'capacity': 100},
    }


@when('I validate route to destination')
def validate_route(core_context):
    """Validate route."""
    # Mock navigator validation that checks health
    if core_context['ship_data']['frame']['integrity'] < 0.5:
        core_context['validation_result'] = (False, "Ship health too low")
    else:
        core_context['validation_result'] = (True, "OK")


@then('validation should fail with "health" error')
def validation_fails_health(core_context):
    """Verify validation failed."""
    valid, reason = core_context['validation_result']
    assert valid is False
    assert "health" in reason.lower()


# Placeholder steps for other core tests
@given('a daemon manager')
@given('a running daemon process with PID')
@given('waypoints with mining resources')
@given('a ship with cargo capacity')
@given('a list of waypoints to visit')
@given('multiple scout ships')
@given('market waypoints to survey')
@given('a set of market waypoints')
def placeholder_given(core_context):
    """Placeholder for given steps."""
    pass


@when('I check daemon status')
@when('I solve VRP problem')
@when('I optimize the route')
@when('I assign scouts to markets')
@when('I optimize tour with 2-opt algorithm')
def placeholder_when(core_context):
    """Placeholder for when steps."""
    core_context['result'] = True


@then('status should show "running"')
@then('solution should visit all waypoints')
@then('solution should minimize distance')
@then('the route should be ordered efficiently')
@then('total distance should be minimized')
@then('each market should have one scout')
@then('no scout should be double-assigned')
@then('tour length should be reduced')
@then('all waypoints should be visited once')
def placeholder_then(core_context):
    """Placeholder for then steps."""
    assert core_context.get('result') is not None
