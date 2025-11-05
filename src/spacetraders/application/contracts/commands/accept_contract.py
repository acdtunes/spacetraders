"""Accept contract command"""
from dataclasses import dataclass

from pymediatr import Request, RequestHandler

from ....domain.shared.contract import Contract
from ....ports.outbound.repositories import IContractRepository
from ....ports.outbound.api_client import ISpaceTradersAPI


@dataclass(frozen=True)
class AcceptContractCommand(Request[Contract]):
    """Command to accept a contract"""
    contract_id: str
    player_id: int


class AcceptContractHandler(RequestHandler[AcceptContractCommand, Contract]):
    """Handler for AcceptContractCommand"""

    def __init__(
        self,
        contract_repository: IContractRepository,
        api_client_factory
    ):
        self._contract_repo = contract_repository
        self._api_client_factory = api_client_factory

    async def handle(self, request: AcceptContractCommand) -> Contract:
        """
        Handle accept contract command

        Args:
            request: Command with contract ID and player ID

        Returns:
            Updated contract entity

        Raises:
            ValueError: If contract not found
        """
        # Get API client for player
        api_client = self._api_client_factory(request.player_id)

        # Call API to accept contract
        api_client.accept_contract(request.contract_id)

        # Load contract from database
        contract = self._contract_repo.find_by_id(
            request.contract_id,
            request.player_id
        )

        if not contract:
            raise ValueError(f"Contract {request.contract_id} not found")

        # Accept the contract (domain logic)
        contract.accept()

        # Save updated contract
        self._contract_repo.save(contract, request.player_id)

        return contract
