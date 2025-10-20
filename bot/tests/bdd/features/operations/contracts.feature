Feature: Contract operations
  As a fleet commander
  I want to evaluate and fulfill contracts profitably
  So that I can maximize revenue and meet contractual obligations

  Background:
    Given a mock API client
    And a mock database for market prices

  Scenario: Evaluate profitable contract with market data
    Given a contract requiring 100 units of "IRON_ORE"
    And contract payment is 50000 credits on acceptance and 100000 credits on fulfillment
    And ship has 40 units cargo capacity
    And market price for "IRON_ORE" is 500 credits per unit
    When I evaluate contract profitability
    Then evaluation should succeed
    And contract should be marked as profitable
    And metrics should show positive net profit
    And metrics should include market price source

  Scenario: Reject unprofitable contract with expensive goods
    Given a contract requiring 100 units of "ADVANCED_CIRCUITRY"
    And contract payment is 20000 credits on acceptance and 30000 credits on fulfillment
    And ship has 40 units cargo capacity
    And market price for "ADVANCED_CIRCUITRY" is 8000 credits per unit
    When I evaluate contract profitability
    Then evaluation should succeed
    And contract should be marked as unprofitable
    And rejection reason should mention insufficient profit

  Scenario: Evaluate contract with no market data uses conservative estimate
    Given a contract requiring 50 units of "GOLD_ORE"
    And contract payment is 100000 credits on acceptance and 200000 credits on fulfillment
    And ship has 40 units cargo capacity
    And no market data available for "GOLD_ORE"
    When I evaluate contract profitability
    Then evaluation should succeed
    And metrics should show price source as "estimated (conservative)"
    And metrics should use 5000 credits per unit estimate

  Scenario: Reject already fulfilled contract
    Given a contract requiring 100 units of "COPPER_ORE"
    And contract payment is 50000 credits on acceptance and 100000 credits on fulfillment
    And contract is already fulfilled
    When I evaluate contract profitability
    Then evaluation should succeed
    And contract should be marked as unprofitable
    And rejection reason should be "Contract already fulfilled"

  Scenario: Calculate trips correctly for cargo capacity
    Given a contract requiring 85 units of "IRON_ORE"
    And contract payment is 50000 credits on acceptance and 100000 credits on fulfillment
    And ship has 40 units cargo capacity
    And market price for "IRON_ORE" is 500 credits per unit
    When I evaluate contract profitability
    Then evaluation should succeed
    And metrics should show 3 trips required
    And fuel cost should be estimated at 300 credits

  Scenario: Accept contract with small acceptable loss
    Given a contract requiring 100 units of "COPPER_ORE"
    And contract payment is 50000 credits on acceptance and 60000 credits on fulfillment
    And ship has 40 units cargo capacity
    And market price for "COPPER_ORE" is 1100 credits per unit
    When I evaluate contract profitability
    Then evaluation should succeed
    And contract should be marked as profitable
    And metrics should show net loss within acceptable range

  Scenario: Reject contract with loss exceeding threshold
    Given a contract requiring 100 units of "PRECIOUS_STONES"
    And contract payment is 10000 credits on acceptance and 20000 credits on fulfillment
    And ship has 40 units cargo capacity
    And market price for "PRECIOUS_STONES" is 2000 credits per unit
    When I evaluate contract profitability
    Then evaluation should succeed
    And contract should be marked as unprofitable
    And rejection reason should mention net profit below minimum

  Scenario: Handle partial fulfillment correctly
    Given a contract requiring 100 units of "IRON_ORE"
    And contract already has 60 units fulfilled
    And contract payment is 50000 credits on acceptance and 100000 credits on fulfillment
    And ship has 40 units cargo capacity
    And market price for "IRON_ORE" is 500 credits per unit
    When I evaluate contract profitability
    Then evaluation should succeed
    And metrics should show 40 units remaining
    And metrics should show 1 trip required
    And contract should be marked as profitable
