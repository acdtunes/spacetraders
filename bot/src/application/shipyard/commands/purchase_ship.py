"""Purchase ship command and handler"""
from dataclasses import dataclass
from pymediatr import Request, RequestHandler

from domain.shared.ship import Ship
from domain.shared.player import Player
from domain.shared.value_objects import Waypoint
from domain.shared.exceptions import ShipNotFoundError, InsufficientCreditsError
from ports.repositories import IShipRepository, IPlayerRepository, IWaypointRepository
from ports.outbound.api_client import ISpaceTradersAPI


@dataclass(frozen=True)
class PurchaseShipCommand(Request[Ship]):
    """
    Command to purchase a ship from a shipyard.

    The purchasing ship will:
    1. Auto-discover nearest shipyard that sells the desired ship type (if not specified)
    2. Navigate to the shipyard waypoint if not already there
    3. Dock if in orbit
    4. Purchase the specified ship type
    5. Update player credits
    6. Save the new ship to repository

    Args:
        purchasing_ship_symbol: Symbol of ship to use for purchase (must be player's ship)
        ship_type: Type of ship to purchase (e.g., "SHIP_MINING_DRONE")
        player_id: ID of player making the purchase
        shipyard_waypoint: (Optional) Waypoint symbol where shipyard is located. If not provided,
                          will auto-discover the nearest shipyard in the current system that sells
                          the desired ship type.
    """
    purchasing_ship_symbol: str
    ship_type: str
    player_id: int
    shipyard_waypoint: str = None  # Optional - will auto-discover if not provided


