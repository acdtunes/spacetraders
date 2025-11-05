Feature: Route Planning
  As a ship operator
  I want to plan navigation routes
  So that I can travel efficiently between waypoints

  Background:
    Given the navigation system is initialized

  # Basic Route Planning
  Scenario: Plan route with sufficient fuel
    Given a ship with 500 fuel capacity at waypoint "X1-A1"
    And the ship has 400 current fuel
    And waypoint "X1-B2" is 200 units away
    When I create a route to "X1-B2"
    Then the route should have 1 segment
    And the segment should use CRUISE mode
    And the segment should require 200 fuel
    And the route status should be PLANNED

  Scenario: Plan route with exact fuel needed
    Given a ship with 300 fuel capacity at waypoint "X1-A1"
    And the ship has 104 current fuel
    And waypoint "X1-B2" is 100 units away
    When I create a route to "X1-B2"
    Then the route should have 1 segment
    And the route should be created successfully
    And the segment should use CRUISE mode
    And the segment should require 100 fuel

  Scenario: Plan multi-segment route
    Given waypoints "X1-A1", "X1-B2", "X1-C3" form a connected path
    And the ship has sufficient fuel for the journey
    When I create a route from "X1-A1" to "X1-C3" via "X1-B2"
    Then the route should have 2 segments
    And segment 1 should go from "X1-A1" to "X1-B2"
    And segment 2 should go from "X1-B2" to "X1-C3"

  # Route Validation
  Scenario: Route segments must be connected
    Given waypoints "X1-A1", "X1-B2", "X1-C3"
    When I create a route with disconnected segments
    Then route creation should fail with InvalidRouteError
    And the error message should mention "not connected"

  Scenario: Route must have at least one segment
    When I attempt to create a route with no segments
    Then route creation should fail with ValueError
    And the error message should mention "at least one segment"

  Scenario: Route fuel requirement cannot exceed ship capacity
    Given a ship with 10 fuel capacity at waypoint "X1-A1"
    And waypoint "X1-B2" is 10000 units away
    When I attempt to create a route to "X1-B2"
    Then route creation should fail with ValueError
    And the error message should mention "ship capacity"

  # Flight Mode Selection
  Scenario: Flight mode selection based on fuel
    Given a ship with 80% fuel
    And waypoint "X1-B2" is 150 units away
    When I create a route to "X1-B2"
    Then the segment should use BURN mode

  Scenario: Flight mode selection with low fuel
    Given a ship with 10% fuel
    And waypoint "X1-B2" is 100 units away
    When I create a route to "X1-B2"
    Then the segment should use DRIFT mode

  Scenario: Flight mode selection at threshold
    Given a ship with exactly 21% fuel
    And waypoint "X1-B2" is 100 units away
    When I create a route to "X1-B2"
    Then the segment should use CRUISE mode

  # Refueling Scenarios
  Scenario: Refuel needed for long distance
    Given a ship with 100 fuel at "X1-A1"
    And destination "X1-Z9" is 500 units away
    And refuel point "X1-M5" is 80 units away from "X1-A1"
    And refuel point "X1-M5" is 420 units away from "X1-Z9"
    When I plan the route with refuel planning
    Then the route should include a refuel stop at "X1-M5"
    And the route should have 2 segments
    And segment 1 should require refuel

  Scenario: No refuel needed for direct travel
    Given a ship with 500 fuel at "X1-A1"
    And destination "X1-B2" is 200 units away
    When I plan the route with refuel planning
    Then the route should not include any refuel stops
    And the route should have 1 segment

  # Route Execution Lifecycle
  Scenario: Start route execution
    Given a planned route from "X1-A1" to "X1-B2"
    When I start route execution
    Then the route status should be EXECUTING
    And the current segment index should be 0

  Scenario: Cannot start route that is not planned
    Given a route in EXECUTING status
    When I attempt to start route execution
    Then the operation should fail with ValueError
    And the error message should mention "Cannot start route"

  Scenario: Complete route segment
    Given an executing route with 3 segments
    And the current segment index is 0
    When I complete the current segment
    Then the current segment index should be 1
    And the route status should still be EXECUTING

  Scenario: Complete entire route
    Given an executing route with 1 segment
    And the current segment index is 0
    When I complete the current segment
    Then the route status should be COMPLETED
    And there should be no current segment

  Scenario: Complete final segment of multi-segment route
    Given an executing route with 3 segments
    And the current segment index is 2
    When I complete the current segment
    Then the route status should be COMPLETED
    And the current segment index should be 3

  Scenario: Cannot complete segment when route is not executing
    Given a planned route
    When I attempt to complete the current segment
    Then the operation should fail with ValueError
    And the error message should mention "Cannot complete segment"

  # Route Status Transitions
  Scenario: Fail route during execution
    Given an executing route
    When I fail the route with reason "Ship damaged"
    Then the route status should be FAILED

  Scenario: Abort route during execution
    Given an executing route
    When I abort the route with reason "Player cancelled"
    Then the route status should be ABORTED

  # Route Metrics
  Scenario: Calculate total distance
    Given a route with segments of distance 100, 200, 150 units
    Then the total distance should be 450 units

  Scenario: Calculate total fuel required
    Given a route with segments requiring 50, 100, 75 fuel
    Then the total fuel required should be 225

  Scenario: Calculate total travel time
    Given a route with segments taking 120, 180, 90 seconds
    Then the total travel time should be 390 seconds

  # Edge Cases
  Scenario: Zero distance orbital hop
    Given a ship at waypoint "X1-A1-ORBITAL"
    And waypoint "X1-A1" is the parent planet
    When I create a route to "X1-A1"
    Then the route should have 1 segment
    And the segment should have 0 distance
    And the segment should require 1 fuel

  Scenario: Route to same waypoint
    Given a ship at waypoint "X1-A1"
    When I attempt to create a route to "X1-A1"
    Then route creation should fail with ValueError
    And the error message should mention "same waypoint"

  # Current and Remaining Segments
  Scenario: Get current segment during execution
    Given an executing route with 3 segments
    And the current segment index is 1
    When I get the current segment
    Then it should be segment 2

  Scenario: Get remaining segments
    Given a route with 5 segments
    And the current segment index is 2
    When I get remaining segments
    Then there should be 3 remaining segments
    And the first remaining segment should be segment 3

  # Refuel Before Departure
  Scenario: Route creation detects need for refuel before departure
    Given a ship with 400 fuel capacity at waypoint "X1-C48"
    And the ship has 57 current fuel
    And waypoint "X1-C48" has MARKETPLACE trait
    And waypoint "X1-J74" is 300 units away from "X1-C48"
    And waypoint "X1-MID" is 80 units away from "X1-C48"
    And waypoint "X1-MID" has MARKETPLACE trait
    When I plan a route from "X1-C48" to "X1-J74" with routing engine
    Then the route should have refuel_before_departure set to true
    And the first action should be refuel at current location
