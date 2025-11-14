Feature: TradeGood Value Object
  As a market scouting system
  I need to represent trade goods with prices and supply information
  So that I can track commodity data across markets

  Background:
    Given I have market trade good data

  Scenario: Create valid trade good with all fields
    When I create a trade good with symbol "IRON_ORE", supply "MODERATE", activity "STRONG", purchase price 50, sell price 100, and trade volume 500
    Then the trade good should have symbol "IRON_ORE"
    And the trade good should have supply "MODERATE"
    And the trade good should have activity "STRONG"
    And the trade good should have purchase price 50
    And the trade good should have sell price 100
    And the trade good should have trade volume 500

  Scenario: Create trade good with optional nil fields
    When I create a trade good with symbol "FUEL", no supply, no activity, purchase price 10, sell price 20, and trade volume 100
    Then the trade good should have symbol "FUEL"
    And the trade good should have nil supply
    And the trade good should have nil activity
    And the trade good should have purchase price 10
    And the trade good should have sell price 20
    And the trade good should have trade volume 100

  Scenario: Reject trade good with negative purchase price
    When I attempt to create a trade good with symbol "IRON", supply "HIGH", activity "WEAK", purchase price -10, sell price 50, and trade volume 100
    Then I should get an error "purchase price must be non-negative"

  Scenario: Reject trade good with negative sell price
    When I attempt to create a trade good with symbol "IRON", supply "HIGH", activity "WEAK", purchase price 10, sell price -50, and trade volume 100
    Then I should get an error "sell price must be non-negative"

  Scenario: Reject trade good with negative trade volume
    When I attempt to create a trade good with symbol "IRON", supply "HIGH", activity "WEAK", purchase price 10, sell price 50, and trade volume -100
    Then I should get an error "trade volume must be non-negative"

  Scenario: Reject trade good with empty symbol
    When I attempt to create a trade good with symbol "", supply "HIGH", activity "WEAK", purchase price 10, sell price 50, and trade volume 100
    Then I should get an error "symbol cannot be empty"

  Scenario: Accept valid supply values
    When I create a trade good with symbol "IRON", supply "SCARCE", activity "WEAK", purchase price 10, sell price 50, and trade volume 100
    Then the trade good should have supply "SCARCE"
    When I create a trade good with symbol "IRON", supply "LIMITED", activity "WEAK", purchase price 10, sell price 50, and trade volume 100
    Then the trade good should have supply "LIMITED"
    When I create a trade good with symbol "IRON", supply "MODERATE", activity "WEAK", purchase price 10, sell price 50, and trade volume 100
    Then the trade good should have supply "MODERATE"
    When I create a trade good with symbol "IRON", supply "HIGH", activity "WEAK", purchase price 10, sell price 50, and trade volume 100
    Then the trade good should have supply "HIGH"
    When I create a trade good with symbol "IRON", supply "ABUNDANT", activity "WEAK", purchase price 10, sell price 50, and trade volume 100
    Then the trade good should have supply "ABUNDANT"

  Scenario: Accept valid activity values
    When I create a trade good with symbol "IRON", supply "HIGH", activity "WEAK", purchase price 10, sell price 50, and trade volume 100
    Then the trade good should have activity "WEAK"
    When I create a trade good with symbol "IRON", supply "HIGH", activity "GROWING", purchase price 10, sell price 50, and trade volume 100
    Then the trade good should have activity "GROWING"
    When I create a trade good with symbol "IRON", supply "HIGH", activity "STRONG", purchase price 10, sell price 50, and trade volume 100
    Then the trade good should have activity "STRONG"
    When I create a trade good with symbol "IRON", supply "HIGH", activity "RESTRICTED", purchase price 10, sell price 50, and trade volume 100
    Then the trade good should have activity "RESTRICTED"

  Scenario: Reject invalid supply value
    When I attempt to create a trade good with symbol "IRON", supply "INVALID", activity "WEAK", purchase price 10, sell price 50, and trade volume 100
    Then I should get an error "invalid supply value"

  Scenario: Reject invalid activity value
    When I attempt to create a trade good with symbol "IRON", supply "HIGH", activity "INVALID", purchase price 10, sell price 50, and trade volume 100
    Then I should get an error "invalid activity value"
