Feature: Contract Price Polling for Profitability
  As a contract acquisition system
  I want to wait for profitable market prices before purchasing resources
  So that I don't overpay and lose money on contracts

  Background:
    Given a contract requiring 100 units of IRON_ORE
    And contract payment is 60,000 credits (10,000 + 50,000)
    And a ship with 40 cargo capacity
    And max polling retries of 12 (1 hour total)
    And retry interval of 300 seconds (5 minutes)

  @xfail
  Scenario: Immediately profitable - no polling needed
    Given IRON_ORE market price is 100 credits per unit (profitable)
    When I check if price polling is needed
    Then profitability check shows net profit of 49,700 credits
    And ROI is approximately 482%
    And the system should proceed immediately without polling
    And sleep should NOT be called

  @xfail
  Scenario: Wait for price drop then proceed
    Given IRON_ORE market price starts at 5,000 credits per unit (unprofitable)
    And after 1 retry, price drops to 100 credits per unit (profitable)
    When I wait for profitable price
    Then the system should poll once
    And sleep should be called with 300 seconds
    And after price drop, profitability check passes
    And the system should proceed with acquisition

  @xfail
  Scenario: Timeout after max retries
    Given IRON_ORE market price stays at 5,000 credits per unit (unprofitable)
    And the price never drops during polling window
    When I wait for profitable price
    Then the system should poll 12 times
    And sleep should be called 12 times with 300 seconds each
    And timeout message should be logged
    And the system should execute anyway (to avoid contract expiration)

  @xfail
  Scenario: Enforce minimum profit threshold (5,000 credits)
    Given a contract with payment of 9,000 credits total
    And IRON_ORE market price is 50 credits per unit
    And total cost is 5,300 credits (5,000 + 300 fuel)
    When I evaluate profitability
    Then net profit is 3,700 credits (below 5,000 minimum)
    And the contract should be unprofitable
    And the system should poll waiting for price drop

  @xfail
  Scenario: Use conservative estimate when no market data
    Given no market data is available for IRON_ORE
    When I evaluate profitability
    Then the system should use 5,000 credits per unit (conservative estimate)
    And total cost is 500,300 credits (500,000 + 300 fuel)
    And net profit is -440,300 credits
    And the contract should be unprofitable
    And price polling should continue (waiting for market data)

  @xfail
  Scenario: Profitable contract accepted immediately
    Given a profitable contract with IRON_ORE at 100 cr/unit
    And net profit is 49,700 credits (meets >5,000 threshold)
    And ROI is 482% (meets >5% threshold)
    When I evaluate profitability
    Then the contract should be profitable
    And the system should log "Price is profitable! Proceeding with acquisition..."
    And no polling should occur
