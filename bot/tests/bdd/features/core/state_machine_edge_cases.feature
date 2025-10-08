Feature: State Machine Edge Cases
  As a bot operator
  I want state transitions to handle edge cases
  So that the bot is robust in all scenarios

  Background:
    Given the SpaceTraders API is mocked
    And the system "X1-HU87" has waypoints:
      | symbol     | x   | y   |
      | X1-HU87-A1 | 0   | 0   |
      | X1-HU87-B9 | 100 | 0   |

  Scenario: IN_TRANSIT ship with missing route data
    Given a ship "TEST-1" is IN_TRANSIT at "X1-HU87-A1"
    And the ship nav data is corrupted with no route
    When I request transition to "IN_ORBIT"
    Then the system should handle gracefully
    And no crash should occur

  Scenario: API failure during state transition
    Given a ship "TEST-1" is DOCKED at "X1-HU87-A1"
    And the orbit API endpoint will fail
    When I request transition to "IN_ORBIT"
    Then the transition should fail
    And the ship should remain DOCKED

  Scenario: Rapid state transitions
    Given a ship "TEST-1" is DOCKED at "X1-HU87-A1"
    When I request these transitions in sequence:
      | from     | to       |
      | DOCKED   | IN_ORBIT |
      | IN_ORBIT | DOCKED   |
      | DOCKED   | IN_ORBIT |
    Then all transitions should succeed
    And the final state should be IN_ORBIT

  Scenario: State transition during navigation
    Given a ship "TEST-1" is IN_ORBIT at "X1-HU87-A1"
    And navigation to "X1-HU87-B9" is in progress
    When navigation completes
    Then the ship should be IN_ORBIT at "X1-HU87-B9"
    And the state should be consistent
