"""Batch contract workflow command"""
from dataclasses import dataclass
import logging
from math import ceil
import requests

from pymediatr import Request, RequestHandler, Mediator

from ..commands.negotiate_contract import NegotiateContractCommand
from ..commands.fetch_contract_from_api import FetchContractFromAPICommand
from ..queries.evaluate_profitability import EvaluateContractProfitabilityQuery
from ..queries.get_active_contracts import GetActiveContractsQuery
from ..commands.accept_contract import AcceptContractCommand
from ..commands.fulfill_contract import FulfillContractCommand
from ..commands.deliver_contract import DeliverContractCommand
from ...trading.commands.purchase_cargo import PurchaseCargoCommand
from ...navigation.commands.navigate_ship import NavigateShipCommand
from ...navigation.commands.dock_ship import DockShipCommand
from ...cargo.commands.jettison_cargo import JettisonCargoCommand

logger = logging.getLogger(__name__)


@dataclass(frozen=True)
class BatchResult:
    """Result from batch contract workflow"""
    negotiated: int
    accepted: int
    fulfilled: int
    failed: int
    total_profit: int
    total_trips: int
    errors: list[str] = None  # List of error messages from failed operations

    def __post_init__(self):
        """Initialize errors list if None"""
        if self.errors is None:
            object.__setattr__(self, 'errors', [])


@dataclass(frozen=True)
class BatchContractWorkflowCommand(Request[BatchResult]):
    """Command to execute batch contract workflow"""
    ship_symbol: str
    iterations: int
    player_id: int


