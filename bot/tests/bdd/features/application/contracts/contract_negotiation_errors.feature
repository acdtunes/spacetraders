Feature: Contract Negotiation Error Visibility
  As a bot operator
  I want to see detailed error messages when contract negotiation fails
  So that I can understand what went wrong and take corrective action

  Background:
    Given a registered player with agent symbol "TEST-AGENT" and player_id 1

  Scenario: API error during negotiation is surfaced with details
    Given a ship "TEST-AGENT-1" exists for player 1
    And the API will return error 4509 "Ship is not at a waypoint with a faction"
    When I attempt to negotiate a contract with ship "TEST-AGENT-1"
    Then the negotiation should fail with a ContractNegotiationError
    And the error message should contain "Ship is not at a waypoint with a faction"
    And the error message should contain "4509"

  Scenario: Rate limit error is surfaced with retry suggestion
    Given a ship "TEST-AGENT-1" exists for player 1
    And the API returns a 429 rate limit error
    When I attempt to negotiate a contract with ship "TEST-AGENT-1"
    Then the negotiation should fail with a RateLimitError
    And the error message should contain "rate limit"

  Scenario: Generic API error is surfaced with HTTP details
    Given a ship "TEST-AGENT-1" exists for player 1
    And the API returns a 500 server error
    When I attempt to negotiate a contract with ship "TEST-AGENT-1"
    Then the negotiation should fail with a ContractNegotiationError
    And the error message should contain "500"
