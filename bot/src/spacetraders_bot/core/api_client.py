#!/usr/bin/env python3
"""
SpaceTraders API Client - Consolidated API request handling
Centralizes all API interactions with rate limiting, retry logic, and error handling
"""

import logging
import time
from dataclasses import dataclass
from typing import Any, Dict, Optional

import requests

# Configure module logger
logger = logging.getLogger(__name__)


@dataclass
class APIResult:
    """Typed result wrapper for API responses."""

    ok: bool
    data: Optional[Dict[str, Any]] = None
    error: Optional[Dict[str, Any]] = None
    status_code: Optional[int] = None

    @property
    def message(self) -> Optional[str]:
        if not self.error:
            return None
        return self.error.get("message")

    @classmethod
    def success(cls, data: Dict[str, Any], status_code: int) -> "APIResult":
        return cls(ok=True, data=data, status_code=status_code)

    @classmethod
    def failure(
        cls,
        error: Dict[str, Any],
        status_code: Optional[int],
        data: Optional[Dict[str, Any]] = None,
    ) -> "APIResult":
        return cls(ok=False, data=data, error=error, status_code=status_code)


class RateLimiter:
    """Rate limiter to ensure API compliance (2 req/sec sustained, 10 burst/10s)"""

    def __init__(self, min_interval: float = 0.6):
        self.min_interval = min_interval
        self.last_request_time = 0

    def wait(self):
        """Wait if necessary to respect rate limits"""
        current_time = time.time()
        elapsed = current_time - self.last_request_time

        if elapsed < self.min_interval:
            wait_time = self.min_interval - elapsed
            time.sleep(wait_time)

        self.last_request_time = time.time()


