"""Contract domain entity and value objects"""
from dataclasses import dataclass
from datetime import datetime
from enum import Enum
from typing import List

from .value_objects import Waypoint
from .exceptions import DomainException


class ContractException(DomainException):
    """Base exception for contract-related errors"""
    pass


class ContractAlreadyAcceptedError(ContractException):
    """Raised when trying to accept an already accepted contract"""
    pass


class ContractStatus(Enum):
    """Contract status enumeration"""
    OFFERED = "OFFERED"
    ACCEPTED = "ACCEPTED"
    FULFILLED = "FULFILLED"
    FAILED = "FAILED"
    EXPIRED = "EXPIRED"


@dataclass(frozen=True)
class Payment:
    """Payment terms for a contract"""
    on_accepted: int  # Credits received when accepting contract
    on_fulfilled: int  # Credits received when fulfilling contract

    def total(self) -> int:
        """Calculate total payment"""
        return self.on_accepted + self.on_fulfilled


@dataclass(frozen=True)
class Delivery:
    """Delivery requirement for a contract"""
    trade_symbol: str  # Good to deliver (e.g., "IRON_ORE")
    destination: Waypoint  # Where to deliver
    units_required: int  # Total units required
    units_fulfilled: int  # Units already delivered

    def __post_init__(self):
        """Validate delivery data"""
        if self.units_required < 0:
            raise ValueError("units_required cannot be negative")
        if self.units_fulfilled < 0:
            raise ValueError("units_fulfilled cannot be negative")
        if self.units_fulfilled > self.units_required:
            raise ValueError("units_fulfilled cannot exceed units_required")

    def remaining(self) -> int:
        """Calculate remaining units to deliver"""
        return self.units_required - self.units_fulfilled

    def is_fulfilled(self) -> bool:
        """Check if delivery requirement is met"""
        return self.units_fulfilled >= self.units_required


@dataclass(frozen=True)
class ContractTerms:
    """Terms and conditions of a contract"""
    deadline: datetime  # Deadline to fulfill contract
    payment: Payment  # Payment terms
    deliveries: List[Delivery]  # Delivery requirements

    def all_deliveries_fulfilled(self) -> bool:
        """Check if all delivery requirements are met"""
        return all(d.is_fulfilled() for d in self.deliveries)


class Contract:
    """
    Contract entity - represents a SpaceTraders contract

    Invariants:
    - contract_id must be unique and non-empty
    - Cannot accept an already accepted contract
    - Status transitions must be valid
    """

    def __init__(
        self,
        contract_id: str,
        faction_symbol: str,
        type: str,
        terms: ContractTerms,
        accepted: bool,
        fulfilled: bool,
        deadline_to_accept: datetime
    ):
        """
        Initialize a Contract entity

        Args:
            contract_id: Unique contract identifier
            faction_symbol: Faction offering the contract
            type: Contract type (e.g., "PROCUREMENT")
            terms: Contract terms and conditions
            accepted: Whether contract has been accepted
            fulfilled: Whether contract has been fulfilled
            deadline_to_accept: Deadline to accept the contract

        Raises:
            ValueError: If validation fails
        """
        if not contract_id or not contract_id.strip():
            raise ValueError("contract_id cannot be empty")
        if not faction_symbol or not faction_symbol.strip():
            raise ValueError("faction_symbol cannot be empty")

        self._contract_id = contract_id.strip()
        self._faction_symbol = faction_symbol.strip()
        self._type = type
        self._terms = terms
        self._accepted = accepted
        self._fulfilled = fulfilled
        self._deadline_to_accept = deadline_to_accept

    @property
    def contract_id(self) -> str:
        """Get contract ID"""
        return self._contract_id

    @property
    def faction_symbol(self) -> str:
        """Get faction symbol"""
        return self._faction_symbol

    @property
    def type(self) -> str:
        """Get contract type"""
        return self._type

    @property
    def terms(self) -> ContractTerms:
        """Get contract terms"""
        return self._terms

    @property
    def accepted(self) -> bool:
        """Check if contract is accepted"""
        return self._accepted

    @property
    def fulfilled(self) -> bool:
        """Check if contract is fulfilled"""
        return self._fulfilled

    @property
    def deadline_to_accept(self) -> datetime:
        """Get deadline to accept contract"""
        return self._deadline_to_accept

    @property
    def status(self) -> ContractStatus:
        """Get current contract status"""
        if self._fulfilled:
            return ContractStatus.FULFILLED
        elif self._accepted:
            return ContractStatus.ACCEPTED
        else:
            return ContractStatus.OFFERED

    def accept(self) -> None:
        """
        Accept the contract

        Raises:
            ContractAlreadyAcceptedError: If contract is already accepted
        """
        if self._accepted:
            raise ContractAlreadyAcceptedError(
                f"Contract {self._contract_id} is already accepted"
            )
        self._accepted = True

    def is_fulfilled(self) -> bool:
        """
        Check if all contract requirements are fulfilled

        Returns:
            True if all deliveries are complete, False otherwise
        """
        return self._terms.all_deliveries_fulfilled()

    def remaining_units(self) -> int:
        """
        Calculate total remaining units across all deliveries

        Returns:
            Total units still needed to fulfill contract
        """
        return sum(d.remaining() for d in self._terms.deliveries)

    def __repr__(self) -> str:
        return (
            f"Contract(id={self._contract_id}, "
            f"faction={self._faction_symbol}, "
            f"status={self.status.value})"
        )
