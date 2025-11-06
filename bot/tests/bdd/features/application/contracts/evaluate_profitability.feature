Feature: Evaluate Contract Profitability
  As a fleet operator
  I want to evaluate if a contract is profitable before accepting
  So that I can avoid losing credits on unprofitable contracts

  Background:
    Given a player with agent "TEST_AGENT"

  Scenario: Contract is profitable with single-trip delivery
    Given a contract pays 10000 credits on acceptance and 15000 on fulfillment
    And the contract requires 50 units of "IRON_ORE" delivery
    And the cheapest market sells "IRON_ORE" for 100 credits per unit
    And the ship has cargo capacity of 100 units
    And estimated fuel cost per trip is 200 credits
    When I evaluate contract profitability
    Then the contract should be profitable
    And the net profit should be 19800 credits
    And 1 trip should be required

  Scenario: Contract requires multiple trips due to cargo capacity
    Given a contract pays 5000 credits on acceptance and 20000 on fulfillment
    And the contract requires 150 units of "IRON_ORE" delivery
    And the cheapest market sells "IRON_ORE" for 100 credits per unit
    And the ship has cargo capacity of 50 units
    And estimated fuel cost per trip is 200 credits
    When I evaluate contract profitability
    Then the contract should be profitable
    And the net profit should be 9400 credits
    And 3 trips should be required

  Scenario: Contract is unprofitable due to high purchase costs but loss exceeds threshold
    Given a contract pays 5000 credits on acceptance and 10000 on fulfillment
    And the contract requires 100 units of "IRON_ORE" delivery
    And the cheapest market sells "IRON_ORE" for 200 credits per unit
    And the ship has cargo capacity of 100 units
    And estimated fuel cost per trip is 200 credits
    When I evaluate contract profitability
    Then the contract should not be profitable
    And the net profit should be -5200 credits
    And the reason should indicate "Loss exceeds acceptable threshold"

  Scenario: Contract with small loss is still considered acceptable
    Given a contract pays 5000 credits on acceptance and 10000 on fulfillment
    And the contract requires 50 units of "IRON_ORE" delivery
    And the cheapest market sells "IRON_ORE" for 302 credits per unit
    And the ship has cargo capacity of 100 units
    And estimated fuel cost per trip is 200 credits
    When I evaluate contract profitability
    Then the contract should be profitable
    And the net profit should be -300 credits
    And the reason should indicate "Acceptable small loss"

  Scenario: No market sells required goods
    Given a contract pays 5000 credits on acceptance and 10000 on fulfillment
    And the contract requires 100 units of "IRON_ORE" delivery
    And no market sells "IRON_ORE"
    And the ship has cargo capacity of 100 units
    When I evaluate contract profitability
    Then the contract should not be profitable
    And the evaluation should indicate "No market found selling IRON_ORE"

  Scenario: Multiple deliveries in single contract
    Given a contract pays 10000 credits on acceptance and 30000 on fulfillment
    And the contract requires 50 units of "IRON_ORE" delivery
    And the contract requires 50 units of "COPPER_ORE" delivery
    And the cheapest market sells "IRON_ORE" for 100 credits per unit
    And the cheapest market sells "COPPER_ORE" for 80 credits per unit
    And the ship has cargo capacity of 100 units
    And estimated fuel cost per trip is 200 credits
    When I evaluate contract profitability
    Then the contract should be profitable
    And the net profit should be 30800 credits
    And 1 trip should be required
