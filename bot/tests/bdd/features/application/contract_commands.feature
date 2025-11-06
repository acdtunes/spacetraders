Feature: Contract Commands
  As an application layer
  I want to execute contract operations
  So that I can manage contracts through the SpaceTraders API

  Background:
    Given a clean database
    And a test player exists

  Scenario: Accept a contract
    Given an unaccepted contract "contract-123" in the database
    And the API will successfully accept the contract
    When I execute AcceptContractCommand for "contract-123"
    Then the command should succeed
    And the contract should be marked as accepted in the database

  Scenario: Deliver cargo for a contract
    Given an accepted contract "contract-123" with delivery requirements
    And the API will successfully record the delivery
    When I execute DeliverContractCommand for "contract-123" with 50 units of "IRON_ORE" from ship "SHIP-1"
    Then the command should succeed
    And the contract delivery progress should be updated in the database

  Scenario: Fulfill a contract
    Given a fully delivered contract "contract-123"
    And the API will successfully fulfill the contract
    When I execute FulfillContractCommand for "contract-123"
    Then the command should succeed
    And the contract should be marked as fulfilled in the database

  Scenario: Negotiate a new contract
    Given a ship "SHIP-1" at a location
    And the API will successfully negotiate a contract
    When I execute NegotiateContractCommand for ship "SHIP-1"
    Then the command should succeed
    And a new contract should be saved in the database
