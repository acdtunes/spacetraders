Feature: Cargo Value Object
  As a cargo management system
  I want to work with immutable cargo value objects
  So that cargo operations are safe and predictable

  # ============================================================================
  # CargoItem Initialization Tests
  # ============================================================================

  Scenario: Create cargo item with valid data
    When I create a cargo item with symbol "IRON_ORE", name "Iron Ore", units 10
    Then the cargo item should have symbol "IRON_ORE"
    And the cargo item should have name "Iron Ore"
    And the cargo item should have units 10

  Scenario: Create cargo item with empty symbol fails
    When I attempt to create a cargo item with empty symbol
    Then cargo item creation should fail with error "cargo symbol cannot be empty"

  Scenario: Create cargo item with negative units fails
    When I attempt to create a cargo item with units -5
    Then cargo item creation should fail with error "cargo units cannot be negative"

  Scenario: Create cargo item with zero units succeeds
    When I create a cargo item with symbol "IRON_ORE", name "Iron Ore", units 0
    Then the cargo item should have units 0

  # ============================================================================
  # Cargo Initialization Tests
  # ============================================================================

  Scenario: Create cargo with valid data
    Given a cargo item with symbol "IRON_ORE" and units 10
    When I create cargo with capacity 40, units 10, and inventory
    Then the cargo should have capacity 40
    And the cargo should have units 10
    And the cargo inventory should contain 1 items

  Scenario: Create empty cargo succeeds
    When I create cargo with capacity 40, units 0, and empty inventory
    Then the cargo should have capacity 40
    And the cargo should have units 0
    And the cargo should be empty

  Scenario: Create cargo with negative units fails
    When I attempt to create cargo with units -5
    Then cargo creation should fail with error "cargo units cannot be negative"

  Scenario: Create cargo with negative capacity fails
    When I attempt to create cargo with capacity -10
    Then cargo creation should fail with error "cargo capacity cannot be negative"

  Scenario: Create cargo with units exceeding capacity fails
    When I attempt to create cargo with capacity 40 and units 50
    Then cargo creation should fail with error "cargo units 50 exceed capacity 40"

  Scenario: Create cargo with mismatched inventory sum fails
    Given a cargo item with symbol "IRON_ORE" and units 10
    When I attempt to create cargo with capacity 40, units 20, and inventory
    Then cargo creation should fail with error "inventory sum 10 != total units 20"

  # ============================================================================
  # Cargo Item Query Tests
  # ============================================================================

  Scenario: Has item returns true when item exists
    Given cargo with items:
      | symbol   | units |
      | IRON_ORE | 10    |
      | COPPER   | 5     |
    When I check if cargo has item "IRON_ORE" with min units 5
    Then the result should be true

  Scenario: Has item returns false when insufficient units
    Given cargo with items:
      | symbol   | units |
      | IRON_ORE | 10    |
    When I check if cargo has item "IRON_ORE" with min units 15
    Then the result should be false

  Scenario: Has item returns false when item not present
    Given cargo with items:
      | symbol   | units |
      | IRON_ORE | 10    |
    When I check if cargo has item "COPPER" with min units 1
    Then the result should be false

  Scenario: Get item units returns correct amount
    Given cargo with items:
      | symbol   | units |
      | IRON_ORE | 10    |
      | COPPER   | 5     |
    When I get units of item "IRON_ORE"
    Then the item units should be 10

  Scenario: Get item units returns zero for non-existent item
    Given cargo with items:
      | symbol   | units |
      | IRON_ORE | 10    |
    When I get units of item "GOLD"
    Then the item units should be 0

  Scenario: Has items other than specific symbol
    Given cargo with items:
      | symbol   | units |
      | IRON_ORE | 10    |
      | COPPER   | 5     |
    When I check if cargo has items other than "IRON_ORE"
    Then the result should be true

  Scenario: Has no items other than specific symbol
    Given cargo with items:
      | symbol   | units |
      | IRON_ORE | 10    |
    When I check if cargo has items other than "IRON_ORE"
    Then the result should be false

  # ============================================================================
  # Cargo Capacity Tests
  # ============================================================================

  Scenario: Calculate available capacity with empty cargo
    Given cargo with capacity 40 and units 0
    When I calculate available capacity
    Then the available capacity should be 40

  Scenario: Calculate available capacity with partial cargo
    Given cargo with capacity 40 and units 25
    When I calculate available capacity
    Then the available capacity should be 15

  Scenario: Calculate available capacity with full cargo
    Given cargo with capacity 40 and units 40
    When I calculate available capacity
    Then the available capacity should be 0

  # ============================================================================
  # Cargo Status Tests
  # ============================================================================

  Scenario: Is empty returns true when no cargo
    Given cargo with capacity 40 and units 0
    When I check if cargo is empty
    Then the result should be true

  Scenario: Is empty returns false when cargo present
    Given cargo with capacity 40 and units 1
    When I check if cargo is empty
    Then the result should be false

  Scenario: Is full returns true when at capacity
    Given cargo with capacity 40 and units 40
    When I check if cargo is full
    Then the result should be true

  Scenario: Is full returns false when below capacity
    Given cargo with capacity 40 and units 39
    When I check if cargo is full
    Then the result should be false

  Scenario: Is full returns false when empty
    Given cargo with capacity 40 and units 0
    When I check if cargo is full
    Then the result should be false

  # ============================================================================
  # Get Other Items Tests (NEW)
  # ============================================================================

  Scenario: Get other items from cargo with multiple types
    Given cargo with items:
      | symbol        | units |
      | IRON_ORE      | 50    |
      | COPPER_ORE    | 25    |
      | ALUMINUM_ORE  | 10    |
    When I get other items excluding "IRON_ORE"
    Then I should have 2 other cargo items
    And other items should contain "COPPER_ORE" with 25 units
    And other items should contain "ALUMINUM_ORE" with 10 units

  Scenario: Get other items returns empty when only specified item exists
    Given cargo with items:
      | symbol   | units |
      | IRON_ORE | 50    |
    When I get other items excluding "IRON_ORE"
    Then I should have 0 other cargo items

  Scenario: Get other items from empty cargo
    Given cargo with capacity 100 and units 0
    When I get other items excluding "IRON_ORE"
    Then I should have 0 other cargo items
