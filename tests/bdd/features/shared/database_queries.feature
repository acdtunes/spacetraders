Feature: Database-only queries should not require API client
  As a developer
  I want to query the database without needing API credentials
  So that read operations work independently

  Scenario: List ships from database without API token
    Given the SPACETRADERS_TOKEN environment variable is not set
    And there is a player in the database
    When I query for all ships for that player
    Then the query should succeed
    And no API client should be initialized
