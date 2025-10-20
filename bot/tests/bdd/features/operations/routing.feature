Feature: Routing operations
  As a fleet manager
  I want to build graphs and plan routes
  So that I can navigate ships efficiently

  Background:
    Given a routing system

  Scenario: Build system graph successfully
    Given system "X1-TEST" has waypoints in database
    When I build graph for system "X1-TEST"
    Then graph should be built successfully
    And output should show waypoint count
    And output should show edge count

  Scenario: Build graph shows fuel station count
    Given system "X1-TEST" has 5 waypoints
    And 2 waypoints have fuel stations
    When I build graph for system "X1-TEST"
    Then output should show "2" fuel stations

  Scenario: Plan route between waypoints
    Given system "X1-TEST" has graph in database
    And ship "SHIP-1" is at waypoint "X1-TEST-A1"
    And ship "SHIP-1" has fuel 100/100
    When I plan route from "X1-TEST-A1" to "X1-TEST-B5"
    Then route should be found
    And output should show total time
    And output should show final fuel

  Scenario: Plan route shows navigation steps
    Given system "X1-TEST" has graph with route
    And ship "SHIP-1" has fuel 100/100
    When I plan route from "X1-TEST-A1" to "X1-TEST-B5"
    Then output should show navigation steps
    And steps should include distance
    And steps should include fuel cost

  Scenario: Plan route fails when no graph
    Given system "X1-EMPTY" has no graph in database
    And ship "SHIP-1" has fuel 100/100
    When I plan route from "X1-EMPTY-A1" to "X1-EMPTY-B5"
    Then route planning should fail
    And output should show graph not found

  Scenario: Plan route fails when routing paused
    Given system "X1-TEST" has graph in database
    And ship "SHIP-1" has fuel 100/100
    And routing is paused
    When I plan route from "X1-TEST-A1" to "X1-TEST-B5"
    Then route planning should fail
    And output should show routing paused

  Scenario: Plan route saves to file when requested
    Given system "X1-TEST" has graph with route
    And ship "SHIP-1" has fuel 100/100
    When I plan route from "X1-TEST-A1" to "X1-TEST-B5" with output "routes/test.json"
    Then route should be saved to file
    And saved route should contain steps

  Scenario: Build graph fails when no waypoints
    Given system "X1-EMPTY" has no waypoints
    When I build graph for system "X1-EMPTY"
    Then graph build should fail
    And output should show failed to build

  Scenario: Plan route fails when ship data unavailable
    Given system "X1-TEST" has graph in database
    And ship "SHIP-1" does not exist
    When I plan route from "X1-TEST-A1" to "X1-TEST-B5"
    Then route planning should fail
    And output should show failed to get ship

  Scenario: Plan route fails when no route found
    Given system "X1-TEST" has graph in database
    And ship "SHIP-1" has fuel 100/100
    And no route exists between waypoints
    When I plan route from "X1-TEST-A1" to "X1-TEST-B5"
    Then route planning should fail
    And output should show no route found

  Scenario: Plan route with refuel stops
    Given system "X1-TEST" has graph with refuel route
    And ship "SHIP-1" has fuel 20/100
    When I plan route from "X1-TEST-A1" to "X1-TEST-B5"
    Then route should include refuel action
    And refuel waypoint should be shown
