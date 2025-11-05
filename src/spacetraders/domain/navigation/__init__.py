"""Navigation domain - Route planning and execution"""

from .route import Route, RouteSegment, RouteStatus
from .exceptions import (
    NavigationException,
    InsufficientFuelError,
    InvalidRouteError,
    RouteExecutionError,
    NoRouteFoundError
)

__all__ = [
    'Route',
    'RouteSegment',
    'RouteStatus',
    'NavigationException',
    'InsufficientFuelError',
    'InvalidRouteError',
    'RouteExecutionError',
    'NoRouteFoundError',
]
