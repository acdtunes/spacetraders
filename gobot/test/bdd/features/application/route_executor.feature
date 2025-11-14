Feature: Route Executor Service
  As a SpaceTraders bot
  I want to execute routes by orchestrating atomic ship commands
  So that I can navigate efficiently using the routing engine's plan

  Background:
    Given a player exists with agent "TEST-AGENT" and token "test-token-123"
    And the player has player_id 1

  Scenario: Execute single-segment route
    Given a ship "SHIP-1" for player 1 at "X1-A1" with status "IN_ORBIT" and fuel 100/100
    And waypoint "X1-A1" exists at coordinates (0, 0) with fuel station
    And waypoint "X1-B2" exists at coordinates (10, 0) without fuel station
    And a route exists for ship "SHIP-1" with 1 segment from "X1-A1" to "X1-B2" in "CRUISE" mode requiring 25 fuel
    When I execute the route for ship "SHIP-1" and player 1
    Then the route execution should succeed
    And the ship should be at "X1-B2"
    And the route status should be "COMPLETED"

  Scenario: Execute multi-segment route
    Given a ship "SHIP-1" for player 1 at "X1-A1" with status "IN_ORBIT" and fuel 100/100
    And waypoint "X1-A1" exists at coordinates (0, 0) without fuel station
    And waypoint "X1-B2" exists at coordinates (10, 0) without fuel station
    And waypoint "X1-C3" exists at coordinates (20, 0) without fuel station
    And a route exists for ship "SHIP-1" with segments:
      | from   | to     | distance | fuel | mode   | refuel |
      | X1-A1  | X1-B2  | 10.0     | 25   | CRUISE | false  |
      | X1-B2  | X1-C3  | 10.0     | 25   | CRUISE | false  |
    When I execute the route for ship "SHIP-1" and player 1
    Then the route execution should succeed
    And the ship should be at "X1-C3"
    And the route status should be "COMPLETED"

  Scenario: Execute route with refuel before departure
    Given a ship "SHIP-1" for player 1 at "X1-A1" with status "DOCKED" and fuel 20/100
    And waypoint "X1-A1" exists at coordinates (0, 0) with fuel station
    And waypoint "X1-B2" exists at coordinates (30, 0) without fuel station
    And a route exists for ship "SHIP-1" requiring refuel before departure
    And the route has 1 segment from "X1-A1" to "X1-B2" in "CRUISE" mode requiring 75 fuel
    When I execute the route for ship "SHIP-1" and player 1
    Then the route execution should succeed
    And the ship should be at "X1-B2"
    And the ship should have consumed fuel for the journey
    And the route status should be "COMPLETED"

  Scenario: Execute route with mid-route refueling
    Given a ship "SHIP-1" for player 1 at "X1-A1" with status "IN_ORBIT" and fuel 100/100
    And waypoint "X1-A1" exists at coordinates (0, 0) without fuel station
    And waypoint "X1-B2" exists at coordinates (30, 0) with fuel station
    And waypoint "X1-C3" exists at coordinates (60, 0) without fuel station
    And a route exists for ship "SHIP-1" with segments:
      | from   | to     | distance | fuel | mode   | refuel |
      | X1-A1  | X1-B2  | 30.0     | 75   | CRUISE | true   |
      | X1-B2  | X1-C3  | 30.0     | 75   | CRUISE | false  |
    When I execute the route for ship "SHIP-1" and player 1
    Then the route execution should succeed
    And the ship should be at "X1-C3"
    And the ship should have refueled at "X1-B2"
    And the route status should be "COMPLETED"

  Scenario: Execute route with opportunistic refueling
    Given a ship "SHIP-1" for player 1 at "X1-A1" with status "IN_ORBIT" and fuel 40/100
    And waypoint "X1-A1" exists at coordinates (0, 0) without fuel station
    And waypoint "X1-B2" exists at coordinates (10, 0) with fuel station
    And waypoint "X1-C3" exists at coordinates (20, 0) without fuel station
    And a route exists for ship "SHIP-1" with segments:
      | from   | to     | distance | fuel | mode   | refuel |
      | X1-A1  | X1-B2  | 10.0     | 25   | CRUISE | false  |
      | X1-B2  | X1-C3  | 10.0     | 25   | CRUISE | false  |
    When I execute the route for ship "SHIP-1" and player 1
    Then the route execution should succeed
    And the ship should be at "X1-C3"
    And the ship should have opportunistically refueled at "X1-B2"
    And the route status should be "COMPLETED"

  Scenario: Execute route with pre-departure refuel prevention
    Given a ship "SHIP-1" for player 1 at "X1-A1" with status "IN_ORBIT" and fuel 50/100
    And waypoint "X1-A1" exists at coordinates (0, 0) with fuel station
    And waypoint "X1-B2" exists at coordinates (40, 0) without fuel station
    And a route exists for ship "SHIP-1" with 1 segment from "X1-A1" to "X1-B2" in "DRIFT" mode requiring 0 fuel
    When I execute the route for ship "SHIP-1" and player 1
    Then the route execution should succeed
    And the ship should have prevented DRIFT mode by refueling at "X1-A1"
    And the ship should be at "X1-B2"
    And the route status should be "COMPLETED"

  Scenario: Handle ship already in transit - wait for arrival first
    Given a ship "SHIP-1" for player 1 in transit to "X1-B2" arriving in 5 seconds
    And waypoint "X1-B2" exists at coordinates (10, 0) without fuel station
    And waypoint "X1-C3" exists at coordinates (20, 0) without fuel station
    And a route exists for ship "SHIP-1" with 1 segment from "X1-B2" to "X1-C3" in "CRUISE" mode requiring 25 fuel
    When I execute the route for ship "SHIP-1" and player 1
    Then the route executor should wait for current transit to complete
    And the route execution should succeed
    And the ship should be at "X1-C3"
    And the route status should be "COMPLETED"

  Scenario: Handle route execution failure - segment navigation fails
    Given a ship "SHIP-1" for player 1 at "X1-A1" with status "IN_ORBIT" and fuel 10/100
    And waypoint "X1-A1" exists at coordinates (0, 0) without fuel station
    And waypoint "X1-B2" exists at coordinates (50, 0) without fuel station
    And a route exists for ship "SHIP-1" with 1 segment from "X1-A1" to "X1-B2" in "CRUISE" mode requiring 90 fuel
    When I execute the route for ship "SHIP-1" and player 1
    Then the route execution should fail
    And the route status should be "FAILED"
    And the error should indicate "insufficient fuel"
