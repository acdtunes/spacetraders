Feature: Ship Converter Cargo Inventory Extraction
  The ship converter should extract full cargo inventory from API responses
  to prevent UNKNOWN placeholder cargo items from being created.

  Background:
    Given a player with ID 1

  Scenario: Ship converter extracts cargo inventory from API response
    Given an API response with ship data containing cargo inventory
      | symbol       | name          | units |
      | IRON_ORE     | Iron Ore      | 10    |
      | COPPER_ORE   | Copper Ore    | 15    |
    When I convert the API response to a Ship entity
    Then the ship should have cargo with 2 items
    And the cargo should contain "IRON_ORE" with 10 units
    And the cargo should contain "COPPER_ORE" with 15 units
    And the cargo should NOT contain "UNKNOWN" items

  Scenario: Ship converter handles empty cargo inventory
    Given an API response with ship data containing empty cargo
    When I convert the API response to a Ship entity
    Then the ship should have cargo with 0 items
    And the cargo units should be 0

  Scenario: Ship converter handles cargo with no inventory field
    Given an API response with ship data containing cargo units but no inventory array
    When I convert the API response to a Ship entity
    Then the ship should have cargo with 0 items
    And the cargo should NOT contain "UNKNOWN" items
