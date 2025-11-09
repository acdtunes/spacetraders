Feature: Log Captain Entry Command
  As the AI captain (TARS)
  I want to log narrative mission entries
  So that I can maintain continuity across sessions and track fleet operations

  Background:
    Given the captain logging system is initialized

  # Happy Path - Basic Logging
  Scenario: Log a session start entry
    Given a registered player with id 1 and agent symbol "ENDURANCE"
    When I log a captain entry with type "session_start" and narrative "Beginning shift at 0800 hours. All systems nominal."
    Then the command should succeed
    And the captain log should be stored in the database
    And the log should have entry type "session_start"
    And the log should have the narrative "Beginning shift at 0800 hours. All systems nominal."

  Scenario: Log an operation started entry with event data
    Given a registered player with id 1 and agent symbol "ENDURANCE"
    When I log a captain entry with type "operation_started" and narrative "Initiating mining operation"
    And the entry has event data with ship "ENDURANCE-1"
    And the entry has event data with operation "mining"
    Then the command should succeed
    And the log should have entry type "operation_started"
    And the log should contain event data with ship "ENDURANCE-1"
    And the log should contain event data with operation "mining"

  Scenario: Log an operation with tags
    Given a registered player with id 1 and agent symbol "ENDURANCE"
    When I log a captain entry with type "operation_completed", narrative "Mining run completed successfully", and tags:
      | mining     |
      | iron_ore   |
      | attempt_3  |
    Then the command should succeed
    And the log should have 3 tags
    And the log should have tag "mining"
    And the log should have tag "iron_ore"
    And the log should have tag "attempt_3"

  Scenario: Log an entry with fleet snapshot
    Given a registered player with id 1 and agent symbol "ENDURANCE"
    When I log a captain entry with type "strategic_decision", narrative "Expanding mining fleet to 5 drones", and fleet snapshot:
      """
      {
        "active_miners": 3,
        "active_scouts": 2,
        "total_credits": 150000
      }
      """
    Then the command should succeed
    And the log should contain fleet snapshot with active_miners 3
    And the log should contain fleet snapshot with total_credits 150000

  Scenario: Log a critical error entry
    Given a registered player with id 1 and agent symbol "ENDURANCE"
    When I log a captain entry with type "critical_error", narrative "Ship ENDURANCE-5 encountered navigation failure", and event data:
      """
      {
        "ship": "ENDURANCE-5",
        "error_type": "navigation_failure",
        "waypoint": "X1-TEST-CD34"
      }
      """
    Then the command should succeed
    And the log should have entry type "critical_error"

  # Entry Type Validation
  Scenario: Reject invalid entry type
    Given a registered player with id 1 and agent symbol "ENDURANCE"
    When I attempt to log a captain entry with type "invalid_type" and narrative "Test"
    Then the command should fail with ValueError
    And the error message should mention "Invalid entry_type"

  Scenario: Reject missing narrative
    Given a registered player with id 1 and agent symbol "ENDURANCE"
    When I attempt to log a captain entry with type "session_start" and empty narrative
    Then the command should fail with ValueError
    And the error message should mention "narrative"

  # Player Validation
  Scenario: Reject missing player_id
    Given no player exists with id 999
    When I attempt to log a captain entry for player 999 with type "session_start" and narrative "Test"
    Then the command should fail with PlayerNotFoundError
    And the error message should mention "Player 999 not found"

  # Data Integrity
  Scenario: Log entry with all fields populated
    Given a registered player with id 1 and agent symbol "ENDURANCE"
    When I log a complete captain entry with:
      | entry_type   | operation_completed                               |
      | narrative    | Successfully delivered 50 units of IRON_ORE       |
      | event_data   | {"ship": "ENDURANCE-1", "cargo": "IRON_ORE"}      |
      | tags         | delivery,iron_ore,success                         |
      | fleet_snapshot | {"active_miners": 5, "total_credits": 200000}   |
    Then the command should succeed
    And the log should have all fields populated correctly

  # Valid Entry Types
  Scenario Outline: Accept all valid entry types
    Given a registered player with id 1 and agent symbol "ENDURANCE"
    When I log a captain entry with type "<entry_type>" and narrative "Test entry"
    Then the command should succeed
    And the log should have entry type "<entry_type>"

    Examples:
      | entry_type            |
      | session_start         |
      | operation_started     |
      | operation_completed   |
      | critical_error        |
      | strategic_decision    |
      | session_end           |

  # JSON Validation
  Scenario: Reject malformed event_data JSON
    Given a registered player with id 1 and agent symbol "ENDURANCE"
    When I attempt to log a captain entry with malformed event_data "not valid json"
    Then the command should fail with ValueError
    And the error message should mention "Invalid JSON"

  Scenario: Reject malformed fleet_snapshot JSON
    Given a registered player with id 1 and agent symbol "ENDURANCE"
    When I attempt to log a captain entry with malformed fleet_snapshot "{invalid"
    Then the command should fail with ValueError
    And the error message should mention "Invalid JSON"

  # Timestamp Handling
  Scenario: Log entry timestamp is automatically set
    Given a registered player with id 1 and agent symbol "ENDURANCE"
    When I log a captain entry with type "session_start" and narrative "Test"
    Then the command should succeed
    And the log should have a timestamp within the last 5 seconds
