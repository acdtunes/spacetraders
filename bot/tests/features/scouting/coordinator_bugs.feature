Feature: Scout Coordinator Real-World Bug Fixes
  As a scout coordinator
  I want robust handling of edge cases and real-world scenarios
  So that scout operations are reliable in production

  Background:
    Given a scout coordinator in production environment
    And real-world market and ship data

  @xfail
  Scenario: Handle empty market list gracefully
    Given scout coordinator receives empty market list
    When attempting to generate scout tours
    Then coordinator should return empty tour assignments
    And no errors should be raised
    And system should log appropriate warning

  @xfail
  Scenario: Handle single market case
    Given only one market in the system
    And multiple scouts available
    When generating scout tours
    Then one scout should be assigned to the market
    And other scouts should remain unassigned
    And no optimization errors should occur

  @xfail
  Scenario: Handle more scouts than markets
    Given 3 markets in the system
    And 5 scouts available
    When assigning scouts to markets
    Then only 3 scouts should be assigned
    And 2 scouts should remain idle
    And each market should be visited by exactly one scout

  @xfail
  Scenario: Recover from malformed tour data
    Given corrupt or incomplete tour data in cache
    When scout coordinator loads tour assignments
    Then coordinator should detect malformed data
    And fallback to fresh tour planning
    And operations should continue without failure
