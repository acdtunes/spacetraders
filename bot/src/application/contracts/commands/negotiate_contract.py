"""Negotiate contract command"""
from dataclasses import dataclass
from datetime import datetime
import requests

from pymediatr import Request, RequestHandler

from domain.shared.contract import Contract, ContractTerms, Delivery, Payment
from domain.shared.value_objects import Waypoint
from domain.shared.exceptions import ContractNegotiationError, RateLimitError
from ports.outbound.repositories import IContractRepository
from ports.outbound.api_client import ISpaceTradersAPI


@dataclass(frozen=True)
class NegotiateContractCommand(Request[Contract]):
    """Command to negotiate a new contract"""
    ship_symbol: str
    player_id: int


class NegotiateContractHandler(RequestHandler[NegotiateContractCommand, Contract]):
    """Handler for NegotiateContractCommand"""

    def __init__(
        self,
        contract_repository: IContractRepository,
        api_client_factory
    ):
        self._contract_repo = contract_repository
        self._api_client_factory = api_client_factory

    async def handle(self, request: NegotiateContractCommand) -> Contract:
        """
        Handle negotiate contract command

        Args:
            request: Command with ship symbol and player ID

        Returns:
            New contract entity

        Raises:
            ContractNegotiationError: If negotiation fails
            RateLimitError: If rate limit is exceeded
        """
        # Get API client for player
        api_client = self._api_client_factory(request.player_id)

        # Call API to negotiate contract with error handling
        try:
            response = api_client.negotiate_contract(request.ship_symbol)
        except requests.exceptions.HTTPError as e:
            # Extract error details from response
            if hasattr(e, 'response') and e.response is not None:
                status_code = e.response.status_code

                # Handle rate limit errors specifically
                if status_code == 429:
                    try:
                        error_data = e.response.json()
                        message = error_data.get('error', {}).get('message', 'Rate limit exceeded')
                        raise RateLimitError(f"Rate limit exceeded (429): {message}")
                    except (ValueError, KeyError):
                        raise RateLimitError("Rate limit exceeded (429)")

                # Handle other HTTP errors
                try:
                    error_data = e.response.json()
                    error_code = error_data.get('error', {}).get('code')
                    error_message = error_data.get('error', {}).get('message', 'Unknown error')

                    # Error 4511 (agent already has active contract) - re-raise HTTPError
                    # to allow batch workflow to handle it by fetching existing contract
                    if error_code == 4511:
                        raise

                    raise ContractNegotiationError(
                        f"Contract negotiation failed (HTTP {status_code}, Error {error_code}): {error_message}"
                    )
                except (ValueError, KeyError):
                    # Response not JSON or missing expected fields
                    raise ContractNegotiationError(
                        f"Contract negotiation failed (HTTP {status_code}): {e.response.text}"
                    )
            else:
                # No response attached to exception
                raise ContractNegotiationError(f"Contract negotiation failed: {str(e)}")

        # Extract contract data from response
        contract_data = response['data']['contract']

        # Parse deliveries
        deliveries = []
        for delivery_data in contract_data['terms']['deliver']:
            delivery = Delivery(
                trade_symbol=delivery_data['tradeSymbol'],
                destination=Waypoint(
                    symbol=delivery_data['destinationSymbol'],
                    x=0.0,  # API doesn't provide coordinates in negotiate response
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

        # Save new contract
        self._contract_repo.save(contract, request.player_id)

        return contract
