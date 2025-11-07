Feature: Navigate Ship Command
  As a ship operator
  I want to navigate ships to destinations
  So that I can complete trade routes and missions

  Background:
    Given the navigate ship command handler is initialized

  # Happy Path - Simple Navigation
  Scenario: Navigate ship with simple single-segment route
    Given a ship "TEST-SHIP-1" at waypoint "X1-TEST-AB12" with 100 fuel
    And the ship is in orbit
    And waypoint "X1-TEST-CD34" exists at distance 14.14
    And a route plan exists to "X1-TEST-CD34" with 1 segment
    When I navigate ship "TEST-SHIP-1" to "X1-TEST-CD34"
    Then the route should be completed
    And the route should have 1 segment
    And the final destination should be "X1-TEST-CD34"
    And the API should have been called to navigate
    And the ship state should be persisted

  # Docked Ship - Auto Orbit
  Scenario: Navigate ship from docked status
    Given a ship "TEST-SHIP-1" at waypoint "X1-TEST-AB12" with 100 fuel
    And the ship is docked
    And waypoint "X1-TEST-CD34" exists at distance 14.14
    And a route plan exists to "X1-TEST-CD34" with 1 segment
    When I navigate ship "TEST-SHIP-1" to "X1-TEST-CD34"
    Then the route should be completed
    And the ship should have been put into orbit first

  # Multi-Segment Routes
  Scenario: Navigate ship with multi-segment route
    Given a ship "TEST-SHIP-1" at waypoint "X1-TEST-AB12" with 100 fuel
    And the ship is in orbit
    And waypoints "X1-TEST-CD34" and "X1-TEST-EF56" exist
    And a route plan exists from "X1-TEST-AB12" to "X1-TEST-EF56" via "X1-TEST-CD34"
    When I navigate ship "TEST-SHIP-1" to "X1-TEST-EF56"
    Then the route should be completed
    And the route should have 2 segments
    And segment 1 should end at "X1-TEST-CD34"
    And segment 2 should end at "X1-TEST-EF56"
    And the API should have been called 2 times for navigation

  # Route with Refueling
  Scenario: Navigate ship with refueling stop
    Given a ship "TEST-SHIP-1" at waypoint "X1-TEST-AB12" with 50 fuel
    And the ship is in orbit
    And waypoint "X1-TEST-CD34" has refueling available
    And waypoint "X1-TEST-EF56" exists at distance 14.14 from "X1-TEST-CD34"
    And a route plan exists with refuel stop at "X1-TEST-CD34"
    When I navigate ship "TEST-SHIP-1" to "X1-TEST-EF56"
    Then the route should be completed
    And the ship should have been docked for refueling
    And the ship should have been refueled

  # Error Conditions
  Scenario: Navigate ship that does not exist
    Given no ships exist in the repository
    When I attempt to navigate ship "NONEXISTENT" to "X1-TEST-CD34"
    Then the command should fail with ShipNotFoundError
    And the error message should contain "NONEXISTENT"

  Scenario: Navigate ship when no path exists
    Given a ship "TEST-SHIP-1" at waypoint "X1-TEST-AB12" with 100 fuel
    And the ship is in orbit
    And no route plan can be found to "X1-TEST-UNREACHABLE"
    When I attempt to navigate ship "TEST-SHIP-1" to "X1-TEST-UNREACHABLE"
    Then the command should fail with ValueError
    And the error message should contain "No waypoints found"

  Scenario: Navigate ship with wrong player ID
    Given a ship "TEST-SHIP-1" belongs to player 1
    And the ship is at waypoint "X1-TEST-AB12" with 100 fuel
    When I attempt to navigate ship "TEST-SHIP-1" as player 2
    Then the command should fail with ShipNotFoundError

  # System Symbol Extraction
  Scenario: Navigate ship with complex system symbol
    Given a ship "TEST-SHIP-1" at waypoint "X1-ABC123-XY99" with 100 fuel
    And the ship is in orbit
    And waypoint "X1-ABC123-ZW88" exists at distance 14.14
    And a route plan exists to "X1-ABC123-ZW88" with 1 segment
    When I navigate ship "TEST-SHIP-1" to "X1-ABC123-ZW88"
    Then the route should be completed
    And the system symbol should be extracted as "X1-ABC123"

  # Ship State Updates
  Scenario: Navigate ship updates fuel correctly
    Given a ship "TEST-SHIP-1" at waypoint "X1-TEST-AB12" with 100 fuel
    And the ship is in orbit
    And waypoint "X1-TEST-CD34" exists at distance 14.14
    And a route plan exists requiring 30 fuel
    When I navigate ship "TEST-SHIP-1" to "X1-TEST-CD34"
    Then the ship fuel should be reduced to 70
    And the ship state should be persisted

  Scenario: Navigate ship persists state to repository
    Given a ship "TEST-SHIP-1" at waypoint "X1-TEST-AB12" with 100 fuel
    And the ship is in orbit
    And waypoint "X1-TEST-CD34" exists at distance 14.14
    And a route plan exists to "X1-TEST-CD34" with 1 segment
    When I navigate ship "TEST-SHIP-1" to "X1-TEST-CD34"
    Then the repository should have been updated at least once

  # Route Return Value
  Scenario: Navigate ship returns completed route entity
    Given a ship "TEST-SHIP-1" at waypoint "X1-TEST-AB12" with 100 fuel
    And the ship is in orbit
    And waypoint "X1-TEST-CD34" exists at distance 14.14
    And a route plan exists to "X1-TEST-CD34" with 1 segment
    When I navigate ship "TEST-SHIP-1" to "X1-TEST-CD34"
    Then a Route entity should be returned
    And the route should belong to ship "TEST-SHIP-1"
    And the route should belong to player 1
    And the route status should be COMPLETED
    And the route should have at least 1 segment
