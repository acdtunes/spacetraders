Feature: Shipyard Operations
  As a SpaceTraders agent
  I want to interact with shipyards to purchase ships
  So that I can expand my fleet

  Background:
    Given a player "TEST-AGENT" exists with 500000 credits
    And a ship "TEST-AGENT-1" exists for player "TEST-AGENT" at waypoint "X1-SYSTEM-A1"
    And the ship "TEST-AGENT-1" is docked
    And a waypoint "X1-SYSTEM-S1" exists with a shipyard at coordinates (100, 100)
    And a waypoint "X1-SYSTEM-A1" exists at coordinates (0, 0)

  # ============================================================================
  # GetShipyardListingsQuery Tests
  # ============================================================================

  Scenario: Query shipyard listings fails when player not found
    When I query shipyard listings for "X1-SYSTEM-S1" as "NONEXISTENT-PLAYER"
    Then the query should fail with error "player not found"

  Scenario: Query shipyard listings fails when API returns error
    Given the API will return an error when getting shipyard "X1-SYSTEM-S1"
    When I query shipyard listings for "X1-SYSTEM-S1" as "TEST-AGENT"
    Then the query should fail

  # ============================================================================
  # PurchaseShipCommand Tests
  # ============================================================================

  Scenario: Purchase ship when already docked at shipyard
    Given the shipyard at "X1-SYSTEM-S1" has the following ships:
      | Type              | Price  |
      | SHIP_MINING_DRONE | 50000  |
    And the ship "TEST-AGENT-1" is at waypoint "X1-SYSTEM-S1"
    And the ship "TEST-AGENT-1" is docked
    When I purchase a "SHIP_MINING_DRONE" ship using "TEST-AGENT-1" at "X1-SYSTEM-S1" as "TEST-AGENT"
    Then the purchase should succeed
    And the player "TEST-AGENT" should have 450000 credits remaining
    And a new ship should be created for player "TEST-AGENT"
    And the new ship should be at waypoint "X1-SYSTEM-S1"
    And the new ship should be docked

  Scenario: Purchase ship when at different location (requires navigation)
    Given the shipyard at "X1-SYSTEM-S1" has the following ships:
      | Type              | Price  |
      | SHIP_MINING_DRONE | 50000  |
    And the ship "TEST-AGENT-1" is at waypoint "X1-SYSTEM-A1"
    And navigation will succeed from "X1-SYSTEM-A1" to "X1-SYSTEM-S1"
    When I purchase a "SHIP_MINING_DRONE" ship using "TEST-AGENT-1" at "X1-SYSTEM-S1" as "TEST-AGENT"
    Then the purchase should succeed
    And the mediator should have been called to navigate from "X1-SYSTEM-A1" to "X1-SYSTEM-S1"
    And the player "TEST-AGENT" should have 450000 credits remaining

  Scenario: Purchase ship when in orbit at shipyard (requires docking)
    Given the shipyard at "X1-SYSTEM-S1" has the following ships:
      | Type              | Price  |
      | SHIP_MINING_DRONE | 50000  |
    And the ship "TEST-AGENT-1" is at waypoint "X1-SYSTEM-S1"
    And the ship "TEST-AGENT-1" is in orbit
    When I purchase a "SHIP_MINING_DRONE" ship using "TEST-AGENT-1" at "X1-SYSTEM-S1" as "TEST-AGENT"
    Then the purchase should succeed
    And the mediator should have been called to dock the ship
    And the player "TEST-AGENT" should have 450000 credits remaining

  Scenario: Purchase ship with auto-discovered nearest shipyard
    Given the shipyard at "X1-SYSTEM-S1" has the following ships:
      | Type              | Price  |
      | SHIP_MINING_DRONE | 50000  |
    And waypoint "X1-SYSTEM-S1" is the nearest shipyard to "X1-SYSTEM-A1"
    And navigation will succeed from "X1-SYSTEM-A1" to "X1-SYSTEM-S1"
    When I purchase a "SHIP_MINING_DRONE" ship using "TEST-AGENT-1" without specifying shipyard as "TEST-AGENT"
    Then the purchase should succeed
    And the shipyard "X1-SYSTEM-S1" should have been auto-discovered
    And the player "TEST-AGENT" should have 450000 credits remaining

  Scenario: Purchase fails when insufficient credits
    Given the shipyard at "X1-SYSTEM-S1" has the following ships:
      | Type              | Price   |
      | SHIP_MINING_DRONE | 600000  |
    And the ship "TEST-AGENT-1" is at waypoint "X1-SYSTEM-S1"
    And the ship "TEST-AGENT-1" is docked
    When I purchase a "SHIP_MINING_DRONE" ship using "TEST-AGENT-1" at "X1-SYSTEM-S1" as "TEST-AGENT"
    Then the purchase should fail with error "insufficient credits"

  Scenario: Purchase fails when ship type not available
    Given the shipyard at "X1-SYSTEM-S1" has the following ships:
      | Type              | Price  |
      | SHIP_PROBE        | 25000  |
    And the ship "TEST-AGENT-1" is at waypoint "X1-SYSTEM-S1"
    And the ship "TEST-AGENT-1" is docked
    When I purchase a "SHIP_MINING_DRONE" ship using "TEST-AGENT-1" at "X1-SYSTEM-S1" as "TEST-AGENT"
    Then the purchase should fail with error "not available"

  Scenario: Purchase fails when no shipyards in system
    Given there are no shipyards in system "X1-SYSTEM"
    When I purchase a "SHIP_MINING_DRONE" ship using "TEST-AGENT-1" without specifying shipyard as "TEST-AGENT"
    Then the purchase should fail with error "no shipyards"

  Scenario: Purchase fails when purchasing ship not found
    Given the shipyard at "X1-SYSTEM-S1" has the following ships:
      | Type              | Price  |
      | SHIP_MINING_DRONE | 50000  |
    When I purchase a "SHIP_MINING_DRONE" ship using "NONEXISTENT-SHIP" at "X1-SYSTEM-S1" as "TEST-AGENT"
    Then the purchase should fail with error "ship not found"

  Scenario: Purchase fails when API purchase fails
    Given the shipyard at "X1-SYSTEM-S1" has the following ships:
      | Type              | Price  |
      | SHIP_MINING_DRONE | 50000  |
    And the ship "TEST-AGENT-1" is at waypoint "X1-SYSTEM-S1"
    And the ship "TEST-AGENT-1" is docked
    And the API will return an error when purchasing a ship
    When I purchase a "SHIP_MINING_DRONE" ship using "TEST-AGENT-1" at "X1-SYSTEM-S1" as "TEST-AGENT"
    Then the purchase should fail

  Scenario: Purchase fails when navigation fails
    Given the shipyard at "X1-SYSTEM-S1" has the following ships:
      | Type              | Price  |
      | SHIP_MINING_DRONE | 50000  |
    And the ship "TEST-AGENT-1" is at waypoint "X1-SYSTEM-A1"
    And navigation will fail from "X1-SYSTEM-A1" to "X1-SYSTEM-S1"
    When I purchase a "SHIP_MINING_DRONE" ship using "TEST-AGENT-1" at "X1-SYSTEM-S1" as "TEST-AGENT"
    Then the purchase should fail

  # ============================================================================
  # BatchPurchaseShipsCommand Tests
  # ============================================================================

  Scenario: Batch purchase multiple ships successfully
    Given the shipyard at "X1-SYSTEM-S1" has the following ships:
      | Type              | Price  |
      | SHIP_MINING_DRONE | 50000  |
    And the ship "TEST-AGENT-1" is at waypoint "X1-SYSTEM-S1"
    And the ship "TEST-AGENT-1" is docked
    When I batch purchase 3 "SHIP_MINING_DRONE" ships using "TEST-AGENT-1" at "X1-SYSTEM-S1" as "TEST-AGENT"
    Then the batch purchase should succeed
    And 3 ships should have been purchased
    And the player "TEST-AGENT" should have 350000 credits remaining
    And all purchased ships should be at waypoint "X1-SYSTEM-S1"

  Scenario: Batch purchase limited by quantity
    Given the shipyard at "X1-SYSTEM-S1" has the following ships:
      | Type              | Price  |
      | SHIP_PROBE        | 25000  |
    And the ship "TEST-AGENT-1" is at waypoint "X1-SYSTEM-S1"
    And the ship "TEST-AGENT-1" is docked
    When I batch purchase 5 "SHIP_PROBE" ships with max budget 200000 using "TEST-AGENT-1" at "X1-SYSTEM-S1" as "TEST-AGENT"
    Then the batch purchase should succeed
    And 5 ships should have been purchased
    And the player "TEST-AGENT" should have 375000 credits remaining

  Scenario: Batch purchase limited by budget
    Given the shipyard at "X1-SYSTEM-S1" has the following ships:
      | Type              | Price  |
      | SHIP_MINING_DRONE | 50000  |
    And the ship "TEST-AGENT-1" is at waypoint "X1-SYSTEM-S1"
    And the ship "TEST-AGENT-1" is docked
    When I batch purchase 10 "SHIP_MINING_DRONE" ships with max budget 125000 using "TEST-AGENT-1" at "X1-SYSTEM-S1" as "TEST-AGENT"
    Then the batch purchase should succeed
    And 2 ships should have been purchased
    And the player "TEST-AGENT" should have 400000 credits remaining

  Scenario: Batch purchase limited by player credits
    Given the shipyard at "X1-SYSTEM-S1" has the following ships:
      | Type              | Price  |
      | SHIP_MINING_DRONE | 50000  |
    And the ship "TEST-AGENT-1" is at waypoint "X1-SYSTEM-S1"
    And the ship "TEST-AGENT-1" is docked
    When I batch purchase 20 "SHIP_MINING_DRONE" ships using "TEST-AGENT-1" at "X1-SYSTEM-S1" as "TEST-AGENT"
    Then the batch purchase should succeed
    And 10 ships should have been purchased
    And the player "TEST-AGENT" should have 0 credits remaining

  Scenario: Batch purchase with partial success (runs out of credits mid-batch)
    Given the player "TEST-AGENT" has 125000 credits
    And the shipyard at "X1-SYSTEM-S1" has the following ships:
      | Type              | Price  |
      | SHIP_MINING_DRONE | 50000  |
    And the ship "TEST-AGENT-1" is at waypoint "X1-SYSTEM-S1"
    And the ship "TEST-AGENT-1" is docked
    When I batch purchase 5 "SHIP_MINING_DRONE" ships using "TEST-AGENT-1" at "X1-SYSTEM-S1" as "TEST-AGENT"
    Then the batch purchase should succeed with partial results
    And 2 ships should have been purchased
    And the player "TEST-AGENT" should have 25000 credits remaining

  Scenario: Batch purchase with zero quantity returns empty result
    Given the shipyard at "X1-SYSTEM-S1" has the following ships:
      | Type              | Price  |
      | SHIP_MINING_DRONE | 50000  |
    When I batch purchase 0 "SHIP_MINING_DRONE" ships using "TEST-AGENT-1" at "X1-SYSTEM-S1" as "TEST-AGENT"
    Then the batch purchase should succeed
    And 0 ships should have been purchased
    And the player "TEST-AGENT" should have 500000 credits remaining

  Scenario: Batch purchase fails when first purchase fails
    Given the shipyard at "X1-SYSTEM-S1" has the following ships:
      | Type              | Price  |
      | SHIP_MINING_DRONE | 50000  |
    And the ship "TEST-AGENT-1" is at waypoint "X1-SYSTEM-A1"
    And navigation will fail from "X1-SYSTEM-A1" to "X1-SYSTEM-S1"
    When I batch purchase 3 "SHIP_MINING_DRONE" ships using "TEST-AGENT-1" at "X1-SYSTEM-S1" as "TEST-AGENT"
    Then the batch purchase should fail
    And 0 ships should have been purchased

  Scenario: Batch purchase fails when ship type not available
    Given the shipyard at "X1-SYSTEM-S1" has the following ships:
      | Type              | Price  |
      | SHIP_PROBE        | 25000  |
    And the ship "TEST-AGENT-1" is at waypoint "X1-SYSTEM-S1"
    And the ship "TEST-AGENT-1" is docked
    When I batch purchase 3 "SHIP_MINING_DRONE" ships using "TEST-AGENT-1" at "X1-SYSTEM-S1" as "TEST-AGENT"
    Then the batch purchase should fail with error "not available"
