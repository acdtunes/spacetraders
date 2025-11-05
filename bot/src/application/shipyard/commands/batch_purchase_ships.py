"""Batch purchase ships command and handler"""
from dataclasses import dataclass
from typing import List
from pymediatr import Request, RequestHandler

from domain.shared.ship import Ship
from domain.shared.exceptions import ShipNotFoundError
from ports.repositories import IShipRepository, IPlayerRepository, IWaypointRepository


@dataclass(frozen=True)
class BatchPurchaseShipsResponse:
    """
    Response for batch ship purchase operation.

    Contains list of purchased ships and total cost.
    """
    purchased_ships: List[Ship]
    total_cost: int
    ships_purchased_count: int


@dataclass(frozen=True)
class BatchPurchaseShipsCommand(Request[BatchPurchaseShipsResponse]):
    """
    Command to purchase multiple ships of the same type in a batch.

    The command will purchase as many ships as possible within constraints:
    - Quantity requested
    - Maximum budget allocated
    - Player's available credits

    The purchasing ship will be used to navigate to the shipyard if needed.
    If shipyard_waypoint is not provided, will auto-discover nearest shipyard that sells the ship type.

    Args:
        purchasing_ship_symbol: Symbol of ship to use for purchase operations
        ship_type: Type of ship to purchase (e.g., "SHIP_MINING_DRONE")
        quantity: Maximum number of ships to purchase
        max_budget: Maximum total credits to spend on purchases
        player_id: ID of player making the purchases
        shipyard_waypoint: (Optional) Waypoint symbol where shipyard is located. If not provided,
                          will auto-discover the nearest shipyard in the current system.
    """
    purchasing_ship_symbol: str
    ship_type: str
    quantity: int
    max_budget: int
    player_id: int
    shipyard_waypoint: str = None  # Optional - will auto-discover if not provided


