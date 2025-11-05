"""Query to get shipyard listings."""
from dataclasses import dataclass
from typing import List
import requests

from domain.shared.shipyard import Shipyard, ShipListing
from domain.shared.exceptions import ShipyardNotFoundError
from ports.outbound.api_client import ISpaceTradersAPI
from pymediatr import Request, RequestHandler


@dataclass(frozen=True)
class GetShipyardListingsQuery(Request[Shipyard]):
    """Query to get available ships at a shipyard."""
    system_symbol: str
    waypoint_symbol: str
    player_id: int


class GetShipyardListingsHandler(RequestHandler[GetShipyardListingsQuery, Shipyard]):
    """Handler for GetShipyardListingsQuery."""

    def __init__(self, api_client_factory=None):
        """
        Initialize handler.

        Args:
            api_client_factory: Optional factory function that takes player_id
                              and returns ISpaceTradersAPI. If None, uses container.
        """
        self._api_client_factory = api_client_factory

    async def handle(self, request: GetShipyardListingsQuery) -> Shipyard:
        """
        Get shipyard listings from the API.

        Args:
            request: Query with system symbol, waypoint symbol, and player ID

        Returns:
            Shipyard domain object with available ship listings

        Raises:
            ShipyardNotFoundError: If shipyard doesn't exist at the waypoint
            HTTPError: For other API errors
        """
        # Get API client for the player
        if self._api_client_factory:
            api_client = self._api_client_factory(request.player_id)
        else:
            # Import here to avoid circular dependency
            from configuration.container import get_api_client_for_player
            api_client = get_api_client_for_player(request.player_id)

        try:
            response = api_client.get_shipyard(
                system_symbol=request.system_symbol,
                waypoint_symbol=request.waypoint_symbol
            )

            # Extract shipyard data from API response
            data = response['data']

            # Convert API ship listings to domain ShipListing objects
            listings: List[ShipListing] = []
            for ship_data in data.get('ships', []):
                listing = ShipListing(
                    ship_type=ship_data['type'],
                    name=ship_data['name'],
                    description=ship_data.get('description', ''),
                    purchase_price=ship_data['purchasePrice'],
                    frame=ship_data.get('frame'),
                    reactor=ship_data.get('reactor'),
                    engine=ship_data.get('engine'),
                    modules=ship_data.get('modules'),
                    mounts=ship_data.get('mounts')
                )
                listings.append(listing)

            # Extract ship types
            ship_types = [st['type'] for st in data.get('shipTypes', [])]

            # Create Shipyard domain object
            shipyard = Shipyard(
                symbol=data['symbol'],
                ship_types=ship_types,
                listings=listings,
                transactions=data.get('transactions', []),
                modification_fee=data.get('modificationsFee', 0)
            )

            return shipyard

        except requests.HTTPError as e:
            if e.response.status_code == 404:
                raise ShipyardNotFoundError(
                    f"No shipyard found at waypoint {request.waypoint_symbol}"
                )
            raise
