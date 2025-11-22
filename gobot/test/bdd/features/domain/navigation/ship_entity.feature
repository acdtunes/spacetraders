Feature: Ship Entity
  As a SpaceTraders bot
  I want to manage ship entities with proper state transitions
  So that I can navigate, refuel, and manage cargo safely

  # ============================================================================
  # Ship Initialization Tests
  # ============================================================================

  Scenario: Create ship with valid data
    When I create a ship with symbol "SHIP-1", player 1, at "X1-A1", fuel 100/100, cargo 0/40, speed 30, status "IN_ORBIT"
    Then the ship should have symbol "SHIP-1"
    And the ship should have player_id 1
    And the ship should be at location "X1-A1"
    And the ship should have 100 units of fuel
    And the ship fuel capacity should be 100
    And the ship cargo capacity should be 40
    And the ship cargo units should be 0
    And the ship engine speed should be 30
    And the ship should be in orbit

  Scenario: Create ship with invalid empty symbol
    When I attempt to create a ship with empty ship_symbol
    Then ship creation should fail with error "ship_symbol cannot be empty"

  Scenario: Create ship with zero player_id
    When I attempt to create a ship with player_id 0
    Then ship creation should fail with error "player_id must be positive"

  Scenario: Create ship with negative player_id
    When I attempt to create a ship with player_id -1
    Then ship creation should fail with error "player_id must be positive"

  Scenario: Create ship with negative fuel capacity
    When I attempt to create a ship with fuel_capacity -10
    Then ship creation should fail with error "fuel_capacity cannot be negative"

  Scenario: Create ship with mismatched fuel capacity
    When I attempt to create a ship with fuel object capacity 80 but fuel_capacity parameter 100
    Then ship creation should fail with error "fuel capacity must match fuel_capacity"

  Scenario: Create ship with negative cargo capacity
    When I attempt to create a ship with cargo_capacity -10
    Then ship creation should fail with error "cargo_capacity cannot be negative"

  Scenario: Create ship with negative cargo units
    When I attempt to create a ship with cargo_units -5
    Then ship creation should fail with error "cargo_units cannot be negative"

  Scenario: Create ship with cargo units exceeding capacity
    When I attempt to create a ship with cargo_capacity 40 and cargo_units 50
    Then ship creation should fail with error "cargo_units cannot exceed cargo_capacity"

  Scenario: Create ship with zero engine speed
    When I attempt to create a ship with engine_speed 0
    Then ship creation should fail with error "engine_speed must be positive"

  Scenario: Create ship with negative engine speed
    When I attempt to create a ship with engine_speed -10
    Then ship creation should fail with error "engine_speed must be positive"

  Scenario: Create ship with invalid nav status
    When I attempt to create a ship with nav_status "FLYING"
    Then ship creation should fail with error "invalid nav_status"

  # ============================================================================
  # Navigation State Machine Tests
  # ============================================================================

  Scenario: Depart from docked to in orbit
    Given a docked ship at "X1-A1"
    When the ship departs
    Then the ship should be in orbit
    And the ship should not be docked

  Scenario: Depart when in transit raises error
    Given a ship in transit to "X1-B2"
    When I attempt to depart the ship
    Then the operation should fail with error "cannot orbit while in transit"

  Scenario: Dock from in orbit to docked
    Given a ship in orbit at "X1-A1"
    When the ship docks
    Then the ship should be docked
    And the ship should not be in orbit

  Scenario: Dock when in transit raises error
    Given a ship in transit to "X1-B2"
    When I attempt to dock the ship
    Then the operation should fail with error "cannot dock while in transit"

  Scenario: Start transit from in orbit
    Given a ship in orbit at "X1-A1"
    When the ship starts transit to "X1-B2"
    Then the ship should be in transit
    And the ship should not be in orbit
    And the ship should be at location "X1-B2"

  Scenario: Start transit when not in orbit raises error
    Given a docked ship at "X1-A1"
    When I attempt to start transit to "X1-B2"
    Then the operation should fail with error "ship must be in orbit to start transit"

  Scenario: Arrive from in transit to in orbit
    Given a ship in transit to "X1-B2"
    When the ship arrives
    Then the ship should be in orbit
    And the ship should not be in transit

  Scenario: Arrive when not in transit raises error
    Given a docked ship at "X1-A1"
    When I attempt to arrive the ship
    Then the operation should fail with error "ship must be in transit to arrive"

  Scenario: Ensure in orbit from docked transitions to orbit
    Given a docked ship at "X1-A1"
    When I ensure the ship is in orbit
    Then the state change result should be true
    And the ship should be in orbit

  Scenario: Ensure in orbit when already in orbit returns false
    Given a ship in orbit at "X1-A1"
    When I ensure the ship is in orbit
    Then the state change result should be false
    And the ship should be in orbit

  Scenario: Ensure in orbit when in transit raises error
    Given a ship in transit to "X1-B2"
    When I attempt to ensure the ship is in orbit
    Then the operation should fail with error "cannot orbit while in transit"

  Scenario: Ensure docked from in orbit transitions to docked
    Given a ship in orbit at "X1-A1"
    When I ensure the ship is docked
    Then the state change result should be true
    And the ship should be docked

  Scenario: Ensure docked when already docked returns false
    Given a docked ship at "X1-A1"
    When I ensure the ship is docked
    Then the state change result should be false
    And the ship should be docked

  Scenario: Ensure docked when in transit raises error
    Given a ship in transit to "X1-B2"
    When I attempt to ensure the ship is docked
    Then the operation should fail with error "cannot dock while in transit"

  # ============================================================================
  # Fuel Management Tests
  # ============================================================================

  Scenario: Consume fuel reduces fuel amount
    Given a ship with 100 units of fuel
    When the ship consumes 30 units of fuel
    Then the ship should have 70 units of fuel

  Scenario: Consume fuel with zero amount
    Given a ship with 100 units of fuel
    When the ship consumes 0 units of fuel
    Then the ship should have 100 units of fuel

  Scenario: Consume fuel with negative amount raises error
    Given a ship with 100 units of fuel
    When I attempt to consume -10 units of fuel
    Then the operation should fail with error "fuel amount cannot be negative"

  Scenario: Consume fuel more than available raises error
    Given a ship with 100 units of fuel
    When I attempt to consume 150 units of fuel
    Then the operation should fail with error "insufficient fuel"

  Scenario: Consume all fuel
    Given a ship with 100 units of fuel
    When the ship consumes 100 units of fuel
    Then the ship should have 0 units of fuel

  Scenario: Refuel increases fuel amount
    Given a ship with 50 units of fuel and capacity 100
    When the ship refuels 30 units
    Then the ship should have 80 units of fuel

  Scenario: Refuel with zero amount
    Given a ship with 100 units of fuel
    When the ship refuels 0 units
    Then the ship should have 100 units of fuel

  Scenario: Refuel with negative amount raises error
    Given a ship with 100 units of fuel
    When I attempt to refuel -10 units
    Then the operation should fail with error "fuel amount cannot be negative"

  Scenario: Refuel caps at capacity
    Given a ship with 50 units of fuel and capacity 100
    When the ship refuels 80 units
    Then the ship should have 100 units of fuel

  Scenario: Refuel to full when partially filled
    Given a ship with 50 units of fuel and capacity 100
    When the ship refuels to full
    Then the fuel added should be 50 units
    And the ship should have 100 units of fuel

  Scenario: Refuel to full when already full
    Given a ship with 100 units of fuel and capacity 100
    When the ship refuels to full
    Then the fuel added should be 0 units
    And the ship should have 100 units of fuel

  # ============================================================================
  # Navigation Calculation Tests
  # ============================================================================

  Scenario: Can navigate to nearby destination with enough fuel
    Given a ship at "X1-A1" with coordinates (0, 0) and 100 units of fuel
    And a waypoint "X1-B2" at coordinates (100, 0)
    When I check if the ship can navigate to "X1-B2"
    Then the result should be true

  Scenario: Cannot navigate to destination without enough fuel
    Given a ship at "X1-A1" with coordinates (0, 0) and 0 units of fuel
    And a waypoint "X1-C3" at coordinates (200, 0)
    When I check if the ship can navigate to "X1-C3"
    Then the result should be false

  Scenario: Can navigate to same location
    Given a ship at "X1-A1" with coordinates (0, 0) and 100 units of fuel
    When I check if the ship can navigate to "X1-A1"
    Then the result should be true

  Scenario: Calculate fuel for trip with cruise mode
    Given a ship at "X1-A1" with coordinates (0, 0)
    And a waypoint "X1-B2" at coordinates (100, 0)
    When I calculate fuel required to "X1-B2" with CRUISE mode
    Then the fuel required should be 100 units

  Scenario: Calculate fuel for trip with drift mode
    Given a ship at "X1-A1" with coordinates (0, 0)
    And a waypoint "X1-B2" at coordinates (100, 0)
    When I calculate fuel required to "X1-B2" with DRIFT mode
    Then the fuel required should be 1 units

  Scenario: Calculate fuel for trip with burn mode
    Given a ship at "X1-A1" with coordinates (0, 0)
    And a waypoint "X1-B2" at coordinates (100, 0)
    When I calculate fuel required to "X1-B2" with BURN mode
    Then the fuel required should be 200 units

  Scenario: Needs refuel for journey with low fuel
    Given a ship at "X1-A1" with coordinates (0, 0) and 10 units of fuel
    And a waypoint "X1-C3" at coordinates (200, 0)
    When I check if the ship needs refuel for journey to "X1-C3"
    Then the result should be true

  Scenario: Does not need refuel with enough fuel and zero safety margin
    Given a ship at "X1-A1" with coordinates (0, 0) and 100 units of fuel
    And a waypoint "X1-B2" at coordinates (100, 0)
    When I check if the ship needs refuel for journey to "X1-B2" with safety margin 0.0
    Then the result should be false

  Scenario: Calculate travel time with cruise mode
    Given a ship at "X1-A1" with coordinates (0, 0) and engine speed 30
    And a waypoint "X1-B2" at coordinates (100, 0)
    When I calculate travel time to "X1-B2" with CRUISE mode
    Then the travel time should be 103 seconds

  Scenario: Calculate travel time with drift mode
    Given a ship at "X1-A1" with coordinates (0, 0) and engine speed 30
    And a waypoint "X1-B2" at coordinates (100, 0)
    When I calculate travel time to "X1-B2" with DRIFT mode
    Then the travel time should be 86 seconds

  Scenario: Calculate travel time with burn mode
    Given a ship at "X1-A1" with coordinates (0, 0) and engine speed 30
    And a waypoint "X1-B2" at coordinates (100, 0)
    When I calculate travel time to "X1-B2" with BURN mode
    Then the travel time should be 50 seconds

  Scenario: Select optimal flight mode with high fuel
    Given a ship with 250 units of fuel at distance 100
    When I select optimal flight mode for distance 100
    Then the ship's selected mode should be BURN

  Scenario: Select optimal flight mode with low fuel
    Given a ship with 50 units of fuel at distance 100
    When I select optimal flight mode for distance 100
    Then the ship's selected mode should be DRIFT

  Scenario: Select optimal flight mode with medium fuel
    Given a ship with 110 units of fuel at distance 100
    When I select optimal flight mode for distance 100
    Then the ship's selected mode should be CRUISE

  # ============================================================================
  # Cargo Management Tests
  # ============================================================================

  Scenario: Has cargo space with empty cargo
    Given a ship with cargo capacity 40 and cargo units 0
    When I check if the ship has cargo space for 1 units
    Then the result should be true

  Scenario: Has cargo space with partial cargo
    Given a ship with cargo capacity 40 and cargo units 20
    When I check if the ship has cargo space for 1 units
    Then the result should be true

  Scenario: Has cargo space with full cargo
    Given a ship with cargo capacity 40 and cargo units 40
    When I check if the ship has cargo space for 1 units
    Then the result should be false

  Scenario: Has cargo space with specific units
    Given a ship with cargo capacity 40 and cargo units 35
    When I check if the ship has cargo space for 5 units
    Then the result should be true

  Scenario: Has cargo space with specific units exceeding available
    Given a ship with cargo capacity 40 and cargo units 35
    When I check if the ship has cargo space for 6 units
    Then the result should be false

  Scenario: Available cargo space with empty cargo
    Given a ship with cargo capacity 40 and cargo units 0
    When I check available cargo space
    Then the available space should be 40 units

  Scenario: Available cargo space with partial cargo
    Given a ship with cargo capacity 40 and cargo units 25
    When I check available cargo space
    Then the available space should be 15 units

  Scenario: Available cargo space with full cargo
    Given a ship with cargo capacity 40 and cargo units 40
    When I check available cargo space
    Then the available space should be 0 units

  Scenario: Is cargo empty when empty
    Given a ship with cargo capacity 40 and cargo units 0
    When I check if cargo is empty
    Then the result should be true

  Scenario: Is cargo empty when not empty
    Given a ship with cargo capacity 40 and cargo units 1
    When I check if cargo is empty
    Then the result should be false

  Scenario: Is cargo full when full
    Given a ship with cargo capacity 40 and cargo units 40
    When I check if cargo is full
    Then the result should be true

  Scenario: Is cargo full when not full
    Given a ship with cargo capacity 40 and cargo units 0
    When I check if cargo is full
    Then the result should be false

  # ============================================================================
  # State Query Tests
  # ============================================================================

  Scenario: Is docked when docked
    Given a docked ship at "X1-A1"
    When I check if the ship is docked
    Then the result should be true

  Scenario: Is docked when in orbit
    Given a ship in orbit at "X1-A1"
    When I check if the ship is docked
    Then the result should be false

  Scenario: Is in orbit when in orbit
    Given a ship in orbit at "X1-A1"
    When I check if the ship is in orbit
    Then the result should be true

  Scenario: Is in orbit when docked
    Given a docked ship at "X1-A1"
    When I check if the ship is in orbit
    Then the result should be false

  Scenario: Is in transit when in transit
    Given a ship in transit to "X1-B2"
    When I check if the ship is in transit
    Then the result should be true

  Scenario: Is in transit when docked
    Given a docked ship at "X1-A1"
    When I check if the ship is in transit
    Then the result should be false

  Scenario: Is at location when at location
    Given a ship at "X1-A1"
    When I check if the ship is at location "X1-A1"
    Then the result should be true

  Scenario: Is at location when not at location
    Given a ship at "X1-A1"
    When I check if the ship is at location "X1-B2"
    Then the result should be false

  # ============================================================================
  # Refueling Decision Tests
  # ============================================================================

  Scenario: Should refuel opportunistically when at fuel station with low fuel
    Given a ship at "X1-A1" with 30 units of fuel and capacity 100
    And waypoint "X1-A1" has trait "MARKETPLACE" and fuel available
    When I check if ship should refuel opportunistically at "X1-A1" with threshold 0.5
    Then the result should be true

  Scenario: Should not refuel opportunistically when fuel above threshold
    Given a ship at "X1-A1" with 60 units of fuel and capacity 100
    And waypoint "X1-A1" has trait "MARKETPLACE" and fuel available
    When I check if ship should refuel opportunistically at "X1-A1" with threshold 0.5
    Then the result should be false

  Scenario: Should not refuel opportunistically when no fuel available at waypoint
    Given a ship at "X1-A1" with 30 units of fuel and capacity 100
    And waypoint "X1-A1" has no fuel available
    When I check if ship should refuel opportunistically at "X1-A1" with threshold 0.5
    Then the result should be false

  Scenario: Should prevent drift mode when fuel below threshold
    Given a ship with 40 units of fuel and capacity 100
    And a route segment requiring 50 units of fuel in DRIFT mode
    When I check if ship should prevent drift mode with threshold 0.5
    Then the result should be true

  Scenario: Should not prevent drift mode when fuel above threshold
    Given a ship with 60 units of fuel and capacity 100
    And a route segment requiring 50 units of fuel in DRIFT mode
    When I check if ship should prevent drift mode with threshold 0.5
    Then the result should be false

  Scenario: Should not prevent drift mode when not using drift flight mode
    Given a ship with 40 units of fuel and capacity 100
    And a route segment requiring 50 units of fuel in CRUISE mode
    When I check if ship should prevent drift mode with threshold 0.5
    Then the result should be false

  # ============================================================================
  # Additional Edge Cases for Increased Coverage
  # ============================================================================

  Scenario: Refuel ship with zero fuel capacity (probe)
    Given a ship with 0 units of fuel and capacity 0
    When the ship refuels to full
    Then the fuel added should be 0 units
    And the ship should have 0 units of fuel

  Scenario: Ship with zero cargo capacity cannot load cargo
    Given a ship with cargo capacity 0 and cargo units 0
    When I check if the ship has cargo space for 1 units
    Then the result should be false

  Scenario: Consume exactly all remaining fuel
    Given a ship with 50 units of fuel
    When the ship consumes 50 units of fuel
    Then the ship should have 0 units of fuel

  Scenario: Cannot consume more fuel than available by 1 unit
    Given a ship with 10 units of fuel
    When I attempt to consume 11 units of fuel
    Then the operation should fail with error "insufficient fuel"
    And the ship should have 10 units of fuel

  Scenario: Ship at exact refuel threshold boundary (low side)
    Given a ship at "X1-A1" with 49 units of fuel and capacity 100
    And waypoint "X1-A1" has trait "MARKETPLACE" and fuel available
    When I check if ship should refuel opportunistically at "X1-A1" with threshold 0.5
    Then the result should be true

  Scenario: Ship at exact refuel threshold boundary (high side)
    Given a ship at "X1-A1" with 50 units of fuel and capacity 100
    And waypoint "X1-A1" has trait "MARKETPLACE" and fuel available
    When I check if ship should refuel opportunistically at "X1-A1" with threshold 0.5
    Then the result should be false

  Scenario: Check cargo space for zero units always returns true
    Given a ship with cargo capacity 40 and cargo units 40
    When I check if the ship has cargo space for 0 units
    Then the result should be true

  Scenario: Calculate fuel for zero distance travel
    Given a ship at "X1-A1" with coordinates (0, 0)
    When I calculate fuel required to "X1-A1" with CRUISE mode
    Then the fuel required should be 0 units

  Scenario: Ship needs refuel when fuel exactly equals required amount
    Given a ship at "X1-A1" with coordinates (0, 0) and 100 units of fuel
    And a waypoint "X1-B2" at coordinates (100, 0)
    When I check if the ship needs refuel for journey to "X1-B2" with safety margin 0.0
    Then the result should be false

  Scenario: Ship needs refuel with small safety margin
    Given a ship at "X1-A1" with coordinates (0, 0) and 100 units of fuel
    And a waypoint "X1-B2" at coordinates (100, 0)
    When I check if the ship needs refuel for journey to "X1-B2" with safety margin 0.1
    Then the result should be true

  Scenario: Prevent drift mode at exact threshold boundary
    Given a ship with 50 units of fuel and capacity 100
    And a route segment requiring 40 units of fuel in DRIFT mode
    When I check if ship should prevent drift mode with threshold 0.5
    Then the result should be false

  Scenario: Update ship location during transit
    Given a ship in orbit at "X1-A1"
    When the ship starts transit to "X1-B2"
    Then the ship should be at location "X1-B2"
    And the ship should be in transit

  Scenario: Cannot start transit to same location
    Given a ship in orbit at "X1-A1"
    When I attempt to start transit to "X1-A1"
    Then the operation should fail with error "cannot transit to same location"
  # ============================================================================
  # Ship Frame Type Detection Tests
  # ============================================================================

  Scenario: IsProbe returns true for probe frame
    Given a ship with frame symbol "FRAME_PROBE"
    When I check if the ship is a probe
    Then the result should be true

  Scenario: IsProbe returns false for non-probe frame
    Given a ship with frame symbol "FRAME_MINER"
    When I check if the ship is a probe
    Then the result should be false

  Scenario: IsDrone returns true for drone frame
    Given a ship with frame symbol "FRAME_DRONE"
    When I check if the ship is a drone
    Then the result should be true

  Scenario: IsDrone returns false for non-drone frame
    Given a ship with frame symbol "FRAME_FRIGATE"
    When I check if the ship is a drone
    Then the result should be false

  Scenario: IsScoutType returns true for satellite role
    Given a ship with role "SATELLITE"
    When I check if the ship is a scout type
    Then the result should be true

  Scenario: IsScoutType returns false for excavator role
    Given a ship with role "EXCAVATOR"
    When I check if the ship is a scout type
    Then the result should be false

  Scenario: IsScoutType returns false for hauler role
    Given a ship with role "HAULER"
    When I check if the ship is a scout type
    Then the result should be false

  # ============================================================================
  # Cargo State Query Tests
  # ============================================================================

  Scenario: IsCargoEmpty returns true for ship with no cargo
    Given a ship with cargo capacity 40 and cargo units 0
    When I check if cargo is empty
    Then the result should be true

  Scenario: IsCargoEmpty returns false for ship with cargo
    Given a ship with cargo capacity 40 and cargo units 20
    When I check if cargo is empty
    Then the result should be false

  Scenario: IsCargoFull returns true for ship at capacity
    Given a ship with cargo capacity 40 and cargo units 40
    When I check if cargo is full
    Then the result should be true

  Scenario: IsCargoFull returns false for ship with available space
    Given a ship with cargo capacity 40 and cargo units 20
    When I check if cargo is full
    Then the result should be false

  # ============================================================================
  # Clone At Location Tests
  # ============================================================================

  Scenario: Clone ship at different location with specified fuel
    Given a ship in orbit at "X1-A1" with 100 units of fuel
    And a waypoint "X1-B2" at coordinates (50, 50)
    When I clone the ship at location "X1-B2" with 60 units of fuel
    Then the cloned ship should be at location "X1-B2"
    And the cloned ship should have 60 units of fuel
    And the cloned ship should be in orbit
    And the cloned ship should have same ship symbol as original
    And the cloned ship should have same cargo capacity as original

  Scenario: Clone ship preserves ship properties
    Given a ship with symbol "PROBE-1" with frame "FRAME_PROBE" and role "SATELLITE"
    And a waypoint "X1-DEST" at coordinates (100, 100)
    When I clone the ship at location "X1-DEST" with 40 units of fuel
    Then the cloned ship should have ship symbol "PROBE-1"
    And the cloned ship should have frame symbol "FRAME_PROBE"
    And the cloned ship should have role "SATELLITE"

