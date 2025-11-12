"""Scout tour command - internal worker for executing market scouting tours

This command is NOT exposed via CLI - it's an internal implementation detail
used by ScoutMarketsCommand to execute the actual touring logic in containers.
"""
from dataclasses import dataclass
from typing import List
import logging
import asyncio
from pymediatr import Request, RequestHandler

from domain.shared.exceptions import ShipNotFoundError
from ports.repositories import IShipRepository
from ports.outbound.market_repository import IMarketRepository

logger = logging.getLogger(__name__)


@dataclass(frozen=True)
class ScoutTourResult:
    """Result of scout tour execution"""
    markets_visited: int
    goods_updated: int


@dataclass(frozen=True)
class ScoutTourCommand(Request[ScoutTourResult]):
    """
    Execute a market scouting tour with a single ship.

    The handler orchestrates:
    1. Navigate to each market waypoint in sequence
    2. Dock at each market
    3. Get market data from API
    4. Persist market data
    5. Return to starting waypoint for multi-market tours (2+)

    Tours by definition complete a circuit and return to start.
    Stationary scouts (1 market) don't need to return as they're already at the start.

    This is an INTERNAL command - not exposed via CLI.
    Used by ScoutMarketsCommand to execute tours in daemon containers.
    """
    ship_symbol: str
    player_id: int
    system: str
    markets: List[str]


