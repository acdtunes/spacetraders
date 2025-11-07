Feature: Daemon Socket Buffer Handling

  As a fleet operator
  I want the daemon client to handle large JSON responses
  So that inspect commands work regardless of response size

  Scenario: DaemonClient handles responses larger than 64KB
    Given a mock daemon server returning 500KB of JSON data
    When I send a request via daemon client
    Then the response should be fully received
    And the JSON should be parsed successfully
    And no data should be truncated
