Feature: Purchase Ship Command
  As a player
  I want to purchase ships from shipyards
  So that I can expand my fleet

  Background:
    Given the purchase ship command handler is initialized

  # Happy Path - Ship already at shipyard and docked
  Scenario: Successfully purchase ship when already at shipyard
    Given I have a player with ID 1 and 100000 credits from API
    And I have a ship "BUYER-1" at waypoint "X1-GZ7-AB12"
    And the ship "BUYER-1" is docked
    And the API returns a shipyard at "X1-GZ7-AB12" with ships:
      | ship_type              | name            | price  |
      | SHIP_MINING_DRONE      | Mining Drone    | 50000  |
    When I purchase a "SHIP_MINING_DRONE" using ship "BUYER-1" at shipyard "X1-GZ7-AB12" for player 1
    Then the purchase should succeed
    And the player should have 50000 credits remaining
    And the new ship should be saved to the repository

  # Happy Path - Ship needs to navigate to shipyard
  Scenario: Successfully purchase ship with auto-navigation to shipyard
    Given I have a player with ID 1 and 100000 credits from API
    And I have a ship "BUYER-1" at waypoint "X1-GZ7-CD34"
    And the ship "BUYER-1" is in orbit with 100 fuel
    And waypoint "X1-GZ7-AB12" exists at distance 50
    And a route exists from "X1-GZ7-CD34" to "X1-GZ7-AB12"
    And the API returns a shipyard at "X1-GZ7-AB12" with ships:
      | ship_type              | name            | price  |
      | SHIP_PROBE             | Probe Satellite | 25000  |
    When I purchase a "SHIP_PROBE" using ship "BUYER-1" at shipyard "X1-GZ7-AB12" for player 1
    Then the purchase should succeed
    And the ship "BUYER-1" should have navigated to "X1-GZ7-AB12"
    And the ship "BUYER-1" should be docked
    And the player should have 75000 credits remaining
    And the new ship should be at waypoint "X1-GZ7-AB12"

  # Happy Path - Ship in orbit needs to dock
  Scenario: Successfully purchase ship when in orbit at shipyard
    Given I have a player with ID 1 and 100000 credits from API
    And I have a ship "BUYER-1" at waypoint "X1-GZ7-AB12"
    And the ship "BUYER-1" is in orbit
    And the API returns a shipyard at "X1-GZ7-AB12" with ships:
      | ship_type              | name            | price  |
      | SHIP_PROBE             | Probe Satellite | 25000  |
    When I purchase a "SHIP_PROBE" using ship "BUYER-1" at shipyard "X1-GZ7-AB12" for player 1
    Then the purchase should succeed
    And the ship "BUYER-1" should have been docked
    And the player should have 75000 credits remaining

  # Error - Insufficient credits
  Scenario: Cannot purchase ship with insufficient credits
    Given I have a player with ID 1 and 30000 credits from API
    And I have a ship "BUYER-1" at waypoint "X1-GZ7-AB12"
    And the ship "BUYER-1" is docked
    And the API returns a shipyard at "X1-GZ7-AB12" with ships:
      | ship_type              | name            | price  |
      | SHIP_MINING_DRONE      | Mining Drone    | 50000  |
    When I attempt to purchase a "SHIP_MINING_DRONE" using ship "BUYER-1" at shipyard "X1-GZ7-AB12" for player 1
    Then the purchase should fail with InsufficientCreditsError
    And the error message should contain "need 50000, have 30000"
    And the player should still have 30000 credits
    And no new ship should be created

  # Error - Ship type not available
  Scenario: Cannot purchase ship type not available at shipyard
    Given I have a player with ID 1 and 100000 credits from API
    And I have a ship "BUYER-1" at waypoint "X1-GZ7-AB12"
    And the ship "BUYER-1" is docked
    And the API returns a shipyard at "X1-GZ7-AB12" with ships:
      | ship_type              | name            | price  |
      | SHIP_MINING_DRONE      | Mining Drone    | 50000  |
    When I attempt to purchase a "SHIP_REFINING_FREIGHTER" using ship "BUYER-1" at shipyard "X1-GZ7-AB12" for player 1
    Then the purchase should fail with ValueError
    And the error message should contain "not available"
    And the player should still have 100000 credits
    And no new ship should be created

  # Error - Shipyard not found
  Scenario: Cannot purchase from non-existent shipyard
    Given I have a player with ID 1 and 100000 credits from API
    And I have a ship "BUYER-1" at waypoint "X1-GZ7-NO-SHIPYARD"
    And the ship "BUYER-1" is docked
    And the API returns a 404 error for shipyard at "X1-GZ7-NO-SHIPYARD"
    When I attempt to purchase a "SHIP_PROBE" using ship "BUYER-1" at shipyard "X1-GZ7-NO-SHIPYARD" for player 1
    Then the purchase should fail with ShipyardNotFoundError
    And the player should still have 100000 credits
    And no new ship should be created

  # Error - Ship cannot navigate to shipyard
  Scenario: Cannot purchase when ship cannot reach shipyard
    Given I have a player with ID 1 and 100000 credits from API
    And I have a ship "BUYER-1" at waypoint "X1-ABC-CD34"
    And the ship "BUYER-1" is in orbit with 100 fuel
    And no route exists from "X1-ABC-CD34" to "X1-GZ7-AB12"
    And the API returns a shipyard at "X1-GZ7-AB12" with ships:
      | ship_type              | name            | price  |
      | SHIP_PROBE             | Probe Satellite | 25000  |
    When I attempt to purchase a "SHIP_PROBE" using ship "BUYER-1" at shipyard "X1-GZ7-AB12" for player 1
    Then the purchase should fail with ValueError
    And the error message should contain "No path found"
    And the player should still have 100000 credits
    And no new ship should be created

  # Verify ship symbol generation
  Scenario: Newly purchased ship has correct symbol from API
    Given I have a player with ID 1 and 100000 credits from API
    And I have a ship "BUYER-1" at waypoint "X1-GZ7-AB12"
    And the ship "BUYER-1" is docked
    And the API returns a shipyard at "X1-GZ7-AB12" with ships:
      | ship_type              | name            | price  |
      | SHIP_PROBE             | Probe Satellite | 25000  |
    And the API will return a new ship "CHROMESAMURAI-3" for purchase
    When I purchase a "SHIP_PROBE" using ship "BUYER-1" at shipyard "X1-GZ7-AB12" for player 1
    Then the purchase should succeed
    And the new ship should have symbol "CHROMESAMURAI-3"
    And the new ship should belong to player 1
    And the new ship should be saved to the repository

  # Auto-discovery: Find nearest shipyard automatically
  Scenario: Automatically find nearest shipyard that sells desired ship type
    Given I have a player with ID 1 and 100000 credits from API
    And I have a ship "BUYER-1" at waypoint "X1-GZ7-CD34"
    And the ship "BUYER-1" is in orbit with 100 fuel
    And the system "X1-GZ7" has the following waypoints with traits:
      | waypoint      | x    | y    | traits    |
      | X1-GZ7-AB12   | 10   | 20   | SHIPYARD  |
      | X1-GZ7-EF56   | 100  | 200  | SHIPYARD  |
      | X1-GZ7-CD34   | 5    | 10   |           |
    And the shipyard at "X1-GZ7-AB12" sells ships:
      | ship_type              | name            | price  |
      | SHIP_MINING_DRONE      | Mining Drone    | 50000  |
    And the shipyard at "X1-GZ7-EF56" sells ships:
      | ship_type              | name            | price  |
      | SHIP_PROBE             | Probe Satellite | 25000  |
    When I purchase ship type "SHIP_MINING_DRONE" using ship "BUYER-1" for player 1
    Then the purchase should succeed
    And the purchasing ship should have navigated to the nearest shipyard "X1-GZ7-AB12"
    And the new ship should be at waypoint "X1-GZ7-AB12"
    And the player should have 50000 credits remaining

  # Auto-discovery: No shipyard in system sells desired ship type
  Scenario: Cannot purchase when no shipyard in system sells desired ship type
    Given I have a player with ID 1 and 100000 credits from API
    And I have a ship "BUYER-1" at waypoint "X1-GZ7-CD34"
    And the ship "BUYER-1" is docked
    And the system "X1-GZ7" has the following waypoints with traits:
      | waypoint      | x    | y    | traits    |
      | X1-GZ7-AB12   | 10   | 20   | SHIPYARD  |
    And the shipyard at "X1-GZ7-AB12" sells ships:
      | ship_type              | name            | price  |
      | SHIP_PROBE             | Probe Satellite | 25000  |
    When I attempt to purchase ship type "SHIP_MINING_DRONE" using ship "BUYER-1" for player 1
    Then the purchase should fail with NoShipyardFoundError
    And the error message should contain "No shipyards in system X1-GZ7 sell SHIP_MINING_DRONE"
    And the player should still have 100000 credits
    And no new ship should be created

  # Auto-discovery: Shipyard is on page 2+ of waypoints (pagination bug scenario)
  Scenario: Successfully find shipyard on second page of waypoint results
    Given I have a player with ID 1 and 100000 credits from API
    And I have a ship "BUYER-1" at waypoint "X1-GZ7-CD34"
    And the ship "BUYER-1" is in orbit with 100 fuel
    And the system "X1-GZ7" has paginated waypoints with the shipyard on page 2
    And the shipyard at "X1-GZ7-A2" sells ships:
      | ship_type              | name            | price  |
      | SHIP_PROBE             | Probe Satellite | 25000  |
    When I purchase ship type "SHIP_PROBE" using ship "BUYER-1" for player 1
    Then the purchase should succeed
    And the purchasing ship should have navigated to the nearest shipyard "X1-GZ7-A2"
    And the new ship should be at waypoint "X1-GZ7-A2"
    And the player should have 75000 credits remaining

  # Performance: Use waypoint cache instead of API pagination
  Scenario: Auto-discovery uses waypoint cache when system is cached
    Given I have a player with ID 1 and 100000 credits from API
    And I have a ship "BUYER-1" at waypoint "X1-GZ7-CD34"
    And the ship "BUYER-1" is in orbit with 100 fuel
    And waypoints are cached for system "X1-GZ7":
      | waypoint      | x    | y    | traits    |
      | X1-GZ7-AB12   | 10   | 20   | SHIPYARD  |
      | X1-GZ7-CD34   | 5    | 10   |           |
    And a route exists from "X1-GZ7-CD34" to "X1-GZ7-AB12"
    And the shipyard at "X1-GZ7-AB12" sells ships:
      | ship_type              | name            | price  |
      | SHIP_MINING_DRONE      | Mining Drone    | 50000  |
    When I purchase ship type "SHIP_MINING_DRONE" using ship "BUYER-1" for player 1
    Then the purchase should succeed
    And the API list_waypoints method should not have been called
    And the purchasing ship should have navigated to the nearest shipyard "X1-GZ7-AB12"

  # Performance: Auto-sync waypoints on first purchase in uncached system
  Scenario: Auto-discovery auto-syncs waypoints when system is not cached
    Given I have a player with ID 1 and 100000 credits from API
    And I have a ship "BUYER-1" at waypoint "X1-GZ7-CD34"
    And the ship "BUYER-1" is in orbit with 100 fuel
    And the system "X1-GZ7" has waypoints but is not cached:
      | waypoint      | x    | y    | traits    |
      | X1-GZ7-AB12   | 10   | 20   | SHIPYARD  |
      | X1-GZ7-CD34   | 5    | 10   |           |
    And a route exists from "X1-GZ7-CD34" to "X1-GZ7-AB12"
    And the shipyard at "X1-GZ7-AB12" sells ships:
      | ship_type              | name            | price  |
      | SHIP_MINING_DRONE      | Mining Drone    | 50000  |
    When I purchase ship type "SHIP_MINING_DRONE" using ship "BUYER-1" for player 1
    Then the purchase should succeed
    And the system "X1-GZ7" should now be cached
    And the purchasing ship should have navigated to the nearest shipyard "X1-GZ7-AB12"
