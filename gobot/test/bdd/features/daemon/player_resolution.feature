Feature: Daemon Player Resolution
  As a daemon service
  I need to resolve player identifiers from requests
  So that operations can be executed for the correct player

  Background:
    Given the daemon player resolution service is initialized
    And player 1 exists with agent symbol "AGENT-1"
    And player 2 exists with agent symbol "AGENT-2"

  # ============================================================================
  # Direct player_id Resolution
  # ============================================================================

  Scenario: Resolve player using player_id only
    When I resolve player with player_id 1
    Then the resolution should succeed
    And the resolved player_id should be 1

  Scenario: Resolve player using non-zero player_id
    When I resolve player with player_id 2
    Then the resolution should succeed
    And the resolved player_id should be 2

  # ============================================================================
  # Agent Symbol Resolution
  # ============================================================================

  Scenario: Resolve player using agent_symbol only
    When I resolve player with agent_symbol "AGENT-1"
    Then the resolution should succeed
    And the resolved player_id should be 1

  Scenario: Resolve player using different agent_symbol
    When I resolve player with agent_symbol "AGENT-2"
    Then the resolution should succeed
    And the resolved player_id should be 2

  # ============================================================================
  # Priority and Fallback Behavior
  # ============================================================================

  Scenario: player_id takes precedence over agent_symbol
    When I resolve player with player_id 1 and agent_symbol "AGENT-2"
    Then the resolution should succeed
    And the resolved player_id should be 1

  Scenario: agent_symbol is used when player_id is zero
    When I resolve player with player_id 0 and agent_symbol "AGENT-1"
    Then the resolution should succeed
    And the resolved player_id should be 1

  # ============================================================================
  # Error Cases
  # ============================================================================

  Scenario: Resolution fails when neither player_id nor agent_symbol provided
    When I resolve player with player_id 0 and no agent_symbol
    Then the resolution should fail
    And the error should contain "either player_id or agent_symbol must be provided"

  Scenario: Resolution fails when agent_symbol is empty string
    When I resolve player with player_id 0 and agent_symbol ""
    Then the resolution should fail
    And the error should contain "either player_id or agent_symbol must be provided"

  Scenario: Resolution fails when agent_symbol not found
    When I resolve player with agent_symbol "UNKNOWN-AGENT"
    Then the resolution should fail
    And the error should contain "failed to resolve agent symbol"

  # ============================================================================
  # Integration with gRPC Operations
  # ============================================================================

  Scenario: NavigateShip resolves player using player_id
    When I send a NavigateShip request with player_id 1
    Then the player should be resolved to player_id 1
    And the operation should use the correct player context

  Scenario: NavigateShip resolves player using agent_symbol
    When I send a NavigateShip request with agent_symbol "AGENT-2"
    Then the player should be resolved to player_id 2
    And the operation should use the correct player context

  Scenario: DockShip resolves player correctly
    When I send a DockShip request with player_id 1
    Then the player should be resolved to player_id 1
    And the operation should use the correct player context

  Scenario: OrbitShip resolves player correctly
    When I send an OrbitShip request with agent_symbol "AGENT-1"
    Then the player should be resolved to player_id 1
    And the operation should use the correct player context

  Scenario: RefuelShip resolves player correctly
    When I send a RefuelShip request with player_id 2
    Then the player should be resolved to player_id 2
    And the operation should use the correct player context
