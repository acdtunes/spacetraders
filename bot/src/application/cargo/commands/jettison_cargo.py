"""Jettison Cargo Command - Remove cargo items from ship"""
from dataclasses import dataclass
from pymediatr import Request, RequestHandler


@dataclass(frozen=True)
class JettisonCargoCommand(Request[dict]):
    """Command to jettison cargo from ship"""
    ship_symbol: str
    player_id: int
    cargo_symbol: str
    units: int


class JettisonCargoHandler(RequestHandler[JettisonCargoCommand, dict]):
    """Handler for jettisoning cargo

    This handler calls the SpaceTraders API to jettison cargo from a ship.
    Returns the updated cargo state from the API response.
    """

    def __init__(self, api_client_factory):
        """
        Initialize handler

        Args:
            api_client_factory: Factory function that returns API client for player
        """
        self._api_client_factory = api_client_factory

    async def handle(self, request: JettisonCargoCommand) -> dict:
        """
        Jettison cargo from ship via API

        Args:
            request: Command with ship symbol, cargo symbol, and units to jettison

        Returns:
            dict: API response containing updated cargo state
        """
        # Get API client for player
        api_client = self._api_client_factory(request.player_id)

        # Call API to jettison cargo
        response = api_client.jettison_cargo(
            ship_symbol=request.ship_symbol,
            cargo_symbol=request.cargo_symbol,
            units=request.units
        )

        # Return the API response containing updated cargo state
        return response
