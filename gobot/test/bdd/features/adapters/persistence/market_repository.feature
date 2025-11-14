Feature: Market Repository Persistence
  As a persistence layer
  I need to store and retrieve market data from the database
  So that the application can cache market information efficiently

  Background:
    Given a clean test database
    And a market repository instance

  Scenario: Upsert market data for the first time
    Given a player with ID 1
    And a waypoint "X1-TEST-A1"
    And market data with 3 trade goods:
      | Symbol     | Supply   | Activity | PurchasePrice | SellPrice | TradeVolume |
      | IRON_ORE   | MODERATE | STRONG   | 100           | 150       | 500         |
      | COPPER_ORE | HIGH     | GROWING  | 80            | 120       | 300         |
      | ALUMINUM   | LIMITED  | WEAK     | 200           | 250       | 100         |
    When I upsert the market data
    Then the market data should be persisted successfully
    And the database should contain 1 market record
    And the database should contain 3 trade goods records

  Scenario: Upsert market data updates existing record
    Given a player with ID 1
    And a waypoint "X1-TEST-A1"
    And existing market data with 2 trade goods:
      | Symbol     | Supply   | Activity | PurchasePrice | SellPrice | TradeVolume |
      | IRON_ORE   | MODERATE | STRONG   | 100           | 150       | 500         |
      | COPPER_ORE | HIGH     | GROWING  | 80            | 120       | 300         |
    And the market data is already persisted
    When I upsert new market data with 3 trade goods:
      | Symbol     | Supply   | Activity | PurchasePrice | SellPrice | TradeVolume |
      | IRON_ORE   | HIGH     | STRONG   | 110           | 160       | 600         |
      | GOLD       | SCARCE   | WEAK     | 500           | 700       | 50          |
      | SILVER     | LIMITED  | GROWING  | 300           | 400       | 150         |
    Then the market data should be persisted successfully
    And the database should contain 1 market record
    And the database should contain 3 trade goods records
    And the trade goods should be updated to the new values

  Scenario: Get market data by waypoint
    Given a player with ID 1
    And a waypoint "X1-TEST-A1"
    And existing market data with 2 trade goods:
      | Symbol     | Supply   | Activity | PurchasePrice | SellPrice | TradeVolume |
      | IRON_ORE   | MODERATE | STRONG   | 100           | 150       | 500         |
      | COPPER_ORE | HIGH     | GROWING  | 80            | 120       | 300         |
    And the market data is already persisted
    When I get market data for player 1 and waypoint "X1-TEST-A1"
    Then the market should be retrieved successfully
    And the market should have waypoint "X1-TEST-A1"
    And the retrieved market should have 2 trade goods
    And the market should contain trade good "IRON_ORE" with purchase price 100
    And the market should contain trade good "COPPER_ORE" with purchase price 80

  Scenario: Get non-existent market data returns nil
    Given a player with ID 1
    And a waypoint "X1-NONEXISTENT"
    When I get market data for player 1 and waypoint "X1-NONEXISTENT"
    Then the market should be nil
    And there should be no error

  Scenario: List markets in system with fresh data
    Given a player with ID 1
    And the following markets in system "X1-TEST":
      | Waypoint    | TradeGoodsCount | AgeMinutes |
      | X1-TEST-A1  | 2               | 5          |
      | X1-TEST-B2  | 3               | 10         |
      | X1-TEST-C3  | 1               | 15         |
    When I list markets for player 1 in system "X1-TEST" with max age 20 minutes
    Then 3 markets should be returned
    And the markets should include "X1-TEST-A1"
    And the markets should include "X1-TEST-B2"
    And the markets should include "X1-TEST-C3"

  Scenario: List markets in system filters stale data
    Given a player with ID 1
    And the following markets in system "X1-TEST":
      | Waypoint    | TradeGoodsCount | AgeMinutes |
      | X1-TEST-A1  | 2               | 5          |
      | X1-TEST-B2  | 3               | 25         |
      | X1-TEST-C3  | 1               | 40         |
    When I list markets for player 1 in system "X1-TEST" with max age 20 minutes
    Then 1 markets should be returned
    And the markets should include "X1-TEST-A1"
    And the markets should not include "X1-TEST-B2"
    And the markets should not include "X1-TEST-C3"

  Scenario: List markets in empty system returns empty list
    Given a player with ID 1
    When I list markets for player 1 in system "X1-EMPTY" with max age 20 minutes
    Then 0 markets should be returned

  Scenario: Upsert with empty trade goods list
    Given a player with ID 1
    And a waypoint "X1-TEST-A1"
    And market data with 0 trade goods
    When I upsert the market data
    Then the market data should be persisted successfully
    And the database should contain 1 market record
    And the database should contain 0 trade goods records

  Scenario: Different players can have separate market data for same waypoint
    Given a player with ID 1
    And a player with ID 2
    And a waypoint "X1-TEST-A1"
    And market data for player 1 with 2 trade goods
    And market data for player 2 with 3 trade goods
    When I upsert market data for both players
    Then player 1 should have 2 trade goods for waypoint "X1-TEST-A1"
    And player 2 should have 3 trade goods for waypoint "X1-TEST-A1"
