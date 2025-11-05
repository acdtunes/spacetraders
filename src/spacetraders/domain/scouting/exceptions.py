"""Scouting domain exceptions"""
from ..shared.exceptions import DomainException


class ScoutException(DomainException):
    """Base exception for scouting operations"""
    pass


class NoMarketsFoundError(ScoutException):
    """Raised when a system has no markets"""
    pass


class MarketDataUnavailableError(ScoutException):
    """Raised when market data cannot be retrieved from API"""
    pass
