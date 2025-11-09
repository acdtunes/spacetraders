"""Convenience re-export of repository interfaces"""
from .outbound.repositories import IPlayerRepository, IShipRepository, IWaypointRepository

__all__ = ['IPlayerRepository', 'IShipRepository', 'IWaypointRepository']
