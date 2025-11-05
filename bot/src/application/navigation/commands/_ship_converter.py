"""Ship conversion utility for converting API responses to Ship entities"""
from domain.shared.ship import Ship
from domain.shared.value_objects import Waypoint, Fuel


def convert_api_ship_to_entity(ship_data: dict, player_id: int, waypoint: Waypoint) -> Ship:
    """
    Convert API ship data to Ship entity.

    Args:
        ship_data: Ship data from SpaceTraders API
        player_id: Player ID owning the ship
        waypoint: Waypoint object for ship's current location

    Returns:
        Ship entity

    Raises:
        KeyError: If required fields are missing from ship_data
    """
    # Extract navigation data
    nav = ship_data.get("nav", {})
    nav_status = nav.get("status", "IN_ORBIT")

    # Extract fuel data
    fuel_data = ship_data.get("fuel", {})
    fuel = Fuel(
        current=fuel_data.get("current", 0),
        capacity=fuel_data.get("capacity", 0)
    )

    # Extract cargo data
    cargo = ship_data.get("cargo", {})
    cargo_capacity = cargo.get("capacity", 0)
    cargo_units = cargo.get("units", 0)

    # Extract engine data
    engine = ship_data.get("engine", {})
    engine_speed = engine.get("speed", 0)

    # Create Ship entity
    return Ship(
        ship_symbol=ship_data["symbol"],
        player_id=player_id,
        current_location=waypoint,
        fuel=fuel,
        fuel_capacity=fuel.capacity,
        cargo_capacity=cargo_capacity,
        cargo_units=cargo_units,
        engine_speed=engine_speed,
        nav_status=nav_status
    )
