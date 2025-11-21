Feature: Evaluate Contract Profitability Query

  Background:
    Given the current time is "2099-01-01T00:00:00Z"

  Scenario: Profitable contract with single delivery
    Given a player with ID 1 and token "test-token" exists in the database
    And a ship "SHIP-1" with cargo capacity 100 for player 1 exists
    And an unaccepted contract "CONTRACT-1" with payment 100000/200000 requiring 100 "IRON_ORE"
    And market price for "IRON_ORE" is 500 credits at "X1-MARKET"
    And fuel cost per trip is 10000 credits
    When I execute evaluate profitability query for "CONTRACT-1" with ship "SHIP-1"
    Then the query should succeed
    And the contract should be evaluated as profitable
    And net profit should be 240000
    And total payment should be 300000
    And purchase cost should be 50000
    And fuel cost should be 10000

  Scenario: Acceptable small loss within threshold
    Given a player with ID 1 and token "test-token" exists in the database
    And a ship "SHIP-1" with cargo capacity 50 for player 1 exists
    And an unaccepted contract "CONTRACT-2" with payment 50000/100000 requiring 100 "IRON_ORE"
    And market price for "IRON_ORE" is 1200 credits at "X1-MARKET"
    And fuel cost per trip is 5000 credits
    When I execute evaluate profitability query for "CONTRACT-2" with ship "SHIP-1"
    Then the query should succeed
    And the contract should be evaluated as profitable
    And profitability reason should be "acceptable loss within threshold"

  Scenario: Unacceptable loss exceeding threshold
    Given a player with ID 1 and token "test-token" exists in the database
    And a ship "SHIP-1" with cargo capacity 50 for player 1 exists
    And an unaccepted contract "CONTRACT-3" with payment 10000/20000 requiring 100 "IRON_ORE"
    And market price for "IRON_ORE" is 1500 credits at "X1-MARKET"
    And fuel cost per trip is 10000 credits
    When I execute evaluate profitability query for "CONTRACT-3" with ship "SHIP-1"
    Then the query should succeed
    And the contract should not be profitable
    And profitability reason should contain "loss exceeds"

  Scenario: Multi-delivery contract
    Given a player with ID 1 and token "test-token" exists in the database
    And a ship "SHIP-1" with cargo capacity 200 for player 1 exists
    And an unaccepted contract "CONTRACT-4" with payment 150000/300000 requiring:
      | TradeSymbol | Units |
      | IRON_ORE    | 100   |
      | COPPER_ORE  | 100   |
    And market prices:
      | TradeSymbol | Price |
      | IRON_ORE    | 500   |
      | COPPER_ORE  | 600   |
    And fuel cost per trip is 15000 credits
    When I execute evaluate profitability query for "CONTRACT-4" with ship "SHIP-1"
    Then the query should succeed
    And the contract should be evaluated as profitable

  Scenario: No cheapest market found
    Given a player with ID 1 and token "test-token" exists in the database
    And a ship "SHIP-1" with cargo capacity 100 for player 1 exists
    And an unaccepted contract "CONTRACT-5" with payment 100000/200000 requiring 100 "IRON_ORE"
    And no market data exists for "IRON_ORE"
    When I try to execute evaluate profitability query for "CONTRACT-5" with ship "SHIP-1"
    Then the query should return an error containing "no market found"

  Scenario: Ship not found error
    Given a player with ID 1 and token "test-token" exists in the database
    And an unaccepted contract "CONTRACT-6" with payment 100000/200000 requiring 100 "IRON_ORE"
    When I try to execute evaluate profitability query for "CONTRACT-6" with ship "NON-EXISTENT"
    Then the query should return an error containing "ship not found"

  Scenario: Contract not found error
    Given a player with ID 1 and token "test-token" exists in the database
    And a ship "SHIP-1" with cargo capacity 100 for player 1 exists
    When I try to execute evaluate profitability query for "NON-EXISTENT" with ship "SHIP-1"
    Then the query should return an error containing "contract not found"
