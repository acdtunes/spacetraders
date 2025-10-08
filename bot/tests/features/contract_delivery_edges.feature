Feature: Contract delivery edge handling
  Scenario: Docking failure aborts the contract
    Given a contract delivery context
    And the purchasing ship fails to dock at the shipyard
    When the contract operation runs
    Then the contract operation should exit with status 1
    And a critical contract error should be logged

  Scenario: Delivery retries after transient error
    Given a contract delivery context
    And the delivery API fails once with code 4502
    When the contract operation runs
    Then the contract operation should exit with status 0
    And the delivery is eventually recorded

  Scenario: Delivering existing cargo completes the contract
    Given a contract delivery context
    And the ship starts at "X1-TEST-A1" with 10 units of cargo
    And the contract requires 10 units of IRON_ORE to "X1-TEST-B1"
    When the contract operation runs
    Then the contract operation should exit with status 0
    And the delivery record should contain "10"
    And the navigator should visit "X1-TEST-B1"
    And the captain log should include "OPERATION_COMPLETED"

  Scenario: Market unavailable triggers failure after retries
    Given a contract delivery context
    And no markets offer IRON_ORE
    And the contract requires 10 units of IRON_ORE to "X1-TEST-B1"
    When the contract operation runs
    Then the contract operation should exit with status 1
    And the sleep function should be called 12 times
    And a critical contract error should be logged

  Scenario: Contract acceptance respects transaction limits
    Given a contract delivery context
    And the contract requires 30 units of IRON_ORE to "X1-TEST-B1"
    And the contract is not accepted and has a transaction limit
    When the contract operation runs
    Then the contract operation should exit with status 0
    And the contract should be accepted
    And the ship should plan purchases from "X1-TEST-M1"
    And the navigator should visit "X1-TEST-M1"
    And the captain log should include "OPERATION_COMPLETED"
    And the first purchase attempt should exceed 20 units

  Scenario: Missing contract aborts the run
    Given a contract delivery context
    And the contract requires 10 units of IRON_ORE to "X1-TEST-B1"
    And the API returns no contract
    When the contract operation runs
    Then the contract operation should exit with status 1
    And stdout should contain "Failed to get contract"

  Scenario: Already fulfilled contract exits immediately
    Given a contract delivery context
    And the contract requires 10 units of IRON_ORE to "X1-TEST-B1"
    And the contract is already fulfilled
    When the contract operation runs
    Then the contract operation should exit with status 0
    And stdout should contain "already fulfilled"

  Scenario: Missing ship status raises critical error
    Given a contract delivery context
    And the contract requires 10 units of IRON_ORE to "X1-TEST-B1"
    And the ship status cannot be retrieved
    When the contract operation runs
    Then the contract operation should exit with status 1
    And a critical contract error should be logged

  Scenario: Full delivery cycle handles multiple trips and purchases
    Given a contract delivery context
    And the contract requires multi-step deliveries
    When the contract operation runs
    Then the contract operation should exit with status 0
    And the delivery record should contain "5,1"
    And the captain log should include "OPERATION_COMPLETED"

  Scenario: Database helpers locate lowest price listings
    Given a market database with primary and fallback listings
    When I search for market listings for "IRON"
    Then the exact listing should be "X1-M1,100,ABUNDANT"
    And the fallback listing should be "X1-M1,120,COMMON"
