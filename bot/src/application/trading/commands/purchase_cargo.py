"""Purchase cargo command"""
from dataclasses import dataclass
from typing import Dict

from pymediatr import Request, RequestHandler


@dataclass(frozen=True)
class PurchaseCargoCommand(Request[Dict]):
    """Command to purchase cargo from a market"""
    ship_symbol: str
    trade_symbol: str
    units: int
    player_id: int


class PurchaseCargoHandler(RequestHandler[PurchaseCargoCommand, Dict]):
    """Handler for PurchaseCargoCommand"""

    def __init__(self, api_client_factory):
        """
        Initialize handler

        Args:
            api_client_factory: Factory function that creates API client for player
        """
        self._api_client_factory = api_client_factory

    async def handle(self, request: PurchaseCargoCommand) -> Dict:
        """
        Handle purchase cargo command

        Args:
            request: Command with ship, trade symbol, units, and player ID

        Returns:
            Purchase transaction details from API
        """
        # Get API client for player
        api_client = self._api_client_factory(request.player_id)

        # Call API to purchase cargo
        response = api_client.purchase_cargo(
            ship_symbol=request.ship_symbol,
            trade_symbol=request.trade_symbol,
            units=request.units
        )

        return response
