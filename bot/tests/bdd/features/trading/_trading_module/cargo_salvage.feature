Feature: Cargo Salvage Service - Emergency cargo cleanup with 3-tier strategy
  As a circuit breaker system
  I want to intelligently salvage unprofitable cargo
  So that operations can continue with minimal losses

  Background:
    Given a test database with market data
    And a mock API client
    And a mock ship controller
    And the following markets in system "X1-TEST":
      | waypoint  | x   | y   |
      | X1-TEST-A | 0   | 0   |
      | X1-TEST-B | 100 | 0   |
      | X1-TEST-C | 200 | 0   |
      | X1-TEST-D | 50  | 0   |
    And ship is currently at "X1-TEST-A"
    And ship is DOCKED

  # ============================================================================
  # Tier 1: Navigate to Planned Destination
  # ============================================================================

  Scenario: Tier 1 - Navigate to planned sell destination successfully
    Given a CargoSalvageService
    And ship has 30 units of "IRON_ORE" in cargo
    And a multi-leg route with planned sell for "IRON_ORE" at "X1-TEST-C"
    And current segment index is 1
    And "X1-TEST-C" buys "IRON_ORE" for 200 credits
    When I salvage cargo for unprofitable "IRON_ORE"
    Then ship should navigate from "X1-TEST-A" to "X1-TEST-C"
    And ship should dock at "X1-TEST-C"
    And ship should sell 30 units of "IRON_ORE"
    And salvage should succeed with revenue greater than 0
    And log should indicate Tier 1 salvage with planned destination

  Scenario: Tier 1 - No planned destination found
    Given a CargoSalvageService
    And ship has 30 units of "IRON_ORE" in cargo
    And route has no planned sell destination for "IRON_ORE"
    And current segment index is 2
    When I salvage cargo for unprofitable "IRON_ORE"
    Then should fall back to Tier 2 or Tier 3
    And log should indicate no planned sell destination found

  Scenario: Tier 1 - Already at planned destination
    Given a CargoSalvageService
    And ship has 30 units of "IRON_ORE" in cargo
    And ship is currently at "X1-TEST-B"
    And route plans to sell "IRON_ORE" at "X1-TEST-B"
    And current segment index is 1
    When I salvage cargo for unprofitable "IRON_ORE"
    Then should skip navigation (already at destination)
    And should fall back to Tier 2

  Scenario: Tier 1 - Navigation fails
    Given a CargoSalvageService
    And ship has 30 units of "IRON_ORE" in cargo
    And route plans to sell "IRON_ORE" at "X1-TEST-C"
    And navigation to "X1-TEST-C" fails
    When I salvage cargo for unprofitable "IRON_ORE"
    Then should fall back to Tier 2 or Tier 3
    And salvage should continue despite navigation failure

  # ============================================================================
  # Tier 2: Sell at Current Market
  # ============================================================================

  Scenario: Tier 2 - Sell at current market successfully
    Given a CargoSalvageService
    And ship has 40 units of "COPPER_ORE" in cargo
    And ship is at "X1-TEST-A"
    And "X1-TEST-A" buys "COPPER_ORE" for 150 credits
    And no planned destination for "COPPER_ORE"
    When I salvage cargo for unprofitable "COPPER_ORE"
    Then ship should sell at current market "X1-TEST-A"
    And salvage should succeed with revenue 6000
    And log should indicate Tier 2 salvage at current market

  Scenario: Tier 2 - Current market doesn't buy good
    Given a CargoSalvageService
    And ship has 30 units of "IRON_ORE" in cargo
    And ship is at "X1-TEST-A"
    And "X1-TEST-A" does not buy "IRON_ORE"
    When I salvage cargo for unprofitable "IRON_ORE"
    Then should skip Tier 2
    And should fall back to Tier 3
    And log should indicate current market doesn't buy good

  # ============================================================================
  # Tier 3: Search Nearby Markets
  # ============================================================================

  Scenario: Tier 3 - Find and navigate to nearby buyer
    Given a CargoSalvageService
    And ship has 25 units of "IRON_ORE" in cargo
    And ship is at "X1-TEST-A"
    And "X1-TEST-A" does not buy "IRON_ORE"
    And "X1-TEST-D" buys "IRON_ORE" for 180 credits at distance 50
    And "X1-TEST-B" buys "IRON_ORE" for 200 credits at distance 100
    When I salvage cargo for unprofitable "IRON_ORE"
    Then should use Tier 3 nearby market search
    And should navigate to "X1-TEST-D" (closest buyer)
    And should sell 25 units for 4500 credits
    And log should indicate Tier 3 salvage with buyer distance

  Scenario: Tier 3 - No nearby buyers found within 200 units
    Given a CargoSalvageService
    And ship has 30 units of "IRON_ORE" in cargo
    And ship is at "X1-TEST-A"
    And no markets buy "IRON_ORE" within 200 units
    When I salvage cargo for unprofitable "IRON_ORE"
    Then salvage should skip the good
    And cargo should remain in ship
    And log should warn no buyers found in system

  Scenario: Tier 3 - Multiple nearby buyers, selects closest
    Given a CargoSalvageService
    And ship has 20 units of "IRON_ORE" in cargo
    And nearby buyers for "IRON_ORE":
      | waypoint  | distance | price |
      | X1-TEST-D | 50       | 180   |
      | X1-TEST-B | 100      | 200   |
      | X1-TEST-C | 200      | 220   |
    When I salvage cargo for unprofitable "IRON_ORE"
    Then should select "X1-TEST-D" as best (closest) buyer
    And should navigate to "X1-TEST-D"
    And log should show buyer selection reasoning

  # ============================================================================
  # Selective vs Full Salvage
  # ============================================================================

  Scenario: Selective salvage - Only salvage unprofitable item
    Given a CargoSalvageService
    And ship has mixed cargo:
      | good          | units |
      | IRON_ORE      | 20    |
      | COPPER_ORE    | 15    |
      | ALUMINUM_ORE  | 10    |
    And only "IRON_ORE" is unprofitable
    When I salvage cargo for unprofitable "IRON_ORE"
    Then should only salvage "IRON_ORE"
    And "COPPER_ORE" should remain in cargo
    And "ALUMINUM_ORE" should remain in cargo
    And log should indicate selective salvage mode
    And log should list items being kept

  Scenario: Full salvage - Salvage all cargo (no specific item)
    Given a CargoSalvageService
    And ship has mixed cargo:
      | good         | units |
      | IRON_ORE     | 20    |
      | COPPER_ORE   | 15    |
    And no specific unprofitable item specified
    When I salvage all cargo
    Then should salvage all items
    And final cargo should be empty
    And log should indicate full salvage mode

  Scenario: Salvage with no cargo - Returns early
    Given a CargoSalvageService
    And ship has empty cargo
    When I salvage cargo
    Then should return success immediately
    And log should indicate no cargo to salvage
    And no market operations should be performed

  Scenario: Unprofitable item not in cargo - Returns success
    Given a CargoSalvageService
    And ship has 20 units of "IRON_ORE" in cargo
    When I salvage cargo for unprofitable "COPPER_ORE"
    Then should return success
    And log should warn item not found in cargo
    And cargo should remain unchanged

  # ============================================================================
  # Ship State Management
  # ============================================================================

  Scenario: Ship is IN_ORBIT when salvage starts
    Given a CargoSalvageService
    And ship has 30 units of "IRON_ORE" in cargo
    And ship is IN_ORBIT
    And current market buys "IRON_ORE"
    When I salvage cargo
    Then ship should dock before selling
    And salvage should succeed

  Scenario: Ship state error - Cannot get ship status
    Given a CargoSalvageService
    And ship.get_status() returns None
    When I salvage cargo
    Then salvage should fail
    And should return False
    And error should be logged

  # ============================================================================
  # Integration & Edge Cases
  # ============================================================================

  Scenario: Mixed tier execution - Planned destination → Current market → Nearby
    Given a CargoSalvageService
    And ship has multiple items:
      | good          | units | tier_used        |
      | IRON_ORE      | 20    | planned_dest     |
      | COPPER_ORE    | 15    | current_market   |
      | ALUMINUM_ORE  | 10    | nearby_market    |
    When I salvage all cargo
    Then "IRON_ORE" should use Tier 1
    And "COPPER_ORE" should use Tier 2
    And "ALUMINUM_ORE" should use Tier 3
    And total revenue should equal sum of all sales

  Scenario: Partial salvage success - Some items salvaged, some failed
    Given a CargoSalvageService
    And ship has multiple items to salvage
    And some markets are unreachable
    When I salvage all cargo
    Then salvage should succeed (partial success acceptable)
    And final cargo should show only unsalvageable items
    And log should list remaining items

  Scenario: Salvage logs comprehensive summary
    Given a CargoSalvageService
    And ship has 30 units of "IRON_ORE" in cargo
    And salvage operation executes
    When I salvage cargo
    Then log should show salvage plan
    Then log should show tier selection reasoning
    Then log should show final revenue summary
    Then log should show final cargo state

  Scenario: Exception handling during salvage
    Given a CargoSalvageService
    And ship has cargo
    And market operation throws exception
    When I salvage cargo
    Then exception should be caught and logged
    And salvage should return False
    And traceback should be logged

  # ============================================================================
  # Real-world Scenarios
  # ============================================================================

  Scenario: Circuit breaker triggered mid-route with cargo blocking
    Given a CargoSalvageService
    And ship has cargo that blocks future segments
    And planned route has 3 remaining profitable segments
    And route profit would be 50,000 credits
    When I salvage cargo for circuit breaker trigger
    Then should prioritize route continuation
    And should use fastest salvage method
    And log should indicate high opportunity cost scenario
