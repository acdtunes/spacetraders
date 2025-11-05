Feature: Get Ship Location Query
  As a ship operator
  I want to query a ship's current location
  So that I can make navigation decisions

  Background:
    Given the get ship location query handler is initialized

  # Query Creation and Immutability

  Scenario: Create query with required fields
    When I create a query for ship "SHIP-1" and player 1
    Then the query should have ship symbol "SHIP-1"
    And the query should have player id 1

  Scenario: Query is immutable
    Given I create a query for ship "SHIP-1" and player 1
    When I attempt to modify the query ship symbol to "SHIP-2"
    Then the modification should fail with AttributeError

  Scenario: Query with different player IDs
    Given I create a query for ship "SHIP-1" and player 1
    And I create a query for ship "SHIP-1" and player 2
    Then the first query should have player id 1
    And the second query should have player id 2

  # Successful Location Retrieval

  Scenario: Successful ship location retrieval
    Given a ship "SHIP-1" exists for player 1
    And the ship is at waypoint "X1-TEST-LOCATION" at coordinates 100.0, 200.0
    And the waypoint has system symbol "X1"
    And the waypoint has type "PLANET"
    And the waypoint has traits "MARKETPLACE" and "SHIPYARD"
    And the waypoint has fuel available
    And the waypoint has orbitals "X1-TEST-ORBITAL-1" and "X1-TEST-ORBITAL-2"
    When I query location for ship "SHIP-1" and player 1
    Then the query should succeed
    And the location should be a Waypoint
    And the location symbol should be "X1-TEST-LOCATION"
    And the location coordinates should be 100.0, 200.0
    And the location system symbol should be "X1"
    And the repository should have been queried for ship "SHIP-1" and player 1

  # Error Conditions

  Scenario: Ship not found
    Given no ships exist in the repository
    When I query location for ship "NONEXISTENT" and player 1
    Then the query should fail with ShipNotFoundError
    And the error message should contain "NONEXISTENT"
    And the error message should contain "player 1"
    And the repository should have been queried for ship "NONEXISTENT" and player 1

  Scenario: Ship not found for different player
    Given a ship "SHIP-1" exists for player 1
    When I query location for ship "SHIP-1" and player 999
    Then the query should fail with ShipNotFoundError
    And the error message should contain "SHIP-1"
    And the error message should contain "player 999"

  # Waypoint Properties

  Scenario: Location preserves all waypoint properties
    Given a ship "SHIP-1" exists for player 1
    And the ship is at waypoint "X1-TEST-LOCATION" at coordinates 100.0, 200.0
    And the waypoint has type "PLANET"
    And the waypoint has traits "MARKETPLACE" and "SHIPYARD"
    And the waypoint has fuel available
    And the waypoint has orbitals "X1-TEST-ORBITAL-1" and "X1-TEST-ORBITAL-2"
    When I query location for ship "SHIP-1" and player 1
    Then the query should succeed
    And the location waypoint type should be "PLANET"
    And the location should have traits "MARKETPLACE" and "SHIPYARD"
    And the location should have fuel available
    And the location should have orbitals "X1-TEST-ORBITAL-1" and "X1-TEST-ORBITAL-2"

  Scenario: Location at orbital station
    Given a ship "SHIP-ORBITAL" exists for player 1
    And the ship is at waypoint "X1-PLANET-ORBITAL" at coordinates 50.0, 75.0
    And the waypoint has system symbol "X1"
    And the waypoint has type "ORBITAL_STATION"
    And the waypoint has fuel available
    And the waypoint has orbitals "X1-PLANET"
    When I query location for ship "SHIP-ORBITAL" and player 1
    Then the query should succeed
    And the location symbol should be "X1-PLANET-ORBITAL"
    And the location waypoint type should be "ORBITAL_STATION"

  Scenario: Location with no fuel station
    Given a ship "SHIP-REMOTE" exists for player 1
    And the ship is at waypoint "X1-REMOTE-ASTEROID" at coordinates 1000.0, 1000.0
    And the waypoint has system symbol "X1"
    And the waypoint has type "ASTEROID"
    And the waypoint does not have fuel available
    When I query location for ship "SHIP-REMOTE" and player 1
    Then the query should succeed
    And the location should not have fuel available
    And the location waypoint type should be "ASTEROID"

  # Multiple Ships

  Scenario: Retrieve locations for multiple ships
    Given a ship "SHIP-1" exists for player 1
    And the ship is at waypoint "X1-LOC-1" at coordinates 0.0, 0.0
    And the waypoint has type "PLANET"
    And a ship "SHIP-2" exists for player 1
    And the second ship is at waypoint "X1-LOC-2" at coordinates 100.0, 100.0
    And the second waypoint has type "MOON"
    When I query location for ship "SHIP-1" and player 1
    And I query location for ship "SHIP-2" and player 1
    Then the first location symbol should be "X1-LOC-1"
    And the first location waypoint type should be "PLANET"
    And the second location symbol should be "X1-LOC-2"
    And the second location waypoint type should be "MOON"

  # Read-Only Operations

  Scenario: Handler is read-only and does not modify ship state
    Given a ship "SHIP-1" exists for player 1
    And the ship is at waypoint "X1-TEST-LOCATION" at coordinates 100.0, 200.0
    And the ship has 100 current fuel and 200 capacity
    When I query location for ship "SHIP-1" and player 1
    Then the query should succeed
    And the ship location should remain unchanged in the repository
    And the repository should not have any save or update calls

  # Coordinate Precision

  Scenario: Coordinates maintain precision
    Given a ship "SHIP-PRECISE" exists for player 1
    And the ship is at waypoint "X1-PRECISE" at coordinates 123.456789, 987.654321
    And the waypoint has system symbol "X1"
    And the waypoint has type "PLANET"
    When I query location for ship "SHIP-PRECISE" and player 1
    Then the query should succeed
    And the location coordinates should be 123.456789, 987.654321
