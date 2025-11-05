Feature: Domain Value Objects
  As a space trader system
  I want immutable value objects for waypoints, fuel, flight modes, and distances
  So that I can safely represent domain concepts throughout the application

  Background:
    Given the value objects system is initialized

  # ============================================================================
  # Waypoint Value Object Tests (14 scenarios)
  # ============================================================================

  Scenario: Create waypoint with required fields
    Given a waypoint with symbol "X1-A1" at coordinates (100.0, 200.0)
    Then the waypoint symbol should be "X1-A1"
    And the waypoint x coordinate should be 100.0
    And the waypoint y coordinate should be 200.0

  Scenario: Create waypoint with system symbol
    Given a waypoint with symbol "X1-A1" at coordinates (100.0, 200.0) and system "X1"
    Then the waypoint system symbol should be "X1"

  Scenario: Create waypoint with type
    Given a waypoint with symbol "X1-A1" at coordinates (100.0, 200.0) and type "PLANET"
    Then the waypoint type should be "PLANET"

  Scenario: Create waypoint with traits
    Given a waypoint with symbol "X1-A1" at coordinates (100.0, 200.0) and traits "MARKETPLACE,SHIPYARD"
    Then the waypoint should have traits "MARKETPLACE" and "SHIPYARD"

  Scenario: Create waypoint with fuel
    Given a waypoint with symbol "X1-A1" at coordinates (100.0, 200.0) and fuel available
    Then the waypoint should have fuel

  Scenario: Create waypoint with orbitals
    Given a waypoint with symbol "X1-A1" at coordinates (100.0, 200.0) and orbital "X1-A1-MOON"
    Then the waypoint should have orbital "X1-A1-MOON"

  Scenario: Waypoint is immutable
    Given a waypoint with symbol "X1-A1" at coordinates (100.0, 200.0)
    When I attempt to modify the waypoint symbol to "X1-A2"
    Then the modification should be rejected

  Scenario: Calculate distance between waypoints
    Given a waypoint "A" with symbol "X1-A1" at coordinates (0.0, 0.0)
    And a waypoint "B" with symbol "X1-B2" at coordinates (3.0, 4.0)
    When I calculate the distance from waypoint "A" to waypoint "B"
    Then the distance should be 5.0 units

  Scenario: Distance calculation is symmetric
    Given a waypoint "A" with symbol "X1-A1" at coordinates (10.0, 20.0)
    And a waypoint "B" with symbol "X1-B2" at coordinates (30.0, 50.0)
    When I calculate the distance from waypoint "A" to waypoint "B"
    And I calculate the distance from waypoint "B" to waypoint "A"
    Then both distances should be equal

  Scenario: Distance to self is zero
    Given a waypoint with symbol "X1-A1" at coordinates (100.0, 200.0)
    When I calculate the distance from the waypoint to itself
    Then the distance should be 0.0 units

  Scenario: Check if waypoint is orbital of another
    Given a waypoint "planet" with symbol "X1-PLANET" at coordinates (0.0, 0.0) with orbital "X1-MOON"
    And a waypoint "moon" with symbol "X1-MOON" at coordinates (10.0, 0.0)
    When I check if waypoint "moon" is an orbital of waypoint "planet"
    Then the orbital relationship should be true
    And the orbital relationship should be symmetric

  Scenario: Waypoints not orbitally related
    Given a waypoint "A" with symbol "X1-A1" at coordinates (0.0, 0.0)
    And a waypoint "B" with symbol "X1-B2" at coordinates (100.0, 0.0)
    When I check if waypoint "A" is an orbital of waypoint "B"
    Then the orbital relationship should be false

  Scenario: Waypoint repr shows symbol
    Given a waypoint with symbol "X1-TEST" at coordinates (0.0, 0.0)
    When I get the string representation of the waypoint
    Then the representation should contain "X1-TEST"

  # ============================================================================
  # Fuel Value Object Tests (26 scenarios)
  # ============================================================================

  Scenario: Create fuel with valid values
    Given fuel with 50 current and 100 capacity
    Then the current fuel should be 50
    And the fuel capacity should be 100

  Scenario: Fuel is immutable
    Given fuel with 50 current and 100 capacity
    When I attempt to modify the current fuel to 60
    Then the modification should be rejected

  Scenario: Reject negative current fuel
    When I attempt to create fuel with -10 current and 100 capacity
    Then fuel creation should fail with error "current fuel cannot be negative"

  Scenario: Reject negative capacity
    When I attempt to create fuel with 0 current and -10 capacity
    Then fuel creation should fail with error "fuel capacity cannot be negative"

  Scenario: Reject current exceeding capacity
    When I attempt to create fuel with 150 current and 100 capacity
    Then fuel creation should fail with error "current fuel cannot exceed capacity"

  Scenario: Allow full fuel
    Given fuel with 100 current and 100 capacity
    Then the current fuel should be 100
    And the fuel capacity should be 100

  Scenario: Allow empty fuel
    Given fuel with 0 current and 100 capacity
    Then the current fuel should be 0

  Scenario: Calculate fuel percentage
    Given fuel with 50 current and 100 capacity
    When I calculate the fuel percentage
    Then the percentage should be 50.0%

  Scenario: Percentage at full capacity
    Given fuel with 100 current and 100 capacity
    When I calculate the fuel percentage
    Then the percentage should be 100.0%

  Scenario: Percentage at empty
    Given fuel with 0 current and 100 capacity
    When I calculate the fuel percentage
    Then the percentage should be 0.0%

  Scenario: Percentage with zero capacity
    Given fuel with 0 current and 0 capacity
    When I calculate the fuel percentage
    Then the percentage should be 0.0%

  Scenario: Consume fuel returns new fuel object
    Given fuel with 100 current and 100 capacity
    When I consume 30 units of fuel
    Then the new fuel should have 70 current
    And the new fuel should have 100 capacity
    And the original fuel should have 100 current

  Scenario: Consume more than available goes to zero
    Given fuel with 20 current and 100 capacity
    When I consume 50 units of fuel
    Then the new fuel should have 0 current

  Scenario: Consume zero returns same values
    Given fuel with 50 current and 100 capacity
    When I consume 0 units of fuel
    Then the new fuel should have 50 current

  Scenario: Consume rejects negative amount
    Given fuel with 50 current and 100 capacity
    When I attempt to consume -10 units of fuel
    Then the consumption should fail with error "consume amount cannot be negative"

  Scenario: Add fuel returns new fuel object
    Given fuel with 30 current and 100 capacity
    When I add 20 units of fuel
    Then the new fuel should have 50 current
    And the original fuel should have 30 current

  Scenario: Add caps at capacity
    Given fuel with 90 current and 100 capacity
    When I add 20 units of fuel
    Then the new fuel should have 100 current

  Scenario: Add zero returns same values
    Given fuel with 50 current and 100 capacity
    When I add 0 units of fuel
    Then the new fuel should have 50 current

  Scenario: Add rejects negative amount
    Given fuel with 50 current and 100 capacity
    When I attempt to add -10 units of fuel
    Then the addition should fail with error "add amount cannot be negative"

  Scenario: Can travel with sufficient fuel
    Given fuel with 100 current and 100 capacity
    When I check if I can travel with 80 units required
    Then travel should be possible

  Scenario: Cannot travel with insufficient fuel
    Given fuel with 50 current and 100 capacity
    When I check if I can travel with 60 units required
    Then travel should not be possible

  Scenario: Can travel includes safety margin
    Given fuel with 100 current and 100 capacity
    When I check if I can travel with 90 units required and 0.1 safety margin
    Then travel should be possible
    When I check if I can travel with 95 units required and 0.1 safety margin
    Then travel should not be possible

  Scenario: Can travel with zero safety margin
    Given fuel with 100 current and 100 capacity
    When I check if I can travel with 100 units required and 0.0 safety margin
    Then travel should be possible

  Scenario: Is full when at capacity
    Given fuel with 100 current and 100 capacity
    When I check if fuel is full
    Then fuel should be full

  Scenario: Is not full when below capacity
    Given fuel with 99 current and 100 capacity
    When I check if fuel is full
    Then fuel should not be full

  Scenario: Fuel repr shows fuel levels
    Given fuel with 50 current and 100 capacity
    When I get the string representation of the fuel
    Then the representation should contain "50"
    And the representation should contain "100"

  # ============================================================================
  # FlightMode Enum Tests (22 scenarios)
  # ============================================================================

  Scenario: All flight modes exist
    Then CRUISE flight mode should exist
    And DRIFT flight mode should exist
    And BURN flight mode should exist
    And STEALTH flight mode should exist

  Scenario: CRUISE mode properties
    Given CRUISE flight mode
    Then the mode name should be "CRUISE"
    And the time multiplier should be 31
    And the fuel rate should be 1.0

  Scenario: DRIFT mode properties
    Given DRIFT flight mode
    Then the mode name should be "DRIFT"
    And the time multiplier should be 26
    And the fuel rate should be 0.003

  Scenario: BURN mode properties
    Given BURN flight mode
    Then the mode name should be "BURN"
    And the time multiplier should be 15
    And the fuel rate should be 2.0

  Scenario: STEALTH mode properties
    Given STEALTH flight mode
    Then the mode name should be "STEALTH"
    And the time multiplier should be 50
    And the fuel rate should be 1.0

  Scenario: Calculate fuel cost for distance
    Given CRUISE flight mode
    When I calculate fuel cost for 100.0 units distance
    Then the fuel cost should be 100 units

  Scenario: Fuel cost minimum is one
    Given DRIFT flight mode
    When I calculate fuel cost for 10.0 units distance
    Then the fuel cost should be at least 1 unit

  Scenario: Fuel cost zero for zero distance
    Given CRUISE flight mode
    When I calculate fuel cost for 0.0 units distance
    Then the fuel cost should be 0 units

  Scenario: Fuel cost ceils fractional values
    Given BURN flight mode
    When I calculate fuel cost for 50.5 units distance
    Then the fuel cost should be 101 units

  Scenario: Calculate travel time
    Given CRUISE flight mode
    When I calculate travel time for 100.0 units distance and engine speed 30
    Then the travel time should be 103 seconds

  Scenario: Travel time minimum is one
    Given CRUISE flight mode
    When I calculate travel time for 0.01 units distance and engine speed 1000
    Then the travel time should be at least 1 second

  Scenario: Travel time zero for zero distance
    Given CRUISE flight mode
    When I calculate travel time for 0.0 units distance and engine speed 30
    Then the travel time should be 0 seconds

  Scenario: Select optimal chooses BURN for high fuel
    Given a ship with 80.0% fuel
    When I select optimal flight mode
    Then BURN mode should be selected

  Scenario: Select optimal chooses DRIFT for low fuel
    Given a ship with 6.0% fuel
    When I select optimal flight mode
    Then DRIFT mode should be selected

  Scenario: Select optimal chooses CRUISE for medium fuel
    Given a ship with 10.0% fuel
    When I select optimal flight mode
    Then CRUISE mode should be selected

  # ============================================================================
  # Distance Value Object Tests (7 scenarios)
  # ============================================================================

  Scenario: Create distance with valid units
    Given a distance of 100.0 units
    Then the distance units should be 100.0

  Scenario: Distance is immutable
    Given a distance of 100.0 units
    When I attempt to modify the distance units to 200.0
    Then the modification should be rejected

  Scenario: Reject negative distance
    When I attempt to create a distance of -10.0 units
    Then distance creation should fail with error "distance cannot be negative"

  Scenario: Allow zero distance
    Given a distance of 0.0 units
    Then the distance units should be 0.0

  Scenario: Apply safety margin
    Given a distance of 100.0 units
    When I apply a safety margin of 0.1
    Then the distance with margin should be 110.0 units

  Scenario: With margin returns new distance
    Given a distance of 100.0 units
    When I apply a safety margin of 0.2
    Then the distance with margin should be 120.0 units
    And the original distance should be 100.0 units

  Scenario: With zero margin
    Given a distance of 100.0 units
    When I apply a safety margin of 0.0
    Then the distance with margin should be 100.0 units

  Scenario: Distance repr shows units
    Given a distance of 123.456 units
    When I get the string representation of the distance
    Then the representation should contain "units"
