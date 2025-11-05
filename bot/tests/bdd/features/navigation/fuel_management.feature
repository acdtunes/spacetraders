Feature: Fuel Management
  As a ship operator
  I want to manage fuel consumption and refueling
  So that I can complete journeys safely and efficiently

  Background:
    Given the fuel management system is initialized

  # Fuel Value Object Basics
  Scenario: Create fuel with valid values
    When I create a fuel object with 100 current and 500 capacity
    Then the fuel should be created successfully
    And the current fuel should be 100
    And the fuel capacity should be 500

  Scenario: Cannot create fuel with negative current
    When I attempt to create a fuel object with -10 current and 500 capacity
    Then fuel creation should fail with ValueError
    And the error message should mention "cannot be negative"

  Scenario: Cannot create fuel with negative capacity
    When I attempt to create a fuel object with 100 current and -500 capacity
    Then fuel creation should fail with ValueError
    And the error message should mention "cannot be negative"

  Scenario: Cannot create fuel exceeding capacity
    When I attempt to create a fuel object with 600 current and 500 capacity
    Then fuel creation should fail with ValueError
    And the error message should mention "cannot exceed capacity"

  # Fuel Percentage Calculations
  Scenario: Calculate fuel percentage
    Given a fuel object with 250 current and 1000 capacity
    When I calculate the fuel percentage
    Then the percentage should be 25.0

  Scenario: Calculate fuel percentage at full capacity
    Given a fuel object with 500 current and 500 capacity
    When I calculate the fuel percentage
    Then the percentage should be 100.0

  Scenario: Calculate fuel percentage when empty
    Given a fuel object with 0 current and 500 capacity
    When I calculate the fuel percentage
    Then the percentage should be 0.0

  Scenario: Calculate fuel percentage with zero capacity
    Given a fuel object with 0 current and 0 capacity
    When I calculate the fuel percentage
    Then the percentage should be 0.0

  # Fuel Consumption
  Scenario: Consume fuel in CRUISE mode
    Given a fuel object with 400 current and 500 capacity
    When I consume 200 fuel units
    Then the new fuel should have 200 current
    And the capacity should remain 500

  Scenario: Consume fuel in DRIFT mode
    Given a fuel object with 400 current and 500 capacity
    And a distance of 1000 units
    When I calculate DRIFT mode fuel consumption
    Then the fuel required should be 3 units

  Scenario: Consume fuel in BURN mode
    Given a fuel object with 400 current and 500 capacity
    And a distance of 200 units
    When I calculate BURN mode fuel consumption
    Then the fuel required should be 400 units

  Scenario: Consume more fuel than available
    Given a fuel object with 50 current and 500 capacity
    When I consume 100 fuel units
    Then the new fuel should have 0 current
    And the capacity should remain 500

  Scenario: Consume negative fuel fails
    Given a fuel object with 100 current and 500 capacity
    When I attempt to consume -50 fuel units
    Then the operation should fail with ValueError
    And the error message should mention "cannot be negative"

  # Adding Fuel (Refueling)
  Scenario: Refuel at marketplace
    Given a fuel object with 100 current and 500 capacity
    When I add 200 fuel units
    Then the new fuel should have 300 current
    And the capacity should remain 500

  Scenario: Refuel to full capacity
    Given a fuel object with 100 current and 500 capacity
    When I add 400 fuel units
    Then the new fuel should have 500 current
    And the fuel should be at full capacity

  Scenario: Overfill fuel stops at capacity
    Given a fuel object with 400 current and 500 capacity
    When I add 200 fuel units
    Then the new fuel should have 500 current
    And the fuel should be at full capacity

  Scenario: Add negative fuel fails
    Given a fuel object with 100 current and 500 capacity
    When I attempt to add -50 fuel units
    Then the operation should fail with ValueError
    And the error message should mention "cannot be negative"

  # Travel Feasibility
  Scenario: Can travel with sufficient fuel
    Given a fuel object with 400 current and 500 capacity
    When I check if I can travel requiring 300 fuel
    Then the travel should be feasible

  Scenario: Can travel with exact fuel needed
    Given a fuel object with 300 current and 500 capacity
    When I check if I can travel requiring 300 fuel with no safety margin
    Then the travel should be feasible

  Scenario: Cannot travel with insufficient fuel
    Given a fuel object with 200 current and 500 capacity
    When I check if I can travel requiring 300 fuel
    Then the travel should not be feasible

  Scenario: Can travel accounts for safety margin
    Given a fuel object with 100 current and 500 capacity
    And a safety margin of 10%
    When I check if I can travel requiring 100 fuel
    Then the travel should not be feasible
    And the required fuel with margin should be 110

  Scenario: Can travel with custom safety margin
    Given a fuel object with 200 current and 500 capacity
    And a safety margin of 20%
    When I check if I can travel requiring 150 fuel
    Then the travel should be feasible
    And the required fuel with margin should be 180

  # Refuel Decision Making - Absolute Threshold (4 units safety margin)
  Scenario: Should refuel when fuel below safety margin
    Given a fuel object with 3 current and 500 capacity
    And I am at a marketplace
    When I check if I should refuel with no next leg distance
    Then refueling should be recommended
    And the reason should be "fuel below safety margin of 4 units"

  Scenario: Should refuel when insufficient for next leg in BURN mode
    Given a fuel object with 50 current and 500 capacity
    And I am at a marketplace
    And a distance of 30 units
    When I check if I should refuel with next leg distance 30
    Then refueling should be recommended

  Scenario: Should not refuel when sufficient for next leg in BURN mode
    Given a fuel object with 100 current and 500 capacity
    And I am at a marketplace
    And a distance of 30 units
    When I check if I should refuel with next leg distance 30
    Then refueling should not be recommended

  Scenario: Should not refuel when not at marketplace
    Given a fuel object with 3 current and 500 capacity
    And I am not at a marketplace
    When I check if I should refuel with no next leg distance
    Then refueling should not be recommended

  Scenario: Should not refuel above safety margin with no next leg
    Given a fuel object with 10 current and 500 capacity
    And I am at a marketplace
    When I check if I should refuel with no next leg distance
    Then refueling should not be recommended

  # Refuel Stop Planning
  Scenario: Needs refuel stop for long journey
    Given a fuel object with 100 current and 500 capacity
    And a destination 500 units away
    And a refuel point 80 units away
    When I check if refuel stop is needed using CRUISE mode
    Then a refuel stop should be needed
    And I should be able to reach the refuel point

  Scenario: No refuel stop needed for short journey
    Given a fuel object with 400 current and 500 capacity
    And a destination 200 units away
    When I check if refuel stop is needed using CRUISE mode
    Then a refuel stop should not be needed

  Scenario: Cannot reach refuel point
    Given a fuel object with 50 current and 500 capacity
    And a destination 500 units away
    And a refuel point 200 units away
    When I check if refuel stop is needed using CRUISE mode
    Then I should not be able to reach the refuel point

  # Fuel Status Checks
  Scenario: Check if fuel is full
    Given a fuel object with 500 current and 500 capacity
    When I check if fuel is full
    Then the fuel should be at full capacity

  Scenario: Check if fuel is not full
    Given a fuel object with 499 current and 500 capacity
    When I check if fuel is full
    Then the fuel should not be at full capacity

  Scenario: Check if fuel is empty
    Given a fuel object with 0 current and 500 capacity
    When I check the fuel percentage
    Then the percentage should be 0.0
    And the travel feasibility for 1 unit should be false

  # Distance and Fuel Calculations
  Scenario: Calculate fuel for zero distance
    Given any flight mode
    And a distance of 0 units
    When I calculate fuel cost
    Then the fuel required should be 0

  Scenario: Calculate minimum fuel for very short distance
    Given CRUISE mode with 1.0 fuel rate
    And a distance of 0.5 units
    When I calculate fuel cost
    Then the fuel required should be at least 1
