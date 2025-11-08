Feature: Never Use DRIFT Mode
  As a fleet operator
  I want ships to NEVER use DRIFT mode
  So that navigation is predictable and fuel-efficient

  Background:
    Given the routing engine is initialized
    And a waypoint graph with the following waypoints:
      | symbol      | x    | y    | has_fuel |
      | X1-TEST-A1  | 0    | 0    | true     |
      | X1-TEST-B2  | 100  | 0    | true     |
      | X1-TEST-C3  | 200  | 0    | true     |
      | X1-TEST-D4  | 300  | 0    | false    |
      | X1-TEST-E5  | 400  | 0    | true     |

  Scenario: Short distance with full fuel never uses DRIFT
    Given a ship with fuel capacity 400 and current fuel 400
    And the ship's engine speed is 30
    When I calculate a route from "X1-TEST-A1" to "X1-TEST-B2"
    Then the route should be found
    And the route should use only BURN or CRUISE modes
    And the route should not use DRIFT mode

  Scenario: Medium distance with moderate fuel never uses DRIFT
    Given a ship with fuel capacity 400 and current fuel 150
    And the ship's engine speed is 30
    When I calculate a route from "X1-TEST-A1" to "X1-TEST-C3"
    Then the route should be found
    And the route should include refuel stops
    And the route should use only BURN or CRUISE modes
    And the route should not use DRIFT mode

  Scenario: Long distance with low fuel inserts refuel stops instead of DRIFT
    Given a ship with fuel capacity 400 and current fuel 50
    And the ship's engine speed is 30
    When I calculate a route from "X1-TEST-A1" to "X1-TEST-E5"
    Then the route should be found
    And the route should include refuel stops
    And the route should use only BURN or CRUISE modes
    And the route should not use DRIFT mode

  Scenario: Route planning never selects DRIFT even when fuel is critically low
    Given a ship with fuel capacity 400 and current fuel 20
    And the ship's engine speed is 30
    When I calculate a route from "X1-TEST-A1" to "X1-TEST-B2"
    Then the route should be found
    And the route should include refuel stops
    And the route should use only BURN or CRUISE modes
    And the route should not use DRIFT mode

  Scenario: BURN mode preferred when fuel is abundant
    Given a ship with fuel capacity 400 and current fuel 400
    And the ship's engine speed is 30
    When I calculate a route from "X1-TEST-A1" to "X1-TEST-C3"
    Then the route should be found
    And the route should use only BURN mode
    And the route should not use DRIFT mode

  Scenario: CRUISE mode used when fuel is above 90% but insufficient for BURN
    Given a ship with fuel capacity 200 and current fuel 185
    And the ship's engine speed is 30
    When I calculate a route from "X1-TEST-B2" to "X1-TEST-C3"
    Then the route should be found
    And the route should use CRUISE mode for at least one segment
    And the route should not use DRIFT mode
