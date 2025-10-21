Feature: Market Service - Price estimation and validation

  As a trading system
  I need accurate market price operations
  So that I can make informed trading decisions

  Background:
    Given a database with market data
    And a logger instance

  # Price Degradation Estimation

  Scenario: Estimate sell price with no degradation for small quantities
    Given a base price of 8000 credits per unit
    When estimating sell price for 10 units
    Then the effective price should be 7960 credits per unit
    And the degradation should be approximately 0.5 percent

  Scenario: Estimate sell price with moderate degradation for medium quantities
    Given a base price of 8000 credits per unit
    When estimating sell price for 40 units
    Then the effective price should be 7840 credits per unit
    And the degradation should be approximately 2.0 percent

  Scenario: Estimate sell price capped at 5% for large quantities
    Given a base price of 8000 credits per unit
    When estimating sell price for 200 units
    Then the effective price should be 7600 credits per unit
    And the degradation should be capped at 5.0 percent

  Scenario: Real-world calibration - SHIP_PLATING sale (18 units, tradeVolume=6)
    Given a base price of 10000 credits per unit
    When estimating sell price for 18 units
    Then the effective price should be approximately 9910 credits per unit
    And the degradation should be less than 3.0 percent

  # Find Planned Sell Price

  Scenario: Find planned sell price in future route segment
    Given a multi-leg route with 3 segments
    And segment 0 has BUY action for "ALUMINUM_ORE" at 68 credits
    And segment 1 has SELL action for "ALUMINUM_ORE" at 558 credits
    When finding planned sell price for "ALUMINUM_ORE" from segment 0
    Then the planned sell price should be 558 credits per unit

  Scenario: No sell price found when good not sold in remaining route
    Given a multi-leg route with 2 segments
    And segment 0 has BUY action for "COPPER" at 100 credits
    And segment 1 has BUY action for "IRON" at 150 credits
    When finding planned sell price for "COPPER" from segment 0
    Then the planned sell price should be None

  Scenario: Find sell price in last segment of route
    Given a multi-leg route with 3 segments
    And segment 0 has BUY action for "GOLD" at 1500 credits
    And segment 2 has SELL action for "GOLD" at 2200 credits
    When finding planned sell price for "GOLD" from segment 0
    Then the planned sell price should be 2200 credits per unit

  # Find Planned Sell Destination

  Scenario: Find planned sell destination waypoint
    Given a multi-leg route with 3 segments
    And segment 0 has BUY action for "ALUMINUM" at waypoint "X1-JB26-E45"
    And segment 1 has SELL action for "ALUMINUM" at waypoint "X1-JB26-D42"
    When finding planned sell destination for "ALUMINUM" from segment 0
    Then the planned sell destination should be "X1-JB26-D42"

  Scenario: No sell destination found when good carried to end
    Given a multi-leg route with 2 segments
    And segment 0 has BUY action for "IRON" at waypoint "X1-TEST-A1"
    And no sell actions for "IRON" in remaining segments
    When finding planned sell destination for "IRON" from segment 0
    Then the planned sell destination should be None

  # Market Price Updates from Transactions

  Scenario: Update database with actual purchase price
    Given a market database with existing data for waypoint "X1-TEST-B7" and good "COPPER"
    When updating market price from PURCHASE transaction
    And the transaction price is 1250 credits per unit
    Then the database should update sell_price to 1250
    And the purchase_price should remain unchanged
    And the last_updated timestamp should be current

  Scenario: Update database with actual sell price
    Given a market database with existing data for waypoint "X1-TEST-B7" and good "IRON"
    When updating market price from SELL transaction
    And the transaction price is 580 credits per unit
    Then the database should update purchase_price to 580
    And the sell_price should remain unchanged
    And the last_updated timestamp should be current

  Scenario: Insert new market data when no existing data
    Given a market database with no data for waypoint "X1-NEW-A1" and good "GOLD"
    When updating market price from PURCHASE transaction
    And the transaction price is 2000 credits per unit
    Then a new market data entry should be created
    And sell_price should be 2000
    And purchase_price should be None

  # Market Data Freshness Validation

  Scenario: All market data is fresh - validation passes
    Given a multi-leg route with 2 segments
    And segment 0 requires "COPPER" at waypoint "X1-TEST-B7"
    And market data for "COPPER" at "X1-TEST-B7" updated 10 minutes ago
    And segment 1 requires "IRON" at waypoint "X1-TEST-D42"
    And market data for "IRON" at "X1-TEST-D42" updated 15 minutes ago
    When validating market data freshness with 1.0 hour stale threshold
    Then validation should pass
    And no stale markets should be reported
    And no aging markets should be reported

  Scenario: Some market data is aging - validation passes with warning
    Given a multi-leg route with 2 segments
    And segment 0 requires "COPPER" at waypoint "X1-TEST-B7"
    And market data for "COPPER" at "X1-TEST-B7" updated 40 minutes ago
    And segment 1 requires "IRON" at waypoint "X1-TEST-D42"
    And market data for "IRON" at "X1-TEST-D42" updated 10 minutes ago
    When validating market data freshness with 1.0 hour stale threshold
    And aging threshold is 0.5 hours
    Then validation should pass
    And 1 aging market should be reported
    And aging market should be "X1-TEST-B7" "COPPER"

  Scenario: Market data is stale - validation fails
    Given a multi-leg route with 2 segments
    And segment 0 requires "COPPER" at waypoint "X1-TEST-B7"
    And market data for "COPPER" at "X1-TEST-B7" updated 2 hours ago
    And segment 1 requires "IRON" at waypoint "X1-TEST-D42"
    And market data for "IRON" at "X1-TEST-D42" updated 10 minutes ago
    When validating market data freshness with 1.0 hour stale threshold
    Then validation should fail
    And 1 stale market should be reported
    And stale market should be "X1-TEST-B7" "COPPER" aged 2.0 hours

  Scenario: Multiple stale markets - validation fails with all reported
    Given a multi-leg route with 3 segments
    And segment 0 requires "COPPER" at waypoint "X1-TEST-B7"
    And market data for "COPPER" at "X1-TEST-B7" updated 2 hours ago
    And segment 1 requires "IRON" at waypoint "X1-TEST-D42"
    And market data for "IRON" at "X1-TEST-D42" updated 1.5 hours ago
    And segment 2 requires "GOLD" at waypoint "X1-TEST-E45"
    And market data for "GOLD" at "X1-TEST-E45" updated 10 minutes ago
    When validating market data freshness with 1.0 hour stale threshold
    Then validation should fail
    And 2 stale markets should be reported
    And stale markets should include "X1-TEST-B7" "COPPER"
    And stale markets should include "X1-TEST-D42" "IRON"

  Scenario: Missing market data - validation warns but continues
    Given a multi-leg route with 2 segments
    And segment 0 requires "COPPER" at waypoint "X1-TEST-B7"
    And no market data exists for "COPPER" at "X1-TEST-B7"
    And segment 1 requires "IRON" at waypoint "X1-TEST-D42"
    And market data for "IRON" at "X1-TEST-D42" updated 10 minutes ago
    When validating market data freshness with 1.0 hour stale threshold
    Then validation should pass
    And a warning should be logged for missing data
    And "X1-TEST-B7" "COPPER" should be reported as missing
