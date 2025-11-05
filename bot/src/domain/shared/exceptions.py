class DomainException(Exception):
    """Base exception for all domain errors"""
    pass

class DuplicateAgentSymbolError(DomainException):
    """Raised when trying to register duplicate agent symbol"""
    pass

class PlayerNotFoundError(DomainException):
    """Raised when player not found"""
    pass

class DuplicateShipError(DomainException):
    """Raised when trying to create duplicate ship"""
    pass

class ShipNotFoundError(DomainException):
    """Raised when ship not found"""
    pass

class InsufficientCreditsError(DomainException):
    """Raised when player doesn't have enough credits for a purchase"""
    pass

class ShipNotAvailableError(DomainException):
    """Raised when requested ship type is not available at shipyard"""
    pass

class ShipyardNotFoundError(DomainException):
    """Raised when shipyard doesn't exist at waypoint"""
    pass

class NoShipyardFoundError(DomainException):
    """Raised when no shipyard in system sells the desired ship type"""
    pass
