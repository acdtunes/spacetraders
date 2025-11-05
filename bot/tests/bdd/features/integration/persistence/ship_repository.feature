Feature: Ship Repository CRUD Operations
  As a persistence layer
  I want to store and retrieve ship data
  So that I can manage fleet operations

  Background:
    Given a fresh ship repository
    And a test player exists

  Scenario: Create new ship
    When I create a ship with symbol "SHIP-1"
    Then the ship should be persisted
    And the ship should have symbol "SHIP-1"
    And the ship fuel should be 100

  Scenario: Create duplicate ship fails
    Given a created ship "SHIP-1"
    When I attempt to create another ship "SHIP-1" for the same player
    Then creation should fail with DuplicateShipError

  Scenario: Same ship symbol for different players
    Given a second test player exists
    When I create ship "SHIP-1" for player 1
    And I create ship "SHIP-1" for player 2
    Then both ships should exist independently

  Scenario: Find ship by symbol when exists
    Given a created ship "SHIP-1"
    When I find the ship by symbol "SHIP-1"
    Then the ship should be found
    And the ship location should be "X1-A1"

  Scenario: Find ship by symbol when not exists
    When I find ship by symbol "NONEXISTENT"
    Then the ship should not be found

  Scenario: Find ship reconstructs waypoint from graph
    Given a created ship "SHIP-1"
    When I find the ship by symbol "SHIP-1"
    Then the ship waypoint should have full details from graph

  Scenario: Find all ships by player when empty
    When I list all ships for the player
    Then I should see 0 ships

  Scenario: Find all ships by player with single ship
    Given a created ship "SHIP-1"
    When I list all ships for the player
    Then I should see 1 ship

  Scenario: Find all ships by player with multiple ships
    Given a created ship "SHIP-1"
    And a created ship "SHIP-2"
    And a created ship "SHIP-3"
    When I list all ships for the player
    Then I should see 3 ships

  Scenario: Ships are returned ordered by symbol
    Given a created ship "SHIP-C"
    And a created ship "SHIP-A"
    And a created ship "SHIP-B"
    When I list all ships for the player
    Then ships should be in alphabetical order

  Scenario: Update ship location and fuel
    Given a created ship "SHIP-1"
    When I move the ship to "X1-A2" and consume fuel to 50
    And I find the ship by symbol "SHIP-1"
    Then the ship location should be "X1-A2"
    And the ship fuel should be 50

  Scenario: Update ship cargo
    Given a created ship "SHIP-1"
    When I update the ship cargo to 40 units
    And I find the ship by symbol "SHIP-1"
    Then the ship cargo should be 40

  Scenario: Update ship nav status
    Given a created ship "SHIP-1"
    When I change the ship nav_status to "DOCKED"
    And I find the ship by symbol "SHIP-1"
    Then the ship nav_status should be "DOCKED"

  Scenario: Update nonexistent ship fails
    When I attempt to update a nonexistent ship
    Then update should fail with ShipNotFoundError

  Scenario: Delete ship
    Given a created ship "SHIP-1"
    When I delete the ship "SHIP-1"
    And I find ship by symbol "SHIP-1"
    Then the ship should not be found

  Scenario: Delete nonexistent ship fails
    When I attempt to delete ship "NONEXISTENT"
    Then deletion should fail with ShipNotFoundError

  Scenario: Ships with different nav statuses
    When I create ship "SHIP-DOCKED" with status "DOCKED"
    And I create ship "SHIP-ORBIT" with status "IN_ORBIT"
    And I create ship "SHIP-TRANSIT" with status "IN_TRANSIT"
    Then all 3 ships should have their respective statuses

  Scenario: Ship with zero fuel
    When I create a ship with zero fuel
    Then the ship should be persisted
    And the ship fuel should be 0

  Scenario: Ship with full cargo
    When I create a ship with full cargo
    Then the ship cargo should equal capacity
    And the ship should be at full cargo

  Scenario: Ships at different waypoint locations
    When I create ship "SHIP-1" at "X1-A1"
    And I create ship "SHIP-2" at "X1-A2"
    And I create ship "SHIP-3" at "X1-A3"
    Then each ship should be at its designated location

  Scenario: Update ship location multiple times
    Given a created ship "SHIP-1"
    When I move the ship to "X1-A2"
    And I move the ship to "X1-A3"
    Then the ship location should be "X1-A3"

  Scenario: Ships with different engine speeds
    When I create ship "SHIP-SLOW" with speed 10
    And I create ship "SHIP-MEDIUM" with speed 30
    And I create ship "SHIP-FAST" with speed 50
    Then each ship should have its designated speed
