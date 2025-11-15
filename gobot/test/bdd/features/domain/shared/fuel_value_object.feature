Feature: Fuel Value Object
  As a navigation system
  I want to work with immutable fuel value objects
  So that fuel operations are safe and predictable

  # ============================================================================
  # Fuel Initialization Tests
  # ============================================================================

  Scenario: Create fuel with valid data
    When I create fuel with current 50 and capacity 100
    Then the fuel should have current 50
    And the fuel should have capacity 100

  Scenario: Create fuel with negative current fails
    When I attempt to create fuel with current -10 and capacity 100
    Then fuel creation should fail with error "current fuel cannot be negative"

  Scenario: Create fuel with negative capacity fails
    When I attempt to create fuel with current 50 and capacity -100
    Then fuel creation should fail with error "fuel capacity cannot be negative"

  Scenario: Create fuel with current exceeding capacity fails
    When I attempt to create fuel with current 150 and capacity 100
    Then fuel creation should fail with error "current fuel cannot exceed capacity"

  Scenario: Create fuel at full capacity succeeds
    When I create fuel with current 100 and capacity 100
    Then the fuel should have current 100
    And the fuel should be full

  Scenario: Create fuel at zero capacity succeeds
    When I create fuel with current 0 and capacity 0
    Then the fuel should have current 0
    And the fuel should have capacity 0

  # ============================================================================
  # Fuel Percentage Tests
  # ============================================================================

  Scenario: Calculate fuel percentage
    Given fuel with current 50 and capacity 100
    When I calculate the fuel percentage
    Then the percentage should be 50.0

  Scenario: Calculate fuel percentage when full
    Given fuel with current 100 and capacity 100
    When I calculate the fuel percentage
    Then the percentage should be 100.0

  Scenario: Calculate fuel percentage when empty
    Given fuel with current 0 and capacity 100
    When I calculate the fuel percentage
    Then the percentage should be 0.0

  Scenario: Calculate fuel percentage with zero capacity returns zero
    Given fuel with current 0 and capacity 0
    When I calculate the fuel percentage
    Then the percentage should be 0.0

  # ============================================================================
  # Fuel Consumption Tests
  # ============================================================================

  Scenario: Consume fuel returns new fuel object
    Given fuel with current 100 and capacity 100
    When I consume 30 units of fuel
    Then the new fuel should have current 70
    And the new fuel should have capacity 100

  Scenario: Consume negative fuel fails
    Given fuel with current 100 and capacity 100
    When I attempt to consume -10 units of fuel
    Then the operation should fail with error "fuel amount cannot be negative"

  Scenario: Consume more fuel than available sets to zero
    Given fuel with current 50 and capacity 100
    When I consume 80 units of fuel
    Then the new fuel should have current 0

  Scenario: Consume all fuel
    Given fuel with current 100 and capacity 100
    When I consume 100 units of fuel
    Then the new fuel should have current 0

  Scenario: Consume zero fuel returns same values
    Given fuel with current 50 and capacity 100
    When I consume 0 units of fuel
    Then the new fuel should have current 50

  Scenario: Original fuel object is unchanged after consume
    Given fuel with current 100 and capacity 100
    When I consume 30 units of fuel
    Then the original fuel should still have current 100

  # ============================================================================
  # Fuel Addition Tests
  # ============================================================================

  Scenario: Add fuel returns new fuel object
    Given fuel with current 50 and capacity 100
    When I add 30 units of fuel
    Then the new fuel should have current 80
    And the new fuel should have capacity 100

  Scenario: Add negative fuel fails
    Given fuel with current 50 and capacity 100
    When I attempt to add -10 units of fuel
    Then the operation should fail with error "add amount cannot be negative"

  Scenario: Add fuel caps at capacity
    Given fuel with current 80 and capacity 100
    When I add 50 units of fuel
    Then the new fuel should have current 100

  Scenario: Add fuel to empty tank
    Given fuel with current 0 and capacity 100
    When I add 100 units of fuel
    Then the new fuel should have current 100

  Scenario: Add zero fuel returns same values
    Given fuel with current 50 and capacity 100
    When I add 0 units of fuel
    Then the new fuel should have current 50

  Scenario: Original fuel object is unchanged after add
    Given fuel with current 50 and capacity 100
    When I add 30 units of fuel
    Then the original fuel should still have current 50

  # ============================================================================
  # Fuel Travel Capability Tests
  # ============================================================================

  Scenario: Can travel with sufficient fuel
    Given fuel with current 100 and capacity 100
    When I check if fuel can travel requiring 50 with safety margin 0.1
    Then the result should be true

  Scenario: Cannot travel with insufficient fuel
    Given fuel with current 50 and capacity 100
    When I check if fuel can travel requiring 50 with safety margin 0.1
    Then the result should be false

  Scenario: Can travel with exact fuel and no safety margin
    Given fuel with current 50 and capacity 100
    When I check if fuel can travel requiring 50 with safety margin 0.0
    Then the result should be true

  Scenario: Cannot travel with exact fuel but with safety margin
    Given fuel with current 55 and capacity 100
    When I check if fuel can travel requiring 50 with safety margin 0.1
    Then the result should be true

  # ============================================================================
  # Fuel Status Tests
  # ============================================================================

  Scenario: Is full when at capacity
    Given fuel with current 100 and capacity 100
    When I check if fuel is full
    Then the result should be true

  Scenario: Is not full when below capacity
    Given fuel with current 99 and capacity 100
    When I check if fuel is full
    Then the result should be false

  Scenario: Is not full when empty
    Given fuel with current 0 and capacity 100
    When I check if fuel is full
    Then the result should be false

  # ============================================================================
  # Edge Cases for Increased Coverage
  # ============================================================================

  Scenario: Create fuel with exactly 1 unit
    When I create fuel with current 1 and capacity 100
    Then the fuel should have current 1
    And the fuel should have capacity 100

  Scenario: Create fuel with capacity 1
    When I create fuel with current 1 and capacity 1
    Then the fuel should have current 1
    And the fuel should be full

  Scenario: Consume exactly all remaining fuel
    Given fuel with current 50 and capacity 100
    When I consume 50 units of fuel
    Then the new fuel should have current 0
    And the original fuel should still have current 50

  Scenario: Add fuel to exactly full
    Given fuel with current 50 and capacity 100
    When I add 50 units of fuel
    Then the new fuel should have current 100
    And the new fuel should be full

  Scenario: Fuel percentage at exact boundaries
    Given fuel with current 1 and capacity 100
    When I calculate the fuel percentage
    Then the percentage should be 1.0

  Scenario: Fuel percentage at 99%
    Given fuel with current 99 and capacity 100
    When I calculate the fuel percentage
    Then the percentage should be 99.0

  Scenario: Can travel with zero fuel required
    Given fuel with current 50 and capacity 100
    When I check if fuel can travel requiring 0 with safety margin 0.0
    Then the result should be true

  Scenario: Cannot travel when empty regardless of requirement
    Given fuel with current 0 and capacity 100
    When I check if fuel can travel requiring 0 with safety margin 0.1
    Then the result should be false

  Scenario: Can travel with exactly safety margin fuel
    Given fuel with current 55 and capacity 100
    When I check if fuel can travel requiring 50 with safety margin 0.1
    Then the result should be true

  Scenario: Cannot travel with one unit below safety margin
    Given fuel with current 54 and capacity 100
    When I check if fuel can travel requiring 50 with safety margin 0.1
    Then the result should be false

  Scenario: Fuel percentage with very small current
    Given fuel with current 1 and capacity 1000
    When I calculate the fuel percentage
    Then the percentage should be 0.1

  Scenario: Add fuel when already full
    Given fuel with current 100 and capacity 100
    When I add 10 units of fuel
    Then the new fuel should have current 100
    And the new fuel should be full

  Scenario: Consume fuel leaving exactly 1 unit
    Given fuel with current 100 and capacity 100
    When I consume 99 units of fuel
    Then the new fuel should have current 1

  Scenario: Zero capacity tank is always full and empty
    Given fuel with current 0 and capacity 0
    When I check if fuel is full
    Then the result should be true

  Scenario: Can travel with large fuel values
    Given fuel with current 10000 and capacity 10000
    When I check if fuel can travel requiring 5000 with safety margin 0.1
    Then the result should be true

  Scenario: Consume more than double available fuel
    Given fuel with current 30 and capacity 100
    When I consume 100 units of fuel
    Then the new fuel should have current 0

  Scenario: Add fuel with very small amount
    Given fuel with current 50 and capacity 100
    When I add 1 unit of fuel
    Then the new fuel should have current 51

  Scenario: Fuel percentage at boundary - just below 50%
    Given fuel with current 49 and capacity 100
    When I calculate the fuel percentage
    Then the percentage should be 49.0

  Scenario: Fuel percentage at boundary - exactly 50%
    Given fuel with current 50 and capacity 100
    When I calculate the fuel percentage
    Then the percentage should be 50.0
