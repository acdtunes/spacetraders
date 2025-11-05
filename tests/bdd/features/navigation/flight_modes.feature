Feature: Flight Mode Selection and Calculations
  As a ship operator
  I want to select optimal flight modes
  So that I can balance speed and fuel efficiency

  Background:
    Given the flight mode system is initialized
    And a ship with engine speed 30

  # Flight Mode Characteristics
  Scenario: CRUISE mode characteristics
    Given CRUISE flight mode
    Then the mode name should be "CRUISE"
    And the time multiplier should be 31
    And the fuel rate should be 1.0

  Scenario: DRIFT mode characteristics
    Given DRIFT flight mode
    Then the mode name should be "DRIFT"
    And the time multiplier should be 26
    And the fuel rate should be 0.003

  Scenario: BURN mode characteristics
    Given BURN flight mode
    Then the mode name should be "BURN"
    And the time multiplier should be 15
    And the fuel rate should be 2.0

  Scenario: STEALTH mode characteristics
    Given STEALTH flight mode
    Then the mode name should be "STEALTH"
    And the time multiplier should be 50
    And the fuel rate should be 1.0

  # Fuel Cost Calculations per Mode
  Scenario: Calculate fuel cost for CRUISE mode
    Given CRUISE flight mode
    And a distance of 200 units
    When I calculate the fuel cost
    Then the fuel required should be 200 units

  Scenario: Calculate fuel cost for DRIFT mode
    Given DRIFT flight mode
    And a distance of 1000 units
    When I calculate the fuel cost
    Then the fuel required should be 3 units

  Scenario: Calculate fuel cost for BURN mode
    Given BURN flight mode
    And a distance of 200 units
    When I calculate the fuel cost
    Then the fuel required should be 400 units

  Scenario: Calculate fuel cost for STEALTH mode
    Given STEALTH flight mode
    And a distance of 150 units
    When I calculate the fuel cost
    Then the fuel required should be 150 units

  Scenario: Fuel cost rounds up for partial units
    Given CRUISE flight mode
    And a distance of 100.7 units
    When I calculate the fuel cost
    Then the fuel required should be 101 units

  Scenario: Fuel cost for zero distance
    Given any flight mode
    And a distance of 0 units
    When I calculate the fuel cost
    Then the fuel required should be 0 units

  Scenario: Minimum fuel cost is 1 for any distance
    Given DRIFT flight mode with very low fuel rate
    And a distance of 0.1 units
    When I calculate the fuel cost
    Then the fuel required should be at least 1 unit

  # Travel Time Calculations per Mode
  Scenario: Calculate travel time for CRUISE mode
    Given CRUISE flight mode
    And a distance of 300 units
    And engine speed 30
    When I calculate the travel time
    Then the time should be 310 seconds

  Scenario: Calculate travel time for DRIFT mode
    Given DRIFT flight mode
    And a distance of 300 units
    And engine speed 30
    When I calculate the travel time
    Then the time should be 260 seconds

  Scenario: Calculate travel time for BURN mode
    Given BURN flight mode
    And a distance of 300 units
    And engine speed 30
    When I calculate the travel time
    Then the time should be 150 seconds

  Scenario: Calculate travel time for STEALTH mode
    Given STEALTH flight mode
    And a distance of 300 units
    And engine speed 30
    When I calculate the travel time
    Then the time should be 500 seconds

  Scenario: Travel time for zero distance
    Given any flight mode
    And a distance of 0 units
    When I calculate the travel time
    Then the time should be 0 seconds

  Scenario: Minimum travel time is 1 second
    Given BURN flight mode
    And a distance of 0.1 units
    And engine speed 100
    When I calculate the travel time
    Then the time should be at least 1 second

  Scenario: Travel time with fast engine
    Given CRUISE flight mode
    And a distance of 200 units
    And engine speed 100
    When I calculate the travel time
    Then the time should be 62 seconds

  Scenario: Travel time with slow engine
    Given CRUISE flight mode
    And a distance of 200 units
    And engine speed 10
    When I calculate the travel time
    Then the time should be 620 seconds

  # Optimal Flight Mode Selection - Speed Priority with Absolute Safety Margin
  # Business Rule: ALWAYS prioritize speed (BURN > CRUISE > DRIFT)
  # Use absolute safety margin of 4 fuel units

  Scenario: Select BURN with high fuel and distance
    Given a ship with 100 current fuel and 100 capacity
    And a distance of 30 units requiring 30 fuel at CRUISE rate
    When I select optimal flight mode for distance
    Then BURN mode should be selected
    And fuel remaining should exceed safety margin of 4 units

  Scenario: Select CRUISE when insufficient fuel for BURN
    Given a ship with 43 current fuel and 100 capacity
    And a distance of 20 units requiring 20 fuel at CRUISE rate
    When I select optimal flight mode for distance
    Then CRUISE mode should be selected
    And fuel remaining should exceed safety margin of 4 units

  Scenario: Select DRIFT when only enough fuel for DRIFT
    Given a ship with 8 current fuel and 100 capacity
    And a distance of 5 units requiring 5 fuel at CRUISE rate
    When I select optimal flight mode for distance
    Then DRIFT mode should be selected

  Scenario: Select BURN with full fuel
    Given a ship with 100 current fuel and 100 capacity
    And a distance of 20 units requiring 20 fuel at CRUISE rate
    When I select optimal flight mode for distance
    Then BURN mode should be selected

  Scenario: Select DRIFT with critically low fuel
    Given a ship with 4 current fuel and 100 capacity
    And a distance of 1 units requiring 1 fuel at CRUISE rate
    When I select optimal flight mode for distance
    Then DRIFT mode should be selected

  Scenario: Safety margin prevents using faster mode
    Given a ship with 43 current fuel and 100 capacity
    And a distance of 20 units requiring 40 fuel at BURN rate
    When I select optimal flight mode for distance
    Then CRUISE mode should be selected
    And BURN mode should be skipped due to safety margin

  # Mode Selection with Distance - Speed First Approach
  Scenario: Select BURN for distance with abundant fuel
    Given a ship with 400 current fuel and 500 capacity
    And a distance of 100 units
    When I select mode for distance
    Then BURN mode should be selected
    And the fuel should be sufficient

  Scenario: Select CRUISE when BURN would violate safety margin
    Given a ship with 203 current fuel and 500 capacity
    And a distance of 100 units
    When I select mode for distance
    Then CRUISE mode should be selected
    And the fuel should be sufficient

  Scenario: Select DRIFT when only DRIFT maintains safety margin
    Given a ship with 103 current fuel and 500 capacity
    And a distance of 100 units
    When I select mode for distance
    Then DRIFT mode should be selected
    And the fuel should be sufficient

  Scenario: Select mode requiring return trip - BURN if possible
    Given a ship with 400 current fuel and 500 capacity
    And a distance of 75 units
    And return trip is required
    When I select mode for distance
    Then the mode should account for 150 units total distance
    And BURN mode should be selected

  Scenario: Select mode for return trip - CRUISE when BURN too expensive
    Given a ship with 250 current fuel and 500 capacity
    And a distance of 75 units
    And return trip is required
    When I select mode for distance
    Then the mode should account for 150 units total distance
    And CRUISE mode should be selected

  Scenario: Select DRIFT for return trip with limited fuel
    Given a ship with 153 current fuel and 500 capacity
    And a distance of 75 units
    And return trip is required
    When I select mode for distance
    Then the mode should account for 150 units total distance
    And DRIFT mode should be selected

  Scenario: Insufficient fuel - return DRIFT and require refueling
    Given a ship with 10 current fuel and 500 capacity
    And a distance of 500 units
    When I select mode for distance
    Then DRIFT mode should be returned as cheapest option
    And the caller should handle refueling

  # Mode Comparison
  Scenario: Compare fuel efficiency between modes
    Given a distance of 300 units
    When I calculate fuel costs for all modes
    Then DRIFT should be most fuel efficient
    And BURN should be least fuel efficient
    And CRUISE and STEALTH should have equal fuel cost

  Scenario: Compare speed between modes
    Given a distance of 300 units
    And engine speed 30
    When I calculate travel times for all modes
    Then BURN should be fastest
    And STEALTH should be slowest
    And DRIFT should be faster than CRUISE

  Scenario: Speed-fuel tradeoff
    Given a distance of 200 units
    When I compare CRUISE and BURN modes
    Then BURN should be twice as fast as CRUISE
    And BURN should use twice as much fuel as CRUISE

  # Edge Cases
  Scenario: Mode selection with zero fuel
    Given a ship with 0 current fuel and 100 capacity
    And a distance of 1 units
    When I select mode for distance
    Then DRIFT mode should be selected

  Scenario: Fuel cost calculation with very large distance
    Given CRUISE flight mode
    And a distance of 100000 units
    When I calculate the fuel cost
    Then the fuel required should be 100000 units

  Scenario: Travel time with zero engine speed
    Given CRUISE flight mode
    And a distance of 100 units
    And engine speed 0
    When I calculate the travel time
    Then the time should be calculated with minimum engine speed 1
