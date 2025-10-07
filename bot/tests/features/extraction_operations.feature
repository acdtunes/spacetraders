Feature: Resource Extraction Operations
  As a mining bot operator
  I want to extract resources from asteroids
  So that I can gather materials for contracts and trading

  Background:
    Given the SpaceTraders API is mocked
    And a waypoint "X1-TEST-A1" exists at (0, 0) with traits ["MARKETPLACE"]
    And a waypoint "X1-TEST-M1" exists at (50, 0) with traits ["COMMON_METAL_DEPOSITS"]
    And a waypoint "X1-TEST-M2" exists at (100, 0) with traits ["STRIPPED"]

  Scenario: Successful resource extraction at asteroid
    Given a ship "MINER-1" at "X1-TEST-M1" is IN_ORBIT
    And the ship has 400/400 fuel
    And the ship has 0/40 cargo units
    When I extract resources
    Then the extraction should succeed
    And the extracted resource should be "IRON_ORE"
    And the cargo should contain extracted resources
    And a cooldown should be active

  Scenario: Extraction with cooldown active
    Given a ship "MINER-1" at "X1-TEST-M1" is IN_ORBIT
    And the ship has 400/400 fuel
    And the ship has 0/40 cargo units
    And the ship has an active cooldown of 80 seconds
    When I extract resources
    Then the extraction should fail
    And the error should indicate cooldown active

  Scenario: Extraction with full cargo
    Given a ship "MINER-1" at "X1-TEST-M1" is IN_ORBIT
    And the ship has 400/400 fuel
    And the ship has 40/40 cargo units with items [{"symbol": "IRON_ORE", "units": 40}]
    When I extract resources
    Then the extraction should fail
    And the error should indicate cargo full

  Scenario: Extraction at wrong location type
    Given a ship "MINER-1" at "X1-TEST-A1" is IN_ORBIT
    And the ship has 400/400 fuel
    And the ship has 0/40 cargo units
    When I extract resources
    Then the extraction should fail
    And the error should indicate invalid location

  Scenario: Extraction while docked
    Given a ship "MINER-1" at "X1-TEST-M1" is DOCKED
    And the ship has 400/400 fuel
    And the ship has 0/40 cargo units
    When I extract resources
    Then the extraction should fail
    And the error should indicate must be in orbit

  Scenario: Multiple extractions with cooldown tracking
    Given a ship "MINER-1" at "X1-TEST-M1" is IN_ORBIT
    And the ship has 400/400 fuel
    And the ship has 0/40 cargo units
    When I extract resources
    Then the extraction should succeed
    And a cooldown should be active
    When I check the cooldown status
    Then the cooldown remaining should be 80 seconds

  Scenario: Get cooldown status when no cooldown
    Given a ship "MINER-1" at "X1-TEST-M1" is IN_ORBIT
    And the ship has 400/400 fuel
    When I check the cooldown status
    Then the cooldown remaining should be 0 seconds

  Scenario: Extraction yields random resources
    Given a ship "MINER-1" at "X1-TEST-M1" is IN_ORBIT
    And the ship has 400/400 fuel
    And the ship has 0/40 cargo units
    When I extract resources
    Then the extraction should succeed
    And the extracted units should be between 1 and 7

  Scenario: Extraction at stripped asteroid yields poor results
    Given a ship "MINER-1" at "X1-TEST-M2" is IN_ORBIT
    And the ship has 400/400 fuel
    And the ship has 0/40 cargo units
    When I extract resources
    Then the extraction should succeed
    And the extracted resource should be "ICE_WATER"
    And the extracted units should be 1
