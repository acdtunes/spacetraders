Feature: Ship State Machine
  As a bot operator
  I want ship state transitions to be handled automatically
  So that navigation always works regardless of current state

  Background:
    Given the SpaceTraders API is mocked
    And the system "X1-HU87" has waypoints:
      | symbol     | x   | y   |
      | X1-HU87-A1 | 0   | 0   |
      | X1-HU87-B9 | 100 | 0   |

  Scenario: DOCKED ship automatically orbits before navigation
    Given a ship "TEST-1" is DOCKED at "X1-HU87-A1" with 400 fuel
    When I navigate to "X1-HU87-B9"
    Then the ship should automatically orbit
    And the ship should navigate to "X1-HU87-B9"
    And the ship should be in "IN_ORBIT" state

  Scenario: IN_ORBIT ship navigates directly
    Given a ship "TEST-1" is IN_ORBIT at "X1-HU87-A1" with 400 fuel
    When I navigate to "X1-HU87-B9"
    Then the ship should navigate directly
    And the ship should be at "X1-HU87-B9"

  Scenario: IN_TRANSIT ship waits for arrival
    Given a ship "TEST-1" is IN_TRANSIT to "X1-HU87-B9"
    When I navigate to "X1-HU87-B9"
    Then the ship should wait for arrival
    And the ship should be at "X1-HU87-B9"

  Scenario: Damaged ship cannot navigate
    Given a ship "TEST-1" at "X1-HU87-A1" with 40% integrity
    When I attempt to navigate to "X1-HU87-B9"
    Then the navigation should fail
    And the error should mention "damage" or "integrity"

  Scenario: Refuel requires DOCKED state
    Given a ship "TEST-1" is IN_ORBIT at "X1-HU87-A1"
    When a refuel stop is executed
    Then the ship should automatically dock
    And the ship should refuel successfully
    And the ship should be DOCKED
