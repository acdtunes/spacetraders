"""Fulfill contract command"""
from dataclasses import dataclass

from pymediatr import Request, RequestHandler

from domain.shared.contract import Contract
from ports.outbound.repositories import IContractRepository
from ports.outbound.api_client import ISpaceTradersAPI


@dataclass(frozen=True)
class FulfillContractCommand(Request[Contract]):
    """Command to fulfill a contract"""
    contract_id: str
    player_id: int


class FulfillContractHandler(RequestHandler[FulfillContractCommand, Contract]):
    """Handler for FulfillContractCommand"""

    def __init__(
        self,
        contract_repository: IContractRepository,
        api_client_factory
    ):
        self._contract_repo = contract_repository
        self._api_client_factory = api_client_factory

    async def handle(self, request: FulfillContractCommand) -> Contract:
        """
        Handle fulfill contract command

        Args:
            request: Command with contract ID and player ID

        Returns:
            Updated contract entity

        Raises:
            ValueError: If contract not found
        """
        # Get API client for player
        api_client = self._api_client_factory(request.player_id)

        # Call API to fulfill contract
        api_client.fulfill_contract(request.contract_id)

        # Load contract from database
        contract = self._contract_repo.find_by_id(
            request.contract_id,
            request.player_id
        )

        if not contract:
            raise ValueError(f"Contract {request.contract_id} not found")

        # Create new contract marked as fulfilled (immutable)
        fulfilled_contract = Contract(
            contract_id=contract.contract_id,
            faction_symbol=contract.faction_symbol,
            type=contract.type,
            terms=contract.terms,
            accepted=contract.accepted,
            fulfilled=True,  # Mark as fulfilled
            deadline_to_accept=contract.deadline_to_accept
        )

        # Save updated contract
        self._contract_repo.save(fulfilled_contract, request.player_id)

        return fulfilled_contract
