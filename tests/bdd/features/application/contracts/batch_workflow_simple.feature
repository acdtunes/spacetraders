Feature: Batch Contract Workflow - Unit Tests
  As a fleet operator
  I want to automatically negotiate and fulfill multiple contracts
  So that I can generate steady income without manual intervention

  Background:
    Given a player with agent "TEST_AGENT"
    And a ship "TEST_AGENT-1" in system "X1-TEST"

  Scenario: Execute single iteration workflow successfully
    Given contract negotiation returns a profitable contract
    And market data is available for required goods
    And ship has sufficient cargo capacity
    When I execute batch workflow with 1 iteration
    Then 1 contract should be negotiated
    And 1 contract should be accepted
    And 1 contract should be fulfilled
    And the result should show positive profit

  Scenario: Skip unprofitable contract and continue
    Given contract negotiation returns an unprofitable contract
    When I execute batch workflow with 1 iteration
    Then 1 contract should be negotiated
    And 0 contracts should be accepted
    And 0 contracts should be fulfilled
    And the result should show zero profit

  Scenario: Execute multiple iterations
    Given contract negotiation returns profitable contracts
    And market data is available for required goods
    When I execute batch workflow with 3 iterations
    Then 3 contracts should be negotiated
    And 3 contracts should be accepted
    And 3 contracts should be fulfilled

  Scenario: Continue on failure
    Given contract 1 will succeed
    And contract 2 will fail during fulfillment
    And contract 3 will succeed
    When I execute batch workflow with 3 iterations
    Then 3 contracts should be negotiated
    And 2 contracts should be fulfilled
    And 1 contract should fail

  Scenario: Resume from existing accepted contract (idempotent)
    Given an active contract already exists and is accepted
    And market data is available for required goods
    When I execute batch workflow with 1 iteration
    Then 0 contracts should be negotiated
    And 0 contracts should be accepted
    And 1 contract should be fulfilled
    And the result should show positive profit

  Scenario: Use actual ship cargo capacity for multi-trip calculation
    Given contract negotiation returns a contract requiring 61 units
    And the ship has cargo capacity of 40 units
    And market data is available for required goods
    When I execute batch workflow with 1 iteration
    Then 1 contract should be negotiated
    And 1 contract should be accepted
    And 1 contract should be fulfilled
    And 2 trips should be required
