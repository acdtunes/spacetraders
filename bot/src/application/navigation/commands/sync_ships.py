"""
Sync Ships Command - Fetch ships from SpaceTraders API and store in database
"""
from dataclasses import dataclass
from typing import List
import logging

from pymediatr import Request, RequestHandler
from ports.repositories import IShipRepository
from ports.outbound.api_client import ISpaceTradersAPI
from domain.shared.ship import Ship
from domain.shared.value_objects import Waypoint, Fuel

logger = logging.getLogger(__name__)


@dataclass(frozen=True)
class SyncShipsCommand(Request[List[Ship]]):
    """
    Command to sync ships from SpaceTraders API to local database.

    Fetches all ships for the player from the API and creates/updates
    them in the local database.
    """
    player_id: int


class SyncShipsHandler(RequestHandler[SyncShipsCommand, List[Ship]]):
    """Handler for syncing ships from API to database"""

    def __init__(self, ship_repository: IShipRepository):
        self._ship_repo = ship_repository

    async def handle(self, request: SyncShipsCommand) -> List[Ship]:
        """
        Sync all ships from API to database.

        Steps:
        1. Get API client from player token in database
        2. Fetch agent info to get agent symbol
        3. Fetch all ships from API
        4. Convert API response to Ship entities
        5. Create or update ships in database

        Returns:
            List of synced Ship entities
        """
        logger.info(f"Syncing ships for player {request.player_id}")

        # Get API client for this player (import here to avoid circular dependency)
        from configuration.container import get_api_client_for_player
        api_client = get_api_client_for_player(request.player_id)

        # Get agent info
        agent_response = api_client.get_agent()
        agent_data = agent_response.get('data', {})
        agent_symbol = agent_data.get('symbol')

        logger.info(f"Agent: {agent_symbol}")

        # Fetch ships from API
        # Note: API returns list of ships under 'data' key
        ships_response = api_client.get_ships()
        ships_data = ships_response.get('data', [])

        logger.info(f"Found {len(ships_data)} ships from API")

        synced_ships = []

        for ship_data in ships_data:
            try:
                ship = self._convert_api_ship_to_entity(
                    ship_data,
                    request.player_id
                )

                # Check if ship exists
                existing_ship = self._ship_repo.find_by_symbol(
                    ship.ship_symbol,
                    request.player_id
                )

                if existing_ship:
                    # Update existing ship
                    self._ship_repo.update(ship)
                    logger.info(f"Updated ship {ship.ship_symbol}")
                else:
                    # Create new ship
                    ship = self._ship_repo.create(ship)
                    logger.info(f"Created ship {ship.ship_symbol}")

                synced_ships.append(ship)

            except Exception as e:
                logger.error(f"Failed to sync ship {ship_data.get('symbol')}: {e}")
                continue

        logger.info(f"Successfully synced {len(synced_ships)} ships")
        return synced_ships

    def _convert_api_ship_to_entity(
        self,
        ship_data: dict,
        player_id: int
    ) -> Ship:
        """
        Convert API ship response to Ship entity.

        API ship structure:
        {
            "symbol": "SHIP-1",
            "nav": {
                "systemSymbol": "X1-DF55",
                "waypointSymbol": "X1-DF55-20250Z",
                "status": "DOCKED"
            },
            "fuel": {
                "current": 100,
                "capacity": 100
            },
            "cargo": {
                "capacity": 40,
                "units": 0
            },
            "engine": {
                "speed": 30
            }
        }
        """
        # Extract basic info
        ship_symbol = ship_data['symbol']

        # Extract navigation info
        nav_data = ship_data.get('nav', {})
        system_symbol = nav_data.get('systemSymbol', '')
        waypoint_symbol = nav_data.get('waypointSymbol', '')
        nav_status = nav_data.get('status', 'DOCKED')

        # Extract location (we need x, y from API or use placeholders)
        route_data = nav_data.get('route', {})
        destination = route_data.get('destination', {})
        x = float(destination.get('x', 0))
        y = float(destination.get('y', 0))
        waypoint_type = destination.get('type', 'UNKNOWN')

        # Create Waypoint value object
        current_location = Waypoint(
            symbol=waypoint_symbol,
            x=x,
            y=y,
            system_symbol=system_symbol,
            waypoint_type=waypoint_type,
            traits=tuple(),  # Simplified - can fetch from waypoint API later
            has_fuel=False,  # Simplified - can determine from traits
            orbitals=tuple()
        )

        # Extract fuel info
        fuel_data = ship_data.get('fuel', {})
        fuel = Fuel(
            current=fuel_data.get('current', 0),
            capacity=fuel_data.get('capacity', 0)
        )

        # Extract cargo info
        cargo_data = ship_data.get('cargo', {})
        cargo_capacity = cargo_data.get('capacity', 0)
        cargo_units = cargo_data.get('units', 0)

        # Extract engine speed
        engine_data = ship_data.get('engine', {})
        engine_speed = engine_data.get('speed', 10)

        # Create Ship entity
        ship = Ship(
            ship_symbol=ship_symbol,
            player_id=player_id,
            current_location=current_location,
            fuel=fuel,
            fuel_capacity=fuel.capacity,
            cargo_capacity=cargo_capacity,
            cargo_units=cargo_units,
            engine_speed=engine_speed,
            nav_status=nav_status
        )

        return ship