class BatchContractWorkflowHandler(RequestHandler[BatchContractWorkflowCommand, BatchResult]):
    """Handler for BatchContractWorkflowCommand

    Orchestrates the full contract workflow:
    1. Negotiate contract
    2. Evaluate profitability
    3. Accept if profitable
    4. Purchase required goods (with transaction splitting)
    5. Deliver goods (handle multi-trip if needed)
    6. Fulfill contract
    """

    def __init__(self, mediator: Mediator, ship_repository, market_repository):
        """
        Initialize handler

        Args:
            mediator: Mediator for sending commands/queries
            ship_repository: Repository for fetching ship data
            market_repository: Repository for fetching market data (transaction limits)
        """
        self._mediator = mediator
        self._ship_repository = ship_repository
        self._market_repository = market_repository

    def _get_transaction_limit(self, market_waypoint: str, trade_symbol: str, player_id: int) -> int:
        """
        Get transaction limit for a trade good at a market.

        Args:
            market_waypoint: Waypoint symbol of the market
            trade_symbol: Trade good symbol
            player_id: Player ID

        Returns:
            Transaction limit for the trade good, or a very high default (999999) if not found
        """
        try:
            market = self._market_repository.get_market_data(
                waypoint=market_waypoint,
                player_id=player_id
            )

            if market:
                for trade_good in market.trade_goods:
                    if trade_good.symbol == trade_symbol:
                        # trade_volume indicates the max units per transaction
                        if trade_good.trade_volume > 0:
                            logger.info(
                                f"Market {market_waypoint} has transaction limit of {trade_good.trade_volume} "
                                f"units for {trade_symbol}"
                            )
                            return trade_good.trade_volume

            # No transaction limit data found - default to very high limit
            logger.warning(
                f"No transaction limit data found for {trade_symbol} at {market_waypoint}, "
                "defaulting to 999999 (unlimited)"
            )
            return 999999

        except Exception as e:
            logger.warning(
                f"Failed to fetch market data for transaction limit: {e}, "
                "defaulting to 999999 (unlimited)"
            )
            return 999999

    async def _jettison_wrong_cargo(
        self,
        ship_symbol: str,
        required_symbol: str,
        player_id: int
    ):
        """Jettison all cargo items that aren't the required symbol

        Args:
            ship_symbol: Ship to jettison cargo from
            required_symbol: Trade symbol that should be kept
            player_id: Player ID

        Returns:
            None - ship state will be synced after jettison
        """
        # Get current ship state
        ship = self._ship_repository.find_by_symbol(
            ship_symbol=ship_symbol,
            player_id=player_id
        )

        # Jettison all items that aren't the required symbol
        for item in ship.cargo.inventory:
            if item.symbol != required_symbol:
                logger.info(f"Jettisoning {item.units} units of {item.symbol}")
                jettison_cmd = JettisonCargoCommand(
                    ship_symbol=ship_symbol,
                    player_id=player_id,
                    cargo_symbol=item.symbol,
                    units=item.units
                )
                await self._mediator.send_async(jettison_cmd)

        # No need to sync - ship repository fetches fresh data from API automatically

    async def _negotiate_or_fetch_contract(self, ship_symbol: str, player_id: int, iteration: int):
        """
        Negotiate a new contract or fetch existing contract from API if error 4511 occurs.

        Args:
            ship_symbol: Ship symbol to negotiate with
            player_id: Player ID
            iteration: Current iteration number (for logging)

        Returns:
            tuple: (contract, was_negotiated) - Contract entity and boolean indicating if it was newly negotiated

        Raises:
            Exception: If negotiation fails for reasons other than error 4511, or if 4511 occurs without contractId
        """
        try:
            negotiate_cmd = NegotiateContractCommand(
                ship_symbol=ship_symbol,
                player_id=player_id
            )
            contract = await self._mediator.send_async(negotiate_cmd)
            return (contract, True)

        except requests.exceptions.HTTPError as e:
            # Check if error is 4511 (agent already has active contract)
            if not hasattr(e, 'response') or e.response is None:
                raise

            try:
                error_data = e.response.json()
                error_code = error_data.get('error', {}).get('code')

                if error_code != 4511:
                    raise

                # Extract contract ID from error response
                contract_id = error_data.get('error', {}).get('data', {}).get('contractId')

                if not contract_id:
                    logger.error(f"Iteration {iteration}: Error 4511 but no contractId in response")
                    raise

                logger.info(f"Iteration {iteration}: API has existing contract {contract_id}, fetching from API")

                # Fetch existing contract from API and save to database
                fetch_cmd = FetchContractFromAPICommand(
                    contract_id=contract_id,
                    player_id=player_id
                )
                contract = await self._mediator.send_async(fetch_cmd)
                logger.info(f"Iteration {iteration}: Fetched and saved existing contract {contract_id}")
                return (contract, False)

            except (ValueError, KeyError) as parse_error:
                logger.error(f"Iteration {iteration}: Failed to parse error response: {parse_error}")
                raise e

    async def handle(self, request: BatchContractWorkflowCommand) -> BatchResult:
        """
        Handle batch contract workflow command

        Orchestrates N iterations of the contract workflow:
        - Negotiate new contract
        - Evaluate profitability (for logging only)
        - ALWAYS accept contract (avoids opportunity cost of waiting)
        - Purchase required goods from cheapest markets
        - Deliver goods (handles multi-trip delivery based on cargo capacity)
        - Fulfill contract

        Note: Contracts are accepted even if unprofitable to avoid contract expiration.
        Small losses (up to 5000 cr) are acceptable to maintain steady workflow.

        Args:
            request: Command with ship, iterations, and player ID

        Returns:
            BatchResult with statistics (negotiated, accepted, fulfilled, failed, profit, trips)
        """
        negotiated = 0
        accepted = 0
        fulfilled = 0
        failed = 0
        total_profit = 0
        total_trips = 0
        errors = []  # Collect error messages

        # Get ship cargo capacity from repository
        ship = self._ship_repository.find_by_symbol(
            ship_symbol=request.ship_symbol,
            player_id=request.player_id
        )
        cargo_capacity = ship.cargo_capacity
        logger.info(f"Ship {request.ship_symbol} cargo capacity: {cargo_capacity}")

        for iteration in range(request.iterations):
            try:
                # Step 1: Check for existing active contracts first (idempotent behavior)
                active_contracts_query = GetActiveContractsQuery(player_id=request.player_id)
                active_contracts = await self._mediator.send_async(active_contracts_query)

                if active_contracts and len(active_contracts) > 0:
                    # Resume from existing active contract
                    contract = active_contracts[0]
                    logger.info(f"Iteration {iteration + 1}: Resuming existing contract {contract.contract_id}")
                else:
                    # Step 2: Negotiate new contract or fetch existing from API
                    contract, was_negotiated = await self._negotiate_or_fetch_contract(
                        ship_symbol=request.ship_symbol,
                        player_id=request.player_id,
                        iteration=iteration + 1
                    )

                    if was_negotiated:
                        negotiated += 1

                    if not contract:
                        logger.warning(f"Iteration {iteration + 1}: Failed to negotiate contract")
                        failed += 1
                        continue

                # Step 2: Evaluate profitability (for logging only - always accept)
                fuel_cost_per_trip = 200  # TODO: Calculate actual fuel cost

                evaluate_query = EvaluateContractProfitabilityQuery(
                    contract=contract,
                    cargo_capacity=cargo_capacity,
                    fuel_cost_per_trip=fuel_cost_per_trip,
                    player_id=request.player_id
                )
                profitability = await self._mediator.send_async(evaluate_query)

                # Log profitability but ALWAYS accept contracts (avoids opportunity cost)
                if profitability.is_profitable:
                    logger.info(f"Iteration {iteration + 1}: Contract profitable ({profitability.reason})")
                else:
                    logger.warning(f"Iteration {iteration + 1}: Contract unprofitable ({profitability.reason}), but accepting anyway")

                # Step 3: Accept contract (skip if already accepted - idempotent)
                if not contract.accepted:
                    accept_cmd = AcceptContractCommand(
                        contract_id=contract.contract_id,
                        player_id=request.player_id
                    )
                    await self._mediator.send_async(accept_cmd)
                    accepted += 1
                else:
                    logger.info(f"Iteration {iteration + 1}: Contract {contract.contract_id} already accepted, skipping acceptance")

                # Step 5: Process each delivery
                for delivery in contract.terms.deliveries:
                    units_remaining = delivery.units_required - delivery.units_fulfilled

                    # Verify we have a cheapest market waypoint
                    cheapest_market = profitability.cheapest_market_waypoint
                    if not cheapest_market:
                        error_msg = f"No market found selling {delivery.trade_symbol}"
                        logger.error(f"Iteration {iteration + 1}: {error_msg}")
                        errors.append(f"Iteration {iteration + 1}: {error_msg}")
                        failed += 1
                        break  # Skip this contract iteration

                    # Step 5a: Check current cargo state (idempotency)
                    # Ship repository is API-only now, so it fetches fresh data automatically
                    logger.info(f"Iteration {iteration + 1}: Loading ship {request.ship_symbol} from API to get cargo state")

                    # Load ship with current cargo (fetches from API)
                    ship = self._ship_repository.find_by_symbol(
                        ship_symbol=request.ship_symbol,
                        player_id=request.player_id
                    )

                    # Defensive check: Fail fast if cargo has UNKNOWN symbols
                    # This indicates incomplete API response or mapper bug
                    for item in ship.cargo.inventory:
                        if item.symbol == "UNKNOWN":
                            error_msg = (
                                f"Ship {ship.ship_symbol} has UNKNOWN cargo even after API sync. "
                                "This indicates incomplete API response or mapper bug. "
                                f"Cargo inventory: {ship.cargo.inventory}"
                            )
                            logger.error(error_msg)
                            raise ValueError(error_msg)

                    # Check current cargo state (now with accurate symbols)
                    current_units = ship.cargo.get_item_units(delivery.trade_symbol)
                    has_wrong_cargo = ship.cargo.has_items_other_than(delivery.trade_symbol)

                    # Determine what actions are needed
                    # Priority 1: Check if we have enough required cargo
                    if current_units >= units_remaining:
                        # We have enough! Check if we need to jettison wrong cargo
                        if has_wrong_cargo:
                            # Jettison wrong cargo but DON'T purchase more
                            logger.info(
                                f"Iteration {iteration + 1}: Ship has enough required cargo ({current_units}/{units_remaining} {delivery.trade_symbol}), "
                                f"jettisoning wrong cargo before delivery"
                            )
                            await self._jettison_wrong_cargo(
                                ship_symbol=request.ship_symbol,
                                required_symbol=delivery.trade_symbol,
                                player_id=request.player_id
                            )
                            units_to_purchase = 0
                        else:
                            # Perfect - have exactly what we need
                            logger.info(
                                f"Iteration {iteration + 1}: Ship {request.ship_symbol} already has required cargo "
                                f"({current_units}/{units_remaining} {delivery.trade_symbol}), skipping purchase"
                            )
                            units_to_purchase = 0

                    elif has_wrong_cargo:
                        # Don't have enough, AND have wrong cargo
                        # Jettison wrong cargo first, then calculate what to buy
                        logger.info(f"Iteration {iteration + 1}: Ship has wrong cargo, jettisoning before purchase")
                        await self._jettison_wrong_cargo(
                            ship_symbol=request.ship_symbol,
                            required_symbol=delivery.trade_symbol,
                            player_id=request.player_id
                        )

                        # Reload ship state after jettison
                        ship = self._ship_repository.find_by_symbol(
                            ship_symbol=request.ship_symbol,
                            player_id=request.player_id
                        )
                        current_units = ship.cargo.get_item_units(delivery.trade_symbol)

                        # Recalculate units needed after jettison
                        units_to_purchase = units_remaining - current_units
                        logger.info(f"Iteration {iteration + 1}: Need to purchase {units_to_purchase} units of {delivery.trade_symbol}")

                    else:
                        # Don't have enough, but cargo is clean (only required item or empty)
                        # Check if cargo is full - if so, deliver first, then purchase remainder
                        if ship.cargo.units >= ship.cargo_capacity and current_units > 0:
                            # Ship is full with required cargo - deliver what we have first
                            logger.info(
                                f"Iteration {iteration + 1}: Ship is FULL ({ship.cargo.units}/{ship.cargo_capacity}) "
                                f"with {current_units} units of {delivery.trade_symbol}. "
                                f"Delivering current cargo first, will need to purchase {units_remaining - current_units} more later."
                            )
                            units_to_purchase = 0  # Don't purchase yet, deliver current cargo first
                        else:
                            # Ship has room - purchase what we need
                            units_to_purchase = units_remaining - current_units
                            logger.info(
                                f"Iteration {iteration + 1}: Ship has {current_units}/{units_remaining} units, "
                                f"purchasing {units_to_purchase} more"
                            )

                    # Step 6: Purchase and deliver in trips (only if needed)
                    if units_to_purchase > 0:
                        trips = ceil(units_to_purchase / cargo_capacity)
                        total_trips += trips

                        for trip in range(trips):
                            units_this_trip = min(cargo_capacity, units_to_purchase)

                            # Step 6a: Navigate to seller market
                            logger.info(f"Iteration {iteration + 1}, Trip {trip + 1}: Navigating to seller market {cheapest_market}")
                            navigate_to_seller_cmd = NavigateShipCommand(
                                ship_symbol=request.ship_symbol,
                                destination_symbol=cheapest_market,
                                player_id=request.player_id
                            )
                            await self._mediator.send_async(navigate_to_seller_cmd)

                            # Step 6b: Dock at seller market
                            logger.info(f"Iteration {iteration + 1}, Trip {trip + 1}: Docking at seller market {cheapest_market}")
                            dock_at_seller_cmd = DockShipCommand(
                                ship_symbol=request.ship_symbol,
                                player_id=request.player_id
                            )
                            await self._mediator.send_async(dock_at_seller_cmd)

                            # Step 6c: Purchase cargo (with transaction splitting)
                            # Get market transaction limit
                            transaction_limit = self._get_transaction_limit(
                                market_waypoint=cheapest_market,
                                trade_symbol=delivery.trade_symbol,
                                player_id=request.player_id
                            )

                            # Split purchase into multiple transactions if needed
                            units_remaining_this_trip = units_this_trip
                            transaction_number = 0

                            while units_remaining_this_trip > 0:
                                units_this_transaction = min(units_remaining_this_trip, transaction_limit)
                                transaction_number += 1

                                logger.info(
                                    f"Iteration {iteration + 1}, Trip {trip + 1}, Transaction {transaction_number}: "
                                    f"Purchasing {units_this_transaction} units of {delivery.trade_symbol} "
                                    f"(limit: {transaction_limit}, remaining: {units_remaining_this_trip})"
                                )

                                purchase_cmd = PurchaseCargoCommand(
                                    ship_symbol=request.ship_symbol,
                                    trade_symbol=delivery.trade_symbol,
                                    units=units_this_transaction,
                                    player_id=request.player_id
                                )
                                await self._mediator.send_async(purchase_cmd)

                                units_remaining_this_trip -= units_this_transaction

                            # Step 6d: Navigate to delivery destination
                            logger.info(f"Iteration {iteration + 1}, Trip {trip + 1}: Navigating to delivery destination {delivery.destination.symbol}")
                            navigate_to_delivery_cmd = NavigateShipCommand(
                                ship_symbol=request.ship_symbol,
                                destination_symbol=delivery.destination.symbol,
                                player_id=request.player_id
                            )
                            await self._mediator.send_async(navigate_to_delivery_cmd)

                            # Step 6e: Dock at delivery destination
                            logger.info(f"Iteration {iteration + 1}, Trip {trip + 1}: Docking at delivery destination {delivery.destination.symbol}")
                            dock_at_delivery_cmd = DockShipCommand(
                                ship_symbol=request.ship_symbol,
                                player_id=request.player_id
                            )
                            await self._mediator.send_async(dock_at_delivery_cmd)

                            # Step 6f: Deliver cargo
                            logger.info(f"Iteration {iteration + 1}, Trip {trip + 1}: Delivering {units_this_trip} units of {delivery.trade_symbol}")
                            deliver_cmd = DeliverContractCommand(
                                contract_id=contract.contract_id,
                                ship_symbol=request.ship_symbol,
                                trade_symbol=delivery.trade_symbol,
                                units=units_this_trip,
                                player_id=request.player_id
                            )
                            await self._mediator.send_async(deliver_cmd)

                            units_to_purchase -= units_this_trip
                    else:
                        # Skip purchase but still need to deliver if we have cargo
                        # Deliver only what we currently have (not units_remaining, which might be more)
                        units_to_deliver = min(current_units, units_remaining)

                        if units_to_deliver > 0:
                            logger.info(
                                f"Iteration {iteration + 1}: Skipping purchase, proceeding directly to delivery "
                                f"of {units_to_deliver} units (ship has {current_units}, contract needs {units_remaining})"
                            )

                            # Navigate to delivery destination
                            logger.info(f"Iteration {iteration + 1}: Navigating to delivery destination {delivery.destination.symbol}")
                            navigate_to_delivery_cmd = NavigateShipCommand(
                                ship_symbol=request.ship_symbol,
                                destination_symbol=delivery.destination.symbol,
                                player_id=request.player_id
                            )
                            await self._mediator.send_async(navigate_to_delivery_cmd)

                            # Dock at delivery destination
                            logger.info(f"Iteration {iteration + 1}: Docking at delivery destination {delivery.destination.symbol}")
                            dock_at_delivery_cmd = DockShipCommand(
                                ship_symbol=request.ship_symbol,
                                player_id=request.player_id
                            )
                            await self._mediator.send_async(dock_at_delivery_cmd)

                            # Deliver cargo (only what we have)
                            logger.info(f"Iteration {iteration + 1}: Delivering {units_to_deliver} units of {delivery.trade_symbol}")
                            deliver_cmd = DeliverContractCommand(
                                contract_id=contract.contract_id,
                                ship_symbol=request.ship_symbol,
                                trade_symbol=delivery.trade_symbol,
                                units=units_to_deliver,
                                player_id=request.player_id
                            )
                            await self._mediator.send_async(deliver_cmd)
                        else:
                            logger.info(f"Iteration {iteration + 1}: No cargo to deliver, skipping delivery step")

                # Step 7: Fulfill contract
                fulfill_cmd = FulfillContractCommand(
                    contract_id=contract.contract_id,
                    player_id=request.player_id
                )
                await self._mediator.send_async(fulfill_cmd)
                fulfilled += 1
                total_profit += profitability.net_profit

                logger.info(f"Iteration {iteration + 1}: Successfully fulfilled contract {contract.contract_id}")

            except Exception as e:
                error_msg = f"Iteration {iteration + 1}: {str(e)}"
                logger.error(f"Contract workflow failed: {error_msg}")
                errors.append(error_msg)
                failed += 1
                # Continue to next iteration

        return BatchResult(
            negotiated=negotiated,
            accepted=accepted,
            fulfilled=fulfilled,
            failed=failed,
            total_profit=total_profit,
            total_trips=total_trips,
            errors=errors
        )
