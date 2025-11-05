Feature: Market Domain Value Objects
  Market scouting domain entities for trade good data

  Scenario: Create valid TradeGood
    Given a trade good with symbol "IRON_ORE"
    And supply level "MODERATE"
    And activity level "STRONG"
    And purchase price 50
    And sell price 100
    And trade volume 1000
    When I create a TradeGood
    Then the trade good should be valid
    And the trade good should be immutable

  Scenario: TradeGood with minimal data
    Given a trade good with symbol "FUEL"
    And no supply level
    And no activity level
    And purchase price 0
    And sell price 20
    And trade volume 0
    When I create a TradeGood
    Then the trade good should be valid

  Scenario: Create Market snapshot
    Given a waypoint "X1-GZ7-A1"
    And trade goods:
      | symbol    | supply   | activity | purchase_price | sell_price | trade_volume |
      | IRON_ORE  | MODERATE | STRONG   | 50             | 100        | 1000         |
      | FUEL      | HIGH     | WEAK     | 10             | 20         | 5000         |
    And last updated timestamp "2025-01-15T10:00:00Z"
    When I create a Market
    Then the market should be valid
    And the market should be immutable
    And the market should have 2 trade goods

  Scenario: Create TourResult
    Given markets visited count 5
    And goods updated count 47
    And duration 120.5 seconds
    When I create a TourResult
    Then the tour result should be valid
    And the tour result should be immutable

  Scenario: Create PollResult
    Given goods updated count 12
    And a waypoint "X1-GZ7-B2"
    When I create a PollResult
    Then the poll result should be valid
    And the poll result should be immutable
