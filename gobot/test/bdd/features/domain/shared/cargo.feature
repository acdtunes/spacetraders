Feature: Cargo Value Object
  As a cargo management system
  I need to track and query cargo inventory
  So that I can manage ship cargo efficiently

  Scenario: Create cargo item with valid data
    When I create a cargo item with symbol "IRON_ORE", name "Iron Ore", description "Raw iron ore", and 50 units
    Then the cargo item creation should succeed
    And the cargo item should have symbol "IRON_ORE"
    And the cargo item should have 50 units

  Scenario: Reject cargo item with negative units
    When I attempt to create a cargo item with symbol "IRON_ORE" and -10 units
    Then the cargo item creation should fail with error "cargo units cannot be negative"

  Scenario: Reject cargo item with empty symbol
    When I attempt to create a cargo item with empty symbol and 50 units
    Then the cargo item creation should fail with error "cargo symbol cannot be empty"

  Scenario: Create cargo manifest with valid data
    Given a cargo item "IRON_ORE" with 50 units
    And a cargo item "COPPER_ORE" with 30 units
    When I create a cargo manifest with capacity 100 and the items
    Then the cargo creation should succeed
    And the cargo should have capacity 100
    And the cargo should have 80 units

  Scenario: Available capacity calculation
    Given a cargo item "IRON_ORE" with 50 units
    When I create a cargo manifest with capacity 100 and the items
    Then the cargo should have 50 units available capacity

  Scenario: Check if cargo is empty
    When I create an empty cargo manifest with capacity 100
    Then the cargo should be empty

  Scenario: Check if cargo is not empty
    Given a cargo item "IRON_ORE" with 50 units
    When I create a cargo manifest with capacity 100 and the items
    Then the cargo should not be empty

  Scenario: Check if cargo is full
    Given a cargo item "IRON_ORE" with 100 units
    When I create a cargo manifest with capacity 100 and the items
    Then the cargo should be full

  Scenario: Check if cargo is not full
    Given a cargo item "IRON_ORE" with 50 units
    When I create a cargo manifest with capacity 100 and the items
    Then the cargo should not be full

  Scenario: Check if cargo has specific item with sufficient units
    Given a cargo item "IRON_ORE" with 50 units
    And a cargo item "COPPER_ORE" with 30 units
    When I create a cargo manifest with capacity 100 and the items
    Then the cargo should have at least 40 units of "IRON_ORE"
    And the cargo should have at least 20 units of "COPPER_ORE"

  Scenario: Check if cargo has specific item with insufficient units
    Given a cargo item "IRON_ORE" with 50 units
    When I create a cargo manifest with capacity 100 and the items
    Then the cargo should not have at least 60 units of "IRON_ORE"

  Scenario: Check if cargo has item that doesn't exist
    Given a cargo item "IRON_ORE" with 50 units
    When I create a cargo manifest with capacity 100 and the items
    Then the cargo should not have at least 1 unit of "GOLD_ORE"

  Scenario: Get units of specific item in cargo
    Given a cargo item "IRON_ORE" with 50 units
    And a cargo item "COPPER_ORE" with 30 units
    When I create a cargo manifest with capacity 100 and the items
    Then the cargo should have exactly 50 units of "IRON_ORE"
    And the cargo should have exactly 30 units of "COPPER_ORE"
    And the cargo should have exactly 0 units of "GOLD_ORE"

  Scenario: Check if cargo has items other than specified symbol
    Given a cargo item "IRON_ORE" with 50 units
    And a cargo item "COPPER_ORE" with 30 units
    When I create a cargo manifest with capacity 100 and the items
    Then the cargo should have items other than "IRON_ORE"
    And the cargo should have items other than "COPPER_ORE"

  Scenario: Check cargo with only one item type
    Given a cargo item "IRON_ORE" with 50 units
    When I create a cargo manifest with capacity 100 and the items
    Then the cargo should not have items other than "IRON_ORE"

  Scenario: Get other items excluding specific symbol
    Given a cargo item "IRON_ORE" with 50 units
    And a cargo item "COPPER_ORE" with 30 units
    And a cargo item "GOLD_ORE" with 10 units
    When I create a cargo manifest with capacity 100 and the items
    And I get items other than "IRON_ORE"
    Then I should get 2 other items
    And the other items should include "COPPER_ORE"
    And the other items should include "GOLD_ORE"
    And the other items should not include "IRON_ORE"

  Scenario: Get other items returns empty when only one type exists
    Given a cargo item "IRON_ORE" with 50 units
    When I create a cargo manifest with capacity 100 and the items
    And I get items other than "IRON_ORE"
    Then I should get 0 other items

  Scenario: Cargo string representation
    Given a cargo item "IRON_ORE" with 50 units
    When I create a cargo manifest with capacity 100 and the items
    Then the cargo string should be "Cargo(50/100)"

  Scenario: Reject cargo with negative capacity
    When I attempt to create a cargo manifest with capacity -100 and no items
    Then the cargo creation should fail with error "cargo_capacity cannot be negative"

  Scenario: Reject cargo with negative units
    When I attempt to create a cargo manifest with capacity 100 and units -10
    Then the cargo creation should fail with error "cargo_units cannot be negative"

  Scenario: Reject cargo with units exceeding capacity
    Given a cargo item "IRON_ORE" with 150 units
    When I attempt to create a cargo manifest with capacity 100 and the items
    Then the cargo creation should fail with error "cargo_units cannot exceed cargo_capacity"

  Scenario: Reject cargo when inventory sum doesn't match units
    Given a cargo item "IRON_ORE" with 50 units
    When I attempt to create a cargo manifest with capacity 100, units 60, and the items
    Then the cargo creation should fail with error "inventory sum 50 != total units 60"
