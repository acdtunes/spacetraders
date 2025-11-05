from ..shared.exceptions import DomainException

class NavigationException(DomainException):
    """Base exception for navigation domain"""
    pass

class InsufficientFuelError(NavigationException):
    """Raised when ship doesn't have enough fuel"""
    pass

class InvalidRouteError(NavigationException):
    """Raised when route is invalid"""
    pass

class RouteExecutionError(NavigationException):
    """Raised when route execution fails"""
    pass

class NoRouteFoundError(NavigationException):
    """Raised when no valid route can be found"""
    pass
