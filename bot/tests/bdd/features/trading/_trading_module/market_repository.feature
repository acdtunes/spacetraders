Feature: Market Repository - Database access layer for market data
  As a trading system
  I want to access market data from the database
  So that I can make informed trading decisions

  Background:
    Given a test database
    And the following waypoints with coordinates:
      | waypoint  | x    | y    |
      | X1-TEST-A | 0    | 0    |
      | X1-TEST-B | 30   | 40   |
      | X1-TEST-C | 100  | 0    |
      | X1-TEST-D | 200  | 150  |
    And the following market data:
      | waypoint  | good       | purchase_price | sell_price |
      | X1-TEST-A | IRON_ORE   | 0              | 100        |
      | X1-TEST-B | IRON_ORE   | 200            | 0          |
      | X1-TEST-B | COPPER_ORE | 300            | 0          |
      | X1-TEST-C | IRON_ORE   | 180            | 0          |
      | X1-TEST-D | IRON_ORE   | 250            | 0          |

  # ============================================================================
  # get_waypoint_coordinates Tests
  # ============================================================================

  Scenario: Get coordinates for existing waypoint
    Given a MarketRepository
    When I get coordinates for "X1-TEST-A"
    Then coordinates should be (0, 0)

  Scenario: Get coordinates for waypoint with non-zero position
    Given a MarketRepository
    When I get coordinates for "X1-TEST-B"
    Then coordinates should be (30, 40)

  Scenario: Get coordinates for missing waypoint
    Given a MarketRepository
    When I get coordinates for "X1-MISSING"
    Then coordinates should be None

  # ============================================================================
  # calculate_distance Tests
  # ============================================================================

  Scenario: Calculate distance between two waypoints
    Given a MarketRepository
    When I calculate distance from "X1-TEST-A" to "X1-TEST-B"
    Then distance should be 50.0
    # sqrt(30^2 + 40^2) = sqrt(900 + 1600) = sqrt(2500) = 50

  Scenario: Calculate distance when waypoints are same location
    Given a MarketRepository
    When I calculate distance from "X1-TEST-A" to "X1-TEST-A"
    Then distance should be 0.0

  Scenario: Calculate distance with missing from_waypoint
    Given a MarketRepository
    When I calculate distance from "X1-MISSING" to "X1-TEST-B"
    Then distance should be 150.0
    # Default distance when coordinates not found

  Scenario: Calculate distance with missing to_waypoint
    Given a MarketRepository
    When I calculate distance from "X1-TEST-A" to "X1-MISSING"
    Then distance should be 150.0

  Scenario: Calculate large distance between far waypoints
    Given a MarketRepository
    When I calculate distance from "X1-TEST-A" to "X1-TEST-D"
    Then distance should be 250.0
    # sqrt(200^2 + 150^2) = sqrt(40000 + 22500) = sqrt(62500) = 250

  # ============================================================================
  # find_nearby_buyers Tests
  # ============================================================================

  Scenario: Find nearby buyers within distance threshold
    Given a MarketRepository
    When I find buyers for "IRON_ORE" near "X1-TEST-A" in system "X1-TEST" within 100 units
    Then I should find 2 buyers
    And buyers should include "X1-TEST-B" at distance 50.0
    And buyers should include "X1-TEST-C" at distance 100.0
    And buyers should not include "X1-TEST-D"
    And buyers should be sorted by distance ascending

  Scenario: Find nearby buyers with limit
    Given a MarketRepository
    When I find buyers for "IRON_ORE" near "X1-TEST-A" in system "X1-TEST" within 300 units with limit 2
    Then I should find exactly 2 buyers
    And first buyer should be closest
    And second buyer should be second closest

  Scenario: Find buyers when none within distance
    Given a MarketRepository
    When I find buyers for "IRON_ORE" near "X1-TEST-A" in system "X1-TEST" within 40 units
    Then I should find 0 buyers

  Scenario: Find buyers for good with no markets
    Given a MarketRepository
    When I find buyers for "PLATINUM_ORE" near "X1-TEST-A" in system "X1-TEST" within 200 units
    Then I should find 0 buyers

  Scenario: Find buyers from waypoint with no coordinates
    Given a MarketRepository
    When I find buyers for "IRON_ORE" near "X1-MISSING" in system "X1-TEST" within 100 units
    Then I should find 0 buyers
    # No origin coordinates, cannot calculate distances

  Scenario: Find buyers returns correct metadata
    Given a MarketRepository
    When I find buyers for "IRON_ORE" near "X1-TEST-A" in system "X1-TEST" within 100 units
    Then each buyer should have waypoint_symbol
    And each buyer should have purchase_price
    And each buyer should have x coordinate
    And each buyer should have y coordinate
    And each buyer should have distance

  Scenario: Find buyers for COPPER_ORE (different good)
    Given a MarketRepository
    When I find buyers for "COPPER_ORE" near "X1-TEST-A" in system "X1-TEST" within 200 units
    Then I should find 1 buyer
    And buyer should be "X1-TEST-B"
    And buyer purchase price should be 300

  Scenario: Find buyers filters by system correctly
    Given a MarketRepository
    And waypoint "X1-OTHER-A" exists in different system
    And "X1-OTHER-A" buys "IRON_ORE" for 300
    When I find buyers for "IRON_ORE" near "X1-TEST-A" in system "X1-TEST" within 500 units
    Then buyers should not include "X1-OTHER-A"
    And all buyers should match system "X1-TEST"

  # ============================================================================
  # check_market_accepts_good Tests
  # ============================================================================

  Scenario: Market accepts good (has purchase_price > 0)
    Given a MarketRepository
    When I check if "X1-TEST-B" accepts "IRON_ORE"
    Then market should accept the good
    # X1-TEST-B has purchase_price 200 for IRON_ORE

  Scenario: Market does not accept good (purchase_price = 0)
    Given a MarketRepository
    When I check if "X1-TEST-A" accepts "IRON_ORE"
    Then market should not accept the good
    # X1-TEST-A has purchase_price 0 (only sells IRON_ORE)

  Scenario: Market has no data for good
    Given a MarketRepository
    When I check if "X1-TEST-A" accepts "PLATINUM_ORE"
    Then market should not accept the good
    # No market data for PLATINUM_ORE at X1-TEST-A

  Scenario: Market does not exist
    Given a MarketRepository
    When I check if "X1-MISSING" accepts "IRON_ORE"
    Then market should not accept the good

  Scenario: Check acceptance for different goods at same market
    Given a MarketRepository
    When I check if "X1-TEST-B" accepts "IRON_ORE"
    Then market should accept the good
    When I check if "X1-TEST-B" accepts "COPPER_ORE"
    Then market should accept the good
    When I check if "X1-TEST-B" accepts "ALUMINUM_ORE"
    Then market should not accept the good

  # ============================================================================
  # Edge Cases & Integration
  # ============================================================================

  Scenario: Repository handles database connection properly
    Given a MarketRepository
    When I perform multiple sequential queries
    Then all queries should succeed
    And database connections should be properly managed

  Scenario: Distance calculation matches coordinates query
    Given a MarketRepository
    When I get coordinates for "X1-TEST-A" and "X1-TEST-B"
    And I calculate distance from "X1-TEST-A" to "X1-TEST-B"
    Then distance should match manual calculation from coordinates
