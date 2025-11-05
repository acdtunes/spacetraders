Feature: Batch Purchase Ships Command
  As a player
  I want to purchase multiple ships in a batch
  So that I can quickly expand my fleet within budget constraints

  Background:
    Given the batch purchase ships command handler is initialized

  # Happy Path - Purchase multiple ships within budget
  Scenario: Successfully purchase multiple ships within budget
    Given I have a player with ID 1 and 200000 credits from API
    And I have a ship "BUYER-1" at waypoint "X1-GZ7-AB12"
    And the ship "BUYER-1" is docked
    And the API returns a shipyard at "X1-GZ7-AB12" with ships:
      | ship_type              | name            | price  |
      | SHIP_PROBE             | Probe Satellite | 25000  |
    When I batch purchase 5 "SHIP_PROBE" ships using ship "BUYER-1" at shipyard "X1-GZ7-AB12" with max budget 150000 for player 1
    Then the batch purchase should succeed
    And 5 ships should be purchased
    And the player should have 75000 credits remaining
    And all purchased ships should be saved to the repository

  # Budget constraint - Purchase only what budget allows
  Scenario: Purchase only ships that fit within budget
    Given I have a player with ID 1 and 100000 credits from API
    And I have a ship "BUYER-1" at waypoint "X1-GZ7-AB12"
    And the ship "BUYER-1" is docked
    And the API returns a shipyard at "X1-GZ7-AB12" with ships:
      | ship_type              | name            | price  |
      | SHIP_PROBE             | Probe Satellite | 25000  |
    When I batch purchase 10 "SHIP_PROBE" ships using ship "BUYER-1" at shipyard "X1-GZ7-AB12" with max budget 80000 for player 1
    Then the batch purchase should succeed
    And 3 ships should be purchased
    And the player should have 25000 credits remaining

  # Credit constraint - Purchase only what credits allow
  Scenario: Purchase only ships that fit within player credits
    Given I have a player with ID 1 and 60000 credits from API
    And I have a ship "BUYER-1" at waypoint "X1-GZ7-AB12"
    And the ship "BUYER-1" is docked
    And the API returns a shipyard at "X1-GZ7-AB12" with ships:
      | ship_type              | name            | price  |
      | SHIP_PROBE             | Probe Satellite | 25000  |
    When I batch purchase 10 "SHIP_PROBE" ships using ship "BUYER-1" at shipyard "X1-GZ7-AB12" with max budget 150000 for player 1
    Then the batch purchase should succeed
    And 2 ships should be purchased
    And the player should have 10000 credits remaining

  # Both constraints - Budget and credits limit purchases
  Scenario: Purchase limited by both budget and credits
    Given I have a player with ID 1 and 80000 credits from API
    And I have a ship "BUYER-1" at waypoint "X1-GZ7-AB12"
    And the ship "BUYER-1" is docked
    And the API returns a shipyard at "X1-GZ7-AB12" with ships:
      | ship_type              | name            | price  |
      | SHIP_PROBE             | Probe Satellite | 25000  |
    When I batch purchase 10 "SHIP_PROBE" ships using ship "BUYER-1" at shipyard "X1-GZ7-AB12" with max budget 60000 for player 1
    Then the batch purchase should succeed
    And 2 ships should be purchased
    And the player should have 30000 credits remaining

  # Edge case - Purchase 0 ships (quantity = 0)
  Scenario: Cannot purchase zero quantity ships
    Given I have a player with ID 1 and 100000 credits from API
    And I have a ship "BUYER-1" at waypoint "X1-GZ7-AB12"
    And the ship "BUYER-1" is docked
    And the API returns a shipyard at "X1-GZ7-AB12" with ships:
      | ship_type              | name            | price  |
      | SHIP_PROBE             | Probe Satellite | 25000  |
    When I batch purchase 0 "SHIP_PROBE" ships using ship "BUYER-1" at shipyard "X1-GZ7-AB12" with max budget 100000 for player 1
    Then the batch purchase should succeed
    And 0 ships should be purchased
    And the player should still have 100000 credits

  # Edge case - Budget is 0
  Scenario: Cannot purchase with zero budget
    Given I have a player with ID 1 and 100000 credits from API
    And I have a ship "BUYER-1" at waypoint "X1-GZ7-AB12"
    And the ship "BUYER-1" is docked
    And the API returns a shipyard at "X1-GZ7-AB12" with ships:
      | ship_type              | name            | price  |
      | SHIP_PROBE             | Probe Satellite | 25000  |
    When I batch purchase 10 "SHIP_PROBE" ships using ship "BUYER-1" at shipyard "X1-GZ7-AB12" with max budget 0 for player 1
    Then the batch purchase should succeed
    And 0 ships should be purchased
    And the player should still have 100000 credits

  # Error - First purchase fails (ship not found)
  Scenario: First purchase failure prevents all purchases
    Given I have a player with ID 1 and 100000 credits from API
    And no ships exist in the repository
    And the API returns a shipyard at "X1-GZ7-AB12" with ships:
      | ship_type              | name            | price  |
      | SHIP_PROBE             | Probe Satellite | 25000  |
    When I attempt to batch purchase 5 "SHIP_PROBE" ships using ship "BUYER-1" at shipyard "X1-GZ7-AB12" with max budget 150000 for player 1
    Then the batch purchase should fail with ShipNotFoundError
    And 0 ships should be purchased
    And the player should still have 100000 credits

  # Verify orchestration - Calls PurchaseShipCommand multiple times
  Scenario: Batch purchase orchestrates multiple PurchaseShipCommand calls
    Given I have a player with ID 1 and 100000 credits from API
    And I have a ship "BUYER-1" at waypoint "X1-GZ7-AB12"
    And the ship "BUYER-1" is docked
    And the API returns a shipyard at "X1-GZ7-AB12" with ships:
      | ship_type              | name            | price  |
      | SHIP_PROBE             | Probe Satellite | 25000  |
    When I batch purchase 3 "SHIP_PROBE" ships using ship "BUYER-1" at shipyard "X1-GZ7-AB12" with max budget 100000 for player 1
    Then the batch purchase should succeed
    And 3 ships should be purchased
    And the player should have 25000 credits remaining
    And all 3 purchased ships should belong to player 1

  # Verify ship types
  Scenario: Batch purchased ships are all of correct type
    Given I have a player with ID 1 and 100000 credits from API
    And I have a ship "BUYER-1" at waypoint "X1-GZ7-AB12"
    And the ship "BUYER-1" is docked
    And the API returns a shipyard at "X1-GZ7-AB12" with ships:
      | ship_type              | name            | price  |
      | SHIP_MINING_DRONE      | Mining Drone    | 50000  |
    When I batch purchase 2 "SHIP_MINING_DRONE" ships using ship "BUYER-1" at shipyard "X1-GZ7-AB12" with max budget 100000 for player 1
    Then the batch purchase should succeed
    And 2 ships should be purchased
    And the player should have 0 credits remaining
