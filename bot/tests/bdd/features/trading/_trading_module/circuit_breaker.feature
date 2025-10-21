Feature: Circuit Breaker - Profitability validation

  As a trading system
  I need to validate purchases before execution
  So that I prevent unprofitable trades from losing credits

  Background:
    Given a mock API client
    And a profitability validator with logger
    And a multi-leg route with planned sell prices

  # Profitable Purchase Validation

  Scenario: Purchase is profitable - validation passes
    Given a BUY action for "COPPER" at 100 credits per unit
    And the planned sell price is 500 credits per unit
    And the live market price is 100 credits per unit
    When validating purchase profitability for 20 units
    Then validation should pass
    And profit margin should be 400 credits per unit
    And profit margin percentage should be 80.0 percent

  Scenario: Purchase with price increase but still profitable
    Given a BUY action for "IRON" at 80 credits per unit
    And the planned sell price is 300 credits per unit
    And the live market price is 120 credits per unit
    When validating purchase profitability for 15 units
    Then validation should pass
    And price change should be 50.0 percent
    And profit margin should be 180 credits per unit
    And a high volatility warning should be logged

  # Unprofitable Purchase Blocking

  Scenario: Purchase would be unprofitable - circuit breaker triggers
    Given a BUY action for "ALUMINUM" at 68 credits per unit
    And the planned sell price is 558 credits per unit
    And the live market price is 600 credits per unit
    When validating purchase profitability for 20 units
    Then validation should fail
    And error message should contain "Unprofitable"
    And purchase should be blocked
    And loss would be 42 credits per unit

  Scenario: Purchase price exactly equals sell price - circuit breaker triggers
    Given a BUY action for "GOLD" at 1500 credits per unit
    And the planned sell price is 2000 credits per unit
    And the live market price is 2000 credits per unit
    When validating purchase profitability for 10 units with degradation
    Then validation should fail
    And error message should contain "Unprofitable"
    And expected sell price after degradation should be 1980 credits

  # Price Volatility Detection

  Scenario: Moderate price increase (<30%) - validation passes with warning
    Given a BUY action for "COPPER" at 1000 credits per unit
    And the planned sell price is 1500 credits per unit
    And the live market price is 1200 credits per unit
    When validating purchase profitability for 10 units
    Then validation should pass
    And price change should be 20.0 percent
    And a price change warning should be logged

  Scenario: High price volatility (>30%) but still profitable
    Given a BUY action for "IRON" at 800 credits per unit
    And the planned sell price is 2000 credits per unit
    And the live market price is 1100 credits per unit
    When validating purchase profitability for 15 units
    Then validation should pass
    And price change should be 37.5 percent
    And a high volatility warning should be logged
    And profit margin should be 900 credits per unit

  Scenario: Extreme volatility (>50%) with no planned sell price - circuit breaker triggers
    Given a BUY action for "PLATINUM" at 3000 credits per unit
    And no planned sell price exists
    And the live market price is 5000 credits per unit
    When validating purchase profitability for 5 units
    Then validation should fail
    And error message should contain "Extreme volatility"
    And error message should contain "no planned sell price"
    And price spike should be 66.7 percent

  Scenario: Moderate volatility with no planned sell price - validation passes
    Given a BUY action for "COPPER" at 100 credits per unit
    And no planned sell price exists
    And the live market price is 130 credits per unit
    When validating purchase profitability for 20 units
    Then validation should pass
    And price change should be 30.0 percent

  # Market Data Failures

  Scenario: Market API call fails - circuit breaker triggers
    Given a BUY action for "IRON" at 150 credits per unit
    And the market API throws an exception
    When validating purchase profitability for 10 units
    Then validation should fail
    And error message should contain "Market API failure"
    And purchase should be blocked for safety

  Scenario: Market data unavailable - circuit breaker triggers
    Given a BUY action for "COPPER" at 100 credits per unit
    And the market API returns None
    When validating purchase profitability for 20 units
    Then validation should fail
    And error message should contain "Market data unavailable"
    And purchase should be blocked

  Scenario: No live price data for good - circuit breaker triggers
    Given a BUY action for "RARE_METAL" at 5000 credits per unit
    And the market API returns data without "RARE_METAL"
    When validating purchase profitability for 3 units
    Then validation should fail
    And error message should contain "No live price data"
    And purchase should be blocked

  # Batch Size Calculation

  Scenario: High-value good (≥2000 cr/unit) uses 2-unit batches
    Given a good priced at 2892 credits per unit
    When calculating batch size
    Then batch size should be 2 units
    And rationale should be "minimal risk strategy"

  Scenario: Medium-high value good (≥1500 cr/unit) uses 3-unit batches
    Given a good priced at 1500 credits per unit
    When calculating batch size
    Then batch size should be 3 units
    And rationale should be "cautious approach"

  Scenario: Standard good (≥50 cr/unit) uses 5-unit batches
    Given a good priced at 150 credits per unit
    When calculating batch size
    Then batch size should be 5 units
    And rationale should be "default batching"

  Scenario: Very low-value good (<50 cr/unit) uses 10-unit batches
    Given a good priced at 30 credits per unit
    When calculating batch size
    Then batch size should be 10 units
    And rationale should be "bulk efficiency"

  Scenario: Boundary case - exactly 2000 credits uses 2-unit batches
    Given a good priced at 2000 credits per unit
    When calculating batch size
    Then batch size should be 2 units

  Scenario: Boundary case - exactly 1500 credits uses 3-unit batches
    Given a good priced at 1500 credits per unit
    When calculating batch size
    Then batch size should be 3 units

  Scenario: Boundary case - exactly 50 credits uses 5-unit batches
    Given a good priced at 50 credits per unit
    When calculating batch size
    Then batch size should be 5 units

  # Price Degradation in Profitability Check

  Scenario: Large quantity sale - degradation affects profitability
    Given a BUY action for "COPPER" at 100 credits per unit
    And the planned sell price is 200 credits per unit
    And the live market price is 100 credits per unit
    When validating purchase profitability for 40 units
    Then expected sell price should account for 2% degradation
    And expected sell price should be 196 credits per unit
    And profit margin should be 96 credits per unit

  Scenario: Very large quantity - degradation capped at 5%
    Given a BUY action for "IRON" at 50 credits per unit
    And the planned sell price is 150 credits per unit
    And the live market price is 50 credits per unit
    When validating purchase profitability for 200 units
    Then expected sell price should account for 5% degradation cap
    And expected sell price should be 142 credits per unit
    And profit margin should be 92 credits per unit
