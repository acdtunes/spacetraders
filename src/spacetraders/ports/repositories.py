"""Convenience re-export of repository interfaces"""
from .outbound.repositories import IPlayerRepository, IShipRepository, IRouteRepository, IWaypointRepository

__all__ = ['IPlayerRepository', 'IShipRepository', 'IRouteRepository', 'IWaypointRepository']
