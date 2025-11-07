import json
from datetime import datetime
from typing import Optional, Dict, Any, List
from sqlite3 import Row

from domain.shared.player import Player
from domain.shared.ship import Ship
from domain.shared.value_objects import Waypoint, Fuel, FlightMode, Cargo, CargoItem
from domain.navigation.route import Route, RouteSegment, RouteStatus

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
            created_at=datetime.fromisoformat(row["created_at"]),
            last_active=datetime.fromisoformat(row["last_active"]) if row["last_active"] else None,
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


class RouteMapper:
    """Map between database rows and Route entities"""

    @staticmethod
    def from_db_row(row: Row) -> Route:
        """
        Convert database row to Route entity

        Args:
            row: Database row with route data

        Returns:
            Route entity
        """
        # Deserialize segments from JSON
        segments_data = json.loads(row["segments_json"])
        segments = [RouteMapper._deserialize_segment(seg) for seg in segments_data]

        # Create route with basic data
        route = Route(
            route_id=row["route_id"],
            ship_symbol=row["ship_symbol"],
            player_id=int(row["player_id"]),
            segments=segments,
            ship_fuel_capacity=int(row["ship_fuel_capacity"])
        )

        # Restore state
        route._status = RouteStatus(row["status"])
        route._current_segment_index = int(row["current_segment_index"])

        return route

    @staticmethod
    def to_db_dict(route: Route) -> Dict[str, Any]:
        """
        Convert Route entity to database dictionary

        Args:
            route: Route entity to convert

        Returns:
            Dictionary with database columns
        """
        # Serialize segments to JSON
        segments_data = [RouteMapper._serialize_segment(seg) for seg in route.segments]

        return {
            "route_id": route.route_id,
            "ship_symbol": route.ship_symbol,
            "player_id": route.player_id,
            "status": route.status.value,
            "current_segment_index": route.get_current_segment_index(),
            "ship_fuel_capacity": route._ship_fuel_capacity,
            "segments_json": json.dumps(segments_data)
        }

    @staticmethod
    def _serialize_segment(segment: RouteSegment) -> Dict[str, Any]:
        """Serialize a RouteSegment to dictionary"""
        return {
            "from_waypoint": {
                "symbol": segment.from_waypoint.symbol,
                "x": segment.from_waypoint.x,
                "y": segment.from_waypoint.y,
                "system_symbol": segment.from_waypoint.system_symbol,
                "waypoint_type": segment.from_waypoint.waypoint_type,
                "traits": list(segment.from_waypoint.traits),
                "has_fuel": segment.from_waypoint.has_fuel,
                "orbitals": list(segment.from_waypoint.orbitals)
            },
            "to_waypoint": {
                "symbol": segment.to_waypoint.symbol,
                "x": segment.to_waypoint.x,
                "y": segment.to_waypoint.y,
                "system_symbol": segment.to_waypoint.system_symbol,
                "waypoint_type": segment.to_waypoint.waypoint_type,
                "traits": list(segment.to_waypoint.traits),
                "has_fuel": segment.to_waypoint.has_fuel,
                "orbitals": list(segment.to_waypoint.orbitals)
            },
            "distance": segment.distance,
            "fuel_required": segment.fuel_required,
            "travel_time": segment.travel_time,
            "flight_mode": segment.flight_mode.mode_name,
            "requires_refuel": segment.requires_refuel
        }

    @staticmethod
    def _deserialize_segment(data: Dict[str, Any]) -> RouteSegment:
        """Deserialize a dictionary to RouteSegment"""
        from_wp_data = data["from_waypoint"]
        to_wp_data = data["to_waypoint"]

        from_waypoint = Waypoint(
            symbol=from_wp_data["symbol"],
            x=from_wp_data["x"],
            y=from_wp_data["y"],
            system_symbol=from_wp_data.get("system_symbol"),
            waypoint_type=from_wp_data.get("waypoint_type"),
            traits=tuple(from_wp_data.get("traits", [])),
            has_fuel=from_wp_data.get("has_fuel", False),
            orbitals=tuple(from_wp_data.get("orbitals", []))
        )

        to_waypoint = Waypoint(
            symbol=to_wp_data["symbol"],
            x=to_wp_data["x"],
            y=to_wp_data["y"],
            system_symbol=to_wp_data.get("system_symbol"),
            waypoint_type=to_wp_data.get("waypoint_type"),
            traits=tuple(to_wp_data.get("traits", [])),
            has_fuel=to_wp_data.get("has_fuel", False),
            orbitals=tuple(to_wp_data.get("orbitals", []))
        )

        # Convert flight mode string to enum
        flight_mode = FlightMode[data["flight_mode"]]

        return RouteSegment(
            from_waypoint=from_waypoint,
            to_waypoint=to_waypoint,
            distance=data["distance"],
            fuel_required=data["fuel_required"],
            travel_time=data["travel_time"],
            flight_mode=flight_mode,
            requires_refuel=data.get("requires_refuel", False)
        )
