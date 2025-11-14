Feature: Negotiate Contract Command
  As a SpaceTraders bot
  I want to negotiate contracts with ships
  So that I can obtain delivery missions and earn credits

  Background:
    Given a player exists with agent "TEST-AGENT" and token "test-token-123"
    And the player has player_id 1

  Scenario: Negotiate new contract successfully
    Given a negotiate contract ship "SHIP-1" for player 1 at "X1-A1" with status "DOCKED"
    And the ship has no active contract
    When I execute NegotiateContractCommand for ship "SHIP-1" and player 1
    Then the negotiate contract command should succeed
    And a new contract should be negotiated
    And the contract should belong to player 1
    And the contract should not be accepted
    And the contract should not be fulfilled

  Scenario: Resume existing contract when agent already has one (error 4511)
    Given a negotiate contract ship "SHIP-1" for player 1 at "X1-A1" with status "DOCKED"
    And agent already has an active contract "CONTRACT-1" for player 1
    When I execute NegotiateContractCommand for ship "SHIP-1" and player 1
    Then the negotiate contract command should succeed
    And the existing contract "CONTRACT-1" should be returned
    And no new contract should be negotiated

  Scenario: Dock ship before negotiating
    Given a negotiate contract ship "SHIP-1" for player 1 at "X1-A1" with status "IN_ORBIT"
    And the ship has no active contract
    When I execute NegotiateContractCommand for ship "SHIP-1" and player 1
    Then the negotiate contract command should succeed
    And the ship should be docked first
    And a new contract should be negotiated

  Scenario: Cannot negotiate contract for ship that does not exist
    When I execute NegotiateContractCommand for ship "NONEXISTENT" and player 1
    Then the negotiate contract command should fail with error "ship not found"

  Scenario: Cannot negotiate contract with ship in transit
    Given a negotiate contract ship "SHIP-1" for player 1 in transit to "X1-B2"
    When I execute NegotiateContractCommand for ship "SHIP-1" and player 1
    Then the negotiate contract command should fail with error "cannot dock while in transit"
