Feature: Opportunistic Refueling in Routing Engine
  The routing engine should refuel opportunistically when fuel drops below 90%
  to prevent ships from running out of fuel at non-fuel waypoints.

  Background:
    Given a system "X1-TEST" with waypoints
      | symbol      | type        | x   | y   | has_fuel |
      | X1-TEST-A1  | MARKETPLACE | 0   | 0   | true     |
      | X1-TEST-B2  | ASTEROID    | 100 | 0   | false    |
      | X1-TEST-C3  | MARKETPLACE | 200 | 0   | true     |

  Scenario: Routing engine refuels when fuel below 90% at fuel station
    Given a ship with 400 fuel capacity
    And the ship has 300 current fuel
    And the ship is at waypoint "X1-TEST-A1" with fuel
    When I plan a route from "X1-TEST-A1" to "X1-TEST-C3"
    Then the route should include a REFUEL action at "X1-TEST-A1"

  Scenario: Routing engine does not refuel when fuel above 90%
    Given a ship with 400 fuel capacity
    And the ship has 380 current fuel
    And the ship is at waypoint "X1-TEST-A1" with fuel
    When I plan a route from "X1-TEST-A1" to "X1-TEST-C3"
    Then the route should NOT include a REFUEL action at "X1-TEST-A1"

  Scenario: Ship refuels at intermediate waypoint to avoid stranding
    Given a ship with 200 fuel capacity
    And the ship has 100 current fuel
    And the ship is at waypoint "X1-TEST-A1" with fuel
    When I plan a route from "X1-TEST-A1" to "X1-TEST-C3" via "X1-TEST-B2"
    Then the route should include a REFUEL action at "X1-TEST-A1"
    And the ship should have sufficient fuel for the journey
