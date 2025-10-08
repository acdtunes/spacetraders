Feature: Contract Fulfillment
  As a bot operator
  I want to fulfill contracts efficiently
  So that I can earn credits and complete contract requirements

  Background:
    Given the SpaceTraders API is mocked
    And a waypoint "X1-TEST-HQ" exists at (0, 0) with traits ["HEADQUARTERS"]
    And a waypoint "X1-TEST-MARKET" exists at (50, 0) with traits ["MARKETPLACE"]
    And the system "X1-TEST" has the contract market configured

  Scenario: Contract with cargo already on ship - partial fulfillment
    Given a ship "CARGO-SHIP" at "X1-TEST-MARKET" is DOCKED
    And the ship has 400/400 fuel
    And the ship has cargo: [{"symbol": "IRON_ORE", "units": 15}]
    And the agent has 100000 credits
    And a contract exists requiring 30 units of "IRON_ORE" to "X1-TEST-HQ"
    And 0 units have been fulfilled
    When I fulfill the contract with cargo already on ship
    Then the contract should show 15 units fulfilled
    And the ship cargo should have 0 units
    And the ship should be at "X1-TEST-HQ"

  Scenario: Contract with cargo already on ship - full fulfillment
    Given a ship "CARGO-SHIP" at "X1-TEST-MARKET" is DOCKED
    And the ship has 400/400 fuel
    And the ship has cargo: [{"symbol": "IRON_ORE", "units": 30}]
    And the agent has 100000 credits
    And a contract exists requiring 30 units of "IRON_ORE" to "X1-TEST-HQ"
    And 0 units have been fulfilled
    When I fulfill the contract with cargo already on ship
    Then the contract should be fulfilled
    And the ship cargo should have 0 units
    And the agent should receive completion payment

  Scenario: Multi-trip delivery when cargo capacity is less than total needed
    Given a ship "CARGO-SHIP" at "X1-TEST-MARKET" is DOCKED
    And the ship has 400/400 fuel
    And the ship has cargo: []
    And the ship has 40 cargo capacity
    And the agent has 100000 credits
    And a contract exists requiring 80 units of "IRON_ORE" to "X1-TEST-HQ"
    And 0 units have been fulfilled
    When I fulfill the contract buying from "X1-TEST-MARKET"
    Then the contract should be fulfilled
    And the delivery should have taken 2 trips
    And the ship should be at "X1-TEST-HQ"

  Scenario: Check existing cargo before buying
    Given a ship "CARGO-SHIP" at "X1-TEST-MARKET" is DOCKED
    And the ship has 400/400 fuel
    And the ship has cargo: [{"symbol": "IRON_ORE", "units": 20}]
    And the agent has 100000 credits
    And a contract exists requiring 50 units of "IRON_ORE" to "X1-TEST-HQ"
    And 0 units have been fulfilled
    When I fulfill the contract buying from "X1-TEST-MARKET"
    Then the contract should be fulfilled
    And only 30 units should have been purchased
    And the agent should have spent credits on 30 units only

  Scenario: Full delivery cycle - accept, buy, deliver, fulfill
    Given a ship "CARGO-SHIP" at "X1-TEST-MARKET" is DOCKED
    And the ship has 400/400 fuel
    And the ship has cargo: []
    And the agent has 100000 credits
    And a contract exists requiring 40 units of "IRON_ORE" to "X1-TEST-HQ"
    And the contract is not accepted
    And 0 units have been fulfilled
    When I execute the full contract fulfillment cycle from "X1-TEST-MARKET"
    Then the contract should be accepted first
    And the contract should be fulfilled
    And the agent should receive acceptance payment
    And the agent should receive completion payment
    And the ship should be at "X1-TEST-HQ"

  Scenario: Multiple cargo types - only deliver contract items
    Given a ship "CARGO-SHIP" at "X1-TEST-MARKET" is DOCKED
    And the ship has 400/400 fuel
    And the ship has cargo: [{"symbol": "IRON_ORE", "units": 20}, {"symbol": "COPPER_ORE", "units": 10}]
    And the agent has 100000 credits
    And a contract exists requiring 20 units of "IRON_ORE" to "X1-TEST-HQ"
    And 0 units have been fulfilled
    When I fulfill the contract with cargo already on ship
    Then the contract should be fulfilled
    And the ship cargo should have 10 units
    And the ship should still have "COPPER_ORE" in cargo

  Scenario: Partial fulfillment then complete in second trip
    Given a ship "CARGO-SHIP" at "X1-TEST-MARKET" is DOCKED
    And the ship has 400/400 fuel
    And the ship has cargo: [{"symbol": "IRON_ORE", "units": 25}]
    And the ship has 40 cargo capacity
    And the agent has 100000 credits
    And a contract exists requiring 60 units of "IRON_ORE" to "X1-TEST-HQ"
    And 0 units have been fulfilled
    When I fulfill the contract buying from "X1-TEST-MARKET"
    Then the contract should be fulfilled
    And the delivery should have taken 2 trips
    And the first trip should deliver 25 units from existing cargo
    And the second trip should deliver 35 units from purchase

  Scenario: Contract already partially fulfilled
    Given a ship "CARGO-SHIP" at "X1-TEST-MARKET" is DOCKED
    And the ship has 400/400 fuel
    And the ship has cargo: []
    And the agent has 100000 credits
    And a contract exists requiring 50 units of "IRON_ORE" to "X1-TEST-HQ"
    And 30 units have been fulfilled
    When I fulfill the contract buying from "X1-TEST-MARKET"
    Then the contract should be fulfilled
    And only 20 units should have been purchased
    And only 20 units should have been delivered

  Scenario: Contract already fully fulfilled
    Given a ship "CARGO-SHIP" at "X1-TEST-MARKET" is DOCKED
    And the ship has 400/400 fuel
    And the ship has cargo: []
    And the agent has 100000 credits
    And a contract exists requiring 40 units of "IRON_ORE" to "X1-TEST-HQ"
    And 40 units have been fulfilled
    When I check the contract status
    Then the contract should already be complete
    And no delivery should be needed

  Scenario: Insufficient cargo space requires multiple trips
    Given a ship "CARGO-SHIP" at "X1-TEST-MARKET" is DOCKED
    And the ship has 400/400 fuel
    And the ship has cargo: [{"symbol": "COPPER_ORE", "units": 10}]
    And the ship has 40 cargo capacity
    And the agent has 100000 credits
    And a contract exists requiring 50 units of "IRON_ORE" to "X1-TEST-HQ"
    And 0 units have been fulfilled
    When I fulfill the contract buying from "X1-TEST-MARKET"
    Then the contract should be fulfilled
    And the delivery should have taken 2 trips
    And the first trip should buy 30 units due to cargo space
    And the second trip should buy 20 units

  Scenario: Contract fulfillment with navigation between locations
    Given a ship "CARGO-SHIP" at "X1-TEST-MARKET" is DOCKED
    And the ship has 400/400 fuel
    And the ship has cargo: []
    And the agent has 100000 credits
    And a contract exists requiring 30 units of "IRON_ORE" to "X1-TEST-HQ"
    And 0 units have been fulfilled
    When I fulfill the contract buying from "X1-TEST-MARKET"
    Then the ship should navigate from "X1-TEST-MARKET" to "X1-TEST-HQ"
    And the contract should be fulfilled
    And fuel should be consumed during navigation

  Scenario: Mixed cargo - contract item and other items
    Given a ship "CARGO-SHIP" at "X1-TEST-MARKET" is DOCKED
    And the ship has 400/400 fuel
    And the ship has cargo: [{"symbol": "IRON_ORE", "units": 15}, {"symbol": "COPPER_ORE", "units": 5}, {"symbol": "GOLD_ORE", "units": 3}]
    And the agent has 100000 credits
    And a contract exists requiring 30 units of "IRON_ORE" to "X1-TEST-HQ"
    And 0 units have been fulfilled
    When I fulfill the contract buying from "X1-TEST-MARKET"
    Then the contract should be fulfilled
    And only 15 additional units should have been purchased
    And the ship should still have "COPPER_ORE" in cargo
    And the ship should still have "GOLD_ORE" in cargo
