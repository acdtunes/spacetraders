Feature: Cargo Operations
  As a bot operator
  I want to manage ship cargo
  So that I can trade resources and manage inventory

  Background:
    Given the SpaceTraders API is mocked
    And a waypoint "X1-TEST-M1" exists at (0, 0) with traits ["MARKETPLACE"]
    And a waypoint "X1-TEST-A2" exists at (100, 0) with traits ["ASTEROID_BASE"]

  Scenario: Sell cargo successfully
    Given a ship "CARGO-SHIP" at "X1-TEST-M1" is DOCKED
    And the ship has 400/400 fuel
    And the ship has cargo: [{"symbol": "IRON_ORE", "units": 10}]
    And the agent has 50000 credits
    When I sell 10 units of "IRON_ORE"
    Then the sale should succeed
    And the ship cargo should have 0 units
    And the agent credits should increase

  Scenario: Sell with insufficient inventory
    Given a ship "CARGO-SHIP" at "X1-TEST-M1" is DOCKED
    And the ship has 400/400 fuel
    And the ship has cargo: [{"symbol": "IRON_ORE", "units": 5}]
    When I sell 10 units of "IRON_ORE"
    Then the sale should fail
    And the ship cargo should have 5 units

  Scenario: Sell item not in cargo
    Given a ship "CARGO-SHIP" at "X1-TEST-M1" is DOCKED
    And the ship has 400/400 fuel
    And the ship has cargo: [{"symbol": "IRON_ORE", "units": 10}]
    When I sell 5 units of "COPPER_ORE"
    Then the sale should fail

  Scenario: Buy cargo successfully
    Given a ship "CARGO-SHIP" at "X1-TEST-M1" is DOCKED
    And the ship has 400/400 fuel
    And the ship has cargo: []
    And the agent has 10000 credits
    When I buy 15 units of "IRON_ORE"
    Then the purchase should succeed
    And the ship cargo should have 15 units
    And the agent credits should decrease

  Scenario: Buy with insufficient cargo capacity
    Given a ship "CARGO-SHIP" at "X1-TEST-M1" is DOCKED
    And the ship has 400/400 fuel
    And the ship has cargo: [{"symbol": "COPPER_ORE", "units": 35}]
    And the agent has 10000 credits
    When I buy 10 units of "IRON_ORE"
    Then the purchase should fail
    And the ship cargo should have 35 units

  Scenario: Buy with insufficient credits
    Given a ship "CARGO-SHIP" at "X1-TEST-M1" is DOCKED
    And the ship has 400/400 fuel
    And the ship has cargo: []
    And the agent has 100 credits
    When I buy 20 units of "IRON_ORE"
    Then the purchase should fail
    And the ship cargo should have 0 units

  Scenario: Jettison cargo into space
    Given a ship "CARGO-SHIP" at "X1-TEST-A2" is IN_ORBIT
    And the ship has 400/400 fuel
    And the ship has cargo: [{"symbol": "ICE_WATER", "units": 20}]
    When I jettison 10 units of "ICE_WATER"
    Then the jettison should succeed
    And the ship cargo should have 10 units

  Scenario: Jettison all cargo
    Given a ship "CARGO-SHIP" at "X1-TEST-A2" is IN_ORBIT
    And the ship has 400/400 fuel
    And the ship has cargo: [{"symbol": "ICE_WATER", "units": 15}]
    When I jettison 15 units of "ICE_WATER"
    Then the jettison should succeed
    And the ship cargo should have 0 units

  Scenario: Jettison item not in cargo
    Given a ship "CARGO-SHIP" at "X1-TEST-A2" is IN_ORBIT
    And the ship has 400/400 fuel
    And the ship has cargo: [{"symbol": "ICE_WATER", "units": 10}]
    When I jettison 5 units of "COPPER_ORE"
    Then the jettison should fail

  Scenario: Get cargo status - empty cargo
    Given a ship "CARGO-SHIP" at "X1-TEST-M1" is IN_ORBIT
    And the ship has 400/400 fuel
    And the ship has cargo: []
    When I get the ship cargo
    Then the cargo should show 0/40 units
    And the cargo should have 0 items

  Scenario: Get cargo status - partial cargo
    Given a ship "CARGO-SHIP" at "X1-TEST-M1" is IN_ORBIT
    And the ship has 400/400 fuel
    And the ship has cargo: [{"symbol": "IRON_ORE", "units": 15}, {"symbol": "COPPER_ORE", "units": 10}]
    When I get the ship cargo
    Then the cargo should show 25/40 units
    And the cargo should have 2 items

  Scenario: Get cargo status - full cargo
    Given a ship "CARGO-SHIP" at "X1-TEST-M1" is IN_ORBIT
    And the ship has 400/400 fuel
    And the ship has cargo: [{"symbol": "IRON_ORE", "units": 40}]
    When I get the ship cargo
    Then the cargo should show 40/40 units
    And the cargo should have 1 items

  Scenario: Multiple cargo operations in sequence
    Given a ship "CARGO-SHIP" at "X1-TEST-M1" is DOCKED
    And the ship has 400/400 fuel
    And the ship has cargo: []
    And the agent has 50000 credits
    When I buy 20 units of "IRON_ORE"
    And I buy 10 units of "COPPER_ORE"
    Then the ship cargo should have 30 units
    When I sell 5 units of "IRON_ORE"
    Then the ship cargo should have 25 units
    When I orbit the ship
    And I jettison 5 units of "COPPER_ORE"
    Then the ship cargo should have 20 units
