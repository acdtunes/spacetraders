Feature: Ship In-Transit Wait Handling
  As a ship operator
  I want the navigation system to wait for ships to arrive before proceeding to next segment
  So that multi-hop routes don't fail with "Ship is currently in-transit" errors

  Background:
    Given the navigation system is initialized

  Scenario: Ship waits for arrival before navigating to next segment in multi-hop route
    Given a test ship "TEST-SHIP-WAIT" is registered for in-transit wait test
    And the ship is at waypoint "X1-ALPHA" with 400 fuel and 400 capacity
    And waypoint "X1-BETA" is 100 units away without marketplace
    And waypoint "X1-GAMMA" is 150 units away from "X1-BETA"
    And the route from "X1-ALPHA" to "X1-GAMMA" requires 2 segments
    When I navigate from "X1-ALPHA" to "X1-GAMMA" via "X1-BETA"
    Then the ship should navigate to "X1-BETA" and enter IN_TRANSIT
    And the system should detect the ship is IN_TRANSIT with arrival time
    And the system should calculate the wait time until arrival
    And the system should sleep for the calculated wait time plus buffer
    And the system should sync ship state after arrival showing IN_ORBIT
    And the ship should then navigate to "X1-GAMMA" successfully
    And the final ship location should be "X1-GAMMA"

  Scenario: Ship in IN_TRANSIT state cannot navigate to next waypoint
    Given a test ship "TEST-SHIP-BLOCKED" is registered for transit block test
    And the ship is IN_TRANSIT to "X1-TARGET" with arrival in 40 seconds
    When I attempt to navigate the ship without waiting for arrival
    Then the navigation should fail with "currently in-transit" error
    And the error message should mention arrival time

  Scenario: Multi-hop navigation with proper wait handles 3-segment route
    Given a test ship "TEST-SHIP-3HOP" is registered for 3-hop test
    And the ship is at waypoint "X1-START" with 500 fuel and 500 capacity
    And waypoint "X1-HOP1" is 80 units away
    And waypoint "X1-HOP2" is 90 units away from "X1-HOP1"
    And waypoint "X1-FINAL" is 100 units away from "X1-HOP2"
    When I navigate through all 3 hops from "X1-START" to "X1-FINAL"
    Then each segment should wait for arrival before proceeding
    And all 3 segments should complete successfully
    And the ship should be at "X1-FINAL" in IN_ORBIT status
