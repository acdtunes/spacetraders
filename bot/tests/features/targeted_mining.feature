Feature: Targeted Mining with Circuit Breaker
  As a bot operator
  I want to mine specific resources with intelligent cargo management
  So that I can efficiently collect needed materials for contracts

  Background:
    Given the SpaceTraders API is mocked
    And a waypoint "X1-TEST-A1" exists at (50, 0) with traits ["COMMON_METAL_DEPOSITS"]
    And a waypoint "X1-TEST-A2" exists at (100, 0) with traits ["MINERAL_DEPOSITS"]
    And a waypoint "X1-TEST-M1" exists at (0, 0) with traits ["MARKETPLACE"]

  Scenario: Jettison wrong cargo when mining for specific resource
    Given a ship "MINING-1" at "X1-TEST-A1" is IN_ORBIT
    And the ship has 400/400 fuel
    And the ship has 35/40 cargo units with items [{"symbol": "IRON_ORE", "units": 20}, {"symbol": "COPPER_ORE", "units": 15}]
    When I jettison wrong cargo for target resource "ALUMINUM_ORE" with threshold 0.8
    Then the jettison should succeed
    And the ship cargo should have 0 units
    And "IRON_ORE" should be jettisoned
    And "COPPER_ORE" should be jettisoned

  Scenario: No jettison when cargo below threshold
    Given a ship "MINING-1" at "X1-TEST-A1" is IN_ORBIT
    And the ship has 400/400 fuel
    And the ship has 20/40 cargo units with items [{"symbol": "IRON_ORE", "units": 20}]
    When I jettison wrong cargo for target resource "ALUMINUM_ORE" with threshold 0.8
    Then no cargo should be jettisoned
    And the ship cargo should have 20 units

  Scenario: Keep target resource when jettisoning
    Given a ship "MINING-1" at "X1-TEST-A1" is IN_ORBIT
    And the ship has 400/400 fuel
    And the ship has 35/40 cargo units with items [{"symbol": "ALUMINUM_ORE", "units": 10}, {"symbol": "IRON_ORE", "units": 15}, {"symbol": "COPPER_ORE", "units": 10}]
    When I jettison wrong cargo for target resource "ALUMINUM_ORE" with threshold 0.8
    Then the jettison should succeed
    And the ship cargo should have 10 units
    And "ALUMINUM_ORE" should not be jettisoned
    And "IRON_ORE" should be jettisoned
    And "COPPER_ORE" should be jettisoned

  Scenario: Circuit breaker triggers after consecutive failures
    Given a ship "MINING-1" at "X1-TEST-A1" is IN_ORBIT
    And the ship has 400/400 fuel
    And the ship has 0/40 cargo units
    And the asteroid yields only ["IRON_ORE", "COPPER_ORE", "QUARTZ_SAND", "IRON_ORE", "COPPER_ORE", "QUARTZ_SAND", "IRON_ORE", "COPPER_ORE", "QUARTZ_SAND", "IRON_ORE"]
    When I mine for "ALUMINUM_ORE" with max failures 10
    Then mining should fail with circuit breaker
    And the failure reason should contain "circuit breaker"
    And the failure reason should contain "consecutive failures"

  Scenario: Jettison multiple cargo types when above threshold
    Given a ship "MINING-1" at "X1-TEST-A1" is IN_ORBIT
    And the ship has 400/400 fuel
    And the ship has 36/40 cargo units with items [{"symbol": "IRON_ORE", "units": 15}, {"symbol": "COPPER_ORE", "units": 10}, {"symbol": "QUARTZ_SAND", "units": 11}]
    When I jettison wrong cargo for target resource "ALUMINUM_ORE" with threshold 0.8
    Then the jettison should succeed
    And the ship cargo should have 0 units
    And "IRON_ORE" should be jettisoned
    And "COPPER_ORE" should be jettisoned
    And "QUARTZ_SAND" should be jettisoned
