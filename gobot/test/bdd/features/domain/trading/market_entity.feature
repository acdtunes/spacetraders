Feature: Market Entity

  Scenario: Create valid market with trade goods
    Given a market at waypoint "X1-GZ7-A1"
    And the market sells "IRON_ORE" at 50 credits with trade volume 100
    And the market sells "COPPER_ORE" at 75 credits with trade volume 200
    When I create the market
    Then the market should be created successfully
    And the waypoint should be "X1-GZ7-A1"
    And the market should have 2 trade goods

  Scenario: Create valid market with no trade goods
    Given a market at waypoint "X1-GZ7-A1"
    When I create the market with no trade goods
    Then the market should be created successfully
    And the market should have 0 trade goods

  Scenario: Cannot create market with empty waypoint symbol
    Given a market at waypoint ""
    When I try to create the market
    Then I should get an error "waypoint symbol cannot be empty"

  Scenario: Get existing trade good
    Given a market with "IRON_ORE" at 50 credits
    When I get trade good "IRON_ORE"
    Then I should find the trade good
    And the sell price should be 50 credits

  Scenario: Get non-existent trade good
    Given a market with "IRON_ORE" at 50 credits
    When I get trade good "COPPER_ORE"
    Then I should not find the trade good

  Scenario: Get transaction limit for existing good
    Given a market with "IRON_ORE" with trade volume 150
    When I get transaction limit for "IRON_ORE"
    Then the transaction limit should be 150

  Scenario: Get transaction limit for non-existent good returns unlimited
    Given a market with "IRON_ORE" with trade volume 150
    When I get transaction limit for "COPPER_ORE"
    Then the transaction limit should be 999999

  Scenario: Check market has good (true)
    Given a market with "IRON_ORE" at 50 credits
    When I check if market has "IRON_ORE"
    Then the market should have the good

  Scenario: Check market has good (false)
    Given a market with "IRON_ORE" at 50 credits
    When I check if market has "COPPER_ORE"
    Then the market should not have the good
