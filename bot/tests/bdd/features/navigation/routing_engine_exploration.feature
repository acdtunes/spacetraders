Feature: Routing Engine State Exploration
  As a routing engine
  I need to explore multiple waypoint states during pathfinding
  So that I can find valid paths even with complex fuel constraints

  Background:
    Given the routing engine is initialized
    And a waypoint graph with the following waypoints:
      | symbol      | x    | y    | has_fuel |
      | X1-TEST-A1  | 0    | 0    | true     |
      | X1-TEST-B2  | 100  | 0    | true     |
      | X1-TEST-C3  | 200  | 0    | true     |
      | X1-TEST-D4  | 300  | 0    | false    |
      | X1-TEST-E5  | 400  | 0    | true     |

  Scenario: Routing engine explores multiple states before finding path
    Given a ship with fuel capacity 400 and current fuel 400
    And the ship's engine speed is 30
    When I calculate a route from "X1-TEST-A1" to "X1-TEST-E5"
    Then the route should be found
    And the routing engine should explore at least 3 states

  Scenario: Routing engine explores multiple states when no direct path exists
    Given a ship with fuel capacity 100 and current fuel 50
    And the ship's engine speed is 30
    When I calculate a route from "X1-TEST-A1" to "X1-TEST-E5"
    Then the route should be found
    And the routing engine should explore at least 3 states

  Scenario: Routing engine explores neighbors from start position
    Given a ship with fuel capacity 400 and current fuel 400
    And the ship's engine speed is 30
    When I calculate a route from "X1-TEST-A1" to "X1-TEST-B2"
    Then the route should be found
    And the routing engine should have considered multiple neighbors from start
