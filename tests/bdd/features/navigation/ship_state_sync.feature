Feature: Ship State Synchronization Before Navigation
  As an autonomous bot
  I need to sync ship state from API before navigation
  So that I don't attempt operations with stale database state

  Background:
    Given a player exists with ID 1 and token "test-token"
    And a ship "SHIP-1" exists for player 1
    And the SpaceTraders API is available

  Scenario: Ship state synced before route planning
    Given the database shows ship "SHIP-1" with status "IN_TRANSIT"
    But the API shows ship "SHIP-1" with status "IN_ORBIT"
    When I send a navigation command for ship "SHIP-1"
    Then the ship state should be synced from API before planning
    And the database should be updated to status "IN_ORBIT"
    And navigation should proceed with correct state

  Scenario: Stale fuel data corrected before navigation
    Given the database shows ship "SHIP-1" with 100 fuel
    But the API shows ship "SHIP-1" with 250 fuel
    When I send a navigation command for ship "SHIP-1"
    Then the ship fuel should be synced to 250 before planning
    And the route should be calculated with correct fuel amount

  Scenario: Navigation prevented if ship in transit
    Given the API shows ship "SHIP-1" with status "IN_TRANSIT"
    When I send a navigation command for ship "SHIP-1"
    Then the sync should update database to "IN_TRANSIT"
    And the navigation should be rejected with error
    And the error should mention ship is in transit

  Scenario: Location sync prevents routing errors
    Given the database shows ship "SHIP-1" at "X1-TEST-A1"
    But the API shows ship "SHIP-1" at "X1-TEST-B2"
    When I send a navigation command for ship "SHIP-1" to "X1-TEST-C3"
    Then the ship location should be synced to "X1-TEST-B2"
    And the route should start from "X1-TEST-B2"
    And not from the stale location "X1-TEST-A1"

  Scenario: Cargo capacity sync affects route validation
    Given the database shows ship "SHIP-1" with 10/40 cargo
    But the API shows ship "SHIP-1" with 35/40 cargo
    When I send a navigation command for ship "SHIP-1"
    Then the cargo units should be synced to 35
    And route planning should use accurate cargo data

  Scenario: Sync failure is handled gracefully
    Given the SpaceTraders API returns error 500
    When I send a navigation command for ship "SHIP-1"
    Then the sync should fail gracefully
    And navigation should be aborted
    And the error should be reported to user

  Scenario: Sync timestamp recorded in database
    Given ship "SHIP-1" exists without recent sync
    When I send a navigation command for ship "SHIP-1"
    Then the ship should be synced from API
    And the synced_at timestamp should be updated in database
    And the timestamp should be within last 5 seconds
