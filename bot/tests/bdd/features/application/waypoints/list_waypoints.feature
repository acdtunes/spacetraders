Feature: List Waypoints Query
  As a SpaceTraders bot operator
  I want to query cached waypoints from the database
  So that I can view waypoint information without making API calls

  Background:
    Given the list waypoints query handler is initialized

  Scenario: List all waypoints in a system
    Given a waypoint "X1-HZ85-A1" exists in system "X1-HZ85"
    And the waypoint has type "PLANET"
    And the waypoint has traits "MARKETPLACE", "SHIPYARD"
    And a waypoint "X1-HZ85-B2" exists in system "X1-HZ85"
    And the waypoint has type "ASTEROID"
    And the waypoint has traits "MARKETPLACE"
    When I query waypoints for system "X1-HZ85"
    Then the query should succeed
    And the result should be a list
    And the list should contain 2 waypoints
    And all waypoints should be Waypoint instances
    And the waypoint at index 0 should have symbol "X1-HZ85-A1"
    And the waypoint at index 1 should have symbol "X1-HZ85-B2"

  Scenario: List waypoints with no filters returns all waypoints
    Given a waypoint "X1-TEST-A1" exists in system "X1-TEST"
    And the waypoint has type "PLANET"
    And the waypoint has traits "MARKETPLACE"
    And a waypoint "X1-TEST-B2" exists in system "X1-TEST"
    And the waypoint has type "MOON"
    And a waypoint "X1-TEST-C3" exists in system "X1-TEST"
    And the waypoint has type "ASTEROID"
    When I query waypoints for system "X1-TEST"
    Then the list should contain 3 waypoints

  Scenario: Filter waypoints by MARKETPLACE trait
    Given a waypoint "X1-GZ7-A1" exists in system "X1-GZ7"
    And the waypoint has traits "MARKETPLACE", "SHIPYARD"
    And a waypoint "X1-GZ7-B2" exists in system "X1-GZ7"
    And the waypoint has traits "MARKETPLACE"
    And a waypoint "X1-GZ7-C3" exists in system "X1-GZ7"
    And the waypoint has traits "SHIPYARD"
    When I query waypoints for system "X1-GZ7" with trait "MARKETPLACE"
    Then the list should contain 2 waypoints
    And the waypoint at index 0 should have symbol "X1-GZ7-A1"
    And the waypoint at index 1 should have symbol "X1-GZ7-B2"

  Scenario: Filter waypoints by SHIPYARD trait
    Given a waypoint "X1-ABC-A1" exists in system "X1-ABC"
    And the waypoint has traits "MARKETPLACE", "SHIPYARD"
    And a waypoint "X1-ABC-B2" exists in system "X1-ABC"
    And the waypoint has traits "SHIPYARD"
    And a waypoint "X1-ABC-C3" exists in system "X1-ABC"
    And the waypoint has traits "MARKETPLACE"
    When I query waypoints for system "X1-ABC" with trait "SHIPYARD"
    Then the list should contain 2 waypoints
    And the waypoint at index 0 should have symbol "X1-ABC-A1"
    And the waypoint at index 1 should have symbol "X1-ABC-B2"

  Scenario: Filter waypoints by fuel availability
    Given a waypoint "X1-FUEL-A1" exists in system "X1-FUEL"
    And the waypoint has fuel available
    And a waypoint "X1-FUEL-B2" exists in system "X1-FUEL"
    And the waypoint has no fuel
    And a waypoint "X1-FUEL-C3" exists in system "X1-FUEL"
    And the waypoint has fuel available
    When I query waypoints for system "X1-FUEL" with fuel filter
    Then the list should contain 2 waypoints
    And the waypoint at index 0 should have symbol "X1-FUEL-A1"
    And the waypoint at index 1 should have symbol "X1-FUEL-C3"

  Scenario: Query empty system returns empty list
    Given no waypoints exist in system "X1-EMPTY"
    When I query waypoints for system "X1-EMPTY"
    Then the list should be empty

  Scenario: Query with non-matching trait returns empty list
    Given a waypoint "X1-NONE-A1" exists in system "X1-NONE"
    And the waypoint has traits "MARKETPLACE"
    When I query waypoints for system "X1-NONE" with trait "SHIPYARD"
    Then the list should be empty

  Scenario: Query with fuel filter on system with no fuel returns empty
    Given a waypoint "X1-DRY-A1" exists in system "X1-DRY"
    And the waypoint has no fuel
    And a waypoint "X1-DRY-B2" exists in system "X1-DRY"
    And the waypoint has no fuel
    When I query waypoints for system "X1-DRY" with fuel filter
    Then the list should be empty

  Scenario: Waypoints preserve all attributes correctly
    Given a waypoint "X1-DATA-A1" exists in system "X1-DATA"
    And the waypoint is at coordinates 100.5, 200.75
    And the waypoint has type "GAS_GIANT"
    And the waypoint has traits "MARKETPLACE", "SHIPYARD", "FUEL_STATION"
    And the waypoint has fuel available
    When I query waypoints for system "X1-DATA"
    Then the waypoint at index 0 should have symbol "X1-DATA-A1"
    And the waypoint at index 0 should have system symbol "X1-DATA"
    And the waypoint at index 0 should be at coordinates 100.5, 200.75
    And the waypoint at index 0 should have type "GAS_GIANT"
    And the waypoint at index 0 should have traits "MARKETPLACE", "SHIPYARD", "FUEL_STATION"
    And the waypoint at index 0 should have fuel available

  Scenario: ListWaypointsQuery is immutable
    Given I create a list waypoints query for system "X1-IMMUTABLE"
    When I attempt to modify the query system to "X1-MODIFIED"
    Then the modification should fail with AttributeError
