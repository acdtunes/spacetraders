Feature: Ship purchasing edge cases
  As a fleet purchasing coordinator
  I want to handle edge cases in ship purchasing
  So that the system is robust against unusual scenarios

  Background:
    Given a purchasing context with ship type "HEAVY_FREIGHTER"

  Scenario: Cross-system navigation attempt fails
    Given the purchasing ship is in system "X1-TEST"
    And the shipyard is in a different system "X1-OTHER"
    And the shipyard lists the ship for 1000 credits with 5000 credits available
    When the purchase operation attempts cross-system navigation
    Then the purchase should fail with message "Cross-system navigation not supported"
    And no purchase requests should be sent

  Scenario: SmartNavigator route validation fails
    Given the purchasing ship is at "X1-TEST-A1" with insufficient fuel
    And the shipyard "X1-TEST-B1" requires navigation
    And the shipyard lists the ship for 1000 credits with 5000 credits available
    And SmartNavigator route validation will fail due to fuel
    When the purchase operation runs for 1 ships with budget 5000
    Then the purchase should fail with message "Route validation failed"
    And no purchase requests should be sent

  Scenario: Shipyard listing missing purchase price
    Given the purchasing ship is already docked at "X1-TEST-B1"
    And the shipyard listing for "HEAVY_FREIGHTER" exists but has no price
    When the purchase operation runs for 1 ships with budget 5000
    Then the purchase should fail with message "Ship listing missing purchase price"
    And no purchase requests should be sent

  Scenario: Budget exhausted mid-purchase during multi-ship buy
    Given the purchasing ship is already docked at "X1-TEST-B1"
    And the shipyard lists the ship for 1000 credits with 5000 credits available
    When the purchase operation runs for 5 ships with budget 2500
    Then the purchase should complete with 2 new ships
    And the purchase should stop due to budget exhaustion
    And total spent should be 2000 credits

  Scenario: Credits exhausted mid-purchase during multi-ship buy
    Given the purchasing ship is already docked at "X1-TEST-B1"
    And the shipyard lists the ship for 1000 credits with 2500 credits available
    When the purchase operation runs for 5 ships with budget 10000
    Then the purchase should complete with 2 new ships
    And the purchase should stop due to credits exhaustion
    And total spent should be 2000 credits

  Scenario: Partial purchase when only some ships affordable
    Given the purchasing ship is already docked at "X1-TEST-B1"
    And the shipyard lists the ship for 2000 credits with 3500 credits available
    When the purchase operation runs for 3 ships with budget 5000
    Then the purchase should complete with 1 new ships
    And remaining credits should be 1500
    And a warning about partial purchase should be logged

  Scenario: Agent data unavailable prevents purchase
    Given the purchasing ship is already docked at "X1-TEST-B1"
    And the shipyard lists the ship for 1000 credits
    And the agent API endpoint returns no data
    When the purchase operation runs for 1 ships with budget 5000
    Then the purchase should fail with message "Failed to load agent data"
    And no purchase requests should be sent

  Scenario: Shipyard data unavailable prevents purchase
    Given the purchasing ship is already docked at "X1-TEST-B1"
    And the shipyard API endpoint returns no data
    When the purchase operation runs for 1 ships with budget 5000
    Then the purchase should fail with message "Failed to load shipyard data"
    And no purchase requests should be sent

  Scenario: Ship type not available at shipyard
    Given the purchasing ship is already docked at "X1-TEST-B1"
    And the shipyard does not list "HEAVY_FREIGHTER"
    When the purchase operation runs for 1 ships with budget 5000
    Then the purchase should fail with message "Ship type HEAVY_FREIGHTER not available"
    And no purchase requests should be sent
