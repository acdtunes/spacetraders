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
