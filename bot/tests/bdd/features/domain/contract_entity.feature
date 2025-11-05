Feature: Contract Entity
  As a SpaceTraders agent
  I want to manage contracts
  So that I can fulfill delivery requirements and earn credits

  Scenario: Create a valid contract
    Given a contract with valid terms
    When I create the contract entity
    Then the contract should be created successfully
    And the contract status should be "OFFERED"

  Scenario: Accept an offered contract
    Given a contract with status "OFFERED"
    When I accept the contract
    Then the contract status should be "ACCEPTED"
    And the contract should be marked as accepted

  Scenario: Cannot accept an already accepted contract
    Given a contract with status "ACCEPTED"
    When I try to accept the contract
    Then it should raise ContractAlreadyAcceptedError

  Scenario: Check if contract is fulfilled
    Given a contract with delivery requirements
    And all delivery units are fulfilled
    When I check if the contract is fulfilled
    Then it should return True

  Scenario: Check remaining units for delivery
    Given a contract requiring 100 units of IRON_ORE
    And 60 units have been fulfilled
    When I check remaining units
    Then it should return 40 units
