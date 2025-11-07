Feature: Ship Repository API-Only Implementation
  As a ship repository
  I want to fetch all ship data directly from the SpaceTraders API
  So that ship state is always fresh and consistent

  Background:
    Given a mock API client that returns ship data
    And a graph provider for waypoint reconstruction
    And an API-only ship repository

  Scenario: Find ship by symbol fetches from API
    Given the mock API returns ship "TEST-1" with location "X1-A1"
    When I find ship "TEST-1" for player 1
    Then the ship should be found
    And the ship symbol should be "TEST-1"
    And the ship location should be "X1-A1"
    And the API client should have been called with "get_ship"

  Scenario: Find ship returns None when API returns 404
    Given the mock API returns 404 for ship "NONEXISTENT"
    When I find ship "NONEXISTENT" for player 1
    Then the ship should not be found

  Scenario: Find all ships by player fetches from API
    Given the mock API returns 3 ships for player 1
    When I list all ships for player 1
    Then I should see 3 ships
    And the API client should have been called with "get_ships"

  Scenario: Find all ships returns empty list when player has no ships
    Given the mock API returns 0 ships for player 1
    When I list all ships for player 1
    Then I should see 0 ships
