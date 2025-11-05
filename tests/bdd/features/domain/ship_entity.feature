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

  Scenario: Create ship with default nav status
    When I create a ship with symbol "SHIP-2", player 1, at "X1-A1", fuel 100/100, cargo 0/40, speed 30
    Then the ship should be in orbit

  Scenario: Create ship trims ship symbol whitespace
    When I create a ship with ship_symbol "  SHIP-3  "
    Then the ship should have symbol "SHIP-3"

  Scenario: Create ship with empty ship symbol raises error
    When I attempt to create a ship with empty ship_symbol
    Then ship creation should fail with InvalidShipDataError matching "ship_symbol cannot be empty"

  Scenario: Create ship with whitespace only ship symbol raises error
    When I attempt to create a ship with ship_symbol "   "
    Then ship creation should fail with InvalidShipDataError matching "ship_symbol cannot be empty"

  Scenario: Create ship with zero player_id raises error
    When I attempt to create a ship with player_id 0
    Then ship creation should fail with InvalidShipDataError matching "player_id must be positive"

  Scenario: Create ship with negative player_id raises error
    When I attempt to create a ship with player_id -1
    Then ship creation should fail with InvalidShipDataError matching "player_id must be positive"

  Scenario: Create ship with zero fuel capacity succeeds
    When I create a ship with fuel_capacity 0 and fuel 0/0
    Then the ship fuel capacity should be 0
    And the ship should have 0 units of fuel

  Scenario: Create ship with negative fuel capacity raises error
    When I attempt to create a ship with fuel_capacity -10
    Then ship creation should fail with InvalidShipDataError matching "fuel_capacity cannot be negative"

  Scenario: Create ship with mismatched fuel capacity raises error
    When I attempt to create a ship with fuel object capacity 80 but fuel_capacity parameter 100
    Then ship creation should fail with InvalidShipDataError matching "fuel capacity must match fuel_capacity"

  Scenario: Create ship with negative cargo capacity raises error
    When I attempt to create a ship with cargo_capacity -10
    Then ship creation should fail with InvalidShipDataError matching "cargo_capacity cannot be negative"

  Scenario: Create ship with zero cargo capacity succeeds
    When I create a ship with cargo_capacity 0
    Then the ship cargo capacity should be 0

  Scenario: Create ship with negative cargo units raises error
    When I attempt to create a ship with cargo_units -5
    Then ship creation should fail with InvalidShipDataError matching "cargo_units cannot be negative"

  Scenario: Create ship with cargo units exceeding capacity raises error
    When I attempt to create a ship with cargo_capacity 40 and cargo_units 50
    Then ship creation should fail with InvalidShipDataError matching "cargo_units cannot exceed cargo_capacity"

  Scenario: Create ship with zero engine speed raises error
    When I attempt to create a ship with engine_speed 0
    Then ship creation should fail with InvalidShipDataError matching "engine_speed must be positive"

  Scenario: Create ship with negative engine speed raises error
    When I attempt to create a ship with engine_speed -10
    Then ship creation should fail with InvalidShipDataError matching "engine_speed must be positive"

  Scenario: Create ship with invalid nav status raises error
    When I attempt to create a ship with nav_status "FLYING"
    Then ship creation should fail with InvalidShipDataError matching "nav_status must be one of"

  # ============================================================================
  # Navigation State Machine Tests
  # ============================================================================

  Scenario: Depart from docked to in orbit
    Given a docked ship at "X1-A1"
    When the ship departs
    Then the ship should be in orbit
    And the ship should not be docked

  Scenario: Depart when already in orbit is noop
    Given a ship in orbit at "X1-A1"
    When the ship departs
    Then the ship should be in orbit

  Scenario: Depart when in transit raises error
    Given a ship in transit to "X1-B2"
    When I attempt to depart the ship
    Then the operation should fail with InvalidNavStatusError matching "Cannot dock while in transit"

  Scenario: Dock from in orbit to docked
    Given a ship in orbit at "X1-A1"
    When the ship docks
    Then the ship should be docked
    And the ship should not be in orbit

  Scenario: Dock when already docked is noop
    Given a docked ship at "X1-A1"
    When the ship docks
    Then the ship should be docked

  Scenario: Dock when in transit raises error
    Given a ship in transit to "X1-B2"
    When I attempt to dock the ship
    Then the operation should fail with InvalidNavStatusError matching "Cannot orbit while in transit"

  Scenario: Start transit from in orbit to in transit
    Given a ship in orbit at "X1-A1"
    When the ship starts transit to "X1-B2"
    Then the ship should be in transit
    And the ship should not be in orbit
    And the ship should be at location "X1-B2"

  Scenario: Start transit from docked transitions via orbit
    Given a docked ship at "X1-A1"
    When the ship starts transit to "X1-B2"
    Then the ship should be in transit
    And the ship should be at location "X1-B2"

  Scenario: Start transit when already in transit raises error
    Given a ship in transit to "X1-B2"
    When I attempt to start transit to "X1-C3"
    Then the operation should fail with InvalidNavStatusError matching "Cannot orbit while in transit"

  Scenario: Arrive from in transit to in orbit
    Given a ship in transit to "X1-B2"
    When the ship arrives
    Then the ship should be in orbit
    And the ship should not be in transit

  Scenario: Arrive when docked raises error
    Given a docked ship at "X1-A1"
    When I attempt to arrive the ship
    Then the operation should fail with InvalidNavStatusError matching "Ship must be in transit to arrive"

  Scenario: Arrive when in orbit raises error
    Given a ship in orbit at "X1-A1"
    When I attempt to arrive the ship
    Then the operation should fail with InvalidNavStatusError matching "Ship must be in transit to arrive"

  Scenario: Ensure in orbit from docked transitions to orbit
    Given a docked ship at "X1-A1"
    When I ensure the ship is in orbit
    Then the result should be True
    And the ship should be in orbit

  Scenario: Ensure in orbit when already in orbit returns false
    Given a ship in orbit at "X1-A1"
    When I ensure the ship is in orbit
    Then the result should be False
    And the ship should be in orbit

  Scenario: Ensure in orbit when in transit raises error
    Given a ship in transit to "X1-B2"
    When I attempt to ensure the ship is in orbit
    Then the operation should fail with InvalidNavStatusError matching "Cannot orbit while in transit"

  Scenario: Ensure docked from in orbit transitions to docked
    Given a ship in orbit at "X1-A1"
    When I ensure the ship is docked
    Then the result should be True
    And the ship should be docked

  Scenario: Ensure docked when already docked returns false
    Given a docked ship at "X1-A1"
    When I ensure the ship is docked
    Then the result should be False
    And the ship should be docked

  Scenario: Ensure docked when in transit raises error
    Given a ship in transit to "X1-B2"
    When I attempt to ensure the ship is docked
    Then the operation should fail with InvalidNavStatusError matching "Cannot dock while in transit"

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
    Then the operation should fail with ValueError matching "fuel amount cannot be negative"

  Scenario: Consume fuel more than available raises error
    Given a ship with 100 units of fuel
    When I attempt to consume 150 units of fuel
    Then the operation should fail with InsufficientFuelError matching "Insufficient fuel"

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
    Then the operation should fail with ValueError matching "fuel amount cannot be negative"

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
    Given a ship at "X1-A1" with 100 units of fuel
    When I check if the ship can navigate to "X1-B2"
    Then the result should be True

  Scenario: Can navigate to distant destination with enough fuel
    Given a ship at "X1-A1" with 100 units of fuel
    When I check if the ship can navigate to "X1-C3"
    Then the result should be True

  Scenario: Cannot navigate to destination without enough fuel
    Given a ship at "X1-A1" with 0 units of fuel
    When I check if the ship can navigate to "X1-C3"
    Then the result should be False

  Scenario: Can navigate to same location
    Given a ship at "X1-A1" with 100 units of fuel
    When I check if the ship can navigate to "X1-A1"
    Then the result should be True

  Scenario: Calculate fuel for trip with cruise mode
    Given a ship at "X1-A1"
    When I calculate fuel required to "X1-B2" with CRUISE mode
    Then the fuel required should be 100 units

  Scenario: Calculate fuel for trip with drift mode
    Given a ship at "X1-A1"
    When I calculate fuel required to "X1-B2" with DRIFT mode
    Then the fuel required should be 1 units

  Scenario: Calculate fuel for trip with burn mode
    Given a ship at "X1-A1"
    When I calculate fuel required to "X1-B2" with BURN mode
    Then the fuel required should be 200 units

  Scenario: Calculate fuel for trip with stealth mode
    Given a ship at "X1-A1"
    When I calculate fuel required to "X1-B2" with STEALTH mode
    Then the fuel required should be 100 units

  Scenario: Needs refuel for journey with low fuel
    Given a ship at "X1-A1" with 10 units of fuel and capacity 100
    When I check if the ship needs refuel for journey to "X1-C3"
    Then the result should be True

  Scenario: Needs refuel for journey with enough fuel
    Given a ship at "X1-A1" with 100 units of fuel and capacity 100
    When I check if the ship needs refuel for journey to "X1-B2"
    Then the result should be True

  Scenario: Needs refuel for journey with custom safety margin
    Given a ship at "X1-A1" with 100 units of fuel and capacity 100
    When I check if the ship needs refuel for journey to "X1-B2" with safety margin 0.0
    Then the result should be False

  Scenario: Calculate travel time with cruise mode
    Given a ship at "X1-A1" with engine speed 30
    When I calculate travel time to "X1-B2" with CRUISE mode
    Then the travel time should be 103 seconds

  Scenario: Calculate travel time with drift mode
    Given a ship at "X1-A1" with engine speed 30
    When I calculate travel time to "X1-B2" with DRIFT mode
    Then the travel time should be 86 seconds

  Scenario: Calculate travel time with burn mode
    Given a ship at "X1-A1" with engine speed 30
    When I calculate travel time to "X1-B2" with BURN mode
    Then the travel time should be 50 seconds

  Scenario: Calculate travel time with stealth mode
    Given a ship at "X1-A1" with engine speed 30
    When I calculate travel time to "X1-B2" with STEALTH mode
    Then the travel time should be 166 seconds

  Scenario: Select optimal flight mode with high fuel
    Given a ship with 100 units of fuel and capacity 100
    When I select optimal flight mode
    Then the selected mode should be BURN

  Scenario: Select optimal flight mode with low fuel
    Given a ship with 10 units of fuel and capacity 100
    When I select optimal flight mode
    Then the selected mode should be DRIFT

  Scenario: Select optimal flight mode with medium fuel
    Given a ship with 50 units of fuel and capacity 100
    When I select optimal flight mode
    Then the selected mode should be CRUISE

  # ============================================================================
  # Cargo Management Tests
  # ============================================================================

  Scenario: Has cargo space with empty cargo
    Given a ship with cargo capacity 40 and cargo units 0
    When I check if the ship has cargo space
    Then the result should be True

  Scenario: Has cargo space with partial cargo
    Given a ship with cargo capacity 40 and cargo units 20
    When I check if the ship has cargo space
    Then the result should be True

  Scenario: Has cargo space with full cargo
    Given a ship with cargo capacity 40 and cargo units 40
    When I check if the ship has cargo space
    Then the result should be False

  Scenario: Has cargo space with specific units
    Given a ship with cargo capacity 40 and cargo units 35
    When I check if the ship has cargo space for 5 units
    Then the result should be True

  Scenario: Has cargo space with specific units exceeding available
    Given a ship with cargo capacity 40 and cargo units 35
    When I check if the ship has cargo space for 6 units
    Then the result should be False

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
    Then the result should be True

  Scenario: Is cargo empty when not empty
    Given a ship with cargo capacity 40 and cargo units 1
    When I check if cargo is empty
    Then the result should be False

  Scenario: Is cargo full when full
    Given a ship with cargo capacity 40 and cargo units 40
    When I check if cargo is full
    Then the result should be True

  Scenario: Is cargo full when not full
    Given a ship with cargo capacity 40 and cargo units 0
    When I check if cargo is full
    Then the result should be False

  # ============================================================================
  # State Query Tests
  # ============================================================================

  Scenario: Is docked when docked
    Given a docked ship at "X1-A1"
    When I check if the ship is docked
    Then the result should be True

  Scenario: Is docked when in orbit
    Given a ship in orbit at "X1-A1"
    When I check if the ship is docked
    Then the result should be False

  Scenario: Is docked when in transit
    Given a ship in transit to "X1-B2"
    When I check if the ship is docked
    Then the result should be False

  Scenario: Is in orbit when in orbit
    Given a ship in orbit at "X1-A1"
    When I check if the ship is in orbit
    Then the result should be True

  Scenario: Is in orbit when docked
    Given a docked ship at "X1-A1"
    When I check if the ship is in orbit
    Then the result should be False

  Scenario: Is in orbit when in transit
    Given a ship in transit to "X1-B2"
    When I check if the ship is in orbit
    Then the result should be False

  Scenario: Is in transit when in transit
    Given a ship in transit to "X1-B2"
    When I check if the ship is in transit
    Then the result should be True

  Scenario: Is in transit when docked
    Given a docked ship at "X1-A1"
    When I check if the ship is in transit
    Then the result should be False

  Scenario: Is in transit when in orbit
    Given a ship in orbit at "X1-A1"
    When I check if the ship is in transit
    Then the result should be False

  Scenario: Is at location when at location
    Given a ship at "X1-A1"
    When I check if the ship is at location "X1-A1"
    Then the result should be True

  Scenario: Is at location when not at location
    Given a ship at "X1-A1"
    When I check if the ship is at location "X1-B2"
    Then the result should be False

  # ============================================================================
  # Equality and Hashing Tests
  # ============================================================================

  Scenario: Ships with same symbol and player are equal
    Given two ships with symbol "SHIP-1" and player_id 1 with different attributes
    When I compare the ships for equality
    Then the ships should be equal

  Scenario: Ships with different symbols are not equal
    Given a ship with symbol "SHIP-1" and player_id 1
    And a ship with symbol "SHIP-2" and player_id 1
    When I compare the ships for equality
    Then the ships should not be equal

  Scenario: Ships with different players are not equal
    Given a ship with symbol "SHIP-1" and player_id 1
    And a ship with symbol "SHIP-1" and player_id 2
    When I compare the ships for equality
    Then the ships should not be equal

  Scenario: Ship not equal to string
    Given a ship with symbol "SHIP-1"
    When I compare the ship to string "SHIP-1"
    Then they should not be equal

  Scenario: Ship not equal to integer
    Given a ship with symbol "SHIP-1"
    When I compare the ship to integer 123
    Then they should not be equal

  Scenario: Ship not equal to None
    Given a ship with symbol "SHIP-1"
    When I compare the ship to None
    Then they should not be equal

  Scenario: Equal ships have same hash
    Given two ships with symbol "SHIP-1" and player_id 1 with different attributes
    When I compute the hash of both ships
    Then the hashes should be equal

  Scenario: Ships can be used in set
    Given three ships: "SHIP-1" player 1, "SHIP-1" player 1, "SHIP-2" player 1
    When I add the ships to a set
    Then the set should contain 2 ships

  # ============================================================================
  # Repr Tests
  # ============================================================================

  Scenario: Repr contains ship info
    Given a ship with symbol "SHIP-1" at "X1-A1" with status "IN_ORBIT" and fuel "100/100"
    When I get the repr of the ship
    Then the repr should contain "SHIP-1"
    And the repr should contain "X1-A1"
    And the repr should contain "IN_ORBIT"
    And the repr should contain fuel information
