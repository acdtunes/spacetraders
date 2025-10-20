Feature: Captain logging operations
  As a fleet operations coordinator
  I want to maintain narrative captain logs
  So that mission history is documented with strategic insights

  Background:
    Given a captain log writer for agent "TEST-AGENT"

  Scenario: Initialize captain log for new agent
    When I initialize a new captain log
    Then the captain log file should exist
    And the log should contain agent information
    And the log should have sections for executive summary and detailed entries

  Scenario: Start logging session with objective and narrative
    Given a captain log has been initialized
    When I start a session with objective "Mine asteroids in X1-TEST" and narrative "Deploying mining fleet"
    Then a new session should be created with ID
    And the session should be saved to disk
    And the log should contain the session start entry
    And the session start should include the narrative

  Scenario: Start session without narrative is skipped
    Given a captain log has been initialized
    When I start a session with objective "Test mission" without narrative
    Then a session ID should still be returned
    But no log entry should be written

  Scenario: Log operation started with narrative
    Given a captain log has been initialized
    And a session is active
    When I log an operation started event with narrative "Deploying SHIP-1 to asteroid B9"
    Then the log should contain the operation started entry
    And the entry should include the narrative
    And the operation should be tracked in the session

  Scenario: Log operation completed with narrative and insights
    Given a captain log has been initialized
    And a session is active
    When I log an operation completed with narrative, insights, and recommendations
    Then the log should contain the operation completed entry
    And the entry should include narrative, insights, and recommendations
    And tags should be present in the entry

  Scenario: Log operation completed without narrative is skipped
    Given a captain log has been initialized
    And a session is active
    When I log an operation completed without narrative
    Then no log entry should be written
    And a warning should be displayed

  Scenario: Scout operations are ignored in logging
    Given a captain log has been initialized
    And a session is active
    When I log a scouting operation start
    Then no log entry should be written
    And a filtered message should be displayed

  Scenario: Critical error logging with narrative
    Given a captain log has been initialized
    And a session is active
    When I log a critical error with error description and narrative
    Then the log should contain the critical error entry
    And the error should be tracked in the session

  Scenario: End session archives to JSON with metadata
    Given a captain log has been initialized
    And a session is active with operations
    When I end the session
    Then the session should be archived to JSON
    And the archive should include session metadata
    And the archive should include duration and net profit
    And the current session should be cleared

  Scenario: Concurrent writes use file locking
    Given a captain log has been initialized
    And multiple writers attempt to log simultaneously
    When concurrent log entries are written
    Then all entries should be written successfully
    And no entries should be corrupted or lost

  Scenario: File lock acquisition retry with exponential backoff
    Given a captain log has been initialized
    And the log file is locked by another process
    When I attempt to write a log entry
    Then the writer should retry with exponential backoff
    And eventually acquire the lock and write successfully
