Feature: Route Planner Service
  As a SpaceTraders bot
  I want to plan routes using the routing client
  So that ships can navigate efficiently with optimal fuel usage

  Background:
    Given a ship "TEST-SHIP" exists with current fuel 100 and capacity 100
    And the ship is at waypoint "X1-A1" with coordinates (0, 0)
    And a destination waypoint "X1-B2" exists at coordinates (100, 50)

  Scenario: Plan simple single-segment direct route
    Given the routing client returns a single-step direct route
    When I plan a route from ship location to "X1-B2"
    Then the planned route should be created successfully
    And the planned route should have 1 segment
    And the planned route should not require refuel before departure
    And planned segment 1 should travel from "X1-A1" to "X1-B2"
    And planned segment 1 should use flight mode "CRUISE"
    And planned segment 1 should not require refuel

  Scenario: Plan multi-segment route with mid-route refueling
    Given a waypoint "X1-FUEL" exists at coordinates (50, 25) with fuel station
    And the routing client returns a route with refueling stop
    When I plan a route from ship location to "X1-B2"
    Then the planned route should be created successfully
    And the planned route should have 2 segments
    And the planned route should not require refuel before departure
    And planned segment 1 should travel from "X1-A1" to "X1-FUEL"
    And planned segment 1 should require refuel
    And planned segment 2 should travel from "X1-FUEL" to "X1-B2"
    And planned segment 2 should not require refuel

  Scenario: Plan route with refuel before departure flag
    Given the ship is at a fuel station waypoint "X1-A1"
    And the routing client returns a route with refuel before departure
    When I plan a route from ship location to "X1-B2"
    Then the planned route should be created successfully
    And the planned route should require refuel before departure
    And the planned route should have at least 1 segment

  Scenario: Plan route with BURN flight mode
    Given the routing client returns a route with BURN flight mode
    When I plan a route from ship location to "X1-B2"
    Then the planned route should be created successfully
    And the planned route should have 1 segment
    And planned segment 1 should use flight mode "BURN"

  Scenario: Plan route with DRIFT flight mode
    Given the routing client returns a route with DRIFT flight mode
    When I plan a route from ship location to "X1-B2"
    Then the planned route should be created successfully
    And the planned route should have 1 segment
    And planned segment 1 should use flight mode "DRIFT"

  Scenario: Plan route with STEALTH flight mode
    Given the routing client returns a route with STEALTH flight mode
    When I plan a route from ship location to "X1-B2"
    Then the planned route should be created successfully
    And the planned route should have 1 segment
    And planned segment 1 should use flight mode "STEALTH"

  Scenario: Handle empty route response from routing client
    Given the routing client returns an empty route
    When I plan a route from ship location to "X1-B2"
    Then route planning should fail with error "no route found: routing engine returned empty plan"

  Scenario: Handle routing client error
    Given the routing client returns an error "connection timeout"
    When I plan a route from ship location to "X1-B2"
    Then route planning should fail with error "routing client error: connection timeout"

  Scenario: Convert complex multi-step route correctly
    Given a waypoint "X1-FUEL-1" exists at coordinates (30, 20) with fuel station
    And a waypoint "X1-FUEL-2" exists at coordinates (70, 40) with fuel station
    And the routing client returns a complex route with multiple refuel stops
    When I plan a route from ship location to "X1-B2"
    Then the planned route should be created successfully
    And the planned route should have 3 segments
    And planned segment 1 should travel from "X1-A1" to "X1-FUEL-1"
    And planned segment 1 should require refuel
    And planned segment 2 should travel from "X1-FUEL-1" to "X1-FUEL-2"
    And planned segment 2 should require refuel
    And planned segment 3 should travel from "X1-FUEL-2" to "X1-B2"
    And planned segment 3 should not require refuel

  Scenario: Handle route with only REFUEL steps (edge case)
    Given the routing client returns a route with only refuel steps
    When I plan a route from ship location to "X1-B2"
    Then route planning should fail with error "route plan has no TRAVEL steps"

  Scenario: Generate correct route ID
    Given the routing client returns a single-step route with total time 120 seconds
    When I plan a route from ship location to "X1-B2"
    Then the planned route should be created successfully
    And the planned route ID should be "TEST-SHIP_120"

  Scenario: Handle missing waypoint in cache during route conversion
    Given the routing client returns a route to unknown waypoint "X1-UNKNOWN"
    When I plan a route from ship location to "X1-B2"
    Then route planning should fail with error "waypoint X1-UNKNOWN not found in cache"
