Feature: Arbitrage Opportunity Finder
  As a SpaceTraders bot
  I want to discover profitable arbitrage opportunities across markets
  So that I can execute automated buy-sell trading cycles

  # ============================================================================
  # Basic Opportunity Discovery
  # ============================================================================

  Scenario: Find simple arbitrage opportunity
    Given markets in system "X1-TEST":
      | Waypoint | Good       | SellPrice | PurchasePrice | Supply   | Activity |
      | X1-A1    | IRON_ORE   | 100       | 0             | ABUNDANT | STRONG   |
      | X1-B1    | IRON_ORE   | 0         | 150           | LIMITED  | GROWING  |
    And ship cargo capacity is 40 units
    And minimum margin threshold is 10.0%
    When I scan for arbitrage opportunities in system "X1-TEST"
    Then I should find 1 opportunity
    And opportunity 1 should have:
      | Good       | IRON_ORE |
      | BuyMarket  | X1-A1    |
      | SellMarket | X1-B1    |
      | BuyPrice   | 100      |
      | SellPrice  | 150      |
      | ProfitPerUnit | 50    |
      | ProfitMargin  | 50.0  |
      | EstimatedProfit | 2000 |

  Scenario: Filter opportunities below margin threshold
    Given markets in system "X1-TEST":
      | Waypoint | Good       | SellPrice | PurchasePrice | Supply   | Activity |
      | X1-A1    | IRON_ORE   | 100       | 0             | ABUNDANT | STRONG   |
      | X1-B1    | IRON_ORE   | 0         | 105           | LIMITED  | WEAK     |
    And ship cargo capacity is 40 units
    And minimum margin threshold is 10.0%
    When I scan for arbitrage opportunities in system "X1-TEST"
    Then I should find 0 opportunities
    # 5% margin ((105-100)/100) is below 10% threshold

  # ============================================================================
  # Multiple Opportunities & Scoring
  # ============================================================================

  Scenario: Rank multiple opportunities by composite score
    Given markets in system "X1-TEST":
      | Waypoint | Good         | SellPrice | PurchasePrice | Supply   | Activity   |
      | X1-A1    | IRON_ORE     | 100       | 0             | ABUNDANT | STRONG     |
      | X1-A2    | COPPER_ORE   | 50        | 0             | SCARCE   | WEAK       |
      | X1-B1    | IRON_ORE     | 0         | 120           | LIMITED  | GROWING    |
      | X1-B2    | COPPER_ORE   | 0         | 80            | MODERATE | GROWING    |
    And ship cargo capacity is 40 units
    And minimum margin threshold is 10.0%
    When I scan for arbitrage opportunities in system "X1-TEST"
    Then I should find 2 opportunities
    And opportunity 1 should have higher score than opportunity 2
    # IRON_ORE: 20% margin, ABUNDANT supply, STRONG activity (high score)
    # COPPER_ORE: 60% margin, SCARCE supply, WEAK activity (mixed score)

  Scenario: Limit results to top N opportunities
    Given markets in system "X1-TEST":
      | Waypoint | Good       | SellPrice | PurchasePrice | Supply   | Activity |
      | X1-A1    | GOOD_1     | 100       | 0             | ABUNDANT | STRONG   |
      | X1-A2    | GOOD_2     | 100       | 0             | ABUNDANT | STRONG   |
      | X1-A3    | GOOD_3     | 100       | 0             | ABUNDANT | STRONG   |
      | X1-B1    | GOOD_1     | 0         | 120           | LIMITED  | GROWING  |
      | X1-B2    | GOOD_2     | 0         | 125           | LIMITED  | GROWING  |
      | X1-B3    | GOOD_3     | 0         | 130           | LIMITED  | GROWING  |
    And ship cargo capacity is 40 units
    And minimum margin threshold is 10.0%
    And result limit is 2
    When I scan for arbitrage opportunities in system "X1-TEST"
    Then I should find exactly 2 opportunities

  # ============================================================================
  # Edge Cases
  # ============================================================================

  Scenario: No profitable opportunities in system
    Given markets in system "X1-TEST":
      | Waypoint | Good       | SellPrice | PurchasePrice | Supply   | Activity |
      | X1-A1    | IRON_ORE   | 100       | 0             | ABUNDANT | STRONG   |
      | X1-B1    | IRON_ORE   | 0         | 90            | LIMITED  | WEAK     |
    And ship cargo capacity is 40 units
    And minimum margin threshold is 10.0%
    When I scan for arbitrage opportunities in system "X1-TEST"
    Then I should find 0 opportunities
    # Sell price (90) < buy price (100) = no profit

  Scenario: Market sells and buys same good (ignore self-trade)
    Given markets in system "X1-TEST":
      | Waypoint | Good       | SellPrice | PurchasePrice | Supply   | Activity |
      | X1-A1    | IRON_ORE   | 100       | 150           | MODERATE | STRONG   |
    And ship cargo capacity is 40 units
    And minimum margin threshold is 10.0%
    When I scan for arbitrage opportunities in system "X1-TEST"
    Then I should find 0 opportunities
    # Cannot trade with same market

  Scenario: No markets in system
    Given system "X1-EMPTY" has no markets
    And ship cargo capacity is 40 units
    And minimum margin threshold is 10.0%
    When I scan for arbitrage opportunities in system "X1-EMPTY"
    Then I should receive error "no arbitrage opportunities found"

  # ============================================================================
  # Scoring Algorithm Validation
  # ============================================================================

  Scenario: Higher profit margin yields higher score
    Given markets in system "X1-TEST":
      | Waypoint | Good     | SellPrice | PurchasePrice | Supply   | Activity |
      | X1-A1    | LOW_MARGIN  | 100    | 0             | MODERATE | STRONG   |
      | X1-A2    | HIGH_MARGIN | 100    | 0             | MODERATE | STRONG   |
      | X1-B1    | LOW_MARGIN  | 0      | 110           | MODERATE | STRONG   |
      | X1-B2    | HIGH_MARGIN | 0      | 150           | MODERATE | STRONG   |
    And ship cargo capacity is 40 units
    And minimum margin threshold is 5.0%
    When I scan for arbitrage opportunities in system "X1-TEST"
    Then opportunity "HIGH_MARGIN" should have higher score than "LOW_MARGIN"
    # HIGH_MARGIN: 50% profit margin
    # LOW_MARGIN: 10% profit margin

  Scenario: Better supply level yields higher score
    Given markets in system "X1-TEST":
      | Waypoint | Good         | SellPrice | PurchasePrice | Supply   | Activity |
      | X1-A1    | ABUNDANT_SUP | 100       | 0             | ABUNDANT | STRONG   |
      | X1-A2    | SCARCE_SUP   | 100       | 0             | SCARCE   | STRONG   |
      | X1-B1    | ABUNDANT_SUP | 0         | 120           | MODERATE | STRONG   |
      | X1-B2    | SCARCE_SUP   | 0         | 120           | MODERATE | STRONG   |
    And ship cargo capacity is 40 units
    And minimum margin threshold is 10.0%
    When I scan for arbitrage opportunities in system "X1-TEST"
    Then opportunity "ABUNDANT_SUP" should have higher score than "SCARCE_SUP"
    # ABUNDANT supply (20 pts) > SCARCE supply (0 pts)

  Scenario: Stronger activity yields higher score
    Given markets in system "X1-TEST":
      | Waypoint | Good        | SellPrice | PurchasePrice | Supply   | Activity   |
      | X1-A1    | STRONG_ACT  | 100       | 0             | MODERATE | STRONG     |
      | X1-A2    | WEAK_ACT    | 100       | 0             | MODERATE | WEAK       |
      | X1-B1    | STRONG_ACT  | 0         | 120           | MODERATE | STRONG     |
      | X1-B2    | WEAK_ACT    | 0         | 120           | MODERATE | WEAK       |
    And ship cargo capacity is 40 units
    And minimum margin threshold is 10.0%
    When I scan for arbitrage opportunities in system "X1-TEST"
    Then opportunity "STRONG_ACT" should have higher score than "WEAK_ACT"
    # STRONG activity (20 pts) > WEAK activity (5 pts)
