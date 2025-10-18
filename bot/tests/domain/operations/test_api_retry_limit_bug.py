#!/usr/bin/env python3
"""
Test: API Rate Limiting - Insufficient Retries Bug

Bug Description:
The API client gives up after only 5 retries when hitting SpaceTraders rate limits (429 errors).
This causes 'Max retries exceeded' failures during heavy trading operations.

Expected Behavior:
- Retry up to 20 times on rate limit (429) errors
- Use exponential backoff: 2s → 4s → 8s → 16s → 32s → 60s (cap at 60s)
- Only give up after 20 attempts

Root Cause:
api_client.py line 83: max_retries: int = 5 (too low for production load)
"""

import pytest
from unittest.mock import Mock, patch, MagicMock
from src.spacetraders_bot.core.api_client import APIClient, APIResult


def test_api_retries_20_times_on_rate_limit():
    """
    GIVEN an API client making a request
    WHEN the server returns 429 (rate limit) errors repeatedly
    THEN the client should retry up to 20 times before giving up
    """
    api = APIClient(token="test_token")

    # Mock response with 429 rate limit
    mock_response = Mock()
    mock_response.status_code = 429
    mock_response.json.return_value = {
        "error": {
            "code": "rate_limit",
            "message": "Rate limit exceeded"
        }
    }

    call_count = 0

    def mock_get(*args, **kwargs):
        nonlocal call_count
        call_count += 1
        return mock_response

    # Patch requests.get and time.sleep to speed up test
    with patch('requests.get', side_effect=mock_get), \
         patch('time.sleep'):

        result = api.request_result("GET", "/my/ships")

        # Should have tried 20 times (not just 5)
        assert call_count == 20, f"Expected 20 retries, got {call_count}"

        # Should return failure result
        assert not result.ok
        assert result.error['code'] == 'rate_limited'


def test_api_exponential_backoff_caps_at_60_seconds():
    """
    GIVEN an API client retrying on rate limits
    WHEN exponential backoff is applied
    THEN wait times should be: 2s, 4s, 8s, 16s, 32s, then cap at 60s
    """
    api = APIClient(token="test_token")

    # Mock response with 429 rate limit
    mock_response = Mock()
    mock_response.status_code = 429
    mock_response.json.return_value = {
        "error": {
            "code": "rate_limit",
            "message": "Rate limit exceeded"
        }
    }

    sleep_times = []

    def mock_sleep(duration):
        sleep_times.append(duration)

    # Patch requests.get and time.sleep
    with patch('requests.get', return_value=mock_response), \
         patch('time.sleep', side_effect=mock_sleep):

        api.request_result("GET", "/my/ships", max_retries=10)

        # Verify exponential backoff pattern
        # Expected: 2, 4, 8, 16, 32, 60, 60, 60, 60, ... (caps at 60)
        # Note: Rate limiter also adds 0.6s waits, so filter for backoff waits (>= 2s)
        backoff_waits = [t for t in sleep_times if t >= 2.0]
        expected_pattern = [2, 4, 8, 16, 32, 60, 60, 60, 60]

        # Check backoff progression
        for i, expected_wait in enumerate(expected_pattern):
            if i < len(backoff_waits):
                assert backoff_waits[i] == expected_wait, \
                    f"Retry {i+1}: expected {expected_wait}s wait, got {backoff_waits[i]}s"


def test_api_succeeds_after_partial_rate_limit_failures():
    """
    GIVEN an API client making a request
    WHEN the first 5 attempts hit rate limits but the 6th succeeds
    THEN the client should return success (not give up after 5 retries)
    """
    api = APIClient(token="test_token")

    call_count = 0

    def mock_get(*args, **kwargs):
        nonlocal call_count
        call_count += 1

        # First 5 calls fail with 429, 6th succeeds
        if call_count <= 5:
            mock_response = Mock()
            mock_response.status_code = 429
            mock_response.json.return_value = {
                "error": {
                    "code": "rate_limit",
                    "message": "Rate limit exceeded"
                }
            }
            return mock_response
        else:
            # 6th call succeeds
            mock_response = Mock()
            mock_response.status_code = 200
            mock_response.json.return_value = {
                "data": {"ship": "SHIP-1"}
            }
            return mock_response

    # Patch requests.get and time.sleep
    with patch('requests.get', side_effect=mock_get), \
         patch('time.sleep'):

        result = api.request_result("GET", "/my/ships")

        # Should have retried and eventually succeeded
        assert call_count == 6, f"Expected 6 attempts, got {call_count}"
        assert result.ok, "Request should succeed after retries"
        assert result.data == {"data": {"ship": "SHIP-1"}}


def test_transaction_limit_error_auto_retries_with_batching():
    """
    GIVEN a ship attempting to sell >20 units of cargo
    WHEN the market has a 20-unit transaction limit (error 4604)
    THEN the ship controller should automatically batch the sale into 20-unit chunks

    This validates Bug #1 fix is working.
    """
    from src.spacetraders_bot.core.ship_controller import ShipController
    from src.spacetraders_bot.core.api_client import APIClient

    api = APIClient(token="test_token")
    ship = ShipController(api, "SHIP-1")

    # Mock ship.get_status() to avoid API call
    mock_status = {
        "nav": {
            "status": "DOCKED",
            "systemSymbol": "X1-TEST",
            "waypointSymbol": "X1-TEST-A1"
        },
        "fuel": {"current": 100, "capacity": 100},
        "cargo": {"units": 45, "capacity": 60}
    }
    ship.get_status = Mock(return_value=mock_status)

    call_count = 0
    total_sold = 0

    def mock_post(endpoint, data=None):
        nonlocal call_count, total_sold
        call_count += 1

        # First call tries to sell 45 units → fails with transaction limit
        if call_count == 1:
            return {
                "error": {
                    "code": 4604,
                    "message": "Market transaction failed. Trade good MEDICINE has a limit of 20 units per transaction."
                }
            }

        # Subsequent calls should be batched to ≤20 units each
        units = data.get('units', 0)
        assert units <= 20, f"Batch {call_count} tried to sell {units} units (should be ≤20)"

        total_sold += units
        return {
            "data": {
                "transaction": {
                    "units": units,
                    "tradeSymbol": "MEDICINE",
                    "pricePerUnit": 100,
                    "totalPrice": units * 100
                }
            }
        }

    # Patch API post method
    api.post = Mock(side_effect=mock_post)

    # Attempt to sell 45 units
    result = ship.sell("MEDICINE", 45)

    # Should have batched into 3 transactions: 20 + 20 + 5
    assert call_count == 4, f"Expected 4 API calls (1 failed + 3 batches), got {call_count}"
    assert total_sold == 45, f"Expected 45 units sold, got {total_sold}"
    assert result is not None, "Sale should succeed with automatic batching"
    assert result['units'] == 45


if __name__ == "__main__":
    pytest.main([__file__, "-v"])
