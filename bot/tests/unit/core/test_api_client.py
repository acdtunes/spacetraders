from typing import Any, Dict

import pytest

from spacetraders_bot.core.api_client import APIClient, APIResult


class StubResponse:
    def __init__(self, status_code: int, payload: Dict[str, Any]):
        self.status_code = status_code
        self._payload = payload

    def json(self):
        return self._payload


@pytest.fixture
def client(monkeypatch):
    api_client = APIClient("token", base_url="https://unit.test")
    monkeypatch.setattr(api_client.rate_limiter, "wait", lambda: None)
    return api_client


def test_api_result_success_and_failure_helpers():
    success = APIResult.success({"foo": "bar"}, status_code=200)
    assert success.ok is True
    assert success.data == {"foo": "bar"}
    assert success.status_code == 200

    failure = APIResult.failure({"message": "boom"}, status_code=400, data={"error": "boom"})
    assert failure.ok is False
    assert failure.error == {"message": "boom"}
    assert failure.data == {"error": "boom"}
    assert failure.status_code == 400


def test_request_result_success(monkeypatch, client):
    response = StubResponse(200, {"data": {"id": "ship-1"}})
    monkeypatch.setattr(
        "spacetraders_bot.core.api_client.requests.get",
        lambda url, headers, timeout: response,
    )

    result = client.request_result("GET", "/my/ships/ship-1")

    assert result.ok is True
    assert result.data == {"data": {"id": "ship-1"}}
    assert result.status_code == 200


def test_request_result_client_error_preserves_payload(monkeypatch, client):
    payload = {"error": {"code": "SHIP_NOT_FOUND", "message": "nope"}}
    response = StubResponse(404, payload)
    monkeypatch.setattr(
        "spacetraders_bot.core.api_client.requests.get",
        lambda url, headers, timeout: response,
    )

    result = client.request_result("GET", "/my/ships/missing")

    assert result.ok is False
    assert result.status_code == 404
    assert result.error == payload["error"]
    assert result.data == payload


def test_request_wraps_result_for_client_error(monkeypatch, client):
    payload = {"error": {"code": "SHIP_NOT_FOUND", "message": "nope"}}
    response = StubResponse(404, payload)
    monkeypatch.setattr(
        "spacetraders_bot.core.api_client.requests.get",
        lambda url, headers, timeout: response,
    )

    raw_response = client.request("GET", "/my/ships/missing")

    assert raw_response == payload


def test_rate_limit_retries_until_success(monkeypatch, client):
    responses = iter(
        [
            StubResponse(429, {"error": {"message": "Rate limit hit"}}),
            StubResponse(200, {"data": {"ok": True}}),
        ]
    )
    call_count = {"count": 0}

    def fake_get(url, headers, timeout):
        call_count["count"] += 1
        return next(responses)

    monkeypatch.setattr(
        "spacetraders_bot.core.api_client.requests.get",
        fake_get,
    )
    monkeypatch.setattr("spacetraders_bot.core.api_client.time.sleep", lambda *_: None)

    result = client.request_result("GET", "/whatever", max_retries=3)

    assert result.ok is True
    assert result.data == {"data": {"ok": True}}
    assert call_count["count"] == 2


def test_request_returns_none_for_server_failure(monkeypatch, client):
    response = StubResponse(503, {"error": {"message": "down"}})
    monkeypatch.setattr(
        "spacetraders_bot.core.api_client.requests.get",
        lambda url, headers, timeout: response,
    )
    monkeypatch.setattr("spacetraders_bot.core.api_client.time.sleep", lambda *_: None)

    result = client.request("GET", "/service", max_retries=1)
    assert result is None
