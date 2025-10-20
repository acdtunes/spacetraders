Feature: Ship cargo operations
  As a ship controller
  I want to manage ship cargo
  So that ships can trade and mine efficiently

  Background:
    Given a mock API client
    And a ship "TEST-SHIP" exists at waypoint "X1-TEST-A1"

  Scenario: Sell all cargo at market
    Given the ship "TEST-SHIP" is DOCKED at "X1-TEST-A1"
    And waypoint "X1-TEST-A1" has an EXCHANGE market
    And the ship has 15 units of "IRON_ORE" in cargo
    And the ship has 10 units of "COPPER_ORE" in cargo
    When I sell all cargo
    Then all cargo should be sold
    And total revenue should be calculated
    And cargo should be empty

  Scenario: Sell specific cargo item
    Given the ship "TEST-SHIP" is DOCKED at "X1-TEST-A1"
    And waypoint "X1-TEST-A1" has an EXCHANGE market
    And the ship has 20 units of "IRON_ORE" in cargo
    When I sell 15 units of "IRON_ORE"
    Then 15 units of "IRON_ORE" should be sold
    And revenue should be for 15 units
    And cargo should contain 5 units of "IRON_ORE"

  Scenario: Jettison cargo item
    Given the ship "TEST-SHIP" is IN_ORBIT at "X1-TEST-A1"
    And the ship has 10 units of "COPPER_ORE" in cargo
    When I jettison 10 units of "COPPER_ORE"
    Then 10 units of "COPPER_ORE" should be jettisoned
    And cargo should not contain "COPPER_ORE"

  Scenario: Extract resources from asteroid
    Given the ship "TEST-SHIP" is IN_ORBIT at "X1-TEST-A1"
    And waypoint "X1-TEST-A1" is an ENGINEERED_ASTEROID
    And the ship has cargo space available
    When I extract resources
    Then extraction should succeed
    And cargo should contain the extracted resource
    And a cooldown should be active

  Scenario: Extract returns cooldown information
    Given the ship "TEST-SHIP" is IN_ORBIT at "X1-TEST-A1"
    And waypoint "X1-TEST-A1" is an ENGINEERED_ASTEROID
    And the ship has cargo space available
    When I extract resources
    Then extraction should succeed
    And a cooldown should be active
    And cargo should contain the extracted resource

  Scenario: Buy cargo with sufficient capacity
    Given the ship "TEST-SHIP" is DOCKED at "X1-TEST-A1"
    And waypoint "X1-TEST-A1" has an EXCHANGE market
    And the ship has 0/40 cargo units used
    And the ship has sufficient credits to buy 10 units
    When I buy 10 units of "IRON_ORE"
    Then 10 units of "IRON_ORE" should be purchased
    And cargo should contain 10 units of "IRON_ORE"
    And credits should be deducted

  Scenario: Buy cargo with insufficient capacity fails
    Given the ship "TEST-SHIP" is DOCKED at "X1-TEST-A1"
    And waypoint "X1-TEST-A1" has an EXCHANGE market
    And the ship has 35/40 cargo units used
    And the ship has sufficient credits to buy 10 units
    When I buy 10 units of "IRON_ORE"
    Then purchase should fail due to insufficient capacity
    And cargo should remain at 35/40

  Scenario: Cargo status query returns accurate data
    Given the ship "TEST-SHIP" is IN_ORBIT at "X1-TEST-A1"
    And the ship has 15 units of "IRON_ORE" in cargo
    And the ship has 10 units of "COPPER_ORE" in cargo
    When I query cargo status
    Then cargo should show 25/40 units used
    And cargo inventory should list "IRON_ORE" with 15 units
    And cargo inventory should list "COPPER_ORE" with 10 units
