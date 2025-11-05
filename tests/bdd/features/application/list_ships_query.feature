Feature: List Ships Query
  As a fleet operator
  I want to query all ships for a player
  So that I can manage my fleet effectively

  Background:
    Given the list ships query handler is initialized

  # Query Creation and Immutability

  Scenario: Create query with required fields
    When I create a list ships query for player 1
    Then the list query should have player id 1

  Scenario: Query is immutable
    Given I create a list ships query for player 1
    When I attempt to modify the list query player id to 2
    Then the modification should fail with AttributeError

  Scenario: Query with different player IDs
    Given I create a list ships query for player 1
    And I create a list ships query for player 2
    Then the first list query should have player id 1
    And the second list query should have player id 2

  # Successful Ship List Retrieval

  Scenario: Successful ship list retrieval
    Given a ship "SHIP-1" exists for player 1
    And a ship "SHIP-2" exists for player 1
    And a ship "SHIP-3" exists for player 1
    When I query all ships for player 1
    Then the query should succeed
    And the result should be a list
    And the list should contain 3 ships
    And all ships should be Ship instances
    And all ships should have player id 1

  Scenario: Empty ship list for player with no ships
    Given no ships exist in the repository
    When I query all ships for player 999
    Then the query should succeed
    And the list should be empty
    And the list should contain 0 ships

  Scenario: Player with single ship
    Given a ship "LONE-SHIP" exists for player 1
    When I query all ships for player 1
    Then the query should succeed
    And the list should contain 1 ships
    And the ship at index 0 should have symbol "LONE-SHIP"

  # Multiple Ships with Different Properties

  Scenario: Ships at different locations
    Given a ship "SHIP-1" exists for player 1
    And the ship is at waypoint "X1-LOC-1" at coordinates 0.0, 0.0
    And the waypoint has type "PLANET"
    And a ship "SHIP-2" exists for player 1
    And the second ship is at waypoint "X1-LOC-2" at coordinates 100.0, 100.0
    And the second waypoint has type "MOON"
    When I query all ships for player 1
    Then the query should succeed
    And the list should contain 2 ships
    And the ship at index 0 should be at location "X1-LOC-1"
    And the ship at index 1 should be at location "X1-LOC-2"

  Scenario: Ships with different fuel levels
    Given a ship "SHIP-FULL" exists for player 1
    And the ship has 200 current fuel and 200 capacity
    And a ship "SHIP-HALF" exists for player 1
    And the second ship has 100 current fuel and 200 capacity
    And a ship "SHIP-EMPTY" exists for player 1
    And the third ship has 0 current fuel and 200 capacity
    When I query all ships for player 1
    Then the query should succeed
    And the list should contain 3 ships
    And the ship at index 0 should have fuel 200
    And the ship at index 1 should have fuel 100
    And the ship at index 2 should have fuel 0

  Scenario: Ships with different cargo levels
    Given a ship "SHIP-EMPTY-CARGO" exists for player 1
    And the ship has cargo capacity 100 with 0 units
    And a ship "SHIP-FULL-CARGO" exists for player 1
    And the second ship has cargo capacity 100 with 100 units
    When I query all ships for player 1
    Then the query should succeed
    And the list should contain 2 ships
    And the ship at index 0 should have cargo 0 units
    And the ship at index 1 should have cargo 100 units

  Scenario: Ships with different navigation statuses
    Given a ship "SHIP-DOCKED" exists for player 1
    And the ship has navigation status "DOCKED"
    And a ship "SHIP-ORBIT" exists for player 1
    And the second ship has navigation status "IN_ORBIT"
    When I query all ships for player 1
    Then the query should succeed
    And the list should contain 2 ships
    And the ship at index 0 should have nav status "DOCKED"
    And the ship at index 1 should have nav status "IN_ORBIT"

  # Player Isolation

  Scenario: Different players get different ship lists
    Given a ship "P1-SHIP-1" exists for player 1
    And a ship "P2-SHIP-1" exists for player 2
    When I query all ships for player 1
    And I query all ships for player 2
    Then the first list should contain 1 ships
    And the first list ship at index 0 should have symbol "P1-SHIP-1"
    And the first list ship at index 0 should have player id 1
    And the second list should contain 1 ships
    And the second list ship at index 0 should have symbol "P2-SHIP-1"
    And the second list ship at index 0 should have player id 2

  # Large Fleets

  Scenario: Player with large fleet
    Given player 1 has 50 ships
    When I query all ships for player 1
    Then the query should succeed
    And the list should contain 50 ships
    And all ships should have player id 1

  # Idempotent Query Operations

  Scenario: Handler returns consistent results
    Given a ship "SHIP-1" exists for player 1
    And a ship "SHIP-2" exists for player 1
    And a ship "SHIP-3" exists for player 1
    When I query all ships for player 1
    Then the query should succeed
    And the list should contain 3 ships
    And all ships should have player id 1

  Scenario: Consecutive queries return same data
    Given a ship "SHIP-1" exists for player 1
    And a ship "SHIP-2" exists for player 1
    And a ship "SHIP-3" exists for player 1
    When I query all ships for player 1
    And I query all ships for player 1 again
    Then both queries should return 3 ships
    And the ship at index 0 should have symbol "SHIP-1"
    And the ship at index 1 should have symbol "SHIP-2"
    And the ship at index 2 should have symbol "SHIP-3"

  # Data Integrity

  Scenario: Ships preserve all properties
    Given a ship "DETAILED-SHIP" exists for player 1
    And the ship is at waypoint "X1-BASE" at coordinates 0.0, 0.0
    And the waypoint has type "PLANET"
    And the ship has 150 current fuel and 250 capacity
    And the ship has cargo capacity 150 with 75 units
    And the ship has engine speed 45
    And the ship has navigation status "IN_ORBIT"
    When I query all ships for player 1
    Then the query should succeed
    And the list should contain 1 ships
    And the ship at index 0 should have symbol "DETAILED-SHIP"
    And the ship at index 0 should have player id 1
    And the ship at index 0 should have fuel 150 with capacity 250
    And the ship at index 0 should have cargo 75 units with capacity 150
    And the ship at index 0 should have engine speed 45
    And the ship at index 0 should have nav status "IN_ORBIT"
    And the ship at index 0 should be at location "X1-BASE"

  # Error Handling

  Scenario: Repository exception is propagated
    Given the repository will raise "Database error"
    When I query all ships for player 1
    Then the query should fail with RuntimeError
    And the error message should contain "Database error"
