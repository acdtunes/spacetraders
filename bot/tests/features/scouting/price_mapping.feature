Feature: Scout Market Price Mapping
  As a scout coordinator
  I want accurate mapping between markets and price data
  So that trade analysis uses correct market prices

  Background:
    Given a scout coordinator collecting market data
    And multiple markets with price information

  @xfail
  Scenario: Correct market-to-price mapping
    Given scout visits 5 markets on tour
    And each market has unique price data
    When storing market price data
    Then each price should be mapped to correct market
    And no price data should be assigned to wrong market

  @xfail
  Scenario: Price mapping bug fix
    Given scout collects prices from Market-A and Market-B
    And Market-A sells IRON_ORE at 100 credits
    And Market-B sells IRON_ORE at 200 credits
    When retrieving price data from database
    Then Market-A price should be 100 credits (not 200)
    And Market-B price should be 200 credits (not 100)
    And price mappings should not be swapped

  @xfail
  Scenario: Verify stationary scout price updates
    Given a stationary scout at Market-X
    And scout collects market data periodically
    When checking price history for Market-X
    Then all prices should be mapped to Market-X
    And no prices from other markets should appear
    And price timeline should be consistent
