"""Deliver contract cargo command"""
from dataclasses import dataclass

from pymediatr import Request, RequestHandler

from ....domain.shared.contract import Contract, Delivery, ContractTerms
from ....ports.outbound.repositories import IContractRepository
from ....ports.outbound.api_client import ISpaceTradersAPI


@dataclass(frozen=True)
class DeliverContractCommand(Request[Contract]):
    """Command to deliver cargo for a contract"""
    contract_id: str
    ship_symbol: str
    trade_symbol: str
    units: int
    player_id: int


class DeliverContractHandler(RequestHandler[DeliverContractCommand, Contract]):
    """Handler for DeliverContractCommand"""

    def __init__(
        self,
        contract_repository: IContractRepository,
        api_client_factory
    ):
        self._contract_repo = contract_repository
        self._api_client_factory = api_client_factory

    async def handle(self, request: DeliverContractCommand) -> Contract:
        """
        Handle deliver contract cargo command

        Args:
            request: Command with delivery details

        Returns:
            Updated contract entity

        Raises:
            ValueError: If contract not found
        """
        # Get API client for player
        api_client = self._api_client_factory(request.player_id)

        # Call API to deliver cargo
        response = api_client.deliver_contract(
            request.contract_id,
            request.ship_symbol,
            request.trade_symbol,
            request.units
        )

        # Load contract from database
        contract = self._contract_repo.find_by_id(
            request.contract_id,
            request.player_id
        )

        if not contract:
            raise ValueError(f"Contract {request.contract_id} not found")

        # Update delivery progress from API response
        api_deliveries = response['data']['contract']['terms']['deliver']

        # Create updated deliveries list
        updated_deliveries = []
        for delivery in contract.terms.deliveries:
            # Find matching delivery in API response
            matching_api = next(
                (d for d in api_deliveries if d['tradeSymbol'] == delivery.trade_symbol),
                None
            )

            if matching_api:
                # Update with API data
                updated_delivery = Delivery(
                    trade_symbol=delivery.trade_symbol,
                    destination=delivery.destination,
                    units_required=delivery.units_required,
                    units_fulfilled=matching_api['unitsFulfilled']
                )
            else:
                # Keep existing
                updated_delivery = delivery

            updated_deliveries.append(updated_delivery)

        # Create new contract with updated deliveries (immutable)
        terms = ContractTerms(
            deadline=contract.terms.deadline,
            payment=contract.terms.payment,
            deliveries=updated_deliveries
        )

        updated_contract = Contract(
            contract_id=contract.contract_id,
            faction_symbol=contract.faction_symbol,
            type=contract.type,
            terms=terms,
            accepted=contract.accepted,
            fulfilled=contract.fulfilled,
            deadline_to_accept=contract.deadline_to_accept
        )

        # Save updated contract
        self._contract_repo.save(updated_contract, request.player_id)

        return updated_contract
