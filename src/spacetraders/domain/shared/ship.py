from typing import Optional
from .value_objects import Waypoint, Fuel, FlightMode
from .exceptions import DomainException


class ShipException(DomainException):
    """Base exception for ship-related errors"""
    pass


class InvalidNavStatusError(ShipException):
    """Raised when ship is in wrong nav status for operation"""
    pass


class InsufficientFuelError(ShipException):
    """Raised when ship doesn't have enough fuel"""
    pass


class FuelCapacityExceededError(ShipException):
    """Raised when trying to add fuel beyond capacity"""
    pass


class InvalidShipDataError(ShipException):
    """Raised when ship data is invalid"""
    pass


class Ship:
    """
    Ship entity - represents a player's spacecraft with navigation capabilities

    Invariants:
    - ship_symbol must be unique and non-empty
    - player_id must be positive
    - nav_status must be one of: IN_ORBIT, DOCKED, IN_TRANSIT
    - fuel operations respect capacity limits
    - cargo_units cannot exceed cargo_capacity
    - engine_speed must be positive

    Navigation state machine:
    - DOCKED -> depart() -> IN_ORBIT
    - IN_ORBIT -> navigate() -> IN_TRANSIT
    - IN_TRANSIT -> arrive() -> IN_ORBIT
    - IN_ORBIT -> dock() -> DOCKED
    """

    # Valid navigation statuses
    DOCKED = "DOCKED"
    IN_ORBIT = "IN_ORBIT"
    IN_TRANSIT = "IN_TRANSIT"

    VALID_NAV_STATUSES = {DOCKED, IN_ORBIT, IN_TRANSIT}

    def __init__(
        self,
        ship_symbol: str,
        player_id: int,
        current_location: Waypoint,
        fuel: Fuel,
        fuel_capacity: int,
        cargo_capacity: int,
        cargo_units: int,
        engine_speed: int,
        nav_status: str = IN_ORBIT
    ):
        """
        Initialize a Ship entity

        Args:
            ship_symbol: Unique identifier for the ship
            player_id: ID of the owning player
            current_location: Current waypoint location
            fuel: Current fuel state
            fuel_capacity: Maximum fuel capacity
            cargo_capacity: Maximum cargo capacity
            cargo_units: Current cargo units
            engine_speed: Ship's engine speed rating
            nav_status: Current navigation status (default: IN_ORBIT)

        Raises:
            InvalidShipDataError: If any validation fails
        """
        self._validate_initialization(
            ship_symbol, player_id, fuel, fuel_capacity,
            cargo_capacity, cargo_units, engine_speed, nav_status
        )

        self._ship_symbol = ship_symbol.strip()
        self._player_id = player_id
        self._current_location = current_location
        self._fuel = fuel
        self._fuel_capacity = fuel_capacity
        self._cargo_capacity = cargo_capacity
        self._cargo_units = cargo_units
        self._engine_speed = engine_speed
        self._nav_status = nav_status

    def _validate_initialization(
        self,
        ship_symbol: str,
        player_id: int,
        fuel: Fuel,
        fuel_capacity: int,
        cargo_capacity: int,
        cargo_units: int,
        engine_speed: int,
        nav_status: str
    ) -> None:
        """Validate ship initialization parameters"""
        if not ship_symbol or not ship_symbol.strip():
            raise InvalidShipDataError("ship_symbol cannot be empty")

        if player_id <= 0:
            raise InvalidShipDataError("player_id must be positive")

        if fuel_capacity < 0:
            raise InvalidShipDataError("fuel_capacity cannot be negative")

        if fuel.capacity != fuel_capacity:
            raise InvalidShipDataError("fuel capacity must match fuel_capacity")

        if cargo_capacity < 0:
            raise InvalidShipDataError("cargo_capacity cannot be negative")

        if cargo_units < 0:
            raise InvalidShipDataError("cargo_units cannot be negative")

        if cargo_units > cargo_capacity:
            raise InvalidShipDataError("cargo_units cannot exceed cargo_capacity")

        if engine_speed <= 0:
            raise InvalidShipDataError("engine_speed must be positive")

        if nav_status not in self.VALID_NAV_STATUSES:
            raise InvalidShipDataError(
                f"nav_status must be one of {self.VALID_NAV_STATUSES}, got: {nav_status}"
            )

    # Properties

    @property
    def ship_symbol(self) -> str:
        """Get ship's unique symbol"""
        return self._ship_symbol

    @property
    def player_id(self) -> int:
        """Get owning player's ID"""
        return self._player_id

    @property
    def current_location(self) -> Waypoint:
        """Get current waypoint location"""
        return self._current_location

    @property
    def fuel(self) -> Fuel:
        """Get current fuel state"""
        return self._fuel

    @property
    def fuel_capacity(self) -> int:
        """Get maximum fuel capacity"""
        return self._fuel_capacity

    @property
    def cargo_capacity(self) -> int:
        """Get maximum cargo capacity"""
        return self._cargo_capacity

    @property
    def cargo_units(self) -> int:
        """Get current cargo units"""
        return self._cargo_units

    @property
    def engine_speed(self) -> int:
        """Get engine speed rating"""
        return self._engine_speed

    @property
    def nav_status(self) -> str:
        """Get current navigation status"""
        return self._nav_status

    # Navigation Status Management

    def ensure_in_orbit(self) -> bool:
        """
        Ensure ship is in orbit (state machine orchestration)

        Transitions:
        - DOCKED → IN_ORBIT (automatic transition)
        - IN_ORBIT → IN_ORBIT (no-op)
        - IN_TRANSIT → error (cannot transition while traveling)

        Returns:
            True if state was changed, False if already in orbit

        Raises:
            InvalidNavStatusError: If ship is in transit
        """
        if self._nav_status == self.IN_ORBIT:
            # Already in orbit, no-op
            return False

        if self._nav_status == self.IN_TRANSIT:
            raise InvalidNavStatusError(
                f"Cannot orbit while in transit"
            )

        # Must be docked, transition to orbit
        self._nav_status = self.IN_ORBIT
        return True

    def ensure_docked(self) -> bool:
        """
        Ensure ship is docked (state machine orchestration)

        Transitions:
        - IN_ORBIT → DOCKED (automatic transition)
        - DOCKED → DOCKED (no-op)
        - IN_TRANSIT → error (cannot transition while traveling)

        Returns:
            True if state was changed, False if already docked

        Raises:
            InvalidNavStatusError: If ship is in transit
        """
        if self._nav_status == self.DOCKED:
            # Already docked, no-op
            return False

        if self._nav_status == self.IN_TRANSIT:
            raise InvalidNavStatusError(
                f"Cannot dock while in transit"
            )

        # Must be in orbit, transition to docked
        self._nav_status = self.DOCKED
        return True

    def depart(self) -> None:
        """
        Depart from docked status to orbit

        Transitions: DOCKED -> IN_ORBIT

        Raises:
            InvalidNavStatusError: If ship is not docked
        """
        self.ensure_docked()
        self._nav_status = self.IN_ORBIT

    def dock(self) -> None:
        """
        Dock at current location

        Transitions: IN_ORBIT -> DOCKED

        Raises:
            InvalidNavStatusError: If ship is not in orbit
        """
        self.ensure_in_orbit()
        self._nav_status = self.DOCKED

    def arrive(self) -> None:
        """
        Arrive at destination after transit

        Transitions: IN_TRANSIT -> IN_ORBIT

        Raises:
            InvalidNavStatusError: If ship is not in transit
        """
        if self._nav_status != self.IN_TRANSIT:
            raise InvalidNavStatusError(
                f"Ship must be in transit to arrive, currently: {self._nav_status}"
            )
        self._nav_status = self.IN_ORBIT

    def start_transit(self, destination: Waypoint) -> None:
        """
        Begin transit to destination

        Transitions: IN_ORBIT -> IN_TRANSIT
        Updates current_location to destination

        Args:
            destination: Target waypoint

        Raises:
            InvalidNavStatusError: If ship is not in orbit
        """
        self.ensure_in_orbit()
        self._nav_status = self.IN_TRANSIT
        self._current_location = destination

    # Fuel Management

    def consume_fuel(self, amount: int) -> None:
        """
        Consume fuel from ship's tanks

        Args:
            amount: Amount of fuel to consume

        Raises:
            ValueError: If amount is negative
            InsufficientFuelError: If not enough fuel available
        """
        if amount < 0:
            raise ValueError("fuel amount cannot be negative")

        if self._fuel.current < amount:
            raise InsufficientFuelError(
                f"Insufficient fuel: need {amount}, have {self._fuel.current}"
            )

        self._fuel = self._fuel.consume(amount)

    def refuel(self, amount: int) -> None:
        """
        Add fuel to ship's tanks

        Args:
            amount: Amount of fuel to add (will cap at capacity)

        Raises:
            ValueError: If amount is negative
        """
        if amount < 0:
            raise ValueError("fuel amount cannot be negative")

        self._fuel = self._fuel.add(amount)

    def refuel_to_full(self) -> int:
        """
        Refuel ship to full capacity

        Returns:
            Amount of fuel added
        """
        fuel_needed = self._fuel_capacity - self._fuel.current
        if fuel_needed > 0:
            self.refuel(fuel_needed)
        return fuel_needed

    # Navigation Calculations

    def can_navigate_to(self, destination: Waypoint) -> bool:
        """
        Check if ship can navigate to destination with current fuel

        Uses DRIFT mode (most fuel-efficient) for calculation

        Args:
            destination: Target waypoint

        Returns:
            True if ship has enough fuel, False otherwise
        """
        distance = self._current_location.distance_to(destination)
        min_fuel_required = FlightMode.DRIFT.fuel_cost(distance)
        return self._fuel.current >= min_fuel_required

    def calculate_fuel_for_trip(
        self,
        destination: Waypoint,
        mode: FlightMode
    ) -> int:
        """
        Calculate fuel required for trip to destination

        Args:
            destination: Target waypoint
            mode: Flight mode to use

        Returns:
            Fuel units required for the trip
        """
        distance = self._current_location.distance_to(destination)
        return mode.fuel_cost(distance)

    def needs_refuel_for_journey(
        self,
        destination: Waypoint,
        safety_margin: float = 0.1
    ) -> bool:
        """
        Check if ship needs refueling before journey

        Uses safety margin to ensure adequate fuel reserves

        Args:
            destination: Target waypoint
            safety_margin: Safety margin as percentage (default: 10%)

        Returns:
            True if refueling is needed, False otherwise
        """
        distance = self._current_location.distance_to(destination)
        # Use CRUISE mode for normal calculations
        fuel_required = FlightMode.CRUISE.fuel_cost(distance)
        return not self._fuel.can_travel(fuel_required, safety_margin)

    def calculate_travel_time(
        self,
        destination: Waypoint,
        mode: FlightMode
    ) -> int:
        """
        Calculate travel time to destination

        Args:
            destination: Target waypoint
            mode: Flight mode to use

        Returns:
            Travel time in seconds
        """
        distance = self._current_location.distance_to(destination)
        return mode.travel_time(distance, self._engine_speed)

    def select_optimal_flight_mode(self, distance: float = 30.0) -> FlightMode:
        """
        Select optimal flight mode for a given distance.

        Uses speed-first selection (BURN > CRUISE > DRIFT) while maintaining
        a safety margin of 4 fuel units.

        Args:
            distance: Distance to travel (default: 30 units for general assessment)

        Returns:
            Recommended flight mode
        """
        # Calculate fuel cost at CRUISE rate as baseline
        cruise_cost = FlightMode.CRUISE.fuel_cost(distance)
        return FlightMode.select_optimal(self._fuel.current, cruise_cost, safety_margin=4)

    # Cargo Management

    def has_cargo_space(self, units: int = 1) -> bool:
        """
        Check if ship has available cargo space

        Args:
            units: Number of cargo units to check (default: 1)

        Returns:
            True if space available, False otherwise
        """
        return (self._cargo_units + units) <= self._cargo_capacity

    def available_cargo_space(self) -> int:
        """
        Get available cargo space

        Returns:
            Number of cargo units available
        """
        return self._cargo_capacity - self._cargo_units

    def is_cargo_empty(self) -> bool:
        """Check if cargo hold is empty"""
        return self._cargo_units == 0

    def is_cargo_full(self) -> bool:
        """Check if cargo hold is full"""
        return self._cargo_units >= self._cargo_capacity

    # State Queries

    def is_docked(self) -> bool:
        """Check if ship is docked"""
        return self._nav_status == self.DOCKED

    def is_in_orbit(self) -> bool:
        """Check if ship is in orbit"""
        return self._nav_status == self.IN_ORBIT

    def is_in_transit(self) -> bool:
        """Check if ship is in transit"""
        return self._nav_status == self.IN_TRANSIT

    def is_at_location(self, waypoint: Waypoint) -> bool:
        """
        Check if ship is at specified waypoint

        Args:
            waypoint: Waypoint to check

        Returns:
            True if at location, False otherwise
        """
        return self._current_location.symbol == waypoint.symbol

    def __repr__(self) -> str:
        return (
            f"Ship(symbol={self._ship_symbol}, "
            f"location={self._current_location.symbol}, "
            f"status={self._nav_status}, "
            f"fuel={self._fuel})"
        )

    def __eq__(self, other: object) -> bool:
        """Two ships are equal if they have the same symbol and player_id"""
        if not isinstance(other, Ship):
            return NotImplemented
        return (
            self._ship_symbol == other._ship_symbol
            and self._player_id == other._player_id
        )

    def __hash__(self) -> int:
        """Hash based on ship_symbol and player_id"""
        return hash((self._ship_symbol, self._player_id))
