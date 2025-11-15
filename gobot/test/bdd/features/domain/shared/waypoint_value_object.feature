Feature: Waypoint Value Object
  As a navigation system
  I want to work with immutable waypoint value objects
  So that I can safely share location data across the system

  # ============================================================================
  # Waypoint Initialization Tests
  # ============================================================================

  Scenario: Create waypoint with valid data
    When I create a waypoint with symbol "X1-A1", x 0.0, y 0.0
    Then the waypoint should have symbol "X1-A1"
    And the waypoint should have x coordinate 0.0
    And the waypoint should have y coordinate 0.0

  Scenario: Create waypoint with empty symbol fails
    When I attempt to create a waypoint with empty symbol
    Then waypoint creation should fail with error "cannot be empty"

  # ============================================================================
  # Distance Calculation Tests
  # ============================================================================

  Scenario: Calculate distance between two waypoints
    Given a waypoint "X1-A1" at coordinates (0.0, 0.0)
    And a waypoint "X1-B2" at coordinates (3.0, 4.0)
    When I calculate distance from "X1-A1" to "X1-B2"
    Then the distance should be 5.0

  Scenario: Calculate distance to same waypoint is zero
    Given a waypoint "X1-A1" at coordinates (0.0, 0.0)
    When I calculate distance from "X1-A1" to "X1-A1"
    Then the distance should be 0.0

  Scenario: Calculate distance with negative coordinates
    Given a waypoint "X1-A1" at coordinates (-10.0, -20.0)
    And a waypoint "X1-B2" at coordinates (10.0, 20.0)
    When I calculate distance from "X1-A1" to "X1-B2"
    Then the distance should be approximately 44.72

  Scenario: Distance calculation is symmetric
    Given a waypoint "X1-A1" at coordinates (0.0, 0.0)
    And a waypoint "X1-B2" at coordinates (100.0, 0.0)
    When I calculate distance from "X1-A1" to "X1-B2"
    And I calculate distance from "X1-B2" to "X1-A1"
    Then both distances should be equal

  # ============================================================================
  # Orbital Relationship Tests
  # ============================================================================

  Scenario: Check if waypoint is orbital of another
    Given a waypoint "X1-A1" with orbitals ["X1-A1-B"]
    And a waypoint "X1-A1-B" at coordinates (0.0, 0.0)
    When I check if "X1-A1-B" is orbital of "X1-A1"
    Then the result should be true

  Scenario: Check waypoint is not orbital of unrelated waypoint
    Given a waypoint "X1-A1" with orbitals ["X1-A1-B"]
    And a waypoint "X1-C3" at coordinates (0.0, 0.0)
    When I check if "X1-C3" is orbital of "X1-A1"
    Then the result should be false

  Scenario: Orbital relationship is bidirectional
    Given a waypoint "X1-A1" with orbitals ["X1-A1-B"]
    And a waypoint "X1-A1-B" at coordinates (0.0, 0.0)
    When I check if "X1-A1" is orbital of "X1-A1-B"
    Then the result should be true

  # ============================================================================
  # Edge Cases for Increased Coverage
  # ============================================================================

  Scenario: Create waypoint at origin
    When I create a waypoint with symbol "ORIGIN", x 0.0, y 0.0
    Then the waypoint should have symbol "ORIGIN"
    And the waypoint should have x coordinate 0.0
    And the waypoint should have y coordinate 0.0

  Scenario: Create waypoint with positive coordinates
    When I create a waypoint with symbol "X1-A1", x 100.5, y 200.75
    Then the waypoint should have x coordinate 100.5
    And the waypoint should have y coordinate 200.75

  Scenario: Create waypoint with negative coordinates
    When I create a waypoint with symbol "X1-A1", x -50.25, y -75.5
    Then the waypoint should have x coordinate -50.25
    And the waypoint should have y coordinate -75.5

  Scenario: Create waypoint with mixed sign coordinates
    When I create a waypoint with symbol "X1-A1", x -100.0, y 200.0
    Then the waypoint should have x coordinate -100.0
    And the waypoint should have y coordinate 200.0

  Scenario: Calculate distance along x-axis only
    Given a waypoint "X1-A1" at coordinates (0.0, 0.0)
    And a waypoint "X1-B2" at coordinates (100.0, 0.0)
    When I calculate distance from "X1-A1" to "X1-B2"
    Then the distance should be 100.0

  Scenario: Calculate distance along y-axis only
    Given a waypoint "X1-A1" at coordinates (0.0, 0.0)
    And a waypoint "X1-B2" at coordinates (0.0, 100.0)
    When I calculate distance from "X1-A1" to "X1-B2"
    Then the distance should be 100.0

  Scenario: Calculate distance with very small difference
    Given a waypoint "X1-A1" at coordinates (0.0, 0.0)
    And a waypoint "X1-B2" at coordinates (0.1, 0.1)
    When I calculate distance from "X1-A1" to "X1-B2"
    Then the distance should be approximately 0.14

  Scenario: Calculate distance with large coordinates
    Given a waypoint "X1-A1" at coordinates (10000.0, 10000.0)
    And a waypoint "X1-B2" at coordinates (20000.0, 20000.0)
    When I calculate distance from "X1-A1" to "X1-B2"
    Then the distance should be approximately 14142.14

  Scenario: Waypoint with no orbitals
    Given a waypoint "X1-A1" with no orbitals
    And a waypoint "X1-B2" at coordinates (0.0, 0.0)
    When I check if "X1-B2" is orbital of "X1-A1"
    Then the result should be false

  Scenario: Waypoint with multiple orbitals
    Given a waypoint "X1-A1" with orbitals ["X1-A1-B", "X1-A1-C", "X1-A1-D"]
    And a waypoint "X1-A1-C" at coordinates (0.0, 0.0)
    When I check if "X1-A1-C" is orbital of "X1-A1"
    Then the result should be true

  Scenario: Check non-orbital from multiple orbital list
    Given a waypoint "X1-A1" with orbitals ["X1-A1-B", "X1-A1-C"]
    And a waypoint "X1-A1-D" at coordinates (0.0, 0.0)
    When I check if "X1-A1-D" is orbital of "X1-A1"
    Then the result should be false

  Scenario: Waypoint symbol is case sensitive
    When I create a waypoint with symbol "X1-a1", x 0.0, y 0.0
    Then the waypoint should have symbol "X1-a1"

  Scenario: Calculate distance from negative to positive quadrant
    Given a waypoint "X1-A1" at coordinates (-50.0, -50.0)
    And a waypoint "X1-B2" at coordinates (50.0, 50.0)
    When I calculate distance from "X1-A1" to "X1-B2"
    Then the distance should be approximately 141.42

  Scenario: Zero distance for identical coordinates different symbols
    Given a waypoint "X1-A1" at coordinates (100.0, 200.0)
    And a waypoint "X1-B2" at coordinates (100.0, 200.0)
    When I calculate distance from "X1-A1" to "X1-B2"
    Then the distance should be 0.0

  Scenario: Distance calculation precision with diagonal
    Given a waypoint "X1-A1" at coordinates (0.0, 0.0)
    And a waypoint "X1-B2" at coordinates (1.0, 1.0)
    When I calculate distance from "X1-A1" to "X1-B2"
    Then the distance should be approximately 1.41

  Scenario: Waypoint with very long symbol
    When I create a waypoint with symbol "X1-VERYLONGWAYPOINTSYMBOLNAME-12345", x 0.0, y 0.0
    Then the waypoint should have symbol "X1-VERYLONGWAYPOINTSYMBOLNAME-12345"

  Scenario: Self-orbital check
    Given a waypoint "X1-A1" with orbitals ["X1-A1"]
    When I check if "X1-A1" is orbital of "X1-A1"
    Then the result should be true