class ScoutTourHandler(RequestHandler[ScoutTourCommand, ScoutTourResult]):
    """
    Handler for scout tour execution.

    Coordinates ship navigation and market data collection.
    """

    def __init__(self, ship_repository: IShipRepository, market_repository: IMarketRepository):
        """
        Initialize ScoutTourHandler.

        Args:
            ship_repository: Repository for ship persistence
            market_repository: Repository for market data persistence
        """
        self._ship_repo = ship_repository
        self._market_repo = market_repository

    async def handle(self, request: ScoutTourCommand) -> ScoutTourResult:
        """
        Execute market scouting tour.

        Process:
        1. Load ship and record starting location
        2. For each market:
           a. Navigate to market waypoint
           b. Dock at market
           c. Get market data from API
           d. Persist market data
        3. For multi-market tours (2+), navigate back to starting waypoint

        Tours by definition complete a circuit and return to start.
        Stationary scouts (1 market) don't need to return.

        Args:
            request: Scout tour command

        Returns:
            ScoutTourResult with metrics

        Raises:
            ShipNotFoundError: If ship doesn't exist
        """
        logger.info(f"Starting scout tour: {request.ship_symbol} visiting {len(request.markets)} markets")

        # 1. Load ship and record starting location
        ship = self._ship_repo.find_by_symbol(request.ship_symbol, request.player_id)
        if ship is None:
            raise ShipNotFoundError(
                f"Ship {request.ship_symbol} not found for player {request.player_id}"
            )

        starting_waypoint = ship.current_location.symbol
        logger.info(f"Ship starting from {starting_waypoint}")

        # IDEMPOTENCY: If ship is at one of the tour markets, start from there
        # This handles daemon restarts mid-tour gracefully
        # NOTE: For IN_TRANSIT ships, current_location is already set to the destination
        markets_to_visit = list(request.markets)

        if starting_waypoint in markets_to_visit:
            # Rotate list so we start from current location (or destination if IN_TRANSIT)
            start_index = markets_to_visit.index(starting_waypoint)
            markets_to_visit = markets_to_visit[start_index:] + markets_to_visit[:start_index]

            from domain.shared.ship import Ship
            if ship.nav_status == Ship.IN_TRANSIT:
                logger.info(f"Ship IN_TRANSIT to tour market {starting_waypoint}, will resume from there (idempotent)")
            else:
                logger.info(f"Ship at tour market {starting_waypoint}, resuming from here (idempotent)")

        total_goods = 0

        # 2. Visit each market in sequence
        for i, market_waypoint in enumerate(markets_to_visit, 1):
            logger.info(f"Visiting market {i}/{len(request.markets)}: {market_waypoint}")

            # Navigate to market (uses NavigateShipCommand via mediator)
            await self._navigate_to(request.ship_symbol, request.player_id, market_waypoint)

            # Dock at market (uses DockShipCommand via mediator)
            await self._dock_at(request.ship_symbol, request.player_id, market_waypoint)

            # Get and persist market data
            goods_count = await self._scout_market(request.player_id, request.system, market_waypoint)
            total_goods += goods_count

            logger.info(f"✅ Market {market_waypoint}: {goods_count} goods updated")

        # 3. Return to start for multi-market tours (2+)
        # Tours by definition complete a circuit
        # Stationary scouts (1 market) don't need to return as they're already at start
        if len(request.markets) > 1:
            # Return to first market in the rotated tour (which is the starting waypoint)
            tour_start = markets_to_visit[0]
            logger.info(f"Tour complete: returning to starting waypoint {tour_start}")
            await self._navigate_to(request.ship_symbol, request.player_id, tour_start)
            await self._dock_at(request.ship_symbol, request.player_id, tour_start)

        logger.info(f"Tour complete: {len(request.markets)} markets, {total_goods} goods, {sum([1 for _ in request.markets])}")

        # Wait between iterations based on tour type
        # Stationary scouts (1 market) need to wait for market refresh
        # Touring scouts (2+ markets) already spend time traveling
        if len(request.markets) == 1:
            logger.info("Stationary scout: waiting 60 seconds for market refresh before next iteration...")
            await asyncio.sleep(60)
        else:
            logger.info(f"Touring scout ({len(request.markets)} markets): no wait needed between iterations (travel time provides natural delay)")

        return ScoutTourResult(
            markets_visited=len(request.markets),
            goods_updated=total_goods
        )

    async def _navigate_to(self, ship_symbol: str, player_id: int, destination: str):
        """Navigate ship to destination waypoint"""
        # Check if ship is already at destination
        ship = self._ship_repo.find_by_symbol(ship_symbol, player_id)
        if ship and ship.current_location.symbol == destination:
            logger.info(f"Ship {ship_symbol} already at {destination}, skipping navigation")
            return

        from configuration.container import get_mediator
        from application.navigation.commands.navigate_ship import NavigateShipCommand

        mediator = get_mediator()
        command = NavigateShipCommand(
            ship_symbol=ship_symbol,
            player_id=player_id,
            destination_symbol=destination
        )
        await mediator.send_async(command)

    async def _dock_at(self, ship_symbol: str, player_id: int, waypoint: str):
        """Dock ship at current waypoint"""
        from configuration.container import get_mediator
        from application.navigation.commands.dock_ship import DockShipCommand

        mediator = get_mediator()
        command = DockShipCommand(
            ship_symbol=ship_symbol,
            player_id=player_id
        )
        await mediator.send_async(command)

    async def _scout_market(self, player_id: int, system: str, market_waypoint: str) -> int:
        """Get market data from API and persist it"""
        from configuration.container import get_api_client_for_player
        from domain.shared.market import TradeGood
        from datetime import datetime, timezone

        api_client = get_api_client_for_player(player_id)

        # Get market data from API
        response = api_client.get_market(system, market_waypoint)
        trade_goods_data = response['data'].get('tradeGoods', [])

        if not trade_goods_data:
            logger.warning(f"No trade goods found at {market_waypoint}")
            return 0

        # Convert to domain objects
        trade_goods = []
        for good_data in trade_goods_data:
            trade_good = TradeGood(
                symbol=good_data['symbol'],
                supply=good_data['supply'],
                activity=good_data.get('activity'),
                purchase_price=good_data['purchasePrice'],
                sell_price=good_data['sellPrice'],
                trade_volume=good_data['tradeVolume']
            )
            trade_goods.append(trade_good)

        # Persist market data
        timestamp = datetime.now(timezone.utc).isoformat()
        self._market_repo.upsert_market_data(
            waypoint=market_waypoint,
            goods=trade_goods,
            timestamp=timestamp,
            player_id=player_id
        )

        return len(trade_goods)
