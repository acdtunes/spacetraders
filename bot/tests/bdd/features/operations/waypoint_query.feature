Feature: Waypoint query operations
  As a fleet manager
  I want to query waypoints with various filters
  So that I can find suitable locations for operations

  Background:
    Given a waypoint query system with database

  Scenario: Query all waypoints in system
    Given system "X1-TEST" has 5 waypoints
    When I query waypoints for system "X1-TEST"
    Then 5 waypoints should be returned
    And all waypoints should be in system "X1-TEST"

  Scenario: Filter waypoints by type
    Given system "X1-TEST" has waypoint "X1-TEST-A1" of type "PLANET"
    And system "X1-TEST" has waypoint "X1-TEST-B2" of type "ASTEROID"
    And system "X1-TEST" has waypoint "X1-TEST-C3" of type "MOON"
    And system "X1-TEST" has waypoint "X1-TEST-D4" of type "ASTEROID"
    When I query waypoints for system "X1-TEST" with type "ASTEROID"
    Then 2 waypoints should be returned
    And all returned waypoints should have type "ASTEROID"

  Scenario: Filter waypoints by trait
    Given system "X1-TEST" has waypoint "X1-TEST-A1" with trait "MARKETPLACE"
    And system "X1-TEST" has waypoint "X1-TEST-B2" with trait "SHIPYARD"
    And system "X1-TEST" has waypoint "X1-TEST-C3" with trait "MARKETPLACE"
    When I query waypoints for system "X1-TEST" with trait "MARKETPLACE"
    Then 2 waypoints should be returned
    And all returned waypoints should have trait "MARKETPLACE"

  Scenario: Exclude waypoints with specific trait
    Given system "X1-TEST" has waypoint "X1-TEST-A1" with trait "SAFE"
    And system "X1-TEST" has waypoint "X1-TEST-B2" with trait "RADIOACTIVE"
    And system "X1-TEST" has waypoint "X1-TEST-C3" with trait "SAFE"
    And system "X1-TEST" has waypoint "X1-TEST-D4" with trait "SAFE"
    When I query waypoints for system "X1-TEST" excluding trait "RADIOACTIVE"
    Then 3 waypoints should be returned
    And no returned waypoints should have trait "RADIOACTIVE"

  Scenario: Exclude multiple traits
    Given system "X1-TEST" has waypoint "X1-TEST-A1" with trait "SAFE"
    And system "X1-TEST" has waypoint "X1-TEST-B2" with trait "RADIOACTIVE"
    And system "X1-TEST" has waypoint "X1-TEST-C3" with trait "EXPLOSIVE_GASES"
    And system "X1-TEST" has waypoint "X1-TEST-D4" with trait "STRIPPED"
    And system "X1-TEST" has waypoint "X1-TEST-E5" with trait "SAFE"
    When I query waypoints for system "X1-TEST" excluding traits "RADIOACTIVE,EXPLOSIVE_GASES,STRIPPED"
    Then 2 waypoints should be returned
    And no returned waypoints should have excluded traits

  Scenario: Filter waypoints with fuel availability
    Given system "X1-TEST" has waypoint "X1-TEST-A1" with fuel
    And system "X1-TEST" has waypoint "X1-TEST-B2" without fuel
    And system "X1-TEST" has waypoint "X1-TEST-C3" with fuel
    And system "X1-TEST" has waypoint "X1-TEST-D4" without fuel
    When I query waypoints for system "X1-TEST" with has_fuel filter
    Then 2 waypoints should be returned

  Scenario: Combine type and trait filters
    Given system "X1-TEST" has waypoint "X1-TEST-A1" of type "PLANET" with trait "MARKETPLACE"
    And system "X1-TEST" has waypoint "X1-TEST-B2" of type "ASTEROID" with trait "MINERALS"
    And system "X1-TEST" has waypoint "X1-TEST-C3" of type "ASTEROID" with trait "MARKETPLACE"
    And system "X1-TEST" has waypoint "X1-TEST-D4" of type "MOON" with trait "MARKETPLACE"
    When I query waypoints for system "X1-TEST" with type "ASTEROID" and trait "MARKETPLACE"
    Then 1 waypoint should be returned
    And returned waypoint should be "X1-TEST-C3"

  Scenario: No waypoints match criteria
    Given system "X1-TEST" has 3 waypoints without "SHIPYARD" trait
    When I query waypoints for system "X1-TEST" with trait "SHIPYARD"
    Then 0 waypoints should be returned
    And query should indicate no matches found

  Scenario: Display waypoint coordinates
    Given system "X1-TEST" has waypoint "X1-TEST-A1" at coordinates (100, 200)
    When I query waypoints for system "X1-TEST"
    Then waypoint "X1-TEST-A1" should show coordinates (100, 200)

  Scenario: Display orbital relationships
    Given system "X1-TEST" has waypoint "X1-TEST-A1" with orbitals "X1-TEST-A2,X1-TEST-A3"
    When I query waypoints for system "X1-TEST"
    Then waypoint "X1-TEST-A1" should list orbitals
