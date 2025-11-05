Feature: Multi-hop Navigation with Refueling
  As a ship operator
  I want ships to dock and refuel at waypoints during multi-hop journeys
  So that long-distance navigation completes successfully

  Background:
    Given the navigation system is initialized

  Scenario: Ship docks and refuels at intermediate waypoint during multi-hop journey
    Given a test ship "TEST-SHIP-1" is registered for multi-hop navigation
    And the ship is at waypoint "X1-START" with 50 fuel and 400 capacity
    And waypoint "X1-MIDDLE" is 100 units away and has a marketplace
    And waypoint "X1-END" is 200 units away from "X1-MIDDLE"
    And the ship requires refuel at "X1-MIDDLE" to reach "X1-END"
    When I execute multi-hop navigation from "X1-START" to "X1-END" via "X1-MIDDLE"
    Then the ship should arrive at "X1-MIDDLE" in orbit
    And the ship should dock at "X1-MIDDLE"
    And the ship nav status should be "DOCKED" before refuel
    And the refuel operation should succeed
    And the ship should have full fuel after refuel
    And the ship should orbit after refuel
    And the ship should continue to "X1-END"
    And the final ship location should be "X1-END"

  Scenario: Refuel operation fails if ship is not properly docked
    Given a test ship "TEST-SHIP-2" is registered for refuel test
    And the ship is at waypoint "X1-FUEL-STATION" in orbit
    And the waypoint has a marketplace for refueling
    When I attempt to refuel without docking
    Then the refuel should fail with "ship must be docked" error

  Scenario: Ship state syncs correctly after dock operation
    Given a test ship "TEST-SHIP-3" is registered for dock sync test
    And the ship is at waypoint "X1-STATION" in orbit
    When I dock the ship using the dock command
    Then the domain entity should show "DOCKED" status
    And the API should confirm "DOCKED" status
    And a subsequent get_ship call should show "DOCKED" status

  Scenario: Navigate command properly docks ship before refuel in multi-hop route
    Given a test ship "TEST-SHIP-4" exists at "X1-A" with low fuel
    And "X1-A" has a marketplace for refueling
    And "X1-B" is 150 units away requiring refuel
    When I navigate from "X1-A" to "X1-B" with initial refuel
    Then the ship should dock at "X1-A" first
    And the ship should refuel successfully at "X1-A"
    And the ship should orbit after refueling
    And the ship should navigate to "X1-B"
    And the ship should arrive at "X1-B"
