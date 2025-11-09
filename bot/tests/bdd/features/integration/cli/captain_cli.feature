Feature: Captain CLI Commands
  As a captain (TARS agent)
  I need CLI commands to log narrative entries and retrieve logs
  So I can maintain continuity across sessions

  Background:
    Given a registered player with agent "ENDURANCE"

  Scenario: Log a captain entry with all fields
    When I log a captain entry with:
      | field          | value                                           |
      | agent          | ENDURANCE                                       |
      | entry_type     | session_start                                   |
      | narrative      | Beginning shift 20251109-1430. Fleet status nominal. |
      | event_data     | {"mission": "contract_fulfillment"}             |
      | tags           | session_start,contract                          |
      | fleet_snapshot | {"active_miners": 7, "total_credits": 50000}    |
    Then the captain log command should succeed
    And the log entry should be saved to the database

  Scenario: Log a captain entry with minimal fields
    When I log a captain entry with:
      | field      | value                                |
      | agent      | ENDURANCE                            |
      | entry_type | operation_started                    |
      | narrative  | Started mining operation at X1-GZ7-B2 |
    Then the captain log command should succeed
    And the log entry should be saved to the database

  Scenario: Retrieve captain logs without filters
    Given the following captain logs exist:
      | entry_type         | narrative                               |
      | session_start      | Session 1 started                       |
      | operation_started  | Mining operation began                  |
      | operation_completed| Mining operation finished               |
    When I retrieve captain logs for agent "ENDURANCE"
    Then the logs command should succeed
    And I should see 3 log entries
    And the entries should be in reverse chronological order

  Scenario: Retrieve captain logs filtered by entry type
    Given the following captain logs exist:
      | entry_type         | narrative                    |
      | session_start      | Session started              |
      | critical_error     | Fuel exhaustion detected     |
      | operation_started  | Mining began                 |
      | critical_error     | Navigation failure           |
    When I retrieve captain logs for agent "ENDURANCE" with type "critical_error"
    Then the logs command should succeed
    And I should see 2 log entries
    And all entries should have type "critical_error"

  Scenario: Retrieve captain logs filtered by tags
    Given the following captain logs exist with tags:
      | entry_type         | narrative           | tags                    |
      | operation_started  | Mining iron ore     | mining,iron_ore         |
      | operation_completed| Mining finished     | mining,iron_ore,success |
      | operation_started  | Trading goods       | trading,fuel            |
    When I retrieve captain logs for agent "ENDURANCE" with tags "mining,iron_ore"
    Then the logs command should succeed
    And I should see 2 log entries

  Scenario: Retrieve captain logs with limit
    Given 50 captain log entries exist for "ENDURANCE"
    When I retrieve captain logs for agent "ENDURANCE" with limit 10
    Then the logs command should succeed
    And I should see 10 log entries

  Scenario: Retrieve captain logs since timestamp
    Given captain logs exist with timestamps:
      | entry_type        | narrative    | timestamp           |
      | session_start     | Old session  | 2025-11-08T00:00:00 |
      | operation_started | Old op       | 2025-11-08T12:00:00 |
      | session_start     | New session  | 2025-11-09T00:00:00 |
      | operation_started | New op       | 2025-11-09T12:00:00 |
    When I retrieve captain logs for agent "ENDURANCE" since "2025-11-09T00:00:00"
    Then the logs command should succeed
    And I should see 2 log entries

  Scenario: Log entry fails with invalid entry type
    When I log a captain entry with:
      | field      | value                  |
      | agent      | ENDURANCE              |
      | entry_type | invalid_type           |
      | narrative  | This should fail       |
    Then the captain log command should fail
    And the error should mention "invalid choice"

  Scenario: Log entry fails with empty narrative
    When I log a captain entry with:
      | field      | value         |
      | agent      | ENDURANCE     |
      | entry_type | session_start |
      | narrative  |               |
    Then the captain log command should fail
    And the error should mention "narrative cannot be empty"

  Scenario: Log entry fails with invalid JSON in event_data
    When I log a captain entry with:
      | field      | value                     |
      | agent      | ENDURANCE                 |
      | entry_type | session_start             |
      | narrative  | Valid narrative           |
      | event_data | {this is invalid json}    |
    Then the captain log command should fail
    And the error should mention "Invalid JSON"

  Scenario: Logs command fails for nonexistent player
    When I retrieve captain logs for agent "NONEXISTENT"
    Then the logs command should fail
    And the error should mention "not found"
