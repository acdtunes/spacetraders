Feature: Advanced Smart Navigator Scenarios
  Advanced edge cases and comprehensive coverage for SmartNavigator

  Background:
    Given a mock API client
    And the system "X1-HU87" has waypoints:
      | symbol     | type        | x    | y   | traits                    |
      | X1-HU87-A1 | PLANET      | 0    | 0   | MARKETPLACE               |
      | X1-HU87-B2 | MOON        | 100  | 0   | MARKETPLACE,SHIPYARD      |
      | X1-HU87-C3 | ASTEROID    | 200  | 0   | COMMON_METAL_DEPOSITS     |
      | X1-HU87-D4 | GAS_GIANT   | 300  | 0   | MARKETPLACE               |
      | X1-HU87-E5 | ASTEROID    | 400  | 0   | STRIPPED                  |

  # GRAPH BUILDING & CACHING (Lines 55-60)
  Scenario: Graph building fails when API returns no data
    Given the API will return empty waypoint data for system "X1-TEST"
    When I initialize SmartNavigator for system "X1-TEST" without a pre-built graph
    Then an exception should be raised with message "Failed to build graph for system X1-TEST"

  # FUEL ESTIMATE METHOD (Lines 124-139)
  Scenario: Get fuel estimate for multi-hop route with refuel stops
    Given a ship "TEST-1" at "X1-HU87-A1" with 50 fuel and 400 capacity
    And a smart navigator for system "X1-HU87"
    When I get fuel estimate for route to "X1-HU87-D4"
    Then the fuel estimate should contain:
      | field            | value |
      | total_fuel_cost  | >0    |
      | refuel_stops     | >=0   |
      | feasible         | True  |
    And the estimate should have "total_time" and "final_fuel" fields

  Scenario: Get fuel estimate returns None for impossible route
    Given a ship "TEST-1" at "X1-HU87-A1" with 0 fuel and 400 capacity
    And a smart navigator for system "X1-HU87"
    When I get fuel estimate for route to "X1-HU87-E5"
    Then the fuel estimate should be None

  # STATE TRANSITION HANDLERS (Lines 170-186)
  Scenario: IN_TRANSIT to IN_ORBIT transition waits for arrival
    Given a ship "TEST-1" is IN_TRANSIT to "X1-HU87-B2" arriving in 5 seconds
    And a ship controller for "TEST-1"
    And a smart navigator for system "X1-HU87"
    When I ensure the ship is in state "IN_ORBIT"
    Then the navigator should wait for arrival
    And the ship should be in state "IN_ORBIT"

  Scenario: IN_TRANSIT to DOCKED transition waits then docks
    Given a ship "TEST-1" is IN_TRANSIT to "X1-HU87-B2" arriving in 5 seconds
    And a ship controller for "TEST-1"
    And a smart navigator for system "X1-HU87"
    When I ensure the ship is in state "DOCKED"
    Then the navigator should wait for arrival
    And the ship should be in state "DOCKED"

  # STATE VALIDATION ERRORS (Lines 207-208, 215-216)
  Scenario: State validation fails when ship status unavailable
    Given a ship controller for "TEST-1" that returns no status
    And a smart navigator for system "X1-HU87"
    When I ensure the ship is in state "IN_ORBIT"
    Then the state validation should fail

  Scenario: Invalid state transition is rejected
    Given a ship "TEST-1" is DOCKED at "X1-HU87-A1"
    And a ship controller for "TEST-1"
    And a smart navigator for system "X1-HU87"
    When I attempt to transition from "DOCKED" to "IN_TRANSIT"
    Then the transition should fail with error "Invalid state transition"

  # SHIP HEALTH VALIDATION (Lines 239, 244, 251)
  Scenario: Ship with critical damage cannot navigate
    Given a ship "TEST-1" at "X1-HU87-A1" with 40% integrity
    And a ship controller for "TEST-1"
    And a smart navigator for system "X1-HU87"
    When I execute route to "X1-HU87-B2"
    Then the navigation should fail
    And the error should mention "Critical damage"

  Scenario: Ship with no fuel capacity cannot navigate
    Given a ship "TEST-1" at "X1-HU87-A1" with 0 fuel capacity
    And a ship controller for "TEST-1"
    And a smart navigator for system "X1-HU87"
    When I execute route to "X1-HU87-B2"
    Then the navigation should fail
    And the error should mention "no fuel capacity"

  Scenario: Ship with moderate damage shows warning but continues
    Given a ship "TEST-1" at "X1-HU87-A1" with 70% integrity
    And a ship controller for "TEST-1"
    And a smart navigator for system "X1-HU87"
    When I execute route to "X1-HU87-B2"
    Then a warning should be logged about ship damage

  Scenario: Ship with active cooldown shows warning
    Given a ship "TEST-1" at "X1-HU87-A1" with 30 seconds cooldown
    And a ship controller for "TEST-1"
    And a smart navigator for system "X1-HU87"
    When I execute route to "X1-HU87-B2"
    Then a warning should be logged about cooldown

  # IN_TRANSIT HANDLING (Lines 280-281, 309-318)
  Scenario: Ship already in transit to destination waits and arrives
    Given a ship "TEST-1" is IN_TRANSIT to "X1-HU87-B2" arriving in 3 seconds
    And a ship controller for "TEST-1"
    And a smart navigator for system "X1-HU87"
    When I execute route to "X1-HU87-B2"
    Then the navigation should succeed
    And the ship should arrive at "X1-HU87-B2"

  Scenario: Ship in transit to different destination waits then replans
    Given a ship "TEST-1" is IN_TRANSIT to "X1-HU87-C3" arriving in 3 seconds
    And a ship controller for "TEST-1"
    And a smart navigator for system "X1-HU87"
    When I execute route to "X1-HU87-B2"
    Then the navigator should wait for arrival at "X1-HU87-C3"
    And then plan new route to "X1-HU87-B2"

  Scenario: Failed to get ship status after IN_TRANSIT wait
    Given a ship "TEST-1" is IN_TRANSIT to "X1-HU87-B2" arriving in 2 seconds
    And the ship controller will fail to get status after arrival
    And a smart navigator for system "X1-HU87"
    When I execute route to "X1-HU87-B2"
    Then the navigation should fail
    And the error should mention "Failed to get ship status"

  # NO ROUTE FOUND (Lines 323-324)
  Scenario: No route found due to insufficient fuel
    Given a ship "TEST-1" at "X1-HU87-E5" with 0 fuel and 400 capacity
    And a smart navigator for system "X1-HU87"
    When I execute route to "X1-HU87-A1"
    Then the navigation should fail
    And the error should mention "No route found"

  # OPERATION CONTROLLER (Lines 335-355, 397)
  Scenario: Navigation resumes from checkpoint after interruption
    Given a ship "TEST-1" at "X1-HU87-A1" with 400 fuel
    And an operation controller with checkpoint at step 2
    And a smart navigator for system "X1-HU87"
    When I execute route to "X1-HU87-D4" with operation controller
    Then the route should resume from step 3
    And steps 1-2 should be skipped

  Scenario: Navigation pauses when operation controller signals pause
    Given a ship "TEST-1" at "X1-HU87-A1" with 400 fuel
    And an operation controller that will signal pause at step 2
    And a smart navigator for system "X1-HU87"
    When I execute route to "X1-HU87-D4" with operation controller
    Then the navigation should pause
    And the operation should be marked as paused

  Scenario: Navigation cancels when operation controller signals cancel
    Given a ship "TEST-1" at "X1-HU87-A1" with 400 fuel
    And an operation controller that will signal cancel at step 2
    And a smart navigator for system "X1-HU87"
    When I execute route to "X1-HU87-D4" with operation controller
    Then the navigation should cancel
    And the operation should be marked as cancelled

  Scenario: Checkpoints are saved after each navigation step
    Given a ship "TEST-1" at "X1-HU87-A1" with 400 fuel
    And an operation controller for tracking checkpoints
    And a smart navigator for system "X1-HU87"
    When I execute route to "X1-HU87-C3" with operation controller
    Then checkpoints should be saved after each step
    And each checkpoint should contain location, fuel, and state

  # NAVIGATION ERROR HANDLING (Lines 363-364, 374-375, 380-381, 387-388, 391)
  Scenario: Navigation fails when orbit transition fails
    Given a ship "TEST-1" is DOCKED at "X1-HU87-A1"
    And the ship controller will fail to orbit
    And a smart navigator for system "X1-HU87"
    When I execute route to "X1-HU87-B2"
    Then the navigation should fail
    And the error should mention "Failed to transition to IN_ORBIT"

  Scenario: Navigation fails when ship controller navigate fails
    Given a ship "TEST-1" at "X1-HU87-A1" with 400 fuel
    And the ship controller will fail navigation
    And a smart navigator for system "X1-HU87"
    When I execute route to "X1-HU87-B2"
    Then the navigation should fail
    And the error should mention "Navigation failed"

  Scenario: Navigation fails when status check after move fails
    Given a ship "TEST-1" at "X1-HU87-A1" with 400 fuel
    And the ship controller will fail status check after navigation
    And a smart navigator for system "X1-HU87"
    When I execute route to "X1-HU87-B2"
    Then the navigation should fail
    And the error should mention "Failed to get ship status after navigation"

  Scenario: Navigation detects wrong arrival location
    Given a ship "TEST-1" at "X1-HU87-A1" with 400 fuel
    And the ship will arrive at wrong location "X1-HU87-C3"
    And a smart navigator for system "X1-HU87"
    When I execute route to "X1-HU87-B2"
    Then the navigation should fail
    And the error should mention "Navigation error: expected X1-HU87-B2"

  Scenario: Unexpected state after navigation logs warning
    Given a ship "TEST-1" at "X1-HU87-A1" with 400 fuel
    And the ship will be DOCKED after navigation instead of IN_ORBIT
    And a smart navigator for system "X1-HU87"
    When I execute route to "X1-HU87-B2"
    Then a warning should be logged about unexpected state

  # REFUEL STEP HANDLING (Lines 410-411, 419-420, 429)
  Scenario: Refuel step requires DOCKED state
    Given a ship "TEST-1" at "X1-HU87-A1" with 50 fuel
    And a route that requires refuel at "X1-HU87-B2"
    And a ship controller for "TEST-1"
    And a smart navigator for system "X1-HU87"
    When I execute the multi-hop route
    Then the ship should dock before refueling
    And the refuel should succeed

  Scenario: Refuel step fails when dock fails
    Given a ship "TEST-1" at "X1-HU87-A1" with 50 fuel
    And a route that requires refuel at "X1-HU87-B2"
    And the ship controller will fail to dock
    And a smart navigator for system "X1-HU87"
    When I execute the multi-hop route
    Then the navigation should fail
    And the error should mention "Failed to dock for refueling"

  Scenario: Refuel step fails when refuel operation fails
    Given a ship "TEST-1" at "X1-HU87-B2" with 50 fuel
    And the ship controller will fail to refuel
    And a smart navigator for system "X1-HU87"
    When I execute route with required refuel
    Then the navigation should fail
    And the error should mention "Refuel failed"

  Scenario: Checkpoint saved after successful refuel
    Given a ship "TEST-1" at "X1-HU87-A1" with 50 fuel
    And a route that requires refuel at "X1-HU87-B2"
    And an operation controller for tracking checkpoints
    And a smart navigator for system "X1-HU87"
    When I execute the multi-hop route with operation controller
    Then a checkpoint should be saved after refuel
    And the checkpoint should show DOCKED state and increased fuel

  # FINAL VERIFICATION (Lines 439-440, 447-448)
  Scenario: Final verification warning when status unavailable
    Given a ship "TEST-1" at "X1-HU87-A1" with 400 fuel
    And the ship controller will fail final status check
    And a smart navigator for system "X1-HU87"
    When I execute route to "X1-HU87-B2"
    Then a warning should be logged about verification
    But the navigation should still succeed

  Scenario: Final verification detects wrong destination
    Given a ship "TEST-1" at "X1-HU87-A1" with 400 fuel
    And the final location will be "X1-HU87-C3" instead of "X1-HU87-B2"
    And a smart navigator for system "X1-HU87"
    When I execute route to "X1-HU87-B2"
    Then the navigation should fail
    And the error should mention "ended at X1-HU87-C3, expected X1-HU87-B2"

  # FIND NEAREST WITH TRAIT (Lines 466-490)
  Scenario: Find nearest waypoints with specific trait
    Given a ship "TEST-1" at "X1-HU87-C3" with 400 fuel
    And a smart navigator for system "X1-HU87"
    When I search for nearest waypoints with trait "MARKETPLACE"
    Then the results should be sorted by distance
    And "X1-HU87-A1" should be closer than "X1-HU87-D4"
    And the results should include waypoint type and traits

  Scenario: Find nearest with trait limits results
    Given a ship "TEST-1" at "X1-HU87-A1" with 400 fuel
    And a smart navigator for system "X1-HU87"
    When I search for nearest waypoints with trait "MARKETPLACE" limited to 2 results
    Then exactly 2 results should be returned
    And they should be the 2 closest MARKETPLACE waypoints

  Scenario: Find nearest with trait returns empty for non-existent trait
    Given a ship "TEST-1" at "X1-HU87-A1" with 400 fuel
    And a smart navigator for system "X1-HU87"
    When I search for nearest waypoints with trait "SHIPYARD_DELUXE"
    Then no results should be returned

  # ROUTE VALIDATION WITH REFUEL (Line 109)
  Scenario: Route validation detects refuel requirement
    Given a ship "TEST-1" at "X1-HU87-A1" with 150 fuel and 400 capacity
    And a smart navigator for system "X1-HU87"
    When I validate route to "X1-HU87-E5"
    Then the route should be valid
    And the reason should mention "refuel stop"

  # ALREADY AT DESTINATION (Lines 294-295)
  Scenario: Navigation succeeds when already at destination
    Given a ship "TEST-1" at "X1-HU87-B2" with 400 fuel
    And a smart navigator for system "X1-HU87"
    When I execute route to "X1-HU87-B2"
    Then the navigation should succeed immediately
    And no waypoints should be traversed
