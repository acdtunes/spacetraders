Feature: Waypoint Value Object
  As a SpaceTraders bot
  I want to work with waypoint value objects
  So that I can manage locations and extract system symbols correctly

  # ============================================================================
  # System Symbol Extraction
  # ============================================================================

  Scenario: Extract system symbol from simple waypoint symbol
    Given a waypoint symbol "X1-A1"
    When I extract the system symbol
    Then the system symbol should be "X1"

  Scenario: Extract system symbol from complex waypoint symbol
    Given a waypoint symbol "X1-AB-C1"
    When I extract the system symbol
    Then the system symbol should be "X1-AB"

  Scenario: Extract system symbol from multi-segment waypoint symbol
    Given a waypoint symbol "X1-AB-CD-E1"
    When I extract the system symbol
    Then the system symbol should be "X1-AB-CD"

  Scenario: Handle waypoint symbol with no hyphen
    Given a waypoint symbol "SINGLESYMBOL"
    When I extract the system symbol
    Then the system symbol should be "SINGLESYMBOL"

  Scenario: Extract system symbol from waypoint object
    Given a waypoint with symbol "X1-C3" at coordinates (10, 20)
    When I get the system symbol from the waypoint
    Then the waypoint's system symbol should be "X1"

  # ============================================================================
  # Find Nearest Waypoint
  # ============================================================================

  Scenario: Find nearest waypoint from multiple targets
    Given a waypoint "START" at coordinates (0, 0)
    And a list of target waypoints:
      | symbol   | x   | y   |
      | TARGET-1 | 10  | 0   |
      | TARGET-2 | 3   | 4   |
      | TARGET-3 | 100 | 100 |
    When I find the nearest waypoint from "START" to the target list
    Then the nearest waypoint should be "TARGET-2"
    And the distance to nearest waypoint should be 5.0

  Scenario: Find nearest waypoint with single target
    Given a waypoint "START" at coordinates (0, 0)
    And a list of target waypoints:
      | symbol   | x  | y  |
      | TARGET-1 | 10 | 10 |
    When I find the nearest waypoint from "START" to the target list
    Then the nearest waypoint should be "TARGET-1"
    And the distance to nearest waypoint should be 14.142135623730951

  Scenario: Find nearest waypoint returns nil for empty list
    Given a waypoint "START" at coordinates (0, 0)
    And an empty list of target waypoints
    When I find the nearest waypoint from "START" to the target list
    Then the nearest waypoint should be nil
    And the distance to nearest waypoint should be 0.0

  Scenario: Find nearest waypoint with identical distances chooses first
    Given a waypoint "START" at coordinates (0, 0)
    And a list of target waypoints:
      | symbol   | x | y |
      | TARGET-1 | 5 | 0 |
      | TARGET-2 | 0 | 5 |
      | TARGET-3 | 3 | 4 |
    When I find the nearest waypoint from "START" to the target list
    Then the nearest waypoint should be "TARGET-1"
    And the distance to nearest waypoint should be 5.0
