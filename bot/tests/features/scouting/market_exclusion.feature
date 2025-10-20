Feature: Scout Market Exclusion
  As a scout coordinator
  I want to exclude specific markets from scout tours
  So that scouts don't waste time visiting irrelevant markets

  Background:
    Given a scout coordinator with tour planning capability
    And a system with multiple markets

  @xfail
  Scenario: Exclude markets from scout tour planning
    Given a list of markets to exclude
    And scout tours are being planned
    When I generate scout tours with exclusions
    Then excluded markets should not appear in any tour
    And scouts should only visit non-excluded markets

  @xfail
  Scenario: J53 market exclusion bug fix
    Given market J53 should be excluded from tours
    And scout tour planning is active
    When I plan tours with J53 in exclusion list
    Then J53 should not appear in any planned tour
    And tour optimization should skip J53

  @xfail
  Scenario: Exclude markets cache consistency
    Given markets are excluded from scout tours
    And cached tour data exists
    When I query cached tours
    Then cached tours should respect current exclusion list
    And cache should not return tours with excluded markets
    And cache key should include exclusion list hash
