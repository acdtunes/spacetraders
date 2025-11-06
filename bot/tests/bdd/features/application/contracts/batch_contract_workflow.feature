Feature: Batch Contract Workflow
  As a fleet operator
  I want to automatically negotiate and fulfill multiple contracts
  So that I can generate steady income without manual intervention

  Background:
    Given a registered player with agent "TEST_AGENT"
    And a ship "TEST_AGENT-1" with cargo capacity 50 in system "X1-TEST"
    And the ship is docked at waypoint "X1-TEST-A1"

  Scenario: Fulfill single contract with profitable market price
    Given a market at "X1-TEST-M1" sells "IRON_ORE" for 100 credits per unit
    And the ship has 10000 credits available
    When I execute batch contract workflow for ship "TEST_AGENT-1" with 1 iteration
    Then the workflow should negotiate 1 contract
    And the workflow should accept the contract when profitable
    And the workflow should purchase required goods from cheapest market
    And the workflow should deliver goods to contract destination
    And the workflow should fulfill 1 contract successfully
    And the workflow should return positive net profit

  Scenario: Handle multi-trip delivery when cargo capacity is insufficient
    Given a market at "X1-TEST-M1" sells "IRON_ORE" for 100 credits per unit
    And a contract requires 150 units of "IRON_ORE" delivery
    And the ship has cargo capacity of 50 units
    And the ship has 20000 credits available
    When I execute batch contract workflow for ship "TEST_AGENT-1" with 1 iteration
    Then the workflow should make 3 trips between market and delivery destination
    And the workflow should deliver 150 units total
    And the workflow should fulfill the contract successfully

  Scenario: Always accept contracts even if initially unprofitable
    Given a market at "X1-TEST-M1" sells "IRON_ORE" for 5000 credits per unit
    And the ship has 100000 credits available
    When I execute batch contract workflow for ship "TEST_AGENT-1" with 1 iteration
    Then the workflow should negotiate 1 contract
    And the workflow should accept the contract even if loss exceeds threshold
    And the workflow should fulfill 1 contract successfully

  Scenario: Poll market prices and accept when profitable
    Given a market at "X1-TEST-M1" initially sells "IRON_ORE" for 5000 credits per unit
    And the market price will drop to 100 credits after 1 poll
    And the ship has 10000 credits available
    When I execute batch contract workflow for ship "TEST_AGENT-1" with 1 iteration
    Then the workflow should poll market prices until profitable
    And the workflow should accept the contract after price becomes profitable
    And the workflow should fulfill the contract successfully

  Scenario: Continue to next contract when one fails
    Given a market at "X1-TEST-M1" sells "IRON_ORE" for 100 credits per unit
    And contract 1 will fail during delivery
    And the ship has 20000 credits available
    When I execute batch contract workflow for ship "TEST_AGENT-1" with 3 iterations
    Then the workflow should negotiate 3 contracts
    And the workflow should report 1 failed contract
    And the workflow should fulfill 2 contracts successfully

  Scenario: Process multiple contracts in batch
    Given a market at "X1-TEST-M1" sells "IRON_ORE" for 100 credits per unit
    And the ship has 50000 credits available
    When I execute batch contract workflow for ship "TEST_AGENT-1" with 5 iterations
    Then the workflow should negotiate 5 contracts
    And the workflow should fulfill 5 contracts successfully
    And the workflow should return batch statistics with total profit

  Scenario: Resume existing active contract from API when local database is empty
    Given the local database has no contracts
    And the API has an active contract "API-CONTRACT-123" for the agent
    And a market at "X1-TEST-M1" sells "IRON_ORE" for 100 credits per unit
    And the ship has 10000 credits available
    When I execute batch contract workflow for ship "TEST_AGENT-1" with 1 iteration
    Then the workflow should not negotiate a new contract
    And the workflow should fetch the existing contract "API-CONTRACT-123" from API
    And the workflow should save the contract to local database
    And the workflow should resume the existing contract
    And the workflow should fulfill 1 contract successfully
