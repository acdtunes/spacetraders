Feature: Player Domain Entity
  As a domain entity
  The Player should enforce business rules and maintain data integrity
  Through its public interface only

  Background:
    Given a base timestamp of "2024-01-01T12:00:00Z"

  # ============================================================================
  # Player Initialization Tests
  # ============================================================================

  Scenario: Create player with all valid data
    When I create a player with player_id 1, agent_symbol "TEST_AGENT", token "test-token-123", and created_at "2024-01-01T12:00:00Z"
    Then the player player_id should be 1
    And the player agent_symbol should be "TEST_AGENT"
    And the player token should be "test-token-123"
    And the player created_at should be "2024-01-01T12:00:00Z"

  Scenario: Create player without player_id (new player)
    When I create a player with player_id None, agent_symbol "NEW_AGENT", token "token-456", and created_at "2024-01-01T12:00:00Z"
    Then the player player_id should be None

  Scenario: Last_active defaults to created_at when not provided
    When I create a player without specifying last_active
    Then the player last_active should equal the player created_at

  Scenario: Last_active uses provided value when specified
    When I create a player with last_active "2024-01-01T13:00:00Z"
    Then the player last_active should be "2024-01-01T13:00:00Z"

  Scenario: Metadata defaults to empty dict when not provided
    When I create a player without specifying metadata
    Then the player metadata should be an empty dictionary

  Scenario: Metadata uses provided value when specified
    When I create a player with metadata containing faction="COSMIC" and credits=1000
    Then the player metadata should contain "faction" with value "COSMIC"
    And the player metadata should contain "credits" with value 1000

  Scenario: Agent_symbol whitespace is trimmed
    When I create a player with agent_symbol "  TEST_AGENT  "
    Then the player agent_symbol should be "TEST_AGENT"

  Scenario: Token whitespace is trimmed
    When I create a player with token "  test-token-123  "
    Then the player token should be "test-token-123"

  Scenario: Empty agent_symbol is rejected
    When I attempt to create a player with agent_symbol ""
    Then a ValueError should be raised with message "agent_symbol cannot be empty"

  Scenario: Whitespace-only agent_symbol is rejected
    When I attempt to create a player with agent_symbol "   "
    Then a ValueError should be raised with message "agent_symbol cannot be empty"

  Scenario: Empty token is rejected
    When I attempt to create a player with token ""
    Then a ValueError should be raised with message "token cannot be empty"

  Scenario: Whitespace-only token is rejected
    When I attempt to create a player with token "   "
    Then a ValueError should be raised with message "token cannot be empty"

  # ============================================================================
  # Player Property Access Tests (Read-Only Enforcement)
  # ============================================================================

  Scenario: Player_id property is readable
    Given a player exists with player_id 1
    Then the player player_id should be 1

  Scenario: Player_id property is read-only
    Given a player exists with player_id 1
    When I attempt to set player_id to 2
    Then an AttributeError should be raised

  Scenario: Agent_symbol property is readable
    Given a player exists with agent_symbol "TEST_AGENT"
    Then the player agent_symbol should be "TEST_AGENT"

  Scenario: Agent_symbol property is read-only
    Given a player exists with agent_symbol "TEST_AGENT"
    When I attempt to set agent_symbol to "NEW_AGENT"
    Then an AttributeError should be raised

  Scenario: Token property is readable
    Given a player exists with token "test-token-123"
    Then the player token should be "test-token-123"

  Scenario: Token property is read-only
    Given a player exists with token "test-token-123"
    When I attempt to set token to "new-token"
    Then an AttributeError should be raised

  Scenario: Created_at property is readable
    Given a player exists with created_at "2024-01-01T12:00:00Z"
    Then the player created_at should be "2024-01-01T12:00:00Z"

  Scenario: Created_at property is read-only
    Given a player exists with created_at "2024-01-01T12:00:00Z"
    When I attempt to set created_at to a new value
    Then an AttributeError should be raised

  Scenario: Last_active property is readable
    Given a player exists
    Then the player last_active should not be None

  Scenario: Last_active property is read-only
    Given a player exists
    When I attempt to set last_active to a new value
    Then an AttributeError should be raised

  Scenario: Metadata property returns a copy (not reference)
    Given a player exists with metadata containing "faction"="COSMIC"
    When I get the metadata and modify it to "faction"="AEGIS"
    Then the player metadata should still contain "faction" with value "COSMIC"

  # ============================================================================
  # Update Last Active Tests
  # ============================================================================

  Scenario: Update_last_active sets timestamp to current UTC time
    Given a player exists with last_active "2024-01-01T12:00:00Z"
    When I call update_last_active
    Then the player last_active should be more recent than "2024-01-01T12:00:00Z"

  Scenario: Multiple update_last_active calls increase timestamp
    Given a player exists
    When I call update_last_active
    And I record the first timestamp
    And I wait a brief moment
    And I call update_last_active again
    Then the second timestamp should be greater than the first timestamp

  Scenario: Update_last_active uses UTC timezone
    Given a player exists
    When I call update_last_active
    Then the player last_active timezone should be UTC

  # ============================================================================
  # Update Metadata Tests
  # ============================================================================

  Scenario: Update_metadata adds new key-value pairs
    Given a player exists with empty metadata
    When I call update_metadata with faction="COSMIC" and credits=1000
    Then the player metadata should contain "faction" with value "COSMIC"
    And the player metadata should contain "credits" with value 1000

  Scenario: Update_metadata updates existing keys
    Given a player exists with metadata containing faction="COSMIC" and credits=1000
    When I call update_metadata with credits=2000
    Then the player metadata should contain "credits" with value 2000
    And the player metadata should contain "faction" with value "COSMIC"

  Scenario: Update_metadata adds keys without removing existing ones
    Given a player exists with metadata containing "faction"="COSMIC"
    When I call update_metadata with credits=1000
    Then the player metadata should contain "faction" with value "COSMIC"
    And the player metadata should contain "credits" with value 1000

  Scenario: Update_metadata handles empty dict
    Given a player exists with empty metadata
    When I call update_metadata with an empty dictionary
    Then the player metadata should be an empty dictionary

  Scenario: Update_metadata supports multiple sequential updates - first update
    Given a player exists with empty metadata
    When I call update_metadata with key1="value1"
    Then the player metadata should contain "key1" with value "value1"

  Scenario: Update_metadata supports multiple sequential updates - second update
    Given a player exists with metadata containing "key1"="value1"
    When I call update_metadata with key2="value2"
    Then the player metadata should contain "key1" with value "value1"
    And the player metadata should contain "key2" with value "value2"

  Scenario: Update_metadata supports multiple sequential updates - third update
    Given a player exists with metadata containing key1="value1" and key2="value2"
    When I call update_metadata with key3="value3"
    Then the player metadata should contain "key1" with value "value1"
    And the player metadata should contain "key2" with value "value2"
    And the player metadata should contain "key3" with value "value3"

  # ============================================================================
  # Is Active Within Tests
  # ============================================================================

  Scenario: Is_active_within returns True when last_active is within hours
    Given a player exists with last_active 1 hour ago
    When I check is_active_within with hours=2
    Then the result should be True

  Scenario: Is_active_within returns False when last_active exceeds hours
    Given a player exists with last_active 3 hours ago
    When I check is_active_within with hours=2
    Then the result should be False

  Scenario: Is_active_within returns True at exact boundary (minus 1 second)
    Given a player exists with last_active "almost 2 hours ago"
    When I check is_active_within with hours=2
    Then the result should be True

  Scenario: Is_active_within returns False just past boundary (plus 1 second)
    Given a player exists with last_active "just over 2 hours ago"
    When I check is_active_within with hours=2
    Then the result should be False

  Scenario: Is_active_within works with fractional hours - within range
    Given a player exists with last_active 30 minutes ago
    When I check is_active_within with hours=1.0
    Then the result should be True

  Scenario: Is_active_within works with fractional hours - outside range
    Given a player exists with last_active 30 minutes ago
    When I check is_active_within with hours=0.49
    Then the result should be False

  Scenario: Is_active_within works with large hour values - within range
    Given a player exists with last_active 5 days ago
    When I check is_active_within with hours=168
    Then the result should be True

  Scenario: Is_active_within works with large hour values - outside range
    Given a player exists with last_active 5 days ago
    When I check is_active_within with hours=96
    Then the result should be False

  # ============================================================================
  # Player Repr Tests
  # ============================================================================

  Scenario: Repr contains player_id
    Given a player exists with player_id 1 and agent_symbol "TEST_AGENT"
    When I get the string representation
    Then the representation should contain "1"

  Scenario: Repr contains agent_symbol
    Given a player exists with player_id 1 and agent_symbol "TEST_AGENT"
    When I get the string representation
    Then the representation should contain "TEST_AGENT"

  Scenario: Repr handles None player_id - contains None
    Given a player exists with player_id None and agent_symbol "NEW_AGENT"
    When I get the string representation
    Then the representation should contain "None"

  Scenario: Repr handles None player_id - contains agent_symbol
    Given a player exists with player_id None and agent_symbol "NEW_AGENT"
    When I get the string representation
    Then the representation should contain "NEW_AGENT"

  # ============================================================================
  # Credits Management Tests
  # ============================================================================

  Scenario: Player initializes with zero credits by default
    When I create a player without specifying credits
    Then the player credits should be 0

  Scenario: Player initializes with specified credits
    When I create a player with credits 150000
    Then the player credits should be 150000

  Scenario: Credits property is readable
    Given a player exists with credits 100000
    Then the player credits should be 100000

  Scenario: Credits property is read-only
    Given a player exists with credits 100000
    When I attempt to set credits to 200000
    Then an AttributeError should be raised

  Scenario: Add_credits increases credits balance
    Given a player exists with credits 100000
    When I call add_credits with amount 50000
    Then the player credits should be 150000

  Scenario: Add_credits with zero amount is allowed
    Given a player exists with credits 100000
    When I call add_credits with amount 0
    Then the player credits should be 100000

  Scenario: Add_credits with negative amount raises ValueError
    Given a player exists with credits 100000
    When I attempt to add_credits with amount -5000
    Then a ValueError should be raised with message "amount cannot be negative"

  Scenario: Spend_credits decreases credits balance
    Given a player exists with credits 100000
    When I call spend_credits with amount 30000
    Then the player credits should be 70000

  Scenario: Spend_credits with zero amount is allowed
    Given a player exists with credits 100000
    When I call spend_credits with amount 0
    Then the player credits should be 100000

  Scenario: Spend_credits with negative amount raises ValueError
    Given a player exists with credits 100000
    When I attempt to spend_credits with amount -5000
    Then a ValueError should be raised with message "amount cannot be negative"

  Scenario: Spend_credits with insufficient balance raises InsufficientCreditsError
    Given a player exists with credits 50000
    When I attempt to spend_credits with amount 75000
    Then an InsufficientCreditsError should be raised with message "Insufficient credits: need 75000, have 50000"

  Scenario: Spend_credits with exact balance succeeds
    Given a player exists with credits 100000
    When I call spend_credits with amount 100000
    Then the player credits should be 0

  Scenario: Multiple credit operations work sequentially - add then spend
    Given a player exists with credits 100000
    When I call add_credits with amount 50000
    And I call spend_credits with amount 30000
    Then the player credits should be 120000

  Scenario: Multiple credit operations work sequentially - spend then add
    Given a player exists with credits 100000
    When I call spend_credits with amount 20000
    And I call add_credits with amount 10000
    Then the player credits should be 90000