class APIClient:
    """SpaceTraders API Client with comprehensive error handling"""

    def __init__(self, token: str, base_url: str = "https://api.spacetraders.io/v2"):
        self.token = token
        self.base_url = base_url
        self.headers = {
            "Authorization": f"Bearer {token}",
            "Content-Type": "application/json"
        }
        self.rate_limiter = RateLimiter()

    def request(
        self,
        method: str,
        endpoint: str,
        data: Optional[Dict[str, Any]] = None,
        max_retries: int = 20
    ) -> Optional[Dict[str, Any]]:
        """
        Make API request with rate limiting and retry logic

        Args:
            method: HTTP method (GET, POST, PATCH)
            endpoint: API endpoint (e.g., '/my/ships')
            data: Request payload for POST/PATCH
            max_retries: Maximum retry attempts

        Returns:
            API response data or None on failure
        """
        result = self.request_result(method, endpoint, data=data, max_retries=max_retries)

        if result.ok:
            return result.data

        if result.status_code and 400 <= result.status_code < 500:
            if result.data:
                return result.data
            if result.error:
                return {"error": result.error}

        return None

    def request_result(
        self,
        method: str,
        endpoint: str,
        data: Optional[Dict[str, Any]] = None,
        max_retries: int = 20,
    ) -> APIResult:
        """Same as `request` but returns typed result wrapper."""

        url = f"{self.base_url}{endpoint}"
        retry_count = 0
        wait_time = 2

        while retry_count < max_retries:
            self.rate_limiter.wait()

            try:
                if method == "GET":
                    response = requests.get(url, headers=self.headers, timeout=30)
                elif method == "POST":
                    response = requests.post(url, headers=self.headers, json=data or {}, timeout=30)
                elif method == "PATCH":
                    response = requests.patch(url, headers=self.headers, json=data or {}, timeout=30)
                else:
                    logger.error(f"Unsupported HTTP method: {method}")
                    raise ValueError(f"Unsupported HTTP method: {method}")

                try:
                    response_data = response.json()
                except ValueError as e:
                    logger.error(
                        "Failed to parse JSON response from %s %s: %s",
                        method,
                        endpoint,
                        e,
                    )
                    return APIResult.failure(
                        {"code": "invalid_json", "message": str(e)},
                        response.status_code,
                    )

                rate_limited = response.status_code == 429 or (
                    isinstance(response_data, dict)
                    and "error" in response_data
                    and "rate limit" in str(response_data["error"].get("message", "")).lower()
                )
                if rate_limited:
                    retry_count += 1
                    logger.warning(
                        "⚠️  %s %s - Rate limit hit, waiting %ss (attempt %s/%s)",
                        method,
                        endpoint,
                        wait_time,
                        retry_count,
                        max_retries,
                    )
                    if retry_count < max_retries:
                        time.sleep(wait_time)
                        wait_time = min(wait_time * 2, 60)
                        continue

                    logger.error("Max retries exceeded after rate limiting on %s %s", method, endpoint)
                    return APIResult.failure(
                        {"code": "rate_limited", "message": "Rate limit exceeded"},
                        response.status_code,
                        data=response_data if isinstance(response_data, dict) else None,
                    )

                if response.status_code in [200, 201]:
                    return APIResult.success(response_data, response.status_code)

                if 400 <= response.status_code < 500:
                    error_payload = (
                        response_data.get("error")
                        if isinstance(response_data, dict)
                        else None
                    ) or {
                        "code": "HTTP_4XX",
                        "message": str(response_data),
                    }
                    logger.error(
                        "❌ %s %s - Client Error (HTTP %s): %s - %s",
                        method,
                        endpoint,
                        response.status_code,
                        error_payload.get("code", "UNKNOWN"),
                        error_payload.get("message", ""),
                    )
                    return APIResult.failure(
                        error_payload,
                        response.status_code,
                        data=response_data if isinstance(response_data, dict) else None,
                    )

                if 500 <= response.status_code < 600:
                    retry_count += 1
                    logger.warning(
                        "⚠️  %s %s - Server Error (HTTP %s), retrying (attempt %s/%s)",
                        method,
                        endpoint,
                        response.status_code,
                        retry_count,
                        max_retries,
                    )

                    if retry_count < max_retries:
                        time.sleep(wait_time)
                        wait_time = min(wait_time * 2, 60)
                        continue

                    logger.error("Max retries exceeded for server error on %s %s", method, endpoint)
                    return APIResult.failure(
                        {"code": "HTTP_5XX", "message": "Server error"},
                        response.status_code,
                        data=response_data if isinstance(response_data, dict) else None,
                    )

                logger.warning("Unexpected status code %s for %s %s", response.status_code, method, endpoint)
                return APIResult.failure(
                    {"code": "unexpected_status", "message": str(response_data)},
                    response.status_code,
                    data=response_data if isinstance(response_data, dict) else None,
                )

            except requests.exceptions.Timeout:
                retry_count += 1
                logger.warning(
                    "⏱️  Request timeout on %s %s (attempt %s/%s)",
                    method,
                    endpoint,
                    retry_count,
                    max_retries,
                )
                if retry_count < max_retries:
                    time.sleep(wait_time)
                    wait_time = min(wait_time * 2, 60)
                    continue
                logger.error("Request timeout - max retries exceeded on %s %s", method, endpoint)
                return APIResult.failure(
                    {"code": "timeout", "message": "Request timed out"},
                    None,
                )

            except requests.exceptions.ConnectionError:
                retry_count += 1
                logger.warning(
                    "🔌 Connection error on %s %s (attempt %s/%s)",
                    method,
                    endpoint,
                    retry_count,
                    max_retries,
                )
                if retry_count < max_retries:
                    time.sleep(wait_time)
                    wait_time = min(wait_time * 2, 60)
                    continue
                logger.error("Connection error - max retries exceeded on %s %s", method, endpoint)
                return APIResult.failure(
                    {"code": "connection_error", "message": "Connection error"},
                    None,
                )

            except requests.exceptions.RequestException as e:
                retry_count += 1
                logger.warning(
                    "Network error on %s %s (attempt %s/%s): %s",
                    method,
                    endpoint,
                    retry_count,
                    max_retries,
                    e,
                )
                if retry_count < max_retries:
                    time.sleep(wait_time)
                    wait_time = min(wait_time * 2, 60)
                    continue
                logger.error(
                    "Request exception - max retries exceeded on %s %s: %s",
                    method,
                    endpoint,
                    e,
                )
                return APIResult.failure(
                    {"code": "request_exception", "message": str(e)},
                    None,
                )

            except Exception as e:
                logger.error(
                    "Unexpected error on %s %s: %s: %s",
                    method,
                    endpoint,
                    type(e).__name__,
                    e,
                )
                logger.exception("Full traceback:")
                return APIResult.failure(
                    {"code": "unexpected_error", "message": str(e)},
                    None,
                )

        logger.error(f"❌ Max retries ({max_retries}) exceeded for {method} {endpoint}")
        return APIResult.failure(
            {"code": "max_retries", "message": "Max retries exceeded"},
            None,
        )

    def get(self, endpoint: str) -> Optional[Dict[str, Any]]:
        """GET request"""
        return self.request("GET", endpoint)

    def post(self, endpoint: str, data: Optional[Dict[str, Any]] = None) -> Optional[Dict[str, Any]]:
        """POST request"""
        return self.request("POST", endpoint, data)

    def patch(self, endpoint: str, data: Optional[Dict[str, Any]] = None) -> Optional[Dict[str, Any]]:
        """PATCH request"""
        return self.request("PATCH", endpoint, data)

    # Convenience methods for common operations
    def get_agent(self) -> Optional[Dict[str, Any]]:
        """Get agent details"""
        result = self.get("/my/agent")
        return result.get("data") if result else None

    def get_ship(self, ship_symbol: str) -> Optional[Dict[str, Any]]:
        """Get ship details"""
        result = self.get(f"/my/ships/{ship_symbol}")
        return result.get("data") if result else None

    def list_ships(self) -> Optional[list]:
        """List all ships"""
        result = self.get("/my/ships")
        return result.get("data") if result else None

    def get_contract(self, contract_id: str) -> Optional[Dict[str, Any]]:
        """Get contract details"""
        result = self.get(f"/my/contracts/{contract_id}")
        return result.get("data") if result else None

    def list_contracts(self) -> Optional[list]:
        """List all contracts"""
        result = self.get("/my/contracts")
        return result.get("data") if result else None

    def get_market(self, system: str, waypoint: str) -> Optional[Dict[str, Any]]:
        """Get market data for waypoint"""
        result = self.get(f"/systems/{system}/waypoints/{waypoint}/market")
        return result.get("data") if result else None

    def get_waypoint(self, system: str, waypoint: str) -> Optional[Dict[str, Any]]:
        """Get waypoint details"""
        result = self.get(f"/systems/{system}/waypoints/{waypoint}")
        return result.get("data") if result else None

    def list_waypoints(self, system: str, limit: int = 20, page: int = 1, traits: Optional[str] = None) -> Optional[Dict[str, Any]]:
        """List waypoints in system with pagination support"""
        endpoint = f"/systems/{system}/waypoints?limit={limit}&page={page}"
        if traits:
            endpoint += f"&traits={traits}"
        result = self.get(endpoint)
        # Return full result (includes meta with pagination info)
        return result if result else None
