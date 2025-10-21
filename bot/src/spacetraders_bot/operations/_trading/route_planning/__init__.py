"""
Route Planning Package

Provides multi-leg trading route optimization and fixed route construction.

Public API:
- FixedRouteBuilder - Simple buy→sell route construction
- create_fixed_route - Legacy function wrapper
"""

from .fixed_route_builder import FixedRouteBuilder, create_fixed_route

__all__ = [
    'FixedRouteBuilder',
    'create_fixed_route',
]
