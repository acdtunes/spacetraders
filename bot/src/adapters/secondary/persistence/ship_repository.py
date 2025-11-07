import logging
from typing import Optional, List, Callable

from ports.outbound.repositories import IShipRepository
from ports.outbound.graph_provider import ISystemGraphProvider
from ports.outbound.api_client import ISpaceTradersAPI
from domain.shared.ship import Ship
from domain.shared.value_objects import Waypoint, Fuel, Cargo, CargoItem
from domain.shared.exceptions import DomainException

logger = logging.getLogger(__name__)


class ShipNotFoundError(DomainException):
    """Raised when ship is not found in repository"""
    pass


class ShipRepository(IShipRepository):
    """
    API-only ship repository implementation.

    All ship data is fetched directly from the SpaceTraders API.
    No caching or persistence - guarantees fresh, consistent data.
    """

    def __init__(
        self,
        api_client_factory: Callable[[int], ISpaceTradersAPI],
        graph_provider_factory: Callable[[int], ISystemGraphProvider]
    ):
        """
        Initialize API-only ship repository.

        Args:
            api_client_factory: Factory function to create API client for a given player_id
            graph_provider_factory: Factory function to create graph provider for a given player_id
        """
        self._api_client_factory = api_client_factory
        self._graph_provider_factory = graph_provider_factory
        logger.info("ShipRepository initialized (API-only mode)")


    def find_by_symbol(self, ship_symbol: str, player_id: int) -> Optional[Ship]:
        """
        Find ship by symbol and player ID from SpaceTraders API.

        Args:
            ship_symbol: Unique ship identifier
            player_id: Owning player's ID

        Returns:
            Ship entity with live data from API, or None if not found
        """
        logger.debug(f"Fetching ship {ship_symbol} from API for player {player_id}")

        try:
            # Get API client for this player
            api_client = self._api_client_factory(player_id)

            # Fetch ship from API
            ship_response = api_client.get_ship(ship_symbol)

            if not ship_response:
                logger.debug(f"Ship not found: {ship_symbol}")
                return None

            ship_data = ship_response.get('data')
            if not ship_data:
                logger.debug(f"Ship not found: {ship_symbol}")
                return None

            # Convert API response to Ship entity
            ship = self._convert_api_ship_to_entity(ship_data, player_id)
            logger.debug(f"Ship fetched from API: {ship_symbol}")
            return ship

        except Exception as e:
            logger.error(f"Failed to fetch ship {ship_symbol} from API: {e}")
            return None

    def find_all_by_player(self, player_id: int) -> List[Ship]:
        """
        Find all ships belonging to a player from SpaceTraders API.

        Args:
            player_id: Player's ID

        Returns:
            List of ships with live data from API (empty if player has no ships)
        """
        logger.debug(f"Fetching all ships from API for player {player_id}")

        try:
            # Get API client for this player
            api_client = self._api_client_factory(player_id)

            # Fetch all ships from API
            ships_response = api_client.get_ships()

            if not ships_response:
                logger.debug(f"No ships found for player {player_id}")
                return []

            ships_data = ships_response.get('data', [])

            # Convert all ships to entities
            ships = []
            for ship_data in ships_data:
                try:
                    ship = self._convert_api_ship_to_entity(ship_data, player_id)
                    ships.append(ship)
                except Exception as e:
                    logger.error(f"Failed to convert ship {ship_data.get('symbol')}: {e}")
                    # Continue processing other ships

            logger.info(f"Fetched {len(ships)} ships from API for player {player_id}")
            return ships

        except Exception as e:
            logger.error(f"Failed to fetch ships from API for player {player_id}: {e}")
            return []

    def _convert_api_ship_to_entity(self, ship_data: dict, player_id: int) -> Ship:
        """
        Convert API ship response to Ship entity.

        Args:
            ship_data: Ship data from SpaceTraders API
            player_id: Player ID owning the ship

        Returns:
            Ship entity

        Raises:
            KeyError: If required fields are missing from ship_data
        """
        # Extract basic info
        ship_symbol = ship_data['symbol']

        # Extract navigation info
        nav_data = ship_data.get('nav', {})
        system_symbol = nav_data.get('systemSymbol', '')
        waypoint_symbol = nav_data.get('waypointSymbol', '')
        nav_status = nav_data.get('status', 'DOCKED')

        # Get graph provider to reconstruct waypoint with full details
        graph_provider = self._graph_provider_factory(player_id)

        # Reconstruct waypoint from graph
        waypoint = self._reconstruct_waypoint(
            waypoint_symbol,
            system_symbol,
            graph_provider
        )

        # Extract fuel info
        fuel_data = ship_data.get('fuel', {})
        fuel = Fuel(
            current=fuel_data.get('current', 0),
            capacity=fuel_data.get('capacity', 0)
        )

        # Extract cargo info with detailed inventory
        cargo_data = ship_data.get('cargo', {})
        cargo_capacity = cargo_data.get('capacity', 0)
        cargo_units = cargo_data.get('units', 0)

        # Extract cargo inventory from API response
        inventory_data = cargo_data.get('inventory', [])
        cargo_items = tuple(
            CargoItem(
                symbol=item['symbol'],
                name=item.get('name', item['symbol']),
                description=item.get('description', ''),
                units=item['units']
            )
            for item in inventory_data
        )

        # Create Cargo object with inventory
        cargo = Cargo(
            capacity=cargo_capacity,
            units=cargo_units,
            inventory=cargo_items
        )

        # Extract engine speed
        engine_data = ship_data.get('engine', {})
        engine_speed = engine_data.get('speed', 10)

        # Create Ship entity with cargo inventory
        ship = Ship(
            ship_symbol=ship_symbol,
            player_id=player_id,
            current_location=waypoint,
            fuel=fuel,
            fuel_capacity=fuel.capacity,
            cargo_capacity=cargo_capacity,
            cargo_units=cargo_units,
            engine_speed=engine_speed,
            nav_status=nav_status,
            cargo=cargo  # Pass cargo object with actual inventory
        )

        return ship

    def _reconstruct_waypoint(
        self,
        waypoint_symbol: str,
        system_symbol: str,
        graph_provider: ISystemGraphProvider
    ) -> Waypoint:
        """
        Reconstruct waypoint object from graph data.

        Args:
            waypoint_symbol: Waypoint symbol to reconstruct
            system_symbol: System the waypoint belongs to
            graph_provider: Graph provider to fetch system graph

        Returns:
            Reconstructed Waypoint object
        """
        try:
            # Get graph from provider
            graph_result = graph_provider.get_graph(system_symbol)
            waypoints = graph_result.graph.get("waypoints", {})

            if waypoint_symbol not in waypoints:
                logger.warning(f"Waypoint {waypoint_symbol} not found in graph, using minimal data")
                return Waypoint(
                    symbol=waypoint_symbol,
                    x=0.0,
                    y=0.0,
                    system_symbol=system_symbol
                )

            wp_data = waypoints[waypoint_symbol]

            # Reconstruct waypoint value object
            # CRITICAL: In production graphs, the waypoint symbol is the DICTIONARY KEY,
            # not a field in the data. Use waypoint_symbol (the key) as the symbol.
            waypoint = Waypoint(
                symbol=waypoint_symbol,  # Symbol is the dict key, not a field!
                x=wp_data["x"],
                y=wp_data["y"],
                system_symbol=wp_data.get("systemSymbol"),
                waypoint_type=wp_data.get("type"),
                traits=tuple(wp_data.get("traits", [])),
                has_fuel=wp_data.get("has_fuel", False),
                orbitals=tuple(wp_data.get("orbitals", []))
            )

            return waypoint

        except Exception as e:
            logger.error(f"Failed to reconstruct waypoint {waypoint_symbol}: {e}")
            # Create minimal waypoint as fallback
            return Waypoint(
                symbol=waypoint_symbol,
                x=0.0,
                y=0.0,
                system_symbol=system_symbol
            )
