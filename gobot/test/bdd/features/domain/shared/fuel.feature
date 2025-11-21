Feature: Fuel Value Object
  As a SpaceTraders navigation system
  I want to manage fuel as an immutable value object
  So that fuel state remains consistent and predictable

  # Fuel.Percentage() - Calculate fuel as percentage of capacity
  Scenario: Calculate fuel percentage with full tank
    Given a fuel value object with 100 current and 100 capacity
    When I get the fuel percentage
    Then the fuel percentage should be 100.0

  Scenario: Calculate fuel percentage with half tank
    Given a fuel value object with 50 current and 100 capacity
    When I get the fuel percentage
    Then the fuel percentage should be 50.0

  Scenario: Calculate fuel percentage with quarter tank
    Given a fuel value object with 25 current and 100 capacity
    When I get the fuel percentage
    Then the fuel percentage should be 25.0

  Scenario: Calculate fuel percentage with empty tank
    Given a fuel value object with 0 current and 100 capacity
    When I get the fuel percentage
    Then the fuel percentage should be 0.0

  Scenario: Calculate fuel percentage with zero capacity returns zero
    Given a fuel value object with 0 current and 0 capacity
    When I get the fuel percentage
    Then the fuel percentage should be 0.0

  # Fuel.IsFull() - Check if fuel is at capacity
  Scenario: IsFull returns true when fuel is at capacity
    Given a fuel value object with 100 current and 100 capacity
    When I check if the fuel is full
    Then fuel should be full

  Scenario: IsFull returns false when fuel is not at capacity
    Given a fuel value object with 99 current and 100 capacity
    When I check if the fuel is full
    Then fuel should not be full

  Scenario: IsFull returns false when fuel is empty
    Given a fuel value object with 0 current and 100 capacity
    When I check if the fuel is full
    Then fuel should not be full

  Scenario: IsFull returns true for zero capacity tank
    Given a fuel value object with 0 current and 0 capacity
    When I check if the fuel is full
    Then fuel should be full

  # Fuel.String() - String representation
  Scenario: String representation shows current and capacity
    Given a fuel value object with 50 current and 100 capacity
    When I get the fuel string representation
    Then the string should be "Fuel(50/100)"

  Scenario: String representation for full tank
    Given a fuel value object with 200 current and 200 capacity
    When I get the fuel string representation
    Then the string should be "Fuel(200/200)"

  Scenario: String representation for empty tank
    Given a fuel value object with 0 current and 150 capacity
    When I get the fuel string representation
    Then the string should be "Fuel(0/150)"

  # Fuel.CanTravel() edge cases
  Scenario: CanTravel returns false with zero current fuel
    Given a fuel value object with 0 current and 100 capacity
    When I check if fuel can travel 10 units with safety margin 0.1
    Then travel should not be possible

  Scenario: CanTravel returns true with exact fuel plus margin
    Given a fuel value object with 110 current and 200 capacity
    When I check if fuel can travel 100 units with safety margin 0.1
    Then travel should be possible

  Scenario: CanTravel returns false when below safety margin
    Given a fuel value object with 109 current and 200 capacity
    When I check if fuel can travel 100 units with safety margin 0.1
    Then travel should not be possible

  # Existing NewFuel validation edge cases
  Scenario: NewFuel accepts zero current and zero capacity
    When I create fuel with 0 current and 0 capacity
    Then fuel creation should succeed

  Scenario: NewFuel rejects negative current
    When I create fuel with -1 current and 100 capacity
    Then fuel creation should fail with "current fuel cannot be negative"

  Scenario: NewFuel rejects negative capacity
    When I create fuel with 50 current and -1 capacity
    Then fuel creation should fail with "fuel capacity cannot be negative"

  Scenario: NewFuel rejects current exceeding capacity
    When I create fuel with 101 current and 100 capacity
    Then fuel creation should fail with "current fuel cannot exceed capacity"
