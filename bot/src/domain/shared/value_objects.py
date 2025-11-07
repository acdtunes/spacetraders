from dataclasses import dataclass
from typing import Optional
from enum import Enum
import math

@dataclass(frozen=True)
class Waypoint:
    """Immutable waypoint value object"""
    symbol: str
    x: float
    y: float
    system_symbol: Optional[str] = None
    waypoint_type: Optional[str] = None
    traits: tuple = ()
    has_fuel: bool = False
    orbitals: tuple = ()

    def distance_to(self, other: 'Waypoint') -> float:
        """Calculate Euclidean distance to another waypoint"""
        return math.hypot(other.x - self.x, other.y - self.y)

    def is_orbital_of(self, other: 'Waypoint') -> bool:
        """Check if this waypoint orbits another"""
        return other.symbol in self.orbitals or self.symbol in other.orbitals

    def __repr__(self) -> str:
        return f"Waypoint({self.symbol})"

@dataclass(frozen=True)
class Fuel:
    """Immutable fuel value object"""
    current: int
    capacity: int

    def __post_init__(self):
        if self.current < 0:
            raise ValueError("current fuel cannot be negative")
        if self.capacity < 0:
            raise ValueError("fuel capacity cannot be negative")
        if self.current > self.capacity:
            raise ValueError("current fuel cannot exceed capacity")

    def percentage(self) -> float:
        """Fuel as percentage of capacity"""
        return (self.current / self.capacity * 100) if self.capacity > 0 else 0.0

    def consume(self, amount: int) -> 'Fuel':
        """Return new Fuel with amount consumed"""
        if amount < 0:
            raise ValueError("consume amount cannot be negative")
        new_current = max(0, self.current - amount)
        return Fuel(current=new_current, capacity=self.capacity)

    def add(self, amount: int) -> 'Fuel':
        """Return new Fuel with amount added"""
        if amount < 0:
            raise ValueError("add amount cannot be negative")
        new_current = min(self.capacity, self.current + amount)
        return Fuel(current=new_current, capacity=self.capacity)

    def can_travel(self, required: int, safety_margin: float = 0.1) -> bool:
        """Check if sufficient fuel for travel with safety margin"""
        required_with_margin = int(required * (1 + safety_margin))
        return self.current >= required_with_margin

    def is_full(self) -> bool:
        """Check if fuel at capacity"""
        return self.current == self.capacity

    def __repr__(self) -> str:
        return f"Fuel({self.current}/{self.capacity})"

class FlightMode(Enum):
    """Flight modes with time/fuel characteristics"""
    CRUISE = ("CRUISE", 31, 1.0)     # Fast, standard fuel
    DRIFT = ("DRIFT", 26, 0.003)     # Slow, minimal fuel
    BURN = ("BURN", 15, 2.0)         # Very fast, high fuel
    STEALTH = ("STEALTH", 50, 1.0)   # Very slow, stealthy

    def __init__(self, mode_name: str, time_multiplier: int, fuel_rate: float):
        self.mode_name = mode_name
        self.time_multiplier = time_multiplier
        self.fuel_rate = fuel_rate

    def fuel_cost(self, distance: float) -> int:
        """Calculate fuel cost for given distance"""
        if distance == 0:
            return 0
        return max(1, math.ceil(distance * self.fuel_rate))

    def travel_time(self, distance: float, engine_speed: int) -> int:
        """Calculate travel time in seconds"""
        if distance == 0:
            return 0
        return max(1, int((distance * self.time_multiplier) / max(1, engine_speed)))

    @staticmethod
    def select_optimal(current_fuel: int, fuel_cost: int, safety_margin: int = 4) -> 'FlightMode':
        """
        Select optimal mode prioritizing speed while maintaining safety margin.

        Strategy: ALWAYS minimize travel time. Use fastest mode that leaves
        at least safety_margin fuel remaining.

        Priority order: BURN > CRUISE > DRIFT

        Args:
            current_fuel: Current fuel units
            fuel_cost: Base fuel cost for distance (at CRUISE rate)
            safety_margin: Minimum fuel to keep in reserve (default: 4 units)

        Returns:
            Fastest FlightMode that maintains safety margin
        """
        # Try BURN first (fastest: 2x fuel cost)
        burn_cost = int(fuel_cost * FlightMode.BURN.fuel_rate / FlightMode.CRUISE.fuel_rate)
        if current_fuel >= burn_cost + safety_margin:
            return FlightMode.BURN

        # Try CRUISE next (standard: 1x fuel cost)
        if current_fuel >= fuel_cost + safety_margin:
            return FlightMode.CRUISE

        # Fall back to DRIFT (slowest but most fuel efficient)
        # Only use when fuel is critically low
        return FlightMode.DRIFT

@dataclass(frozen=True)
class Distance:
    """Immutable distance value object"""
    units: float

    def __post_init__(self):
        if self.units < 0:
            raise ValueError("distance cannot be negative")

    def with_margin(self, margin: float) -> 'Distance':
        """Return distance with safety margin applied"""
        return Distance(units=self.units * (1 + margin))

    def __repr__(self) -> str:
        return f"{self.units:.1f} units"


@dataclass(frozen=True)
class CargoItem:
    """Individual cargo item in ship's hold"""
    symbol: str        # Trade good symbol (e.g., "IRON_ORE")
    name: str          # Human-readable name
    description: str   # Item description
    units: int         # Quantity in cargo

    def __post_init__(self):
        if self.units < 0:
            raise ValueError("Cargo units cannot be negative")
        if not self.symbol:
            raise ValueError("Cargo symbol cannot be empty")


@dataclass(frozen=True)
class Cargo:
    """Ship cargo manifest with detailed inventory"""
    capacity: int                      # Total cargo capacity
    units: int                         # Total units currently loaded
    inventory: tuple[CargoItem, ...]   # Frozen tuple of items

    def __post_init__(self):
        if self.units < 0:
            raise ValueError("Cargo units cannot be negative")
        if self.capacity < 0:
            raise ValueError("Cargo capacity cannot be negative")
        if self.units > self.capacity:
            raise ValueError(f"Cargo units {self.units} exceed capacity {self.capacity}")

        # Verify inventory sum matches total units
        inventory_sum = sum(item.units for item in self.inventory)
        if inventory_sum != self.units:
            raise ValueError(f"Inventory sum {inventory_sum} != total units {self.units}")

    def has_item(self, symbol: str, min_units: int = 1) -> bool:
        """Check if cargo contains at least min_units of specific item"""
        return self.get_item_units(symbol) >= min_units

    def get_item_units(self, symbol: str) -> int:
        """Get units of specific trade good in cargo (0 if not present)"""
        for item in self.inventory:
            if item.symbol == symbol:
                return item.units
        return 0

    def has_items_other_than(self, symbol: str) -> bool:
        """Check if cargo contains items other than specified symbol"""
        for item in self.inventory:
            if item.symbol != symbol and item.units > 0:
                return True
        return False

    def available_capacity(self) -> int:
        """Calculate available cargo space"""
        return self.capacity - self.units

    def is_empty(self) -> bool:
        """Check if cargo hold is empty"""
        return self.units == 0
