Feature: Evaluate Contract Profitability Query

  Background:
    Given a system "X1-GZ7"

  Scenario: Profitable contract with single delivery
    Given a contract paying 10000 credits on acceptance and 50000 on fulfillment
    And the contract requires delivery of 100 "IRON_ORE" to "X1-GZ7-A1"
    And the cheapest market sells "IRON_ORE" at 200 credits per unit in system "X1-GZ7"
    And a ship with 100 cargo capacity
    And fuel cost is 1000 credits per trip
    When I evaluate the contract profitability
    Then the net profit should be 39000 credits
    And the contract should be profitable
    And trips required should be 1
    And the purchase cost should be 20000 credits

  Scenario: Acceptable small loss within threshold
    Given a contract paying 10000 credits on acceptance and 20000 on fulfillment
    And the contract requires delivery of 100 "IRON_ORE" to "X1-GZ7-A1"
    And the cheapest market sells "IRON_ORE" at 300 credits per unit in system "X1-GZ7"
    And a ship with 100 cargo capacity
    And fuel cost is 4000 credits per trip
    When I evaluate the contract profitability
    Then the net profit should be -4000 credits
    And the contract should be profitable
    And the reason should contain "Acceptable small loss"
    And trips required should be 1

  Scenario: Unacceptable loss exceeding threshold
    Given a contract paying 5000 credits on acceptance and 10000 on fulfillment
    And the contract requires delivery of 200 "IRON_ORE" to "X1-GZ7-A1"
    And the cheapest market sells "IRON_ORE" at 300 credits per unit in system "X1-GZ7"
    And a ship with 50 cargo capacity
    And fuel cost is 2000 credits per trip
    When I evaluate the contract profitability
    Then the net profit should be -53000 credits
    And the contract should not be profitable
    And the reason should contain "Loss exceeds acceptable threshold"
    And trips required should be 4

  Scenario: Multi-delivery contract
    Given a contract paying 20000 credits on acceptance and 80000 on fulfillment
    And the contract requires delivery of 100 "IRON_ORE" to "X1-GZ7-A1"
    And the contract requires delivery of 50 "COPPER_ORE" to "X1-GZ7-B2"
    And the cheapest market sells "IRON_ORE" at 200 credits per unit in system "X1-GZ7"
    And the cheapest market sells "COPPER_ORE" at 300 credits per unit in system "X1-GZ7"
    And a ship with 100 cargo capacity
    And fuel cost is 1500 credits per trip
    When I evaluate the contract profitability
    Then the purchase cost should be 35000 credits
    And trips required should be 2
    And the net profit should be 62000 credits
    And the contract should be profitable

  Scenario: No cheapest market found
    Given a contract paying 10000 credits on acceptance and 50000 on fulfillment
    And the contract requires delivery of 100 "RARE_ORE" to "X1-GZ7-A1"
    And no market sells "RARE_ORE" in system "X1-GZ7"
    And a ship with 100 cargo capacity
    When I try to evaluate the contract profitability
    Then I should get an error containing "no market found selling RARE_ORE"

  Scenario: Ship not found
    Given a contract paying 10000 credits on acceptance and 50000 on fulfillment
    And the contract requires delivery of 100 "IRON_ORE" to "X1-GZ7-A1"
    And the cheapest market sells "IRON_ORE" at 200 credits per unit in system "X1-GZ7"
    When I try to evaluate the contract profitability with an invalid ship
    Then I should get an error containing "ship not found"

  Scenario: Contract with partially fulfilled deliveries
    Given a contract paying 10000 credits on acceptance and 50000 on fulfillment
    And the contract requires delivery of 100 "IRON_ORE" to "X1-GZ7-A1" with 40 units already fulfilled
    And the cheapest market sells "IRON_ORE" at 200 credits per unit in system "X1-GZ7"
    And a ship with 60 cargo capacity
    And fuel cost is 1000 credits per trip
    When I evaluate the contract profitability
    Then the purchase cost should be 12000 credits
    And trips required should be 1
    And the net profit should be 47000 credits
    And the contract should be profitable

  Scenario: Multiple deliveries with same trade symbol
    Given a contract paying 30000 credits on acceptance and 70000 on fulfillment
    And the contract requires delivery of 50 "IRON_ORE" to "X1-GZ7-A1"
    And the contract requires delivery of 75 "IRON_ORE" to "X1-GZ7-B2"
    And the cheapest market sells "IRON_ORE" at 200 credits per unit in system "X1-GZ7"
    And a ship with 100 cargo capacity
    And fuel cost is 1500 credits per trip
    When I evaluate the contract profitability
    Then the purchase cost should be 25000 credits
    And trips required should be 2
    And the net profit should be 72000 credits
    And the contract should be profitable
