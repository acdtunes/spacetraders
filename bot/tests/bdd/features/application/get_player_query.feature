Feature: Get Player Query
  As a fleet management system
  I want to retrieve player information
  So that I can access player data by ID or agent symbol

  Background:
    Given the get player query handlers are initialized

  # Get Player by ID - Happy Path
  Scenario: Get player by ID successfully
    Given a registered player with id 1 and agent symbol "TEST-AGENT"
    When I execute get player query for player id 1
    Then the query should succeed
    And the returned player should have player_id 1
    And the returned player should have agent symbol "TEST-AGENT"

  Scenario: Get different players by ID
    Given a registered player with id 1 and agent symbol "AGENT-1"
    And a registered player with id 2 and agent symbol "AGENT-2"
    When I execute get player query for player id 1
    And I execute get player query for player id 2
    Then the first result should have player_id 1
    And the first result should have agent symbol "AGENT-1"
    And the second result should have player_id 2
    And the second result should have agent symbol "AGENT-2"

  Scenario: Get player returns same instance on multiple calls
    Given a registered player with id 1 and agent symbol "TEST-AGENT"
    When I execute get player query for player id 1
    And I execute get player query for player id 1
    Then both results should be the same player instance

  # Get Player by ID - Error Cases
  Scenario: Cannot get non-existent player by ID
    Given no player exists with id 999
    When I attempt to get player by id 999
    Then the query should fail with PlayerNotFoundError
    And the error message should mention "Player 999 not found"

  Scenario: Cannot get player with zero ID
    Given no player exists with id 0
    When I attempt to get player by id 0
    Then the query should fail with PlayerNotFoundError

  Scenario: Cannot get player with negative ID
    Given no player exists with id -1
    When I attempt to get player by id -1
    Then the query should fail with PlayerNotFoundError

  # Get Player by Agent Symbol - Happy Path
  Scenario: Get player by agent symbol successfully
    Given a registered player with id 1 and agent symbol "TEST-AGENT"
    When I execute get player by agent query for agent symbol "TEST-AGENT"
    Then the query should succeed
    And the returned player should have player_id 1
    And the returned player should have agent symbol "TEST-AGENT"

  Scenario: Get correct player by agent symbol when multiple exist
    Given a registered player with id 1 and agent symbol "AGENT-1"
    And a registered player with id 2 and agent symbol "AGENT-2"
    And a registered player with id 3 and agent symbol "AGENT-3"
    When I execute get player by agent query for agent symbol "AGENT-2"
    Then the query should succeed
    And the returned player should have player_id 2
    And the returned player should have agent symbol "AGENT-2"

  Scenario: Agent symbol lookup is case-sensitive
    Given a registered player with id 1 and agent symbol "TEST-AGENT"
    When I attempt to get player by agent symbol "test-agent"
    Then the query should fail with PlayerNotFoundError

  Scenario: Get player with special characters in agent symbol
    Given a registered player with id 1 and agent symbol "TEST-AGENT_123"
    When I execute get player by agent query for agent symbol "TEST-AGENT_123"
    Then the query should succeed
    And the returned player should have agent symbol "TEST-AGENT_123"

  # Get Player by Agent Symbol - Error Cases
  Scenario: Cannot get non-existent player by agent symbol
    Given no player exists with agent symbol "NONEXISTENT"
    When I attempt to get player by agent symbol "NONEXISTENT"
    Then the query should fail with PlayerNotFoundError
    And the error message should mention "Agent 'NONEXISTENT' not found"

  Scenario: Cannot get player with empty agent symbol
    Given no player exists with agent symbol ""
    When I attempt to get player by agent symbol ""
    Then the query should fail with PlayerNotFoundError

  # Handler Initialization
  Scenario: Get player handler initializes with repository correctly
    Given a mock player repository is created
    When I create a get player handler with the repository
    Then the handler should have the repository initialized

  Scenario: Get player by agent handler initializes with repository correctly
    Given a mock player repository is created
    When I create a get player by agent handler with the repository
    Then the handler should have the repository initialized
