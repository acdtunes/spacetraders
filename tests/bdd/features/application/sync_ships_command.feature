Feature: Sync Ships Command
  As a bot operator
  I want to synchronize ship data from the SpaceTraders API
  So that my local database reflects the current state of all ships

  Scenario: Sync creates new ships
    Given the API returns 3 ships
    When I sync ships for player 1
    Then 3 ships should be created
    And all ships should be Ship entities
    And ships "SHIP-1", "SHIP-2", "SHIP-3" should exist in the database

  Scenario: Sync updates existing ships
    Given a ship "SHIP-1" exists for player 1 at "X1-OLD-AB12" with 50 fuel
    And the API returns ship "SHIP-1" at "X1-NEW-AB12" with 100 fuel
    When I sync ships for player 1
    Then 1 ship should be returned
    And no new ships should be created
    And 1 ship should be updated
    And ship "SHIP-1" should be in the updated list

  Scenario: Sync mixed create and update
    Given a ship "SHIP-1" exists for player 1 at "X1-TEST-AB12" with 50 fuel
    And the API returns ship "SHIP-1" with 100 fuel
    And the API returns a new ship "SHIP-2"
    When I sync ships for player 1
    Then 2 ships should be returned
    And 1 new ship should be created
    And 1 ship should be updated
    And ship "SHIP-2" should be created
    And ship "SHIP-1" should be updated

  Scenario: Sync with empty API response
    Given the API returns no ships
    When I sync ships for player 1
    Then 0 ships should be returned
    And no ships should be created

  Scenario: Sync converts API data correctly
    Given the API returns ship "SHIP-1" with:
      | field           | value        |
      | waypoint        | X1-TEST-AB12 |
      | system          | X1-TEST      |
      | status          | DOCKED       |
      | fuel_current    | 75           |
      | fuel_capacity   | 100          |
      | cargo_capacity  | 50           |
      | cargo_units     | 10           |
      | engine_speed    | 25           |
      | x               | 10.0         |
      | y               | 20.0         |
      | waypoint_type   | ASTEROID     |
    When I sync ships for player 1
    Then the synced ship should have:
      | field           | value        |
      | ship_symbol     | SHIP-1       |
      | player_id       | 1            |
      | waypoint        | X1-TEST-AB12 |
      | system          | X1-TEST      |
      | x               | 10.0         |
      | y               | 20.0         |
      | waypoint_type   | ASTEROID     |
      | nav_status      | DOCKED       |
      | fuel_current    | 75           |
      | fuel_capacity   | 100          |
      | cargo_capacity  | 50           |
      | cargo_units     | 10           |
      | engine_speed    | 25           |

  Scenario: Sync handles different nav statuses
    Given the API returns ship "SHIP-1" with status "DOCKED"
    And the API returns ship "SHIP-2" with status "IN_ORBIT"
    And the API returns ship "SHIP-3" with status "IN_TRANSIT"
    When I sync ships for player 1
    Then 3 ships should be returned
    And ship "SHIP-1" should have nav status "DOCKED"
    And ship "SHIP-2" should have nav status "IN_ORBIT"
    And ship "SHIP-3" should have nav status "IN_TRANSIT"

  Scenario: Sync continues on single ship error
    Given the API returns ship "SHIP-1" with valid data
    And the API returns ship "SHIP-2" with invalid data
    And the API returns ship "SHIP-3" with valid data
    When I sync ships for player 1
    Then 2 ships should be returned
    And ships "SHIP-1", "SHIP-3" should be synced
    And ship "SHIP-2" should not be synced

  Scenario: Sync fetches agent info
    Given the API agent symbol is "MY-AGENT"
    And the API returns no ships
    When I sync ships for player 1
    Then agent info should be fetched successfully

  Scenario: Sync assigns correct player ID
    Given the API returns 2 ships
    When I sync ships for player 42
    Then all synced ships should have player_id 42

  Scenario: Sync updates fuel state
    Given a ship "SHIP-1" exists for player 1 with 50 fuel
    And the API returns ship "SHIP-1" with 100 fuel
    When I sync ships for player 1
    Then ship "SHIP-1" should have 100 fuel

  Scenario: Sync updates location
    Given a ship "SHIP-1" exists for player 1 at "X1-OLD-AB12"
    And the API returns ship "SHIP-1" at "X1-NEW-CD34"
    When I sync ships for player 1
    Then ship "SHIP-1" should be at "X1-NEW-CD34"

  Scenario: Sync returns all synced ships
    Given the API returns 4 ships "SHIP-1", "SHIP-2", "SHIP-3", "SHIP-4"
    When I sync ships for player 1
    Then 4 ships should be returned
    And the returned ships should be "SHIP-1", "SHIP-2", "SHIP-3", "SHIP-4"
