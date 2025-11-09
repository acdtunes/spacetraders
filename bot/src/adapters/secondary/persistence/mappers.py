import json
from datetime import datetime
from typing import Optional, Dict, Any, List
from sqlite3 import Row

from domain.shared.player import Player
from domain.shared.ship import Ship
from domain.shared.value_objects import Waypoint, Fuel, FlightMode, Cargo, CargoItem


def _parse_datetime(value):
    """Parse datetime from database - handles both SQLite strings and PostgreSQL datetime objects

    Ensures all datetimes are timezone-aware (UTC) for consistency.
    """
    from datetime import timezone

    if value is None:
        return None

    if isinstance(value, datetime):
        # PostgreSQL returns datetime objects
        dt = value
    else:
        # SQLite returns ISO strings
        dt = datetime.fromisoformat(value)

    # Ensure timezone-aware (assume UTC if naive)
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)

    return dt


class PlayerMapper:
    """Map between database rows and Player entities"""

    @staticmethod
    def from_db_row(row: Row) -> Player:
        """
        Convert database row to Player entity.

        NOTE: Credits are cached in database but should be synchronized from API
        via SyncPlayerCommand to ensure they remain fresh.
        """
        return Player(
            player_id=int(row["player_id"]),
            agent_symbol=row["agent_symbol"],
            token=row["token"],
            created_at=_parse_datetime(row["created_at"]),
            last_active=_parse_datetime(row["last_active"]),
            metadata=json.loads(row["metadata"]) if row["metadata"] else {},
            credits=int(row["credits"] if "credits" in row.keys() else 0)  # Default to 0 if not present
        )

    @staticmethod
    def to_db_dict(player: Player) -> Dict[str, Any]:
        """
        Convert Player entity to database dictionary.

        NOTE: Credits are cached in database for display purposes.
        Use SyncPlayerCommand to keep them synchronized with the API.
        """
        return {
            "player_id": player.player_id,
            "agent_symbol": player.agent_symbol,
            "token": player.token,
            "created_at": player.created_at.isoformat(),
            "last_active": player.last_active.isoformat() if player.last_active else None,
            "metadata": json.dumps(player.metadata) if player.metadata else None,
            "credits": player.credits
        }


class ShipMapper:
    """Map between database rows and Ship entities"""

    @staticmethod
    def from_db_row(row: Row, waypoint: Waypoint) -> Ship:
        """
        Convert database row to Ship entity

        Args:
            row: Database row with ship data
            waypoint: Reconstructed Waypoint object for current location

        Returns:
            Ship entity
        """
        fuel = Fuel(
            current=int(row["fuel_current"]),
            capacity=int(row["fuel_capacity"])
        )

        # Parse cargo inventory from JSON (if present)
        try:
            cargo_inventory_json = row["cargo_inventory"]
            if cargo_inventory_json:
                try:
                    inventory_data = json.loads(cargo_inventory_json)
                    cargo_items = tuple(
                        CargoItem(
                            symbol=item['symbol'],
                            name=item.get('name', item['symbol']),
                            description=item.get('description', ''),
                            units=item['units']
                        )
                        for item in inventory_data
                    )
                except (json.JSONDecodeError, KeyError):
                    # If parsing fails, fall back to empty inventory
                    cargo_items = ()
            else:
                cargo_items = ()
        except (KeyError, IndexError):
            # Column doesn't exist or is not accessible - use empty inventory
            cargo_items = ()

        # Create Cargo object
        cargo_units = int(row["cargo_units"])
        cargo_capacity = int(row["cargo_capacity"])

        # Backward compatibility: if we have cargo_units but no inventory, create placeholder
        if cargo_units > 0 and not cargo_items:
            cargo_items = (CargoItem(
                symbol="UNKNOWN",
                name="Unknown Cargo",
                description="Legacy cargo without detailed inventory",
                units=cargo_units
            ),)

        cargo = Cargo(
            capacity=cargo_capacity,
            units=cargo_units,
            inventory=cargo_items
        )

        return Ship(
            ship_symbol=row["ship_symbol"],
            player_id=int(row["player_id"]),
            current_location=waypoint,
            fuel=fuel,
            fuel_capacity=int(row["fuel_capacity"]),
            cargo_capacity=cargo_capacity,
            cargo_units=cargo_units,
            engine_speed=int(row["engine_speed"]),
            nav_status=row["nav_status"],
            cargo=cargo
        )

    @staticmethod
    def to_db_dict(ship: Ship) -> Dict[str, Any]:
        """
        Convert Ship entity to database dictionary

        Args:
            ship: Ship entity to convert

        Returns:
            Dictionary with database columns
        """
        # Serialize cargo inventory to JSON
        cargo_inventory_json = json.dumps([
            {
                'symbol': item.symbol,
                'name': item.name,
                'description': item.description,
                'units': item.units
            }
            for item in ship.cargo.inventory
        ])

        return {
            "ship_symbol": ship.ship_symbol,
            "player_id": ship.player_id,
            "current_location_symbol": ship.current_location.symbol,
            "fuel_current": ship.fuel.current,
            "fuel_capacity": ship.fuel_capacity,
            "cargo_capacity": ship.cargo_capacity,
            "cargo_units": ship.cargo_units,
            "cargo_inventory": cargo_inventory_json,
            "engine_speed": ship.engine_speed,
            "nav_status": ship.nav_status,
            "system_symbol": ship.current_location.system_symbol or ""
        }


