"""Sell cargo command"""
from dataclasses import dataclass
from typing import Dict

from pymediatr import Request, RequestHandler


@dataclass(frozen=True)
class SellCargoCommand(Request[Dict]):
    """Command to sell cargo at a market"""
    ship_symbol: str
    trade_symbol: str
    units: int
    player_id: int


class SellCargoHandler(RequestHandler[SellCargoCommand, Dict]):
    """Handler for SellCargoCommand"""

    def __init__(self, api_client_factory):
        """
        Initialize handler

        Args:
            api_client_factory: Factory function that creates API client for player
        """
        self._api_client_factory = api_client_factory

    async def handle(self, request: SellCargoCommand) -> Dict:
        """
        Handle sell cargo command

        Args:
            request: Command with ship, trade symbol, units, and player ID

        Returns:
            Sell transaction details from API containing:
            - agent: Updated agent info (credits increased)
            - cargo: Updated ship cargo
            - transaction: Transaction details with pricePerUnit, totalPrice, etc.
        """
        # Get API client for player
        api_client = self._api_client_factory(request.player_id)

        # Call API to sell cargo
        response = api_client.sell_cargo(
            ship_symbol=request.ship_symbol,
            trade_symbol=request.trade_symbol,
            units=request.units
        )

        return response