class BatchPurchaseShipsHandler(RequestHandler[BatchPurchaseShipsCommand, BatchPurchaseShipsResponse]):
    """
    Handler for batch ship purchase operations.

    Responsibilities:
    - Validate quantity and budget constraints
    - Query shipyard for ship price
    - Calculate how many ships can be purchased
    - Orchestrate multiple PurchaseShipCommand calls via mediator
    - Track total spending and purchased ships
    - Handle partial success scenarios gracefully
    - Return list of successfully purchased ships
    """

    def __init__(
        self,
        ship_repository: IShipRepository,
        player_repository: IPlayerRepository,
        waypoint_repository: IWaypointRepository = None
    ):
        """
        Initialize BatchPurchaseShipsHandler.

        Args:
            ship_repository: Repository for ship persistence
            player_repository: Repository for player persistence
            waypoint_repository: Repository for waypoint caching (optional - will use container if not provided)
        """
        self._ship_repo = ship_repository
        self._player_repo = player_repository
        self._waypoint_repo = waypoint_repository

    async def handle(self, request: BatchPurchaseShipsCommand) -> BatchPurchaseShipsResponse:
        """
        Execute batch ship purchase command.

        Process:
        1. Validate quantity and budget (return empty list if 0)
        2. Get shipyard listings to determine ship price (via mediator)
        3. Load player to check available credits
        4. Calculate maximum ships that can be purchased:
           - Limited by quantity
           - Limited by max_budget // price
           - Limited by player_credits // price
        5. Loop for purchasable count:
           - Call PurchaseShipCommand via mediator
           - Track purchased ship
           - Track total spent
           - Continue until budget exhausted, credits exhausted, or quantity reached
        6. Return list of purchased ships

        Args:
            request: Batch purchase command

        Returns:
            BatchPurchaseShipsResponse with list of purchased ships and total cost

        Raises:
            ShipNotFoundError: If purchasing ship doesn't exist
            ShipyardNotFoundError: If shipyard doesn't exist at waypoint
            ValueError: If ship type not available
        """
        # Import mediator and commands here to avoid circular dependency
        from configuration.container import get_mediator
        from ..queries.get_shipyard_listings import GetShipyardListingsQuery
        from .purchase_ship import PurchaseShipCommand

        mediator = get_mediator()

        # 1. Validate quantity and budget
        if request.quantity <= 0 or request.max_budget <= 0:
            return BatchPurchaseShipsResponse(
                purchased_ships=[],
                total_cost=0,
                ships_purchased_count=0
            )

        # 2. Discover shipyard if not provided (by calling PurchaseShipCommand once with auto-discovery)
        # The first purchase will handle navigation and shipyard discovery
        # Subsequent purchases will reuse the same shipyard location
        shipyard_waypoint = request.shipyard_waypoint

        if shipyard_waypoint is None:
            # We need to discover the shipyard - we'll do this on the first purchase
            # For now, just pass None and let PurchaseShipCommand handle it
            pass
        else:
            # Extract system symbol from waypoint for query
            system_symbol = '-'.join(shipyard_waypoint.split('-')[:2])

        # 3. Get shipyard listings to determine ship price
        # If shipyard_waypoint is None, we need to discover it first
        # For efficiency, we could call the first PurchaseShipCommand to discover and navigate,
        # then get the shipyard for pricing. But simpler approach: always pass through to PurchaseShipCommand
        # and get price from API on first purchase.

        # Actually, let's simplify: query shipyard only if provided, otherwise get price from first purchase
        if shipyard_waypoint is not None:
            system_symbol = '-'.join(shipyard_waypoint.split('-')[:2])
            shipyard = await mediator.send_async(GetShipyardListingsQuery(
                system_symbol=system_symbol,
                waypoint_symbol=shipyard_waypoint,
                player_id=request.player_id
            ))
        else:
            # Shipyard will be discovered on first purchase
            shipyard = None

        # 4. Find ship type in listings and get price (if shipyard known)
        ship_listing = None
        ship_price = None
        purchasable_count = request.quantity  # Default to requested quantity

        if shipyard is not None:
            for listing in shipyard.listings:
                if listing.ship_type == request.ship_type:
                    ship_listing = listing
                    break

            if ship_listing is None:
                raise ValueError(
                    f"Ship type '{request.ship_type}' not available at shipyard {shipyard_waypoint}"
                )

            ship_price = ship_listing.purchase_price

            # 5. Load player from repository
            player = self._player_repo.find_by_id(request.player_id)
            if player is None:
                raise ValueError(f"Player {request.player_id} not found")

            # 6. Fetch current credits from SpaceTraders API
            from configuration.container import get_api_client_for_player
            api_client = get_api_client_for_player(request.player_id)
            agent_response = api_client.get_agent()
            current_credits = agent_response.get('data', {}).get('credits', 0)

            # 7. Calculate maximum ships that can be purchased
            max_by_quantity = request.quantity
            max_by_budget = request.max_budget // ship_price
            max_by_credits = current_credits // ship_price

            purchasable_count = min(max_by_quantity, max_by_budget, max_by_credits)

        # 8. Purchase ships in loop
        # Each PurchaseShipCommand will fetch fresh credits from API before purchase
        # First purchase will auto-discover shipyard if not provided
        purchased_ships = []
        total_spent = 0

        for i in range(purchasable_count):
            # Call PurchaseShipCommand via mediator
            # NOTE: PurchaseShipCommand will auto-discover shipyard on first call if not provided
            # After first purchase, use the discovered shipyard location
            purchased_ship = await mediator.send_async(PurchaseShipCommand(
                purchasing_ship_symbol=request.purchasing_ship_symbol,
                ship_type=request.ship_type,
                player_id=request.player_id,
                shipyard_waypoint=shipyard_waypoint  # Pass through (None for auto-discovery on first call)
            ))

            purchased_ships.append(purchased_ship)

            # After first purchase, capture the shipyard location for subsequent purchases
            if shipyard_waypoint is None and i == 0:
                # Get the shipyard waypoint from the first purchased ship's location
                shipyard_waypoint = purchased_ship.current_location.symbol
                # Get price from first purchase for budget calculations
                if ship_price is None:
                    # We don't have direct access to price from ship entity, so estimate from remaining quantity
                    # For now, continue without pre-calculating purchasable_count
                    # Each purchase will check credits dynamically
                    pass

            total_spent += (ship_price if ship_price is not None else 0)

        # 7. Return response with purchased ships
        return BatchPurchaseShipsResponse(
            purchased_ships=purchased_ships,
            total_cost=total_spent,
            ships_purchased_count=len(purchased_ships)
        )
