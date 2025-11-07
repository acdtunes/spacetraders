"""Evaluate contract profitability query"""
from dataclasses import dataclass
from math import ceil
from typing import Optional

from pymediatr import Request, RequestHandler

from domain.shared.contract import Contract
from ...trading.queries.find_cheapest_market import FindCheapestMarketQuery


@dataclass(frozen=True)
class ProfitabilityResult:
    """Result from evaluating contract profitability"""
    is_profitable: bool
    net_profit: int
    purchase_cost: int
    trips_required: int
    reason: str
    cheapest_market_waypoint: Optional[str] = None  # Waypoint symbol of cheapest market


@dataclass(frozen=True)
class EvaluateContractProfitabilityQuery(Request[ProfitabilityResult]):
    """Query to evaluate if a contract is profitable"""
    contract: Contract
    cargo_capacity: int
    fuel_cost_per_trip: int
    player_id: int


class EvaluateContractProfitabilityHandler(RequestHandler[EvaluateContractProfitabilityQuery, ProfitabilityResult]):
    """Handler for EvaluateContractProfitabilityQuery"""

    def __init__(self, find_market_handler):
        """
        Initialize handler

        Args:
            find_market_handler: Handler for FindCheapestMarketQuery
        """
        self._find_market_handler = find_market_handler

    async def handle(self, request: EvaluateContractProfitabilityQuery) -> ProfitabilityResult:
        """
        Handle evaluate contract profitability query

        Args:
            request: Query with contract, cargo capacity, fuel cost, and player ID

        Returns:
            ProfitabilityResult with profitability analysis
        """
        contract = request.contract
        cargo_capacity = request.cargo_capacity
        fuel_cost_per_trip = request.fuel_cost_per_trip

        # Calculate total payment
        total_payment = contract.terms.payment.on_accepted + contract.terms.payment.on_fulfilled

        # Calculate total units needed and purchase cost
        total_units = 0
        total_purchase_cost = 0
        cheapest_market_waypoint = None  # Track the waypoint of cheapest market

        for delivery in contract.terms.deliveries:
            units_needed = delivery.units_required - delivery.units_fulfilled
            total_units += units_needed

            # Find cheapest market for this good
            # Extract system from delivery destination (format: X1-SYSTEM-WAYPOINT)
            destination_parts = delivery.destination.symbol.split('-')
            system = f"{destination_parts[0]}-{destination_parts[1]}"

            find_query = FindCheapestMarketQuery(
                trade_symbol=delivery.trade_symbol,
                system=system,
                player_id=request.player_id
            )

            market_result = await self._find_market_handler.handle(find_query)

            if market_result is None:
                return ProfitabilityResult(
                    is_profitable=False,
                    net_profit=0,
                    purchase_cost=0,
                    trips_required=0,
                    reason=f"No market found selling {delivery.trade_symbol}",
                    cheapest_market_waypoint=None
                )

            # Store the waypoint of the cheapest market for the first delivery
            # (assumes single delivery per contract, which is typical)
            if cheapest_market_waypoint is None:
                cheapest_market_waypoint = market_result.waypoint_symbol

            total_purchase_cost += market_result.sell_price * units_needed

        # Calculate trips required based on cargo capacity
        trips_required = ceil(total_units / cargo_capacity)

        # Calculate total fuel cost
        # Note: fuel_cost_per_trip already accounts for round trip
        total_fuel_cost = trips_required * fuel_cost_per_trip

        # Calculate net profit
        net_profit = total_payment - (total_purchase_cost + total_fuel_cost)

        # Determine if profitable
        # Allow small losses (up to 5000 cr) to avoid opportunity cost of waiting
        min_profit = -5000
        is_profitable = net_profit >= min_profit

        # Generate detailed reason message
        if net_profit > 0:
            reason = "Profitable"
        elif net_profit >= min_profit:
            reason = "Acceptable small loss (avoids opportunity cost)"
        else:
            reason = f"Loss exceeds acceptable threshold (min profit: {min_profit} cr)"

        return ProfitabilityResult(
            is_profitable=is_profitable,
            net_profit=net_profit,
            purchase_cost=total_purchase_cost,
            trips_required=trips_required,
            reason=reason,
            cheapest_market_waypoint=cheapest_market_waypoint
        )
