Feature: Arbitrage Coordinator Ship Assignment
  As a SpaceTraders bot
  I want the arbitrage coordinator to assign ships optimally
  So that profitable trades use the nearest available ships

  # ============================================================================
  # Profit-First Ship Assignment Algorithm
  # ============================================================================
  #
  # Algorithm: For each opportunity (sorted by profitability), assign the
  # closest available ship to minimize travel costs.
  #
  # Benefits:
  # - Best trades always executed first (maximize margins)
  # - Each trade uses most efficient ship (minimize fuel costs)
  # - No wasted travel time on low-margin opportunities
  # ============================================================================

  Background:
    Given a system "X1-TEST" with the following waypoints:
      | Symbol  | X    | Y    |
      | X1-A1   | 0    | 0    |
      | X1-B1   | 10   | 0    |
      | X1-C1   | 50   | 0    |
      | X1-D1   | 100  | 0    |

  Scenario: Assign closest ship to profitable opportunity
    Given the following idle hauler ships:
      | Symbol     | Location |
      | HAULER-1   | X1-A1    |
      | HAULER-2   | X1-D1    |
    And the following arbitrage opportunities sorted by profitability:
      | Good      | BuyMarket | SellMarket | Margin |
      | IRON_ORE  | X1-B1     | X1-C1      | 25%    |
    When the coordinator assigns ships to opportunities
    Then ship "HAULER-1" should be assigned to "IRON_ORE" opportunity
    # HAULER-1 at X1-A1 is distance 10 from X1-B1
    # HAULER-2 at X1-D1 is distance 90 from X1-B1
    # Coordinator chooses HAULER-1 (closer)

  Scenario: Assign multiple ships with spatial optimization
    Given the following idle hauler ships:
      | Symbol     | Location |
      | HAULER-1   | X1-A1    |
      | HAULER-2   | X1-D1    |
      | HAULER-3   | X1-C1    |
    And the following arbitrage opportunities sorted by profitability:
      | Good         | BuyMarket | SellMarket | Margin |
      | IRON_ORE     | X1-B1     | X1-C1      | 25%    |
      | COPPER_ORE   | X1-D1     | X1-A1      | 20%    |
      | ALUMINUM_ORE | X1-A1     | X1-B1      | 15%    |
    When the coordinator assigns ships to opportunities
    Then ship "HAULER-1" should be assigned to "IRON_ORE" opportunity
    # Best opportunity (25%), HAULER-1 closest to X1-B1 (distance 10)
    And ship "HAULER-2" should be assigned to "COPPER_ORE" opportunity
    # Second best (20%), HAULER-2 closest to X1-D1 (distance 0)
    And ship "HAULER-3" should be assigned to "ALUMINUM_ORE" opportunity
    # Third best (15%), HAULER-3 is only remaining ship

  Scenario: Respect maxWorkers limit
    Given the following idle hauler ships:
      | Symbol     | Location |
      | HAULER-1   | X1-A1    |
      | HAULER-2   | X1-B1    |
      | HAULER-3   | X1-C1    |
    And the following arbitrage opportunities sorted by profitability:
      | Good         | BuyMarket | SellMarket | Margin |
      | IRON_ORE     | X1-B1     | X1-C1      | 25%    |
      | COPPER_ORE   | X1-C1     | X1-D1      | 20%    |
      | ALUMINUM_ORE | X1-A1     | X1-B1      | 15%    |
    And max workers is set to 2
    When the coordinator assigns ships to opportunities
    Then exactly 2 workers should be spawned
    And ship "HAULER-2" should be assigned to "IRON_ORE" opportunity
    # HAULER-2 at X1-B1 is distance 0 from buy market X1-B1
    And ship "HAULER-3" should be assigned to "COPPER_ORE" opportunity
    # HAULER-3 at X1-C1 is distance 0 from buy market X1-C1
    # ALUMINUM_ORE opportunity not executed due to maxWorkers=2

  Scenario: More opportunities than ships
    Given the following idle hauler ships:
      | Symbol     | Location |
      | HAULER-1   | X1-A1    |
      | HAULER-2   | X1-D1    |
    And the following arbitrage opportunities sorted by profitability:
      | Good         | BuyMarket | SellMarket | Margin |
      | IRON_ORE     | X1-B1     | X1-C1      | 30%    |
      | COPPER_ORE   | X1-C1     | X1-D1      | 25%    |
      | ALUMINUM_ORE | X1-A1     | X1-B1      | 20%    |
      | GOLD         | X1-D1     | X1-A1      | 15%    |
    When the coordinator assigns ships to opportunities
    Then exactly 2 workers should be spawned
    # Only 2 ships available, so only top 2 opportunities executed
    And ship "HAULER-1" should be assigned to "IRON_ORE" opportunity
    And ship "HAULER-2" should be assigned to "COPPER_ORE" opportunity

  Scenario: More ships than opportunities
    Given the following idle hauler ships:
      | Symbol     | Location |
      | HAULER-1   | X1-A1    |
      | HAULER-2   | X1-B1    |
      | HAULER-3   | X1-C1    |
      | HAULER-4   | X1-D1    |
    And the following arbitrage opportunities sorted by profitability:
      | Good         | BuyMarket | SellMarket | Margin |
      | IRON_ORE     | X1-B1     | X1-C1      | 25%    |
      | COPPER_ORE   | X1-D1     | X1-A1      | 20%    |
    When the coordinator assigns ships to opportunities
    Then exactly 2 workers should be spawned
    # Only 2 opportunities, so 2 ships remain idle
    And ship "HAULER-2" should be assigned to "IRON_ORE" opportunity
    # HAULER-2 at X1-B1 is distance 0 from X1-B1 (closest)
    And ship "HAULER-4" should be assigned to "COPPER_ORE" opportunity
    # HAULER-4 at X1-D1 is distance 0 from X1-D1 (closest)

  # ============================================================================
  # Distance Calculation Examples
  # ============================================================================

  Scenario: Choose ship based on distance to buy market
    Given the following idle hauler ships:
      | Symbol     | Location |
      | HAULER-1   | X1-A1    |
      | HAULER-2   | X1-C1    |
    And the following arbitrage opportunities sorted by profitability:
      | Good      | BuyMarket | SellMarket | Margin |
      | IRON_ORE  | X1-B1     | X1-D1      | 20%    |
    When the coordinator assigns ships to opportunities
    Then ship "HAULER-1" should be assigned to "IRON_ORE" opportunity
    # HAULER-1 at X1-A1 (0,0) -> X1-B1 (10,0) = distance 10
    # HAULER-2 at X1-C1 (50,0) -> X1-B1 (10,0) = distance 40
    # Coordinator chooses HAULER-1 (closer to buy market)

  Scenario: Distance to buy market matters, not sell market
    Given the following idle hauler ships:
      | Symbol     | Location |
      | HAULER-1   | X1-A1    |
      | HAULER-2   | X1-D1    |
    And the following arbitrage opportunities sorted by profitability:
      | Good      | BuyMarket | SellMarket | Margin |
      | IRON_ORE  | X1-A1     | X1-D1      | 20%    |
    When the coordinator assigns ships to opportunities
    Then ship "HAULER-1" should be assigned to "IRON_ORE" opportunity
    # HAULER-1 at X1-A1 (0,0) -> buy at X1-A1 (0,0) = distance 0
    # HAULER-2 at X1-D1 (100,0) -> buy at X1-A1 (0,0) = distance 100
    # Even though HAULER-2 is at sell market, HAULER-1 is closer to buy market

  # ============================================================================
  # Logging and Observability
  # ============================================================================

  Scenario: Log assignment decisions
    Given the following idle hauler ships:
      | Symbol     | Location |
      | HAULER-1   | X1-B1    |
    And the following arbitrage opportunities sorted by profitability:
      | Good      | BuyMarket | SellMarket | Margin |
      | IRON_ORE  | X1-C1     | X1-D1      | 25%    |
    When the coordinator assigns ships to opportunities
    Then coordinator should log "Assigned ship HAULER-1 to opportunity IRON_ORE (distance: 40.0, margin: 25.0%)"
    # Provides visibility into assignment decisions

  Scenario: Log optimal assignment completion
    Given 3 idle hauler ships
    And 3 arbitrage opportunities
    When the coordinator assigns ships to opportunities
    Then coordinator should log "Spawning 3 arbitrage workers with optimal assignments"
