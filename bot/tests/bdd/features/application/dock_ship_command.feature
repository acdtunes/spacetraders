Feature: Dock Ship Command
  As a ship operator
  I want to dock ships at waypoints
  So that I can access marketplace services and refueling

  Background:
    Given the dock ship command handler is initialized

  # Happy Path - Successful Docking
  Scenario: Dock ship successfully from orbit
    Given a ship "TEST-SHIP-1" for player 1 in orbit at "X1-TEST-AB12"
    When I execute dock ship command for "TEST-SHIP-1" and player 1
    Then the ship should be docked
    And the API dock method should be called with "TEST-SHIP-1"
    And the ship should be persisted with nav status "DOCKED"

  Scenario: Dock ship that is already docked
    Given a ship "TEST-SHIP-1" for player 1 already docked at "X1-TEST-AB12"
    When I execute dock ship command for "TEST-SHIP-1" and player 1
    Then the ship should be docked
    And the ship nav status should remain "DOCKED"

  # Error Conditions
  Scenario: Cannot dock non-existent ship
    Given no ship exists with symbol "NONEXISTENT" for player 1
    When I attempt to dock ship "NONEXISTENT" for player 1
    Then the command should fail with ShipNotFoundError
    And the error message should mention "NONEXISTENT"
    And the error message should mention "player 1"

  # Eventual Consistency - Waiting for Ship State
  Scenario: Dock command waits for ship in transit to arrive
    Given a ship "TEST-SHIP-1" for player 1 in transit arriving in 0.1 seconds
    When I execute dock ship command for "TEST-SHIP-1" and player 1
    Then the handler should wait for arrival
    And the ship should be docked after waiting
    And the API dock method should be called with "TEST-SHIP-1"

  Scenario: Cannot dock ship belonging to different player
    Given a ship "TEST-SHIP-1" for player 1 in orbit at "X1-TEST-AB12"
    When I attempt to dock ship "TEST-SHIP-1" for player 2
    Then the command should fail with ShipNotFoundError

  # State Transitions
  Scenario: Ship transitions from orbit to docked
    Given a ship "TEST-SHIP-1" for player 1 in orbit at "X1-TEST-AB12"
    When I execute dock ship command for "TEST-SHIP-1" and player 1
    Then the ship nav status should change from "IN_ORBIT" to "DOCKED"

  Scenario: Docking preserves all other ship properties
    Given a ship "TEST-SHIP-1" for player 1 in orbit at "X1-TEST-AB12" with fuel 50/100
    When I execute dock ship command for "TEST-SHIP-1" and player 1
    Then the ship should be docked
    And the ship should have fuel current 50 and capacity 100
    And the ship should have cargo capacity 40 and units 0
    And the ship should have engine speed 30
    And the ship should be at location "X1-TEST-AB12"
