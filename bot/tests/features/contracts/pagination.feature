Feature: Contract Pagination Handling
  As a contract batch operation
  I want to check ALL pages of contracts for active ones
  So that I don't get ERROR 4511 when active contracts exist on later pages

  Background:
    Given an agent with multiple contracts across pages
    And the API returns 20 contracts per page by default

  @xfail
  Scenario: Detect active contract on page 2
    Given the agent has 16 total contracts
    And page 1 has 10 fulfilled contracts
    And page 2 has 5 fulfilled contracts and 1 active contract
    When I start a batch contract operation
    Then the system should fetch page 1 (contracts 1-10)
    And detect total=16 from meta
    And fetch page 2 (contracts 11-16)
    And find the active contract on page 2
    And fulfill the active contract before negotiating new ones

  @xfail
  Scenario: Prevent ERROR 4511 with pagination
    Given the agent has 16 total contracts
    And contract 16 is ACTIVE (accepted but not fulfilled)
    And contracts 1-15 are fulfilled
    And the user requests batch operation with count=1
    When I check for active contracts
    Then the system should fetch all pages
    And find contract 16 on page 2
    And fulfill contract 16 first
    And then negotiate 1 new contract
    And there should be NO error 4511

  @xfail
  Scenario: Single page with no active contracts
    Given the agent has 8 total contracts
    And all 8 contracts are fulfilled
    And the user requests batch operation with count=2
    When I check for active contracts
    Then the system should fetch page 1
    And find no active contracts
    And negotiate 2 new contracts
    And fulfill them successfully
