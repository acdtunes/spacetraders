Feature: Ship navigation operations
  As a ship controller
  I want to navigate ships between waypoints
  So that ships can reach destinations efficiently

  Background:
    Given a mock API client
    And a ship "TEST-SHIP" exists at waypoint "X1-TEST-A1"

  Scenario: Navigate with sufficient fuel
    Given the ship "TEST-SHIP" is IN_ORBIT at "X1-TEST-A1"
    And the ship has 400/400 fuel
    And waypoint "X1-TEST-B2" exists at distance 100 from "X1-TEST-A1"
    When I navigate to "X1-TEST-B2" with auto-refuel disabled
    Then navigation should succeed
    And the ship should be DOCKED at "X1-TEST-B2"
    And fuel should be consumed for the journey

  Scenario: Navigate with insufficient fuel fails when auto-refuel disabled
    Given the ship "TEST-SHIP" is IN_ORBIT at "X1-TEST-A1"
    And the ship has 50/400 fuel
    And waypoint "X1-TEST-B2" exists at distance 150 from "X1-TEST-A1"
    When I navigate to "X1-TEST-B2" with auto-refuel disabled
    Then navigation should fail due to insufficient fuel
    And the ship should still be at "X1-TEST-A1"

  Scenario: Navigate to same location is a no-op
    Given the ship "TEST-SHIP" is IN_ORBIT at "X1-TEST-A1"
    And the ship has 400/400 fuel
    When I navigate to "X1-TEST-A1"
    Then navigation should succeed immediately
    And the ship should still be IN_ORBIT at "X1-TEST-A1"
    And no fuel should be consumed

  Scenario: Navigate with flight mode auto-selection based on fuel
    Given the ship "TEST-SHIP" is IN_ORBIT at "X1-TEST-A1"
    And the ship has 400/400 fuel
    And waypoint "X1-TEST-B2" exists at distance 100 from "X1-TEST-A1"
    When I navigate to "X1-TEST-B2" without specifying flight mode
    Then flight mode should be auto-selected based on fuel level
    And navigation should succeed

  Scenario: Navigate with explicit CRUISE mode
    Given the ship "TEST-SHIP" is IN_ORBIT at "X1-TEST-A1"
    And the ship has 400/400 fuel
    And waypoint "X1-TEST-B2" exists at distance 100 from "X1-TEST-A1"
    When I navigate to "X1-TEST-B2" with flight mode "CRUISE"
    Then navigation should succeed
    And flight mode should be set to "CRUISE"

  Scenario: Navigate with explicit DRIFT mode
    Given the ship "TEST-SHIP" is IN_ORBIT at "X1-TEST-A1"
    And the ship has 150/400 fuel
    And waypoint "X1-TEST-B2" exists at distance 100 from "X1-TEST-A1"
    When I navigate to "X1-TEST-B2" with flight mode "DRIFT"
    Then navigation should succeed
    And flight mode should be set to "DRIFT"

  Scenario: Wait for arrival when already in transit to different destination
    Given the ship "TEST-SHIP" is IN_TRANSIT to "X1-TEST-C3"
    And the ship will arrive in 20 seconds
    And the ship has 250/400 fuel
    And waypoint "X1-TEST-B2" exists at distance 50 from "X1-TEST-C3"
    When I navigate to "X1-TEST-B2"
    Then the operation should wait for arrival at "X1-TEST-C3"
    And then navigate to "X1-TEST-B2"

  Scenario: Arrival time calculation is accurate
    Given the ship "TEST-SHIP" is IN_ORBIT at "X1-TEST-A1"
    And the ship has 400/400 fuel
    And waypoint "X1-TEST-B2" exists at distance 200 from "X1-TEST-A1"
    When I navigate to "X1-TEST-B2"
    Then an arrival time should be calculated
    And the ship should wait for the calculated arrival time

  Scenario: Navigation to nonexistent waypoint fails gracefully
    Given the ship "TEST-SHIP" is IN_ORBIT at "X1-TEST-A1"
    And the ship has 400/400 fuel
    And waypoint "X1-INVALID-99" does not exist
    When I navigate to "X1-INVALID-99"
    Then navigation should fail with error "Failed to get waypoint coordinates"
    And the ship should still be at "X1-TEST-A1"
