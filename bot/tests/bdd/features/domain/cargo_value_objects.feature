Feature: Cargo Value Objects

  Scenario: CargoItem creation with valid data
    When I create a cargo item with symbol "IRON_ORE", name "Iron Ore", description "Raw iron ore", and 10 units
    Then the cargo item should have symbol "IRON_ORE"
    And the cargo item should have 10 units

  Scenario: CargoItem validation - negative units
    When I try to create a cargo item with -5 units
    Then a ValueError should be raised with message "Cargo units cannot be negative"

  Scenario: CargoItem validation - empty symbol
    When I try to create a cargo item with empty symbol
    Then a ValueError should be raised with message "Cargo symbol cannot be empty"

  Scenario: Cargo creation with valid inventory
    Given a cargo item "IRON_ORE" with 10 units
    And a cargo item "COPPER" with 5 units
    When I create cargo with capacity 40 and the items
    Then the cargo should have capacity 40
    And the cargo should have 15 total units
    And the cargo should have 2 items in inventory

  Scenario: Cargo validation - units exceed capacity
    Given a cargo item "IRON_ORE" with 50 units
    When I try to create cargo with capacity 40 and the item
    Then a ValueError should be raised with message "Cargo units 50 exceed capacity 40"

  Scenario: Cargo validation - inventory sum mismatch
    Given a cargo item "IRON_ORE" with 10 units
    When I try to create cargo with total units 15 but inventory sums to 10
    Then a ValueError should be raised with message "Inventory sum 10 != total units 15"

  Scenario: Cargo has_item check - item exists with sufficient units
    Given a cargo with IRON_ORE 10 units and capacity 40
    When I check if cargo has "IRON_ORE" with at least 5 units
    Then the result should be true

  Scenario: Cargo has_item check - item exists but insufficient units
    Given a cargo with IRON_ORE 10 units and capacity 40
    When I check if cargo has "IRON_ORE" with at least 15 units
    Then the result should be false

  Scenario: Cargo has_item check - item does not exist
    Given a cargo with IRON_ORE 10 units and capacity 40
    When I check if cargo has "COPPER" with at least 1 unit
    Then the result should be false

  Scenario: Cargo get_item_units - item exists
    Given a cargo with IRON_ORE 10 units and COPPER 5 units and capacity 40
    When I get units for "COPPER"
    Then the result should be 5

  Scenario: Cargo get_item_units - item does not exist
    Given a cargo with IRON_ORE 10 units and capacity 40
    When I get units for "ALUMINUM"
    Then the result should be 0

  Scenario: Cargo has_items_other_than - has other items
    Given a cargo with IRON_ORE 10 units and COPPER 5 units and capacity 40
    When I check if cargo has items other than "IRON_ORE"
    Then the result should be true

  Scenario: Cargo has_items_other_than - only has specified item
    Given a cargo with IRON_ORE 10 units and capacity 40
    When I check if cargo has items other than "IRON_ORE"
    Then the result should be false

  Scenario: Cargo has_items_other_than - empty cargo
    Given an empty cargo with capacity 40
    When I check if cargo has items other than "IRON_ORE"
    Then the result should be false

  Scenario: Cargo available_capacity calculation
    Given a cargo with capacity 40 and 15 units used
    When I check available capacity
    Then the result should be 25

  Scenario: Cargo is_empty check - empty cargo
    Given an empty cargo with capacity 40
    When I check if cargo is empty
    Then the result should be true

  Scenario: Cargo is_empty check - cargo has items
    Given a cargo with IRON_ORE 10 units and capacity 40
    When I check if cargo is empty
    Then the result should be false
