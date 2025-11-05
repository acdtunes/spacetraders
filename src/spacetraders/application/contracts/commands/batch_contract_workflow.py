"""Batch contract workflow command"""
from dataclasses import dataclass
import logging
from math import ceil

from pymediatr import Request, RequestHandler, Mediator

from ..commands.negotiate_contract import NegotiateContractCommand
from ..queries.evaluate_profitability import EvaluateContractProfitabilityQuery
from ..queries.get_active_contracts import GetActiveContractsQuery
from ..commands.accept_contract import AcceptContractCommand
from ..commands.fulfill_contract import FulfillContractCommand
from ..commands.deliver_contract import DeliverContractCommand
from ...trading.commands.purchase_cargo import PurchaseCargoCommand

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
    4. Purchase required goods
    5. Deliver goods (handle multi-trip if needed)
    6. Fulfill contract
    """

    def __init__(self, mediator: Mediator, ship_repository):
        """
        Initialize handler

        Args:
            mediator: Mediator for sending commands/queries
            ship_repository: Repository for fetching ship data
        """
        self._mediator = mediator
        self._ship_repository = ship_repository

    async def handle(self, request: BatchContractWorkflowCommand) -> BatchResult:
        """
        Handle batch contract workflow command

        Orchestrates N iterations of the contract workflow:
        - Negotiate new contract
        - Evaluate profitability (skip if unprofitable)
        - Accept contract
        - Purchase required goods from cheapest markets
        - Deliver goods (handles multi-trip delivery based on cargo capacity)
        - Fulfill contract

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
                    # Step 2: Negotiate new contract
                    negotiate_cmd = NegotiateContractCommand(
                        ship_symbol=request.ship_symbol,
                        player_id=request.player_id
                    )
                    contract = await self._mediator.send_async(negotiate_cmd)
                    negotiated += 1

                    if not contract:
                        logger.warning(f"Iteration {iteration + 1}: Failed to negotiate contract")
                        failed += 1
                        continue

                # Step 2: Evaluate profitability
                fuel_cost_per_trip = 200  # TODO: Calculate actual fuel cost

                evaluate_query = EvaluateContractProfitabilityQuery(
                    contract=contract,
                    cargo_capacity=cargo_capacity,
                    fuel_cost_per_trip=fuel_cost_per_trip,
                    player_id=request.player_id
                )
                profitability = await self._mediator.send_async(evaluate_query)

                # Step 3: Skip if not profitable
                if not profitability.is_profitable:
                    logger.info(f"Iteration {iteration + 1}: Contract not profitable ({profitability.reason}), skipping")
                    continue

                # Step 4: Accept contract (skip if already accepted - idempotent)
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

                    # Step 6: Purchase and deliver in trips
                    trips = ceil(units_remaining / cargo_capacity)
                    total_trips += trips

                    for trip in range(trips):
                        units_this_trip = min(cargo_capacity, units_remaining)

                        # Purchase cargo
                        purchase_cmd = PurchaseCargoCommand(
                            ship_symbol=request.ship_symbol,
                            trade_symbol=delivery.trade_symbol,
                            units=units_this_trip,
                            player_id=request.player_id
                        )
                        await self._mediator.send_async(purchase_cmd)

                        # Deliver cargo
                        deliver_cmd = DeliverContractCommand(
                            contract_id=contract.contract_id,
                            ship_symbol=request.ship_symbol,
                            trade_symbol=delivery.trade_symbol,
                            units=units_this_trip,
                            player_id=request.player_id
                        )
                        await self._mediator.send_async(deliver_cmd)

                        units_remaining -= units_this_trip

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
                logger.error(f"Iteration {iteration + 1}: Contract workflow failed: {e}")
                failed += 1
                # Continue to next iteration

        return BatchResult(
            negotiated=negotiated,
            accepted=accepted,
            fulfilled=fulfilled,
            failed=failed,
            total_profit=total_profit,
            total_trips=total_trips
        )
