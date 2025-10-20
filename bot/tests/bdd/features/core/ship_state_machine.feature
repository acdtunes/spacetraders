Feature: Ship state machine
  As a ship controller
  I want to manage ship state transitions
  So that ships can perform operations safely

  Background:
    Given a mock API client
    And a ship "TEST-SHIP" exists at waypoint "X1-TEST-A1"

  Scenario: Orbit from DOCKED state
    Given the ship "TEST-SHIP" is DOCKED at "X1-TEST-A1"
    And the ship has 400/400 fuel
    When I orbit the ship
    Then the ship should be IN_ORBIT
    And the ship should still be at "X1-TEST-A1"

  Scenario: Dock from IN_ORBIT state
    Given the ship "TEST-SHIP" is IN_ORBIT at "X1-TEST-A1"
    And the ship has 400/400 fuel
    When I dock the ship
    Then the ship should be DOCKED
    And the ship should still be at "X1-TEST-A1"

  Scenario: Navigate from IN_ORBIT state
    Given the ship "TEST-SHIP" is IN_ORBIT at "X1-TEST-A1"
    And the ship has 400/400 fuel
    And waypoint "X1-TEST-B2" exists at distance 100 from "X1-TEST-A1"
    When I navigate to "X1-TEST-B2"
    Then the operation should wait for arrival
    And the ship should be DOCKED at "X1-TEST-B2"

  Scenario: Dock from IN_TRANSIT waits for arrival
    Given the ship "TEST-SHIP" is IN_TRANSIT to "X1-TEST-B2"
    And the ship will arrive in 10 seconds
    And the ship has 300/400 fuel
    When I dock the ship
    Then the operation should wait for arrival
    And the ship should be DOCKED at "X1-TEST-B2"

  Scenario: Navigate from DOCKED auto-orbits first
    Given the ship "TEST-SHIP" is DOCKED at "X1-TEST-A1"
    And the ship has 400/400 fuel
    And waypoint "X1-TEST-B2" exists at distance 100 from "X1-TEST-A1"
    When I navigate to "X1-TEST-B2"
    Then the ship should orbit before navigating
    And the operation should wait for arrival
    And the ship should be DOCKED at "X1-TEST-B2"

  Scenario: Extract requires IN_ORBIT state
    Given the ship "TEST-SHIP" is IN_ORBIT at "X1-TEST-A1"
    And waypoint "X1-TEST-A1" is an ENGINEERED_ASTEROID
    And the ship has 200/400 fuel
    And the ship has cargo space available
    When I extract resources
    Then extraction should succeed
    And cargo should contain the extracted resource

  Scenario: Get status returns correct ship state
    Given the ship "TEST-SHIP" is DOCKED at "X1-TEST-A1"
    And the ship has 350/400 fuel
    And the ship has 10/40 cargo units used
    When I query ship status
    Then status should show nav_status "DOCKED"
    And status should show location "X1-TEST-A1"
    And status should show fuel 350/400
    And status should show cargo 10/40

  Scenario: Wait for arrival when already in transit to destination
    Given the ship "TEST-SHIP" is IN_TRANSIT to "X1-TEST-B2"
    And the ship will arrive in 15 seconds
    And the ship has 300/400 fuel
    When I navigate to "X1-TEST-B2"
    Then the operation should wait for arrival
    And the ship should be DOCKED at "X1-TEST-B2"
