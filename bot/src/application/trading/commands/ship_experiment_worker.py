"""Ship Experiment Worker - Executes market liquidity experiments for a single ship."""

import logging
from dataclasses import dataclass
from typing import Dict, List
from datetime import datetime, timezone

from pymediatr import Request, RequestHandler, Mediator

from adapters.secondary.persistence.work_queue_repository import WorkQueueRepository, MarketPair
from adapters.secondary.persistence.experiment_repository import ExperimentRepository
from ports.outbound.market_repository import IMarketRepository
from application.navigation.commands.navigate_ship import NavigateShipCommand
from application.navigation.commands.dock_ship import DockShipCommand
from application.trading.commands.purchase_cargo import PurchaseCargoCommand
from application.trading.commands.sell_cargo import SellCargoCommand


logger = logging.getLogger(__name__)


@dataclass(frozen=True)
class ShipExperimentWorkerCommand(Request[Dict]):
    """Command for a ship to process experiment work queue."""
    run_id: str
    ship_symbol: str
    player_id: int
    iterations_per_batch: int
    batch_size_fractions: List[float]


class ShipExperimentWorkerHandler(RequestHandler[ShipExperimentWorkerCommand, Dict]):
    """Handler for ship experiment worker - claims and executes market experiments."""

    def __init__(
        self,
        work_queue_repo: WorkQueueRepository,
        experiment_repo: ExperimentRepository,
        market_repo: IMarketRepository,
        api_client_factory,
        mediator: Mediator
    ):
        """
        Initialize worker handler.

        Args:
            work_queue_repo: Work queue repository for claiming pairs
            experiment_repo: Experiment repository for recording results
            market_repo: Market repository for fetching and storing market data
            api_client_factory: Factory function that creates API client for player
            mediator: Mediator for dispatching commands
        """
        self._work_queue = work_queue_repo
        self._experiment_repo = experiment_repo
        self._market_repo = market_repo
        self._api_client_factory = api_client_factory
        self._mediator = mediator

    async def handle(self, request: ShipExperimentWorkerCommand) -> Dict:
        """
        Worker loop: claim pairs until queue empty.

        Args:
            request: Worker command with run_id, ship, and experiment parameters

        Returns:
            Dict with pairs_completed and pairs_failed counts
        """
        pairs_completed = 0
        pairs_failed = 0

        logger.info(f"Ship {request.ship_symbol}: Starting worker loop for run {request.run_id}")

        while True:
            # Atomically claim next pair
            pair = self._work_queue.claim_next_pair(
                request.run_id,
                request.ship_symbol
            )

            if pair is None:
                # Queue empty - we're done
                logger.info(f"Ship {request.ship_symbol}: Queue empty, stopping")
                break

            logger.info(f"Ship {request.ship_symbol}: Starting pair {pair.pair_id}")

            try:
                # Execute full experiment on this pair
                await self._execute_pair_experiment(request, pair)

                # Mark complete
                self._work_queue.mark_complete(pair.queue_id)
                pairs_completed += 1

                logger.info(
                    f"Ship {request.ship_symbol}: Completed {pair.pair_id} "
                    f"({pairs_completed} total)"
                )

            except Exception as e:
                # Mark failed, continue to next pair
                self._work_queue.mark_failed(pair.queue_id, str(e))
                pairs_failed += 1
                logger.error(f"Ship {request.ship_symbol}: Failed {pair.pair_id}: {e}")

        return {
            'ship_symbol': request.ship_symbol,
            'pairs_completed': pairs_completed,
            'pairs_failed': pairs_failed
        }

    async def _execute_pair_experiment(
        self,
        request: ShipExperimentWorkerCommand,
        pair: MarketPair
    ):
        """
        Execute buy/sell experiment for one market pair.

        Handles cargo capacity and leftover cargo by:
        1. Buy until cargo full or all purchases complete
        2. Sell ALL cargo of this good (current + leftovers) in one transaction
        3. Record the consolidated sell with market impact data
        4. If more buys pending, return to buy market and repeat

        This ensures symmetric experiment data - all sells are measured.

        Args:
            request: Worker command with parameters
            pair: Market pair to test
        """

        # Track which buys are pending
        pending_buys = [
            (batch_fraction, iteration)
            for batch_fraction in request.batch_size_fractions
            for iteration in range(1, request.iterations_per_batch + 1)
        ]

        # Extract system from waypoint (e.g., "X1-GZ7-A1" -> "X1-GZ7")
        system = self._extract_system(pair.buy_market)

        # Loop until all buys complete
        while pending_buys:
            # Track what we buy in this cycle (for selling later)
            units_bought_this_cycle = {}

            # === BUY PHASE ===
            await self._navigate_and_dock(request, pair.buy_market)

            # Get ship's current cargo state
            api_client = self._api_client_factory(request.player_id)
            ship_data = api_client.get_ship(request.ship_symbol)
            cargo_capacity = ship_data['data']['cargo']['capacity']
            cargo_used = ship_data['data']['cargo']['units']

            # Get initial market data for trade volume
            market_data = self._market_repo.get_market_data(pair.buy_market, request.player_id)
            if not market_data:
                raise ValueError(f"Market {pair.buy_market} not found")

            trade_good = next(
                (g for g in market_data.trade_goods if g.symbol == pair.good_symbol),
                None
            )

            if not trade_good:
                raise ValueError(f"Good {pair.good_symbol} not available at {pair.buy_market}")

            # Execute purchases for pending buys (until cargo full)
            buys_completed_this_cycle = []
            cargo_full = False

            for (batch_fraction, iteration) in pending_buys:
                if cargo_full:
                    break  # Stop buying, proceed to sell

                units_to_buy = int(trade_good.trade_volume * batch_fraction)

                # Check cargo capacity BEFORE attempting purchase
                available_space = cargo_capacity - cargo_used
                if units_to_buy > available_space:
                    logger.info(
                        f"Cargo full: need {units_to_buy} units but only "
                        f"{available_space} space available. Proceeding to sell."
                    )
                    cargo_full = True
                    break

                # Get market state BEFORE and capture poll timestamp
                market_poll_time = datetime.now(timezone.utc)
                market_before = self._market_repo.get_market_data(pair.buy_market, request.player_id)
                good_before = next(
                    g for g in market_before.trade_goods
                    if g.symbol == pair.good_symbol
                )

                # Get last transaction timestamp for this market+good
                last_tx_time = self._experiment_repo.get_last_transaction_timestamp(
                    market=pair.buy_market,
                    good_symbol=pair.good_symbol,
                    operation='BUY',
                    player_id=request.player_id
                )

                # Calculate minutes since last trade
                minutes_since_last = None
                if last_tx_time:
                    time_delta = market_poll_time - last_tx_time
                    minutes_since_last = time_delta.total_seconds() / 60.0

                # Execute purchase
                try:
                    result = await self._mediator.send_async(PurchaseCargoCommand(
                        ship_symbol=request.ship_symbol,
                        trade_symbol=pair.good_symbol,
                        units=units_to_buy,
                        player_id=request.player_id
                    ))

                    # Track actual units bought for this cycle
                    units_actually_bought = result['data']['transaction']['units']
                    units_bought_this_cycle[(batch_fraction, iteration)] = units_actually_bought

                    # Update cargo tracking for next iteration
                    cargo_used = result['data']['cargo']['units']

                    # Mark this buy as complete
                    buys_completed_this_cycle.append((batch_fraction, iteration))

                except Exception as e:
                    error_msg = str(e).lower()
                    # Cargo full - stop buying, proceed to sell what we have
                    if 'cargo' in error_msg or 'capacity' in error_msg:
                        logger.info(f"Cargo full at {pair.buy_market}, proceeding to sell")
                        cargo_full = True
                        break
                    else:
                        # Other error - log and mark as 0 units, continue
                        logger.error(f"Buy failed at {pair.buy_market}: {e}")
                        buys_completed_this_cycle.append((batch_fraction, iteration))
                        units_bought_this_cycle[(batch_fraction, iteration)] = 0
                        continue

                # Get market state AFTER (fetch fresh from API and update DB)
                market_after = self._fetch_and_update_market(pair.buy_market, request.player_id)
                good_after = next(
                    g for g in market_after.trade_goods
                    if g.symbol == pair.good_symbol
                )

                # Calculate impact
                price_impact = (
                    (good_after.sell_price - good_before.sell_price)
                    / good_before.sell_price * 100
                )
                supply_change = f"{good_before.supply}→{good_after.supply}"

                # Record transaction
                self._experiment_repo.record_transaction({
                    'run_id': request.run_id,
                    'player_id': request.player_id,
                    'ship_symbol': request.ship_symbol,
                    'pair_id': pair.pair_id,
                    'good_symbol': pair.good_symbol,
                    'buy_market': pair.buy_market,
                    'sell_market': pair.sell_market,
                    'operation': 'BUY',
                    'iteration': iteration,
                    'batch_size_fraction': batch_fraction,
                    'units': units_actually_bought,
                    'price_per_unit': result['data']['transaction']['pricePerUnit'],
                    'total_credits': result['data']['transaction']['totalPrice'],
                    'supply_before': good_before.supply,
                    'activity_before': good_before.activity,
                    'trade_volume_before': good_before.trade_volume,
                    'price_before': good_before.sell_price,
                    'supply_after': good_after.supply,
                    'price_after': good_after.sell_price,
                    'supply_change': supply_change,
                    'price_impact_percent': price_impact,
                    'ship_cargo_capacity': result['data']['cargo']['capacity'],
                    'ship_cargo_used': result['data']['cargo']['units'],
                    'minutes_since_last_trade': minutes_since_last,
                    'market_poll_timestamp': market_poll_time,
                    'timestamp': datetime.now(timezone.utc)
                })

            # Remove completed buys from pending list
            for buy in buys_completed_this_cycle:
                pending_buys.remove(buy)

            # === NAVIGATE TO SELL MARKET ===
            await self._navigate_and_dock(request, pair.sell_market)

            # === SELL PHASE ===
            # Get ship's actual cargo to sell everything (including leftovers)
            api_client = self._api_client_factory(request.player_id)
            ship_data = api_client.get_ship(request.ship_symbol)
            cargo_items = {item['symbol']: item['units'] for item in ship_data['data']['cargo']['inventory']}
            total_units_of_good = cargo_items.get(pair.good_symbol, 0)

            if total_units_of_good == 0:
                logger.info(f"No {pair.good_symbol} cargo to sell, skipping sell phase")
                continue

            # Sell ALL cargo of this good in one transaction (current + leftovers)
            logger.info(
                f"Selling {total_units_of_good} units of {pair.good_symbol} "
                f"(bought this cycle: {sum(units_bought_this_cycle.values())}, "
                f"leftovers: {total_units_of_good - sum(units_bought_this_cycle.values())})"
            )

            # Get market state BEFORE and capture poll timestamp
            market_poll_time = datetime.now(timezone.utc)
            market_before = self._market_repo.get_market_data(pair.sell_market, request.player_id)
            good_before = next(
                g for g in market_before.trade_goods
                if g.symbol == pair.good_symbol
            )

            # Get last transaction timestamp for this market+good
            last_tx_time = self._experiment_repo.get_last_transaction_timestamp(
                market=pair.sell_market,
                good_symbol=pair.good_symbol,
                operation='SELL',
                player_id=request.player_id
            )

            # Calculate minutes since last trade
            minutes_since_last = None
            if last_tx_time:
                time_delta = market_poll_time - last_tx_time
                minutes_since_last = time_delta.total_seconds() / 60.0

            # Execute sell of ALL cargo
            result = await self._mediator.send_async(SellCargoCommand(
                ship_symbol=request.ship_symbol,
                trade_symbol=pair.good_symbol,
                units=total_units_of_good,
                player_id=request.player_id
            ))

            # Get market state AFTER (fetch fresh from API and update DB)
            market_after = self._fetch_and_update_market(pair.sell_market, request.player_id)
            good_after = next(
                g for g in market_after.trade_goods
                if g.symbol == pair.good_symbol
            )

            # Calculate impact (for sell, use purchase_price)
            price_impact = (
                (good_after.purchase_price - good_before.purchase_price)
                / good_before.purchase_price * 100
            )
            supply_change = f"{good_before.supply}→{good_after.supply}"

            # Record consolidated transaction (we sell all cargo in one API call)
            # Use the batch_fraction/iteration from the first buy for labeling
            first_buy = list(units_bought_this_cycle.keys())[0] if units_bought_this_cycle else (None, 0)
            batch_fraction_label, iteration_label = first_buy

            self._experiment_repo.record_transaction({
                'run_id': request.run_id,
                'player_id': request.player_id,
                'ship_symbol': request.ship_symbol,
                'pair_id': pair.pair_id,
                'good_symbol': pair.good_symbol,
                'buy_market': pair.buy_market,
                'sell_market': pair.sell_market,
                'operation': 'SELL',
                'iteration': iteration_label,
                'batch_size_fraction': batch_fraction_label,
                'units': total_units_of_good,
                'price_per_unit': result['data']['transaction']['pricePerUnit'],
                'total_credits': result['data']['transaction']['totalPrice'],
                'supply_before': good_before.supply,
                'activity_before': good_before.activity,
                'trade_volume_before': good_before.trade_volume,
                'price_before': good_before.purchase_price,
                'supply_after': good_after.supply,
                'price_after': good_after.purchase_price,
                'supply_change': supply_change,
                'price_impact_percent': price_impact,
                'ship_cargo_capacity': result['data']['cargo']['capacity'],
                'ship_cargo_used': result['data']['cargo']['units'],
                'minutes_since_last_trade': minutes_since_last,
                'market_poll_timestamp': market_poll_time,
                'timestamp': datetime.now(timezone.utc)
            })

            # Cargo now empty, loop continues if more buys pending

    async def _navigate_and_dock(self, request: ShipExperimentWorkerCommand, waypoint: str):
        """
        Navigate to waypoint and dock.

        Args:
            request: Worker command with ship and player info
            waypoint: Destination waypoint
        """
        # Navigate (handles fuel, refueling, route planning)
        await self._mediator.send_async(NavigateShipCommand(
            ship_symbol=request.ship_symbol,
            destination_symbol=waypoint,
            player_id=request.player_id
        ))

        # Dock
        await self._mediator.send_async(DockShipCommand(
            ship_symbol=request.ship_symbol,
            player_id=request.player_id
        ))

    def _extract_system(self, waypoint: str) -> str:
        """
        Extract system symbol from waypoint.

        Args:
            waypoint: Waypoint symbol (e.g., "X1-GZ7-A1")

        Returns:
            System symbol (e.g., "X1-GZ7")
        """
        parts = waypoint.split('-')
        return f"{parts[0]}-{parts[1]}"

    def _fetch_and_update_market(self, waypoint: str, player_id: int):
        """
        Fetch fresh market data from API and update database.

        Args:
            waypoint: Waypoint to fetch market data for
            player_id: Player ID for API access and database storage

        Returns:
            Market domain object with fresh data
        """
        from domain.shared.market import Market, TradeGood

        # Get API client
        api_client = self._api_client_factory(player_id)

        # Fetch fresh data from API
        system = self._extract_system(waypoint)
        api_response = api_client.get_market(system, waypoint)

        # Extract market data from response
        market_data = api_response['data']
        timestamp = market_data.get('timestamp', datetime.now(timezone.utc).isoformat())

        # Convert API trade goods to domain objects
        trade_goods = []
        for good in market_data.get('tradeGoods', []):
            trade_goods.append(TradeGood(
                symbol=good['symbol'],
                supply=good.get('supply'),
                activity=good.get('activity'),
                purchase_price=good['purchasePrice'],
                sell_price=good['sellPrice'],
                trade_volume=good['tradeVolume']
            ))

        # Update database cache with fresh data
        self._market_repo.upsert_market_data(
            waypoint=waypoint,
            goods=trade_goods,
            timestamp=timestamp,
            player_id=player_id
        )

        # Return Market domain object
        return Market(
            waypoint_symbol=waypoint,
            trade_goods=tuple(trade_goods),
            last_updated=timestamp
        )
