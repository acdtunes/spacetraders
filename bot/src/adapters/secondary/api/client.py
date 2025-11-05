import requests
import logging
import time
from typing import Dict, Optional

from ports.outbound.api_client import ISpaceTradersAPI
from .rate_limiter import RateLimiter

logger = logging.getLogger(__name__)

class SpaceTradersAPIClient(ISpaceTradersAPI):
    """
    SpaceTraders HTTP API client with rate limiting

    Rate limit: 2 requests/second (token bucket)
    Automatic retry on 429 errors
    """

    BASE_URL = "https://api.spacetraders.io/v2"

    def __init__(self, token: str):
        self._token = token
        self._session = requests.Session()
        self._session.headers.update({
            "Authorization": f"Bearer {token}",
            "Content-Type": "application/json"
        })
        self._rate_limiter = RateLimiter(max_requests=2, time_window=1.0)

    def _request(self, method: str, endpoint: str, **kwargs) -> Dict:
        """Make rate-limited HTTP request with retry"""
        self._rate_limiter.acquire()

        url = f"{self.BASE_URL}{endpoint}"
        max_retries = 3

        for attempt in range(max_retries):
            try:
                response = self._session.request(method, url, **kwargs)

                if response.status_code == 429:
                    # Rate limited - exponential backoff
                    wait_time = (2 ** attempt) * 1.0
                    logger.warning(f"Rate limited, waiting {wait_time}s")
                    time.sleep(wait_time)
                    continue

                # Check for error status before raising
                if not response.ok:
                    # Log the error response body for debugging
                    try:
                        error_body = response.json()
                        logger.error(f"API error {response.status_code}: {error_body}")
                    except:
                        logger.error(f"API error {response.status_code}: {response.text}")

                response.raise_for_status()
                return response.json()

            except requests.exceptions.RequestException as e:
                if attempt == max_retries - 1:
                    raise
                logger.warning(f"Request failed, retrying: {e}")
                time.sleep(1.0)

        raise RuntimeError(f"Request failed after {max_retries} attempts")

    def get_agent(self) -> Dict:
        return self._request("GET", "/my/agent")

    def get_ship(self, ship_symbol: str) -> Dict:
        return self._request("GET", f"/my/ships/{ship_symbol}")

    def get_ships(self) -> Dict:
        return self._request("GET", "/my/ships")

    def navigate_ship(self, ship_symbol: str, waypoint: str) -> Dict:
        return self._request(
            "POST",
            f"/my/ships/{ship_symbol}/navigate",
            json={"waypointSymbol": waypoint}
        )

    def dock_ship(self, ship_symbol: str) -> Dict:
        return self._request("POST", f"/my/ships/{ship_symbol}/dock", json={})

    def orbit_ship(self, ship_symbol: str) -> Dict:
        return self._request("POST", f"/my/ships/{ship_symbol}/orbit", json={})

    def refuel_ship(self, ship_symbol: str) -> Dict:
        return self._request("POST", f"/my/ships/{ship_symbol}/refuel", json={})

    def set_flight_mode(self, ship_symbol: str, flight_mode: str) -> Dict:
        return self._request(
            "PATCH",
            f"/my/ships/{ship_symbol}/nav",
            json={"flightMode": flight_mode}
        )

    def list_waypoints(
        self,
        system_symbol: str,
        page: int = 1,
        limit: int = 20
    ) -> Dict:
        return self._request(
            "GET",
            f"/systems/{system_symbol}/waypoints",
            params={"page": page, "limit": limit}
        )

    def get_shipyard(self, system_symbol: str, waypoint_symbol: str) -> Dict:
        """Get shipyard details at a waypoint.

        Endpoint: GET /systems/{systemSymbol}/waypoints/{waypointSymbol}/shipyard

        Args:
            system_symbol: The system symbol (e.g., "X1-GZ7")
            waypoint_symbol: The waypoint symbol (e.g., "X1-GZ7-AB12")

        Returns:
            Dict containing shipyard information including available ships
        """
        return self._request(
            "GET",
            f"/systems/{system_symbol}/waypoints/{waypoint_symbol}/shipyard"
        )

    def purchase_ship(self, ship_type: str, waypoint_symbol: str) -> Dict:
        """Purchase a ship at a shipyard.

        Endpoint: POST /my/ships with payload {shipType, waypointSymbol}

        Args:
            ship_type: The type of ship to purchase (e.g., "SHIP_MINING_DRONE")
            waypoint_symbol: The waypoint symbol where the shipyard is located

        Returns:
            Dict containing the newly purchased ship data and transaction info
        """
        return self._request(
            "POST",
            "/my/ships",
            json={"shipType": ship_type, "waypointSymbol": waypoint_symbol}
        )

    def get_market(self, system: str, waypoint: str) -> Dict:
        """Get market data for a waypoint.

        Endpoint: GET /systems/{systemSymbol}/waypoints/{waypointSymbol}/market

        Args:
            system: System symbol (e.g., "X1-GZ7")
            waypoint: Waypoint symbol (e.g., "X1-GZ7-A1")

        Returns:
            Dict containing market data including tradeGoods list
        """
        return self._request(
            "GET",
            f"/systems/{system}/waypoints/{waypoint}/market"
        )

    def get_contracts(self, page: int = 1, limit: int = 20) -> Dict:
        """Get all contracts with pagination.

        Endpoint: GET /my/contracts

        Args:
            page: Page number (default: 1)
            limit: Results per page (default: 20)

        Returns:
            Dict containing contracts list
        """
        return self._request(
            "GET",
            "/my/contracts",
            params={"page": page, "limit": limit}
        )

    def get_contract(self, contract_id: str) -> Dict:
        """Get specific contract details.

        Endpoint: GET /my/contracts/{contractId}

        Args:
            contract_id: Contract identifier

        Returns:
            Dict containing contract details
        """
        return self._request(
            "GET",
            f"/my/contracts/{contract_id}"
        )

    def accept_contract(self, contract_id: str) -> Dict:
        """Accept a contract.

        Endpoint: POST /my/contracts/{contractId}/accept

        Args:
            contract_id: Contract identifier

        Returns:
            Dict containing updated contract and agent data
        """
        return self._request(
            "POST",
            f"/my/contracts/{contract_id}/accept",
            json={}
        )

    def deliver_contract(self, contract_id: str, ship_symbol: str, trade_symbol: str, units: int) -> Dict:
        """Deliver goods to fulfill contract.

        Endpoint: POST /my/contracts/{contractId}/deliver

        Args:
            contract_id: Contract identifier
            ship_symbol: Ship making the delivery
            trade_symbol: Good being delivered
            units: Number of units to deliver

        Returns:
            Dict containing updated contract and cargo data
        """
        return self._request(
            "POST",
            f"/my/contracts/{contract_id}/deliver",
            json={
                "shipSymbol": ship_symbol,
                "tradeSymbol": trade_symbol,
                "units": units
            }
        )

    def fulfill_contract(self, contract_id: str) -> Dict:
        """Complete and fulfill a contract.

        Endpoint: POST /my/contracts/{contractId}/fulfill

        Args:
            contract_id: Contract identifier

        Returns:
            Dict containing fulfilled contract and payment data
        """
        return self._request(
            "POST",
            f"/my/contracts/{contract_id}/fulfill",
            json={}
        )

    def negotiate_contract(self, ship_symbol: str) -> Dict:
        """Negotiate a new contract with ship.

        Endpoint: POST /my/ships/{shipSymbol}/negotiate/contract

        Args:
            ship_symbol: Ship negotiating the contract

        Returns:
            Dict containing new contract data
        """
        return self._request(
            "POST",
            f"/my/ships/{ship_symbol}/negotiate/contract",
            json={}
        )

    def purchase_cargo(self, ship_symbol: str, trade_symbol: str, units: int) -> Dict:
        """Purchase cargo from market.

        Endpoint: POST /my/ships/{shipSymbol}/purchase

        Args:
            ship_symbol: Ship making the purchase
            trade_symbol: Good to purchase (e.g., "IRON_ORE")
            units: Number of units to purchase

        Returns:
            Dict containing transaction details including updated cargo and credits
        """
        return self._request(
            "POST",
            f"/my/ships/{ship_symbol}/purchase",
            json={
                "symbol": trade_symbol,
                "units": units
            }
        )
