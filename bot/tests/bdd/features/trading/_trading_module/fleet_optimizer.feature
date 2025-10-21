Feature: Fleet Trade Optimizer - Multi-ship conflict avoidance
  As a fleet operator
  I want to assign conflict-free trading routes to multiple ships
  So that ships don't compete for the same resources at the same markets

  Background:
    Given a test database with market data
    And a mock API client
    And the following markets in system "X1-TEST":
      | waypoint  | x   | y   |
      | X1-TEST-A | 0   | 0   |
      | X1-TEST-B | 100 | 0   |
      | X1-TEST-C | 200 | 0   |
      | X1-TEST-D | 300 | 0   |
    And the following trade opportunities:
      | buy_waypoint | sell_waypoint | good              | buy_price | sell_price | spread | volume |
      | X1-TEST-A    | X1-TEST-B     | IRON_ORE          | 100       | 200        | 100    | 50     |
      | X1-TEST-A    | X1-TEST-C     | COPPER_ORE        | 150       | 250        | 100    | 50     |
      | X1-TEST-C    | X1-TEST-D     | ADVANCED_CIRCUITRY| 500       | 700        | 200    | 30     |

  Scenario: Basic conflict avoidance - two ships get different routes
    Given a fleet optimizer for player 1
    And ship "SHIP-1" at "X1-TEST-A" with cargo capacity 50
    And ship "SHIP-2" at "X1-TEST-A" with cargo capacity 50
    When I optimize fleet routes for 2 ships with max 3 stops
    Then ship "SHIP-1" should have a profitable route
    And ship "SHIP-2" should have a profitable route
    And the routes should have no resource conflicts
    And ship "SHIP-1" should buy "IRON_ORE" at "X1-TEST-A"
    And ship "SHIP-2" should buy "COPPER_ORE" at "X1-TEST-A"

  Scenario: No conflicts when different markets available
    Given a fleet optimizer for player 1
    And ship "SHIP-1" at "X1-TEST-A" with cargo capacity 40
    And ship "SHIP-2" at "X1-TEST-C" with cargo capacity 40
    When I optimize fleet routes for 2 ships with max 2 stops
    Then ship "SHIP-1" should have a profitable route
    And ship "SHIP-2" should have a profitable route
    And the routes should have no resource conflicts
    And total fleet profit should be greater than 0

  Scenario: Second ship blocked - no profitable routes after filtering
    Given a fleet optimizer for player 1
    And only one profitable trade opportunity exists
    And ship "SHIP-1" at "X1-TEST-A" with cargo capacity 50
    And ship "SHIP-2" at "X1-TEST-A" with cargo capacity 50
    When I optimize fleet routes for 2 ships with max 2 stops
    Then ship "SHIP-1" should have a profitable route
    And ship "SHIP-2" should have no route assigned
    And the result should indicate 1 out of 2 ships have routes

  Scenario: Three ships with cascading conflict avoidance
    Given a fleet optimizer for player 1
    And ship "SHIP-1" at "X1-TEST-A" with cargo capacity 50
    And ship "SHIP-2" at "X1-TEST-A" with cargo capacity 50
    And ship "SHIP-3" at "X1-TEST-C" with cargo capacity 30
    When I optimize fleet routes for 3 ships with max 3 stops
    Then ship "SHIP-1" should have a profitable route
    And ship "SHIP-2" should have a profitable route
    And ship "SHIP-3" should have a profitable route
    And the routes should have no resource conflicts
    And reserved resource pairs should equal the count of unique BUY actions

  Scenario: Ships with residual cargo from previous operations
    Given a fleet optimizer for player 1
    And ship "SHIP-1" at "X1-TEST-A" with cargo capacity 50
    And ship "SHIP-1" has 10 units of "ALUMINUM_ORE" in cargo
    And ship "SHIP-2" at "X1-TEST-C" with cargo capacity 40
    And ship "SHIP-2" has 5 units of "QUARTZ_SAND" in cargo
    When I optimize fleet routes for 2 ships with max 3 stops
    Then ship "SHIP-1" route should account for 10 units of existing cargo
    And ship "SHIP-2" route should account for 5 units of existing cargo
    And both routes should be profitable considering residual cargo

  Scenario: Mixed cargo capacities - small and large ships
    Given a fleet optimizer for player 1
    And ship "SMALL-SHIP" at "X1-TEST-A" with cargo capacity 20
    And ship "LARGE-SHIP" at "X1-TEST-A" with cargo capacity 100
    When I optimize fleet routes for 2 ships with max 2 stops
    Then ship "SMALL-SHIP" should buy units appropriate for capacity 20
    And ship "LARGE-SHIP" should buy units appropriate for capacity 100
    And the routes should have no resource conflicts

  Scenario: Unprofitable route after conflict filtering
    Given a fleet optimizer for player 1
    And ship "SHIP-1" at "X1-TEST-A" with cargo capacity 50
    And ship "SHIP-2" at "X1-TEST-A" with cargo capacity 50
    And all opportunities become unprofitable after first ship assignment
    When I optimize fleet routes for 2 ships with max 2 stops
    Then ship "SHIP-1" should have a profitable route
    And ship "SHIP-2" should have no route assigned
    And ship "SHIP-2" should show no profitable routes in logs

  Scenario: Fleet optimization returns correct metadata
    Given a fleet optimizer for player 1
    And ship "SHIP-1" at "X1-TEST-A" with cargo capacity 50
    And ship "SHIP-2" at "X1-TEST-A" with cargo capacity 50
    When I optimize fleet routes for 2 ships with max 3 stops
    Then the result should contain "ship_routes" dictionary
    And the result should contain "total_fleet_profit"
    And the result should contain "reserved_pairs" set
    And the result should show "conflicts" equals 0
    And "total_fleet_profit" should equal sum of individual route profits

  Scenario: No profitable opportunities for entire fleet
    Given a fleet optimizer for player 1
    And ship "SHIP-1" at "X1-TEST-A" with cargo capacity 50
    And ship "SHIP-2" at "X1-TEST-A" with cargo capacity 50
    And no profitable trade opportunities exist
    When I optimize fleet routes for 2 ships with max 2 stops
    Then the result should be None
    And the error log should indicate no profitable routes found

  Scenario: Resource reservation tracking across ships
    Given a fleet optimizer for player 1
    And ship "SHIP-1" at "X1-TEST-A" with cargo capacity 50
    And ship "SHIP-2" at "X1-TEST-A" with cargo capacity 50
    And ship "SHIP-3" at "X1-TEST-C" with cargo capacity 40
    When I optimize fleet routes for 3 ships with max 3 stops
    Then reserved pairs should include all BUY actions from ship "SHIP-1"
    And reserved pairs should include all BUY actions from ship "SHIP-2"
    And reserved pairs should include all BUY actions from ship "SHIP-3"
    And no reserved pair should appear in multiple ship routes

  Scenario: Single ship fleet optimization (edge case)
    Given a fleet optimizer for player 1
    And ship "SHIP-1" at "X1-TEST-A" with cargo capacity 50
    When I optimize fleet routes for 1 ship with max 3 stops
    Then ship "SHIP-1" should have a profitable route
    And reserved pairs should not be empty
    And total fleet profit should equal ship "SHIP-1" route profit