class PurchaseShipHandler(RequestHandler[PurchaseShipCommand, Ship]):
    """
    Handler for ship purchase operations.

    Responsibilities:
    - Load purchasing ship from repository
    - Navigate ship to shipyard if needed (via NavigateShipCommand)
    - Dock ship if in orbit (via DockShipCommand)
    - Query shipyard for available ships (via GetShipyardListingsQuery)
    - Validate ship type is available and get price
    - Load player and validate sufficient credits
    - Call API purchase_ship()
    - Update player credits
    - Create new Ship entity from API response
    - Save new ship to repository
    - Return new Ship entity
    """

    def __init__(
        self,
        ship_repository: IShipRepository,
        player_repository: IPlayerRepository,
        waypoint_repository: IWaypointRepository = None
    ):
        """
        Initialize PurchaseShipHandler.

        Args:
            ship_repository: Repository for ship persistence
            player_repository: Repository for player persistence
            waypoint_repository: Repository for waypoint caching (optional - will use container if not provided)
        """
        self._ship_repo = ship_repository
        self._player_repo = player_repository
        self._waypoint_repo = waypoint_repository

    async def handle(self, request: PurchaseShipCommand) -> Ship:
        """
        Execute ship purchase command.

        Process:
        1. Load purchasing ship from repository
        2. If shipyard_waypoint not provided, auto-discover nearest shipyard that sells ship type
        3. If ship not at shipyard waypoint, navigate there (orchestrate via mediator)
        4. If ship in orbit, dock it (orchestrate via mediator)
        5. Query shipyard for available ships (orchestrate via mediator)
        6. Validate ship type is available and get price
        7. Load player from repository
        8. Validate player has sufficient credits (domain validation)
        9. Call API purchase_ship()
        10. Deduct credits from player (domain method)
        11. Update player in repository
        12. Convert API response to Ship entity
        13. Save new ship to repository
        14. Return new Ship entity

        Args:
            request: Purchase command with ship symbol, ship type, and player ID
                    (shipyard_waypoint optional - will auto-discover if not provided)

        Returns:
            Ship entity for the newly purchased ship

        Raises:
            ShipNotFoundError: If purchasing ship doesn't exist
            ShipyardNotFoundError: If shipyard doesn't exist at waypoint
            NoShipyardFoundError: If no shipyard in system sells the desired ship type
            ValueError: If ship type not available or cannot navigate to shipyard
            InsufficientCreditsError: If player doesn't have enough credits
        """
        # Import mediator here to avoid circular dependency
        from configuration.container import get_mediator, get_api_client_for_player
        from ..queries.get_shipyard_listings import GetShipyardListingsQuery
        from ...navigation.commands.navigate_ship import NavigateShipCommand
        from ...navigation.commands.dock_ship import DockShipCommand
        from ...navigation.commands._ship_converter import convert_api_ship_to_entity
        from domain.shared.exceptions import NoShipyardFoundError
        import math

        mediator = get_mediator()
        api_client = get_api_client_for_player(request.player_id)

        # 1. Load purchasing ship from repository
        purchasing_ship = self._ship_repo.find_by_symbol(
            request.purchasing_ship_symbol,
            request.player_id
        )
        if purchasing_ship is None:
            raise ShipNotFoundError(
                f"Ship '{request.purchasing_ship_symbol}' not found for player {request.player_id}"
            )

        # 2. Auto-discover shipyard if not provided
        shipyard_waypoint = request.shipyard_waypoint
        if shipyard_waypoint is None:
            # Get ship's current system
            system_symbol = purchasing_ship.current_location.system_symbol

            # Get waypoint repository (lazy-load from container if not injected)
            waypoint_repo = self._waypoint_repo
            if waypoint_repo is None:
                from configuration.container import get_waypoint_repository
                waypoint_repo = get_waypoint_repository()

            # Check if waypoints are cached for this system
            cached_waypoints = waypoint_repo.find_by_system(system_symbol)

            # If not cached, sync waypoints first
            if not cached_waypoints:
                from .sync_waypoints import SyncSystemWaypointsCommand
                await mediator.send_async(SyncSystemWaypointsCommand(
                    system_symbol=system_symbol,
                    player_id=request.player_id
                ))
                # Re-query cache after sync
                cached_waypoints = waypoint_repo.find_by_system(system_symbol)

            # Filter waypoints with SHIPYARD trait from cache
            shipyard_waypoints = waypoint_repo.find_by_trait(system_symbol, 'SHIPYARD')

            # For each shipyard, check if it sells the desired ship type
            valid_shipyards = []
            for waypoint in shipyard_waypoints:
                try:
                    shipyard_data = api_client.get_shipyard(system_symbol, waypoint.symbol)
                    shipyard_info = shipyard_data.get('data', {})

                    # Check if this shipyard sells the desired ship type
                    ship_types = shipyard_info.get('shipTypes', [])
                    for ship_type_info in ship_types:
                        if ship_type_info.get('type') == request.ship_type:
                            # Calculate distance from current location using domain method
                            distance = purchasing_ship.current_location.distance_to(waypoint)

                            valid_shipyards.append({
                                'waypoint': waypoint.symbol,
                                'distance': distance
                            })
                            break
                except Exception:
                    # Skip waypoints where shipyard data cannot be retrieved
                    continue

            # If no valid shipyards found, raise error
            if not valid_shipyards:
                raise NoShipyardFoundError(
                    f"No shipyards in system {system_symbol} sell {request.ship_type}"
                )

            # Select the nearest shipyard
            nearest_shipyard = min(valid_shipyards, key=lambda s: s['distance'])
            shipyard_waypoint = nearest_shipyard['waypoint']

        # 3. Navigate to shipyard if not already there
        if purchasing_ship.current_location.symbol != shipyard_waypoint:
            # This will orchestrate the complete navigation including refueling if needed
            await mediator.send_async(NavigateShipCommand(
                ship_symbol=request.purchasing_ship_symbol,
                destination_symbol=shipyard_waypoint,
                player_id=request.player_id
            ))
            # Reload ship after navigation
            purchasing_ship = self._ship_repo.find_by_symbol(
                request.purchasing_ship_symbol,
                request.player_id
            )

        # 4. Dock ship if in orbit
        if purchasing_ship.nav_status == "IN_ORBIT":
            await mediator.send_async(DockShipCommand(
                ship_symbol=request.purchasing_ship_symbol,
                player_id=request.player_id
            ))
            # Reload ship after docking
            purchasing_ship = self._ship_repo.find_by_symbol(
                request.purchasing_ship_symbol,
                request.player_id
            )

        # 5. Get shipyard listings and validate ship type is available
        # Extract system symbol from waypoint (e.g., "X1-GZ7" from "X1-GZ7-AB12")
        system_symbol = '-'.join(shipyard_waypoint.split('-')[:2])

        shipyard = await mediator.send_async(GetShipyardListingsQuery(
            system_symbol=system_symbol,
            waypoint_symbol=shipyard_waypoint,
            player_id=request.player_id
        ))

        # 5. Find ship type in listings and get price
        ship_listing = None
        for listing in shipyard.listings:
            if listing.ship_type == request.ship_type:
                ship_listing = listing
                break

        if ship_listing is None:
            raise ValueError(
                f"Ship type '{request.ship_type}' not available at shipyard {shipyard_waypoint}"
            )

        purchase_price = ship_listing.purchase_price

        # 6. Load player from repository
        player = self._player_repo.find_by_id(request.player_id)
        if player is None:
            raise ValueError(f"Player {request.player_id} not found")

        # 7. Fetch current credits from SpaceTraders API
        agent_response = api_client.get_agent()
        current_credits = agent_response.get('data', {}).get('credits', 0)

        # 8. Create transient Player instance with current credits for validation
        player_with_credits = Player(
            player_id=player.player_id,
            agent_symbol=player.agent_symbol,
            token=player.token,
            created_at=player.created_at,
            last_active=player.last_active,
            metadata=player.metadata,
            credits=current_credits
        )

        # 9. Validate sufficient credits (domain validation - will raise InsufficientCreditsError if not enough)
        player_with_credits.spend_credits(purchase_price)

        # 10. Call API to purchase ship
        purchase_result = api_client.purchase_ship(
            ship_type=request.ship_type,
            waypoint_symbol=shipyard_waypoint
        )

        # NOTE: We do NOT update player credits in repository - they're fetched from API

        # 11. Extract new ship data from API response
        ship_data = purchase_result.get('data', {}).get('ship')
        if not ship_data:
            raise ValueError("API returned no ship data for purchase operation")

        # 12. Convert API response to Ship entity
        # The new ship starts at the shipyard waypoint
        shipyard_waypoint_obj = Waypoint(
            symbol=shipyard_waypoint,
            x=0,  # These will be overwritten if waypoint data is in API response
            y=0,
            system_symbol=system_symbol,
            waypoint_type='SHIPYARD',
            has_fuel=False
        )

        new_ship = convert_api_ship_to_entity(
            ship_data,
            request.player_id,
            shipyard_waypoint_obj
        )

        # 13. Save new ship to repository (for test compatibility and caching)
        self._ship_repo.create(new_ship)

        # 14. Return the new ship
        return new_ship
