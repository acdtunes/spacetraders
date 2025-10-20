Feature: Batch Contract Operations
  As a contract batch operator
  I want to negotiate and fulfill multiple contracts sequentially
  So that I don't get ERROR 4511 from overlapping active contracts

  Background:
    Given a ship "SHIP-1" with 40 cargo capacity
    And system "X1-TEST" with markets

  Scenario: Fulfill all profitable contracts in batch
    Given a batch operation with count=3
    And all 3 contracts are profitable
    When I run the batch operation
    Then 3 contracts should be negotiated
    And 3 contracts should be fulfilled
    And the operation should succeed

  Scenario: Accept all contracts regardless of profitability
    Given a batch operation with count=3
    And contract 1 is profitable
    And contract 2 is unprofitable (low payment)
    And contract 3 is profitable
    When I run the batch operation
    Then 3 contracts should be negotiated
    And ALL 3 contracts should be fulfilled (no skipping)
    And the operation should succeed

  Scenario: Continue after fulfillment failure
    Given a batch operation with count=3
    And all 3 contracts are profitable
    And contract 2 fulfillment will fail
    When I run the batch operation
    Then 3 contracts should be negotiated
    And 3 fulfillment attempts should be made
    And contracts 1 and 3 should succeed
    And the operation should still report success (2/3 fulfilled)

  @xfail
  Scenario: Continue after negotiation failure
    Given a batch operation with count=3
    And contract 2 negotiation will fail (returns None)
    When I run the batch operation
    Then 3 negotiation attempts should be made
    But only 2 contracts should be fulfilled (skipping failed negotiation)
    And the operation should succeed (2/3 fulfilled)

  Scenario: Fail when all fulfillments fail
    Given a batch operation with count=3
    And all 3 contract fulfillments will fail
    When I run the batch operation
    Then 3 contracts should be negotiated
    And 3 fulfillment attempts should be made
    And the operation should FAIL (0/3 fulfilled)

  @xfail
  Scenario: Sequential execution prevents ERROR 4511
    Given a batch operation with count=2
    When I negotiate contract 1
    Then contract 1 should be fulfilled completely
    And contract 1 should become INACTIVE
    Then I negotiate contract 2
    And contract 2 should be fulfilled completely
    And there should be NO error 4511

  @xfail
  Scenario: Simulate ERROR 4511 without sequential execution
    Given a batch operation with count=2
    And the system checks if previous contract is fulfilled before negotiating
    When I try to negotiate while a contract is still active
    Then the negotiation should fail with error 4511
    But with sequential execution, the previous contract is fulfilled first
    And the negotiation succeeds

  Scenario: Always fulfill unprofitable contracts (no filtering)
    Given a batch operation with count=2
    And both contracts are unprofitable (100 credits payment vs 75,000 cost)
    When I run the batch operation
    Then 2 contracts should be negotiated
    And BOTH contracts should be fulfilled (no profitability filter)
    And the operation should succeed

  @xfail
  Scenario: Fulfill existing active contract before new negotiation
    Given the agent has 1 existing active unfulfilled contract
    And a batch operation with count=2
    When I run the batch operation
    Then the existing contract should be fulfilled first
    And then 2 new contracts should be negotiated
    And total contracts fulfilled should be 3 (1 existing + 2 new)

  @xfail
  Scenario: Prevent ERROR 4511 with existing active contract
    Given the agent has 1 active contract on page 2
    And a batch operation with count=2
    When I check for active contracts
    Then all pages should be fetched
    And the active contract should be found on page 2
    And it should be fulfilled before negotiating new ones
    And there should be NO error 4511
