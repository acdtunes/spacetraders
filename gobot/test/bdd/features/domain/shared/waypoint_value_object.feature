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
