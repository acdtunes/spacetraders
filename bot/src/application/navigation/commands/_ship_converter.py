"""Ship conversion utility for converting API responses to Ship entities"""
from domain.shared.ship import Ship
from domain.shared.value_objects import Waypoint, Fuel, Cargo, CargoItem


def convert_api_ship_to_entity(ship_data: dict, player_id: int, waypoint: Waypoint) -> Ship:
    """
    Convert API ship data to Ship entity with full cargo inventory.

    Args:
        ship_data: Ship data from SpaceTraders API
        player_id: Player ID owning the ship
        waypoint: Waypoint object for ship's current location

    Returns:
        Ship entity with full cargo inventory

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

    # Extract cargo data with inventory
    cargo_data = ship_data.get("cargo", {})
    cargo_capacity = cargo_data.get("capacity", 0)
    cargo_units = cargo_data.get("units", 0)

    # Extract cargo inventory from API response
    inventory_data = cargo_data.get("inventory", [])
    cargo_items = tuple(
        CargoItem(
            symbol=item['symbol'],
            name=item.get('name', item['symbol']),
            description=item.get('description', ''),
            units=item['units']
        )
        for item in inventory_data
    )

    # Create Cargo object with inventory
    cargo = Cargo(
        capacity=cargo_capacity,
        units=cargo_units,
        inventory=cargo_items
    )

    # Extract engine data
    engine = ship_data.get("engine", {})
    engine_speed = engine.get("speed", 0)

    # Create Ship entity with cargo inventory
    return Ship(
        ship_symbol=ship_data["symbol"],
        player_id=player_id,
        current_location=waypoint,
        fuel=fuel,
        fuel_capacity=fuel.capacity,
        cargo_capacity=cargo_capacity,
        cargo_units=cargo_units,
        engine_speed=engine_speed,
        nav_status=nav_status,
        cargo=cargo  # Pass full cargo object with inventory
    )
