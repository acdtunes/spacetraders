Feature: Route Entity
  As a SpaceTraders bot
  I want to manage route entities with proper validation and execution
  So that I can plan and execute multi-hop navigation safely

  # ============================================================================
  # Route Creation and Validation
  # ============================================================================

  Scenario: Create valid connected route
    Given waypoints:
      | symbol | x   | y   |
      | X1-A1  | 0   | 0   |
      | X1-B1  | 10  | 0   |
      | X1-C1  | 20  | 0   |
    And route segments:
      | from  | to    | distance | fuel | time | mode   |
      | X1-A1 | X1-B1 | 10.0     | 5    | 100  | CRUISE |
      | X1-B1 | X1-C1 | 10.0     | 5    | 100  | CRUISE |
    When I create a route for ship "SHIP-1", player 1, fuel_capacity 100
    Then the route should be valid
    And the route status should be "PLANNED"
    And the route should have 2 segments

  Scenario: Reject disconnected route segments
    Given waypoints:
      | symbol | x   | y   |
      | X1-A1  | 0   | 0   |
      | X1-B1  | 10  | 0   |
      | X1-C1  | 20  | 0   |
      | X1-D1  | 30  | 0   |
    And route segments:
      | from  | to    | distance | fuel | time | mode   |
      | X1-A1 | X1-B1 | 10.0     | 5    | 100  | CRUISE |
      | X1-C1 | X1-D1 | 10.0     | 5    | 100  | CRUISE |
    When I attempt to create a route for ship "SHIP-1", player 1, fuel_capacity 100
    Then route creation should fail with error "segments not connected: X1-B1 → X1-C1"

  Scenario: Reject route with segment exceeding fuel capacity
    Given waypoints:
      | symbol | x   | y   |
      | X1-A1  | 0   | 0   |
      | X1-B1  | 100 | 0   |
    And route segments:
      | from  | to    | distance | fuel | time | mode |
      | X1-A1 | X1-B1 | 100.0    | 150  | 500  | BURN |
    When I attempt to create a route for ship "SHIP-1", player 1, fuel_capacity 100
    Then route creation should fail with error "segment requires 150 fuel but ship capacity is 100"

  Scenario: Create route with refuel before departure
    Given waypoints:
      | symbol | x   | y   |
      | X1-A1  | 0   | 0   |
      | X1-B1  | 10  | 0   |
    And route segments:
      | from  | to    | distance | fuel | time | mode   |
      | X1-A1 | X1-B1 | 10.0     | 5    | 100  | CRUISE |
    When I create a route for ship "SHIP-1", player 1, fuel_capacity 100, refuel_before_departure true
    Then the route should be valid
    And the route should require refuel at start

  Scenario: Create empty route with no segments
    When I create a route for ship "SHIP-1", player 1, fuel_capacity 100 with no segments
    Then the route should be valid
    And the route should have 0 segments

  # ============================================================================
  # Route Execution State Machine
  # ============================================================================

  Scenario: Start route execution from planned state
    Given a valid route in "PLANNED" state
    When I start route execution
    Then the route status should be "EXECUTING"

  Scenario: Cannot start route already executing
    Given a valid route in "EXECUTING" state
    When I attempt to start route execution
    Then the route operation should fail with error "cannot start route in status EXECUTING"

  Scenario: Cannot start completed route
    Given a valid route in "COMPLETED" state
    When I attempt to start route execution
    Then the route operation should fail with error "cannot start route in status COMPLETED"

  Scenario: Complete segment advances to next
    Given a valid route in "EXECUTING" state at segment 0 of 3
    When I complete the current segment
    Then the route current segment index should be 1
    And the route status should be "EXECUTING"

  Scenario: Complete final segment marks route complete
    Given a valid route in "EXECUTING" state at segment 2 of 3
    When I complete the current segment
    Then the route status should be "COMPLETED"
    And the route current segment index should be 3

  Scenario: Cannot complete segment when not executing
    Given a valid route in "PLANNED" state
    When I attempt to complete the current segment
    Then the route operation should fail with error "cannot complete segment when route status is PLANNED"

  Scenario: Fail route from executing state
    Given a valid route in "EXECUTING" state
    When I fail the route with reason "navigation error"
    Then the route status should be "FAILED"

  Scenario: Abort route from executing state
    Given a valid route in "EXECUTING" state
    When I abort the route with reason "user cancelled"
    Then the route status should be "ABORTED"

  # ============================================================================
  # Route Queries
  # ============================================================================

  Scenario: Calculate total distance of route
    Given waypoints:
      | symbol | x   | y   |
      | X1-A1  | 0   | 0   |
      | X1-B1  | 10  | 0   |
      | X1-C1  | 25  | 0   |
    And route segments:
      | from  | to    | distance | fuel | time | mode   |
      | X1-A1 | X1-B1 | 10.0     | 5    | 100  | CRUISE |
      | X1-B1 | X1-C1 | 15.0     | 8    | 150  | CRUISE |
    And a valid route in "PLANNED" state
    When I calculate total distance
    Then the total distance should be 25.0

  Scenario: Calculate total fuel required
    Given waypoints:
      | symbol | x   | y   |
      | X1-A1  | 0   | 0   |
      | X1-B1  | 10  | 0   |
      | X1-C1  | 20  | 0   |
    And route segments:
      | from  | to    | distance | fuel | time | mode   |
      | X1-A1 | X1-B1 | 10.0     | 5    | 100  | CRUISE |
      | X1-B1 | X1-C1 | 10.0     | 7    | 120  | CRUISE |
    And a valid route in "PLANNED" state
    When I calculate total fuel required
    Then the total fuel required should be 12

  Scenario: Calculate total travel time
    Given waypoints:
      | symbol | x   | y   |
      | X1-A1  | 0   | 0   |
      | X1-B1  | 10  | 0   |
      | X1-C1  | 20  | 0   |
    And route segments:
      | from  | to    | distance | fuel | time | mode   |
      | X1-A1 | X1-B1 | 10.0     | 5    | 100  | CRUISE |
      | X1-B1 | X1-C1 | 10.0     | 7    | 150  | CRUISE |
    And a valid route in "PLANNED" state
    When I calculate total travel time
    Then the total travel time should be 250 seconds

  Scenario: Get current segment during execution
    Given a valid route in "EXECUTING" state at segment 1 of 3
    When I get the current segment
    Then the current segment should be from "X1-B1" to "X1-C1"

  Scenario: Get current segment returns nil when route complete
    Given a valid route in "COMPLETED" state
    When I get the current segment
    Then the current segment should be nil

  Scenario: Get remaining segments at start
    Given a valid route in "EXECUTING" state at segment 0 of 3
    When I get the remaining segments
    Then there should be 3 remaining segments

  Scenario: Get remaining segments mid-route
    Given a valid route in "EXECUTING" state at segment 1 of 3
    When I get the remaining segments
    Then there should be 2 remaining segments

  Scenario: Get remaining segments returns empty when complete
    Given a valid route in "COMPLETED" state
    When I get the remaining segments
    Then there should be 0 remaining segments

  # ============================================================================
  # Route State Checks
  # ============================================================================

  Scenario: IsComplete returns true for completed route
    Given a valid route in "COMPLETED" state
    When I check if route is complete
    Then the route is complete

  Scenario: IsComplete returns false for executing route
    Given a valid route in "EXECUTING" state
    When I check if route is complete
    Then the route is not complete

  Scenario: IsFailed returns true for failed route
    Given a valid route in "FAILED" state
    When I check if route is failed
    Then the route is failed

  Scenario: IsFailed returns false for executing route
    Given a valid route in "EXECUTING" state
    When I check if route is failed
    Then the route is not failed

  # ============================================================================
  # Route Segment Immutability
  # ============================================================================

  Scenario: Route segments are defensive copies
    Given a valid route with 2 segments
    When I get the route segments
    And I modify the returned segments array
    Then the original route segments should be unchanged

  Scenario: Remaining segments are defensive copies
    Given a valid route in "EXECUTING" state at segment 0 of 3
    When I get the remaining segments
    And I modify the returned segments array
    Then the original route segments should be unchanged
