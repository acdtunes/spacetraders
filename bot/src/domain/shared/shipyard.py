"""Shipyard domain value objects."""
from dataclasses import dataclass
from typing import List, Optional, Dict, Any


@dataclass(frozen=True)
class ShipListing:
    """Represents a ship listing in a shipyard.

    A ship listing contains all the information about a ship type available
    for purchase, including its specifications and price.
    """
    ship_type: str
    name: str
    description: str
    purchase_price: int
    frame: Optional[Dict[str, Any]] = None
    reactor: Optional[Dict[str, Any]] = None
    engine: Optional[Dict[str, Any]] = None
    modules: Optional[List[Dict[str, Any]]] = None
    mounts: Optional[List[Dict[str, Any]]] = None


@dataclass(frozen=True)
class Shipyard:
    """Represents a shipyard at a waypoint.

    A shipyard is where ships can be purchased. It contains information about
    available ship types, current listings, transaction history, and modification fees.
    """
    symbol: str
    ship_types: List[str]
    listings: List[ShipListing]
    transactions: List[Dict[str, Any]]
    modification_fee: int
