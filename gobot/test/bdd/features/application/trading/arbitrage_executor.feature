Feature: Arbitrage Executor
  As a SpaceTraders bot
  I want to execute complete arbitrage trading cycles
  So that I can automatically profit from market price differences

  # ============================================================================
  # Complete Arbitrage Cycle
  # ============================================================================

  Scenario: Execute successful arbitrage run
    Given ship "TRADER-1" at waypoint "X1-A1" with 40 cargo capacity
    And ship "TRADER-1" has 0 cargo units
    And an arbitrage opportunity:
      | Good       | IRON_ORE |
      | BuyMarket  | X1-A1    |
      | SellMarket | X1-B1    |
      | BuyPrice   | 100      |
      | SellPrice  | 150      |
    When I execute arbitrage with ship "TRADER-1"
    Then the execution should succeed
    And ship should navigate to "X1-A1"
    And ship should dock at "X1-A1"
    And ship should purchase 40 units of "IRON_ORE" at 100 credits each
    And ship should navigate to "X1-B1"
    And ship should dock at "X1-B1"
    And ship should sell 40 units of "IRON_ORE" at 150 credits each
    And net profit should be 2000 credits
    # Net profit = (40 × 150) - (40 × 100) = 6000 - 4000 = 2000

  Scenario: Execute with partial cargo capacity
    Given ship "TRADER-2" at waypoint "X1-A1" with 40 cargo capacity
    And ship "TRADER-2" has 25 cargo units of "FUEL"
    And an arbitrage opportunity:
      | Good       | COPPER_ORE |
      | BuyMarket  | X1-A1      |
      | SellMarket | X1-C1      |
      | BuyPrice   | 50         |
      | SellPrice  | 80         |
    When I execute arbitrage with ship "TRADER-2"
    Then the execution should succeed
    And ship should purchase 15 units of "COPPER_ORE"
    # Only 15 units available (40 - 25 existing cargo)
    And ship should sell 15 units of "COPPER_ORE"
    And net profit should be 450 credits
    # Net profit = (15 × 80) - (15 × 50) = 1200 - 750 = 450

  # ============================================================================
  # Ledger Integration
  # ============================================================================

  Scenario: Record transactions in ledger with operation context
    Given ship "TRADER-3" at waypoint "X1-A1"
    And container ID "arbitrage-worker-123" for operation tracking
    And an arbitrage opportunity for "IRON_ORE" from "X1-A1" to "X1-B1"
    When I execute arbitrage with ship "TRADER-3"
    Then ledger should contain purchase transaction:
      | Type         | CARGO_PURCHASE        |
      | Amount       | -4000                 |
      | Units        | 40                    |
      | RelatedEntity| arbitrage-worker-123  |
    And ledger should contain sale transaction:
      | Type         | CARGO_SALE            |
      | Amount       | 6000                  |
      | Units        | 40                    |
      | RelatedEntity| arbitrage-worker-123  |
    # Fuel costs also recorded but vary by distance

  # ============================================================================
  # Error Handling
  # ============================================================================

  Scenario: Fail when ship has no cargo space
    Given ship "FULL-SHIP" at waypoint "X1-A1" with 40 cargo capacity
    And ship "FULL-SHIP" has 40 cargo units of "OTHER_GOODS"
    And an arbitrage opportunity for "IRON_ORE" from "X1-A1" to "X1-B1"
    When I execute arbitrage with ship "FULL-SHIP"
    Then the execution should fail with error "ship has no available cargo space"

  Scenario: Fail when navigation fails
    Given ship "NAV-FAIL" at waypoint "X1-A1"
    And navigation to "X1-B1" will fail with "insufficient fuel"
    And an arbitrage opportunity for "IRON_ORE" from "X1-A1" to "X1-B1"
    When I execute arbitrage with ship "NAV-FAIL"
    Then the execution should fail with error "navigation to buy market failed"

  Scenario: Fail when market purchase fails
    Given ship "BUY-FAIL" at waypoint "X1-A1"
    And purchase at "X1-A1" will fail with "insufficient credits"
    And an arbitrage opportunity for "IRON_ORE" from "X1-A1" to "X1-B1"
    When I execute arbitrage with ship "BUY-FAIL"
    Then the execution should fail with error "purchase failed"

  # ============================================================================
  # Workflow Validation
  # ============================================================================

  Scenario: Track execution duration
    Given ship "TIMER-SHIP" at waypoint "X1-A1"
    And an arbitrage opportunity for "IRON_ORE" from "X1-A1" to "X1-B1"
    When I execute arbitrage with ship "TIMER-SHIP"
    Then the execution should succeed
    And execution duration should be greater than 0 seconds

  Scenario: Navigation uses BURN flight mode for speed
    Given ship "SPEED-SHIP" at waypoint "X1-A1"
    And an arbitrage opportunity for "IRON_ORE" from "X1-A1" to "X1-B1"
    When I execute arbitrage with ship "SPEED-SHIP"
    Then navigation commands should use flight mode "BURN"
    # Arbitrage prioritizes speed over fuel efficiency
