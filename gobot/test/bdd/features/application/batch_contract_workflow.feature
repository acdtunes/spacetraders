Feature: Batch Contract Workflow
  As a space trader
  I want to execute automated contract workflows in batches
  So that I can efficiently complete multiple contracts with minimal manual intervention

  Background:
    Given a mediator is configured with all contract handlers
    And a player with ID 1 exists with agent "TEST-AGENT"
    And a system "X1-TEST" exists with multiple waypoints

  Scenario: Single contract workflow end-to-end
    Given a ship "SHIP-1" owned by player 1 at waypoint "X1-TEST-A1"
    And the ship "SHIP-1" has 100 cargo capacity
    And no active contracts exist for player 1
    And a market at "X1-TEST-B1" sells "IRON_ORE" for 50 credits per unit
    And waypoint "X1-TEST-C1" exists as a delivery destination
    When I execute batch contract workflow with:
      | ship_symbol | SHIP-1 |
      | iterations  | 1      |
      | player_id   | 1      |
    Then the workflow result should show:
      | negotiated  | 1 |
      | accepted    | 1 |
      | fulfilled   | 1 |
      | failed      | 0 |
    And no errors should be recorded

  Scenario: Resume existing active contract (idempotency)
    Given a ship "SHIP-1" owned by player 1 at waypoint "X1-TEST-A1"
    And the ship "SHIP-1" has 100 cargo capacity
    And an existing active contract "CONTRACT-1" for player 1 requiring:
      | trade_symbol       | IRON_ORE    |
      | units_required     | 100         |
      | units_fulfilled    | 0           |
      | destination_symbol | X1-TEST-C1  |
    And a market at "X1-TEST-B1" sells "IRON_ORE" for 50 credits per unit
    When I execute batch contract workflow with:
      | ship_symbol | SHIP-1 |
      | iterations  | 1      |
      | player_id   | 1      |
    Then the workflow result should show:
      | negotiated  | 0 |
      | accepted    | 1 |
      | fulfilled   | 1 |
      | failed      | 0 |
    And the existing contract "CONTRACT-1" should be fulfilled

  Scenario: Multi-trip delivery when cargo capacity is less than required units
    Given a ship "SHIP-1" owned by player 1 at waypoint "X1-TEST-A1"
    And the ship "SHIP-1" has 100 cargo capacity
    And no active contracts exist for player 1
    And a contract requiring 200 units of "IRON_ORE" to be delivered to "X1-TEST-C1"
    And a market at "X1-TEST-B1" sells "IRON_ORE" for 50 credits per unit
    When I execute batch contract workflow with:
      | ship_symbol | SHIP-1 |
      | iterations  | 1      |
      | player_id   | 1      |
    Then the workflow result should show:
      | negotiated  | 1 |
      | accepted    | 1 |
      | fulfilled   | 1 |
      | failed      | 0 |
    And the workflow should have executed 2 trips
    And all 200 units should be delivered

  Scenario: Jettison wrong cargo before purchase when cargo is full
    Given a ship "SHIP-1" owned by player 1 at waypoint "X1-TEST-A1"
    And the ship "SHIP-1" has 100 cargo capacity
    And the ship "SHIP-1" has cargo:
      | symbol     | units |
      | COPPER_ORE | 50    |
    And no active contracts exist for player 1
    And a contract requiring 100 units of "IRON_ORE" to be delivered to "X1-TEST-C1"
    And a market at "X1-TEST-B1" sells "IRON_ORE" for 50 credits per unit
    When I execute batch contract workflow with:
      | ship_symbol | SHIP-1 |
      | iterations  | 1      |
      | player_id   | 1      |
    Then the workflow result should show:
      | negotiated  | 1 |
      | accepted    | 1 |
      | fulfilled   | 1 |
      | failed      | 0 |
    And "COPPER_ORE" should have been jettisoned
    And "IRON_ORE" should have been purchased

  Scenario: Jettison wrong cargo when ship has mixed cargo and needs more space
    Given a ship "SHIP-1" owned by player 1 at waypoint "X1-TEST-A1"
    And the ship "SHIP-1" has 100 cargo capacity
    And the ship "SHIP-1" has cargo:
      | symbol     | units |
      | IRON_ORE   | 30    |
      | COPPER_ORE | 40    |
    And no active contracts exist for player 1
    And a contract requiring 100 units of "IRON_ORE" to be delivered to "X1-TEST-C1"
    And a market at "X1-TEST-B1" sells "IRON_ORE" for 50 credits per unit
    When I execute batch contract workflow with:
      | ship_symbol | SHIP-1 |
      | iterations  | 1      |
      | player_id   | 1      |
    Then the workflow result should show:
      | negotiated  | 1 |
      | accepted    | 1 |
      | fulfilled   | 1 |
      | failed      | 0 |
    And "COPPER_ORE" should have been jettisoned
    And 70 units of "IRON_ORE" should have been purchased

  Scenario: Transaction splitting when market has transaction limits
    Given a ship "SHIP-1" owned by player 1 at waypoint "X1-TEST-A1"
    And the ship "SHIP-1" has 100 cargo capacity
    And no active contracts exist for player 1
    And a contract requiring 100 units of "IRON_ORE" to be delivered to "X1-TEST-C1"
    And a market at "X1-TEST-B1" sells "IRON_ORE" for 50 credits per unit with transaction limit 30
    When I execute batch contract workflow with:
      | ship_symbol | SHIP-1 |
      | iterations  | 1      |
      | player_id   | 1      |
    Then the workflow result should show:
      | negotiated  | 1 |
      | accepted    | 1 |
      | fulfilled   | 1 |
      | failed      | 0 |
    And purchases should have been split into 4 transactions

  Scenario: Multiple iterations with profit tracking
    Given a ship "SHIP-1" owned by player 1 at waypoint "X1-TEST-A1"
    And the ship "SHIP-1" has 100 cargo capacity
    And no active contracts exist for player 1
    And markets sell "IRON_ORE" at various prices
    When I execute batch contract workflow with:
      | ship_symbol | SHIP-1 |
      | iterations  | 3      |
      | player_id   | 1      |
    Then the workflow result should show:
      | negotiated  | 3 |
      | accepted    | 3 |
      | fulfilled   | 3 |
      | failed      | 0 |
    And total profit should be calculated across all contracts

  Scenario: Graceful error handling - continue to next iteration on failure
    Given a ship "SHIP-1" owned by player 1 at waypoint "X1-TEST-A1"
    And the ship "SHIP-1" has 100 cargo capacity
    And no active contracts exist for player 1
    And the second contract negotiation will fail
    When I execute batch contract workflow with:
      | ship_symbol | SHIP-1 |
      | iterations  | 3      |
      | player_id   | 1      |
    Then the workflow result should show:
      | negotiated  | 2 |
      | accepted    | 2 |
      | fulfilled   | 2 |
      | failed      | 1 |
    And 1 error should be recorded
    And the workflow should complete all 3 iterations

  Scenario: Unprofitable contract still accepted (matches Python behavior)
    Given a ship "SHIP-1" owned by player 1 at waypoint "X1-TEST-A1"
    And the ship "SHIP-1" has 100 cargo capacity
    And no active contracts exist for player 1
    And a contract with net profit of -3000 credits
    And a market sells the required goods
    When I execute batch contract workflow with:
      | ship_symbol | SHIP-1 |
      | iterations  | 1      |
      | player_id   | 1      |
    Then the workflow result should show:
      | negotiated  | 1 |
      | accepted    | 1 |
      | fulfilled   | 1 |
      | failed      | 0 |
    And a warning should be logged about unprofitability
