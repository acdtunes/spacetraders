Feature: Ship Operations
  As a bot operator
  I want to control ship actions
  So that I can manage my fleet effectively

  Background:
    Given the SpaceTraders API is mocked
    And a waypoint "X1-TEST-A1" exists at (0, 0) with traits ["MARKETPLACE"]
    And a waypoint "X1-TEST-B2" exists at (100, 0) with traits ["ASTEROID_BASE"]

  Scenario: Orbit from docked position
    Given a ship "TEST-SHIP" at "X1-TEST-A1" is DOCKED
    And the ship has 400/400 fuel
    When I orbit the ship
    Then the ship should be IN_ORBIT
    And no error should occur

  Scenario: Orbit when already in orbit
    Given a ship "TEST-SHIP" at "X1-TEST-A1" is IN_ORBIT
    And the ship has 400/400 fuel
    When I orbit the ship
    Then the ship should be IN_ORBIT
    And no error should occur

  Scenario: Dock from orbit
    Given a ship "TEST-SHIP" at "X1-TEST-A1" is IN_ORBIT
    And the ship has 400/400 fuel
    When I dock the ship
    Then the ship should be DOCKED
    And no error should occur

  Scenario: Dock when already docked
    Given a ship "TEST-SHIP" at "X1-TEST-A1" is DOCKED
    And the ship has 400/400 fuel
    When I dock the ship
    Then the ship should be DOCKED
    And no error should occur

  Scenario: Navigate with sufficient fuel
    Given a ship "TEST-SHIP" at "X1-TEST-A1" is IN_ORBIT
    And the ship has 400/400 fuel
    When I navigate the ship to "X1-TEST-B2"
    Then the ship should be at "X1-TEST-B2"
    And the ship fuel should be less than 400

  Scenario: Navigate fails with insufficient fuel
    Given a ship "TEST-SHIP" at "X1-TEST-A1" is IN_ORBIT
    And the ship has 10/400 fuel
    And the agent has 0 credits
    When I navigate the ship to "X1-TEST-B2"
    Then navigation should fail
    And the ship should be at "X1-TEST-A1"

  Scenario: Refuel at marketplace
    Given a ship "TEST-SHIP" at "X1-TEST-A1" is DOCKED
    And the ship has 200/400 fuel
    And the agent has 10000 credits
    When I refuel the ship
    Then the ship should have 400/400 fuel
    And the agent credits should decrease

  Scenario: Refuel when already at capacity
    Given a ship "TEST-SHIP" at "X1-TEST-A1" is DOCKED
    And the ship has 400/400 fuel
    When I refuel the ship
    Then the ship should have 400/400 fuel
    And no error should occur

  Scenario: Navigate to nonexistent waypoint
    Given a ship "TEST-SHIP" at "X1-TEST-A1" is IN_ORBIT
    And the ship has 400/400 fuel
    When I navigate the ship to "X1-NONEXISTENT"
    Then navigation should fail
