"""Jettison Cargo Command - Remove cargo items from ship"""
from dataclasses import dataclass
from pymediatr import Request, RequestHandler


@dataclass(frozen=True)
class JettisonCargoCommand(Request[None]):
    """Command to jettison cargo from ship"""
    ship_symbol: str
    player_id: int
    cargo_symbol: str
    units: int


class JettisonCargoHandler(RequestHandler[JettisonCargoCommand, None]):
    """Handler for jettisoning cargo

    This handler calls the SpaceTraders API to jettison cargo from a ship.
    The ship state will be synchronized automatically on next operation.
    """

    def __init__(self, api_client_factory):
        """
        Initialize handler

        Args:
            api_client_factory: Factory function that returns API client for player
        """
        self._api_client_factory = api_client_factory

    async def handle(self, request: JettisonCargoCommand) -> None:
        """
        Jettison cargo from ship via API

        Args:
            request: Command with ship symbol, cargo symbol, and units to jettison
        """
        # Get API client for player
        api_client = self._api_client_factory(request.player_id)

        # Call API to jettison cargo
        api_client.jettison_cargo(
            ship_symbol=request.ship_symbol,
            cargo_symbol=request.cargo_symbol,
            units=request.units
        )

        # Ship will be synced on next operation
        # No need to manually update - sync_from_api will fetch latest state
