#!/usr/bin/env python3
"""
SpaceTraders API Client - Consolidated API request handling
Centralizes all API interactions with rate limiting, retry logic, and error handling
"""

import requests
import time
import logging
from typing import Optional, Dict, Any
from datetime import datetime

# Configure module logger
logger = logging.getLogger(__name__)


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
        max_retries: int = 5
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
        url = f"{self.base_url}{endpoint}"
        retry_count = 0
        wait_time = 2

        while retry_count < max_retries:
            # Rate limiting
            self.rate_limiter.wait()

            try:
                # Make request
                if method == "GET":
                    response = requests.get(url, headers=self.headers, timeout=30)
                elif method == "POST":
                    response = requests.post(url, headers=self.headers, json=data or {}, timeout=30)
                elif method == "PATCH":
                    response = requests.patch(url, headers=self.headers, json=data or {}, timeout=30)
                else:
                    logger.error(f"Unsupported HTTP method: {method}")
                    raise ValueError(f"Unsupported HTTP method: {method}")

                # Parse response
                try:
                    response_data = response.json()
                except ValueError as e:
                    logger.error(f"Failed to parse JSON response from {method} {endpoint}: {e}")
                    return None

                # Handle rate limiting (429 or error message)
                if response.status_code == 429 or (
                    "error" in response_data
                    and "rate limit" in response_data["error"].get("message", "").lower()
                ):
                    retry_count += 1
                    logger.warning(f"Rate limit hit on {method} {endpoint}, waiting {wait_time}s (attempt {retry_count}/{max_retries})")
                    time.sleep(wait_time)
                    wait_time = min(wait_time * 2, 60)  # Exponential backoff, max 60s
                    continue

                # Handle success
                if response.status_code in [200, 201]:
                    return response_data

                # Handle client errors (4xx)
                if 400 <= response.status_code < 500:
                    error_msg = response_data.get("error", {})
                    error_code = error_msg.get("code", "UNKNOWN")
                    error_message = error_msg.get("message", str(response_data))

                    logger.error(f"❌ {method} {endpoint} - Client Error (HTTP {response.status_code}): {error_code} - {error_message}")

                    # Return response_data with error info instead of None
                    # This allows caller to handle specific error codes (e.g., 4604 transaction limits)
                    return response_data

                # Handle server errors (5xx) - retry these
                if 500 <= response.status_code < 600:
                    retry_count += 1
                    logger.warning(f"⚠️  {method} {endpoint} - Server Error (HTTP {response.status_code}), retrying (attempt {retry_count}/{max_retries})")

                    if retry_count < max_retries:
                        time.sleep(wait_time)
                        wait_time = min(wait_time * 2, 60)
                        continue
                    else:
                        logger.error(f"Max retries exceeded for server error on {method} {endpoint}")
                        return None

                # Handle other status codes
                logger.warning(f"Unexpected status code {response.status_code} for {method} {endpoint}")
                return None

            except requests.exceptions.Timeout:
                retry_count += 1
                logger.warning(f"⏱️  Request timeout on {method} {endpoint} (attempt {retry_count}/{max_retries})")
                if retry_count < max_retries:
                    time.sleep(wait_time)
                    wait_time = min(wait_time * 2, 60)
                    continue
                logger.error(f"Request timeout - max retries exceeded on {method} {endpoint}")
                return None

            except requests.exceptions.ConnectionError:
                retry_count += 1
                logger.warning(f"🔌 Connection error on {method} {endpoint} (attempt {retry_count}/{max_retries})")
                if retry_count < max_retries:
                    time.sleep(wait_time)
                    wait_time = min(wait_time * 2, 60)
                    continue
                logger.error(f"Connection error - max retries exceeded on {method} {endpoint}")
                return None

            except requests.exceptions.RequestException as e:
                retry_count += 1
                logger.warning(f"Network error on {method} {endpoint} (attempt {retry_count}/{max_retries}): {e}")
                if retry_count < max_retries:
                    time.sleep(wait_time)
                    wait_time = min(wait_time * 2, 60)
                    continue
                logger.error(f"Request exception - max retries exceeded on {method} {endpoint}: {e}")
                return None

            except Exception as e:
                logger.error(f"Unexpected error on {method} {endpoint}: {type(e).__name__}: {e}")
                logger.exception("Full traceback:")
                return None

        logger.error(f"❌ Max retries ({max_retries}) exceeded for {method} {endpoint}")
        return None

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
