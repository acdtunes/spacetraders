Feature: Orbit Ship Command
  As a ship operator
  I want to orbit my ships
  So that I can prepare them for navigation

  Background:
    Given the orbit ship command system is initialized

  # Happy Path Scenarios
  Scenario: Successfully orbit a docked ship
    Given a ship "TEST-SHIP-1" for player 1 with status "DOCKED"
    When I execute orbit command for ship "TEST-SHIP-1" and player 1
    Then the command should succeed
    And the ship status should be "IN_ORBIT"
    And the API orbit endpoint should be called with "TEST-SHIP-1"
    And the ship should be updated in the repository

  Scenario: Orbit a ship already in orbit
    Given a ship "TEST-SHIP-1" for player 1 with status "IN_ORBIT"
    When I execute orbit command for ship "TEST-SHIP-1" and player 1
    Then the command should succeed
    And the ship status should be "IN_ORBIT"

  # Error Scenarios
  Scenario: Cannot orbit a non-existent ship
    Given no ship exists with symbol "NONEXISTENT"
    When I attempt to orbit ship "NONEXISTENT" for player 1
    Then the command should fail with ShipNotFoundError
    And the error message should contain "NONEXISTENT"
    And the error message should contain "player 1"

  # Eventual Consistency - Waiting for Ship State
  Scenario: Orbit command waits for ship in transit to arrive
    Given a ship "TEST-SHIP-1" for player 1 in transit arriving in 0.1 seconds at "X1-TEST-AB12"
    When I execute orbit command for ship "TEST-SHIP-1" and player 1
    Then the command should succeed
    And the ship status should be "IN_ORBIT"

  Scenario: Cannot orbit another player's ship
    Given a ship "TEST-SHIP-1" for player 1 with status "DOCKED"
    When I attempt to orbit ship "TEST-SHIP-1" for player 2
    Then the command should fail with ShipNotFoundError

  # State Transition Scenarios
  Scenario: Ship transitions from DOCKED to IN_ORBIT
    Given a ship "TEST-SHIP-1" for player 1 with status "DOCKED"
    When I execute orbit command for ship "TEST-SHIP-1" and player 1
    Then the ship status should be "IN_ORBIT"
    And the ship status should not be "DOCKED"

  Scenario: Orbiting preserves all ship properties
    Given a ship "TEST-SHIP-1" for player 1 with the following properties:
      | property       | value        |
      | nav_status     | DOCKED       |
      | fuel_current   | 50           |
      | fuel_capacity  | 100          |
      | cargo_capacity | 40           |
      | cargo_units    | 0            |
      | engine_speed   | 30           |
      | location       | X1-TEST-AB12 |
    When I execute orbit command for ship "TEST-SHIP-1" and player 1
    Then the ship should have the following properties:
      | property       | value        |
      | nav_status     | IN_ORBIT     |
      | fuel_current   | 50           |
      | fuel_capacity  | 100          |
      | cargo_capacity | 40           |
      | cargo_units    | 0            |
      | engine_speed   | 30           |
      | location       | X1-TEST-AB12 |

  Scenario: API is called with correct ship symbol
    Given a ship "CUSTOM-SHIP" for player 1 with status "DOCKED"
    When I execute orbit command for ship "CUSTOM-SHIP" and player 1
    Then the API orbit endpoint should be called with "CUSTOM-SHIP"
