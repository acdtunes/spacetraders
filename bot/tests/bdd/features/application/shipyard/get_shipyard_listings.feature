Feature: Get Shipyard Listings Query
  As a player
  I want to view available ships at a shipyard
  So that I can decide which ship to purchase

  Scenario: Successfully get shipyard listings with available ships
    Given I have a player with ID 1
    And the API returns a shipyard at "X1-GZ7-AB12" with ships:
      | ship_type              | name            | price  |
      | SHIP_MINING_DRONE      | Mining Drone    | 50000  |
      | SHIP_PROBE             | Probe Satellite | 25000  |
    When I query shipyard listings for system "X1-GZ7" and waypoint "X1-GZ7-AB12" as player 1
    Then the query should succeed
    And the shipyard should have symbol "X1-GZ7-AB12"
    And the shipyard should have 2 listings
    And the shipyard should have a listing for "SHIP_MINING_DRONE" priced at 50000
    And the shipyard should have a listing for "SHIP_PROBE" priced at 25000

  Scenario: Shipyard not found at waypoint
    Given I have a player with ID 1
    And the API returns a 404 error for shipyard at "X1-GZ7-NO-SHIPYARD"
    When I query shipyard listings for system "X1-GZ7" and waypoint "X1-GZ7-NO-SHIPYARD" as player 1
    Then the query should fail with "ShipyardNotFoundError"

  Scenario: Empty shipyard with no ships available
    Given I have a player with ID 1
    And the API returns a shipyard at "X1-GZ7-AB12" with no ships
    When I query shipyard listings for system "X1-GZ7" and waypoint "X1-GZ7-AB12" as player 1
    Then the query should succeed
    And the shipyard should have symbol "X1-GZ7-AB12"
    And the shipyard should have 0 listings

  Scenario: API returns invalid system symbol
    Given I have a player with ID 1
    And the API returns a 400 error for invalid system "INVALID-SYSTEM"
    When I query shipyard listings for system "INVALID-SYSTEM" and waypoint "X1-GZ7-AB12" as player 1
    Then the query should fail with error status 400
