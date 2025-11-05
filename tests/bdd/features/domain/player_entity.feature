Feature: Player Entity Domain Logic
  As a domain entity
  The Player entity should enforce business rules and invariants
  So that player data integrity is maintained

  Background:
    Given a base timestamp of "2024-01-01T12:00:00Z"

  # Player Initialization and Validation
  Scenario: Create player with valid data
    When I create a player with:
      | player_id    | agent_symbol | token         | created_at              |
      | 1            | TEST_AGENT   | test-token-123 | 2024-01-01T12:00:00Z   |
    Then the player should have player_id 1
    And the player should have agent_symbol "TEST_AGENT"
    And the player should have token "test-token-123"
    And the player should have created_at "2024-01-01T12:00:00Z"

  Scenario: Create player without player_id
    When I create a player with:
      | player_id    | agent_symbol | token         | created_at              |
      | None         | NEW_AGENT    | token-456     | 2024-01-01T12:00:00Z   |
    Then the player should have player_id None
    And the player should have agent_symbol "NEW_AGENT"

  Scenario: Default last_active to created_at
    When I create a player without last_active
    Then the player last_active should equal created_at

  Scenario: Set last_active when provided
    When I create a player with last_active "2024-01-01T13:00:00Z"
    Then the player should have last_active "2024-01-01T13:00:00Z"

  Scenario: Default metadata to empty dict
    When I create a player without metadata
    Then the player metadata should be empty

  Scenario: Set metadata when provided
    When I create a player with metadata:
      | faction | credits |
      | COSMIC  | 1000    |
    Then the player metadata should contain "faction" with value "COSMIC"
    And the player metadata should contain "credits" with value 1000

  Scenario: Trim agent_symbol whitespace
    When I create a player with agent_symbol "  TEST_AGENT  "
    Then the player should have agent_symbol "TEST_AGENT"

  Scenario: Trim token whitespace
    When I create a player with token "  test-token-123  "
    Then the player should have token "test-token-123"

  Scenario: Reject empty agent_symbol
    When I attempt to create a player with empty agent_symbol
    Then player creation should fail with error "agent_symbol cannot be empty"

  Scenario: Reject whitespace-only agent_symbol
    When I attempt to create a player with whitespace agent_symbol
    Then player creation should fail with error "agent_symbol cannot be empty"

  Scenario: Reject empty token
    When I attempt to create a player with empty token
    Then player creation should fail with error "token cannot be empty"

  Scenario: Reject whitespace-only token
    When I attempt to create a player with whitespace token
    Then player creation should fail with error "token cannot be empty"

  # Player Property Access
  Scenario: Player_id property is readonly
    Given a player with player_id 1
    When I attempt to modify player_id to 2
    Then the modification should be rejected

  Scenario: Agent_symbol property is readonly
    Given a player with agent_symbol "TEST_AGENT"
    When I attempt to modify agent_symbol to "NEW_AGENT"
    Then the modification should be rejected

  Scenario: Token property is readonly
    Given a player with token "test-token-123"
    When I attempt to modify token to "new-token"
    Then the modification should be rejected

  Scenario: Created_at property is readonly
    Given a player with created_at "2024-01-01T12:00:00Z"
    When I attempt to modify created_at
    Then the modification should be rejected

  Scenario: Last_active property is readonly
    Given a player with last_active set
    When I attempt to modify last_active
    Then the modification should be rejected

  Scenario: Metadata returns copy preventing external mutation
    Given a player with metadata containing "faction" = "COSMIC"
    When I modify the returned metadata to "faction" = "AEGIS"
    Then the player metadata should still contain "faction" with value "COSMIC"

  # Update Last Active
  Scenario: Update last_active to current time
    Given a player with last_active "2024-01-01T12:00:00Z"
    When I call update_last_active
    Then the player last_active should be more recent than "2024-01-01T12:00:00Z"

  Scenario: Multiple updates increase timestamp
    Given a player with last_active set
    When I call update_last_active
    And I wait briefly
    And I call update_last_active again
    Then the second timestamp should be greater than the first

  Scenario: Update uses UTC timezone
    Given a player
    When I call update_last_active
    Then the last_active timezone should be UTC

  # Update Metadata
  Scenario: Update metadata with new values
    Given a player with empty metadata
    When I update metadata with:
      | faction | credits |
      | COSMIC  | 1000    |
    Then the player metadata should contain "faction" with value "COSMIC"
    And the player metadata should contain "credits" with value 1000

  Scenario: Update existing metadata keys
    Given a player with metadata:
      | faction | credits |
      | COSMIC  | 1000    |
    When I update metadata with:
      | credits |
      | 2000    |
    Then the player metadata should contain "credits" with value 2000
    And the player metadata should contain "faction" with value "COSMIC"

  Scenario: Add new keys to metadata
    Given a player with metadata:
      | faction |
      | COSMIC  |
    When I update metadata with:
      | credits |
      | 1000    |
    Then the player metadata should contain "faction" with value "COSMIC"
    And the player metadata should contain "credits" with value 1000

  Scenario: Handle empty metadata update
    Given a player with empty metadata
    When I update metadata with empty dict
    Then the player metadata should be empty

  Scenario: Handle multiple metadata updates
    Given a player with empty metadata
    When I update metadata with "key1" = "value1"
    And I update metadata with "key2" = "value2"
    And I update metadata with "key3" = "value3"
    Then the player metadata should contain "key1" with value "value1"
    And the player metadata should contain "key2" with value "value2"
    And the player metadata should contain "key3" with value "value3"

  # Is Active Within
  Scenario: Active within specified hours
    Given a player with last_active 1 hour ago
    When I check if active within 2 hours
    Then the result should be True

  Scenario: Not active within specified hours
    Given a player with last_active 3 hours ago
    When I check if active within 2 hours
    Then the result should be False

  Scenario: Active at exact boundary
    Given a player with last_active almost 2 hours ago
    When I check if active within 2 hours
    Then the result should be True

  Scenario: Not active just past boundary
    Given a player with last_active just over 2 hours ago
    When I check if active within 2 hours
    Then the result should be False

  Scenario: Works with fractional hours - within
    Given a player with last_active 30 minutes ago
    When I check if active within 1 hour
    Then the result should be True

  Scenario: Works with fractional hours - not within
    Given a player with last_active 30 minutes ago
    When I check if active within 0.49 hours
    Then the result should be False

  Scenario: Works with large hour values - within
    Given a player with last_active 5 days ago
    When I check if active within 168 hours
    Then the result should be True

  Scenario: Works with large hour values - not within
    Given a player with last_active 5 days ago
    When I check if active within 96 hours
    Then the result should be False

  # Player String Representation
  Scenario: Repr contains player info
    Given a player with player_id 1 and agent_symbol "TEST_AGENT"
    When I get the repr string
    Then the repr should contain "1"
    And the repr should contain "TEST_AGENT"

  Scenario: Repr with None player_id
    Given a player with player_id None and agent_symbol "NEW_AGENT"
    When I get the repr string
    Then the repr should contain "None"
    And the repr should contain "NEW_AGENT"
