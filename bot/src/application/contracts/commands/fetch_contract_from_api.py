"""Fetch contract from API and save to database command"""
from dataclasses import dataclass
from datetime import datetime

from pymediatr import Request, RequestHandler

from domain.shared.contract import Contract, ContractTerms, Delivery, Payment
from domain.shared.value_objects import Waypoint
from ports.outbound.repositories import IContractRepository


@dataclass(frozen=True)
class FetchContractFromAPICommand(Request[Contract]):
    """Command to fetch a contract from API by ID and save to database"""
    contract_id: str
    player_id: int


class FetchContractFromAPIHandler(RequestHandler[FetchContractFromAPICommand, Contract]):
    """Handler for FetchContractFromAPICommand"""

    def __init__(
        self,
        contract_repository: IContractRepository,
        api_client_factory
    ):
        self._contract_repo = contract_repository
        self._api_client_factory = api_client_factory

    async def handle(self, request: FetchContractFromAPICommand) -> Contract:
        """
        Handle fetch contract from API command

        Args:
            request: Command with contract ID and player ID

        Returns:
            Contract entity fetched from API and saved to database

        Raises:
            ValueError: If API call fails
        """
        # Get API client for player
        api_client = self._api_client_factory(request.player_id)

        # Call API to get contract
        response = api_client.get_contract(request.contract_id)

        # Extract contract data from response
        contract_data = response['data']

        # Parse deliveries
        deliveries = []
        for delivery_data in contract_data['terms']['deliver']:
            delivery = Delivery(
                trade_symbol=delivery_data['tradeSymbol'],
                destination=Waypoint(
                    symbol=delivery_data['destinationSymbol'],
                    x=0.0,  # API doesn't provide coordinates in get_contract response
                    y=0.0
                ),
                units_required=delivery_data['unitsRequired'],
                units_fulfilled=delivery_data.get('unitsFulfilled', 0)
            )
            deliveries.append(delivery)

        # Create payment
        payment = Payment(
            on_accepted=contract_data['terms']['payment']['onAccepted'],
            on_fulfilled=contract_data['terms']['payment']['onFulfilled']
        )

        # Create terms
        terms = ContractTerms(
            deadline=datetime.fromisoformat(contract_data['terms']['deadline']),
            payment=payment,
            deliveries=deliveries
        )

        # Create contract
        contract = Contract(
            contract_id=contract_data['id'],
            faction_symbol=contract_data['factionSymbol'],
            type=contract_data['type'],
            terms=terms,
            accepted=contract_data.get('accepted', False),
            fulfilled=contract_data.get('fulfilled', False),
            deadline_to_accept=datetime.fromisoformat(contract_data['deadlineToAccept'])
        )

        # Save contract to database
        self._contract_repo.save(contract, request.player_id)

        return contract
