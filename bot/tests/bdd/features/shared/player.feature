Feature: Player Registration
  As a bot operator
  I want to register SpaceTraders agents
  So that I can manage multiple accounts

  Scenario: Register new player
    When I register player "AGENT-1" with token "TOKEN-123"
    Then the player should have a player_id
    And the player agent_symbol should be "AGENT-1"
    And the player token should be "TOKEN-123"
    And last_active should be set

  Scenario: Duplicate agent symbol rejected
    Given a player with agent_symbol "AGENT-1" exists
    When I attempt to register player "AGENT-1" with token "TOKEN-456"
    Then registration should fail with DuplicateAgentSymbolError

  Scenario: Empty agent symbol rejected
    When I attempt to register player "" with token "TOKEN-123"
    Then registration should fail with ValueError

  Scenario: Update player metadata
    Given a registered player with id 1
    When I update metadata with {"faction": "COSMIC"}
    Then the player metadata should contain "faction"

  Scenario: Touch last active timestamp
    Given a registered player with id 1
    When I touch the player's last_active
    Then last_active should be updated

  Scenario: List all players
    Given players "AGENT-1", "AGENT-2", "AGENT-3" are registered
    When I list all players
    Then I should see 3 players
