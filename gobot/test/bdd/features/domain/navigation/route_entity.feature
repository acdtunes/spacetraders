Feature: Route Entity
  As a navigation system
  I want to manage routes and route segments
  So that ships can navigate between waypoints

  Background:
    Given test waypoints are available:
      | symbol  | x     | y    |
      | X1-A1   | 0.0   | 0.0  |
      | X1-B2   | 100.0 | 0.0  |
      | X1-C3   | 200.0 | 0.0  |

  # ============================================================================
  # RouteSegment Value Object Tests
  # ============================================================================

  Scenario: Create route segment with required fields
    When I create a route segment from "X1-A1" to "X1-B2" with:
      | distance      | 100.0  |
      | fuel_required | 50     |
      | travel_time   | 100    |
      | flight_mode   | CRUISE |
    Then the segment should have from_waypoint "X1-A1"
    And the segment should have to_waypoint "X1-B2"
    And the segment should have distance 100.0
    And the segment should have fuel_required 50
    And the segment should have travel_time 100
    And the segment should have flight_mode "CRUISE"
    And the segment should have requires_refuel false

  Scenario: Create route segment with refuel flag
    When I create a route segment from "X1-A1" to "X1-B2" with:
      | distance        | 100.0  |
      | fuel_required   | 50     |
      | travel_time     | 100    |
      | flight_mode     | CRUISE |
      | requires_refuel | true   |
    Then the segment should have requires_refuel true

  # ============================================================================
  # Route Initialization Tests
  # ============================================================================

  Scenario: Create route with valid data
    Given a route segment from "X1-A1" to "X1-B2" with distance 100.0
    And a route segment from "X1-B2" to "X1-C3" with distance 100.0
    When I create a route with:
      | route_id           | route-1 |
      | ship_symbol        | SHIP-1  |
      | player_id          | 1       |
      | ship_fuel_capacity | 100     |
    Then the route should have route_id "route-1"
    And the route should have ship_symbol "SHIP-1"
    And the route should have player_id 1
    And the route should have 2 segments
    And the route should have status "PLANNED"

  Scenario: Can create route with empty segments for already-at-destination case
    When I create a route with empty segments
    Then the route should have 0 segments
    And the route should have status "PLANNED"

  Scenario: Cannot create route with disconnected segments
    Given a route segment from "X1-A1" to "X1-B2" with distance 100.0
    And a route segment from "X1-A1" to "X1-C3" with distance 100.0
    When I attempt to create a route with disconnected segments
    Then the route creation should fail with error "segments not connected"

  Scenario: Cannot create route when segment exceeds fuel capacity
    Given a route segment from "X1-A1" to "X1-B2" requiring 150 fuel
    When I attempt to create a route with fuel capacity 100
    Then the route creation should fail with error "requires 150 fuel but ship capacity is 100"

  Scenario: Allow segment at capacity limit
    Given a route segment from "X1-A1" to "X1-B2" requiring 100 fuel
    When I create a route with fuel capacity 100
    Then the route should be created successfully

  # ============================================================================
  # Route Execution Tests
  # ============================================================================

  Scenario: Route starts in planned status
    Given a newly created route
    Then the route should have status "PLANNED"

  Scenario: Start execution transitions to executing
    Given a route in "PLANNED" status
    When I start route execution
    Then the route should have status "EXECUTING"

  Scenario: Cannot start execution when already executing
    Given a route in "EXECUTING" status
    When I attempt to start route execution
    Then the operation should fail with error "cannot start route in status EXECUTING"

  Scenario: Cannot start execution when completed
    Given a route in "COMPLETED" status
    When I attempt to start route execution
    Then the operation should fail with error "cannot start route in status COMPLETED"

  Scenario: Complete segment advances index
    Given a route in "EXECUTING" status
    And the current_segment_index is 0
    When I complete the current segment
    Then the current_segment_index should be 1

  Scenario: Complete segment multiple times
    Given a route in "EXECUTING" status with 2 segments
    When I complete the current segment
    And I complete the current segment
    Then the current_segment_index should be 2

  Scenario: Complete segment transitions to completed when done
    Given a route in "EXECUTING" status with 2 segments
    When I complete the current segment
    Then the route should have status "EXECUTING"
    When I complete the current segment
    Then the route should have status "COMPLETED"

  Scenario: Cannot complete segment when not executing
    Given a route in "PLANNED" status
    When I attempt to complete the current segment
    Then the operation should fail with error "cannot complete segment when route status is PLANNED"

  Scenario: Fail route transitions to failed
    Given a route in "EXECUTING" status
    When I fail the route with reason "Navigation error"
    Then the route should have status "FAILED"

  Scenario: Abort route transitions to aborted
    Given a route in "EXECUTING" status
    When I abort the route with reason "User cancelled"
    Then the route should have status "ABORTED"

  # ============================================================================
  # Route Calculations Tests
  # ============================================================================

  Scenario: Calculate total distance
    Given a route with segments having distances:
      | 100.0 |
      | 100.0 |
    When I calculate the total distance
    Then the total distance should be 200.0

  Scenario: Calculate total fuel required
    Given a route with segments requiring fuel:
      | 50 |
      | 50 |
    When I calculate the total fuel required
    Then the total fuel required should be 100

  Scenario: Calculate total travel time
    Given a route with segments having travel times:
      | 100 |
      | 100 |
    When I calculate the total travel time
    Then the total travel time should be 200

  Scenario: Calculations with single segment
    Given a route with a single segment:
      | distance      | 100.0 |
      | fuel_required | 50    |
      | travel_time   | 100   |
    When I calculate route totals
    Then the total distance should be 100.0
    And the total fuel required should be 50
    And the total travel time should be 100

  # ============================================================================
  # Current Segment Tests
  # ============================================================================

  Scenario: Get first segment initially
    Given a route with 2 segments
    When I get the current segment
    Then it should be the first segment

  Scenario: Get next segment after completion
    Given a route in "EXECUTING" status with 2 segments
    When I complete the current segment
    And I get the current segment
    Then it should be the second segment

  Scenario: Returns nil when route completed
    Given a route in "EXECUTING" status with 2 segments
    When I complete the current segment
    And I complete the current segment
    And I get the current segment
    Then the current segment should be nil

  # ============================================================================
  # Remaining Segments Tests
  # ============================================================================

  Scenario: Returns all segments initially
    Given a route with 2 segments
    When I get the remaining segments
    Then there should be 2 remaining segments

  Scenario: Returns remaining after completion
    Given a route in "EXECUTING" status with 2 segments
    When I complete the current segment
    And I get the remaining segments
    Then there should be 1 remaining segment
    And the remaining segment should be the second segment

  Scenario: Returns empty list when completed
    Given a route in "EXECUTING" status with 2 segments
    When I complete the current segment
    And I complete the current segment
    And I get the remaining segments
    Then there should be 0 remaining segments

  # ============================================================================
  # Get Current Segment Index Tests
  # ============================================================================

  Scenario: Returns zero initially
    Given a newly created route
    When I get the current segment index
    Then the index should be 0

  Scenario: Returns updated index after completion
    Given a route in "EXECUTING" status with 2 segments
    When I complete the current segment
    And I get the current segment index
    Then the index should be 1

  Scenario: Returns segment count when completed
    Given a route in "EXECUTING" status with 2 segments
    When I complete the current segment
    And I complete the current segment
    And I get the current segment index
    Then the index should be 2

  # ============================================================================
  # Route Query Tests
  # ============================================================================

  Scenario: Has refuel at start when route requires refuel before departure
    Given a route with refuel_before_departure true
    Then HasRefuelAtStart should return true

  Scenario: Does not have refuel at start when route does not require refuel before departure
    Given a route with refuel_before_departure false
    Then HasRefuelAtStart should return false

  Scenario: NextSegment returns first segment initially
    Given a route with 2 segments
    When I call NextSegment
    Then it should return the first segment

  Scenario: NextSegment returns current segment when executing
    Given a route in "EXECUTING" status with 2 segments
    And the current_segment_index is 1
    When I call NextSegment
    Then it should return the second segment

  Scenario: NextSegment returns nil when route completed
    Given a route in "COMPLETED" status with 2 segments
    When I call NextSegment
    Then it should return nil

  Scenario: IsComplete returns false when route is planned
    Given a route in "PLANNED" status
    When I call IsComplete
    Then it should return false

  Scenario: IsComplete returns false when route is executing
    Given a route in "EXECUTING" status
    When I call IsComplete
    Then it should return false

  Scenario: IsComplete returns true when route is completed
    Given a route in "COMPLETED" status
    When I call IsComplete
    Then it should return true

  Scenario: IsFailed returns false when route is planned
    Given a route in "PLANNED" status
    When I call IsFailed
    Then it should return false

  Scenario: IsFailed returns false when route is executing
    Given a route in "EXECUTING" status
    When I call IsFailed
    Then it should return false

  Scenario: IsFailed returns true when route is failed
    Given a route in "FAILED" status
    When I call IsFailed
    Then it should return true
