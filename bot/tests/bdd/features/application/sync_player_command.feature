Feature: Sync Player Command
  As a bot operator
  I want to synchronize player data from the SpaceTraders API
  So that my local database reflects the current agent credits and metadata

  Scenario: Sync updates player credits from API
    Given a player exists with player_id 1 and agent_symbol "TEST-AGENT" and credits 0
    And the API returns agent "TEST-AGENT" with credits 175000
    When I sync player data for player 1
    Then the player should have credits 175000

  Scenario: Sync updates player headquarters from API
    Given a player exists with player_id 1 and agent_symbol "TEST-AGENT"
    And the API returns agent "TEST-AGENT" with headquarters "X1-GZ7-A1"
    When I sync player data for player 1
    Then the player headquarters should be "X1-GZ7-A1"

  Scenario: Sync updates both credits and headquarters
    Given a player exists with player_id 1 and agent_symbol "TEST-AGENT" and credits 0
    And the API returns agent "TEST-AGENT" with credits 250000 and headquarters "X1-TEST-A1"
    When I sync player data for player 1
    Then the player should have credits 250000
    And the player headquarters should be "X1-TEST-A1"

  Scenario: Sync updates existing metadata without replacing it
    Given a player exists with player_id 1 and agent_symbol "TEST-AGENT" with metadata key "custom_field" value "preserved"
    And the API returns agent "TEST-AGENT" with headquarters "X1-GZ7-A1"
    When I sync player data for player 1
    Then the player headquarters should be "X1-GZ7-A1"
    And the player metadata should contain "custom_field" with value "preserved"

  Scenario: Sync converts API agent data correctly
    Given a player exists with player_id 1 and agent_symbol "CHROMESAMURAI"
    And the API returns agent "CHROMESAMURAI" with:
      | field        | value        |
      | credits      | 175000       |
      | headquarters | X1-GZ7-A1    |
      | shipCount    | 2            |
      | accountId    | abc-123      |
    When I sync player data for player 1
    Then the player should have credits 175000
    And the player headquarters should be "X1-GZ7-A1"
    And the player metadata should contain "shipCount" with value 2
    And the player metadata should contain "accountId" with value "abc-123"

  Scenario: Sync returns updated player entity
    Given a player exists with player_id 1 and agent_symbol "TEST-AGENT" and credits 0
    And the API returns agent "TEST-AGENT" with credits 100000
    When I sync player data for player 1
    Then the sync should return a Player entity
    And the returned player should have credits 100000
